package com.localghost.app.ui

import java.time.DayOfWeek
import java.time.LocalDate

/**
 * THE HEALTH SCREEN'S ARITHMETIC AND WORDS, pure so the tests read them. The box answers with
 * one series per metric (a day and a value each); the screen cuts windows from it (a week, a
 * month, a season, a year, everything), sums what sums and averages what does not, and says each
 * number against the window's mean. Stats, not judgements: no goals, no scores, no streaks.
 */
object HealthText {
    /** How a metric is shown and added up. [cumulative] metrics (steps, distance) have a total
     *  over a window; the rest (a heart rate, a weight) only a mean. */
    data class Metric(val key: String, val label: String, val group: String, val cumulative: Boolean)

    const val MOVEMENT = "MOVEMENT"
    const val SLEEP = "SLEEP"
    const val HEART = "HEART"
    const val BODY = "BODY"
    val groups = listOf(MOVEMENT, SLEEP, HEART, BODY)

    /** Every metric the sync ships, in the order the screen lists them. */
    val metrics: List<Metric> = listOf(
        Metric("steps", "steps", MOVEMENT, true),
        Metric("distance_km", "distance", MOVEMENT, true),
        Metric("active_calories", "active kcal", MOVEMENT, true),
        Metric("floors", "floors", MOVEMENT, true),
        Metric("exercise_minutes", "exercise", MOVEMENT, true),
        Metric("sleep_minutes", "sleep", SLEEP, false),
        Metric("sleep_deep_minutes", "deep sleep", SLEEP, false),
        Metric("sleep_light_minutes", "light sleep", SLEEP, false),
        Metric("sleep_rem_minutes", "REM sleep", SLEEP, false),
        Metric("sleep_awake_minutes", "awake in bed", SLEEP, false),
        Metric("resting_hr", "resting heart rate", HEART, false),
        Metric("hr_avg", "heart rate (avg)", HEART, false),
        Metric("hr_min", "heart rate (min)", HEART, false),
        Metric("hr_max", "heart rate (max)", HEART, false),
        Metric("hrv_ms", "heart rate variability", HEART, false),
        Metric("spo2_pct", "blood oxygen", HEART, false),
        Metric("resp_rate", "breathing rate", HEART, false),
        Metric("vo2max", "VO2 max", BODY, false),
        Metric("weight_kg", "weight", BODY, false),
        Metric("body_fat_pct", "body fat", BODY, false),
    )
    private val byKey = metrics.associateBy { it.key }

    fun metric(key: String): Metric = byKey[key] ?: Metric(key, key.replace('_', ' '), BODY, false)
    fun label(key: String): String = metric(key).label
    fun order(key: String): Int = metrics.indexOfFirst { it.key == key }.let { if (it < 0) 99 else it }

    /** The windows the chips offer: days back, 0 for everything. */
    data class Window(val label: String, val days: Int)
    val windows = listOf(Window("7D", 7), Window("30D", 30), Window("90D", 90), Window("1Y", 365), Window("ALL", 0))

    private fun isTime(key: String) = key.startsWith("sleep_") || key == "exercise_minutes"

    /** A value in the metric's unit: 15,909 · 7h12m · 61 bpm · 97% · 1.3 km. */
    fun fmt(key: String, v: Double): String = when {
        isTime(key) -> "${(v / 60).toInt()}h${"%02d".format((v % 60).toInt())}m"
        key == "spo2_pct" || key == "body_fat_pct" -> "%.0f%%".format(v)
        key == "hrv_ms" -> "%.0f ms".format(v)
        key == "resp_rate" -> "%.1f/min".format(v)
        key == "vo2max" -> "%.1f".format(v)
        key == "distance_km" -> "%.1f km".format(v)
        key == "weight_kg" -> "%.1f kg".format(v)
        key.startsWith("hr_") || key == "resting_hr" -> "%.0f bpm".format(v)
        key == "active_calories" -> "%,d kcal".format(v.toInt())
        else -> "%,d".format(v.toInt())
    }

    /** A difference, signed: +1,204 · −0h20m · +0.3 kg · ±0. */
    fun fmtDelta(key: String, d: Double): String {
        if (d.isNaN()) return ""
        val sign = if (d > 0) "+" else if (d < 0) "−" else "±"
        val a = Math.abs(d)
        val body = when {
            isTime(key) -> "${(a / 60).toInt()}h${"%02d".format((a % 60).toInt())}m"
            key == "spo2_pct" || key == "body_fat_pct" -> "%.1f%%".format(a)
            key == "hrv_ms" -> "%.0f ms".format(a)
            key == "resp_rate" -> "%.1f/min".format(a)
            key == "vo2max" -> "%.1f".format(a)
            key == "distance_km" -> "%.1f km".format(a)
            key == "weight_kg" -> "%.1f kg".format(a)
            key.startsWith("hr_") || key == "resting_hr" -> "%.0f bpm".format(a)
            key == "active_calories" -> "%,d kcal".format(a.toInt())
            else -> "%,d".format(a.toInt())
        }
        return sign + body
    }

    /** The days and values on or after [since] (YYYY-MM-DD; "" keeps everything). The series is
     *  oldest first, as the box sends it. */
    fun window(days: List<String>, values: List<Double>, since: String): Pair<List<String>, List<Double>> {
        if (since.isEmpty()) return days to values
        val d = ArrayList<String>(); val v = ArrayList<Double>()
        for (i in days.indices) if (days[i] >= since) { d.add(days[i]); v.add(values[i]) }
        return d to v
    }

    /** The date [n] days before [today], or "" for everything. */
    fun since(today: LocalDate, n: Int): String = if (n <= 0) "" else today.minusDays((n - 1).toLong()).toString()

    data class Summary(val n: Int, val latest: Double, val latestDay: String, val mean: Double, val median: Double,
                       val min: Double, val minDay: String, val max: Double, val maxDay: String, val total: Double)

    fun summary(days: List<String>, values: List<Double>): Summary? {
        if (values.isEmpty() || days.size != values.size) return null
        var mi = 0; var ma = 0
        for (i in values.indices) { if (values[i] < values[mi]) mi = i; if (values[i] > values[ma]) ma = i }
        val sorted = values.sorted()
        val median = if (sorted.size % 2 == 1) sorted[sorted.size / 2] else (sorted[sorted.size / 2 - 1] + sorted[sorted.size / 2]) / 2
        return Summary(values.size, values.last(), days.last(), values.average(), median,
            values[mi], days[mi], values[ma], days[ma], values.sum())
    }

    /** The latest value against the window's mean: "+1,204 on the 30-day mean of 14,705". */
    fun againstMean(key: String, latest: Double, mean: Double, windowLabel: String): String =
        "${fmtDelta(key, latest - mean)} on the $windowLabel mean of ${fmt(key, mean)}"

    /** The last [n] days against the [n] before them, as a signed percentage of the earlier
     *  mean; null when either side is empty. Days are counted by the calendar, not by rows, so a
     *  day the watch missed is not a day borrowed from the period before. */
    fun trend(days: List<String>, values: List<Double>, today: LocalDate, n: Int): Double? {
        val cut = today.minusDays((n - 1).toLong()).toString()
        val before = today.minusDays((2L * n - 1)).toString()
        var a = 0.0; var an = 0; var b = 0.0; var bn = 0
        for (i in days.indices) {
            when {
                days[i] >= cut -> { a += values[i]; an++ }
                days[i] >= before -> { b += values[i]; bn++ }
            }
        }
        if (an == 0 || bn == 0 || b == 0.0) return null
        return ((a / an) / (b / bn) - 1) * 100
    }

    /** "last 7 days +8% on the 7 before" or "" when one side has no days. */
    fun trendLine(days: List<String>, values: List<Double>, today: LocalDate, n: Int): String {
        val t = trend(days, values, today, n) ?: return ""
        val sign = if (t > 0) "+" else if (t < 0) "−" else "±"
        return "last $n days $sign${"%.0f".format(Math.abs(t))}% on the $n before"
    }

    /** The mean per weekday, Monday first; null where the window has no such day. */
    fun weekdayMeans(days: List<String>, values: List<Double>): List<Double?> {
        val sums = DoubleArray(7); val counts = IntArray(7)
        for (i in days.indices) {
            val d = try { LocalDate.parse(days[i]) } catch (_: Exception) { continue }
            val w = d.dayOfWeek.value - 1
            sums[w] += values[i]; counts[w]++
        }
        return (0 until 7).map { if (counts[it] == 0) null else sums[it] / counts[it] }
    }

    val weekdayLetters = listOf("M", "T", "W", "T", "F", "S", "S")

    /** The weekday pattern in words: "most on Saturdays (18,204), least on Tuesdays (9,870)". */
    fun weekdayLine(key: String, means: List<Double?>): String {
        val present = means.withIndex().filter { it.value != null }
        if (present.size < 2) return ""
        val hi = present.maxByOrNull { it.value!! }!!
        val lo = present.minByOrNull { it.value!! }!!
        if (hi.index == lo.index) return ""
        return "most on ${plural(hi.index)} (${fmt(key, hi.value!!)}), least on ${plural(lo.index)} (${fmt(key, lo.value!!)})"
    }

    private fun plural(i: Int) = DayOfWeek.of(i + 1).name.lowercase().replaceFirstChar { it.uppercase() } + "s"

    /** The stats block for a metric over a window, one line each. */
    fun statLines(key: String, s: Summary, windowLabel: String): List<String> {
        val m = metric(key)
        val out = ArrayList<String>()
        out.add("latest ${fmt(key, s.latest)} · ${s.latestDay}")
        out.add("mean ${fmt(key, s.mean)} · median ${fmt(key, s.median)}")
        out.add("max ${fmt(key, s.max)} · ${s.maxDay}")
        out.add("min ${fmt(key, s.min)} · ${s.minDay}")
        if (m.cumulative) out.add("total ${fmt(key, s.total)} over ${s.n} ${if (s.n == 1) "day" else "days"} ($windowLabel)")
        else out.add("${s.n} ${if (s.n == 1) "day" else "days"} with a reading ($windowLabel)")
        return out
    }

    /** Sleep's efficiency where the stages are known: asleep over time in bed, as a percentage. */
    fun sleepEfficiency(total: Double, awake: Double): Double? =
        if (total <= 0 || awake < 0 || awake > total) null else (total - awake) / total * 100

    /** One line of the day page per metric: "steps 15,909 · +1,204 on the 30-day mean". */
    fun dayLine(key: String, v: Double, mean: Double?): String =
        "${label(key)} ${fmt(key, v)}" + (if (mean == null) "" else " · ${fmtDelta(key, v - mean)} on the 30-day mean")

    /** The heart rate samples of a day in words: "52..134 bpm · lowest 03:40 · highest 18:05 · 211 readings". */
    fun samplesLine(samples: List<Pair<Long, Double>>, hhmm: (Long) -> String): String {
        if (samples.isEmpty()) return "no heart rate readings kept for the day"
        val lo = samples.minByOrNull { it.second }!!
        val hi = samples.maxByOrNull { it.second }!!
        return "${lo.second.toInt()}..${hi.second.toInt()} bpm · lowest ${hhmm(lo.first)} · highest ${hhmm(hi.first)} · ${samples.size} readings"
    }

    /** The header line under the window chips: "30 days · 2026-09-11 to 2026-10-10 · 12 kinds". */
    fun windowLine(days: List<String>, kinds: Int, w: Window): String {
        if (days.isEmpty()) return "no days in this window"
        val span = if (days.first() == days.last()) days.first() else "${days.first()} to ${days.last()}"
        val n = days.size
        return "$n ${if (n == 1) "day" else "days"} · $span · $kinds ${if (kinds == 1) "kind" else "kinds"}"
    }
}
