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

    /** An ask's state line: "you answered: yes", "waiting for your answer". */
    fun askLine(options: List<String>, answer: String): String = when {
        options.isEmpty() -> ""
        answer.isNotEmpty() -> "you answered: $answer"
        else -> "waiting for your answer"
    }
}
