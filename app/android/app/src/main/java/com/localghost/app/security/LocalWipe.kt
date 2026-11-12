package com.localghost.app.security

import android.content.Context
import java.io.File

/**
 * WHAT GOES WHEN THE PHONE FORGETS A BOX. "WIPE THIS PHONE" used to remove the connection, the
 * certificate and the device keys, and leave everything else the box's session had put on the
 * phone: the session token, voice notes recorded and not yet sent (plain audio), the last
 * news, prices and weather with the place they were for, the map's last view, the last fix
 * (second audit, 12 Nov 2026). Scanning another box's QR left the old box's session token to
 * be sent to the new one. The list of what goes is here, in one place, and is pure over a
 * directory tree so the tests can check it.
 *
 * What stays, on purpose: the trail not yet sent (it is sealed to the box's key, so only that
 * box opens it, and it is the person's record to deliver after pairing again); map tiles and
 * the phone's own model (public data, gigabytes); the app's own settings and the phrases.
 */
object LocalWipe {
    /** Preference files that hold the box's session or what it sent, cleared whole. */
    val SESSION_PREFS = listOf("lg_session", "lg_home_brief", "lg_server_update", "lg_boxfetch",
        "localghost.sync.cursor", "unlock_clock", "lg_map_camera", "ghost_geo")

    /** Under filesDir: the last news, prices, home snapshot and weather. */
    val SESSION_DIRS = listOf("home")

    /**
     * A new box (or the same one, enrolled again): nothing of the last session is carried
     * over. The pending voice notes and the trail stay, they are the person's and go to the
     * box they pair with.
     */
    fun session(ctx: Context) {
        for (name in SESSION_PREFS) runCatching { ctx.getSharedPreferences(name, Context.MODE_PRIVATE).edit().clear().commit() }
        files(ctx.filesDir, null, ctx.cacheDir, voiceToo = false)
        runCatching { ctx.getSharedPreferences("lg_location", Context.MODE_PRIVATE).edit().remove("last_sealed").commit() }
    }

    /** WIPE THIS PHONE: the session, and what the person made that had not reached the box. */
    fun all(ctx: Context) {
        for (name in SESSION_PREFS) runCatching { ctx.getSharedPreferences(name, Context.MODE_PRIVATE).edit().clear().commit() }
        files(ctx.filesDir, ctx.noBackupFilesDir, ctx.cacheDir, voiceToo = true)
        runCatching { ctx.getSharedPreferences("lg_location", Context.MODE_PRIVATE).edit().remove("last_sealed").commit() }
        // the search key: where it is kept now, and where it was until 12 Nov 2026
        runCatching { BoxConfig.removeSecret(ctx, com.localghost.app.settings.AppSettings.BRAVE_SECRET) }
        runCatching { ctx.getSharedPreferences("lg_settings", Context.MODE_PRIVATE).edit().remove("brave_key").commit() }
    }

    /**
     * The files: the home cache, everything in the cache directory (captures, clips fetched to
     * play, exports, a release being handed over), the crash report, and with [voiceToo] the
     * voice notes waiting for the box. Returns how many files and directories went.
     */
    fun files(filesDir: File, noBackupDir: File?, cacheDir: File, voiceToo: Boolean): Int {
        var n = 0
        fun drop(f: File) { if (f.exists() && f.deleteRecursively()) n++ }
        for (d in SESSION_DIRS) drop(File(filesDir, d))
        drop(File(filesDir, "last_crash.txt"))
        cacheDir.listFiles()?.forEach { drop(it) }
        if (voiceToo && noBackupDir != null) drop(File(noBackupDir, "voice"))
        return n
    }
}
