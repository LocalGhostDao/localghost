package com.localghost.app.chat

/** The trail's lines: a status line made fit to keep ("searching the web on this phone for:
 *  ferry…" loses its trailing dots and the " , asking your box…" tail that only said what came
 *  next). Pure, for the tests. */
object Trail {
    fun line(status: String): String {
        var s = status.trim()
        for (tail in listOf(" , asking your box again…", " , asking your box…", " , searching again…")) {
            if (s.endsWith(tail)) s = s.removeSuffix(tail).trim()
        }
        s = s.trimEnd('…', '.', ' ')
        if (s == "asking your box" || s.startsWith("your box's model is ready")) return ""
        return s
    }
}
