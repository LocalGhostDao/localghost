package com.localghost.app.sync

/** One line that says what is wrong with the health data, from the evidence the phone has: pure,
 *  so the tests read it; HealthDiag gathers the evidence. */
object HealthVerdict {
    fun of(available: Boolean, missing: Int, lines: List<String>, samsung: Boolean): String {
        if (!available) return "Health Connect is not available on this phone"
        if (missing > 0 && lines.isEmpty()) return "no Health Connect permission is granted: SETTINGS › HEALTH › CONNECT HEALTH"
        val empty = lines.count { it.contains("nothing in Health Connect") }
        val unreadable = lines.count { it.contains("not readable") }
        return when {
            lines.isNotEmpty() && empty == lines.size && samsung ->
                "Health Connect is empty: Samsung Health is installed but not sharing; Samsung Health › Settings › Health Connect › allow every data type"
            lines.isNotEmpty() && empty == lines.size -> "Health Connect is empty: no app writes to it on this phone"
            unreadable > 0 -> "$unreadable type(s) not readable: a permission missing (SETTINGS › HEALTH › CONNECT HEALTH)"
            empty > 0 -> "$empty type(s) empty in Health Connect (Samsung Health › Settings › Health Connect shares them one by one); the rest read"
            missing > 0 -> "$missing permission(s) not granted; the rest read"
            else -> "Health Connect reads; if the box has nothing, the sync did not reach it (SETTINGS › HEALTH says the last run)"
        }
    }
}
