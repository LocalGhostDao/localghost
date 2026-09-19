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
 * moving one every run. The spool is a plain append-only text file, "ts lat lon" per line, capped
 * so a phone without a box for a year does not grow it without bound.
 */
object LocationLog {
    private const val FILE = "location-trail.log"
    private const val PREFS = "lg_location"
    private const val MAX_BYTES = 2_000_000 // ~45k points; the oldest fall off past this
    private const val MIN_MOVE_M = 25.0
    private const val MIN_GAP_S = 3600L
    private const val BATCH = 4000 // points per POST; a batch is ~200KB, far under the box's 16MB cap
    private const val NAME = "localghost.location"
    private const val NOW_NAME = "localghost.location.now"

    data class Point(val ts: Long, val lat: Double, val lon: Double)

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

    /** Append a point unless it is the same place as the last one, recently. Returns whether it
     *  was kept. */
    @Synchronized
    fun record(ctx: Context, pt: Point): Boolean {
        val prev = last(ctx)
        if (prev != null) {
            val moved = FloatArray(1).also {
                Location.distanceBetween(prev.lat, prev.lon, pt.lat, pt.lon, it)
            }[0]
            if (moved < MIN_MOVE_M && pt.ts - prev.ts < MIN_GAP_S) return false
        }
        val f = file(ctx)
        f.appendText("${pt.ts} ${pt.lat} ${pt.lon}\n")
        if (f.length() > MAX_BYTES) trimOldest(f)
        prefs(ctx).edit().putLong("last_ts", pt.ts).putFloat("last_lat", pt.lat.toFloat())
            .putFloat("last_lon", pt.lon.toFloat()).apply()
        return true
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
        return f.readLines().mapNotNull { line ->
            val parts = line.trim().split(' ')
            if (parts.size != 3) return@mapNotNull null
            val ts = parts[0].toLongOrNull() ?: return@mapNotNull null
            val lat = parts[1].toDoubleOrNull() ?: return@mapNotNull null
            val lon = parts[2].toDoubleOrNull() ?: return@mapNotNull null
            Point(ts, lat, lon)
        }
    }

    fun pendingCount(ctx: Context): Int = pending(ctx).size

    /** Drop the points the box has accepted (everything up to and including [upToTs]). */
    @Synchronized
    private fun ack(ctx: Context, upToTs: Long) {
        val f = file(ctx)
        if (!f.exists()) return
        val keep = f.readLines().filter { line ->
            val ts = line.trim().substringBefore(' ').toLongOrNull() ?: return@filter false
            ts > upToTs
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
                if (g != null) return Point(g.time / 1000, g.latitude, g.longitude)
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
        return Point(b.time / 1000, b.latitude, b.longitude)
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

    /** Hand the spool to the box in batches. True when nothing is left waiting. Quietly false when
     *  there is no box, no session, or the box is down , the spool keeps everything. */
    suspend fun flush(ctx: Context): Boolean {
        if (!BoxConfig.isConfigured(ctx) || SessionStore.read(ctx) == null) return false
        while (true) {
            val pts = pending(ctx)
            if (pts.isEmpty()) return true
            val batch = pts.take(BATCH)
            val arr = JSONArray()
            for (pt in batch) arr.put(JSONObject().put("ts", pt.ts).put("lat", pt.lat).put("lon", pt.lon))
            val body = JSONObject().put("source", "phone").put("points", arr)
            val code = try {
                BoxHttp.postJsonCode(ctx, "/v1/locations", body)
            } catch (e: Exception) {
                android.util.Log.w("LocalGhost", "location flush failed: ${e.message}"); return false
            }
            if (code != 202 && code != 200) {
                android.util.Log.w("LocalGhost", "location flush: box answered HTTP $code")
                return false
            }
            ack(ctx, batch.last().ts)
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

    /** The switch went off: no more fixes. What is already in the spool stays until it is flushed
     *  or the person wipes the app; it is theirs. */
    fun stop(ctx: Context) {
        WorkManager.getInstance(ctx).cancelUniqueWork(NAME)
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
            if (LocationLog.geocode(ctx, pt) != null) {
                // The country changed: the lock-screen card should speak the new language now, not
                // at its next rotation tick.
                com.localghost.app.phrases.PhraseSurface.refresh(ctx)
            }
        }
        LocationLog.flush(ctx)
        return Result.success()
    }
}
