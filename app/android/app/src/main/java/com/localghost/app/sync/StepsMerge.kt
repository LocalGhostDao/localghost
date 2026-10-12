package com.localghost.app.sync

/**
 * THE WATCH'S OWN NUMBER. Samsung Health's steps tile merges its devices (the watch and the
 * phone) by taking the larger count in each slice of the day, and Health Connect's total for
 * the day came out under it (15,909 against 18,776 on 10 October 2026, 9,162 against 12,032
 * the day after). From the raw records the sync rebuilds that merge: the day in ten-minute
 * bins, each record's count spread over the bins it spans, the larger count per bin across the
 * devices, summed. Never a sum of two devices for the same minutes. Pure, so the tests read it.
 */
object StepsMerge {
    /** One steps record: unix seconds, the count, and the device that counted ("" when unsaid). */
    data class Entry(val start: Long, val end: Long, val count: Long, val device: String)

    const val BIN_S = 600L

    /** The merged count over [entries]: per bin the largest device's share, summed. */
    fun merged(entries: List<Entry>, binS: Long = BIN_S): Double {
        // bin -> device -> steps in the bin
        val bins = HashMap<Long, HashMap<String, Double>>()
        entries.forEach { e ->
            if (e.count <= 0) return@forEach
            val len = (e.end - e.start).coerceAtLeast(1)
            var b = e.start / binS
            val last = (e.end - 1).coerceAtLeast(e.start) / binS
            while (b <= last) {
                val from = maxOf(e.start, b * binS)
                val to = minOf(e.end, (b + 1) * binS)
                val share = if (e.end <= e.start) e.count.toDouble() else e.count * (to - from).toDouble() / len
                val d = bins.getOrPut(b) { HashMap() }
                d[e.device] = (d[e.device] ?: 0.0) + share
                b++
            }
        }
        return bins.values.sumOf { d -> d.values.max() }
    }

    /** The sum per device, for the diagnostics ("watch 15,909, phone 2,867"). */
    fun byDevice(entries: List<Entry>): Map<String, Long> {
        val out = java.util.TreeMap<String, Long>()
        entries.forEach { e -> out[e.device] = (out[e.device] ?: 0L) + e.count }
        return out
    }
}
