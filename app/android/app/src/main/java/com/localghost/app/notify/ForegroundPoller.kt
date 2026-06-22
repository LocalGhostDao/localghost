package com.localghost.app.notify

import android.content.Context
import com.localghost.app.net.BoxClient
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlin.coroutines.coroutineContext

object ForegroundPoller {
    private const val INTERVAL_MS = 10_000L
    suspend fun run(ctx: Context) {
        Notifications.ensureChannel(ctx)
        while (coroutineContext.isActive) {
            try { Notifications.postBatch(ctx, BoxClient.pollPending(ctx)) } catch (_: Exception) {}
            delay(INTERVAL_MS)
        }
    }
}
