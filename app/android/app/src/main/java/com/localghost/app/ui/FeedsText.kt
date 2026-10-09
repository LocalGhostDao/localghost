package com.localghost.app.ui

import org.json.JSONObject

/**
 * THE DATA FEEDS PANEL'S WORDS. The box judges each feed (internal/monitor: ok, filling, waiting,
 * flaky, late, failing) and writes the lines; the phone only reads the report, picks the mark and
 * the tone for each state, and says how old things are. Pure, so the JVM tests read it.
 */
object FeedsText {
    data class Row(val k: String, val v: String, val state: String)
    data class Section(val id: String, val title: String, val state: String, val line: String, val ageS: Long, val every: String, val rows: List<Row>)
    data class Report(val at: Long, val state: String, val summary: String, val sections: List<Section>)

    enum class Tone { GOOD, QUIET, WARN, BAD }

    /** The report as /v1/feeds/status sends it; null when it is not one. */
    fun parse(o: JSONObject): Report? {
        val arr = o.optJSONArray("sections") ?: return null
        val secs = (0 until arr.length()).mapNotNull { i ->
            val s = arr.optJSONObject(i) ?: return@mapNotNull null
            val ra = s.optJSONArray("rows")
            val rows = if (ra == null) emptyList() else (0 until ra.length()).mapNotNull { j ->
                val r = ra.optJSONObject(j) ?: return@mapNotNull null
                Row(r.optString("k"), r.optString("v"), r.optString("state"))
            }
            Section(s.optString("id"), s.optString("title"), s.optString("state"), s.optString("line"),
                s.optLong("ageS", -1), s.optString("every"), rows)
        }
        return Report(o.optLong("at"), o.optString("state"), o.optString("summary"), secs)
    }

    /** The mark before a section: a full dot when well, a ring while it waits or fills. */
    fun mark(state: String): String = when (state) {
        "ok" -> "●"
        "filling" -> "◐"
        "waiting" -> "○"
        "flaky", "late" -> "▲"
        "failing" -> "✕"
        else -> "·"
    }

    fun tone(state: String): Tone = when (state) {
        "ok" -> Tone.GOOD
        "filling", "waiting" -> Tone.QUIET
        "flaky", "late" -> Tone.WARN
        "failing" -> Tone.BAD
        else -> Tone.QUIET
    }

    /** The folded line under ghost.tallyd: "data feeds · all well", "data feeds · 2 want a look"
     *  (the sections not well, counted; filling and waiting are not a problem). */
    fun foldLine(r: Report?, unsupported: Boolean): String {
        if (r == null) return if (unsupported) "data feeds · not reported by this build" else "data feeds · reading…"
        val n = r.sections.count { tone(it.state) == Tone.WARN || tone(it.state) == Tone.BAD }
        return "data feeds · " + when {
            n == 1 -> "1 wants a look"
            n > 1 -> "$n want a look"
            r.state == "filling" -> "filling"
            r.state == "waiting" -> "waiting"
            else -> "all well"
        }
    }

    /** The folded line under ghost.framed: "archive · 99% at the latest stage · 26 damaged". */
    fun pipelineLine(total: Int, atLatest: Int, damaged: Int, read: Boolean, unsupported: Boolean): String {
        if (!read) return if (unsupported) "archive pipeline · not reported by this build" else "archive pipeline · reading…"
        if (total == 0) return "archive pipeline · nothing archived yet"
        val pct = if (atLatest >= total) 100 else (100L * atLatest / total).toInt()
        return "archive pipeline · " + (if (atLatest >= total) "all at the latest stage" else "$pct% at the latest stage") +
            (if (damaged > 0) " · $damaged damaged" else "")
    }

    /** The word on the panel's title row. */
    fun word(state: String): String = when (state) {
        "ok" -> "ALL WELL"
        "filling" -> "FILLING"
        "waiting" -> "WAITING"
        "flaky" -> "FLAKY"
        "late" -> "LATE"
        "failing" -> "FAILING"
        else -> state.uppercase()
    }

    /** An age the way the panel writes it: "12 s", "4 min", "3 h", "2 days"; "" for none. */
    fun age(sec: Long): String = when {
        sec < 0 -> ""
        sec < 90 -> "$sec s"
        sec < 90 * 60 -> "${(sec + 30) / 60} min"
        sec < 48 * 3600 -> "${(sec + 1800) / 3600} h"
        else -> "${sec / 86400} days"
    }

    /** The section's age as of now: the box said how old the newest piece was when it wrote the
     *  report, and the report itself may be a poll old. */
    fun ageNow(s: Section, reportAt: Long, nowS: Long): String =
        if (s.ageS < 0) "" else age(s.ageS + (nowS - reportAt).coerceAtLeast(0))

    /** A line under the title when the report itself is old (the box stopped answering the polls). */
    fun staleness(r: Report, nowS: Long): String {
        val d = nowS - r.at
        return if (d >= 120) "the box's last report is ${age(d)} old" else ""
    }

    /** One feed's line on the FEEDS page: "answered 40 min ago · 32 entries", "off", "failed 3 times:
     *  404 · last answered 2 days ago", "never answered yet". */
    fun feedLine(enabled: Boolean, lastOk: Long, lastFetch: Long, lastStatus: String, lastItems: Int, failures: Int, nowS: Long): String {
        if (!enabled) return "off" + (if (lastOk > 0) " · last answered ${age(nowS - lastOk)} ago" else "")
        return when {
            lastFetch == 0L -> "not fetched yet"
            failures > 0 -> "failed $failures time${if (failures == 1) "" else "s"}" + (if (lastStatus.isNotBlank()) ": $lastStatus" else "") +
                (if (lastOk > 0) " · last answered ${age(nowS - lastOk)} ago" else " · never answered yet")
            lastOk > 0 -> "answered ${age(nowS - lastOk)} ago · $lastItems entr${if (lastItems == 1) "y" else "ies"}"
            else -> "never answered yet"
        }
    }
}
