package com.localghost.app.phrases

import android.app.AlarmManager
import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.appwidget.AppWidgetManager
import android.appwidget.AppWidgetProvider
import android.content.BroadcastReceiver
import android.content.ComponentName
import android.content.Context
import android.content.Intent
import android.os.Build
import android.os.SystemClock
import android.widget.RemoteViews
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import com.localghost.app.MainActivity
import com.localghost.app.R
import com.localghost.app.notify.Notifications
import java.util.Calendar
import org.json.JSONArray
import org.json.JSONObject

/**
 * The glanceable surfaces: the widget (home screen, and the lock screen on Android 16 QPR2+ and
 * One UI 8+), and the lock-screen card , a silent, public, ongoing notification, promoted to a
 * LIVE UPDATE where the phone offers it (Android 16: the status-bar chip, the top of the lock
 * screen, the always-on display; Samsung's Now Bar). The card carries SAY, NEXT and GOT IT, all
 * working without unlocking; pulled open, it shows how to say it, what it means, the aside, the
 * next two phrases and where you are in the language ([expanded]).
 *
 * Both are drawn from a [Snapshot]: the slot's whole phrase list flattened to strings and kept in
 * prefs by [refresh]. A tap on the lock screen wakes a COLD process (the app was killed hours
 * ago), and the old path then parsed eighteen packs, resolved the country and rebuilt the list
 * before it could answer , the pause between NEXT and the card changing. Now NEXT and GOT IT read
 * a few strings from prefs, redraw, and only THEN re-sync from the packs in the background, with
 * the cursor pinned to the card just shown and a redraw of the same card skipped, so nothing
 * blinks. One inexact alarm redraws at the next rotation tick or slot boundary; nothing polls
 * between ticks.
 */
object PhraseSurface {
    const val CHANNEL_ID = "localghost.phrases"
    const val NOTIF_ID = 4712
    const val ACTION_NEXT = "com.localghost.app.phrases.NEXT"
    const val ACTION_SAY = "com.localghost.app.phrases.SAY"
    const val ACTION_GOT_IT = "com.localghost.app.phrases.GOT_IT"
    const val ACTION_REFRESH = "com.localghost.app.phrases.REFRESH"
    private const val PREFS = "lg_phrase_card"

    fun ensureChannel(ctx: Context) {
        // LOW, not MIN: a live update may not live on a MIN channel, and LOW is still silent.
        val ch = NotificationChannel(CHANNEL_ID, "Phrase on the lock screen", NotificationManager.IMPORTANCE_LOW).apply {
            description = "The phrase you are likely to need right now, in the language around you. Silent, never vibrates."
            setShowBadge(false)
            lockscreenVisibility = Notification.VISIBILITY_PUBLIC
        }
        ctx.getSystemService(NotificationManager::class.java).createNotificationChannel(ch)
    }

    // --- the snapshot ---

    /** One phrase, flattened for a surface. [id] and [level] are what GOT IT and the progress
     *  line need; the rest is what gets drawn. */
    class Card(val id: String, val level: Int, val local: String, val say: String, val roman: String, val en: String, val note: String)

    /** Everything a surface needs, with no pack in memory: the slot's ordered phrases, where the
     *  automatic cursor sits, the headline, the voice, and the progress numbers for the expanded
     *  card. [why] is set instead when there is nothing to show (no country, no pack). */
    class Snapshot(
        val slotKey: String,
        val late: Boolean,
        val headline: String,
        val lang: String,     // the language in itself, for the header
        val langCode: String, // the pack code, for the known-phrase keys
        val tts: String,
        val cards: List<Card>,
        val why: String,
        val known: Int = 0,
        val total: Int = 0,
        val band: Int = 1,
    ) {
        /** The card the rotation and the person's NEXT taps point at right now. */
        fun index(ctx: Context, cal: Calendar = Calendar.getInstance()): Int {
            if (cards.isEmpty()) return 0
            return PhraseEngine.cursor(intoSlot(cal), PhraseState.manualNext(ctx, slotKey), cards.size)
        }

        fun intoSlot(cal: Calendar = Calendar.getInstance()): Int =
            PhraseEngine.minutesIntoSlot(cal.get(Calendar.HOUR_OF_DAY), cal.get(Calendar.MINUTE), late)

        /** Pin the cursor on [index]: the manual-NEXT count that makes [index] the current card
         *  given where the automatic rotation sits. What NEXT and GOT IT do after they change the
         *  list, so the card under the thumb is the card that stays. */
        fun pinIndex(ctx: Context, index: Int, cal: Calendar = Calendar.getInstance()) {
            if (cards.isEmpty()) return
            val auto = (intoSlot(cal) / PhraseEngine.ROTATE_MINUTES).coerceAtLeast(0)
            val manual = ((index - auto) % cards.size + cards.size) % cards.size
            PhraseState.setManualNext(ctx, slotKey, manual)
        }

        fun without(id: String): Snapshot = Snapshot(slotKey, late, headline, lang, langCode, tts,
            cards.filter { it.id != id }, why, known + 1, total, band)

        fun toJson(): String {
            val arr = JSONArray()
            for (c in cards) arr.put(JSONObject().put("id", c.id).put("lv", c.level).put("l", c.local).put("s", c.say)
                .put("r", c.roman).put("e", c.en).put("n", c.note))
            return JSONObject().put("k", slotKey).put("late", late).put("h", headline).put("lang", lang).put("code", langCode)
                .put("tts", tts).put("why", why).put("cards", arr).put("known", known).put("total", total).put("band", band).toString()
        }

        companion object {
            fun fromJson(s: String): Snapshot? = runCatching {
                val o = JSONObject(s)
                val arr = o.optJSONArray("cards") ?: JSONArray()
                val cards = (0 until arr.length()).map { i ->
                    val c = arr.getJSONObject(i)
                    Card(c.optString("id"), c.optInt("lv", 1), c.optString("l"), c.optString("s"), c.optString("r"), c.optString("e"), c.optString("n"))
                }
                Snapshot(o.optString("k"), o.optBoolean("late"), o.optString("h"), o.optString("lang"), o.optString("code"),
                    o.optString("tts"), cards, o.optString("why"), o.optInt("known"), o.optInt("total"), o.optInt("band", 1))
            }.getOrNull()

            fun of(now: Now): Snapshot {
                val pack = now.pack
                val pick = now.pick
                if (pack == null || pick == null) {
                    val why = if (now.where.country.isEmpty()) "no network country yet , pick one in PHRASES"
                        else "no phrases for ${CountryNames.of(now.where.country)} yet , pick a language in PHRASES"
                    return Snapshot(now.slotKey, now.late, now.headline, "", "", "", emptyList(), why)
                }
                val cards = pick.list.map { p -> Card(p.id, p.level, p.localFor(now.form), p.sayItFor(now.form), p.roman, p.en, p.note) }
                val learnable = pack.phrases.filter { it.situation != Situation.EMERGENCY }
                return Snapshot(now.slotKey, now.late, now.headline, pack.nativeName, pack.lang, pack.ttsTag, cards, "",
                    learnable.count { it.id in now.known }, learnable.size, now.band)
            }
        }
    }

    private fun prefs(ctx: Context) = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    /** The last snapshot, if it is still this slot's. A slot change means the list is different
     *  (the evening opens with its own greeting), so the surfaces go back to the packs. */
    fun current(ctx: Context, cal: Calendar = Calendar.getInstance()): Snapshot? {
        val s = prefs(ctx).getString("snap", null)?.let { Snapshot.fromJson(it) } ?: return null
        val slot = PhraseEngine.slotFor(cal.get(Calendar.HOUR_OF_DAY), cal.get(Calendar.MINUTE), s.late)
        return if (CountryDetect.slotKey(slot, cal) == s.slotKey) s else null
    }

    /** Redraw every surface from the current moment (parses the packs; not for the UI thread) and
     *  schedule the next redraw. Safe to call from anywhere, any time; idempotent.
     *  [keep] is the id of the card a tap just settled on: the rebuilt list may place it elsewhere
     *  (a card marked known becomes a review card), and the cursor follows it, so a tap on the
     *  lock screen never shows one card and then, a second later, another. */
    fun refresh(ctx: Context, keep: String? = null) {
        val app = ctx.applicationContext
        val now = PhraseNow.resolve(app)
        val snap = Snapshot.of(now)
        if (keep != null) {
            val j = snap.cards.indexOfFirst { it.id == keep }
            if (j >= 0) snap.pinIndex(app, j)
        }
        prefs(app).edit().putString("snap", snap.toJson()).apply()
        draw(app, snap)
        schedule(app, now.late)
    }

    /** What the card last showed in THIS process: a redraw that would post the same card again
     *  is skipped, so the background re-sync after a tap does not blink the notification. A cold
     *  process always posts once (the notification may be gone after a reboot). The widgets are
     *  always redrawn: a launcher callback is often the first draw of a widget just placed. */
    @Volatile private var lastPosted: String = ""

    /** Redraw from the snapshot alone: what a lock-screen tap does first. */
    private fun draw(ctx: Context, snap: Snapshot) {
        val i = snap.index(ctx)
        updateWidgets(ctx, snap, i)
        if (!PhraseState.lockScreenOn(ctx)) {
            NotificationManagerCompat.from(ctx).cancel(NOTIF_ID)
            lastPosted = ""
            return
        }
        val card = snap.cards.getOrNull(i)
        val next = snap.cards.getOrNull((i + 1) % snap.cards.size.coerceAtLeast(1))
        val key = "${snap.slotKey}|$i|${card?.id}|${card?.local}|${next?.id}|${snap.known}|${snap.why}|${PhraseState.liveUpdate(ctx)}"
        if (key == lastPosted) return
        lastPosted = key
        postCard(ctx, snap, i)
    }

    private fun schedule(ctx: Context, late: Boolean) {
        val hasWidgets = AppWidgetManager.getInstance(ctx)
            .getAppWidgetIds(ComponentName(ctx, PhraseWidget::class.java)).isNotEmpty()
        val am = ctx.getSystemService(AlarmManager::class.java)
        val pi = broadcast(ctx, ACTION_REFRESH, 0)
        am.cancel(pi)
        if (!hasWidgets && !PhraseState.lockScreenOn(ctx)) return // nothing to keep fresh
        val delay = PhraseNow.nextRefreshDelayMs(late)
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

    /** Whether the OS lets this app promote its card to a live update (Android 16+, and the person
     *  has not switched it off for LocalGhost in Settings). Null below 16: the concept does not exist. */
    fun canPromote(ctx: Context): Boolean? {
        if (Build.VERSION.SDK_INT < 36) return null
        return runCatching { ctx.getSystemService(NotificationManager::class.java).canPostPromotedNotifications() }.getOrDefault(false)
    }

    /** The settings page where live updates are allowed per app (Android 16+). The action is the
     *  string, not a Settings constant: the constant did not resolve against the SDK the app is
     *  built with, and the string is what the intent carries either way. [openPromotedSettings]
     *  falls back to the app's notification page when the phone has no such screen. */
    fun promotedSettingsIntent(ctx: Context): Intent? {
        if (Build.VERSION.SDK_INT < 36) return null
        return Intent("android.settings.MANAGE_APP_PROMOTED_NOTIFICATIONS")
            .putExtra(Intent.EXTRA_PACKAGE_NAME, ctx.packageName)
    }

    /** Open the live-updates page for this app, or the app's notification settings when the
     *  phone does not have one (an ActivityNotFoundException is the phone saying so). */
    fun openPromotedSettings(ctx: Context) {
        val page = promotedSettingsIntent(ctx)?.addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)
        if (page != null && runCatching { ctx.startActivity(page) }.isSuccess) return
        runCatching {
            ctx.startActivity(Intent(android.provider.Settings.ACTION_APP_NOTIFICATION_SETTINGS)
                .putExtra(android.provider.Settings.EXTRA_APP_PACKAGE, ctx.packageName)
                .addFlags(Intent.FLAG_ACTIVITY_NEW_TASK))
        }
    }

    private fun postCard(ctx: Context, snap: Snapshot, index: Int) {
        if (!Notifications.hasPermission(ctx)) return
        ensureChannel(ctx)
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
        val card = snap.cards.getOrNull(index)
        var chip = ""
        if (card == null) {
            b.setSubText("ghost.phrased").setContentTitle("…").setContentText(snap.why)
        } else {
            chip = card.local
            b.setSubText(snap.headline.lowercase())
                .setContentTitle(card.local)
                .setContentText("${card.say}  ·  ${card.en}")
                .setStyle(NotificationCompat.BigTextStyle().bigText(expanded(snap, index)).setSummaryText(snap.lang))
                .addAction(0, "SAY", broadcast(ctx, ACTION_SAY, 1))
                .addAction(0, "NEXT", broadcast(ctx, ACTION_NEXT, 2))
                .addAction(0, "GOT IT", broadcast(ctx, ACTION_GOT_IT, 3))
        }
        // LIVE UPDATE (Android 16+): the same notification, promoted , a chip in the status bar,
        // the top of the lock screen and the always-on display, Samsung's Now Bar. The OS decides,
        // and only says yes when the person allows live updates for LocalGhost (canPromote).
        // Everything above already meets the rules: ongoing, a title, BigText, no custom views,
        // not colorized, a channel above MIN. Below 16 the compat call is a no-op.
        if (PhraseState.liveUpdate(ctx) && card != null) {
            b.setRequestPromotedOngoing(true).setShortCriticalText(chip.take(24))
        }
        NotificationManagerCompat.from(ctx).notify(NOTIF_ID, b.build())
    }

    /**
     * The card pulled open: how to say it and what it means, the aside, then what comes NEXT and
     * what comes after that (so a glance teaches three phrases, and NEXT is never a surprise), and
     * one line of where you are in the language. Plain text on purpose: a live update on Android
     * 16 accepts BigText and nothing custom, and the same string reads fine on every older phone.
     */
    internal fun expanded(snap: Snapshot, index: Int): String {
        val card = snap.cards.getOrNull(index) ?: return snap.why
        val sb = StringBuilder(320)
        sb.append(card.say)
        if (card.roman.isNotEmpty()) sb.append("  ·  ").append(card.roman)
        sb.append('\n').append(card.en)
        if (card.note.isNotEmpty()) sb.append('\n').append("· ").append(card.note)
        val n = snap.cards.size
        if (n > 1) {
            sb.append('\n')
            val next = snap.cards[(index + 1) % n]
            sb.append('\n').append("next   ").append(next.local).append("  ·  ").append(next.en.lowercase())
            if (n > 2) {
                val then = snap.cards[(index + 2) % n]
                sb.append('\n').append("then   ").append(then.local).append("  ·  ").append(then.en.lowercase())
            }
        }
        if (snap.total > 0) {
            sb.append('\n').append(snap.known).append(" of ").append(snap.total).append(" known")
            sb.append(" · level ").append(snap.band).append(", ").append(Levels.name(snap.band))
            sb.append(" · ").append(index + 1).append('/').append(n).append(' ').append(snap.headline.substringBefore(" ·").lowercase())
        }
        return sb.toString()
    }

    // --- the widget ---

    /** Redraw the widgets from the packs (a launcher callback). */
    fun updateWidgets(ctx: Context) {
        val snap = current(ctx) ?: Snapshot.of(PhraseNow.resolve(ctx))
        updateWidgets(ctx, snap, snap.index(ctx))
    }

    private fun updateWidgets(ctx: Context, snap: Snapshot, index: Int) {
        val awm = AppWidgetManager.getInstance(ctx)
        val ids = awm.getAppWidgetIds(ComponentName(ctx, PhraseWidget::class.java))
        if (ids.isEmpty()) return
        val rv = RemoteViews(ctx.packageName, R.layout.widget_phrase)
        val card = snap.cards.getOrNull(index)
        if (card == null) {
            rv.setTextViewText(R.id.w_head, "› ghost.phrased")
            rv.setTextViewText(R.id.w_local, snap.headline.substringAfter("· ", "where are we?"))
            rv.setTextViewText(R.id.w_say, snap.why.substringBefore(" ,"))
            rv.setTextViewText(R.id.w_en, "tap to choose")
        } else {
            val progress = if (snap.total > 0) " · ${snap.known}/${snap.total}" else ""
            rv.setTextViewText(R.id.w_head, "› ${snap.headline.lowercase()} · ${snap.lang}$progress")
            rv.setTextViewText(R.id.w_local, card.local)
            rv.setTextViewText(R.id.w_say, card.say + (if (card.roman.isNotEmpty()) "  ·  " + card.roman else ""))
            rv.setTextViewText(R.id.w_en, card.en)
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

    // --- the taps ---

    /** NEXT from a surface: answered from the snapshot at once, re-synced from the packs after. */
    fun next(ctx: Context) {
        val app = ctx.applicationContext
        val snap = current(app)
        if (snap == null) {
            refresh(app) // a new slot, or nothing drawn yet: the full path
            return
        }
        PhraseState.bumpManualNext(app, snap.slotKey)
        draw(app, snap)
        val shown = snap.cards.getOrNull(snap.index(app))?.id
        Thread { refresh(app, keep = shown) }.start() // same result, from the source of truth; keeps the alarm honest
    }

    /**
     * GOT IT from a surface: this phrase is known. It leaves the walk at once (the next card slides
     * under the thumb, no pause), is counted in the progress line, and comes back only as a
     * review. The greeting is the one card that stays even when known , it opens every slot , so
     * GOT IT on it marks it and moves on. Undo is a tap on the ✓ in the PHRASES list.
     */
    fun gotIt(ctx: Context) {
        val app = ctx.applicationContext
        val snap = current(app)
        val i = snap?.index(app) ?: 0
        val card = snap?.cards?.getOrNull(i)
        if (snap == null || card == null) {
            refresh(app)
            return
        }
        PhraseState.setKnown(app, snap.langCode, card.id, true)
        val after: Snapshot
        val at: Int
        if (i == 0 || snap.cards.size <= 1) { // the greeting, or the last card standing: just advance
            after = Snapshot(snap.slotKey, snap.late, snap.headline, snap.lang, snap.langCode, snap.tts, snap.cards, snap.why,
                snap.known + 1, snap.total, snap.band)
            at = if (snap.cards.size > 1) 1 else 0
        } else {
            after = snap.without(card.id)
            at = if (i < after.cards.size) i else 0
        }
        after.pinIndex(app, at)
        prefs(app).edit().putString("snap", after.toJson()).apply()
        draw(app, after)
        val shown = after.cards.getOrNull(at)?.id
        Thread { refresh(app, keep = shown) }.start()
    }

    /** SAY from a surface: the current card's words, from the snapshot, into the voice engine. */
    fun say(ctx: Context): Boolean {
        val app = ctx.applicationContext
        val snap = current(app) ?: Snapshot.of(PhraseNow.resolve(app))
        val card = snap.cards.getOrNull(snap.index(app)) ?: return false
        PhraseSpeaker.say(app, card.local, snap.tts)
        return true
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
            PhraseSurface.ACTION_NEXT -> PhraseSurface.next(context)
            PhraseSurface.ACTION_GOT_IT -> PhraseSurface.gotIt(context)
            PhraseOffer.ACTION_ACCEPT -> PhraseOffer.accept(context)
            PhraseOffer.ACTION_DECLINE -> PhraseOffer.decline(context)
            PhraseSurface.ACTION_SAY -> {
                // The engine binds asynchronously; a receiver that returns at once can have its
                // process reaped before the first syllable. goAsync keeps us alive long enough to
                // warm the engine and hand it the sentence , and keeps the process warm for the
                // NEXT that usually follows.
                val pr = goAsync()
                if (!PhraseSurface.say(context)) { pr.finish(); return }
                android.os.Handler(android.os.Looper.getMainLooper()).postDelayed({ pr.finish() }, 6000)
            }
            Intent.ACTION_BOOT_COMPLETED,
            Intent.ACTION_MY_PACKAGE_REPLACED -> {
                // Both are moments the trail's service is dead and the OS lets us start one from
                // the background; the phrases redraw too.
                com.localghost.app.sync.LocationLog.scheduleIfActive(context)
                PhraseSurface.refresh(context)
                PhraseOffer.check(context)
            }
            Intent.ACTION_TIMEZONE_CHANGED -> {
                // A new time zone is the cheapest "you have landed" there is.
                PhraseSurface.refresh(context)
                PhraseOffer.check(context)
            }
            PhraseSurface.ACTION_REFRESH,
            Intent.ACTION_TIME_CHANGED -> PhraseSurface.refresh(context)
        }
    }
}
