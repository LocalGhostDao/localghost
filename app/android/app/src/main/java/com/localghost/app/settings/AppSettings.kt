package com.localghost.app.settings

import android.content.Context

/**
 * Persisted user settings. Privacy-respecting defaults: sync is Wi-Fi only unless the
 * user explicitly opts into mobile data.
 */
object AppSettings {
    private const val PREFS = "lg_settings"
    private const val KEY_MOBILE_SYNC = "allow_mobile_sync"
    private const val KEY_ASKED_MEDIA = "ever_asked_media"
    private const val KEY_LAST_AUTOSYNC = "last_autosync_at"

    private fun prefs(ctx: Context) = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    /** Deliberation depth sent with every chat: "" (direct), "brief", or "deep". */
    fun thinkLevel(ctx: Context): String = prefs(ctx).getString("think_level", "") ?: ""
    fun setThinkLevel(ctx: Context, level: String) =
        prefs(ctx).edit().putString("think_level", level).apply()

    /** Sync pause: honored by BOTH the periodic worker and the auto/manual one-shots. */
    /** The last saved (non-incognito) box chat the person was in , restored after re-unlock so the
     *  conversation survives the app process, not just the box (the box always had it; the SCREEN
     *  forgot). 0 = none. */
    /** GLOBAL DEBUG MODE , toggled from Settings ("set app in debug mode"). Gates the tok/s
     *  meter and whatever diagnostics attach later. Off by default; a user flag, not a build. */
    fun debugMode(ctx: Context): Boolean = prefs(ctx).getBoolean("debug_mode", false)
    fun setDebugMode(ctx: Context, on: Boolean) = prefs(ctx).edit().putBoolean("debug_mode", on).apply()

    fun lastCheckinDay(ctx: Context): String = prefs(ctx).getString("last_checkin_day", "") ?: ""
    fun setLastCheckinDay(ctx: Context, d: String) = prefs(ctx).edit().putString("last_checkin_day", d).apply()

    fun lastChatId(ctx: Context): Long = prefs(ctx).getLong("last_chat_id", 0L)
    fun setLastChatId(ctx: Context, id: Long) = prefs(ctx).edit().putLong("last_chat_id", id).apply()

    fun syncPaused(ctx: Context): Boolean = prefs(ctx).getBoolean("sync_paused", false)
    fun setSyncPaused(ctx: Context, paused: Boolean) =
        prefs(ctx).edit().putBoolean("sync_paused", paused).apply()

    /** Epoch millis of the last AUTOMATIC sync kick, so returning to the app does not restart sync. */
    fun lastAutoSyncAt(ctx: Context): Long = prefs(ctx).getLong(KEY_LAST_AUTOSYNC, 0L)
    fun setLastAutoSyncAt(ctx: Context, at: Long) =
        prefs(ctx).edit().putLong(KEY_LAST_AUTOSYNC, at).apply()

    /** false = Wi-Fi only (default). true = also sync on 4G/5G. */
    fun allowMobileSync(ctx: Context): Boolean = prefs(ctx).getBoolean(KEY_MOBILE_SYNC, false)
    fun setAllowMobileSync(ctx: Context, allow: Boolean) =
        prefs(ctx).edit().putBoolean(KEY_MOBILE_SYNC, allow).apply()

    /** Whether we've ever shown the media permission prompt — distinguishes never-asked
     *  (prompt still works) from permanently-denied (prompt dead, settings only). */
    fun everAskedMedia(ctx: Context): Boolean = prefs(ctx).getBoolean(KEY_ASKED_MEDIA, false)
    fun setEverAskedMedia(ctx: Context, asked: Boolean) =
        prefs(ctx).edit().putBoolean(KEY_ASKED_MEDIA, asked).apply()

    /** The welcome screen has run once: every permission asked in one place, before any scan.
     *  False on an upgrade from a build that had no welcome, so existing installs see it once too. */
    fun onboarded(ctx: Context): Boolean = prefs(ctx).getBoolean("onboarded", false)
    fun setOnboarded(ctx: Context, done: Boolean) = prefs(ctx).edit().putBoolean("onboarded", done).apply()

    /** GRANT ACCESS was tapped at least once: after that, a permission the OS will not ask about
     *  again is BLOCKED, not merely unasked. Separate from everAskedMedia, which an upgraded
     *  install already has set. */
    fun welcomeAsked(ctx: Context): Boolean = prefs(ctx).getBoolean("welcome_asked", false)
    fun setWelcomeAsked(ctx: Context, asked: Boolean) = prefs(ctx).edit().putBoolean("welcome_asked", asked).apply()

    /** Web search before a question goes to the box: "off", "auto" (when the question looks like
     *  it needs the outside world), "on" (every question). The PHONE searches; the box never does.
     *  Off by default: nothing leaves the phone for a third party unless the person says so. */
    fun webMode(ctx: Context): String = prefs(ctx).getString("web_mode", "off") ?: "off"
    fun setWebMode(ctx: Context, m: String) = prefs(ctx).edit().putString("web_mode", m).apply()

    /** The location trail: a position every quarter hour, kept on the phone and handed to the box
     *  when there is one. On by default once location is allowed; the switch is on the welcome
     *  screen and in settings, and the permission itself is the second switch. */
    fun locationTrail(ctx: Context): Boolean = prefs(ctx).getBoolean("location_trail", true)
    fun setLocationTrail(ctx: Context, on: Boolean) = prefs(ctx).edit().putBoolean("location_trail", on).apply()
}
