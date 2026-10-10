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
    // v2 (30 Sep 2026): a channel's lock-screen setting is fixed when it is made, so the private
    // one is a new channel and the old is deleted
    const val CHANNEL_ID = "localghost.daemons.private"
    private const val OLD_CHANNEL_ID = "localghost.daemons"
    private const val CHANNEL_NAME = "Daemon alerts"
    private const val GROUP_KEY = "com.localghost.app.DAEMONS"
    private const val SUMMARY_ID = 1
    const val ACTION_MUTE = "com.localghost.app.action.MUTE"

    /**
     * The box's notifications carry its reflections, which are about your life. On a locked phone
     * they showed in full, because Android shows a notification's text on the lock screen unless
     * the person changed the global setting. The channel now asks for PRIVATE on the lock screen,
     * which the system honours whatever the global setting: the public version ("a note from your
     * box") shows until the phone is unlocked.
     */
    fun ensureChannel(ctx: Context) {
        val nm = ctx.getSystemService(NotificationManager::class.java)
        val channel = NotificationChannel(CHANNEL_ID, CHANNEL_NAME, NotificationManager.IMPORTANCE_DEFAULT)
            .apply {
                description = "Reflections and flags from your box"
                lockscreenVisibility = android.app.Notification.VISIBILITY_PRIVATE
            }
        nm.createNotificationChannel(channel)
        runCatching { nm.deleteNotificationChannel(OLD_CHANNEL_ID) }
    }

    /** What a locked phone shows instead: that the box said something, not what. */
    private fun publicVersion(ctx: Context, icon: Int) = NotificationCompat.Builder(ctx, CHANNEL_ID)
        .setSmallIcon(icon)
        .setContentTitle("LocalGhost")
        .setContentText("a note from your box")
        .build()

    fun hasPermission(ctx: Context) =
        ContextCompat.checkSelfPermission(ctx, Manifest.permission.POST_NOTIFICATIONS) ==
            PackageManager.PERMISSION_GRANTED

    private fun mutePI(ctx: Context): PendingIntent = PendingIntent.getBroadcast(
        ctx, 0, Intent(ctx, MuteReceiver::class.java).setAction(ACTION_MUTE),
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)

    /** Tapping a notification opens the app; AFTER the security gate (biometric/PIN as always)
     *  the app goes to the thing it is about (NotifLink): the day on MAP, the memory, NEWS, Box
     *  Status; NOTIFICATIONS when it names nothing. The extra rides the launch intent; unlock
     *  consumes it. */
    private fun tapPI(ctx: Context, item: PendingNotification, reqCode: Int): PendingIntent =
        // its own page when the box gave it an id (the whole of it, and what it is about under
        // it); else straight to where it goes (the day, the memory, NEWS), else by its kind
        tapPI(ctx, if (item.id > 0) "notification:${item.id}" else com.localghost.app.ui.NotifLink.nav(item.link, item.daemonId, item.kind), reqCode)

    /** The summary of several goes to the list of them. */
    private fun tapPI(ctx: Context, nav: String, reqCode: Int): PendingIntent {
        val i = android.content.Intent(ctx, com.localghost.app.MainActivity::class.java).apply {
            action = "com.localghost.app.OPEN_NOTIFICATION"
            putExtra("nav", nav)
            flags = android.content.Intent.FLAG_ACTIVITY_SINGLE_TOP
        }
        return PendingIntent.getActivity(ctx, reqCode, i,
            PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    }

    fun postBatch(ctx: Context, items: List<PendingNotification>) {
        if (!hasPermission(ctx) || items.isEmpty()) return
        val nm = NotificationManagerCompat.from(ctx)
        items.forEach { item ->
            val d = Daemon.from(item.daemonId)
            nm.notify(d.ordinal + 100, NotificationCompat.Builder(ctx, CHANNEL_ID)
                .setContentIntent(tapPI(ctx, item, (item.id % 10000).toInt()))
                .setSmallIcon(d.icon)
                .setColor(d.color)
                .setSubText(d.label)
                .setContentTitle(item.title)
                .setContentText(com.localghost.app.ui.NotifPage.firstLine(item.body))
                .setStyle(NotificationCompat.BigTextStyle().bigText(item.body))
                .setVisibility(NotificationCompat.VISIBILITY_PRIVATE)
                .setPublicVersion(publicVersion(ctx, d.icon))
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
            .setVisibility(NotificationCompat.VISIBILITY_PRIVATE)
            .setPublicVersion(publicVersion(ctx, R.drawable.ic_ghost_notif))
            .setContentIntent(tapPI(ctx, "notifications", 9999))
            .setGroup(GROUP_KEY)
            .setGroupSummary(true)
            .setAutoCancel(true)
            .addAction(0, "MUTE", mutePI(ctx))
            .build())
    }

    /** A newer server release is on the mirror (update/ServerUpdates.kt): once per version. Opens the
     *  app's SETTINGS, where DEPLOY is, after the gate. Nothing private in it: a version number. */
    fun postServerRelease(ctx: Context, version: String, changes: Int) {
        if (!hasPermission(ctx)) return
        val i = android.content.Intent(ctx, com.localghost.app.MainActivity::class.java).apply {
            action = "com.localghost.app.OPEN_NOTIFICATION"
            putExtra("nav", "settings")
            flags = android.content.Intent.FLAG_ACTIVITY_SINGLE_TOP
        }
        val pi = PendingIntent.getActivity(ctx, 7001, i, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
        NotificationManagerCompat.from(ctx).notify(7001, NotificationCompat.Builder(ctx, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_ghost_notif)
            .setContentTitle("LocalGhost server $version is out")
            .setContentText((if (changes > 0) "$changes changes. " else "") + "Deploy it from SETTINGS › SERVER when you choose.")
            .setContentIntent(pi)
            .setAutoCancel(true)
            .build())
    }

    fun cancelAll(ctx: Context) {
        val nm = NotificationManagerCompat.from(ctx)
        Daemon.entries.forEach { nm.cancel(it.ordinal + 100) }
        nm.cancel(SUMMARY_ID)
    }
}
