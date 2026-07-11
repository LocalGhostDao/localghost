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

        // Go foreground immediately so the OS lets us keep the CPU + network while locked.
        setForeground(foregroundInfo(0, 0, 0))

        val engine = SyncEngine(applicationContext)
        var doneCount = 0
        var totalCount = 0
        // Progress drives the notification so a long upload shows N/total off-screen, and publishes the
        // same counts as WorkManager progress data so an observing Activity can fill its bar.
        val progress = object : SyncEngine.Progress {
            override fun onStart(kind: MediaKind, total: Int, totalBytes: Long) {
                totalCount = total
                setProgressAsync(androidx.work.Data.Builder()
                    .putInt("done", doneCount).putInt("total", totalCount).build())
            }
            override fun onItemStart(kind: MediaKind, name: String, index: Int, total: Int, size: Long) {}
            override fun onItemBytes(kind: MediaKind, read: Long, size: Long, runBytesSent: Long, speedBps: Double, etaSeconds: Long) {
                try { setForegroundAsync(foregroundInfo(doneCount, totalCount, runBytesSent)) } catch (_: Exception) {}
            }
            override fun onItemDone(kind: MediaKind, sent: Int, total: Int) {
                doneCount = sent; totalCount = total
                setProgressAsync(androidx.work.Data.Builder()
                    .putInt("done", doneCount).putInt("total", totalCount).build())
                try { setForegroundAsync(foregroundInfo(doneCount, totalCount, 0)) } catch (_: Exception) {}
            }
            override fun onDone(result: CommandResult) {}
        }
        return try {
            engine.runCamera(MediaKind.PHOTO, progress)
            engine.runCamera(MediaKind.VIDEO, progress)
            Result.success()
        } catch (e: Exception) {
            android.util.Log.w("LocalGhost", "background sync failed, will retry: ${e.message}")
            Result.retry()
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
                .setConstraints(Constraints.Builder().setRequiredNetworkType(net).build())
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
        fun syncNow(ctx: Context) {
            val request = OneTimeWorkRequestBuilder<SyncWorker>()
                .setExpedited(OutOfQuotaPolicy.RUN_AS_NON_EXPEDITED_WORK_REQUEST)
                .setInputData(androidx.work.Data.Builder().putBoolean(KEY_MANUAL, true).build())
                .build()
            WorkManager.getInstance(ctx).enqueueUniqueWork(
                NOW_NAME, ExistingWorkPolicy.KEEP, request) // KEEP: a tap while one runs does not double it
        }
    }
}
