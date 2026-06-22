package com.localghost.app.notify

import android.Manifest
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.content.pm.PackageManager
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import androidx.core.content.ContextCompat
import com.localghost.app.R
import com.localghost.app.net.PendingNotification

object Notifications {
    const val CHANNEL_ID = "localghost.daemons"
    private const val CHANNEL_NAME = "Daemon alerts"
    private const val GROUP_KEY = "com.localghost.app.DAEMONS"
    private const val SUMMARY_ID = 1
    const val ACTION_MUTE = "com.localghost.app.action.MUTE"

    fun ensureChannel(ctx: Context) {
        val channel = NotificationChannel(CHANNEL_ID, CHANNEL_NAME, NotificationManager.IMPORTANCE_DEFAULT)
            .apply { description = "Reflections and flags from your box" }
        ctx.getSystemService(NotificationManager::class.java).createNotificationChannel(channel)
    }

    fun hasPermission(ctx: Context) =
        ContextCompat.checkSelfPermission(ctx, Manifest.permission.POST_NOTIFICATIONS) ==
            PackageManager.PERMISSION_GRANTED

    private fun mutePI(ctx: Context): PendingIntent = PendingIntent.getBroadcast(
        ctx, 0, Intent(ctx, MuteReceiver::class.java).setAction(ACTION_MUTE),
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)

    fun postBatch(ctx: Context, items: List<PendingNotification>) {
        if (!hasPermission(ctx) || items.isEmpty()) return
        val nm = NotificationManagerCompat.from(ctx)
        items.forEach { item ->
            val d = Daemon.from(item.daemonId)
            nm.notify(d.ordinal + 100, NotificationCompat.Builder(ctx, CHANNEL_ID)
                .setSmallIcon(d.icon)
                .setColor(d.color)
                .setSubText(d.label)
                .setContentTitle(item.title)
                .setContentText(item.body)
                .setStyle(NotificationCompat.BigTextStyle().bigText(item.body))
                .setGroup(GROUP_KEY)
                .setAutoCancel(true)
                .addAction(0, "MUTE", mutePI(ctx))
                .build())
        }
        val inbox = NotificationCompat.InboxStyle().setSummaryText("${items.size} updates")
        items.forEach { inbox.addLine("${Daemon.from(it.daemonId).label}  ·  ${it.title}") }
        nm.notify(SUMMARY_ID, NotificationCompat.Builder(ctx, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_ghost_notif)
            .setColor(Daemon.WATCHD.color)
            .setContentTitle("LocalGhost")
            .setContentText("${items.size} updates from your box")
            .setStyle(inbox)
            .setGroup(GROUP_KEY)
            .setGroupSummary(true)
            .setAutoCancel(true)
            .addAction(0, "MUTE", mutePI(ctx))
            .build())
    }

    fun cancelAll(ctx: Context) {
        val nm = NotificationManagerCompat.from(ctx)
        Daemon.entries.forEach { nm.cancel(it.ordinal + 100) }
        nm.cancel(SUMMARY_ID)
    }
}
