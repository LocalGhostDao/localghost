package com.localghost.app.sync

import android.Manifest
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.content.pm.PackageManager
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.core.content.ContextCompat
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.ExistingWorkPolicy
import androidx.work.ForegroundInfo
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.OutOfQuotaPolicy
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import com.localghost.app.settings.AppSettings
import java.util.concurrent.TimeUnit
import kotlinx.coroutines.async
import kotlinx.coroutines.coroutineScope

/**
 * Background camera sync. Runs on WorkManager's own threads, NOT the Activity's lifecycleScope, so it
 * keeps going when the screen locks or the app is backgrounded , which is the whole point: a 400MB
 * video upload cannot depend on the user staring at the screen. It promotes itself to a FOREGROUND
 * service (dataSync) with a progress notification so Android does not kill it mid-upload and the user
 * can watch progress from the notification shade.
 *
 * Two entry points: a 15-min periodic schedule(), and syncNow() , an expedited one-shot the SYNC NOW
 * button fires so a manual sync also survives locking the phone.
 */
class SyncWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {

    private val manual: Boolean get() = inputData.getBoolean(KEY_MANUAL, false)

    // WorkManager calls this to promote the worker to a foreground service. Providing it (not just
    // calling setForeground inside doWork) is what makes the ongoing notification appear reliably ,
    // including when the app is in the foreground and for expedited work on Android 12+. Without this
    // override the promotion could be skipped and the notification never showed.
    override suspend fun getForegroundInfo(): ForegroundInfo = foregroundInfo(0, 0, 0)

    override suspend fun doWork(): Result {
        val granted = ContextCompat.checkSelfPermission(
            applicationContext, Manifest.permission.READ_MEDIA_IMAGES
        ) == PackageManager.PERMISSION_GRANTED
        if (!granted) return Result.success()
        if (AppSettings.syncPaused(applicationContext)) {
            android.util.Log.i("LocalGhost", "sync paused by user , skipping (periodic and one-shot both honor this)")
            return Result.success()
        }

        // ONE sync at a time, process-wide. The periodic worker and the one-shot (manual/auto) live in
        // DIFFERENT WorkManager unique-work namespaces, so WorkManager happily runs both concurrently ,
        // two uploaders racing the same cursor, double-uploading, and showing two notifications. If a
        // sync is already running, this one simply yields; the running one covers the work.
        if (!syncRunning.compareAndSet(false, true)) {
            android.util.Log.i("LocalGhost", "sync already running, this worker yields")
            return Result.success()
        }
        try {

        // Go foreground immediately so the OS lets us keep the CPU + network while locked.
        setForeground(foregroundInfo(0, 0, 0))

        val engine = SyncEngine(applicationContext)
        // The worker OWNS every on-screen number and publishes the COMPLETE set on every update ,
        // both kinds' counters plus the byte meter. The old shape (kind + done/total only) forced
        // the Activity to merge partial updates into whatever state it already had, which produced
        // exactly the observed screen: last run's photo numbers frozen mid-bar while this run
        // painted videos (three counters apparently racing), and a byte line permanently stuck at
        // "0KB / measuring…" because worker mode never fed bytes at all , the 9510.6MB total on
        // screen was a stale leftover from a legacy in-Activity run.
        var pDone = 0; var pTotal = 0
        var vDone = 0; var vTotal = 0
        var phaseBytes = 0L; var phaseBytesTotal = 0L
        var speed = 0.0; var eta = 0L
        var lastBytesPub = 0L
        fun publish() {
            setProgressAsync(androidx.work.Data.Builder()
                .putInt("pdone", pDone).putInt("ptotal", pTotal)
                .putInt("vdone", vDone).putInt("vtotal", vTotal)
                .putLong("bytes", phaseBytes).putLong("bytestotal", phaseBytesTotal)
                .putDouble("speed", speed).putLong("eta", eta)
                .build())
        }
        var doneCount = 0
        var totalCount = 0
        // Progress drives the notification so a long upload shows N/total off-screen, and publishes the
        // same counts as WorkManager progress data so an observing Activity can fill its bar.
        val progress = object : SyncEngine.Progress {
            override fun onStart(kind: MediaKind, total: Int, totalBytes: Long) {
                doneCount = 0 // fresh counter per kind , photo counts must not bleed into the video bar
                totalCount = total
                if (kind == MediaKind.PHOTO) {
                    // Photos are phase ONE of a run , this is the run boundary, so the video bar's
                    // numbers from the PREVIOUS run are cleared here instead of lingering mid-fill.
                    pDone = 0; pTotal = total
                    vDone = 0; vTotal = 0
                } else {
                    vDone = 0; vTotal = total
                }
                phaseBytes = 0; phaseBytesTotal = totalBytes; speed = 0.0; eta = 0L
                publish()
                // Show the notification from the SCAN phase onward , previously it only became visible
                // once bytes started flowing, so the checking/skipping stretch looked like nothing was
                // happening at all when the app was backgrounded.
                try { setForegroundAsync(foregroundInfo(doneCount, totalCount, 0)) } catch (_: Exception) {}
            }
            override fun onItemStart(kind: MediaKind, name: String, index: Int, total: Int, size: Long) {
                try { setForegroundAsync(foregroundInfo(doneCount, totalCount, 0)) } catch (_: Exception) {}
            }
            override fun onItemBytes(kind: MediaKind, read: Long, size: Long, runBytesSent: Long, speedBps: Double, etaSeconds: Long) {
                phaseBytes = runBytesSent; speed = speedBps; eta = etaSeconds
                // Byte callbacks fire per chunk; publishing each one churns WorkManager's progress DB
                // for nothing a human can perceive. Two a second is plenty for a smooth meter.
                val now = System.currentTimeMillis()
                if (now - lastBytesPub >= 500) { lastBytesPub = now; publish() }
                try { setForegroundAsync(foregroundInfo(doneCount, totalCount, runBytesSent)) } catch (_: Exception) {}
            }
            override fun onItemDone(kind: MediaKind, sent: Int, total: Int) {
                doneCount = sent; totalCount = total
                if (kind == MediaKind.PHOTO) { pDone = sent; pTotal = total } else { vDone = sent; vTotal = total }
                publish()
                try { setForegroundAsync(foregroundInfo(doneCount, totalCount, 0)) } catch (_: Exception) {}
            }
            override fun onDone(result: CommandResult) {}
        }
        return try {
            // TWO STREAMS , photos and videos concurrently. Their cursors are separate by design,
            // the progress UI is per-kind already, and a second TCP stream is the cheap half of
            // "use the whole link" without touching the contiguity-safe per-kind pipeline.
            if (!NetGuard.uploadsAllowed(applicationContext)) {
                // Belt to the constraint's braces: WorkManager should not have started us on
                // metered without the setting, but expedited paths and OEM quirks exist. A run
                // that begins gated ends immediately, shipping nothing.
                return Result.success()
            }
            coroutineScope {
                val p = async { engine.runCamera(MediaKind.PHOTO, progress) }
                val v = async { engine.runCamera(MediaKind.VIDEO, progress) }
                p.await(); v.await()
            }
            Result.success()
        } catch (e: Exception) {
            android.util.Log.w("LocalGhost", "background sync failed, will retry: ${e.message}")
            // BOUNDED retries. Unbounded Result.retry() turns any persistent per-run exception into
            // an all-day scan/crash/backoff loop , invisible except as battery drain. Three attempts
            // is honest effort; after that, stop and say so. The next scheduled/manual sync starts a
            // fresh attempt count anyway.
            if (runAttemptCount < 3) Result.retry()
            else {
                android.util.Log.w("LocalGhost", "sync failed $runAttemptCount times, giving up until next trigger")
                Result.failure()
            }
        }
        } finally {
            syncRunning.set(false)
        }
    }

    private fun foregroundInfo(done: Int, total: Int, curBytes: Long): ForegroundInfo {
        val ctx = applicationContext
        val nm = ctx.getSystemService(Context.NOTIFICATION_SERVICE) as NotificationManager
        // Two channels: MANUAL is DEFAULT importance so a sync the user just started shows at the top of
        // the shade with the ghost logo; AUTO is MIN importance so the 15-min background syncs are
        // effectively invisible (required foreground notification, but no intrusion).
        val channel = if (manual) CHANNEL_MANUAL else CHANNEL_AUTO
        if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.O) {
            nm.createNotificationChannel(
                NotificationChannel(CHANNEL_MANUAL, "Sync (manual)", NotificationManager.IMPORTANCE_DEFAULT)
            )
            nm.createNotificationChannel(
                NotificationChannel(CHANNEL_AUTO, "Sync (background)", NotificationManager.IMPORTANCE_MIN)
            )
        }
        val text = when {
            total > 0 -> "Uploading to your box… $done / $total"
            else -> "Syncing to your box…"
        }
        // Tapping the notification opens the app.
        val openApp = ctx.packageManager.getLaunchIntentForPackage(ctx.packageName)?.let {
            android.app.PendingIntent.getActivity(ctx, 0, it,
                android.app.PendingIntent.FLAG_IMMUTABLE or android.app.PendingIntent.FLAG_UPDATE_CURRENT)
        }
        val builder = NotificationCompat.Builder(ctx, channel)
            .setContentTitle(if (manual) "LocalGhost , syncing now" else "LocalGhost")
            .setContentText(text)
            .setSmallIcon(com.localghost.app.R.drawable.ic_ghost_notif) // the ghost logo, monochrome notif variant
            .setOngoing(true)
            // Show the moment the upload starts , without this, Android defers foreground-service
            // notifications ~10 seconds, so short syncs looked like they never notified at all.
            // setOngoing above makes it non-swipeable on Android 13 and below; 14+ lets the user
            // swipe ANY foreground notification by system policy , the sync keeps running either
            // way, and the notification returns on the next progress update.
            .setForegroundServiceBehavior(NotificationCompat.FOREGROUND_SERVICE_IMMEDIATE)
            .setSilent(!manual) // manual can make its initial sound/heads-up; auto is always silent
            .setContentIntent(openApp)
        if (total > 0) builder.setProgress(total, done, false)
        val notif: Notification = builder.build()
        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q) {
            ForegroundInfo(NOTIF_ID, notif, android.content.pm.ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
        } else {
            ForegroundInfo(NOTIF_ID, notif)
        }
    }

    companion object {
        /** Process-wide "a sync is running" latch shared by the periodic and one-shot workers. */
        private val syncRunning = java.util.concurrent.atomic.AtomicBoolean(false)
        private const val NAME = "localghost.sync"
        private const val NOW_NAME = "localghost.sync.now"
        private const val CHANNEL_MANUAL = "localghost.sync.manual"
        private const val CHANNEL_AUTO = "localghost.sync.auto"
        private const val KEY_MANUAL = "manual"
        private const val NOTIF_ID = 4711

        /** Schedule (or reschedule) the periodic sync with the current network setting. */
        fun schedule(ctx: Context) {
            val net = if (AppSettings.allowMobileSync(ctx)) NetworkType.CONNECTED else NetworkType.UNMETERED
            val request = PeriodicWorkRequestBuilder<SyncWorker>(15, TimeUnit.MINUTES)
                .setConstraints(Constraints.Builder()
                    .setRequiredNetworkType(net)
                    // A background archive job has no business draining a low battery , it can
                    // always run later. Manual sync (the button) carries no such constraint: an
                    // explicit human decision outranks the heuristic.
                    .setRequiresBatteryNotLow(true)
                    .build())
                // Without an initial delay the FIRST periodic run fires the moment it is scheduled ,
                // i.e. at app start, right alongside the auto-sync one-shot , two simultaneous
                // uploads and two notifications. First periodic run waits a full interval.
                .setInitialDelay(15, TimeUnit.MINUTES)
                .build()
            WorkManager.getInstance(ctx).enqueueUniquePeriodicWork(
                NAME, ExistingPeriodicWorkPolicy.UPDATE, request)
        }

        /**
         * Fire a one-shot sync NOW that survives the screen locking. Expedited so it starts immediately
         * rather than waiting for a WorkManager batch window. This is what SYNC NOW calls instead of an
         * Activity coroutine , the Activity coroutine died the moment the screen locked. manual=true
         * gives it the visible "Sync running" notification with the ghost logo; the periodic sync passes
         * manual=false and stays quiet.
         */
        fun syncNow(ctx: Context, manual: Boolean = true) {
            val request = OneTimeWorkRequestBuilder<SyncWorker>()
                .setExpedited(OutOfQuotaPolicy.RUN_AS_NON_EXPEDITED_WORK_REQUEST)
                .setInputData(androidx.work.Data.Builder().putBoolean(KEY_MANUAL, manual).build())
                .build()
            WorkManager.getInstance(ctx).enqueueUniqueWork(
                NOW_NAME, ExistingWorkPolicy.KEEP, request) // KEEP: a tap while one runs does not double it
        }
    }
}
