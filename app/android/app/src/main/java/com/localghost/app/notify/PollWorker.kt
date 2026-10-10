package com.localghost.app.notify

import android.content.Context
import androidx.work.CoroutineWorker
import androidx.work.ExistingPeriodicWorkPolicy
import androidx.work.PeriodicWorkRequestBuilder
import androidx.work.WorkManager
import androidx.work.WorkerParameters
import com.localghost.app.net.BoxClient
import java.util.concurrent.TimeUnit

/** 15-min notification poll. Tiny payloads, so no network constraint. */
class PollWorker(ctx: Context, params: WorkerParameters) : CoroutineWorker(ctx, params) {
    override suspend fun doWork(): Result {
        Notifications.ensureChannel(applicationContext)
        // voice notes made while the box was out of reach go now (a few MB each; kept until it has them)
        try { com.localghost.app.voice.VoiceNotes.uploadPending(applicationContext) } catch (_: Exception) {}
        // the box hears what network this phone is on: on Wi-Fi the phone fetches the feeds and
        // the tickers for it, on mobile data or in silence the box fetches for itself
        try { BoxClient.reportNet(applicationContext) } catch (_: Exception) {}
        // the lock-screen card's home brief: the news the box picked and the prices, every quarter
        // hour, while something shows it (the card, a widget); nothing fetched for nothing
        if (com.localghost.app.phrases.PhraseSurface.anythingShowing(applicationContext)) {
            try { com.localghost.app.phrases.HomeBrief.fetch(applicationContext) } catch (_: Exception) {}
        }
        // the phone's certificate, renewed from here too once it is a day old: a phone that only
        // polls in the background keeps its door open as long as its session lasts
        if (com.localghost.app.net.DeviceCert.renewDue(applicationContext)) {
            try { com.localghost.app.net.DeviceCert.rotateIfNeeded(applicationContext) } catch (_: Exception) {}
        }
        return try {
            Notifications.postBatch(applicationContext, BoxClient.pollPending(applicationContext))
            Result.success()
        } catch (e: Exception) { Result.retry() }
    }
    companion object {
        private const val NAME = "localghost.poll"
        fun schedule(ctx: Context) {
            WorkManager.getInstance(ctx).enqueueUniquePeriodicWork(
                NAME, ExistingPeriodicWorkPolicy.KEEP,
                PeriodicWorkRequestBuilder<PollWorker>(15, TimeUnit.MINUTES)
                    .setConstraints(androidx.work.Constraints.Builder().setRequiredNetworkType(androidx.work.NetworkType.CONNECTED).build())
                    .build())
        }
    }
}
