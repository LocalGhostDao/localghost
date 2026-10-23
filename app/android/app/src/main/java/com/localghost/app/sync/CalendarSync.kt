package com.localghost.app.sync

import android.Manifest
import android.content.ContentUris
import android.content.Context
import android.content.pm.PackageManager
import android.provider.CalendarContract
import com.localghost.app.net.BoxClient
import com.localghost.app.ui.CalendarText
import java.time.ZoneId
import java.util.concurrent.TimeUnit

/**
 * THE CALENDAR, read here and kept on the box. Android's own calendar store holds every calendar
 * the phone syncs (Google's, Samsung's, an Exchange account's); this reads a year back and a year
 * ahead of it, with each event's guests, and posts the window to the box, which replaces what it
 * held for it. No calendar service is asked anything and nothing is ever written to a calendar.
 * Sent when it changed: the calendar store says when it did (a content trigger), every six hours
 * besides, and on unlock when the last one is a quarter of an hour old.
 */
object CalendarSync {
    const val PERMISSION = Manifest.permission.READ_CALENDAR
    private const val PREFS = "lg_calendar"
    private const val DEVICE = "phone" // one phone per box: a new phone's calendar replaces the old one's window
    private const val YEAR = 366L * 86400L

    fun granted(ctx: Context): Boolean = ctx.checkSelfPermission(PERMISSION) == PackageManager.PERMISSION_GRANTED

    /** Off by the person in SETTINGS › CALENDAR even with the permission there. */
    fun enabled(ctx: Context): Boolean = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getBoolean("on", true)

    fun setEnabled(ctx: Context, on: Boolean) {
        ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putBoolean("on", on).putString("hash", "").apply()
        if (on) schedule(ctx) else {
            val wm = androidx.work.WorkManager.getInstance(ctx)
            wm.cancelUniqueWork("localghost.calendar"); wm.cancelUniqueWork("localghost.calendar.change")
        }
    }

    data class LastRun(val at: Long, val events: Int, val calendars: Int, val error: String, val skipped: Boolean)

    fun lastRun(ctx: Context): LastRun? {
        val p = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        val at = p.getLong("at", 0L)
        if (at <= 0) return null
        return LastRun(at, p.getInt("events", 0), p.getInt("calendars", 0), p.getString("error", "") ?: "", p.getBoolean("skipped", false))
    }

    private fun note(ctx: Context, events: Int, cals: Int, error: String, skipped: Boolean, hash: String?) {
        val e = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit()
            .putLong("at", System.currentTimeMillis() / 1000).putInt("events", events).putInt("calendars", cals)
            .putString("error", error).putBoolean("skipped", skipped)
        if (hash != null) e.putLong("sentAt", System.currentTimeMillis() / 1000).putString("hash", hash)
        e.apply()
    }

    /** Every calendar on the phone, with how many events each has in the window. */
    private fun calendars(ctx: Context): MutableMap<Long, CalendarText.CalInfo> {
        val out = LinkedHashMap<Long, CalendarText.CalInfo>()
        val cols = arrayOf(CalendarContract.Calendars._ID, CalendarContract.Calendars.CALENDAR_DISPLAY_NAME, CalendarContract.Calendars.ACCOUNT_NAME,
            CalendarContract.Calendars.ACCOUNT_TYPE, CalendarContract.Calendars.CALENDAR_COLOR, CalendarContract.Calendars.VISIBLE)
        ctx.contentResolver.query(CalendarContract.Calendars.CONTENT_URI, cols, null, null, null)?.use { c ->
            while (c.moveToNext()) {
                val id = c.getLong(0)
                out[id] = CalendarText.CalInfo(id, c.getString(1) ?: "", c.getString(2) ?: "", c.getString(3) ?: "",
                    CalendarText.colour(if (c.isNull(4)) null else c.getInt(4)), !c.isNull(5) && c.getInt(5) == 1, 0)
            }
        }
        return out
    }

    /** The guests of the given events, by event, in batches the store's query can take. */
    private fun attendees(ctx: Context, ids: Collection<Long>): Map<Long, List<CalendarText.Attendee>> {
        val out = HashMap<Long, MutableList<CalendarText.Attendee>>()
        val cols = arrayOf(CalendarContract.Attendees.EVENT_ID, CalendarContract.Attendees.ATTENDEE_NAME,
            CalendarContract.Attendees.ATTENDEE_EMAIL, CalendarContract.Attendees.ATTENDEE_STATUS)
        ids.toList().chunked(400).forEach { chunk ->
            val sel = CalendarContract.Attendees.EVENT_ID + " IN (" + chunk.joinToString(",") + ")"
            ctx.contentResolver.query(CalendarContract.Attendees.CONTENT_URI, cols, sel, null, null)?.use { c ->
                while (c.moveToNext()) {
                    val l = out.getOrPut(c.getLong(0)) { ArrayList() }
                    if (l.size < 100) l.add(CalendarText.Attendee((c.getString(1) ?: "").take(200), (c.getString(2) ?: "").take(200),
                        CalendarText.answer(if (c.isNull(3)) null else c.getInt(3))))
                }
            }
        }
        return out
    }

    /** The window read: every instance overlapping it (a weekly meeting is one per week). */
    fun read(ctx: Context, fromSec: Long, toSec: Long): Pair<List<CalendarText.CalInfo>, List<CalendarText.Event>> {
        val cals = calendars(ctx)
        val uri = CalendarContract.Instances.CONTENT_URI.buildUpon().also {
            ContentUris.appendId(it, fromSec * 1000); ContentUris.appendId(it, toSec * 1000)
        }.build()
        val cols = arrayOf(CalendarContract.Instances.EVENT_ID, CalendarContract.Instances.CALENDAR_ID, CalendarContract.Instances.TITLE,
            CalendarContract.Instances.EVENT_LOCATION, CalendarContract.Instances.DESCRIPTION, CalendarContract.Instances.BEGIN,
            CalendarContract.Instances.END, CalendarContract.Instances.ALL_DAY, CalendarContract.Instances.EVENT_TIMEZONE,
            CalendarContract.Instances.STATUS, CalendarContract.Instances.SELF_ATTENDEE_STATUS, CalendarContract.Instances.AVAILABILITY,
            CalendarContract.Instances.ORGANIZER)
        data class Row(val id: Long, val cal: Long, val title: String, val loc: String, val notes: String, val begin: Long, val end: Long,
                       val allDay: Boolean, val tz: String, val status: String, val self: String, val busy: Boolean, val org: String)
        val rows = ArrayList<Row>()
        ctx.contentResolver.query(uri, cols, null, null, CalendarContract.Instances.BEGIN + " ASC")?.use { c ->
            fun i(n: Int): Int? = if (c.isNull(n)) null else c.getInt(n)
            while (c.moveToNext()) {
                val begin = c.getLong(5) / 1000
                if (begin < fromSec || begin >= toSec) continue // the box replaces the window by start time
                rows.add(Row(c.getLong(0), c.getLong(1), (c.getString(2) ?: "").take(500), (c.getString(3) ?: "").take(500),
                    (c.getString(4) ?: "").take(4000), begin, c.getLong(6) / 1000, i(7) == 1, c.getString(8) ?: "",
                    CalendarText.eventStatus(i(9)), CalendarText.answer(i(10)), CalendarText.busy(i(11)), c.getString(12) ?: ""))
            }
        }
        val guests = attendees(ctx, rows.map { it.id }.toSet())
        val counts = rows.groupingBy { it.cal }.eachCount()
        val events = rows.map { r ->
            val ci = cals[r.cal]
            CalendarText.Event(r.id, r.cal, ci?.name ?: "", ci?.account ?: "", r.title, r.loc, r.notes, r.begin, r.end, r.allDay, r.tz,
                r.status, r.self, r.busy, r.org, guests[r.id] ?: emptyList())
        }
        return cals.values.map { it.copy(events = counts[it.id] ?: 0) } to events
    }

    data class Result(val events: Int, val removed: Int, val skipped: Boolean, val error: String = "")

    /** Reads the window and sends it when it differs from the last one sent (or that one is a day
     *  old); force sends regardless. */
    suspend fun sync(ctx: Context, force: Boolean = false): Result {
        if (!granted(ctx)) return Result(0, 0, false, "no permission to read the calendar")
        val now = System.currentTimeMillis() / 1000
        val day = now - now % 86400
        val from = day - YEAR
        val to = day + YEAR
        val (cals, events) = try { read(ctx, from, to) } catch (e: Exception) {
            note(ctx, 0, 0, "the calendar store would not read: " + (e.message ?: e.javaClass.simpleName), false, null)
            return Result(0, 0, false, "the calendar store would not read")
        }
        val zone = ZoneId.systemDefault()
        val offset = zone.rules.getOffset(java.time.Instant.now()).totalSeconds
        val body = CalendarText.upload(DEVICE, zone.id, offset, from, to, cals, events)
        val hash = CalendarText.fingerprint(body)
        val p = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        if (!force && hash == p.getString("hash", "") && now - p.getLong("sentAt", 0L) < 86400) {
            note(ctx, events.size, cals.size, "", true, null)
            return Result(events.size, 0, true)
        }
        val r = BoxClient.calendarUpload(ctx, body)
        return if (r != null) {
            note(ctx, r.first, cals.size, "", false, hash)
            Result(r.first, r.second, false)
        } else {
            note(ctx, events.size, cals.size, "the box did not take it", false, null)
            Result(events.size, 0, false, "the box did not take it")
        }
    }

    /** On unlock, when the last read is a quarter of an hour old. */
    suspend fun syncIfDue(ctx: Context) {
        if (!enabled(ctx) || !granted(ctx) || !com.localghost.app.security.BoxConfig.isConfigured(ctx)) return
        val last = lastRun(ctx)
        if (last != null && System.currentTimeMillis() / 1000 - last.at < 900) return
        sync(ctx)
    }

    /** Every six hours, and a run half a minute after the calendar store changes (a content
     *  trigger lasts one change, so each run puts it back). */
    fun schedule(ctx: Context) {
        if (!enabled(ctx) || !granted(ctx)) return
        val wm = androidx.work.WorkManager.getInstance(ctx)
        val periodic = androidx.work.PeriodicWorkRequestBuilder<CalendarWorker>(6, TimeUnit.HOURS)
            .setConstraints(androidx.work.Constraints.Builder().setRequiresBatteryNotLow(true).build()).build()
        wm.enqueueUniquePeriodicWork("localghost.calendar", androidx.work.ExistingPeriodicWorkPolicy.KEEP, periodic)
        watch(ctx)
    }

    fun watch(ctx: Context, policy: androidx.work.ExistingWorkPolicy = androidx.work.ExistingWorkPolicy.KEEP) {
        val c = androidx.work.Constraints.Builder()
            .addContentUriTrigger(CalendarContract.Events.CONTENT_URI, true)
            .addContentUriTrigger(CalendarContract.Attendees.CONTENT_URI, true)
            .setTriggerContentUpdateDelay(30, TimeUnit.SECONDS)
            .setTriggerContentMaxDelay(5, TimeUnit.MINUTES)
            .build()
        val req = androidx.work.OneTimeWorkRequestBuilder<CalendarWorker>().setConstraints(c).build()
        androidx.work.WorkManager.getInstance(ctx).enqueueUniqueWork("localghost.calendar.change", policy, req)
    }
}

/** The background calendar sync: the window, when the permission is there and a box is enrolled. */
class CalendarWorker(ctx: Context, params: androidx.work.WorkerParameters) : androidx.work.CoroutineWorker(ctx, params) {
    override suspend fun doWork(): Result {
        val ctx = applicationContext
        if (!CalendarSync.enabled(ctx) || !CalendarSync.granted(ctx) || !com.localghost.app.security.BoxConfig.isConfigured(ctx)) return Result.success()
        runCatching { CalendarSync.sync(ctx) }
        // the content trigger is spent once it fires; the next watch waits behind this run
        if (triggeredContentUris.isNotEmpty() || triggeredContentAuthorities.isNotEmpty())
            CalendarSync.watch(ctx, androidx.work.ExistingWorkPolicy.APPEND_OR_REPLACE)
        return Result.success()
    }
}
