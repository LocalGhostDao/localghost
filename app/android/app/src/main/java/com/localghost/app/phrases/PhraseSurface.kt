package com.localghost.app.phrases

import android.app.AlarmManager
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.content.BroadcastReceiver
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.os.SystemClock
import android.widget.RemoteViews
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import com.localghost.app.MainActivity
import com.localghost.app.R
import com.localghost.app.notify.Notifications

/**
 * The glanceable surfaces: the widget (home screen, and the lock screen on Android 16 QPR2+) and
 * the lock-screen card , a silent, public, ongoing notification that every phone shows without
 * unlocking. Both are drawn from the same [Now] by [refresh], and both carry SAY and NEXT actions
 * that work from the lock screen. One inexact alarm re-draws them at the next rotation tick or
 * slot boundary; nothing polls, nothing runs between ticks.
 */
object PhraseSurface {
    const val CHANNEL_ID = "localghost.phrases"
    const val NOTIF_ID = 4712
    const val ACTION_NEXT = "com.localghost.app.phrases.NEXT"
    const val ACTION_SAY = "com.localghost.app.phrases.SAY"
    const val ACTION_REFRESH = "com.localghost.app.phrases.REFRESH"

    fun ensureChannel(ctx: Context) {
        val ch = NotificationChannel(CHANNEL_ID, "Phrase on the lock screen", NotificationManager.IMPORTANCE_LOW).apply {
            description = "The phrase you are likely to need right now, in the language around you. Silent, never vibrates."
            setShowBadge(false)
            lockscreenVisibility = android.app.Notification.VISIBILITY_PUBLIC
        }
        ctx.getSystemService(NotificationManager::class.java).createNotificationChannel(ch)
    }

    /** Redraw every surface from the current moment and schedule the next redraw. Safe to call
     *  from anywhere, any time; idempotent. */
    fun refresh(ctx: Context) {
        val app = ctx.applicationContext
        val now = PhraseNow.resolve(app)
        updateWidgets(app, now)
        if (PhraseState.lockScreenOn(app)) postCard(app, now) else NotificationManagerCompat.from(app).cancel(NOTIF_ID)
        schedule(app, now)
    }

    private fun schedule(ctx: Context, now: Now) {
        val hasWidgets = AppWidgetManager.getInstance(ctx)
            .getAppWidgetIds(ComponentName(ctx, PhraseWidget::class.java)).isNotEmpty()
        val am = ctx.getSystemService(AlarmManager::class.java)
        val pi = broadcast(ctx, ACTION_REFRESH, 0)
        am.cancel(pi)
        if (!hasWidgets && !PhraseState.lockScreenOn(ctx)) return // nothing to keep fresh
        val delay = PhraseNow.nextRefreshDelayMs(now.late)
        // Inexact and allowed while idle: a phrase changing a minute late is fine, a phone woken
        // to the second for it is not.
        am.setAndAllowWhileIdle(AlarmManager.ELAPSED_REALTIME, SystemClock.elapsedRealtime() + delay, pi)
    }

    fun broadcast(ctx: Context, action: String, req: Int): PendingIntent = PendingIntent.getBroadcast(
        ctx, req, Intent(ctx, PhraseReceiver::class.java).setAction(action),
        PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)

    private fun openApp(ctx: Context): PendingIntent {
        val i = Intent(ctx, MainActivity::class.java).apply {
            action = "com.localghost.app.OPEN_PHRASES"
            putExtra("nav", "phrases")
            flags = Intent.FLAG_ACTIVITY_SINGLE_TOP
        }
        return PendingIntent.getActivity(ctx, 4712, i, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT)
    }

    // --- the lock-screen card ---

    private fun postCard(ctx: Context, now: Now) {
        if (!Notifications.hasPermission(ctx)) return
        ensureChannel(ctx)
        val p = now.phrase
        val pack = now.pack
        val b = NotificationCompat.Builder(ctx, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_ghost_notif)
            .setColor(0xFF33FF00.toInt())
            .setOngoing(true)
            .setSilent(true)
            .setOnlyAlertOnce(true)
            .setShowWhen(false)
            .setVisibility(NotificationCompat.VISIBILITY_PUBLIC)
            .setPriority(NotificationCompat.PRIORITY_LOW)
            .setContentIntent(openApp(ctx))
        if (p == null || pack == null) {
            val why = if (now.where.country.isEmpty()) "no network country yet , pick one in PHRASES"
                else "no phrases for ${CountryNames.of(now.where.country)} yet , pick a language in PHRASES"
            b.setSubText("ghost.phrased").setContentTitle("…").setContentText(why)
        } else {
            val local = p.localFor(now.form)
            val say = p.sayItFor(now.form)
            val big = buildString {
                append(say)
                if (p.roman.isNotEmpty()) append("  ·  ").append(p.roman)
                append('\n').append(p.en)
                if (p.note.isNotEmpty()) append('\n').append(p.note)
            }
            b.setSubText(now.headline.lowercase())
                .setContentTitle(local)
                .setContentText("$say  ·  ${p.en}")
                .setStyle(NotificationCompat.BigTextStyle().bigText(big))
                .addAction(0, "SAY", broadcast(ctx, ACTION_SAY, 1))
                .addAction(0, "NEXT", broadcast(ctx, ACTION_NEXT, 2))
        }
        NotificationManagerCompat.from(ctx).notify(NOTIF_ID, b.build())
    }

    // --- the widget ---

    fun updateWidgets(ctx: Context, now: Now = PhraseNow.resolve(ctx)) {
        val awm = AppWidgetManager.getInstance(ctx)
        val ids = awm.getAppWidgetIds(ComponentName(ctx, PhraseWidget::class.java))
        if (ids.isEmpty()) return
        val rv = RemoteViews(ctx.packageName, R.layout.widget_phrase)
        val p = now.phrase
        if (p == null || now.pack == null) {
            rv.setTextViewText(R.id.w_head, "› ghost.phrased")
            rv.setTextViewText(R.id.w_local, if (now.where.country.isEmpty()) "where are we?" else CountryNames.of(now.where.country))
            rv.setTextViewText(R.id.w_say, if (now.where.country.isEmpty()) "no network country yet" else "no phrases for this country yet")
            rv.setTextViewText(R.id.w_en, "tap to choose")
        } else {
            rv.setTextViewText(R.id.w_head, "› ${now.headline.lowercase()} · ${now.pack.nativeName}")
            rv.setTextViewText(R.id.w_local, p.localFor(now.form))
            rv.setTextViewText(R.id.w_say, p.sayItFor(now.form) + (if (p.roman.isNotEmpty()) "  ·  " + p.roman else ""))
            rv.setTextViewText(R.id.w_en, p.en)
        }
        rv.setOnClickPendingIntent(R.id.w_root, openApp(ctx))
        rv.setOnClickPendingIntent(R.id.w_say_btn, broadcast(ctx, ACTION_SAY, 1))
        rv.setOnClickPendingIntent(R.id.w_next_btn, broadcast(ctx, ACTION_NEXT, 2))
        for (id in ids) awm.updateAppWidget(id, rv)
    }

    /** Ask the launcher to place the widget , the button on the PHRASES screen. False when the
     *  launcher does not support pinning (then the person adds it the long way). */
    fun requestPin(ctx: Context): Boolean {
        val awm = AppWidgetManager.getInstance(ctx)
        if (!awm.isRequestPinAppWidgetSupported) return false
        return awm.requestPinAppWidget(ComponentName(ctx, PhraseWidget::class.java), null, null)
    }
}

/** The widget provider: every system callback just redraws from the current moment. */
class PhraseWidget : AppWidgetProvider() {
    override fun onUpdate(context: Context, appWidgetManager: AppWidgetManager, appWidgetIds: IntArray) {
        PhraseSurface.refresh(context)
    }
    override fun onEnabled(context: Context) { PhraseSurface.refresh(context) }
    override fun onDisabled(context: Context) { PhraseSurface.refresh(context) } // drops the alarm if nothing else needs it
}

/** SAY / NEXT from the lock screen and the widget, the refresh alarm, and the system events that
 *  move the clock or the phone: boot, time zone, time set, our own upgrade. */
class PhraseReceiver : BroadcastReceiver() {
    override fun onReceive(context: Context, intent: Intent) {
        when (intent.action) {
            PhraseSurface.ACTION_NEXT -> {
                val now = PhraseNow.resolve(context)
                PhraseState.bumpManualNext(context, now.slotKey)
                PhraseSurface.refresh(context)
            }
            PhraseSurface.ACTION_SAY -> {
                val now = PhraseNow.resolve(context)
                val p = now.phrase ?: return
                val pack = now.pack ?: return
                // The engine binds asynchronously; a receiver that returns at once can have its
                // process reaped before the first syllable. goAsync keeps us alive long enough to
                // warm the engine and hand it the sentence.
                val pr = goAsync()
                PhraseSpeaker.say(context, p.localFor(now.form), pack.ttsTag)
                android.os.Handler(android.os.Looper.getMainLooper()).postDelayed({ pr.finish() }, 6000)
            }
            PhraseSurface.ACTION_REFRESH,
            Intent.ACTION_BOOT_COMPLETED,
            Intent.ACTION_TIMEZONE_CHANGED,
            Intent.ACTION_TIME_CHANGED,
            Intent.ACTION_MY_PACKAGE_REPLACED -> PhraseSurface.refresh(context)
        }
    }
}
