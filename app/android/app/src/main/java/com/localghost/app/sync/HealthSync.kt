package com.localghost.app.sync

import android.content.Context
import androidx.health.connect.client.HealthConnectClient
import androidx.health.connect.client.permission.HealthPermission
import androidx.health.connect.client.records.ActiveCaloriesBurnedRecord
import androidx.health.connect.client.records.BodyFatRecord
import androidx.health.connect.client.records.DistanceRecord
import androidx.health.connect.client.records.ExerciseSessionRecord
import androidx.health.connect.client.records.FloorsClimbedRecord
import androidx.health.connect.client.records.HeartRateRecord
import androidx.health.connect.client.records.HeartRateVariabilityRmssdRecord
import androidx.health.connect.client.records.OxygenSaturationRecord
import androidx.health.connect.client.records.RespiratoryRateRecord
import androidx.health.connect.client.records.RestingHeartRateRecord
import androidx.health.connect.client.records.SleepSessionRecord
import androidx.health.connect.client.records.StepsRecord
import androidx.health.connect.client.records.Vo2MaxRecord
import androidx.health.connect.client.records.WeightRecord
import androidx.health.connect.client.records.metadata.DataOrigin
import androidx.health.connect.client.request.AggregateGroupByPeriodRequest
import androidx.health.connect.client.request.ReadRecordsRequest
import androidx.health.connect.client.time.TimeRangeFilter
import com.localghost.app.net.BoxClient
import java.time.Instant
import java.time.ZoneId
import java.time.format.DateTimeFormatter
import java.time.temporal.ChronoUnit

/**
 * Reads the phone's Health Connect store , where Samsung Health (and every other health app that
 * plays fair) writes , and ships day-batches to the box: steps, sleep minutes, exercise minutes.
 * Health Connect is on-device; the ONLY network hop this data ever takes is phone -> box over the
 * paired channel, same as photos. The last 7 days each sync: metrics upsert on the box, so
 * refinement is free and re-sync is harmless.
 */
object HealthSync {
    /** Samsung Health's package: its own count wins where it wrote one (see sync). */
    const val SAMSUNG_HEALTH = "com.sec.android.app.shealth"

    val PERMISSIONS = setOf(
        HealthPermission.getReadPermission(StepsRecord::class),
        HealthPermission.getReadPermission(SleepSessionRecord::class),
        HealthPermission.getReadPermission(ExerciseSessionRecord::class),
        HealthPermission.getReadPermission(HeartRateRecord::class),
        HealthPermission.getReadPermission(DistanceRecord::class),
        // ACTIVE calories, the kcal Samsung Health shows: the total Health Connect answers is its
        // own resting estimate (1,564 kcal every day, measured or not), not a measurement
        HealthPermission.getReadPermission(ActiveCaloriesBurnedRecord::class),
        HealthPermission.getReadPermission(FloorsClimbedRecord::class),
        HealthPermission.getReadPermission(WeightRecord::class),
        // what a watch measures at night and at rest (10 Oct 2026): the stages of sleep come
        // with the sleep sessions; these are their own types
        HealthPermission.getReadPermission(RestingHeartRateRecord::class),
        HealthPermission.getReadPermission(HeartRateVariabilityRmssdRecord::class),
        HealthPermission.getReadPermission(OxygenSaturationRecord::class),
        HealthPermission.getReadPermission(RespiratoryRateRecord::class),
        HealthPermission.getReadPermission(Vo2MaxRecord::class),
        HealthPermission.getReadPermission(BodyFatRecord::class),
        // History gate , literal string (the constant arrived in a later client than ours):
        // without it, reads reach only 30 days before the grant, which is exactly the "one month
        // then nothing" the first full-history walk produced.
        "android.permission.health.READ_HEALTH_DATA_HISTORY",
    )

    fun available(ctx: Context): Boolean =
        HealthConnectClient.getSdkStatus(ctx) == HealthConnectClient.SDK_AVAILABLE

    /** How many of the read permissions are granted , the SYNC screen shows n/total. */
    suspend fun grantedCount(ctx: Context): Int = try {
        val granted = HealthConnectClient.getOrCreate(ctx).permissionController.getGrantedPermissions()
        PERMISSIONS.count { it in granted }
    } catch (_: Exception) { 0 }

    suspend fun hasPermissions(ctx: Context): Boolean = try {
        val granted = HealthConnectClient.getOrCreate(ctx).permissionController.getGrantedPermissions()
        granted.containsAll(PERMISSIONS)
    } catch (_: Exception) { false }

    data class SyncResult(val days: Int, val skipped: List<String>, val error: String? = null,
                          val calOnlyDays: Int = 0)

    /** FULL HISTORY , walks back month by month from now, syncing each window, until `emptyStop`
     *  consecutive empty months say the record ends (capped 20 years , if your watch predates
     *  that, congratulations). Each month is its own upload chunk, so memory and the box's 1MB
     *  cap stay honoured no matter how dense a life gets. onProgress gets a short status line. */
    suspend fun syncAll(ctx: Context, onProgress: (String) -> Unit): SyncResult {
        var months = 0
        var shipped = 0
        var empties = 0
        var calOnlyTotal = 0
        val allSkipped = LinkedHashSet<String>()
        val cal = java.util.Calendar.getInstance()
        while (months < 240 && empties < 6) {
            val end = cal.timeInMillis
            cal.add(java.util.Calendar.MONTH, -1)
            val start = cal.timeInMillis
            val fmt = java.text.SimpleDateFormat("MMM yyyy", java.util.Locale.US)
            onProgress("reading ${fmt.format(java.util.Date(start))}…")
            val r = sync(ctx, Instant.ofEpochMilli(start), Instant.ofEpochMilli(end))
            allSkipped.addAll(r.skipped)
            if (r.error != null && shipped == 0 && months == 0) return SyncResult(0, r.skipped, r.error)
            calOnlyTotal += r.calOnlyDays
            if (r.days == 0) empties++ else { empties = 0; shipped += r.days }
            months++
            onProgress("$shipped day(s) shipped · ${months} month(s) walked")
        }
        if (months >= 7 && shipped in 0..35) {
            // TWO different walls, now told apart by evidence. If the filter ate calories-only
            // days, the permission is fine , Health Connect's past holds ONLY Samsung's
            // synthesized BMR line, meaning the real measurements never left Samsung Health.
            // Only when nothing at all came back is the permission the suspect.
            if (calOnlyTotal > 30) {
                allSkipped.add("$calOnlyTotal past day(s) held only synthesized calories , " +
                    "check Samsung Health > Settings > Health Connect: enable sharing for steps/" +
                    "sleep/heart; real history may live only inside Samsung Health")
            } else {
                allSkipped.add("older history locked , tap CONNECT HEALTH again and allow access to past data")
            }
        }
        return SyncResult(shipped, allSkipped.toList())
    }

    /** Read one window and upload. EACH record type is isolated: a denied permission or a
     *  flaky provider skips that type (named in `skipped`) rather than failing the sync , partial
     *  data honestly labelled beats all-or-nothing. */
    suspend fun sync(ctx: Context, fromT: Instant? = null, toT: Instant? = null): SyncResult = try {
        val client = HealthConnectClient.getOrCreate(ctx)
        val zone = ZoneId.systemDefault()
        val fmt = DateTimeFormatter.ofPattern("yyyy-MM-dd")
        val now = toT ?: Instant.now()
        // the window starts at a LOCAL MIDNIGHT: the daily buckets below are cut from its start,
        // and a window that began at the time of day the sync ran cut the days at that hour,
        // filed each under the date it began, and rewrote a day's total at every sync
        val from = fromT ?: now.atZone(zone).toLocalDate().minusDays(7).atStartOfDay(zone).toInstant()
        val range = TimeRangeFilter.between(from, now)
        val days = HashMap<String, HashMap<String, Double>>()
        fun bucket(t: Instant): HashMap<String, Double> {
            val d = t.atZone(zone).toLocalDate().format(fmt)
            return days.getOrPut(d) { HashMap() }
        }
        val skipped = ArrayList<String>()
        suspend fun <T> tryRead(name: String, cls: kotlin.reflect.KClass<T>, use: suspend (List<T>) -> Unit)
            where T : Any, T : androidx.health.connect.client.records.Record {
            try {
                // PAGINATED , Health Connect returns ~1000 records a page; taking only page one
                // silently dropped everything past it. Loop the token until the store runs dry.
                var token: String? = null
                do {
                    val resp = client.readRecords(
                        if (token == null) ReadRecordsRequest(cls, range)
                        else ReadRecordsRequest(cls, range, pageToken = token))
                    use(resp.records)
                    token = resp.pageToken
                } while (!token.isNullOrEmpty())
            } catch (e: Exception) {
                skipped.add(name)
                android.util.Log.w("LocalGhost", "health read $name skipped: ${e.message}")
            }
        }
        // DAILY TOTALS VIA THE AGGREGATE API , the double-counting fix. When the watch AND the
        // phone both write steps, raw record-summing counts both; aggregate() dedupes across data
        // origins with Health Connect's own source-priority rules. One call covers steps,
        // distance, calories, floors and exercise duration, bucketed per local day. If aggregate
        // itself fails (older provider), the raw per-record fallback below still runs , counts
        // may inflate there, which the skipped-list names honestly.
        var aggregated = false
        try {
            val zStart = from.atZone(zone).toLocalDate().atStartOfDay()
            val zEnd = now.atZone(zone).toLocalDateTime()
            val buckets = client.aggregateGroupByPeriod(
                AggregateGroupByPeriodRequest(
                    metrics = setOf(
                        StepsRecord.COUNT_TOTAL,
                        DistanceRecord.DISTANCE_TOTAL,
                        ActiveCaloriesBurnedRecord.ACTIVE_CALORIES_TOTAL,
                        FloorsClimbedRecord.FLOORS_CLIMBED_TOTAL,
                        ExerciseSessionRecord.EXERCISE_DURATION_TOTAL,
                    ),
                    timeRangeFilter = TimeRangeFilter.between(zStart, zEnd),
                    timeRangeSlicer = java.time.Period.ofDays(1)))
            // SAMSUNG HEALTH'S OWN COUNT WINS where it wrote one. Health Connect's total follows its
            // source priority, and a phone that counts steps itself (Health Connect's own
            // counter, a fitness app) can sit above Samsung Health there: the box then got the
            // phone's steps without the watch's, a few per cent under what Samsung Health shows.
            // The same aggregate, Samsung Health's records only, replaces those days' steps,
            // distance and active kcal; a day it did not write keeps Health Connect's total.
            val samsung = try {
                client.aggregateGroupByPeriod(
                    AggregateGroupByPeriodRequest(
                        metrics = setOf(StepsRecord.COUNT_TOTAL, DistanceRecord.DISTANCE_TOTAL, ActiveCaloriesBurnedRecord.ACTIVE_CALORIES_TOTAL),
                        timeRangeFilter = TimeRangeFilter.between(zStart, zEnd),
                        timeRangeSlicer = java.time.Period.ofDays(1),
                        dataOriginFilter = setOf(DataOrigin(SAMSUNG_HEALTH))))
                    .associateBy { it.startTime.toLocalDate().format(fmt) }
            } catch (_: Exception) { emptyMap() }
            buckets.forEach { b ->
                // ZERO IS NOT DATA here , empty buckets return non-null zeros for some metric
                // types (Duration aggregates in particular), and writing them created 7,305
                // phantom "health days" back to 2006: every day had one zero metric, so the
                // six-empty-months stop never fired and the walk ran to its 20-year cap. A day
                // exists only when a POSITIVE value landed.
                val vals = HashMap<String, Double>()
                b.result[StepsRecord.COUNT_TOTAL]?.toDouble()?.takeIf { it > 0 }?.let { vals["steps"] = it }
                b.result[DistanceRecord.DISTANCE_TOTAL]?.inKilometers?.takeIf { it > 0 }?.let { vals["distance_km"] = it }
                b.result[ActiveCaloriesBurnedRecord.ACTIVE_CALORIES_TOTAL]?.inKilocalories?.takeIf { it > 0 }?.let { vals["active_calories"] = it }
                samsung[b.startTime.toLocalDate().format(fmt)]?.let { sb ->
                    sb.result[StepsRecord.COUNT_TOTAL]?.toDouble()?.takeIf { it > 0 }?.let { vals["steps"] = it }
                    sb.result[DistanceRecord.DISTANCE_TOTAL]?.inKilometers?.takeIf { it > 0 }?.let { vals["distance_km"] = it }
                    sb.result[ActiveCaloriesBurnedRecord.ACTIVE_CALORIES_TOTAL]?.inKilocalories?.takeIf { it > 0 }?.let { vals["active_calories"] = it }
                }
                b.result[FloorsClimbedRecord.FLOORS_CLIMBED_TOTAL]?.takeIf { it > 0 }?.let { vals["floors"] = it }
                b.result[ExerciseSessionRecord.EXERCISE_DURATION_TOTAL]?.seconds?.takeIf { it > 0 }
                    ?.let { vals["exercise_minutes"] = it / 60.0 }
                if (vals.isNotEmpty()) {
                    days.getOrPut(b.startTime.toLocalDate().format(fmt)) { HashMap() }.putAll(vals)
                }
            }
            aggregated = true
        } catch (e: Exception) {
            android.util.Log.w("LocalGhost", "aggregate unavailable, raw fallback: ${e.message}")
        }
        if (!aggregated) tryRead("steps", StepsRecord::class) { recs ->
            recs.forEach { r ->
                val m = bucket(r.startTime)
                m["steps"] = (m["steps"] ?: 0.0) + r.count.toDouble()
            }
        }
        tryRead("sleep", SleepSessionRecord::class) { recs ->
            // OVERLAP MERGE , two origins (watch app + phone app) can record the SAME night as two
            // overlapping sessions; naive summing invents extra sleep. Merge intervals first, then
            // bucket each merged block by its END (a night belongs to the day you wake).
            val ivs = recs.map { it.startTime.epochSecond to it.endTime.epochSecond }
                .filter { it.second > it.first }.sortedBy { it.first }
            val merged = ArrayList<Pair<Long, Long>>()
            for (iv in ivs) {
                val last = merged.lastOrNull()
                if (last != null && iv.first <= last.second) {
                    merged[merged.size - 1] = last.first to maxOf(last.second, iv.second)
                } else merged.add(iv)
            }
            merged.forEach { (st, en) ->
                val m = bucket(Instant.ofEpochSecond(en))
                m["sleep_minutes"] = (m["sleep_minutes"] ?: 0.0) + (en - st) / 60.0
            }
            // THE STAGES, where the watch gives them: deep, light, REM and awake minutes, each
            // night's under the day it ends (the day the person wakes), the way the total is
            recs.forEach { r ->
                if (r.stages.isEmpty()) return@forEach
                val m = bucket(r.endTime)
                r.stages.forEach { st ->
                    val minutes = (st.endTime.epochSecond - st.startTime.epochSecond) / 60.0
                    if (minutes <= 0) return@forEach
                    val key = when (st.stage) {
                        SleepSessionRecord.STAGE_TYPE_DEEP -> "sleep_deep_minutes"
                        SleepSessionRecord.STAGE_TYPE_LIGHT -> "sleep_light_minutes"
                        SleepSessionRecord.STAGE_TYPE_REM -> "sleep_rem_minutes"
                        SleepSessionRecord.STAGE_TYPE_AWAKE, SleepSessionRecord.STAGE_TYPE_AWAKE_IN_BED -> "sleep_awake_minutes"
                        else -> return@forEach
                    }
                    m[key] = (m[key] ?: 0.0) + minutes
                }
            }
        }
        if (!aggregated) tryRead("exercise", ExerciseSessionRecord::class) { recs ->
            recs.forEach { r ->
                val m = bucket(r.startTime)
                m["exercise_minutes"] = (m["exercise_minutes"] ?: 0.0) +
                    (r.endTime.epochSecond - r.startTime.epochSecond) / 60.0
            }
        }
        // Heart rate: DAILY avg/min/max into metrics, plus the raw series THINNED to 5-minute
        // buckets as samples , a watch-day is ~1440 readings, thinning keeps a week's upload at a
        // few thousand points while preserving the shape of the day.
        // one reading per five-minute bucket, whichever source wrote it (a watch and a phone both
        // writing gave the same bucket twice, and the box refused the whole batch)
        val hrBuckets = java.util.TreeMap<Long, Double>()
        val hrByDay = HashMap<String, MutableList<Double>>()
        tryRead("heart rate", HeartRateRecord::class) { recs ->
          recs.forEach { r ->
            r.samples.forEach { smp ->
                val d = smp.time.atZone(zone).toLocalDate().format(fmt)
                hrByDay.getOrPut(d) { ArrayList() }.add(smp.beatsPerMinute.toDouble())
                hrBuckets.putIfAbsent(smp.time.epochSecond / 300 * 300, smp.beatsPerMinute.toDouble())
            }
          }
        }
        val hrSamples = hrBuckets.map { (b, v) -> Triple("heart_rate", b, v) }
        hrByDay.forEach { (d, vals) ->
            if (vals.isNotEmpty()) {
                val m = days.getOrPut(d) { HashMap() }
                m["hr_avg"] = vals.average()
                m["hr_min"] = vals.min()
                m["hr_max"] = vals.max()
            }
        }
        if (!aggregated) tryRead("distance", DistanceRecord::class) { recs ->
            recs.forEach { r ->
                val m = bucket(r.startTime)
                m["distance_km"] = (m["distance_km"] ?: 0.0) + r.distance.inKilometers
            }
        }
        if (!aggregated) tryRead("active calories", ActiveCaloriesBurnedRecord::class) { recs ->
            recs.forEach { r ->
                val m = bucket(r.startTime)
                m["active_calories"] = (m["active_calories"] ?: 0.0) + r.energy.inKilocalories
            }
        }
        if (!aggregated) tryRead("floors", FloorsClimbedRecord::class) { recs ->
            recs.forEach { r ->
                val m = bucket(r.startTime)
                m["floors"] = (m["floors"] ?: 0.0) + r.floors
            }
        }
        tryRead("weight", WeightRecord::class) { recs ->
            recs.forEach { r -> bucket(r.time)["weight_kg"] = r.weight.inKilograms }
        }
        // the night's and the rest's numbers: the day's mean of each (a watch writes several),
        // the resting heart rate and the VO2 max as the day's last reading
        val means = HashMap<String, HashMap<String, MutableList<Double>>>()
        fun mean(metric: String, t: Instant, v: Double) {
            if (v.isNaN() || v <= 0) return
            val d = t.atZone(zone).toLocalDate().format(fmt)
            means.getOrPut(metric) { HashMap() }.getOrPut(d) { ArrayList() }.add(v)
        }
        tryRead("resting heart rate", RestingHeartRateRecord::class) { recs ->
            recs.forEach { r -> bucket(r.time)["resting_hr"] = r.beatsPerMinute.toDouble() }
        }
        tryRead("heart rate variability", HeartRateVariabilityRmssdRecord::class) { recs ->
            recs.forEach { r -> mean("hrv_ms", r.time, r.heartRateVariabilityMillis) }
        }
        tryRead("oxygen saturation", OxygenSaturationRecord::class) { recs ->
            recs.forEach { r -> mean("spo2_pct", r.time, r.percentage.value) }
        }
        tryRead("respiratory rate", RespiratoryRateRecord::class) { recs ->
            recs.forEach { r -> mean("resp_rate", r.time, r.rate) }
        }
        tryRead("vo2 max", Vo2MaxRecord::class) { recs ->
            recs.forEach { r -> bucket(r.time)["vo2max"] = r.vo2MillilitersPerMinuteKilogram }
        }
        tryRead("body fat", BodyFatRecord::class) { recs ->
            recs.forEach { r -> bucket(r.time)["body_fat_pct"] = r.percentage.value }
        }
        means.forEach { (metric, byDay) ->
            byDay.forEach { (d, vals) -> days.getOrPut(d) { HashMap() }[metric] = vals.average() }
        }
        // A CONSTANT IS NOT A MEASUREMENT. Health Connect's TOTAL calories is a resting estimate
        // for any day asked (1,564 kcal, every day since 2006, data or no data), so it is not read
        // at all: active kcal is (2 Oct 2026). calOnly stays for the full-history walk's message.
        val calOnly = 0
        when {
            days.isEmpty() && hrSamples.isEmpty() ->
                SyncResult(0, skipped, if (skipped.size >= 8) "every record type failed , re-check permissions" else null).also { noteRun(ctx, it, days) }
            BoxClient.healthUpload(ctx, days, hrSamples) -> SyncResult(days.size, skipped, calOnlyDays = calOnly).also { noteRun(ctx, it, days) }
            else -> SyncResult(0, skipped, "box unreachable , is it unlocked?").also { noteRun(ctx, it, days) }
        }
    } catch (e: Exception) {
        android.util.Log.w("LocalGhost", "health sync: ${e.message}")
        SyncResult(0, emptyList(), e.message ?: "health sync failed")
    }

    // --- the record of the last run, and the daily run ---

    private const val PREFS = "lg_health"

    /** How the last sync went: when, how many days, the newest day, and what went wrong. */
    data class LastRun(val at: Long, val days: Int, val newestDay: String, val error: String, val skipped: String)

    private fun noteRun(ctx: Context, r: SyncResult, days: Map<String, *>) {
        ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit()
            .putLong("at", System.currentTimeMillis() / 1000).putInt("days", r.days)
            .putString("newest", days.keys.maxOrNull() ?: "").putString("error", r.error ?: "")
            .putString("skipped", r.skipped.joinToString(", ")).apply()
    }

    fun lastRun(ctx: Context): LastRun? {
        val p = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        val at = p.getLong("at", 0L)
        if (at <= 0) return null
        return LastRun(at, p.getInt("days", 0), p.getString("newest", "") ?: "", p.getString("error", "") ?: "", p.getString("skipped", "") ?: "")
    }

    /** Ships the last week every six hours, in the background, while the permissions are there
     *  and a box is enrolled. It used to ship only when a button was pressed, so a watch's days
     *  reached the box only on the days the person remembered. */
    fun schedule(ctx: Context) {
        val req = androidx.work.PeriodicWorkRequestBuilder<HealthWorker>(6, java.util.concurrent.TimeUnit.HOURS)
            .setConstraints(androidx.work.Constraints.Builder().setRequiresBatteryNotLow(true).build())
            .build()
        androidx.work.WorkManager.getInstance(ctx).enqueueUniquePeriodicWork("localghost.health", androidx.work.ExistingPeriodicWorkPolicy.KEEP, req)
    }

    /**
     * THE PROBE , what Health Connect ACTUALLY holds, per type, and which app put it there.
     * Written after a watch-and-smart-scale owner saw two metrics land: when the reader is known
     * good, the next honest question is whether the data is even THERE, and the only witness is
     * Health Connect itself. Reports records seen in the last 90 days, the date span, and the
     * source packages , so "Samsung is not sharing sleep" stops being a theory and becomes a
     * line of text. Bounded: 3 pages per type, enough to prove presence without a long walk.
     */
    suspend fun probe(ctx: Context): List<String> {
        val client = try { HealthConnectClient.getOrCreate(ctx) }
                     catch (e: Exception) { return listOf("Health Connect unavailable (${e.message?.take(40)})") }
        val end = Instant.now()
        val start = end.minus(90, ChronoUnit.DAYS)
        val range = TimeRangeFilter.between(start, end)
        val out = ArrayList<String>()
        suspend fun <T> one(label: String, cls: kotlin.reflect.KClass<T>, stamp: (T) -> Instant)
            where T : Any, T : androidx.health.connect.client.records.Record {
            try {
                var token: String? = null
                var n = 0
                var pages = 0
                var oldest: Instant? = null
                var newest: Instant? = null
                val apps = LinkedHashSet<String>()
                // NEWEST FIRST: three pages from the oldest end said when the record STARTS and
                // nothing about whether it stopped (a source that went quiet a fortnight ago
                // looked as healthy as one writing now)
                do {
                    val resp = client.readRecords(
                        if (token == null) ReadRecordsRequest(cls, range, ascendingOrder = false)
                        else ReadRecordsRequest(cls, range, ascendingOrder = false, pageToken = token))
                    resp.records.forEach { r ->
                        n++
                        val t = stamp(r)
                        if (oldest == null || t.isBefore(oldest)) oldest = t
                        if (newest == null || t.isAfter(newest)) newest = t
                        val pkg = r.metadata.dataOrigin.packageName
                        if (pkg.isNotEmpty()) apps.add(pkg.substringAfterLast('.'))
                    }
                    token = resp.pageToken
                    pages++
                } while (!token.isNullOrEmpty() && pages < 3)
                val span = if (oldest != null && newest != null)
                    " " + oldest.toString().take(10) + ".." + newest.toString().take(10) +
                        (if (pages >= 3) " (the newest)" else "") else ""
                val who = if (apps.isEmpty()) "" else " from " + apps.joinToString("/")
                out.add(if (n == 0) "$label: nothing in Health Connect"
                        else "$label: $n${if (pages >= 3) "+" else ""} record(s)$span$who")
            } catch (e: Exception) {
                out.add("$label: not readable (${e.message?.take(40)})")
            }
        }
        one("steps", StepsRecord::class) { it.startTime }
        one("heart rate", HeartRateRecord::class) { it.startTime }
        one("sleep", SleepSessionRecord::class) { it.startTime }
        one("weight", WeightRecord::class) { it.time }
        one("exercise", ExerciseSessionRecord::class) { it.startTime }
        one("active calories", ActiveCaloriesBurnedRecord::class) { it.startTime }
        one("distance", DistanceRecord::class) { it.startTime }
        return out
    }
}

/**
 * THE DIAGNOSTICS, sent to the box. A box with no health data cannot say why from its side;
 * the phone can: whether Health Connect is there, which permissions are granted, what each
 * record type holds (the probe), how the last sync went, whether Samsung Health is installed.
 * Posted after every sync and on request from SETTINGS; the box keeps the last and writes it
 * to its log, so the reason is a line of text wherever the person looks. No measurement is in
 * it, only counts and dates.
 */
object HealthDiag {
    suspend fun build(ctx: Context): org.json.JSONObject {
        val o = org.json.JSONObject().put("at", System.currentTimeMillis() / 1000)
        val sdk = try { HealthConnectClient.getSdkStatus(ctx) } catch (_: Exception) { -1 }
        o.put("sdk", when (sdk) {
            HealthConnectClient.SDK_AVAILABLE -> "available"
            HealthConnectClient.SDK_UNAVAILABLE -> "unavailable"
            HealthConnectClient.SDK_UNAVAILABLE_PROVIDER_UPDATE_REQUIRED -> "provider update required"
            else -> "unknown ($sdk)"
        })
        val granted = try { HealthConnectClient.getOrCreate(ctx).permissionController.getGrantedPermissions() } catch (_: Exception) { emptySet() }
        val short = { p: String -> p.removePrefix("android.permission.health.").lowercase() }
        o.put("granted", org.json.JSONArray(HealthSync.PERMISSIONS.filter { it in granted }.map(short)))
        o.put("missing", org.json.JSONArray(HealthSync.PERMISSIONS.filter { it !in granted }.map(short)))
        o.put("samsungHealth", try { ctx.packageManager.getPackageInfo(HealthSync.SAMSUNG_HEALTH, 0); true } catch (_: Exception) { false })
        val lines = if (sdk == HealthConnectClient.SDK_AVAILABLE) HealthSync.probe(ctx) else emptyList()
        o.put("lines", org.json.JSONArray(lines))
        HealthSync.lastRun(ctx)?.let { r ->
            o.put("lastRun", "${java.text.SimpleDateFormat("yyyy-MM-dd HH:mm", java.util.Locale.UK).format(java.util.Date(r.at * 1000))} · ${r.days} day(s)" +
                (if (r.newestDay.isNotEmpty()) " · newest ${r.newestDay}" else "") + (if (r.error.isNotEmpty()) " · ${r.error}" else "") +
                (if (r.skipped.isNotEmpty()) " · skipped ${r.skipped}" else ""))
        }
        o.put("verdict", HealthVerdict.of(sdk == HealthConnectClient.SDK_AVAILABLE, HealthSync.PERMISSIONS.count { it !in granted }, lines, o.optBoolean("samsungHealth")))
        return o
    }

    /** Build and send; the verdict, or why it could not be sent. */
    suspend fun send(ctx: Context): String {
        val o = build(ctx)
        val ok = BoxClient.healthDiag(ctx, o)
        return (if (ok) "sent to the box · " else "the box did not take it · ") + o.optString("verdict")
    }
}

/** The background health sync: the last seven days, when Health Connect is here and allowed. */
class HealthWorker(ctx: Context, params: androidx.work.WorkerParameters) : androidx.work.CoroutineWorker(ctx, params) {
    override suspend fun doWork(): Result {
        val ctx = applicationContext
        if (!HealthSync.available(ctx) || !com.localghost.app.security.BoxConfig.isConfigured(ctx)) return Result.success()
        if (HealthSync.grantedCount(ctx) == 0) return Result.success()
        HealthSync.sync(ctx)
        // the account of what was readable goes with every run, so the box can say why a day
        // is missing without the person opening SETTINGS
        runCatching { HealthDiag.send(ctx) }
        return Result.success()
    }
}
