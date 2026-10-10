package com.localghost.app.ui

/** The HEALTH screen's status line: the newest day the box holds, and how this phone's last
 *  hand-over went. Pure, so the tests read it. [at] 0 means the phone never ran a sync. */
object HealthStatus {
    fun line(newestOnBox: String?, at: Long, days: Int, newestDay: String, error: String, skipped: String, now: Long): String {
        val box = if (newestOnBox.isNullOrEmpty()) "the box holds no day yet" else "the box holds days up to $newestOnBox"
        val skip = if (skipped.isNotEmpty()) " (skipped $skipped)" else ""
        val phone = when {
            at <= 0 -> "this phone has not shipped yet"
            error.isNotEmpty() -> "last try ${TrailStatus.ago(now - at)}: $error"
            days == 0 -> "last run ${TrailStatus.ago(now - at)}: nothing found in Health Connect for the last week$skip"
            else -> "last run ${TrailStatus.ago(now - at)}: shipped $days ${if (days == 1) "day" else "days"}, newest $newestDay$skip"
        }
        return "$box · $phone"
    }
}
