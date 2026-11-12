package com.localghost.app.ui

import android.content.Context
import android.content.Intent
import android.net.Uri

/**
 * The one way the app hands an address to the phone to open. A link comes from outside: a feed
 * entry, a coin's listing, a search result, and each used to go to Android as it stood, so a
 * "link" that was really another app's own address (a dialler, a payment app, a file on the
 * phone) opened that app on a tap (second audit, 12 Nov 2026). Only a web address is opened:
 * http or https, with a host, no login in front of it, nothing hidden in it. The rule is pure,
 * for the tests.
 */
object SafeLink {
    /** Whether [url] is a web address a browser should be given. */
    fun isWeb(url: String): Boolean {
        val u = url.trim()
        if (u.length > 2048 || u.any { it.isISOControl() || it.isWhitespace() }) return false
        val rest = when {
            u.startsWith("https://", ignoreCase = true) -> u.substring(8)
            u.startsWith("http://", ignoreCase = true) -> u.substring(7)
            else -> return false
        }
        val host = rest.takeWhile { it != '/' && it != '?' && it != '#' }
        // "https://bank.example@other.example/" is other.example with a login: not opened
        if (host.isEmpty() || host.contains('@') || host.contains('\\')) return false
        return host.any { it.isLetterOrDigit() }
    }

    /** Opens [url] in the phone's browser when it is a web address; false when it is not or
     *  nothing on the phone takes it. */
    fun open(ctx: Context, url: String): Boolean {
        if (!isWeb(url)) return false
        return runCatching {
            ctx.startActivity(Intent(Intent.ACTION_VIEW, Uri.parse(url.trim())).addCategory(Intent.CATEGORY_BROWSABLE))
        }.isSuccess
    }
}
