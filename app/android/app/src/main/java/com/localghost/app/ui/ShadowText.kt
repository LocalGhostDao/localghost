package com.localghost.app.ui

import org.json.JSONArray
import org.json.JSONObject
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.util.Locale
import kotlin.math.abs
import kotlin.math.roundToInt

/**
 * SHADOW, in words. ghost.shadowd is the daemon that checks the box itself: it asks a saved
 * question again with nothing about you and compares (the cold read), it reads a chat for the box
 * giving way, pressing or leading, and it counts what the box already holds. It names what it
 * sees and stops. This is the reading of what the box says about it (/v1/shadow…) and every line
 * the screen shows, kept apart from the screen so it runs in a plain JVM test.
 *
 * Pure: JSON in, text out; no Android in here.
 */
object ShadowText {
    class Entry(val n: Int, val slug: String, val name: String, val origin: List<String>, val need: List<String>,
                val level: String, val danger: String, val isNew: Boolean, val tells: String, val does: String, val detectors: List<String>) {
        val built get() = detectors.isNotEmpty()
    }

    class Detector(val id: String, val entry: Int, val name: String, val reads: String, val unit: String,
                   val baseline: Double, val min: Double, val max: Double, val step: Double, val announce: Boolean, val model: Boolean,
                   val threshold: Double, val mutedUntil: Long, val fired: Int, val lastFired: Long) {
        /** The box says what one press moves it by; a detector with no step, or no room, is fixed. */
        val tunable get() = max > min && step > 0.0
    }

    class Observation(val id: Long, val detector: String, val subject: String, val at: Long, val title: String, val body: String, val muted: Boolean)

    class Change(val at: Long, val detector: String, val what: String, val old: String, val new: String, val quieter: Boolean)

    class Arbiter(val has: Boolean, val model: String, val ready: Boolean, val why: String, val servedBy: String, val main: String)

    class Fleet(val catalogue: List<Entry>, val detectors: List<Detector>, val nextReset: Long, val resetDays: Int,
                val maxMuteHours: Int, val changes: List<Change>, val arbiter: Arbiter?)

    class Finding(val kind: String, val quote: String, val after: String)

    class Read(val kind: String, val verdict: String, val sided: String, val cold: String, val coldModel: String, val sameModel: Boolean, val conduct: List<Finding>)

    class TrailEntry(val msgId: Long, val atMs: Long, val asked: List<String>, val given: List<String>, val reasoning: String, val praise: Boolean, val read: Read?)

    class Asked(val since: Long, val done: Long, val read: Int, val why: String)

    class Trail(val chat: Long, val entries: List<TrailEntry>, val reading: Boolean, val asked: Asked?)

    private fun strings(a: JSONArray?): List<String> = if (a == null) emptyList() else (0 until a.length()).map { a.optString(it) }.filter { it.isNotEmpty() }
    private fun <T> objects(a: JSONArray?, f: (JSONObject) -> T?): List<T> =
        if (a == null) emptyList() else (0 until a.length()).mapNotNull { i -> a.optJSONObject(i)?.let(f) }

    fun parseFleet(o: JSONObject): Fleet {
        val cat = objects(o.optJSONArray("catalogue")) { e ->
            Entry(e.optInt("n"), e.optString("slug"), e.optString("name"), strings(e.optJSONArray("origin")), strings(e.optJSONArray("need")),
                e.optString("level"), e.optString("danger"), e.optBoolean("new"), e.optString("tells"), e.optString("does"), strings(e.optJSONArray("detectors")))
        }
        val dets = objects(o.optJSONArray("detectors")) { d ->
            val id = d.optString("id")
            if (id.isEmpty()) null else Detector(id, d.optInt("entry"), d.optString("name"), d.optString("reads"), d.optString("unit"),
                d.optDouble("baseline", 0.0), d.optDouble("min", 0.0), d.optDouble("max", 0.0), d.optDouble("step", 0.0), d.optBoolean("announce"), d.optBoolean("model"),
                d.optDouble("threshold", d.optDouble("baseline", 0.0)), d.optLong("mutedUntil"), d.optInt("fired"), d.optLong("lastFired"))
        }
        val changes = objects(o.optJSONArray("changes")) { c ->
            Change(c.optLong("at"), c.optString("detector"), c.optString("what"), c.optString("old"), c.optString("new"), c.optBoolean("quieter"))
        }
        val arb = o.optJSONObject("arbiter")?.let { a ->
            Arbiter(a.optBoolean("has"), a.optString("model"), a.optBoolean("ready"), a.optString("why"), a.optString("servedBy"), a.optString("main"))
        }
        return Fleet(cat, dets, o.optLong("nextReset"), o.optInt("resetDays", 90), o.optInt("maxMuteHours", 72), changes, arb)
    }

    fun parseObservations(o: JSONObject): List<Observation> = objects(o.optJSONArray("observations")) { x ->
        Observation(x.optLong("id"), x.optString("detector"), x.optString("subject"), x.optLong("at"), x.optString("title"), x.optString("body"), x.optBoolean("muted"))
    }

    fun parseTrail(o: JSONObject): Trail {
        val entries = objects(o.optJSONArray("trail")) { e ->
            val read = e.optJSONObject("read")?.let { r ->
                Read(r.optString("kind"), r.optString("verdict"), r.optString("sided"), r.optString("cold"), r.optString("coldModel"), r.optBoolean("sameModel", true),
                    objects(r.optJSONArray("conduct")) { f -> Finding(f.optString("kind"), f.optString("quote"), f.optString("after")) })
            }
            TrailEntry(e.optLong("msgId"), e.optLong("at"), strings(e.optJSONArray("asked")), strings(e.optJSONArray("given")),
                e.optString("reasoning"), e.optBoolean("opensWithPraise"), read)
        }
        val asked = o.optJSONObject("asked")?.let { a -> Asked(a.optLong("since"), a.optLong("done"), a.optInt("read"), a.optString("why")) }
        return Trail(o.optLong("chat"), entries, o.optBoolean("reading"), asked)
    }

    // ---- the lines ----

    /** A number as a person writes it: 3, 0.25, 0.9; never 3.0. */
    fun num(v: Double): String = if (v == Math.rint(v) && abs(v) < 1e9) v.toLong().toString()
        else String.format(Locale.UK, "%.2f", v).trimEnd('0').trimEnd('.')

    /** Which model reads cold, said the way the box's own records say it. */
    fun readBy(a: Arbiter?): String = when {
        a == null -> "the cold read is by the box's model"
        a.has && a.ready -> "the cold read is by ${a.model}, a second model beside ${a.main.ifBlank { "the main one" }}"
        a.has -> "the cold read is waiting for ${a.model}, which is not up" + if (a.why.isNotBlank()) ": ${a.why}" else ""
        else -> "the cold read is by ${a.main.ifBlank { "the box's one model" }}, with nothing about you"
    }

    /** What that means, under it: one model is the same opinion with less to go on. */
    fun readByNote(a: Arbiter?): String = when {
        a == null || (a.has && a.ready) -> ""
        a.has -> "Until it is, an answer is not read. After fifteen minutes the main model reads instead, and says so."
        else -> "That takes you out of the question, which is most of it. It is not a second opinion: for one, the box needs a second model (see the box's docs/SHADOW.md)."
    }

    /** The value one step quieter (up) or louder (down), kept inside the bounds; null when there is none. */
    fun stepped(d: Detector, up: Boolean): Double? {
        if (!d.tunable) return null
        val v = ((d.threshold + if (up) d.step else -d.step) * 100).roundToInt() / 100.0
        val c = v.coerceIn(d.min, d.max)
        return if (abs(c - d.threshold) < 1e-9) null else c
    }

    fun thresholdLine(d: Detector): String = when {
        !d.tunable -> "fixed: " + d.unit
        abs(d.threshold - d.baseline) < 1e-9 -> "speaks at ${num(d.threshold)} (the baseline; ${num(d.min)} to ${num(d.max)})"
        else -> "speaks at ${num(d.threshold)} (baseline ${num(d.baseline)}; ${num(d.min)} to ${num(d.max)})"
    }

    /** Where a detector's words go. The ones about other people are never put on a lock screen. */
    fun sentLine(d: Detector): String = when {
        d.announce -> "sent to this phone when it speaks"
        d.id == "mail-pressure" -> "marked on the mail, not sent"
        else -> "kept here for you to find, never sent"
    }

    private val dayFmt = DateTimeFormatter.ofPattern("d MMM", Locale.UK)
    private val dayTimeFmt = DateTimeFormatter.ofPattern("d MMM, HH:mm", Locale.UK)
    private val longFmt = DateTimeFormatter.ofPattern("d MMMM yyyy", Locale.UK)

    fun day(sec: Long, zone: ZoneId = ZoneId.systemDefault()): String = dayFmt.format(Instant.ofEpochSecond(sec).atZone(zone))
    fun dayTime(sec: Long, zone: ZoneId = ZoneId.systemDefault()): String = dayTimeFmt.format(Instant.ofEpochSecond(sec).atZone(zone))

    fun mutedLine(d: Detector, nowSec: Long, zone: ZoneId = ZoneId.systemDefault()): String =
        if (d.mutedUntil > nowSec) "muted until ${dayTime(d.mutedUntil, zone)}. It goes on recording; what it sees is kept and marked." else ""

    fun spokenLine(d: Detector, zone: ZoneId = ZoneId.systemDefault()): String = when (d.fired) {
        0 -> "has not spoken"
        1 -> "spoken once, ${day(d.lastFired, zone)}"
        else -> "spoken ${d.fired} times, last ${day(d.lastFired, zone)}"
    }

    fun resetLine(f: Fleet, zone: ZoneId = ZoneId.systemDefault()): String =
        "Every threshold goes back to its baseline every ${f.resetDays} days; next on ${longFmt.format(Instant.ofEpochSecond(f.nextReset).atZone(zone))}. A mute lasts ${f.maxMuteHours / 24} days at most. Neither is a setting."

    fun changeLine(c: Change, name: String, zone: ZoneId = ZoneId.systemDefault()): String {
        val what = when (c.what) {
            "threshold" -> "threshold ${c.old} to ${c.new}"
            "mute" -> if (c.new.isBlank() || c.new == "0") "mute lifted" else "muted"
            "reset" -> "back to the baseline" + if (c.old.isNotBlank()) " (was ${c.old})" else ""
            else -> c.what
        }
        return "${day(c.at, zone)}  ${name.ifBlank { c.detector }}: $what" + if (c.quieter) "  (quieter)" else ""
    }

    /** The entry's tags, as the post prints them. */
    fun tags(e: Entry): String = (listOf(e.origin.joinToString("/")) + e.need + listOf(e.level, e.danger) + if (e.isNew) listOf("NEW") else emptyList())
        .filter { it.isNotBlank() }.joinToString(" · ")

    fun builtCount(f: Fleet): String {
        val built = f.catalogue.count { it.built }
        return "${f.detectors.size} detectors over $built of ${f.catalogue.size} patterns. The other ${f.catalogue.size - built} are listed with why not."
    }

    /** The arbiter's record of one answer, in a line. */
    fun readLine(r: Read?): String = when {
        r == null -> "not read yet"
        r.kind == "other" || r.kind.isBlank() -> "looked at; not a question of judgement, so not read cold"
        r.kind == "unclear" -> "read cold; the comparison came back unclear"
        r.kind == "fact" || r.kind == "task" -> "read cold; a ${r.kind}, so nothing to side with"
        r.verdict == "yes" -> "read cold: the same conclusion" + by(r)
        r.verdict == "partly" -> "read cold: the same direction, a different weight" + by(r)
        r.verdict == "no" -> "read cold: a different conclusion" + by(r)
        else -> "read cold" + by(r)
    }

    private fun by(r: Read): String = when {
        r.coldModel.isBlank() -> ""
        r.sameModel -> " (by the same model, with nothing about you)"
        else -> " (by ${r.coldModel})"
    }

    fun findingTitle(kind: String): String = when (kind) {
        "caved" -> "gave way when pushed"
        "pressed" -> "pushed past your reason"
        "led" -> "asked its way to an answer"
        else -> kind
    }

    /** What the read asked for came to. */
    fun askedLine(t: Trail, nowSec: Long, zone: ZoneId = ZoneId.systemDefault()): String {
        val a = t.asked
        return when {
            t.reading -> "the arbiter is reading this chat now. A minute or two on the card, several on the CPU."
            a == null || a.done == 0L -> ""
            a.why.isNotBlank() && a.read == 0 -> a.why
            a.read == 0 -> "asked ${dayTime(a.done, zone)}: there was nothing left to read"
            else -> "read ${dayTime(a.done, zone)}: ${a.read} answer${if (a.read == 1) "" else "s"}" + if (a.why.isNotBlank()) ". ${a.why}" else ""
        }
    }

    /** The fleet in the order the screen lists it: what the box does first, then what you do with
     *  it, then what you wrote, then what reaches you; a detector the app has no heading for goes last. */
    fun sections(f: Fleet): List<Pair<String, List<Detector>>> {
        val heads = listOf(
            "THE BOX'S OWN ANSWERS" to setOf(29, 30, 21, 26),
            "YOUR TIME WITH IT" to setOf(18, 19),
            "YOUR OWN WORDS" to setOf(22, 11),
            "WHAT YOU WROTE ABOUT OTHER PEOPLE" to setOf(1, 2, 3),
            "WHAT REACHES YOU" to setOf(8, 9),
            "THESE SETTINGS" to setOf(28),
        )
        val known = heads.flatMap { it.second }.toSet()
        val out = heads.map { (title, entries) -> title to f.detectors.filter { it.entry in entries } }.toMutableList()
        out.add("OTHER" to f.detectors.filter { it.entry !in known })
        return out.filter { it.second.isNotEmpty() }
    }

    /** A link's argument: "chat:12" is a chat's audit trail, anything else a detector's page. */
    fun chatOf(arg: String): Long = if (arg.startsWith("chat:")) arg.removePrefix("chat:").toLongOrNull() ?: 0L else 0L
}
