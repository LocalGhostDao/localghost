package com.localghost.app.ui

/** The WIKIPEDIA page in words: the state line, a hit's "how" in a word, a stored body split into
 *  its sections. Pure, for the tests. */
object WikiText {
    /** "Wikipedia, 2026-06 · 6.9 million articles, 11.2 million redirects", "importing: 42% of the
     *  file read, 2.1 million articles so far", "downloading", "none on the box", "the import
     *  stopped: …", "the box is locked". */
    fun state(state: String, edition: String, articles: Long, redirects: Long, imported: Long, entries: Long, error: String,
              leftMinutes: Long = 0, readers: Int = 0): String = when (state) {
        "ready" -> "$edition · ${millions(articles)} articles, ${millions(redirects)} redirects, on your box"
        "importing" -> {
            val pct = if (entries > 0) (100 * imported / entries) else 0
            "importing: $pct% of the file read, ${millions(articles)} articles in so far" +
                (if (readers > 1) " ($readers readers)" else "") +
                (if (leftMinutes > 0) ", about ${left(leftMinutes)} to go" else "") + " · a title search finds what is in already"
        }
        "downloading" -> "downloading the file (about 50 GB) · imported into the box's database once it is here"
        "failed" -> "the import stopped: $error · tried again every minute"
        "locked" -> "the box is locked"
        "missing", "" -> "none on the box · INTEGRATIONS › Wikipedia fetches it from the mirror (about 50 GB)"
        else -> "the box said \"$state\" · INTEGRATIONS › Wikipedia says more"
    }

    /** The stats lines under the state: the counts and the size, when it was imported and how long
     *  it took, what was skipped, what the search can do, the questions it answered. */
    fun stats(articles: Long, redirects: Long, bytes: Long, startedAt: Long, doneAt: Long, skipped: Long,
              indexed: Boolean, likeness: Boolean, answers: Int, state: String): List<String> {
        val out = ArrayList<String>()
        if (articles > 0) out.add("${millions(articles)} articles · ${millions(redirects)} redirects" + (if (bytes > 0) " · ${gb(bytes)} in the database" else ""))
        if (state == "ready" && doneAt > 0) {
            val took = if (startedAt in 1 until doneAt) " (took ${left((doneAt - startedAt) / 60)})" else ""
            out.add("imported ${day(doneAt)}$took" + (if (skipped > 0) " · ${millions(skipped)} entries skipped" else ""))
        }
        if (state == "ready") out.add("search: by title and prefix" + (if (indexed) ", the words of a lead" else "") + (if (indexed && likeness) ", and close spellings" else "") +
            (if (!indexed) " · the lead and likeness indexes are being built" else ""))
        if (answers > 0) out.add("answered $answers chat question${if (answers == 1) "" else "s"} since the box started")
        return out
    }

    fun gb(bytes: Long): String = when {
        bytes >= 1L shl 30 -> "%.1f GB".format(java.util.Locale.US, bytes / (1024.0 * 1024 * 1024))
        bytes >= 1L shl 20 -> "${bytes shr 20} MB"
        else -> "${bytes shr 10} KB"
    }

    /** "6 Oct 2026, 03:12" from unix seconds, in the phone's zone. */
    fun day(unix: Long): String = java.text.SimpleDateFormat("d MMM yyyy, HH:mm", java.util.Locale.UK).format(java.util.Date(unix * 1000))

    /** "40 minutes", "3 hours", "2 days": the coarsest unit that fits. */
    fun left(minutes: Long): String = when {
        minutes < 60 -> "$minutes minute" + (if (minutes == 1L) "" else "s")
        minutes < 48 * 60 -> ((minutes + 30) / 60).let { "$it hour" + (if (it == 1L) "" else "s") }
        else -> ((minutes + 720) / 1440).let { "$it day" + (if (it == 1L) "" else "s") }
    }

    fun millions(n: Long): String = when {
        n >= 1_000_000 -> "%.1f million".format(java.util.Locale.US, n / 1e6)
        n >= 1_000 -> "%,d".format(java.util.Locale.US, n)
        else -> n.toString()
    }

    /** How a hit was found, as the page says it under the title. */
    fun how(how: String): String = when (how) {
        "exact" -> "the title"
        "redirect" -> "a redirect"
        "qualified" -> "the place, qualified"
        "prefix" -> "starts with it"
        "like" -> "looks like it"
        "text" -> "the words of it"
        else -> how
    }

    data class Part(val heading: String, val text: String)

    /** A stored body ("== Heading ==" lines between the sections) as parts. */
    fun parts(body: String): List<Part> {
        val out = ArrayList<Part>()
        var heading = ""
        val text = StringBuilder()
        fun flush() {
            if (text.isNotBlank()) out.add(Part(heading, text.toString().trim()))
            text.setLength(0)
        }
        for (line in body.split('\n')) {
            if (line.startsWith("== ") && line.endsWith(" ==")) {
                flush(); heading = line.removePrefix("== ").removeSuffix(" ==")
            } else if (line.isNotBlank()) {
                if (text.isNotEmpty()) text.append('\n')
                text.append(line)
            }
        }
        flush()
        return out
    }
}
