package com.localghost.app.local

import android.content.Context
import com.localghost.app.net.WebSearch

/**
 * WHO READS THE PAGES. The phone fetches the web (the box never does); then someone has to read
 * what came back. The box's model on its GPU reads a few pages in about two seconds, so it gets
 * the paragraphs, as before. When the box is slow (its model on the CPU reads a page in half a
 * minute) the phone's own small model reads each page into a handful of notes, checked against
 * the page, and the box writes the answer from notes and quotes instead of pages. When there is no
 * box at all, the phone reads and answers by itself.
 *
 * The decisions and the checks are plain functions (Plan) so they are tested without a phone; the
 * reading itself (digest, answer) drives LocalModel.
 */
object PhoneReader {

    enum class Route { BOX_READS, PHONE_READS, PHONE_ALONE, NOBODY }

    /** Pure: who reads, given what is known. */
    object Plan {
        const val CPU_BOX_PROMPT_TPS = 40.0  // a 12B model on a CPU, when the box did not say
        const val NOTE_TOKENS = 110          // what one page's notes cost to write
        const val PAGE_TOKENS_MAX = 1400     // a page's text the phone will read at most
        const val PAGE_TOKENS_MIN = 250      // below this a page is not worth the phone's model
        const val BUDGET_S = 45.0            // the phone's reading budget for one question

        /**
         * [pageTokens] is what the box would read if it got the pages (the paragraphs the phone
         * sends); the phone reads at most PAGE_TOKENS_MAX of each page itself.
         */
        fun route(boxReachable: Boolean, box: WebSearch.BoxSpeed?, planAnswered: Boolean, phoneUsable: Boolean,
                  phonePromptTps: Double, phoneGenTps: Double, pages: Int, pageTokens: Int): Route {
            if (!boxReachable) return if (phoneUsable) Route.PHONE_ALONE else Route.NOBODY
            if (!phoneUsable || pages == 0) return Route.BOX_READS
            // the box on its GPU reads everything faster than the phone could start
            if (box != null && box.known && box.onGPU) return Route.BOX_READS
            // the box said nothing (old box, or no answer in time): slow or far , the phone reads
            if (box == null || !box.known) return if (planAnswered) Route.BOX_READS else Route.PHONE_READS
            // the box on its CPU: whoever finishes first
            val boxTps = if (box.promptTps > 0) box.promptTps else CPU_BOX_PROMPT_TPS
            val boxSeconds = pageTokens / boxTps
            val perPage = minOf(PAGE_TOKENS_MAX, pageTokens / pages.coerceAtLeast(1))
            val phoneSeconds = pages * (perPage / phonePromptTps + NOTE_TOKENS / phoneGenTps) +
                // and the box still reads the notes: ~150 tokens a page
                pages * 150 / boxTps
            return if (phoneSeconds < boxSeconds * 0.8) Route.PHONE_READS else Route.BOX_READS
        }

        /** How many tokens of each page the phone reads, so [pages] fit the budget at its speed. */
        fun perPageTokens(pages: Int, promptTps: Double, genTps: Double, budgetS: Double = BUDGET_S): Int {
            if (pages <= 0) return 0
            val perPageS = budgetS / pages - NOTE_TOKENS / genTps
            return (perPageS * promptTps).toInt().coerceIn(0, PAGE_TOKENS_MAX)
        }

        private val numberRe = Regex("\\d+(?:[.,]\\d+)*")

        /**
         * The notes a small model wrote, kept only where they stay inside the page: at most five
         * lines, each short, and every number in a line present in the page (as written, or with
         * its thousands separators dropped). "NOTHING" or nothing left: no notes.
         */
        fun groundNotes(notes: String, page: String): List<String> {
            if (notes.trim().uppercase().startsWith("NOTHING")) return emptyList()
            val pageNums = HashSet<String>()
            for (m in numberRe.findAll(page)) {
                pageNums.add(m.value); pageNums.add(m.value.replace(",", "")); pageNums.add(m.value.replace(",", "."))
            }
            val out = ArrayList<String>()
            for (raw in notes.lines()) {
                var line = raw.trim().trimStart('-', '*', '•', ' ').trim()
                if (line.isEmpty() || line.uppercase() == "NOTHING") continue
                if (line.length > 280) line = line.take(280).substringBeforeLast(' ') + "…"
                val ok = numberRe.findAll(line).all { m ->
                    m.value in pageNums || m.value.replace(",", "") in pageNums || m.value.replace(",", ".") in pageNums
                }
                if (!ok) continue
                out.add(line)
                if (out.size == 5) break
            }
            return out
        }

        /** The page's paragraph that says most about the need, verbatim, clipped. */
        fun bestQuote(paragraphs: List<String>, need: String, max: Int = 420): String {
            val terms = WebSearch.terms(need).map { it.lowercase() }
            var best = ""; var bestN = -1
            for (p in paragraphs) {
                val low = p.lowercase()
                val n = terms.count { low.contains(it) }
                if (n > bestN) { bestN = n; best = p }
            }
            if (best.length <= max) return best
            return best.take(max).substringBeforeLast(' ') + "…"
        }

        /** A page's text for the phone's model: description then paragraphs, cut to about [tokens]. */
        fun pageText(description: String, paragraphs: List<String>, tokens: Int): String {
            val maxChars = tokens * 4
            val sb = StringBuilder()
            if (description.isNotBlank()) sb.append(description.trim()).append("\n\n")
            for (p in paragraphs) {
                if (sb.length + p.length + 2 > maxChars) {
                    val room = maxChars - sb.length
                    if (room > 200) sb.append(p.take(room).substringBeforeLast(' ')).append("…")
                    break
                }
                sb.append(p.trim()).append("\n\n")
            }
            return sb.toString().trim()
        }

        const val NOTE_SYSTEM = "You read one web page and write down only what it says about one question. You never add anything that is not on the page."

        fun notePrompt(need: String, title: String, site: String, published: String, text: String): String =
            "What is needed: $need\n\nPage: $title , $site" + (if (published.isNotBlank()) ", $published" else "") +
                "\n\"\"\"\n$text\n\"\"\"\n\n" +
                "Write at most 5 short lines, each starting with \"- \", with the facts from this page that answer what is needed. " +
                "Copy numbers, prices, dates and names exactly as the page writes them. " +
                "If the page says nothing useful about it, write only: NOTHING"

        fun answerPrompt(question: String, noted: List<WebSearch.Hit>): String {
            val sb = StringBuilder("Question: ").append(question).append("\n\nNotes from web pages this phone just read:\n")
            noted.forEachIndexed { i, h ->
                sb.append("[${i + 1}] ${h.title} (${h.site}")
                if (h.published.isNotBlank()) sb.append(", ${h.published}")
                sb.append(")\n")
                val body = if (h.kind == "note") h.excerpt else h.excerpt.ifBlank { h.snippet }
                sb.append(body.take(900)).append("\n")
            }
            sb.append("\nAnswer the question in two to five sentences, from these notes where they help, citing the notes by number like [1]. ")
                .append("Where the notes do not cover it, answer from what you know and say that part is not from the notes.")
            return sb.toString()
        }
    }

    /** What the phone read, for the chat's status line and the findings. */
    class Read(val hits: List<WebSearch.Hit>, val digested: Int, val seconds: Double, val skipped: Int)

    /**
     * The phone's model reads the page hits into notes, within the budget; a page it has no time
     * for, or whose notes do not survive the check, goes on as the page's best paragraph (its quote)
     * so nothing found is lost. Non-page hits (weather, rates, summaries) pass through untouched.
     * [status] is told what is happening ("reading page 2 of 3 on this phone…").
     */
    suspend fun digest(ctx: Context, need: String, hits: List<WebSearch.Hit>, status: (String) -> Unit): Read {
        val t0 = System.currentTimeMillis()
        val pages = hits.filter { it.kind == "page" && (it.paragraphs.isNotEmpty() || it.excerpt.isNotBlank()) }
        val per = Plan.perPageTokens(pages.size, LocalModel.Speed.promptTps(ctx), LocalModel.Speed.genTps(ctx))
        var digested = 0; var skipped = 0; var n = 0
        val out = ArrayList<WebSearch.Hit>()
        for (h in hits) {
            if (h !in pages) { out.add(h); continue }
            n++
            val paras = h.paragraphs.ifEmpty { listOf(h.excerpt) }
            val quote = Plan.bestQuote(paras, need)
            val elapsed = (System.currentTimeMillis() - t0) / 1000.0
            if (per < Plan.PAGE_TOKENS_MIN || elapsed > Plan.BUDGET_S) {
                skipped++
                out.add(noteHit(h, "", quote))
                continue
            }
            status("reading page $n of ${pages.size} on this phone (${h.site})…")
            val text = Plan.pageText("", paras, per)
            val r = LocalModel.complete(ctx, Plan.NOTE_SYSTEM, Plan.notePrompt(need, h.title, h.site, h.published, text),
                maxTokens = Plan.NOTE_TOKENS + 40, temperature = 0.1f)
            val notes = if (r != null) Plan.groundNotes(r.text, text) else emptyList()
            if (notes.isNotEmpty()) digested++ else skipped++
            out.add(noteHit(h, notes.joinToString("\n") { "- $it" }, quote))
        }
        return Read(out, digested, (System.currentTimeMillis() - t0) / 1000.0, skipped)
    }

    private fun noteHit(h: WebSearch.Hit, notes: String, quote: String): WebSearch.Hit =
        WebSearch.Hit(h.title, h.url, h.snippet, notes, "note", h.source, h.published, emptyList(), quote)

    /** No box: the phone's model answers from what it read. Streams through [onText]. */
    suspend fun answerAlone(ctx: Context, question: String, read: List<WebSearch.Hit>,
                            history: List<Pair<Boolean, String>> = emptyList(),
                            length: String = com.localghost.app.chat.AnswerLength.MEDIUM,
                            onText: (String) -> Boolean): String? {
        val useful = read.filter { it.excerpt.isNotBlank() || it.quote.isNotBlank() }
            .map { if (it.kind == "note" && it.excerpt.isBlank()) WebSearch.Hit(it.title, it.url, it.snippet, it.quote, "page", it.source, it.published) else it }
        // how much answer: a word for the model before the question, and its room (AnswerLength)
        val how = com.localghost.app.chat.AnswerLength.phoneInstruction(length)
        val asked = if (how.isEmpty()) question else how + "\n" + question
        val prompt = if (useful.isEmpty()) asked else Plan.answerPrompt(asked, useful)
        // the conversation so far rides in front, so "and in euros?" is read after the question it follows
        return LocalModel.complete(ctx, LocalModel.LIFEBOAT_SYSTEM, Transcript.withHistory(prompt, history),
            maxTokens = com.localghost.app.chat.AnswerLength.phoneMaxTokens(length), temperature = 0.3f, onText = onText)?.text
    }
}
