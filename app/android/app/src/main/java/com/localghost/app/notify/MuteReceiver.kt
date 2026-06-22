package com.localghost.app.notify

import android.content.BroadcastReceiver
import android.content.Context
import android.content.Intent

class MuteReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        if (intent.action == Notifications.ACTION_MUTE) {
            NotifyState.setMuted(context, true)
            Notifications.cancelAll(context)
        }
    }
}
