package com.localghost.app.ui

/**
 * A NOTIFICATION'S PAGE IN WORDS: who said it and what kind of thing it is, the line that says
 * where it leads, and what the page shows under it for each kind. Pure, so the JVM tests read it.
 */
object NotifPage {
    /** "ghost.cued · something nearby", "ghost.framed · the week's highlight". */
    fun who(service: String, kind: String): String {
        val k = when (kind) {
            "checkin" -> "the evening check-in"
            "nearby" -> "something nearby"
            "reflection" -> "a memory brought back"
            "news" -> "the news"
            "highlight" -> "the week's highlight"
            "observation" -> "an observation"
            "ask" -> "a question"
            "", "message" -> ""
            else -> kind
        }
        return service.removePrefix("ghost.") + (if (k.isNotEmpty()) " · $k" else "")
    }

    /** What the page shows under the notification, by where it leads: "day", "memory", "near",
     *  "memories", "checkin", "news", "status", "" for nothing more. */
    fun shows(t: NotifLink.Target): String = when (t.dest) {
        "day" -> if (t.arg.isNotEmpty()) "day" else ""
        "map" -> if (t.arg.isNotEmpty()) "day" else ""
        "memories" -> when {
            t.arg == "near" -> "near"
            t.arg.isNotEmpty() -> "memory"
            else -> "memories"
        }
        "checkin" -> "checkin"
        "news" -> "news"
        "status" -> "status"
        else -> ""
    }

    /** The day a target names, "" for none (a day page or the map on a day). */
    fun dayOf(t: NotifLink.Target): String = if ((t.dest == "day" || t.dest == "map") && t.arg.isNotEmpty()) t.arg else ""

    /** The memory id a target names, 0 for none. */
    fun memoryOf(t: NotifLink.Target): Long = if (t.dest == "memories") t.arg.toLongOrNull() ?: 0L else 0L

    /** The button that goes on: "the whole day ›", "the memory ›", "near you ›", "CHECK-IN ›", "NEWS ›", "Box Status ›". */
    fun goes(t: NotifLink.Target): String = when (shows(t)) {
        "day" -> "the whole day ›"
        "memory" -> "the memory ›"
        "near" -> "near you ›"
        "memories" -> "MEMORIES ›"
        "checkin" -> "CHECK-IN ›"
        "news" -> "NEWS ›"
        "status" -> "Box Status ›"
        else -> ""
    }

    /** The body as points, when it is a list: the news digest is one story a line (the box
     *  writes "• " first on each since 0.0.7; older digests have the bare lines), and any body
     *  whose lines all start with a bullet or a dash. Empty for a body to show as it is. */
    fun points(kind: String, body: String): List<String> {
        val lines = body.split('\n').map { it.trim() }.filter { it.isNotEmpty() }
        if (lines.size < 2 && kind != "news") return emptyList()
        val marked = lines.all { it.startsWith("• ") || it.startsWith("- ") }
        if (kind != "news" && !marked) return emptyList()
        return lines.map { it.removePrefix("• ").removePrefix("- ").trim() }
    }

    /** The list's preview of a body of points: the first point and how many more ("… and 7
     *  more"); the body itself when it is prose. */
    fun preview(kind: String, body: String): String {
        val p = points(kind, body)
        if (p.size < 2) return body
        return p[0] + " … and ${p.size - 1} more"
    }

    /** The collapsed notification's one line for a body of points: the first point, bare. */
    fun firstLine(body: String): String = body.lineSequence().firstOrNull()?.trim()?.removePrefix("• ")?.trim() ?: body

    /** An ask's state line: "you answered: yes", "waiting for your answer". */
    fun askLine(options: List<String>, answer: String): String = when {
        options.isEmpty() -> ""
        answer.isNotEmpty() -> "you answered: $answer"
        else -> "waiting for your answer"
    }
}
