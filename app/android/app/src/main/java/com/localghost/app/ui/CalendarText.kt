package com.localghost.app.ui

import org.json.JSONArray
import org.json.JSONObject
import java.time.Instant
import java.time.LocalDate
import java.time.ZoneId
import java.time.ZoneOffset
import java.time.format.DateTimeFormatter
import java.util.Locale

/**
 * THE CALENDAR's words and shapes, pure: what the phone's calendar store's numbers mean, the
 * upload the box takes, the events as the box gives them back, and the lines the agenda, a day
 * and HOME show. No Android in here, so the tests read it. Read-only, everywhere: nothing in the
 * app writes to a calendar.
 */
object CalendarText {
    data class Attendee(val name: String, val email: String, val status: String)

    /** One instance of an event, times in unix seconds. An all-day one starts at UTC midnight of
     *  its date and ends at UTC midnight after its last (that is how the calendar store keeps it). */
    data class Event(
        val id: Long, val cal: Long, val calendar: String, val account: String, val title: String,
        val location: String, val notes: String, val begin: Long, val end: Long, val allDay: Boolean,
        val tz: String, val status: String, val self: String, val busy: Boolean, val organizer: String,
        val attendees: List<Attendee>,
    ) {
        /** The person is going: not cancelled, not declined. */
        val live: Boolean get() = status != "cancelled" && self != "declined"
    }

    data class CalInfo(val id: Long, val name: String, val account: String, val type: String, val color: String, val visible: Boolean, val events: Int)

    data class Meta(val at: Long, val from: Long, val to: Long, val zone: String, val events: Int, val removed: Int, val calendars: List<CalInfo>)

    data class Page(val events: List<Event>, val meta: Meta?)

    // ── the calendar store's numbers (CalendarContract) ──

    /** Events.STATUS: 0 tentative, 1 confirmed, 2 cancelled. */
    fun eventStatus(i: Int?): String = when (i) { 0 -> "tentative"; 1 -> "confirmed"; 2 -> "cancelled"; else -> "" }

    /** Attendees.ATTENDEE_STATUS and Events.SELF_ATTENDEE_STATUS: 0 none, 1 accepted, 2 declined, 3 invited, 4 tentative. */
    fun answer(i: Int?): String = when (i) { 0 -> "none"; 1 -> "accepted"; 2 -> "declined"; 3 -> "invited"; 4 -> "tentative"; else -> "" }

    /** Events.AVAILABILITY: 0 busy, 1 free, 2 tentative (counted as busy). */
    fun busy(i: Int?): Boolean = i == null || i != 1

    /** A colour int as #rrggbb. */
    fun colour(i: Int?): String = if (i == null) "" else String.format(Locale.ROOT, "#%06x", i and 0xffffff)

    // ── the upload ──

    fun eventJson(e: Event): JSONObject {
        val o = JSONObject().put("id", e.id).put("cal", e.cal).put("title", e.title).put("begin", e.begin).put("end", e.end)
        if (e.calendar.isNotEmpty()) o.put("calendar", e.calendar)
        if (e.account.isNotEmpty()) o.put("account", e.account)
        if (e.location.isNotEmpty()) o.put("location", e.location)
        if (e.notes.isNotEmpty()) o.put("notes", e.notes)
        if (e.allDay) o.put("allDay", true)
        if (e.tz.isNotEmpty()) o.put("tz", e.tz)
        if (e.status.isNotEmpty()) o.put("status", e.status)
        if (e.self.isNotEmpty()) o.put("self", e.self)
        if (e.busy) o.put("busy", true)
        if (e.organizer.isNotEmpty()) o.put("organizer", e.organizer)
        if (e.attendees.isNotEmpty()) {
            val a = JSONArray()
            e.attendees.forEach { at ->
                val ao = JSONObject()
                if (at.name.isNotEmpty()) ao.put("name", at.name)
                if (at.email.isNotEmpty()) ao.put("email", at.email)
                if (at.status.isNotEmpty()) ao.put("status", at.status)
                a.put(ao)
            }
            o.put("attendees", a)
        }
        return o
    }

    /** The body POST /v1/calendar/upload takes. Events sorted so the same calendar is the same
     *  bytes, which is what [fingerprint] relies on. */
    fun upload(device: String, zone: String, utcOffset: Int, from: Long, to: Long, cals: List<CalInfo>, events: List<Event>): JSONObject {
        val ca = JSONArray()
        cals.sortedBy { it.id }.forEach {
            ca.put(JSONObject().put("id", it.id).put("name", it.name).put("account", it.account).put("type", it.type)
                .put("color", it.color).put("visible", it.visible).put("events", it.events))
        }
        val ea = JSONArray()
        events.sortedWith(compareBy({ it.begin }, { it.id })).forEach { ea.put(eventJson(it)) }
        return JSONObject().put("device", device).put("zone", zone).put("utcOffset", utcOffset)
            .put("from", from).put("to", to).put("calendars", ca).put("events", ea)
    }

    /** What decides whether an upload is worth sending: the calendars and the events, not the
     *  window (which moves every day). */
    fun fingerprint(body: JSONObject): String {
        val s = (body.optJSONArray("calendars")?.toString() ?: "") + "|" + (body.optJSONArray("events")?.toString() ?: "") + "|" + body.optString("zone")
        val d = java.security.MessageDigest.getInstance("SHA-256").digest(s.toByteArray())
        return d.take(12).joinToString("") { String.format(Locale.ROOT, "%02x", it) }
    }

    // ── what the box gives back ──

    fun parseEvent(o: JSONObject): Event {
        val at = ArrayList<Attendee>()
        o.optJSONArray("attendees")?.let { a ->
            for (i in 0 until a.length()) {
                val x = a.optJSONObject(i) ?: continue
                at.add(Attendee(x.optString("name"), x.optString("email"), x.optString("status")))
            }
        }
        return Event(o.optLong("id"), o.optLong("cal"), o.optString("calendar"), o.optString("account"), o.optString("title"),
            o.optString("location"), o.optString("notes"), o.optLong("begin"), o.optLong("end"), o.optBoolean("allDay"),
            o.optString("tz"), o.optString("status"), o.optString("self"), o.optBoolean("busy"), o.optString("organizer"), at)
    }

    fun parseEvents(a: JSONArray?): List<Event> {
        if (a == null) return emptyList()
        return (0 until a.length()).mapNotNull { a.optJSONObject(it)?.let(::parseEvent) }
    }

    fun parseMeta(o: JSONObject?): Meta? {
        if (o == null || o.optLong("at") <= 0) return null
        val cs = ArrayList<CalInfo>()
        o.optJSONArray("calendars")?.let { a ->
            for (i in 0 until a.length()) {
                val x = a.optJSONObject(i) ?: continue
                cs.add(CalInfo(x.optLong("id"), x.optString("name"), x.optString("account"), x.optString("type"), x.optString("color"),
                    x.optBoolean("visible"), x.optInt("events")))
            }
        }
        return Meta(o.optLong("at"), o.optLong("from"), o.optLong("to"), o.optString("zone"), o.optInt("events"), o.optInt("removed"), cs)
    }

    fun parsePage(o: JSONObject): Page = Page(parseEvents(o.optJSONArray("events")), parseMeta(o.optJSONObject("meta")))

    // ── the lines ──

    private val hm = DateTimeFormatter.ofPattern("HH:mm", Locale.UK)
    private val dayFmt = DateTimeFormatter.ofPattern("EEE d MMM", Locale.UK)
    private val longDay = DateTimeFormatter.ofPattern("EEEE d MMMM", Locale.UK)

    /** The dates an event covers: one for a timed one (its start's, in the zone), every date of
     *  an all-day one (in UTC, as kept). */
    fun dates(e: Event, zone: ZoneId): List<LocalDate> {
        if (!e.allDay) return listOf(Instant.ofEpochSecond(e.begin).atZone(zone).toLocalDate())
        val first = Instant.ofEpochSecond(e.begin).atOffset(ZoneOffset.UTC).toLocalDate()
        val last = Instant.ofEpochSecond(maxOf(e.begin, e.end - 1)).atOffset(ZoneOffset.UTC).toLocalDate()
        val out = ArrayList<LocalDate>()
        var d = first
        while (!d.isAfter(last) && out.size < 62) { out.add(d); d = d.plusDays(1) }
        return out
    }

    /** The time part of a row: "10:00–11:00", "all day", "from 22:00", "until 01:00". */
    fun time(e: Event, zone: ZoneId, on: LocalDate? = null): String {
        if (e.allDay) {
            val ds = dates(e, zone)
            return if (ds.size > 1 && on != null) "all day, ${ds.indexOf(on) + 1} of ${ds.size}" else "all day"
        }
        val b = Instant.ofEpochSecond(e.begin).atZone(zone)
        val en = Instant.ofEpochSecond(e.end).atZone(zone)
        if (!en.isAfter(b)) return b.format(hm)
        if (on != null && b.toLocalDate() != en.toLocalDate()) return b.format(hm) + "–" + en.format(dayFmt) + " " + en.format(hm)
        return b.format(hm) + "–" + en.format(hm)
    }

    /** The second line: the place, the guests, the calendar. */
    fun detail(e: Event): String {
        val parts = ArrayList<String>()
        if (e.location.isNotBlank()) parts.add(e.location.trim())
        val others = e.attendees.filter { it.email.isEmpty() || !it.email.equals(e.account, ignoreCase = true) }
        if (others.isNotEmpty()) {
            val names = others.map { it.name.ifBlank { it.email } }.filter { it.isNotBlank() }
            if (names.isNotEmpty()) parts.add("with " + names.take(3).joinToString(", ") + if (names.size > 3) " and ${names.size - 3} more" else "")
        }
        if (e.calendar.isNotBlank()) parts.add(e.calendar)
        return parts.joinToString(" · ")
    }

    fun title(e: Event): String = e.title.trim().ifEmpty { "(no title)" }

    /** cancelled, declined, maybe, or "" for a plain one. */
    fun mark(e: Event): String = when {
        e.status == "cancelled" -> "cancelled"
        e.self == "declined" -> "declined"
        e.self == "tentative" || e.status == "tentative" -> "maybe"
        e.self == "invited" -> "not answered"
        else -> ""
    }

    /** The agenda: the events grouped by date in order, an all-day one under each of its dates,
     *  timed ones by their start; all-day ones first within a date. */
    fun agenda(events: List<Event>, zone: ZoneId, from: LocalDate? = null, to: LocalDate? = null): List<Pair<LocalDate, List<Event>>> {
        val by = java.util.TreeMap<LocalDate, MutableList<Event>>()
        for (e in events) for (d in dates(e, zone)) {
            if (from != null && d.isBefore(from)) continue
            if (to != null && !d.isBefore(to)) continue
            by.getOrPut(d) { ArrayList() }.add(e)
        }
        return by.map { (d, l) -> d to l.sortedWith(compareBy({ !it.allDay }, { it.begin }, { it.title })) }
    }

    /** A date's header: "TODAY · Friday 16 October", "TOMORROW · …", the date else. */
    fun header(d: LocalDate, today: LocalDate): String = when (d) {
        today -> "TODAY · " + d.format(longDay)
        today.plusDays(1) -> "TOMORROW · " + d.format(longDay)
        today.minusDays(1) -> "YESTERDAY · " + d.format(longDay)
        else -> d.format(longDay).uppercase(Locale.UK)
    }

    /** HOME's line for what is next: "now, until 14:00", "in 40 min", "at 18:30", "tomorrow 09:00". */
    fun whenNext(e: Event, nowSec: Long, zone: ZoneId): String {
        val b = Instant.ofEpochSecond(e.begin).atZone(zone)
        val today = Instant.ofEpochSecond(nowSec).atZone(zone).toLocalDate()
        return when {
            e.begin <= nowSec && e.end > nowSec -> "now, until " + Instant.ofEpochSecond(e.end).atZone(zone).format(hm)
            e.begin - nowSec in 0..3599 -> "in ${maxOf(1, (e.begin - nowSec + 59) / 60)} min"
            b.toLocalDate() == today -> "at " + b.format(hm)
            b.toLocalDate() == today.plusDays(1) -> "tomorrow " + b.format(hm)
            else -> b.format(dayFmt) + " " + b.format(hm)
        }
    }

    /** The bounds the box is asked for, for one local date: midnight to midnight in the zone, and
     *  wide enough to catch the all-day events kept at UTC midnight. */
    fun dayBounds(d: LocalDate, zone: ZoneId): Pair<Long, Long> {
        val a = d.atStartOfDay(zone).toEpochSecond()
        val b = d.plusDays(1).atStartOfDay(zone).toEpochSecond()
        val ua = d.atStartOfDay(ZoneOffset.UTC).toEpochSecond()
        val ub = d.plusDays(1).atStartOfDay(ZoneOffset.UTC).toEpochSecond()
        return minOf(a, ua) to maxOf(b, ub)
    }

    /** The events a day page shows: timed ones starting that date in the zone, all-day ones
     *  covering it, and a timed one running over from the night before. */
    fun onDay(events: List<Event>, d: LocalDate, zone: ZoneId): List<Event> {
        val a = d.atStartOfDay(zone).toEpochSecond()
        val b = d.plusDays(1).atStartOfDay(zone).toEpochSecond()
        return events.filter { e ->
            if (e.allDay) d in dates(e, zone) else (e.begin < b && (e.end > a || (e.end <= e.begin && e.begin >= a)))
        }.sortedWith(compareBy({ !it.allDay }, { it.begin }))
    }

    /** SETTINGS' line for the last sync. */
    fun synced(m: Meta?, nowSec: Long): String {
        if (m == null) return "the box has no calendar from this phone yet"
        val ago = nowSec - m.at
        val w = when {
            ago < 90 -> "just now"
            ago < 3600 -> "${ago / 60} min ago"
            ago < 86400 -> "${ago / 3600} h ago"
            else -> "${ago / 86400} days ago"
        }
        val cals = m.calendars.size
        return "synced $w · ${m.events} events from $cals calendar" + (if (cals == 1) "" else "s") + " · a year back and a year ahead"
    }
}
