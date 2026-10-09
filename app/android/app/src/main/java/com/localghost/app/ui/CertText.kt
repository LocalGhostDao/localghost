package com.localghost.app.ui

/** The line SETTINGS shows about this phone's certificate. Pure, for the tests. */
object CertText {
    /** "this phone's key: made 3 h ago, good for 13 more days · renewed once a day while the phone talks to the box". */
    fun line(notBeforeMs: Long, notAfterMs: Long, nowMs: Long): String {
        val age = nowMs - notBeforeMs
        val made = when {
            age < 0 -> "just made"
            age < 3600_000L -> "made ${age / 60_000L} min ago"
            age < 48 * 3600_000L -> "made ${age / 3600_000L} h ago"
            else -> "made ${age / 86_400_000L} days ago"
        }
        val left = notAfterMs - nowMs
        val good = when {
            left <= 0 -> "ran out"
            left < 3600_000L -> "good for ${left / 60_000L} more min"
            left < 48 * 3600_000L -> "good for ${left / 3600_000L} more h"
            else -> "good for ${left / 86_400_000L} more days"
        }
        return "this phone's key: $made, $good · renewed once a day while the phone talks to the box"
    }
}
