package com.localghost.app.ui

/** The WIKIPEDIA page in words: the state line, a hit's "how" in a word, a stored body split into
 *  its sections. Pure, for the tests. */
object WikiText {
    /** "Wikipedia, 2026-06 · 6.9 million articles, 11.2 million redirects", "importing: 42% of the
     *  file read, 2.1 million articles so far", "downloading", "none on the box", "the import
     *  stopped: …", "the box is locked". */
    fun state(state: String, edition: String, articles: Long, redirects: Long, imported: Long, entries: Long, error: String): String = when (state) {
        "ready" -> "$edition · ${millions(articles)} articles, ${millions(redirects)} redirects, on your box"
        "importing" -> {
            val pct = if (entries > 0) (100 * imported / entries) else 0
            "importing: $pct% of the file read, ${millions(articles)} articles in so far · the chat uses it once it is all in"
        }
        "downloading" -> "downloading the file (about 50 GB) · imported into the box's database once it is here"
        "failed" -> "the import stopped: $error · tried again every minute"
        "locked" -> "the box is locked"
        else -> "none on the box · sudo ./tools/update.sh wiki fetches it from the mirror (about 50 GB)"
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
