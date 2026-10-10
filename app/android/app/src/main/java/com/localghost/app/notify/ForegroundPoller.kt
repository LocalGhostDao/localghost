package com.localghost.app.notify

import android.content.Context
import com.localghost.app.net.BoxClient
import kotlinx.coroutines.delay
import kotlinx.coroutines.isActive
import kotlin.coroutines.coroutineContext

/** The box's notifications, asked for while the app is in front and unlocked: every half
 *  minute, and only while [live] says there is a session to ask with (on the gate, the PIN and
 *  the welcome there is none, and every ask was a 503 every ten seconds). */
object ForegroundPoller {
    private const val INTERVAL_MS = 30_000L
    private const val IDLE_MS = 5_000L
    suspend fun run(ctx: Context, live: () -> Boolean = { true }) {
        Notifications.ensureChannel(ctx)
        while (coroutineContext.isActive) {
            if (!live()) { delay(IDLE_MS); continue }
            try { Notifications.postBatch(ctx, BoxClient.pollPending(ctx)) } catch (_: Exception) {}
            delay(INTERVAL_MS)
        }
    }
}
