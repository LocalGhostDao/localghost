package com.localghost.app.sync

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
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
 * moving one every run. The spool is an append-only text file, one point per line, capped so a
 * phone without a box for a year does not grow it without bound. A point is "ts lat lon [acc]"
 * (acc, the fix's error radius in metres), and since 30 Sep 2026 each line is that text SEALED
 * ([TrailSeal], "s1:..."): to a key whose private half is in the box's vault, or on a phone with no
 * box, behind the phone's own unlock ([TrailKeys]). The worker seals with the public half and holds
 * nothing that opens what it wrote; the app reads the last two days again only after a PIN unlock.
 * The last point (the worker compares every fix with it) is sealed to this phone's hardware
 * ([com.localghost.app.security.DeviceSealed]); the country for the phrases stays plain.
 *
 * A fix comes with its error radius, and the radius is what tells a cell-tower guess from a GPS
 * position: a COARSE fix (radius over [COARSE_M]) never moves the trail. It is not evidence the
 * phone moved, only that it is somewhere; past the hourly gap it is written with the LAST point's
 * coordinates ("still here, as far as the phone can tell"), never its own. (Until 2026-09-29 a
 * coarse fix whose circle did not hold the last point was taken as a move: on Paxos the phone
 * latched onto the mainland's towers and the map drew ferry crossings that never happened.)
 * What still gets through (a wrong fix with an honest-looking radius) the trail's rules catch at
 * draw time, on the phone and on the box alike (TrailClean, framed/clean.go).
 */
object LocationLog {
    private const val FILE = "location-trail.log"
    private const val RECENT_FILE = "location-recent.log"
    private const val RECENT_S = 48 * 3600L // how far back the phone can draw on its own
    private const val RING_MAX = 256_000 // the recent ring's cap (a sealed line is ~130 bytes; the age trim runs when the app opens it)
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
    data class Point(val ts: Long, val lat: Double, val lon: Double, val acc: Float = 0f, val via: String = "")

    /** How a point was taken, carried with it to the box ("which one ingested it"): the quarter-
     *  hour fix, a copy of another app's fix, or the fix the app takes when it opens. */
    const val VIA_WORKER = "w"
    const val VIA_PASSIVE = "p"
    const val VIA_APP = "a"

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

    /** The last point kept, sealed to this phone's hardware (readable in the background: the
     *  worker compares each fix with it). */
    fun last(ctx: Context): Point? {
        migratePlainState(ctx)
        val line = com.localghost.app.security.DeviceSealed.open(prefs(ctx).getString("last_sealed", null)) ?: return null
        return parseLine(line)
    }

    /** Whether the phone holds a last point and can open it: "none", "ok", or "unreadable" (a
     *  point is kept but this phone's own hardware key will not open it: a restore from another
     *  phone, or a Keystore that refused; the next fix re-seals it). SETTINGS says which. */
    fun lastState(ctx: Context): String {
        val sealed = prefs(ctx).getString("last_sealed", null)
        if (sealed.isNullOrEmpty()) return "none"
        return if (com.localghost.app.security.DeviceSealed.open(sealed) != null) "ok" else "unreadable"
    }

    /** Before 30 Sep 2026 the last point and the country's reference point were kept in the clear. */
    private fun migratePlainState(ctx: Context) {
        val p = prefs(ctx)
        if (!p.contains("last_ts") && !p.contains("country_lat")) return
        val e = p.edit()
        if (p.contains("last_ts")) {
            val ts = p.getLong("last_ts", 0L)
            if (ts > 0) com.localghost.app.security.DeviceSealed.seal("$ts ${p.getFloat("last_lat", 0f)} ${p.getFloat("last_lon", 0f)} ${p.getFloat("last_acc", 0f).toInt()}")
                ?.let { e.putString("last_sealed", it) }
            e.remove("last_ts").remove("last_lat").remove("last_lon").remove("last_acc")
        }
        if (p.contains("country_lat")) {
            com.localghost.app.security.DeviceSealed.seal("${p.getFloat("country_lat", 999f)} ${p.getFloat("country_lon", 999f)}")
                ?.let { e.putString("country_ref", it) }
            e.remove("country_lat").remove("country_lon")
        }
        e.apply()
    }

    /** A point's text, sealed when there is a key (there nearly always is: [TrailKeys.ensure]
     *  makes one the first time). */
    private fun seal(ctx: Context, plain: String): String {
        val pub = TrailKeys.ensure(ctx) ?: return plain
        val p = prefs(ctx)
        if (!p.getBoolean("plain_sealed", false)) {
            sealPlainLines(ctx) // what was written before there was a key
            p.edit().putBoolean("plain_sealed", true).apply()
        }
        return TrailSeal.seal(pub, plain)
    }

    /** Every plain line of the spool and the ring, sealed to the current key (lines from before
     *  there was one, or from before this build). */
    @Synchronized
    fun sealPlainLines(ctx: Context) {
        val pub = TrailKeys.publicKey(ctx) ?: return
        for (f in listOf(file(ctx), File(ctx.filesDir, RECENT_FILE))) {
            if (!f.exists()) continue
            val lines = f.readLines().filter { it.isNotBlank() }
            if (lines.all { TrailSeal.isSealed(it) }) continue
            val out = lines.mapNotNull { l -> if (TrailSeal.isSealed(l)) l else parseLine(l)?.let { TrailSeal.seal(pub, l.trim()) } }
            f.writeText(if (out.isEmpty()) "" else out.joinToString("\n", postfix = "\n"))
        }
    }

    /** A line of the spool or the ring as a point: sealed lines only with [op] (the app unlocked). */
    private fun readLine(line: String, op: TrailSeal.Opener?): Point? =
        if (TrailSeal.isSealed(line)) op?.open(line.trim())?.let { parseLine(it) } else parseLine(line)

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
            if (coarse) {
                // A tower's or a wifi guess never moves the trail: on an island a phone hops to the
                // mainland's towers, 25 km across the sea and back, and each hop was drawn as a
                // journey. It only says the phone is still somewhere: past the hourly gap, a
                // heartbeat at the LAST place. A real move shows up with the next proper fix.
                if (pt.ts - prev.ts < MIN_GAP_S) return false
                pt = Point(pt.ts, prev.lat, prev.lon, pt.acc, pt.via) // still here, as far as the phone can tell
            } else if (moved < MIN_MOVE_M && pt.ts - prev.ts < MIN_GAP_S) {
                return false
            }
        } else if (pt.acc > HOPELESS_M) {
            return false
        }
        // "ts lat lon [acc [via]]" (TrailLine): the accuracy stays on the phone, the way it was
        // taken goes to the box (secd reads the first three and the fifth)
        val plain = TrailLine.format(pt.ts, pt.lat, pt.lon, pt.acc, pt.via)
        val line = seal(ctx, plain) + "\n"
        val f = file(ctx)
        f.appendText(line)
        if (f.length() > MAX_BYTES) trimOldest(f)
        // The recent ring keeps a copy the box's ack never removes, so the map can draw today
        // (and yesterday) from the phone alone: the spool empties as it syncs, and without this
        // the last two days would vanish from the map the moment they reached the box.
        val r = File(ctx.filesDir, RECENT_FILE)
        r.appendText(line)
        if (r.length() > RING_MAX) trimOldest(r)
        prefs(ctx).edit().putString("last_sealed", com.localghost.app.security.DeviceSealed.seal(plain) ?: "").apply()
        bumpToday(ctx, pt.via)
        return true
    }

    /** A spool line, "ts lat lon [acc]"; null for anything else. */
    private fun parseLine(line: String): Point? =
        TrailLine.parse(line)?.let { Point(it.ts, it.lat, it.lon, it.acc, it.via) }

    /** The phone's own points from the last [RECENT_S] seconds (synced or not), oldest first ,
     *  what the map draws for today before and beside what the box has. Sealed lines open only
     *  while the app is unlocked ([TrailKeys.opener]); then the ring also loses what is older than
     *  two days (the worker, which cannot read a sealed line's time, trims by size alone). */
    @Synchronized
    fun recent(ctx: Context, sinceTs: Long = System.currentTimeMillis() / 1000 - RECENT_S): List<Point> {
        val r = File(ctx.filesDir, RECENT_FILE)
        if (!r.exists()) return emptyList()
        val op = TrailKeys.opener()
        val floor = System.currentTimeMillis() / 1000 - RECENT_S
        val keep = ArrayList<String>()
        val out = ArrayList<Point>()
        var changed = false
        for (line in r.readLines()) {
            if (line.isBlank()) continue
            val pt = readLine(line, op)
            if (op != null) {
                // unlocked: what is too old, or can never open (a key that is gone), leaves the ring
                if (pt == null || pt.ts < floor) { changed = true; continue }
                keep.add(line)
            }
            if (pt != null && pt.ts >= sinceTs) out.add(pt)
        }
        if (changed) r.writeText(if (keep.isEmpty()) "" else keep.joinToString("\n", postfix = "\n"))
        return out
    }

    /** The person said they were not there (a trail question answered no): the points recorded
     *  at these seconds leave the phone's own two days too. Needs the app unlocked (sealed lines);
     *  returns how many went. */
    @Synchronized
    fun forget(ctx: Context, ts: Set<Long>): Int {
        val r = File(ctx.filesDir, RECENT_FILE)
        if (!r.exists() || ts.isEmpty()) return 0
        val op = TrailKeys.opener()
        var gone = 0
        val keep = r.readLines().filter { line ->
            if (line.isBlank()) return@filter false
            val pt = readLine(line, op)
            if (pt != null && pt.ts in ts) { gone++; false } else true
        }
        if (gone > 0) r.writeText(if (keep.isEmpty()) "" else keep.joinToString("\n", postfix = "\n"))
        return gone
    }

    private fun trimOldest(f: File) {
        val lines = f.readLines()
        val keep = lines.drop(lines.size / 4)
        f.writeText(keep.joinToString("\n", postfix = "\n"))
    }

    /** The spool's lines, sealed or not, oldest first. */
    @Synchronized
    private fun pendingLines(ctx: Context): List<String> {
        val f = file(ctx)
        if (!f.exists()) return emptyList()
        return f.readLines().map { it.trim() }.filter { it.isNotEmpty() }
    }

    fun pendingCount(ctx: Context): Int = pendingLines(ctx).size

    /** Drop exactly the lines the box has accepted , by the lines themselves (a sealed line is
     *  unique: a new one-off key each time), never by position or range, so a point recorded while
     *  the batch was in flight is untouched. */
    @Synchronized
    private fun ack(ctx: Context, sent: Set<String>) {
        val f = file(ctx)
        if (!f.exists()) return
        val keep = f.readLines().map { it.trim() }.filter { it.isNotEmpty() && it !in sent }
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

    /**
     * FOLLOW the phone while a screen that shows it is open (the map): fixes every few seconds from
     * the fused provider (else GPS with the fine permission, else the network), each handed to
     * [onFix] on the main thread and to [record], whose own rules (25 m or an hour) keep a still
     * phone from writing a point a second. Returns the function that stops it; the caller stops it
     * the moment the screen goes, so the receiver never runs in the background. A quarter-hour
     * fix is enough for the trail; a map with the phone on it wants where the phone is now.
     */
    fun follow(ctx: Context, onFix: (Point) -> Unit): () -> Unit {
        if (!hasPermission(ctx)) return {}
        val lm = ctx.getSystemService(Context.LOCATION_SERVICE) as? LocationManager ?: return {}
        val fine = granted(ctx, Manifest.permission.ACCESS_FINE_LOCATION)
        val provider = when {
            Build.VERSION.SDK_INT >= Build.VERSION_CODES.S && lm.allProviders.contains(LocationManager.FUSED_PROVIDER) -> LocationManager.FUSED_PROVIDER
            fine && lm.isProviderEnabled(LocationManager.GPS_PROVIDER) -> LocationManager.GPS_PROVIDER
            lm.isProviderEnabled(LocationManager.NETWORK_PROVIDER) -> LocationManager.NETWORK_PROVIDER
            else -> return {}
        }
        val writer = java.util.concurrent.Executors.newSingleThreadExecutor()
        val listener = object : android.location.LocationListener {
            override fun onLocationChanged(loc: Location) {
                val pt = Point(loc.time / 1000, loc.latitude, loc.longitude, if (loc.hasAccuracy()) loc.accuracy else 0f, VIA_APP)
                onFix(pt)
                writer.execute { runCatching { record(ctx, pt) } }
            }
            @Deprecated("Deprecated in Java") override fun onStatusChanged(provider: String?, status: Int, extras: android.os.Bundle?) {}
            override fun onProviderEnabled(provider: String) {}
            override fun onProviderDisabled(provider: String) {}
        }
        try {
            lm.requestLocationUpdates(provider, FOLLOW_EVERY_MS, FOLLOW_MIN_M, listener, android.os.Looper.getMainLooper())
        } catch (_: SecurityException) {
            writer.shutdown()
            return {}
        } catch (e: Exception) {
            android.util.Log.w("LocalGhost", "location updates refused: ${e.message}")
            writer.shutdown()
            return {}
        }
        return {
            runCatching { lm.removeUpdates(listener) }
            writer.shutdown()
        }
    }

    private const val FOLLOW_EVERY_MS = 4_000L
    private const val FOLLOW_MIN_M = 3f

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

    /** Resolve the fix's country on the box (its own Natural Earth polygons, /v1/geo/at), only when
     *  the phone moved far enough for the answer to change. The OS geocoder did this before, and on
     *  most phones that is a network call to Google carrying the fix; nothing outside the box sees
     *  one now. A phone with no box keeps the country it had (CountryDetect then reads the mobile
     *  network, the SIM or the time zone). Returns the country when it is new. */
    suspend fun geocode(ctx: Context, pt: Point): String? {
        migratePlainState(ctx)
        val p = prefs(ctx)
        // where the country was last looked up, sealed like the last point; the country itself is plain
        val ref = com.localghost.app.security.DeviceSealed.open(p.getString("country_ref", null))?.split(' ')
        val prevLat = ref?.getOrNull(0)?.toDoubleOrNull() ?: 999.0
        val prevLon = ref?.getOrNull(1)?.toDoubleOrNull() ?: 999.0
        val prevTs = p.getLong("country_ts", 0L)
        if (prevLat < 900) {
            val moved = FloatArray(1).also { Location.distanceBetween(prevLat, prevLon, pt.lat, pt.lon, it) }[0]
            if (moved < 20_000 && pt.ts - prevTs < 12 * 3600) {
                // Same neighbourhood, same half day: the country did not change. Keep it fresh.
                p.edit().putLong("country_ts", pt.ts).apply()
                return null
            }
        }
        val cc = com.localghost.app.net.BoxClient.countryAt(ctx, pt.lat, pt.lon) ?: return null
        if (cc.length != 2) return null
        val changed = cc.uppercase() != (p.getString("country", "") ?: "")
        p.edit().putString("country", cc.uppercase()).putLong("country_ts", pt.ts)
            .putString("country_ref", com.localghost.app.security.DeviceSealed.seal("${pt.lat} ${pt.lon}") ?: "").apply()
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
        if (!BoxConfig.isConfigured(ctx)) { noteSend(ctx, "no box enrolled: the points stay on this phone"); return false }
        if (SessionStore.read(ctx) == null) { noteSend(ctx, "not sent: no box session (it comes with a PIN unlock)"); return false }
        val src = source(ctx)
        var sent = 0
        while (true) {
            val lines = pendingLines(ctx)
            if (lines.isEmpty()) { noteSend(ctx, if (sent > 0) "sent $sent" else "nothing waiting", ok = true); return true }
            val batch = lines.take(BATCH)
            // sealed lines go as they are, and secd opens them with this phone's key from the vault
            // before anything else on the box sees them; a plain line (from before there was a key)
            // goes as a point
            val arr = JSONArray()
            val sealed = JSONArray()
            for (l in batch) {
                if (TrailSeal.isSealed(l)) sealed.put(l)
                else parseLine(l)?.let { pt ->
                    arr.put(JSONObject().put("ts", pt.ts).put("lat", pt.lat).put("lon", pt.lon).apply { if (pt.via.isNotEmpty()) put("via", pt.via) })
                }
            }
            val body = JSONObject().put("source", src).put("points", arr)
            if (sealed.length() > 0) body.put("sealed", sealed)
            val code = try {
                val (c, answer) = BoxHttp.postJsonCodeBody(ctx, "/v1/locations", body)
                // home's numbers ride along on the answer (HomeCache)
                answer?.optJSONObject("home")?.let { com.localghost.app.net.HomeCache.putSnap(ctx, it) }
                c
            } catch (e: Exception) {
                android.util.Log.w("LocalGhost", "location flush failed: ${e.message}")
                noteSend(ctx, "not sent: the box did not answer (${e.javaClass.simpleName})")
                return false
            }
            if (code == 409) {
                // the box has no key for this phone's trail yet: it is handed over at the next unlock
                android.util.Log.i("LocalGhost", "location flush: the box has no trail key for this phone yet; kept")
                noteSend(ctx, "not sent: the box has no key for this phone's trail yet (handed over at the next PIN unlock)")
                return false
            }
            if (code != 202 && code != 200) {
                android.util.Log.w("LocalGhost", "location flush: box answered HTTP $code")
                noteSend(ctx, "not sent: the box answered HTTP $code")
                return false
            }
            ack(ctx, batch.toHashSet())
            bumpSent(ctx, batch.size)
            sent += batch.size
            if (batch.size < BATCH) { noteSend(ctx, "sent $sent", ok = true); return true }
        }
    }

    /** How the last hand-over to the box went, and when: what SETTINGS › keep the trail shows. */
    data class SendNote(val at: Long, val what: String, val ok: Boolean, val lastOkAt: Long)

    private fun noteSend(ctx: Context, what: String, ok: Boolean = false) {
        val now = System.currentTimeMillis() / 1000
        val e = prefs(ctx).edit().putLong("send_at", now).putString("send_what", what).putBoolean("send_ok", ok)
        if (ok) e.putLong("send_ok_at", now)
        e.apply()
    }

    fun lastSend(ctx: Context): SendNote? {
        val p = prefs(ctx)
        val at = p.getLong("send_at", 0L)
        if (at <= 0) return null
        return SendNote(at, p.getString("send_what", "") ?: "", p.getBoolean("send_ok", false), p.getLong("send_ok_at", 0L))
    }

    /** The newest point this phone can read now: the sealed last point (readable in the
     *  background), else, with the app unlocked, the newest in the recent ring. */
    fun newest(ctx: Context): Point? = last(ctx) ?: recent(ctx).lastOrNull()

    // --- scheduling ---

    /** A fix every 15 minutes (WorkManager's floor), no network needed to take one. Idempotent. */
    fun schedule(ctx: Context) {
        val request = PeriodicWorkRequestBuilder<LocationWorker>(15, TimeUnit.MINUTES)
            .setConstraints(Constraints.Builder().setRequiresBatteryNotLow(true).build())
            .build()
        WorkManager.getInstance(ctx).enqueueUniquePeriodicWork(NAME, ExistingPeriodicWorkPolicy.UPDATE, request)
        // and copies of whatever fixes other apps ask for, at no cost of our own (PassiveFixReceiver)
        PassiveFixReceiver.register(ctx)
    }

    /** schedule() when the trail is on and allowed; a no-op otherwise. */
    fun scheduleIfActive(ctx: Context) {
        if (active(ctx)) schedule(ctx)
    }

    /** The switch went off: no more fixes. What is already in the spool stays until it is flushed
     *  or the person wipes the app; it is theirs. */
    fun stop(ctx: Context) {
        WorkManager.getInstance(ctx).cancelUniqueWork(NAME)
        PassiveFixReceiver.unregister(ctx)
    }

    /** Passive fixes kept since local midnight, for the settings line ("of which N passive"). */
    fun passiveToday(ctx: Context): Int {
        val p = prefs(ctx)
        return if (p.getLong("passive_from", 0L) == localMidnight()) p.getInt("passive_n", 0) else 0
    }

    internal fun notePassive(ctx: Context, n: Int) {
        val p = prefs(ctx)
        val midnight = localMidnight()
        val have = if (p.getLong("passive_from", 0L) == midnight) p.getInt("passive_n", 0) else 0
        p.edit().putLong("passive_from", midnight).putInt("passive_n", have + n).apply()
    }

    private fun localMidnight(): Long {
        val cal = java.util.Calendar.getInstance()
        cal.set(java.util.Calendar.HOUR_OF_DAY, 0); cal.set(java.util.Calendar.MINUTE, 0); cal.set(java.util.Calendar.SECOND, 0)
        return cal.timeInMillis / 1000
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

    internal fun bumpToday(ctx: Context, via: String = "") {
        val midnight = localMidnight()
        val p = prefs(ctx)
        val same = p.getLong("today_from", 0L) == midnight
        val n = if (same) p.getInt("today_n", 0) else 0
        val e = p.edit().putLong("today_from", midnight).putInt("today_n", n + 1)
        for (v in listOf(VIA_WORKER, VIA_PASSIVE, VIA_APP)) {
            val have = if (same) p.getInt("today_via_$v", 0) else 0
            e.putInt("today_via_$v", have + if (v == via) 1 else 0)
        }
        e.apply()
    }

    /** Points kept since local midnight, by how they were taken (VIA_*). */
    fun todayByVia(ctx: Context): Map<String, Int> {
        val p = prefs(ctx)
        if (p.getLong("today_from", 0L) != localMidnight()) return emptyMap()
        return listOf(VIA_WORKER, VIA_PASSIVE, VIA_APP).associateWith { p.getInt("today_via_$it", 0) }.filterValues { it > 0 }
    }

    /** Points the box has taken (acknowledged), since local midnight and ever. */
    private fun bumpSent(ctx: Context, n: Int) {
        val midnight = localMidnight()
        val p = prefs(ctx)
        val today = if (p.getLong("sent_from", 0L) == midnight) p.getInt("sent_today", 0) else 0
        p.edit().putLong("sent_from", midnight).putInt("sent_today", today + n).putLong("sent_total", p.getLong("sent_total", 0L) + n).apply()
    }

    fun sentToday(ctx: Context): Int {
        val p = prefs(ctx)
        return if (p.getLong("sent_from", 0L) == localMidnight()) p.getInt("sent_today", 0) else 0
    }

    fun sentTotal(ctx: Context): Long = prefs(ctx).getLong("sent_total", 0L)


    /** Take a fix now (the welcome screen just got the permission; the app just opened). */
    fun sampleNow(ctx: Context) {
        val request = OneTimeWorkRequestBuilder<LocationWorker>().setInputData(androidx.work.workDataOf("now" to true)).build()
        WorkManager.getInstance(ctx).enqueueUniqueWork(NOW_NAME, ExistingWorkPolicy.KEEP, request)
    }
}

/** One run: a fix, a line in the spool, the country for the phrases, and the backlog to the box if
 *  there is one. Cheap and quiet; there is no notification because there is nothing to watch. */
class LocationWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
    override suspend fun doWork(): Result {
        val ctx = applicationContext
        if (!LocationLog.active(ctx)) return Result.success()
        val pt = LocationLog.sample(ctx)?.copy(via = if (inputData.getBoolean("now", false)) LocationLog.VIA_APP else LocationLog.VIA_WORKER)
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
