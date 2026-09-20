package com.localghost.app.net

import java.io.ByteArrayOutputStream
import java.net.HttpURLConnection
import java.net.URL
import java.net.URLDecoder
import java.net.URLEncoder
import java.util.Calendar
import java.util.concurrent.Callable
import java.util.concurrent.Executors
import java.util.concurrent.Future
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import org.json.JSONArray
import org.json.JSONObject

/**
 * The box never reaches the internet; the phone does, on the person's say-so. Before a question
 * goes to the box, the phone can look it up and hand the findings along, so the box answers with
 * the archive AND the outside world without ever opening a socket to it.
 *
 * No library, no API key, and nothing that needs an account. What runs, in order:
 *  1. PLAN , the question becomes one to three searches: the question itself with the chat filler
 *     cut off, its bare keywords when they differ, and the same again with the year when the
 *     question is about now ([plan]).
 *  2. TOOLS , some questions have a better answer than a search page. A weather question goes to
 *     Open-Meteo for the place named (or where the phone is); a currency question to the ECB's
 *     rates via Frankfurter; a "who is / what is" question to Wikipedia's summary. Each returns
 *     one [Hit] with a [Hit.kind] of its own, so the box can see it is a figure, not a web page.
 *  3. SEARCH , DuckDuckGo's HTML endpoint (built for browsers without scripts), and its "lite"
 *     endpoint when the first one answers with nothing or a bot check. Results from every
 *     planned query merge by URL, the ones several queries agree on first.
 *  4. READ , the top pages are fetched in parallel and cut to the passage that best matches the
 *     question by a small readability pass: the article or main element when there is one,
 *     boilerplate blocks dropped, paragraphs with more link than text dropped, and the page's own
 *     title, description and publish date kept ([Page]). A Wikipedia page is read through its
 *     summary API instead of scraped.
 * Everything is bounded , five results, three page fetches, single-digit seconds , returns nothing
 * rather than late, and is cached for ten minutes so a re-ask costs no traffic. What was sent is
 * shown in the chat, numbered, so the box's "[2]" points at a link the person can open.
 */
object WebSearch {
    /** One thing found. [kind]: page (a search result, read), summary (Wikipedia), weather, rate. */
    class Hit(
        val title: String,
        val url: String,
        val snippet: String,
        var excerpt: String = "",
        val kind: String = "page",
        val source: String = "duckduckgo",
        var published: String = "",
    ) {
        val site: String get() = runCatching { URL(url).host.removePrefix("www.") }.getOrDefault("")

        fun toJson(fetched: String): JSONObject = JSONObject()
            .put("title", title).put("url", url).put("snippet", snippet).put("excerpt", excerpt)
            .put("kind", kind).put("source", source).put("published", published).put("fetched", fetched)

        companion object {
            fun fromJson(o: JSONObject): Hit = Hit(o.optString("title"), o.optString("url"), o.optString("snippet"),
                o.optString("excerpt"), o.optString("kind", "page"), o.optString("source", "duckduckgo"), o.optString("published"))
        }
    }

    /** Where the phone is, for a weather question that names no place. */
    class Here(val lat: Double, val lon: Double)

    private const val UA = "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Mobile Safari/537.36"
    private const val MAX_HITS = 5
    private const val FETCH_TOP = 3
    private const val PAGE_CAP = 400 * 1024
    private const val EXCERPT_CHARS = 1400
    private const val CACHE_TTL_MS = 10 * 60 * 1000L
    private const val CACHE_MAX = 32

    // --- modes ---

    /** Modes: "off" never searches, "auto" searches when the question looks like it needs the
     *  outside world, "on" searches every time. */
    fun shouldSearch(mode: String, question: String): Boolean = when (mode) {
        "on" -> true
        "auto" -> looksFresh(question)
        else -> false
    }

    private val fresh = Regex(
        "\\b(today|tonight|tomorrow|yesterday|latest|news|price|prices|cost|costs|weather|forecast|score|scores|results?|" +
        "open now|opening hours|hours|near me|who is|who was|what is the|when is|when does|when did|how much|how many|" +
        "stock|shares|exchange rate|rate|election|release|released|version|update|current|currently|now|" +
        "20(2[4-9]|3[0-9])|schedule|timetable|flight|train|traffic|recipe|reviews?|compare|vs\\.?|best|convert|in (usd|eur|gbp|euros?|dollars|pounds))\\b",
        RegexOption.IGNORE_CASE)

    /** A question that mentions time, money, news, places or comparisons is one the archive alone
     *  cannot answer; everything else stays private by default. */
    fun looksFresh(q: String): Boolean = fresh.containsMatchIn(q) && (q.contains('?') || q.length > 24)

    // --- the plan ---

    private val filler = Regex(
        "^\\s*(hey|hi|hello|ok|okay|so|ghost|please|pls|can you|could you|would you|will you|tell me|show me|find|find out|look up|search|search for|google|i want to know|i wonder|do you know|any idea|quick question)[,:]?\\s+",
        RegexOption.IGNORE_CASE)
    private val trailingFiller = Regex("[\\s,]*(please|pls|thanks|thank you|for me)\\s*[?.!]*\\s*$", RegexOption.IGNORE_CASE)

    /** The question as a search: chat filler off both ends, quotes and stray punctuation out. */
    internal fun cleanQuery(question: String): String {
        var q = question.trim().replace(Regex("\\s+"), " ")
        var prev: String
        do { prev = q; q = filler.replace(q, "") } while (q != prev) // "hey, can you please tell me…"
        q = trailingFiller.replace(q, "")
        q = q.trim().trimEnd('?', '.', '!', ',').trim()
        q = q.replace(Regex("[\"“”‘’]"), "")
        return if (q.isEmpty()) question.trim() else q
    }

    /** One search in the plan; [always] runs regardless, the rest only when the searches before
     *  it came back thin. */
    internal class Query(val text: String, val always: Boolean)

    /** One to three searches for a question: the cleaned question (always); its keywords when
     *  they cut enough away to be a different search (only if the first search was thin); and the
     *  question with the year when it asks about now and names no year (always , so "latest
     *  android version" does not surface a 2019 answer). */
    internal fun plan(question: String, cal: Calendar = Calendar.getInstance()): List<Query> {
        val q1 = cleanQuery(question)
        val out = ArrayList<Query>(3)
        out.add(Query(q1, true))
        val kw = terms(q1)
        val q2 = kw.joinToString(" ")
        if (kw.size >= 2 && q2.length <= q1.length - 8) out.add(Query(q2, false))
        val year = cal.get(Calendar.YEAR).toString()
        if (looksFresh(question) && !question.contains(Regex("\\b20[2-3][0-9]\\b"))) out.add(Query("$q1 $year", true))
        return out
    }

    // --- cache ---

    private val cache = object : LinkedHashMap<String, Pair<Long, List<Hit>>>(CACHE_MAX, 0.75f, true) {
        override fun removeEldestEntry(eldest: MutableMap.MutableEntry<String, Pair<Long, List<Hit>>>): Boolean = size > CACHE_MAX
    }

    private fun cacheKey(q: String, here: Here?): String =
        q.lowercase().replace(Regex("[^\\p{L}\\p{N}]+"), " ").trim() + (if (here != null) "@%.1f,%.1f".format(here.lat, here.lon) else "")

    // --- the whole thing ---

    /** Search and read, bounded; empty on any failure. Call from a coroutine (IO inside). */
    suspend fun search(question: String, here: Here? = null): List<Hit> = withContext(Dispatchers.IO) {
        val key = cacheKey(question, here)
        synchronized(cache) { cache[key]?.let { (at, hits) -> if (System.currentTimeMillis() - at < CACHE_TTL_MS) return@withContext hits } }
        val out = run(question, here)
        if (out.isNotEmpty()) synchronized(cache) { cache[key] = System.currentTimeMillis() to out }
        out
    }

    private fun run(question: String, here: Here?): List<Hit> {
        val pool = Executors.newFixedThreadPool(FETCH_TOP + 2)
        try {
            // Tools and the first search leave together; the extra queries only when the first
            // came back thin. Every future is bounded on its own, so one slow host costs its own
            // seconds and nobody else's.
            val toolFutures: List<Future<Hit?>> = Tools.forQuestion(question, here).map { t -> pool.submit(Callable { runCatching { t.call() }.getOrNull() }) }
            val queries = plan(question)
            val pages = ArrayList<Hit>()
            val agree = HashMap<String, Int>()
            for (q in queries) {
                if (!q.always && pages.size >= 3) continue // enough already
                val found = try { results(q.text) } catch (e: Exception) {
                    android.util.Log.w("LocalGhost", "web search failed: ${e.message}"); emptyList()
                }
                for (h in found) {
                    val n = agree[h.url]
                    if (n == null) { pages.add(h); agree[h.url] = 1 } else agree[h.url] = n + 1
                }
            }
            // Several queries agreeing on a page moves it up; otherwise the search engine's order.
            val ordered = pages.withIndex().sortedWith(compareByDescending<IndexedValue<Hit>> { agree[it.value.url] ?: 1 }.thenBy { it.index }).map { it.value }.take(MAX_HITS)
            val termList = terms(cleanQuery(question))
            val reads = ordered.take(FETCH_TOP).map { h -> pool.submit(Callable { read(h, termList) }) }
            for (f in reads) runCatching { f.get(8, TimeUnit.SECONDS) }
            val tools = toolFutures.mapNotNull { f -> runCatching { f.get(6, TimeUnit.SECONDS) }.getOrNull() }
            return tools + ordered
        } finally {
            pool.shutdownNow()
        }
    }

    fun toJson(hits: List<Hit>): JSONArray {
        val fetched = java.text.SimpleDateFormat("yyyy-MM-dd HH:mm 'UTC'", java.util.Locale.US)
            .apply { timeZone = java.util.TimeZone.getTimeZone("UTC") }.format(java.util.Date())
        return JSONArray().apply { hits.forEach { put(it.toJson(fetched)) } }
    }

    fun fromJson(arr: JSONArray?): List<Hit> =
        if (arr == null) emptyList() else (0 until arr.length()).mapNotNull { i -> arr.optJSONObject(i)?.let { Hit.fromJson(it) } }

    // --- the search engines ---

    // Result anchors are found by class, whatever the attribute order: the HTML endpoint marks the
    // title link result__a and the snippet result__snippet; the lite endpoint is a table whose link
    // has class result-link and whose snippet sits in the next row's td.result-snippet.
    private val anchorTag = Regex("<a\\b([^>]*)>(.*?)</a\\s*>", setOf(RegexOption.DOT_MATCHES_ALL, RegexOption.IGNORE_CASE))
    private val resultSnippet = Regex("<a[^>]*class=\"result__snippet\"[^>]*>(.*?)</a>", RegexOption.DOT_MATCHES_ALL)
    private val liteSnippet = Regex("<td[^>]*class=['\"]result-snippet['\"][^>]*>(.*?)</td>", RegexOption.DOT_MATCHES_ALL)

    /** (href, title) of every anchor whose class carries [cls]. */
    internal fun anchorsWithClass(html: String, cls: String): List<Pair<String, String>> {
        val out = ArrayList<Pair<String, String>>()
        for (m in anchorTag.findAll(html)) {
            val attrs = m.groupValues[1]
            var href = ""; var klass = ""
            for (am in attr.findAll(attrs)) {
                val v = am.groupValues[2].ifEmpty { am.groupValues[3].ifEmpty { am.groupValues[4] } }
                when (am.groupValues[1].lowercase()) { "href" -> href = v; "class" -> klass = v }
            }
            if (klass.split(' ').any { it == cls }) out.add(decodeLink(href) to clean(m.groupValues[2]))
        }
        return out
    }

    /** DuckDuckGo, HTML first, lite as the fallback. Empty when both come back empty or with the
     *  bot check (DuckDuckGo answers a burst of requests with a challenge page instead of results;
     *  the lite endpoint is rate-limited separately). */
    private fun results(query: String): List<Hit> {
        val html = try { ddgHtml(query) } catch (e: Exception) { emptyList() }
        if (html.isNotEmpty()) return html
        return try { ddgLite(query) } catch (e: Exception) { emptyList() }
    }

    private fun ddgHtml(query: String): List<Hit> {
        val conn = (URL("https://html.duckduckgo.com/html/").openConnection() as HttpURLConnection).apply {
            requestMethod = "POST"; doOutput = true; instanceFollowRedirects = true
            connectTimeout = 6000; readTimeout = 6000
            setRequestProperty("User-Agent", UA)
            setRequestProperty("Content-Type", "application/x-www-form-urlencoded")
        }
        conn.outputStream.use { it.write(("q=" + URLEncoder.encode(query, "UTF-8") + "&kl=wt-wt").toByteArray()) }
        val html = try {
            if (conn.responseCode !in 200..299) return emptyList()
            readCapped(conn)
        } finally { conn.disconnect() }
        return parseDdgHtml(html)
    }

    internal fun parseDdgHtml(html: String): List<Hit> {
        if (isBotCheck(html)) return emptyList()
        val titles = anchorsWithClass(html, "result__a")
        val snippets = resultSnippet.findAll(html).map { clean(it.groupValues[1]) }.toList()
        return collect(titles, snippets)
    }

    private fun ddgLite(query: String): List<Hit> {
        val conn = (URL("https://lite.duckduckgo.com/lite/").openConnection() as HttpURLConnection).apply {
            requestMethod = "POST"; doOutput = true; instanceFollowRedirects = true
            connectTimeout = 6000; readTimeout = 6000
            setRequestProperty("User-Agent", UA)
            setRequestProperty("Content-Type", "application/x-www-form-urlencoded")
        }
        conn.outputStream.use { it.write(("q=" + URLEncoder.encode(query, "UTF-8") + "&kl=wt-wt").toByteArray()) }
        val html = try {
            if (conn.responseCode !in 200..299) return emptyList()
            readCapped(conn)
        } finally { conn.disconnect() }
        return parseDdgLite(html)
    }

    internal fun parseDdgLite(html: String): List<Hit> {
        if (isBotCheck(html)) return emptyList()
        val titles = anchorsWithClass(html, "result-link")
        val snippets = liteSnippet.findAll(html).map { clean(it.groupValues[1]) }.toList()
        return collect(titles, snippets)
    }

    internal fun isBotCheck(html: String): Boolean =
        html.contains("anomaly-modal") || html.contains("bots use DuckDuckGo too") || html.contains("challenge-form")

    private fun collect(titles: List<Pair<String, String>>, snippets: List<String>): List<Hit> {
        val out = ArrayList<Hit>()
        val seen = HashSet<String>()
        for ((i, t) in titles.withIndex()) {
            val (url, title) = t
            if (url.isEmpty() || title.isEmpty() || !url.startsWith("http") || !seen.add(url)) continue
            if (url.contains("duckduckgo.com")) continue // ads and internal links
            out.add(Hit(title, url, snippets.getOrNull(i) ?: ""))
            if (out.size == MAX_HITS) break
        }
        return out
    }

    /** DDG wraps every result in a redirect: //duckduckgo.com/l/?uddg=<encoded url>&rut=... */
    internal fun decodeLink(href: String): String {
        val h = href.replace("&amp;", "&")
        val i = h.indexOf("uddg=")
        if (i < 0) return if (h.startsWith("//")) "https:$h" else h
        val end = h.indexOf('&', i).let { if (it < 0) h.length else it }
        return runCatching { URLDecoder.decode(h.substring(i + 5, end), "UTF-8") }.getOrDefault("")
    }

    // --- reading a page ---

    /** A Wikipedia result is read through the summary API, which hands back the lead paragraph
     *  clean; anything else is fetched and cut down. */
    private fun read(h: Hit, terms: List<String>) {
        val wiki = Regex("^https?://([a-z]{2,3})\\.(?:m\\.)?wikipedia\\.org/wiki/([^#?]+)").find(h.url)
        if (wiki != null) {
            val s = runCatching { Tools.wikipediaSummary(wiki.groupValues[2], wiki.groupValues[1]) }.getOrNull()
            if (s != null) { h.excerpt = s.excerpt; h.published = s.published; return }
        }
        val page = fetchPage(h.url) ?: return
        h.excerpt = page.excerptFor(terms)
        h.published = page.published
    }

    /** What a page says about itself and what it says. */
    internal class Page(val title: String, val description: String, val published: String, val paragraphs: List<String>) {
        /** The window of ~EXCERPT_CHARS around the paragraph with the most question terms, with the
         *  page's own description in front when the window does not already carry it. */
        fun excerptFor(terms: List<String>): String {
            val body = bestWindow(paragraphs, terms)
            if (description.isEmpty() || body.contains(description.take(40))) return body
            return (description + " — " + body).take(EXCERPT_CHARS)
        }
    }

    private fun fetchPage(url: String): Page? {
        val conn = try {
            (URL(url).openConnection() as HttpURLConnection).apply {
                requestMethod = "GET"; instanceFollowRedirects = true
                connectTimeout = 5000; readTimeout = 5000
                setRequestProperty("User-Agent", UA)
                setRequestProperty("Accept", "text/html,application/xhtml+xml")
            }
        } catch (e: Exception) { return null }
        try {
            if (conn.responseCode !in 200..299) return null
            val type = conn.contentType ?: ""
            if (!type.contains("html") && !type.contains("text")) return null
            return extract(readCapped(conn))
        } catch (e: Exception) {
            return null
        } finally {
            conn.disconnect()
        }
    }

    private fun readCapped(conn: HttpURLConnection): String {
        val out = ByteArrayOutputStream()
        conn.inputStream.use { ins ->
            val buf = ByteArray(16 * 1024)
            while (out.size() < PAGE_CAP) {
                val n = ins.read(buf); if (n < 0) break
                out.write(buf, 0, minOf(n, PAGE_CAP - out.size()))
            }
        }
        val bytes = out.toByteArray()
        val cs = Regex("charset=([\\w-]+)", RegexOption.IGNORE_CASE).find(conn.contentType ?: "")?.groupValues?.get(1)
            ?: Regex("<meta[^>]+charset=[\"']?([\\w-]+)", RegexOption.IGNORE_CASE).find(String(bytes, 0, minOf(bytes.size, 4096), Charsets.ISO_8859_1))?.groupValues?.get(1)
        return runCatching { String(bytes, charset(cs ?: "UTF-8")) }.getOrElse { String(bytes, Charsets.UTF_8) }
    }

    private val dropBlocks = Regex("<(script|style|noscript|svg|iframe|nav|header|footer|aside|form|template|figure)\\b[^>]*>.*?</\\1\\s*>", setOf(RegexOption.DOT_MATCHES_ALL, RegexOption.IGNORE_CASE))
    private val comments = Regex("<!--.*?-->", RegexOption.DOT_MATCHES_ALL)
    private val blockEnd = Regex("</(p|div|li|h[1-6]|tr|td|th|section|article|blockquote|pre|dd|dt)\\s*>|<br\\s*/?>|<hr\\s*/?>", RegexOption.IGNORE_CASE)
    private val anyTag = Regex("<[^>]+>")
    private val anchor = Regex("<a\\b[^>]*>(.*?)</a\\s*>", setOf(RegexOption.DOT_MATCHES_ALL, RegexOption.IGNORE_CASE))
    private val titleTag = Regex("<title[^>]*>(.*?)</title>", setOf(RegexOption.DOT_MATCHES_ALL, RegexOption.IGNORE_CASE))
    private val metaTag = Regex("<meta\\b[^>]*>", RegexOption.IGNORE_CASE)
    private val attr = Regex("([a-zA-Z:_-]+)\\s*=\\s*(?:\"([^\"]*)\"|'([^']*)'|([^\\s>]+))")
    private val timeTag = Regex("<time\\b[^>]*datetime=[\"']([^\"']+)[\"']", RegexOption.IGNORE_CASE)
    private val ldDate = Regex("\"datePublished\"\\s*:\\s*\"([^\"]+)\"")
    private val mainBlock = Regex("<(article|main)\\b[^>]*>(.*?)</\\1\\s*>", setOf(RegexOption.DOT_MATCHES_ALL, RegexOption.IGNORE_CASE))

    /**
     * The readability pass. Title and description from the head; the publish date from the
     * article meta, JSON-LD or the first <time>; then the body , the article/main element when
     * the page has one, else everything , with the boilerplate elements removed, split into
     * paragraphs at block ends, each paragraph kept only when it is long enough to say something
     * and is not mostly links (a menu, a tag cloud, a "related" list).
     */
    internal fun extract(html: String): Page {
        val noComments = comments.replace(html, "")
        val head = noComments.take(64 * 1024)
        val title = titleTag.find(head)?.let { clean(it.groupValues[1]) } ?: ""
        var description = ""
        var published = ""
        for (m in metaTag.findAll(head)) {
            val a = HashMap<String, String>()
            for (am in attr.findAll(m.value)) a[am.groupValues[1].lowercase()] = am.groupValues[2].ifEmpty { am.groupValues[3].ifEmpty { am.groupValues[4] } }
            val key = (a["property"] ?: a["name"] ?: a["itemprop"] ?: "").lowercase()
            val content = a["content"] ?: continue
            when (key) {
                "description", "og:description", "twitter:description" -> if (description.isEmpty() || key == "og:description") description = clean(content)
                "article:published_time", "datepublished", "date", "dc.date", "dc.date.issued", "pubdate", "publish-date", "og:updated_time", "article:modified_time" ->
                    if (published.isEmpty() || key == "article:published_time") published = content.take(32)
            }
        }
        if (published.isEmpty()) published = ldDate.find(noComments)?.groupValues?.get(1)?.take(32) ?: ""
        if (published.isEmpty()) published = timeTag.find(noComments)?.groupValues?.get(1)?.take(32) ?: ""
        val bodyStart = noComments.indexOf("<body", ignoreCase = true).let { if (it < 0) 0 else it }
        var body = noComments.substring(bodyStart)
        mainBlock.find(body)?.let { m -> if (m.groupValues[2].length > 400) body = m.groupValues[2] }
        body = dropBlocks.replace(body, " ")
        val paragraphs = ArrayList<String>()
        for (raw in blockEnd.split(body)) {
            val linkChars = anchor.findAll(raw).sumOf { clean(it.groupValues[1]).length }
            val text = clean(anyTag.replace(raw, " ")).replace('\n', ' ').replace(Regex(" {2,}"), " ").trim()
            if (text.length < 40) continue
            if (linkChars * 2 > text.length) continue // more link than prose: a menu, not a paragraph
            paragraphs.add(text)
        }
        return Page(title, description, published, paragraphs)
    }

    /** Visible text, one line per block, entities decoded, whitespace collapsed (the plain path,
     *  kept for callers that want the whole page as lines). */
    internal fun textOf(html: String): String = extract(html).paragraphs.joinToString("\n")

    /** The window of ~EXCERPT_CHARS around the paragraph with the most question terms. */
    internal fun bestWindow(lines: List<String>, terms: List<String>): String {
        if (lines.isEmpty()) return ""
        var best = 0; var bestScore = -1
        for ((i, l) in lines.withIndex()) {
            val low = l.lowercase()
            val score = terms.count { low.contains(it) } * 10 + minOf(l.length, 300) / 100
            if (score > bestScore) { bestScore = score; best = i }
        }
        val sb = StringBuilder()
        var i = best
        while (i < lines.size && sb.length < EXCERPT_CHARS) { sb.append(lines[i]).append(' '); i++ }
        var j = best - 1
        while (j >= 0 && sb.length < EXCERPT_CHARS) { sb.insert(0, lines[j] + " "); j-- }
        return sb.toString().trim().take(EXCERPT_CHARS)
    }

    internal fun bestWindow(text: String, terms: List<String>): String = bestWindow(text.lines(), terms)

    private val stop = setOf("what", "when", "where", "which", "that", "this", "with", "from", "about", "does", "have", "will", "there", "their", "they", "them", "than", "then", "your", "into", "some", "much", "many", "been", "were", "would", "could", "should")

    internal fun terms(q: String): List<String> =
        q.lowercase().split(Regex("[^\\p{L}\\p{N}]+")).filter { it.length > 3 && it !in stop }.distinct().take(8)

    private val entity = Regex("&(#x?[0-9a-fA-F]+|[a-zA-Z]+);")
    private val named: Map<String, String> = run {
        val m = hashMapOf("amp" to "&", "lt" to "<", "gt" to ">", "quot" to "\"", "apos" to "'", "nbsp" to " ",
            "rsquo" to "’", "lsquo" to "‘", "ldquo" to "“", "rdquo" to "”", "mdash" to "—", "ndash" to "–", "hellip" to "…",
            "copy" to "©", "reg" to "®", "trade" to "™", "euro" to "€", "pound" to "£", "yen" to "¥", "deg" to "°", "middot" to "·", "bull" to "•", "laquo" to "«", "raquo" to "»", "szlig" to "ß", "ntilde" to "ñ", "Ntilde" to "Ñ", "ccedil" to "ç", "Ccedil" to "Ç", "oslash" to "ø", "aring" to "å", "aelig" to "æ")
        // Latin-1 accented vowels by the HTML naming rule: <vowel><acute|grave|circ|uml|tilde>.
        val forms = mapOf("acute" to "\u0301", "grave" to "\u0300", "circ" to "\u0302", "uml" to "\u0308", "tilde" to "\u0303")
        for (v in listOf("a", "e", "i", "o", "u", "y", "A", "E", "I", "O", "U", "Y")) for ((f, mark) in forms) {
            m["$v$f"] = java.text.Normalizer.normalize(v + mark, java.text.Normalizer.Form.NFC)
        }
        m
    }

    internal fun clean(s: String): String {
        val noTags = anyTag.replace(s, "")
        val decoded = entity.replace(noTags) { m ->
            val e = m.groupValues[1]
            when {
                e.startsWith("#x") || e.startsWith("#X") -> e.substring(2).toIntOrNull(16)?.let { String(Character.toChars(it)) } ?: ""
                e.startsWith("#") -> e.substring(1).toIntOrNull()?.let { String(Character.toChars(it)) } ?: ""
                else -> named[e] ?: m.value // an entity we do not know stays as written
            }
        }
        return decoded.replace(Regex("[ \\t\\r\\u00a0]+"), " ").replace(Regex(" *\\n *"), "\n").trim()
    }

    // --- one JSON GET, bounded ---

    internal fun getJson(url: String, timeoutMs: Int = 5000): JSONObject? {
        val conn = (URL(url).openConnection() as HttpURLConnection).apply {
            requestMethod = "GET"; instanceFollowRedirects = true
            connectTimeout = timeoutMs; readTimeout = timeoutMs
            setRequestProperty("User-Agent", UA)
            setRequestProperty("Accept", "application/json")
        }
        try {
            if (conn.responseCode !in 200..299) return null
            return JSONObject(readCapped(conn))
        } finally { conn.disconnect() }
    }

    /**
     * The tools: questions with a better source than a search page. Each is a [Callable] that
     * returns one [Hit] or null; [forQuestion] decides which apply. Every endpoint here is public,
     * keyless and rate-limited only by decency; each is one small GET.
     */
    object Tools {
        private val weatherQ = Regex("\\b(weather|forecast|rain|raining|temperature|how (hot|cold|warm) is it|umbrella|sunny|snow|wind|windy|humid)\\b", RegexOption.IGNORE_CASE)
        private val placeAfter = Regex("\\b(?:in|at|for|around|near)\\s+([\\p{L}][\\p{L} .'-]{1,40}?)(?:\\s+(?:today|tomorrow|tonight|this|next|on|at|now|right)\\b|[?.!,]|$)", RegexOption.IGNORE_CASE)
        private val codes = mapOf(
            "usd" to "USD", "dollar" to "USD", "dollars" to "USD", "$" to "USD", "us$" to "USD",
            "eur" to "EUR", "euro" to "EUR", "euros" to "EUR", "€" to "EUR",
            "gbp" to "GBP", "pound" to "GBP", "pounds" to "GBP", "£" to "GBP", "quid" to "GBP", "sterling" to "GBP",
            "jpy" to "JPY", "yen" to "JPY", "¥" to "JPY", "chf" to "CHF", "franc" to "CHF", "francs" to "CHF",
            "ron" to "RON", "lei" to "RON", "leu" to "RON", "try" to "TRY", "lira" to "TRY", "pln" to "PLN", "zloty" to "PLN", "złoty" to "PLN",
            "czk" to "CZK", "koruna" to "CZK", "huf" to "HUF", "forint" to "HUF", "sek" to "SEK", "nok" to "NOK", "dkk" to "DKK", "krona" to "SEK", "kroner" to "NOK",
            "cad" to "CAD", "aud" to "AUD", "nzd" to "NZD", "cny" to "CNY", "yuan" to "CNY", "rmb" to "CNY", "inr" to "INR", "rupee" to "INR", "rupees" to "INR",
            "krw" to "KRW", "won" to "KRW", "thb" to "THB", "baht" to "THB", "idr" to "IDR", "rupiah" to "IDR", "mxn" to "MXN", "peso" to "MXN", "pesos" to "MXN",
            "brl" to "BRL", "real" to "BRL", "reais" to "BRL", "zar" to "ZAR", "rand" to "ZAR", "sgd" to "SGD", "hkd" to "HKD", "ils" to "ILS", "shekel" to "ILS", "shekels" to "ILS",
        )
        private val rateQ = Regex(
            "(?:(\\d+(?:[.,]\\d+)?)\\s*)?(" + codes.keys.joinToString("|") { Regex.escape(it) } + ")\\s*(?:to|in|into|->|→|=|per|vs)\\s*(" + codes.keys.joinToString("|") { Regex.escape(it) } + ")\\b",
            RegexOption.IGNORE_CASE)
        private val whoWhat = Regex("^\\s*(?:who|what)\\s+(?:is|was|are|were)\\s+(?:the\\s+|a\\s+|an\\s+)?([\\p{L}\\p{N}][\\p{L}\\p{N} .'-]{1,60}?)\\s*\\??\\s*$", RegexOption.IGNORE_CASE)

        private val timeWords = Regex("\\b(today|tomorrow|tonight|now|this|next|the|week|weekend|morning|afternoon|evening|night|hour|hours|day|days|moment)\\b", RegexOption.IGNORE_CASE)

        /** The place a weather question names, or null when it names none ("weather for tomorrow"
         *  names a day, not a place). */
        internal fun placeOf(q: String): String? {
            for (m in placeAfter.findAll(q)) {
                val raw = m.groupValues[1].trim()
                val place = timeWords.replace(raw, " ").replace(Regex("\\s+"), " ").trim().trimEnd('.', ',')
                if (place.length in 2..40 && !weatherQ.containsMatchIn(place)) return place
            }
            return null
        }

        fun forQuestion(question: String, here: Here?): List<Callable<Hit?>> {
            val q = WebSearch.cleanQuery(question)
            val out = ArrayList<Callable<Hit?>>(3)
            if (weatherQ.containsMatchIn(q)) {
                val place = placeOf(q)
                if (place != null) out.add(Callable { weatherByName(place) })
                else if (here != null) out.add(Callable { weather(here.lat, here.lon, "where you are") })
            }
            rateQ.find(q)?.let { m ->
                val amount = m.groupValues[1].replace(',', '.').toDoubleOrNull() ?: 1.0
                val from = codes[m.groupValues[2].lowercase()]; val to = codes[m.groupValues[3].lowercase()]
                if (from != null && to != null && from != to) out.add(Callable { rate(amount, from, to) })
            }
            whoWhat.find(q)?.let { m ->
                val subject = m.groupValues[1].trim()
                if (subject.split(' ').size <= 5 && !weatherQ.containsMatchIn(subject)) out.add(Callable { wikipediaHit(subject) })
            }
            return out
        }

        // Open-Meteo: keyless, no account, a JSON forecast by coordinates, and a geocoder by name.
        fun weatherByName(place: String): Hit? {
            val g = getJson("https://geocoding-api.open-meteo.com/v1/search?name=" + URLEncoder.encode(place, "UTF-8") + "&count=1&language=en&format=json") ?: return null
            val r = g.optJSONArray("results")?.optJSONObject(0) ?: return null
            val name = listOfNotNull(r.optString("name").ifEmpty { null }, r.optString("admin1").ifEmpty { null }, r.optString("country").ifEmpty { null }).distinct().joinToString(", ")
            return weather(r.optDouble("latitude"), r.optDouble("longitude"), name)
        }

        fun weather(lat: Double, lon: Double, label: String): Hit? {
            val url = "https://api.open-meteo.com/v1/forecast?latitude=%.4f&longitude=%.4f".format(java.util.Locale.US, lat, lon) +
                "&current=temperature_2m,apparent_temperature,relative_humidity_2m,precipitation,weather_code,wind_speed_10m" +
                "&daily=weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max,precipitation_sum,sunrise,sunset" +
                "&forecast_days=4&timezone=auto"
            val j = getJson(url) ?: return null
            return Hit("Weather · $label", url, "Open-Meteo forecast", formatWeather(j, label), kind = "weather", source = "open-meteo")
        }

        /** The forecast as one plain paragraph the model can quote: now, then four days. */
        internal fun formatWeather(j: JSONObject, label: String): String {
            val sb = StringBuilder()
            val cur = j.optJSONObject("current")
            val unit = j.optJSONObject("current_units")?.optString("temperature_2m", "°C") ?: "°C"
            if (cur != null) {
                sb.append("Now in ").append(label).append(": ").append(code(cur.optInt("weather_code", -1)))
                    .append(", ").append(fmt(cur.optDouble("temperature_2m"))).append(unit)
                if (cur.has("apparent_temperature")) sb.append(" (feels ").append(fmt(cur.optDouble("apparent_temperature"))).append(unit).append(")")
                if (cur.has("relative_humidity_2m")) sb.append(", humidity ").append(cur.optInt("relative_humidity_2m")).append("%")
                if (cur.has("wind_speed_10m")) sb.append(", wind ").append(fmt(cur.optDouble("wind_speed_10m"))).append(" km/h")
                sb.append(". ")
            }
            val d = j.optJSONObject("daily")
            val days = d?.optJSONArray("time")
            if (d != null && days != null) {
                val names = arrayOf("Today", "Tomorrow")
                for (i in 0 until days.length()) {
                    val day = if (i < names.size) names[i] else dayName(days.optString(i))
                    sb.append(day).append(": ").append(code(d.optJSONArray("weather_code")?.optInt(i, -1) ?: -1))
                    sb.append(", ").append(fmt(d.optJSONArray("temperature_2m_min")?.optDouble(i) ?: Double.NaN)).append("–")
                        .append(fmt(d.optJSONArray("temperature_2m_max")?.optDouble(i) ?: Double.NaN)).append(unit)
                    d.optJSONArray("precipitation_probability_max")?.let { p -> if (!p.isNull(i)) sb.append(", rain ").append(p.optInt(i)).append("%") }
                    d.optJSONArray("precipitation_sum")?.let { p -> val mm = p.optDouble(i); if (!mm.isNaN() && mm > 0) sb.append(" (").append(fmt(mm)).append(" mm)") }
                    if (i == 0) {
                        val rise = d.optJSONArray("sunrise")?.optString(i)?.substringAfter('T', "") ?: ""
                        val set = d.optJSONArray("sunset")?.optString(i)?.substringAfter('T', "") ?: ""
                        if (rise.isNotEmpty() && set.isNotEmpty()) sb.append(", sun ").append(rise).append("–").append(set)
                    }
                    sb.append(". ")
                }
            }
            j.optString("timezone").takeIf { it.isNotEmpty() }?.let { sb.append("Local time zone ").append(it).append('.') }
            return sb.toString().trim()
        }

        private fun fmt(v: Double): String = if (v.isNaN()) "?" else if (v == Math.rint(v)) v.toInt().toString() else "%.1f".format(java.util.Locale.US, v)
        private fun dayName(iso: String): String = runCatching {
            val p = iso.split('-'); val c = Calendar.getInstance(); c.set(p[0].toInt(), p[1].toInt() - 1, p[2].toInt())
            arrayOf("", "Sunday", "Monday", "Tuesday", "Wednesday", "Thursday", "Friday", "Saturday")[c.get(Calendar.DAY_OF_WEEK)]
        }.getOrDefault(iso)

        /** WMO weather codes, the ones Open-Meteo uses. */
        internal fun code(c: Int): String = when (c) {
            0 -> "clear"; 1 -> "mostly clear"; 2 -> "partly cloudy"; 3 -> "overcast"
            45, 48 -> "fog"; 51, 53, 55 -> "drizzle"; 56, 57 -> "freezing drizzle"
            61 -> "light rain"; 63 -> "rain"; 65 -> "heavy rain"; 66, 67 -> "freezing rain"
            71 -> "light snow"; 73 -> "snow"; 75 -> "heavy snow"; 77 -> "snow grains"
            80 -> "light showers"; 81 -> "showers"; 82 -> "violent showers"; 85, 86 -> "snow showers"
            95 -> "thunderstorm"; 96, 99 -> "thunderstorm with hail"
            else -> "unknown"
        }

        // Frankfurter: the European Central Bank's reference rates, keyless, one GET.
        fun rate(amount: Double, from: String, to: String): Hit? {
            val url = "https://api.frankfurter.app/latest?amount=" + fmt(amount) + "&from=" + from + "&to=" + to
            val j = getJson(url) ?: return null
            return formatRate(j, amount, from, to)?.let { Hit("$from → $to", url, "ECB reference rate via Frankfurter", it, kind = "rate", source = "frankfurter", published = j.optString("date")) }
        }

        internal fun formatRate(j: JSONObject, amount: Double, from: String, to: String): String? {
            val v = j.optJSONObject("rates")?.optDouble(to) ?: return null
            if (v.isNaN()) return null
            val one = if (amount != 0.0) v / amount else v
            return "${fmt(amount)} $from = ${"%.2f".format(java.util.Locale.US, v)} $to (1 $from = ${"%.4f".format(java.util.Locale.US, one)} $to), ECB reference rate dated ${j.optString("date")}."
        }

        // Wikipedia's REST summary: the lead paragraph, clean, with a timestamp.
        class Summary(val title: String, val url: String, val excerpt: String, val published: String)

        fun wikipediaSummary(titlePath: String, lang: String = "en"): Summary? {
            val t = URLEncoder.encode(URLDecoder.decode(titlePath, "UTF-8").replace(' ', '_'), "UTF-8")
            val j = getJson("https://$lang.wikipedia.org/api/rest_v1/page/summary/$t") ?: return null
            return parseSummary(j)
        }

        internal fun parseSummary(j: JSONObject): Summary? {
            if (j.optString("type") == "disambiguation") return null
            val extract = j.optString("extract").trim()
            if (extract.isEmpty()) return null
            val desc = j.optString("description")
            val url = j.optJSONObject("content_urls")?.optJSONObject("desktop")?.optString("page") ?: ""
            val text = (if (desc.isNotEmpty()) "$desc. " else "") + extract
            return Summary(j.optString("title"), url, text.take(1500), j.optString("timestamp").take(10))
        }

        fun wikipediaHit(subject: String): Hit? {
            val s = wikipediaSummary(subject) ?: return null
            return Hit("Wikipedia · ${s.title}", s.url, "Wikipedia summary", s.excerpt, kind = "summary", source = "wikipedia", published = s.published)
        }
    }
}
