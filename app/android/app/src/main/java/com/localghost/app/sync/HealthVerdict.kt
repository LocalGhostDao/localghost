package com.localghost.app.sync

/** One line that says what is wrong with the health data, from the evidence the phone has: pure,
 *  so the tests read it; HealthDiag gathers the evidence. */
object HealthVerdict {
    /** The way into Samsung Health's sharing page, which is off by default for most kinds. */
    const val SAMSUNG_PATH = "Samsung Health › ⋮ › Settings › Health Connect (or Connected services › Health Connect) › allow all data types"

    fun of(available: Boolean, missing: Int, lines: List<String>, samsung: Boolean, samsungWrites: Boolean = true): String {
        if (!available) return "Health Connect is not available on this phone"
        if (missing > 0 && lines.isEmpty()) return "no Health Connect permission is granted: SETTINGS › HEALTH › CONNECT HEALTH"
        val empty = lines.count { it.contains("nothing in Health Connect") }
        val unreadable = lines.count { it.contains("not readable") }
        val read = lines.size - empty - unreadable
        return when {
            // the clearest case: Samsung Health is on the phone and not one record in Health
            // Connect is its; whatever is there (the phone's own steps, say) another app wrote
            samsung && !samsungWrites && lines.isNotEmpty() && unreadable == 0 ->
                "Samsung Health writes nothing to Health Connect" + (if (read > 0) " (what is there, another app wrote)" else "") + ": $SAMSUNG_PATH"
            lines.isNotEmpty() && empty == lines.size && samsung ->
                "Health Connect is empty: Samsung Health is installed but not sharing; $SAMSUNG_PATH"
            lines.isNotEmpty() && empty == lines.size -> "Health Connect is empty: no app writes to it on this phone"
            unreadable > 0 -> "$unreadable type(s) not readable: a permission missing (SETTINGS › HEALTH › CONNECT HEALTH)"
            empty > 0 -> {
                // the empty kinds by name: the person switches them on one by one in Samsung
                // Health's Health Connect page, so the line says which
                val names = lines.filter { it.contains("nothing in Health Connect") }.map { it.substringBefore(':') }
                val read = lines.size - empty
                "nothing in Health Connect for ${names.joinToString(", ")}" +
                    (if (samsung) " ($SAMSUNG_PATH)" else " (no app writes them)") +
                    "; $read kind(s) read"
            }
            missing > 0 -> "$missing permission(s) not granted; the rest read"
            else -> "Health Connect reads; if the box has nothing, the sync did not reach it (SETTINGS › HEALTH says the last run)"
        }
    }
}
