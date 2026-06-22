package com.localghost.app.notify

import android.content.Context

object NotifyState {
    private const val PREFS = "lg_notify"
    private const val KEY_MUTED = "muted"
    private const val KEY_LAST = "last_posted"
    private fun prefs(ctx: Context) = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
    fun isMuted(ctx: Context) = prefs(ctx).getBoolean(KEY_MUTED, false)
    fun setMuted(ctx: Context, m: Boolean) = prefs(ctx).edit().putBoolean(KEY_MUTED, m).apply()
    fun lastPostedAt(ctx: Context) = prefs(ctx).getLong(KEY_LAST, 0L)
    fun setLastPostedAt(ctx: Context, t: Long) = prefs(ctx).edit().putLong(KEY_LAST, t).apply()
}
