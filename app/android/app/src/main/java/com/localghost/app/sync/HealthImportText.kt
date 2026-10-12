package com.localghost.app.sync

/** The words of the Samsung Health import (HealthImport does the zipping and the sending):
 *  the box's scan as lines, and the import's progress as a line. Pure, so the tests read it. */
object HealthImportText {
    /** One table of the box's scan, as it lists them. */
    data class Table(val name: String, val rows: Int, val from: String, val to: String, val read: String, val err: String)
    data class Scan(val file: String, val tables: List<Table>, val days: Int, val note: String)

    /** The scan in lines for the screen: the known tables first, with what they give. */
    fun lines(s: Scan): List<String> {
        val out = ArrayList<String>()
        val known = s.tables.filter { it.read != "not read" }
        val other = s.tables.filter { it.read == "not read" }
        known.forEach { t ->
            val span = if (t.from.isNotEmpty()) " · ${t.from}..${t.to}" else ""
            out.add("${t.name}: ${"%,d".format(t.rows)} rows$span · ${t.read}" + (if (t.err.isNotEmpty()) " · ! ${t.err}" else ""))
        }
        if (other.isNotEmpty()) out.add("not read (${other.size}): " + other.joinToString(", ") { it.name })
        if (s.note.isNotEmpty()) out.add(s.note)
        out.add("${s.days} days named across the tables the box reads")
        return out
    }

    data class Progress(val state: String, val table: String, val tables: Int, val rows: Int, val days: Int, val months: Int, val samples: Int, val oldest: String, val newest: String, val error: String)

    fun progressOf(o: org.json.JSONObject?): Progress? {
        if (o == null) return null
        return Progress(o.optString("state"), o.optString("table"), o.optInt("tables"), o.optInt("rows"), o.optInt("days"), o.optInt("months"),
            o.optInt("samples"), o.optString("oldest"), o.optString("newest"), o.optString("error"))
    }

    /** The import's progress in a line. */
    fun progressLine(p: Progress): String = when (p.state) {
        "reading" -> "reading ${p.table.ifBlank { "the tables" }} · ${p.tables} tables, ${"%,d".format(p.rows)} rows so far"
        "writing" -> "writing · ${p.months} months, ${"%,d".format(p.days)} days, ${"%,d".format(p.samples)} heart rate readings" + (if (p.oldest.isNotEmpty()) " · ${p.oldest}..${p.newest}" else "")
        "done" -> "done · ${"%,d".format(p.days)} days from ${p.oldest} to ${p.newest}, ${"%,d".format(p.samples)} heart rate readings, in ${p.months} months"
        "failed" -> "! failed: ${p.error}"
        else -> p.state
    }
}
