package com.localghost.app.phrases

import android.app.Notification
import android.app.NotificationChannel
import android.app.NotificationManager
import android.app.PendingIntent
import android.content.Context
import android.content.Intent
import android.telephony.TelephonyManager
import androidx.core.app.NotificationCompat
import androidx.core.app.NotificationManagerCompat
import com.localghost.app.MainActivity
import com.localghost.app.R
import com.localghost.app.notify.Notifications
import com.localghost.app.settings.AppSettings

/**
 * ghost.phrased stays out of the way until it has a reason to exist: the phone lands in a country
 * that is not home and has a pack, and ONCE, for that country, it asks , a quiet notification
 * with TURN ON and NO THANKS, and the same question as a line in the app until it is answered.
 * Yes turns the feature on (drawer entry, lock-screen card); no is remembered for that country
 * and never asked again for it. At home nothing is ever offered: a lock-screen card teaching you
 * your own language is a mistake, and a feature you did not ask for is noise.
 *
 * "New country" comes from wherever the app already knows the country: the trail's geocoded fix
 * (LocationWorker, in the background, so the offer arrives on the day you land), the boot and
 * time-zone broadcasts, and the app opening. Home is the SIM's country, taken once at the welcome
 * screen and changeable in settings.
 */
object PhraseOffer {
    const val CHANNEL_ID = "localghost.phrases.offer"
    const val NOTIF_ID = 4713
    const val ACTION_ACCEPT = "com.localghost.app.phrases.OFFER_ACCEPT"
    const val ACTION_DECLINE = "com.localghost.app.phrases.OFFER_DECLINE"

    /** Home, as a country code: the setting, else the SIM's country, else "". */
    fun homeCountry(ctx: Context): String {
        AppSettings.homeCountry(ctx).takeIf { it.length == 2 }?.let { return it }
        return simCountry(ctx)
    }

    fun simCountry(ctx: Context): String = try {
        (ctx.getSystemService(Context.TELEPHONY_SERVICE) as? TelephonyManager)?.simCountryIso
            ?.takeIf { it.length == 2 }?.uppercase() ?: ""
    } catch (_: Exception) { "" }

    /** Record home once, from the SIM, when nothing is set yet (the welcome screen's job). */
    fun settleHome(ctx: Context) {
        if (AppSettings.homeCountry(ctx).length != 2) simCountry(ctx).takeIf { it.isNotEmpty() }?.let { AppSettings.setHomeCountry(ctx, it) }
    }

    /** Look at where the phone is right now and offer if that is somewhere new. Safe to call from
     *  anywhere, any time; a no-op in every case but the one it is for. */
    fun check(ctx: Context) {
        val where = CountryDetect.detect(ctx.applicationContext)
        if (where.source == "unknown" || where.country.isEmpty()) return
        maybeOffer(ctx, where.country)
    }

    /**
     * Offer the phrases for [country] if: the feature is off, the country is not home, a pack
     * covers it, and it has not been offered before. Marks it offered either way it goes.
     * Returns true when an offer was made.
     */
    fun maybeOffer(ctx: Context, country: String): Boolean {
        val app = ctx.applicationContext
        val cc = country.uppercase()
        if (cc.length != 2 || PhraseState.enabled(app)) return false
        val home = homeCountry(app)
        if (home.isEmpty() || cc == home) return false
        if (PhraseState.offered(app, cc)) return false
        val packs = PhraseEngine.langsFor(cc, PhrasePacks.all(app))
        if (packs.isEmpty()) return false
        PhraseState.setOffered(app, cc)
        PhraseState.setOfferPending(app, cc)
        post(app, cc, packs[0])
        return true
    }

    /** TURN ON: the feature and the lock-screen card, the surfaces drawn, the question gone. */
    fun accept(ctx: Context) {
        val app = ctx.applicationContext
        PhraseState.setEnabled(app, true)
        PhraseState.setLockScreenOn(app, true)
        PhraseState.setOfferPending(app, "")
        NotificationManagerCompat.from(app).cancel(NOTIF_ID)
        PhraseSurface.refresh(app)
    }

    /** NO THANKS: the question gone, the country remembered as asked. */
    fun decline(ctx: Context) {
        val app = ctx.applicationContext
        PhraseState.setOfferPending(app, "")
        NotificationManagerCompat.from(app).cancel(NOTIF_ID)
    }

    /** The one-line version of the question, for the in-app banner: "in Greece? …" or "". */
    fun pendingLine(ctx: Context): String {
        val cc = PhraseState.offerPending(ctx)
        if (cc.isEmpty() || PhraseState.enabled(ctx)) return ""
        val pack = PhraseEngine.langsFor(cc, PhrasePacks.all(ctx)).firstOrNull() ?: return ""
        return "in ${CountryNames.of(cc)}? ${pack.name} phrases for the lock screen, one for this hour"
    }

    private fun ensureChannel(ctx: Context) {
        val ch = NotificationChannel(CHANNEL_ID, "Phrases, when you travel", NotificationManager.IMPORTANCE_DEFAULT).apply {
            description = "Asked once per country you land in: whether you want the phrase of the hour on your lock screen."
            setShowBadge(false)
            lockscreenVisibility = Notification.VISIBILITY_PUBLIC
        }
        ctx.getSystemService(NotificationManager::class.java).createNotificationChannel(ch)
    }

    private fun post(ctx: Context, cc: String, pack: PhrasePack) {
        if (!Notifications.hasPermission(ctx)) return // the in-app line still asks
        ensureChannel(ctx)
        val greeting = pack.phrases.firstOrNull { it.id == "hello" } ?: pack.phrases.firstOrNull { it.id == "good_morning" }
        val title = (greeting?.local?.let { "$it , " } ?: "") + "you're in ${CountryNames.of(cc)}"
        val open = Intent(ctx, MainActivity::class.java).apply {
            action = "com.localghost.app.OPEN_PHRASES"
            putExtra("nav", "phrases")
            flags = Intent.FLAG_ACTIVITY_SINGLE_TOP
        }
        val b = NotificationCompat.Builder(ctx, CHANNEL_ID)
            .setSmallIcon(R.drawable.ic_ghost_notif)
            .setColor(0xFF33FF00.toInt())
            .setSilent(true)
            .setAutoCancel(true)
            .setVisibility(NotificationCompat.VISIBILITY_PUBLIC)
            .setContentTitle(title)
            .setContentText("Want the ${pack.name} phrase you'll need on your lock screen? It changes with the hour and never leaves the phone.")
            .setStyle(NotificationCompat.BigTextStyle().bigText(
                "Want the ${pack.name} phrase you'll need on your lock screen? Good morning at eight, one coffee at nine, the bill at eleven , silent, changes with the hour, never leaves the phone. Asked once; no is no."))
            .setContentIntent(PendingIntent.getActivity(ctx, 4713, open, PendingIntent.FLAG_IMMUTABLE or PendingIntent.FLAG_UPDATE_CURRENT))
            .addAction(0, "TURN ON", PhraseSurface.broadcast(ctx, ACTION_ACCEPT, 11))
            .addAction(0, "NO THANKS", PhraseSurface.broadcast(ctx, ACTION_DECLINE, 12))
        NotificationManagerCompat.from(ctx).notify(NOTIF_ID, b.build())
    }
}
