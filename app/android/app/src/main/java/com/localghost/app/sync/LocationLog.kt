package com.localghost.app.sync

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.location.Geocoder
import android.location.Location
import android.location.LocationManager
import android.os.Build
import android.os.CancellationSignal
import androidx.core.content.ContextCompat
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.ExistingWorkPolicy
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import com.localghost.app.net.BoxHttp
import com.localghost.app.security.BoxConfig
import com.localghost.app.security.SessionStore
import com.localghost.app.settings.AppSettings
import java.io.File
import java.util.Locale
import java.util.concurrent.CountDownLatch
import java.util.concurrent.TimeUnit
import org.json.JSONArray
import org.json.JSONObject

/**
 * The location trail: where the phone was, a point every quarter hour, kept HERE first. The box's
 * map already draws track points (POST /v1/locations, the same shape a watch would send), but
 * nothing on the phone ever sent any; the trail was Google Timeline exports and photo GPS. Now the
 * phone keeps its own, from the day the app is installed , no box needed , and hands the backlog
 * over the moment a box is enrolled and unlocked. A person who never enrols a box has a trail that
 * never leaves the phone.
 *
 * Framework LocationManager only (no Play Services, no library): one fix per worker run, from the
 * fused provider where the OS has one, network otherwise, GPS last. A point is kept when the phone
 * moved at least [MIN_MOVE_M] or [MIN_GAP_S] passed , a parked phone writes a point an hour, a
 * moving one every run. The spool is a plain append-only text file, "ts lat lon [acc]" per line
 * (acc, the fix's error radius in metres, since this build; older lines have three fields), capped
 * so a phone without a box for a year does not grow it without bound.
 *
 * A fix comes with its error radius, and the radius is what tells a cell-tower guess from a GPS
 * position: a COARSE fix (radius over [COARSE_M]) whose circle still contains the last point is
 * not evidence the phone moved , it confirms where it was , so it is not written as a new place;
 * past the hourly gap it is written with the LAST point's coordinates ("still here, as far as the
 * phone can tell"), never its own, or a parked phone on a tower fix wanders two kilometres every
 * hour. A HOPELESS fix (radius over [HOPELESS_M]) is never a position, only such a confirmation.
 * What still gets through (a wrong fix with an honest-looking radius) the trail's rules catch at
 * draw time, on the phone and on the box alike (TrailClean, framed/clean.go).
 */
object LocationLog {
    private const val FILE = "location-trail.log"
    private const val RECENT_FILE = "location-recent.log"
    private const val RECENT_S = 48 * 3600L // how far back the phone can draw on its own
    private const val PREFS = "lg_location"
    private const val MAX_BYTES = 2_000_000 // ~45k points; the oldest fall off past this
    private const val MIN_MOVE_M = 25.0
    private const val MIN_GAP_S = 3600L
    private const val COARSE_M = 200f   // a fix wider than this is a tower or a wifi guess, not a position
    private const val HOPELESS_M = 5000f // and wider than this says nothing about where, only that we are somewhere
    private const val BATCH = 4000 // points per POST; a batch is ~200KB, far under the box's 16MB cap
    private const val NAME = "localghost.location"
    private const val NOW_NAME = "localghost.location.now"

    /** One fix. [acc] is the OS's 68% error radius in metres, 0 when unknown (older spool lines,
     *  points the box hands back). It never leaves the phone: the box gets ts/lat/lon. */
    data class Point(val ts: Long, val lat: Double, val lon: Double, val acc: Float = 0f)

    private fun prefs(ctx: Context) = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
    private fun file(ctx: Context) = File(ctx.filesDir, FILE)

    fun hasPermission(ctx: Context): Boolean =
        granted(ctx, Manifest.permission.ACCESS_FINE_LOCATION) || granted(ctx, Manifest.permission.ACCESS_COARSE_LOCATION)

    /** Background access is what lets the worker take a fix while the app is closed; without it
     *  the trail only grows while the app is on screen, which is honest but thin. */
    fun hasBackground(ctx: Context): Boolean =
        Build.VERSION.SDK_INT < Build.VERSION_CODES.Q || granted(ctx, Manifest.permission.ACCESS_BACKGROUND_LOCATION)

    private fun granted(ctx: Context, p: String) =
        ContextCompat.checkSelfPermission(ctx, p) == PackageManager.PERMISSION_GRANTED

    /** The trail is ON when the person left the switch on and the OS lets us look. */
    fun active(ctx: Context): Boolean = AppSettings.locationTrail(ctx) && hasPermission(ctx)

    // --- the spool ---

    fun last(ctx: Context): Point? {
        val p = prefs(ctx)
        val ts = p.getLong("last_ts", 0L)
        if (ts == 0L) return null
        return Point(ts, p.getFloat("last_lat", 0f).toDouble(), p.getFloat("last_lon", 0f).toDouble())
    }

    /** Append a point unless it is the same place as the last one, recently, or not newer than
     *  the last one at all (a cached fix older than what we already hold is not news). Returns
     *  whether it was kept. Timestamps in the spool are therefore strictly increasing, which is
     *  what lets a batch be acknowledged by its exact timestamps and nothing else. */
    @Synchronized
    fun record(ctx: Context, fix: Point): Boolean {
        var pt = fix
        val prev = last(ctx)
        if (prev != null) {
            if (pt.ts <= prev.ts) return false
            val moved = FloatArray(1).also {
                Location.distanceBetween(prev.lat, prev.lon, pt.lat, pt.lon, it)
            }[0]
            val coarse = pt.acc > COARSE_M
            if (coarse && (moved < pt.acc || pt.acc > HOPELESS_M)) {
                // The circle still holds the last point (or is too wide to say): not a move.
                if (pt.ts - prev.ts < MIN_GAP_S) return false
                pt = Point(pt.ts, prev.lat, prev.lon, pt.acc) // still here, as far as the phone can tell
            } else if (pt.acc > HOPELESS_M) {
                return false // beyond the last point's reach, but "somewhere in a 5 km circle" is not a place
            } else if (moved < MIN_MOVE_M && pt.ts - prev.ts < MIN_GAP_S) {
                return false
            }
        } else if (pt.acc > HOPELESS_M) {
            return false
        }
        val line = "${pt.ts} ${pt.lat} ${pt.lon}" + (if (pt.acc > 0f) " ${pt.acc.toInt()}" else "") + "\n"
        val f = file(ctx)
        f.appendText(line)
        if (f.length() > MAX_BYTES) trimOldest(f)
        // The recent ring keeps a copy the box's ack never removes, so the map can draw today
        // (and yesterday) from the phone alone: the spool empties as it syncs, and without this
        // the last two days would vanish from the map the moment they reached the box.
        val r = File(ctx.filesDir, RECENT_FILE)
        r.appendText(line)
        if (r.length() > 64_000) trimRecent(r, pt.ts)
        prefs(ctx).edit().putLong("last_ts", pt.ts).putFloat("last_lat", pt.lat.toFloat())
            .putFloat("last_lon", pt.lon.toFloat()).apply()
        bumpToday(ctx)
        return true
    }

    /** A spool line, "ts lat lon [acc]"; null for anything else. */
    private fun parseLine(line: String): Point? {
        val parts = line.trim().split(' ')
        if (parts.size != 3 && parts.size != 4) return null
        val ts = parts[0].toLongOrNull() ?: return null
        val lat = parts[1].toDoubleOrNull() ?: return null
        val lon = parts[2].toDoubleOrNull() ?: return null
        val acc = if (parts.size == 4) parts[3].toFloatOrNull() ?: 0f else 0f
        return Point(ts, lat, lon, acc)
    }

    private fun trimRecent(r: File, now: Long) {
        val keep = r.readLines().filter { (it.trim().substringBefore(' ').toLongOrNull() ?: 0L) >= now - RECENT_S }
        r.writeText(if (keep.isEmpty()) "" else keep.joinToString("\n", postfix = "\n"))
    }

    /** The phone's own points from the last [RECENT_S] seconds (synced or not), oldest first ,
     *  what the map draws for today before and beside what the box has. */
    @Synchronized
    fun recent(ctx: Context, sinceTs: Long = System.currentTimeMillis() / 1000 - RECENT_S): List<Point> {
        val r = File(ctx.filesDir, RECENT_FILE)
        if (!r.exists()) return emptyList()
        return r.readLines().mapNotNull { line -> parseLine(line)?.takeIf { it.ts >= sinceTs } }
    }

    private fun trimOldest(f: File) {
        val lines = f.readLines()
        val keep = lines.drop(lines.size / 4)
        f.writeText(keep.joinToString("\n", postfix = "\n"))
    }

    @Synchronized
    fun pending(ctx: Context): List<Point> {
        val f = file(ctx)
        if (!f.exists()) return emptyList()
        return f.readLines().mapNotNull { line -> parseLine(line) }
    }

    fun pendingCount(ctx: Context): Int = pending(ctx).size

    /** Drop exactly the points the box has accepted , by their timestamps, never by position or
     *  range, so a point recorded while the batch was in flight is untouched. */
    @Synchronized
    private fun ack(ctx: Context, sent: Set<Long>) {
        val f = file(ctx)
        if (!f.exists()) return
        val keep = f.readLines().filter { line ->
            val ts = line.trim().substringBefore(' ').toLongOrNull() ?: return@filter false
            ts !in sent
        }
        if (keep.isEmpty()) f.delete() else f.writeText(keep.joinToString("\n", postfix = "\n"))
    }

    // --- one fix ---

    /** One position, or null when the OS has none to give within [timeoutMs]. Blocking: call it
     *  off the main thread. Never throws , a missing permission or a disabled provider is a null. */
    fun sample(ctx: Context, timeoutMs: Long = 25_000): Point? {
        if (!hasPermission(ctx)) return null
        val lm = ctx.getSystemService(Context.LOCATION_SERVICE) as? LocationManager ?: return null
        val fine = granted(ctx, Manifest.permission.ACCESS_FINE_LOCATION)
        val provider = when {
            Build.VERSION.SDK_INT >= Build.VERSION_CODES.S && lm.allProviders.contains(LocationManager.FUSED_PROVIDER) -> LocationManager.FUSED_PROVIDER
            lm.isProviderEnabled(LocationManager.NETWORK_PROVIDER) -> LocationManager.NETWORK_PROVIDER
            fine && lm.isProviderEnabled(LocationManager.GPS_PROVIDER) -> LocationManager.GPS_PROVIDER
            else -> return lastKnown(lm, fine)
        }
        try {
            if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.R) {
                val latch = CountDownLatch(1)
                var got: Location? = null
                val signal = CancellationSignal()
                lm.getCurrentLocation(provider, signal, ContextCompat.getMainExecutor(ctx)) { loc ->
                    got = loc; latch.countDown()
                }
                if (!latch.await(timeoutMs, TimeUnit.MILLISECONDS)) signal.cancel()
                val g = got
                if (g != null) return Point(g.time / 1000, g.latitude, g.longitude, if (g.hasAccuracy()) g.accuracy else 0f)
            }
        } catch (_: SecurityException) {
            return null
        } catch (e: Exception) {
            android.util.Log.w("LocalGhost", "location fix failed: ${e.message}")
        }
        return lastKnown(lm, fine)
    }

    private fun lastKnown(lm: LocationManager, fine: Boolean): Point? {
        var best: Location? = null
        for (p in lm.allProviders) {
            if (!fine && p == LocationManager.GPS_PROVIDER) continue
            val l = try { lm.getLastKnownLocation(p) } catch (_: SecurityException) { null } ?: continue
            if (best == null || l.time > best.time) best = l
        }
        val b = best ?: return null
        // A fix older than two hours says where the phone WAS; the trail wants where it is.
        if (System.currentTimeMillis() - b.time > 2 * 3600_000L) return null
        return Point(b.time / 1000, b.latitude, b.longitude, if (b.hasAccuracy()) b.accuracy else 0f)
    }

    // --- the country the fix is in, for the phrases ---

    /** The country of the last fix, as "CC" plus the epoch seconds it was resolved; null when no fix
     *  was ever geocoded. Read by CountryDetect ahead of the mobile network: a person on hotel Wi-Fi
     *  with a foreign SIM is placed by where the phone IS, not whose network it last saw. */
    fun lastCountry(ctx: Context): Pair<String, Long>? {
        val p = prefs(ctx)
        val cc = p.getString("country", "") ?: ""
        if (cc.length != 2) return null
        return cc to p.getLong("country_ts", 0L)
    }

    /** Resolve the fix's country through the OS geocoder (device-local on most phones, a network
     *  call on some), only when the phone moved far enough for the answer to change. Returns the
     *  country when it is new. */
    fun geocode(ctx: Context, pt: Point): String? {
        val p = prefs(ctx)
        val prevLat = p.getFloat("country_lat", 999f).toDouble()
        val prevLon = p.getFloat("country_lon", 999f).toDouble()
        val prevTs = p.getLong("country_ts", 0L)
        if (prevLat < 900) {
            val moved = FloatArray(1).also { Location.distanceBetween(prevLat, prevLon, pt.lat, pt.lon, it) }[0]
            if (moved < 20_000 && pt.ts - prevTs < 12 * 3600) {
                // Same neighbourhood, same half day: the country did not change. Keep it fresh.
                p.edit().putLong("country_ts", pt.ts).apply()
                return null
            }
        }
        if (!Geocoder.isPresent()) return null
        val cc = try {
            @Suppress("DEPRECATION")
            Geocoder(ctx, Locale.US).getFromLocation(pt.lat, pt.lon, 1)?.firstOrNull()?.countryCode
        } catch (e: Exception) {
            android.util.Log.w("LocalGhost", "geocode failed: ${e.message}"); null
        } ?: return null
        if (cc.length != 2) return null
        val changed = cc.uppercase() != (p.getString("country", "") ?: "")
        p.edit().putString("country", cc.uppercase()).putLong("country_ts", pt.ts)
            .putFloat("country_lat", pt.lat.toFloat()).putFloat("country_lon", pt.lon.toFloat()).apply()
        return if (changed) cc.uppercase() else null
    }

    // --- to the box ---

    /** The trail's source name on the box: "phone-" plus this phone's stable id. The box keys
     *  location_points on (ts, source), so two phones in one archive never overwrite each other's
     *  point at the same second, and a re-sent batch from THIS phone lands on its own rows. */
    fun source(ctx: Context): String {
        val id = com.localghost.app.net.BoxClient.stableId(ctx)
        return if (id.isEmpty()) "phone" else "phone-" + id.take(8)
    }

    /**
     * Hand the spool to the box in batches, oldest first. True when nothing is left waiting.
     * Quietly false when there is no box, no session, or the box is down , the spool keeps
     * everything.
     *
     * NO DUPLICATES, by construction rather than by comparison:
     *   - a point exists once on the phone (record keeps one per 25 m / hour, timestamps strictly
     *     increasing) and is removed from the spool only when the box has said 202 for the batch
     *     it was in (ack, by exact timestamps), so a lost reply re-sends the same points and a
     *     point recorded mid-flight is never dropped as if sent;
     *   - the box stores location_points keyed on (ts, source) with ON CONFLICT DO NOTHING, so a
     *     re-sent batch is absorbed, and the source is per phone, so nothing from another phone
     *     can collide with it;
     *   - a batch that fails leaves everything from it onwards in place; the next flush starts
     *     exactly there. Nothing is ever sent twice AND kept twice.
     */
    suspend fun flush(ctx: Context): Boolean {
        if (!BoxConfig.isConfigured(ctx) || SessionStore.read(ctx) == null) return false
        val src = source(ctx)
        while (true) {
            val pts = pending(ctx)
            if (pts.isEmpty()) return true
            val batch = pts.take(BATCH)
            val arr = JSONArray()
            for (pt in batch) arr.put(JSONObject().put("ts", pt.ts).put("lat", pt.lat).put("lon", pt.lon))
            val body = JSONObject().put("source", src).put("points", arr)
            val code = try {
                BoxHttp.postJsonCode(ctx, "/v1/locations", body)
            } catch (e: Exception) {
                android.util.Log.w("LocalGhost", "location flush failed: ${e.message}"); return false
            }
            if (code != 202 && code != 200) {
                android.util.Log.w("LocalGhost", "location flush: box answered HTTP $code")
                return false
            }
            ack(ctx, batch.mapTo(HashSet()) { it.ts })
            if (batch.size < BATCH) return true
        }
    }

    // --- scheduling ---

    /** A fix every 15 minutes (WorkManager's floor), no network needed to take one. Idempotent. */
    fun schedule(ctx: Context) {
        val request = PeriodicWorkRequestBuilder<LocationWorker>(15, TimeUnit.MINUTES)
            .setConstraints(Constraints.Builder().setRequiresBatteryNotLow(true).build())
            .build()
        WorkManager.getInstance(ctx).enqueueUniquePeriodicWork(NAME, ExistingPeriodicWorkPolicy.UPDATE, request)
    }

    /** schedule() when the trail is on and allowed; a no-op otherwise. */
    fun scheduleIfActive(ctx: Context) {
        if (active(ctx)) schedule(ctx)
    }

    /** The switch went off: no more fixes. What is already in the spool stays until it is flushed
     *  or the person wipes the app; it is theirs. */
    fun stop(ctx: Context) {
        WorkManager.getInstance(ctx).cancelUniqueWork(NAME)
    }

    /** Points recorded since local midnight , the number the settings line shows. */
    fun countToday(ctx: Context): Int {
        val cal = java.util.Calendar.getInstance()
        cal.set(java.util.Calendar.HOUR_OF_DAY, 0); cal.set(java.util.Calendar.MINUTE, 0); cal.set(java.util.Calendar.SECOND, 0)
        val midnight = cal.timeInMillis / 1000
        return prefs(ctx).getInt("today_n", 0).let { n ->
            if (prefs(ctx).getLong("today_from", 0L) == midnight) n else 0
        }
    }

    internal fun bumpToday(ctx: Context) {
        val cal = java.util.Calendar.getInstance()
        cal.set(java.util.Calendar.HOUR_OF_DAY, 0); cal.set(java.util.Calendar.MINUTE, 0); cal.set(java.util.Calendar.SECOND, 0)
        val midnight = cal.timeInMillis / 1000
        val p = prefs(ctx)
        val n = if (p.getLong("today_from", 0L) == midnight) p.getInt("today_n", 0) else 0
        p.edit().putLong("today_from", midnight).putInt("today_n", n + 1).apply()
    }

    /** Take a fix now (the welcome screen just got the permission; the app just opened). */
    fun sampleNow(ctx: Context) {
        val request = OneTimeWorkRequestBuilder<LocationWorker>().build()
        WorkManager.getInstance(ctx).enqueueUniqueWork(NOW_NAME, ExistingWorkPolicy.KEEP, request)
    }
}

/** One run: a fix, a line in the spool, the country for the phrases, and the backlog to the box if
 *  there is one. Cheap and quiet; there is no notification because there is nothing to watch. */
class LocationWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
    override suspend fun doWork(): Result {
        val ctx = applicationContext
        if (!LocationLog.active(ctx)) return Result.success()
        val pt = LocationLog.sample(ctx)
        if (pt != null) {
            LocationLog.record(ctx, pt)
            LocationLog.geocode(ctx, pt)?.let { cc ->
                // The country changed: the lock-screen card should speak the new language now, not
                // at its next rotation tick , and if the phrases are off and this is not home, this
                // is the moment they offer themselves, once.
                com.localghost.app.phrases.PhraseSurface.refresh(ctx)
                com.localghost.app.phrases.PhraseOffer.maybeOffer(ctx, cc)
            }
        }
        LocationLog.flush(ctx)
        return Result.success()
    }
}
