package com.localghost.app.sync

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import androidx.core.content.ContextCompat
import androidx.work.Constraints
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.NetworkType
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import com.localghost.app.settings.AppSettings
import java.util.concurrent.TimeUnit

/**
 * 15-min background camera sync. Network constraint depends on the user's setting:
 * Wi-Fi only by default (UNMETERED), or any network (CONNECTED) if they opt into mobile.
 * The constraint is fixed at schedule time, so reschedule() must run when the toggle flips.
 */
class SyncWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {

    override suspend fun doWork(): Result {
        val granted = ContextCompat.checkSelfPermission(
            applicationContext, Manifest.permission.READ_MEDIA_IMAGES
        ) == PackageManager.PERMISSION_GRANTED
        if (!granted) return Result.success()

        val engine = SyncEngine(applicationContext)
        val noop = object : SyncEngine.Progress {
            override fun onStart(kind: MediaKind, total: Int) {}
            override fun onItemStart(kind: MediaKind, name: String, index: Int, total: Int, size: Long) {}
            override fun onItemBytes(kind: MediaKind, read: Long, size: Long) {}
            override fun onItemDone(kind: MediaKind, sent: Int, total: Int) {}
            override fun onDone(result: CommandResult) {}
        }
        return try {
            engine.runCamera(MediaKind.PHOTO, noop)
            engine.runCamera(MediaKind.VIDEO, noop)
            Result.success()
        } catch (e: Exception) { Result.retry() }
    }

    companion object {
        private const val NAME = "localghost.sync"

        /** Schedule (or reschedule) the periodic sync with the current network setting. */
        fun schedule(ctx: Context) {
            val net = if (AppSettings.allowMobileSync(ctx)) NetworkType.CONNECTED else NetworkType.UNMETERED
            val request = PeriodicWorkRequestBuilder<SyncWorker>(15, TimeUnit.MINUTES)
                .setConstraints(Constraints.Builder().setRequiredNetworkType(net).build())
                .build()
            // REPLACE so a flipped setting takes effect immediately.
            WorkManager.getInstance(ctx).enqueueUniquePeriodicWork(
                NAME, ExistingPeriodicWorkPolicy.UPDATE, request)
        }
    }
}
