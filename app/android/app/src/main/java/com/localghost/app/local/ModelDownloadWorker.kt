package com.localghost.app.local

import android.app.NotificationChannel
import android.app.NotificationManager
import android.content.Context
import android.content.pm.ServiceInfo
import android.os.Build
import androidx.core.app.NotificationCompat
import androidx.work.CoroutineWorker
import androidx.work.Constraints
import androidx.work.ExistingWorkPolicy
import androidx.work.ForegroundInfo
import androidx.work.NetworkType
import androidx.work.OneTimeWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import androidx.work.workDataOf
import com.localghost.app.net.BoxClient
import com.localghost.app.settings.AppSettings

/**
 * Long-running model pull from the box (via secd). Foreground service so it survives the app
 * backgrounding or the screen locking, with a progress notification. Resumable: continues from
 * the existing .part length. Respects the Wi-Fi-only default like camera sync.
 */
class ModelDownloadWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {

    override suspend fun getForegroundInfo(): ForegroundInfo = makeForegroundInfo(0, 0)

    override suspend fun doWork(): Result {
        val id = inputData.getString(KEY_ID) ?: return Result.failure()
        val name = inputData.getString(KEY_NAME) ?: id
        val total = inputData.getLong(KEY_SIZE, 0L)
        val sha = inputData.getString(KEY_SHA)

        setForeground(makeForegroundInfo(0, total, name))

        val part = ModelStore.partFile(applicationContext, id)
        val out = ModelStore.file(applicationContext, id)
        val existing = if (part.exists()) part.length() else 0L

        return try {
            val input = BoxClient.downloadModel(id, existing)
            input.use { ins ->
                java.io.RandomAccessFile(part, "rw").use { raf ->
                    raf.seek(existing)
                    val buf = ByteArray(1 shl 16)
                    var downloaded = existing
                    var lastTick = 0L
                    while (true) {
                        if (isStopped) return Result.retry()   // cancelled or constraint lost → resume later
                        val n = ins.read(buf); if (n < 0) break
                        raf.write(buf, 0, n)
                        downloaded += n
                        val now = System.currentTimeMillis()
                        if (now - lastTick > 500) {             // throttle UI + notification updates
                            lastTick = now
                            setProgress(workDataOf(P_DONE to downloaded, P_TOTAL to total))
                            setForeground(makeForegroundInfo(downloaded, total, name))
                        }
                    }
                }
            }
            if (sha != null && !verify(part, sha)) { part.delete(); return Result.failure() }
            if (!part.renameTo(out)) return Result.failure()
            if (ModelStore.activeId(applicationContext) == null) ModelStore.setActive(applicationContext, id)
            Result.success()
        } catch (e: Exception) {
            Result.retry()   // transient (box unreachable etc.) → WorkManager retries, .part resumes
        }
    }

    private fun makeForegroundInfo(done: Long, total: Long, name: String = "model"): ForegroundInfo {
        ensureChannel(applicationContext)
        val pct = if (total > 0) ((done * 100) / total).toInt() else 0
        val notif = NotificationCompat.Builder(applicationContext, CHANNEL)
            .setContentTitle("Downloading $name from the box")
            .setContentText("$pct%  ·  ${gb(done)} / ${gb(total)}")
            .setSmallIcon(com.localghost.app.R.drawable.ic_ghost_notif)
            .setOngoing(true)
            .setProgress(100, pct, total <= 0)
            .build()
        return if (Build.VERSION.SDK_INT >= Build.VERSION_CODES.Q)
            ForegroundInfo(NOTIF_ID, notif, ServiceInfo.FOREGROUND_SERVICE_TYPE_DATA_SYNC)
        else ForegroundInfo(NOTIF_ID, notif)
    }

    private fun verify(file: java.io.File, sha256: String): Boolean {
        val md = java.security.MessageDigest.getInstance("SHA-256")
        file.inputStream().use { ins ->
            val buf = ByteArray(1 shl 16)
            while (true) { val n = ins.read(buf); if (n < 0) break; md.update(buf, 0, n) }
        }
        return md.digest().joinToString("") { "%02x".format(it) }.equals(sha256, ignoreCase = true)
    }

    companion object {
        const val KEY_ID = "id"; const val KEY_NAME = "name"; const val KEY_SIZE = "size"; const val KEY_SHA = "sha"
        const val P_DONE = "done"; const val P_TOTAL = "total"
        private const val CHANNEL = "localghost.downloads"
        private const val NOTIF_ID = 4242

        fun workName(id: String) = "model-download-$id"

        /** Enqueue a resumable, Wi-Fi-by-default download. Unique per model id (dedupes). */
        fun enqueue(ctx: Context, id: String, name: String, size: Long, sha: String?) {
            val net = if (AppSettings.allowMobileSync(ctx)) NetworkType.CONNECTED else NetworkType.UNMETERED
            val req = OneTimeWorkRequestBuilder<ModelDownloadWorker>()
                .setConstraints(Constraints.Builder().setRequiredNetworkType(net).build())
                .setInputData(workDataOf(KEY_ID to id, KEY_NAME to name, KEY_SIZE to size, KEY_SHA to sha))
                .build()
            WorkManager.getInstance(ctx).enqueueUniqueWork(workName(id), ExistingWorkPolicy.KEEP, req)
        }

        fun cancel(ctx: Context, id: String) =
            WorkManager.getInstance(ctx).cancelUniqueWork(workName(id))

        private fun ensureChannel(ctx: Context) {
            val ch = NotificationChannel(CHANNEL, "Model downloads", NotificationManager.IMPORTANCE_LOW)
            ctx.getSystemService(NotificationManager::class.java).createNotificationChannel(ch)
        }

        private fun gb(b: Long) = "%.1f GB".format(b / 1_000_000_000.0)
    }
}
