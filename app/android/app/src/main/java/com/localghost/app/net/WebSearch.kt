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
 *  2. TOOLS , some questions have a better answer than a search page: a "who is / what is"
 *     question goes to Wikipedia's summary (on a box without its own copy of Wikipedia), one
 *     [Hit] with a [Hit.kind] of its own, so the box can see it is a summary, not a web page. The
 *     weather and the exchange rates are not tools any more: the box pulls the forecast of the
 *     world's larger places once a day and keeps the ECB's table itself, and answers from those, so
 *     the phone never tells a weather service where it is and asks no rate service at all (its
 *     plan says the box has them, and [BoxKnows] says so when the plan is late).
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
    /** One thing found. [kind]: page (a search result, read), summary (Wikipedia), rate. */
    class Hit(
        val title: String,
        val url: String,
        val snippet: String,
        var excerpt: String = "",
        val kind: String = "page",
        val source: String = "duckduckgo",
        var published: String = "",
        /** The page's paragraphs as read (the ones most worth the box's time, in page order), so
         *  the box can pick the passages that answer rather than the window that matches words. */
        var paragraphs: List<String> = emptyList(),
        /** Beside a phone model's notes (kind "note"): the page's own best paragraph, verbatim. */
        var quote: String = "",
    ) {
        val site: String get() = runCatching { URL(url).host.removePrefix("www.") }.getOrDefault("")

        fun toJson(fetched: String): JSONObject = JSONObject()
            .put("title", title).put("url", url).put("snippet", snippet).put("excerpt", excerpt)
            .put("kind", kind).put("source", source).put("published", published).put("fetched", fetched)
            .apply { if (paragraphs.isNotEmpty()) put("paragraphs", JSONArray().also { a -> paragraphs.forEach { a.put(it) } }) }
            .apply { if (quote.isNotEmpty()) put("quote", quote) }

        companion object {
            fun fromJson(o: JSONObject): Hit = Hit(o.optString("title"), o.optString("url"), o.optString("snippet"),
                o.optString("excerpt"), o.optString("kind", "page"), o.optString("source", "duckduckgo"), o.optString("published"),
                o.optJSONArray("paragraphs")?.let { a -> (0 until a.length()).map { a.optString(it) } } ?: emptyList(),
                o.optString("quote"))
        }
    }

    /** What the box's model said the question needs from the web (/v1/chat/plan): whether to
     *  search at all, the fact in one sentence, its shape, whether it goes stale, and the searches
     *  that would find it, most specific first. */
    class Plan(val search: Boolean, val need: String, val shape: String, val fresh: Boolean, val queries: List<String>, val box: BoxSpeed? = null,
               /** What the box keeps that answers this (its prices, the rank list, the ECB, the day's
                *  news); "" when the web may help. Final in every web mode: the box's number is fresher. */
               val boxHas: String = "",
               /** Why the box planned no search although one would have helped: every search it
                *  would take carried the person's own details. "" otherwise. */
               val withheld: String = "") {
        /** The searches run in the first round: the first two. */
        val first: List<String> get() = queries.take(2)
        /** The rest, kept for a second round the box may ask for when the first read thin. */
        val spare: List<String> get() = queries.drop(2)
    }

    /** How fast the box reads, as it said in its plan answer: where its model runs and its measured
     *  prompt tokens per second. Null when the box said nothing (old box, no answer in time). */
    class BoxSpeed(val known: Boolean, val onGPU: Boolean, val promptTps: Double, val genTps: Double)

    private const val UA = "Mozilla/5.0 (Linux; Android 14) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/124.0 Mobile Safari/537.36"
    private const val MAX_HITS = 5
    private const val FETCH_TOP = 3
    private const val PAGE_CAP = 400 * 1024
    private const val EXCERPT_CHARS = 1400
    private const val PARAS_PER_PAGE = 12
    private const val PARAS_CHARS = 6000
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

    /**
     * Whether the box's model is ASKED what this question needs from the web. In auto mode that
     * used to be [looksFresh] too: one list of words (today, price, latest …) stood between every
     * question and the planner, so "who built hagia sophia" or "is tungsten magnetic" was never
     * offered to it and the chat answered from what the model remembered. Now the planner is
     * asked for every question that is not small talk and not plainly about the person's own
     * life; the planner says search or not, and the searches it writes are held to a rule on the
     * box and again on the phone ([QueryGuard]). Without the box's plan the old rule stands.
     */
    fun shouldAskPlan(mode: String, question: String): Boolean = when (mode) {
        "on" -> true
        "auto" -> !looksChatter(question) && !(FollowUp.looksPersonal(question) && !looksFresh(question))
        else -> false
    }

    private val chatter = Regex(
        "^(hi|hello|hey|yo|thanks|thank you|thx|ty|ok|okay|cool|nice|great|good (morning|evening|night|afternoon)|bye|goodbye|" +
        "lol|haha|yes|no|yep|nope|sure|please|sorry|never mind|nevermind|got it|i see)[\\s!.,]*$", RegexOption.IGNORE_CASE)

    /** Small talk: a greeting, a thank-you, a word of agreement. Nothing to look up. */
    fun looksChatter(q: String): Boolean { val t = q.trim(); return t.length < 3 || chatter.matches(t) }

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

    private fun cacheKey(q: String): String = q.lowercase().replace(Regex("[^\\p{L}\\p{N}]+"), " ").trim()

    // --- the whole thing ---

    /** Search and read, bounded; empty on any failure. Call from a coroutine (IO inside). */
    /** Which engine, and the key when it needs one. */
    class Engine(val name: String = "duckduckgo", val key: String = "") {
        val brave: Boolean get() = name == "brave" && key.isNotBlank()
    }

    /** Search for a question. With [queries] (the box's plan, or the searches it asked for in a
     *  second round) those run instead of the phone's own plan , the first always, the rest when
     *  the first came back thin, or all of them when [runAll]. [need] steers which paragraphs of a
     *  page are worth sending: the box's one sentence beats the question's words. */
    suspend fun search(question: String, engine: Engine = Engine(), queries: List<String>? = null,
                       need: String = "", runAll: Boolean = false, subject: String = ""): List<Hit> = withContext(Dispatchers.IO) {
        val key = cacheKey(question) + (if (engine.brave) "#b" else "") + (queries?.joinToString("|", prefix = "#") ?: "") + (if (runAll) "#all" else "") +
            (if (subject.isNotBlank()) "#s$subject" else "")
        synchronized(cache) { cache[key]?.let { (at, hits) -> if (System.currentTimeMillis() - at < CACHE_TTL_MS) return@withContext hits } }
        val out = run(question, engine, queries, need, runAll, subject)
        if (out.isNotEmpty()) synchronized(cache) { cache[key] = System.currentTimeMillis() to out }
        out
    }

    /** Two rounds' findings as one list: by URL, the first round's order, the new ones after. */
    fun merge(first: List<Hit>, second: List<Hit>): List<Hit> {
        val seen = HashSet<String>()
        val out = ArrayList<Hit>()
        for (h in first + second) if (seen.add(h.url.ifEmpty { h.title })) out.add(h)
        return out
    }

    private fun run(question: String, engine: Engine = Engine(), planned: List<String>? = null, need: String = "", runAll: Boolean = false, subject: String = ""): List<Hit> {
        val pool = Executors.newFixedThreadPool(FETCH_TOP + 2)
        try {
            // Tools and the first search leave together; the extra queries only when the first
            // came back thin. Every future is bounded on its own, so one slow host costs its own
            // seconds and nobody else's.
            // Wikipedia's summary by the question's own shape ("what is X") or, when the question
            // has no such shape, by the subject the box's plan named: a question asked loosely
            // ("that greek island with the venetian fortress") still gets the encyclopedia's
            // paragraph beside the search results
            val toolList = Tools.forQuestion(question).ifEmpty { Tools.forSubject(subject) }
            val toolFutures: List<Future<Hit?>> = toolList.map { t -> pool.submit(Callable { runCatching { t.call() }.getOrNull() }) }
            val queries = if (planned != null && planned.isNotEmpty()) planned.mapIndexed { i, q -> Query(q, runAll || i == 0) } else plan(question)
            val pages = ArrayList<Hit>()
            val agree = HashMap<String, Int>()
            for (q in queries) {
                if (!q.always && pages.size >= 3) continue // enough already
                val found = try { results(q.text, engine) } catch (e: Exception) {
                    android.util.Log.w("LocalGhost", "web search failed: ${e.message}"); emptyList()
                }
                for (h in found) {
                    val n = agree[h.url]
                    if (n == null) { pages.add(h); agree[h.url] = 1 } else agree[h.url] = n + 1
                }
            }
            // Several queries agreeing on a page moves it up; otherwise the search engine's order.
            val ordered = pages.withIndex().sortedWith(compareByDescending<IndexedValue<Hit>> { agree[it.value.url] ?: 1 }.thenBy { it.index }).map { it.value }.take(MAX_HITS)
            val termList = terms(cleanQuery(question)) + (if (need.isNotBlank()) terms(need) else emptyList())
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
    // Read with the same one-pass cuts as a page (elementsNamed, attrsOf, below): the results
    // page is DuckDuckGo's, but the titles and snippets on it are strangers' words.
    private const val RESULT_ELEMENTS = 2000

    /** (href, title) of every anchor whose class carries [cls]. */
    internal fun anchorsWithClass(html: String, cls: String): List<Pair<String, String>> =
        elementsNamed(html, "a", RESULT_ELEMENTS).mapNotNull { (tag, inside) ->
            val a = attrsOf(tag)
            if ((a["class"] ?: "").split(' ').any { it == cls }) decodeLink(a["href"] ?: "") to clean(inside) else null
        }

    /** The text inside every <[name]> whose class carries [cls]. */
    private fun textWithClass(html: String, name: String, cls: String): List<String> =
        elementsNamed(html, name, RESULT_ELEMENTS).mapNotNull { (tag, inside) ->
            if ((attrsOf(tag)["class"] ?: "").split(' ').any { it == cls }) clean(inside) else null
        }

    /** DuckDuckGo, HTML first, lite as the fallback. Empty when both come back empty or with the
     *  bot check (DuckDuckGo answers a burst of requests with a challenge page instead of results;
     *  the lite endpoint is rate-limited separately). */
    private fun results(query: String, engine: Engine = Engine()): List<Hit> {
        // Brave first when the person gave a key; DuckDuckGo behind it either way, so a spent
        // credit or a bad key degrades to the keyless search instead of to nothing.
        if (engine.brave) {
            val b = try { brave(query, engine.key) } catch (e: Exception) {
                android.util.Log.w("LocalGhost", "brave search failed: ${e.message}"); emptyList()
            }
            if (b.isNotEmpty()) return b
        }
        val html = try { ddgHtml(query) } catch (e: Exception) { emptyList() }
        if (html.isNotEmpty()) return html
        return try { ddgLite(query) } catch (e: Exception) { emptyList() }
    }

    /** The Brave Search API: GET /res/v1/web/search with the key in X-Subscription-Token; JSON back. */
    private fun brave(query: String, key: String): List<Hit> {
        val conn = (URL("https://api.search.brave.com/res/v1/web/search?count=$MAX_HITS&q=" + URLEncoder.encode(query, "UTF-8"))
            .openConnection() as HttpURLConnection).apply {
            connectTimeout = 6000; readTimeout = 6000
            setRequestProperty("Accept", "application/json")
            setRequestProperty("X-Subscription-Token", key)
        }
        try {
            if (conn.responseCode !in 200..299) {
                android.util.Log.w("LocalGhost", "brave search: HTTP ${conn.responseCode}")
                return emptyList()
            }
            return parseBrave(readCapped(conn))
        } finally {
            conn.disconnect()
        }
    }

    /** web.results[] → hits: title, url, the description (Brave marks the matched words with
     *  <strong>, which comes out), and page_age as the page's own date when it has one. */
    internal fun parseBrave(json: String): List<Hit> {
        val root = try { JSONObject(json) } catch (_: Exception) { return emptyList() }
        val arr = root.optJSONObject("web")?.optJSONArray("results") ?: return emptyList()
        val out = ArrayList<Hit>()
        for (i in 0 until arr.length()) {
            val o = arr.optJSONObject(i) ?: continue
            val url = o.optString("url")
            if (!url.startsWith("http")) continue
            val title = clean(o.optString("title").replace(Regex("<[^>]+>"), ""))
            val snippet = clean(o.optString("description").replace(Regex("<[^>]+>"), ""))
            val age = o.optString("page_age").takeIf { it.length >= 10 }?.substring(0, 10) ?: ""
            out.add(Hit(title.ifBlank { url }, url, snippet, "", "page", "brave", age))
            if (out.size == MAX_HITS) break
        }
        return out
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
        val snippets = textWithClass(html, "a", "result__snippet")
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
        val snippets = textWithClass(html, "td", "result-snippet")
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
            if (s != null) { h.excerpt = s.excerpt; if (s.published.isNotBlank()) h.published = s.published; return }
        }
        val page = fetchPage(h.url) ?: return
        h.excerpt = page.excerptFor(terms)
        h.paragraphs = page.worthSending(terms)
        // The page's own date when it states one; otherwise keep what the search engine knew (Brave's page_age).
        if (page.published.isNotBlank()) h.published = page.published
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

        /** The paragraphs worth the box's time: the ones with the most question terms, up to
         *  PARAS_PER_PAGE and PARAS_CHARS, plus the first paragraph and the description, back in
         *  page order , the box ranks them by meaning; this is only the cut that keeps a 400 KB
         *  page from travelling whole over the phone's link. */
        fun worthSending(terms: List<String>): List<String> {
            if (paragraphs.isEmpty()) return if (description.isEmpty()) emptyList() else listOf(description)
            val lower = terms.map { it.lowercase() }
            val scored = paragraphs.withIndex().map { (i, p) ->
                val t = p.lowercase()
                var n = 0
                for (w in lower) if (t.contains(w)) n++
                Triple(i, n + (if (i == 0) 1 else 0), p)
            }
            val picked = scored.sortedWith(compareByDescending<Triple<Int, Int, String>> { it.second }.thenBy { it.first })
            val keep = ArrayList<Triple<Int, Int, String>>()
            var chars = 0
            for (s in picked) {
                if (keep.size >= PARAS_PER_PAGE) break
                val len = minOf(s.third.length, 1200)
                if (chars + len > PARAS_CHARS && keep.isNotEmpty()) continue
                keep.add(s); chars += len
            }
            val out = keep.sortedBy { it.first }.map { it.third.take(1200) }
            return if (description.isNotEmpty() && out.none { it.contains(description.take(40)) }) listOf(description) + out else out
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

    // THE PAGE IS A STRANGER'S. Until the second audit (12 Nov 2026) the reduction below was a
    // handful of regular expressions of the shape "<x …>.*?</x>", each of which, on a page with
    // thousands of openings and no closing (four hundred kilobytes of "<nav>" or "<!--"), starts
    // again at every opening and reads to the end: the square of the page, minutes of one core,
    // on a thread that cannot be stopped. A page like that only has to rank for something the
    // person asks. The cuts are now made by hand, each in one pass over the page: an element
    // that is never closed is left in (and its kind not looked for again), a tag that never
    // ends ends the search.
    private val dropNames = arrayOf("script", "style", "noscript", "svg", "iframe", "nav", "header", "footer", "aside", "form", "template", "figure")
    private val mainNames = arrayOf("article", "main")

    private fun wordChar(c: Char) = c.isLetterOrDigit() || c == '_'

    /** The name, of [names], of the element that opens at [i] (where a '<' stands), or null. */
    private fun opensAt(s: String, i: Int, names: Array<String>): String? {
        for (n in names) {
            val end = i + 1 + n.length
            if (end < s.length && s.regionMatches(i + 1, n, 0, n.length, ignoreCase = true) && !wordChar(s[end])) return n
        }
        return null
    }

    /** Where the closing tag of [name] starts, at or after [from]: the '<' of "</name>", or -1. */
    private fun closeAt(s: String, name: String, from: Int): Int {
        val needle = "</$name"
        var j = s.indexOf(needle, from, ignoreCase = true)
        while (j >= 0) {
            var k = j + needle.length
            while (k < s.length && s[k].isWhitespace()) k++
            if (k < s.length && s[k] == '>') return j
            j = s.indexOf(needle, j + 1, ignoreCase = true)
        }
        return -1
    }

    /** [html] without its comments; one that is never closed takes the rest of the page. */
    internal fun stripComments(html: String): String {
        var i = html.indexOf("<!--")
        if (i < 0) return html
        val sb = StringBuilder(html.length)
        var from = 0
        while (i >= 0) {
            sb.append(html, from, i)
            val end = html.indexOf("-->", i + 4)
            if (end < 0) return sb.toString()
            from = end + 3
            i = html.indexOf("<!--", from)
        }
        sb.append(html, from, html.length)
        return sb.toString()
    }

    /** [html] with each element named in [names] cut out whole, a space in its place. */
    internal fun dropElements(html: String, names: Array<String>): String {
        val dead = HashSet<String>() // kinds with no closing tag left on the page
        val sb = StringBuilder(html.length)
        var from = 0
        var i = html.indexOf('<')
        while (i >= 0) {
            val name = opensAt(html, i, names)
            if (name != null && name !in dead) {
                val gt = html.indexOf('>', i + 1)
                if (gt < 0) break // no tag ends after this point
                val close = closeAt(html, name, gt + 1)
                if (close < 0) dead.add(name)
                else {
                    sb.append(html, from, i).append(' ')
                    from = html.indexOf('>', close) + 1
                    i = html.indexOf('<', from)
                    continue
                }
            }
            i = html.indexOf('<', i + 1)
        }
        sb.append(html, from, html.length)
        return sb.toString()
    }

    /** The inside of the first <article> or <main> on the page that is closed, or null. */
    internal fun mainElement(html: String): String? {
        val dead = HashSet<String>()
        var i = html.indexOf('<')
        while (i >= 0 && dead.size < mainNames.size) {
            val name = opensAt(html, i, mainNames)
            if (name != null && name !in dead) {
                val gt = html.indexOf('>', i + 1)
                if (gt < 0) return null
                val close = closeAt(html, name, gt + 1)
                if (close >= 0) return html.substring(gt + 1, close)
                dead.add(name)
            }
            i = html.indexOf('<', i + 1)
        }
        return null
    }

    /** [s] with every tag replaced by [with]. A tag is a '<' with a letter, '/', '!' or '?' after
     *  it and a '>' somewhere on; any other '<' is text ("1 < 2 and 3 > 2" stays whole, where the
     *  expression before took "< 2 and 3 >" for a tag). */
    internal fun stripTags(s: String, with: String = ""): String {
        var i = s.indexOf('<')
        if (i < 0) return s
        val sb = StringBuilder(s.length)
        var from = 0
        while (i >= 0) {
            val c = if (i + 1 < s.length) s[i + 1] else ' '
            if (c.isLetter() || c == '/' || c == '!' || c == '?') {
                val gt = s.indexOf('>', i + 1)
                if (gt < 0) break
                sb.append(s, from, i).append(with)
                from = gt + 1
                i = s.indexOf('<', from)
            } else i = s.indexOf('<', i + 1)
        }
        sb.append(s, from, s.length)
        return sb.toString()
    }

    /** How much of [raw] is link text: what stands between each <a …> and its </a>. */
    internal fun linkChars(raw: String): Int = elementsNamed(raw, "a", Int.MAX_VALUE).sumOf { clean(it.second).length }

    /** Every <[name] …>…</[name]> in [html] in order, as (the opening tag, what is inside), up
     *  to [max]. The search goes on after each closing tag, so one inside another of its own
     *  kind is not listed twice; an opening with no closing after it ends the list. */
    internal fun elementsNamed(html: String, name: String, max: Int): List<Pair<String, String>> {
        val out = ArrayList<Pair<String, String>>()
        val needle = "<$name"
        var i = html.indexOf(needle, ignoreCase = true)
        while (i >= 0 && out.size < max) {
            val after = i + needle.length
            if (after < html.length && !wordChar(html[after])) { // "<a>", "<a href…", not "<abbr" or "<article"
                val gt = html.indexOf('>', after)
                if (gt < 0) break
                val close = closeAt(html, name, gt + 1)
                if (close < 0) break
                out.add(html.substring(i, gt + 1) to html.substring(gt + 1, close))
                i = html.indexOf(needle, close, ignoreCase = true)
                continue
            }
            i = html.indexOf(needle, after, ignoreCase = true)
        }
        return out
    }

    /** The attributes of one tag as written ("<meta name=… content=…>"), names in lower case,
     *  the first of a repeated name kept (as a browser does), at most 64. Read in one pass: the
     *  expression this replaces began again at every letter of a long tag. */
    internal fun attrsOf(tag: String): Map<String, String> {
        val out = HashMap<String, String>()
        val n = tag.length
        var i = 0
        if (i < n && tag[i] == '<') { // the tag's own name
            i++
            while (i < n && !tag[i].isWhitespace() && tag[i] != '>' && tag[i] != '/') i++
        }
        while (i < n && out.size < 64) {
            while (i < n && (tag[i].isWhitespace() || tag[i] == '/')) i++
            if (i >= n || tag[i] == '>') break
            val start = i
            while (i < n && !tag[i].isWhitespace() && tag[i] != '=' && tag[i] != '>' && tag[i] != '/') i++
            if (i == start) { i++; continue } // an '=' with no name before it
            val name = tag.substring(start, i).lowercase()
            while (i < n && tag[i].isWhitespace()) i++
            var value = ""
            if (i < n && tag[i] == '=') {
                i++
                while (i < n && tag[i].isWhitespace()) i++
                if (i < n && (tag[i] == '"' || tag[i] == '\'')) {
                    val end = tag.indexOf(tag[i], i + 1)
                    if (end < 0) { value = tag.substring(i + 1); i = n } else { value = tag.substring(i + 1, end); i = end + 1 }
                } else {
                    val from = i
                    while (i < n && !tag[i].isWhitespace() && tag[i] != '>') i++
                    value = tag.substring(from, i)
                }
            }
            if (name !in out) out[name] = value
        }
        return out
    }

    /** Every <[name] …> tag in [html], as written, up to [max] of them. */
    private fun tagsNamed(html: String, name: String, max: Int): List<String> {
        val out = ArrayList<String>()
        val needle = "<$name"
        var i = html.indexOf(needle, ignoreCase = true)
        while (i >= 0 && out.size < max) {
            val after = i + needle.length
            val gt = html.indexOf('>', after)
            if (gt < 0) break
            if (after < html.length && !wordChar(html[after]) && gt - i <= 4096) out.add(html.substring(i, gt + 1))
            i = html.indexOf(needle, gt, ignoreCase = true)
        }
        return out
    }

    /** The text of the page's <title>, "" without one. */
    private fun titleOf(head: String): String {
        val i = head.indexOf("<title", ignoreCase = true)
        if (i < 0) return ""
        val gt = head.indexOf('>', i + 6)
        if (gt < 0) return ""
        val close = closeAt(head, "title", gt + 1)
        return if (close < 0) "" else clean(head.substring(gt + 1, close))
    }
    private val blockEnd = Regex("</(p|div|li|h[1-6]|tr|td|th|section|article|blockquote|pre|dd|dt)\\s*>|<br\\s*/?>|<hr\\s*/?>", RegexOption.IGNORE_CASE)
    private val ldDate = Regex("\"datePublished\"\\s*:\\s*\"([^\"]+)\"")

    /**
     * The readability pass. Title and description from the head; the publish date from the
     * article meta, JSON-LD or the first <time>; then the body , the article/main element when
     * the page has one, else everything , with the boilerplate elements removed, split into
     * paragraphs at block ends, each paragraph kept only when it is long enough to say something
     * and is not mostly links (a menu, a tag cloud, a "related" list).
     */
    internal fun extract(html: String): Page {
        val noComments = stripComments(html)
        val head = noComments.take(64 * 1024)
        val title = titleOf(head)
        var description = ""
        var published = ""
        for (tag in tagsNamed(head, "meta", 400)) {
            val a = attrsOf(tag)
            val key = (a["property"] ?: a["name"] ?: a["itemprop"] ?: "").lowercase()
            val content = a["content"] ?: continue
            when (key) {
                "description", "og:description", "twitter:description" -> if (description.isEmpty() || key == "og:description") description = clean(content)
                "article:published_time", "datepublished", "date", "dc.date", "dc.date.issued", "pubdate", "publish-date", "og:updated_time", "article:modified_time" ->
                    if (published.isEmpty() || key == "article:published_time") published = content.take(32)
            }
        }
        if (published.isEmpty()) published = ldDate.find(noComments)?.groupValues?.get(1)?.take(32) ?: ""
        if (published.isEmpty()) published = tagsNamed(noComments, "time", 200)
            .firstNotNullOfOrNull { attrsOf(it)["datetime"]?.takeIf { d -> d.isNotEmpty() } }?.take(32) ?: ""
        val bodyStart = noComments.indexOf("<body", ignoreCase = true).let { if (it < 0) 0 else it }
        var body = noComments.substring(bodyStart)
        mainElement(body)?.let { inside -> if (inside.length > 400) body = inside }
        body = dropElements(body, dropNames)
        val paragraphs = ArrayList<String>()
        for (raw in blockEnd.split(body)) {
            val linkChars = linkChars(raw)
            val text = clean(stripTags(raw, " ")).replace('\n', ' ').replace(Regex(" {2,}"), " ").trim()
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

    /** A numeric entity as text; "" for a number that is no character (Character.toChars throws
     *  on one, and "&#x7fffffff;" in a title used to throw the whole page away). */
    private fun codePoint(n: Int?): String =
        if (n == null || n <= 0 || !Character.isValidCodePoint(n) || n in 0xD800..0xDFFF) "" else String(Character.toChars(n))

    internal fun clean(s: String): String {
        val noTags = stripTags(s)
        val decoded = entity.replace(noTags) { m ->
            val e = m.groupValues[1]
            when {
                e.startsWith("#x") || e.startsWith("#X") -> codePoint(e.substring(2).toIntOrNull(16))
                e.startsWith("#") -> codePoint(e.substring(1).toIntOrNull())
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
     * returns one [Hit] or null; [forQuestion] decides which apply. The one endpoint here is
     * public, keyless and rate-limited only by decency; one small GET. The weather and the
     * exchange rates are the box's (see the file's note), so those questions get no tool here:
     * the rates came from Frankfurter until 0.0.5, the same ECB table the box keeps, asked once
     * more from the phone for nothing.
     */
    object Tools {
        /** A weather question, kept out of Wikipedia's "what is" ("what is the weather like"). */
        private val weatherQ = Regex("\\b(weather|forecast|rain|raining|temperature|how (hot|cold|warm) is it|umbrella|sunny|snow|snowing|wind|windy|humid|humidity)\\b", RegexOption.IGNORE_CASE)
        private val whoWhat = Regex("^\\s*(?:who|what)\\s+(?:is|was|are|were)\\s+(?:the\\s+|a\\s+|an\\s+)?([\\p{L}\\p{N}][\\p{L}\\p{N} .'-]{1,60}?)\\s*\\??\\s*$", RegexOption.IGNORE_CASE)

        fun forQuestion(question: String): List<Callable<Hit?>> {
            val q = WebSearch.cleanQuery(question)
            val out = ArrayList<Callable<Hit?>>(1)
            whoWhat.find(q)?.let { m ->
                val subject = m.groupValues[1].trim()
                if (subject.split(' ').size <= 5 && !weatherQ.containsMatchIn(subject)) out.add(Callable { wikipediaHit(subject) })
            }
            return out
        }

        /** The plan's subject as a Wikipedia lookup: a name of at most five words that may leave
         *  the phone ([QueryGuard]) and is not the weather's. */
        fun forSubject(subject: String): List<Callable<Hit?>> {
            val s = subject.trim().trimEnd('?', '.', '!')
            if (!subjectFits(s)) return emptyList()
            return listOf(Callable { wikipediaHit(s) })
        }

        internal fun subjectFits(s: String): Boolean =
            s.length in 2..60 && s.split(' ').size <= 5 && QueryGuard.ok(s) && !weatherQ.containsMatchIn(s)

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
