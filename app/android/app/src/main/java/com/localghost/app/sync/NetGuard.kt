package com.localghost.app.sync

import android.content.Context
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import com.localghost.app.settings.AppSettings
import java.io.IOException
import java.io.InputStream

/**
 * THE METERED-DATA GUARD , enforcement in the data path, not just the schedule. WorkManager's
 * UNMETERED constraint gates when a run STARTS; a run that began on Wi-Fi keeps its blocking
 * upload I/O alive after the phone walks onto 5G, and a 130GB library on mobile data is a phone
 * bill with a story. Two layers here: uploadsAllowed() for between-item checks (clean pause), and
 * GuardedInputStream for MID-FILE , it re-checks the network every 2MB of reads and kills the
 * transfer the moment Wi-Fi is gone, so the largest video can leak at most 2MB before dying.
 * The cursor's contiguity rule makes every such death a clean resume point on the next Wi-Fi run.
 */
object NetGuard {

    class MeteredBlocked : IOException("uploads paused , not on Wi-Fi and mobile sync is off")

    /** Wi-Fi or ethernet always; anything else only with the explicit mobile-sync setting. */
    fun uploadsAllowed(ctx: Context): Boolean {
        val cm = ctx.getSystemService(Context.CONNECTIVITY_SERVICE) as? ConnectivityManager
            ?: return false
        val caps = cm.getNetworkCapabilities(cm.activeNetwork) ?: return false
        val unmeteredTransport = caps.hasTransport(NetworkCapabilities.TRANSPORT_WIFI) ||
            caps.hasTransport(NetworkCapabilities.TRANSPORT_ETHERNET)
        return unmeteredTransport || AppSettings.allowMobileSync(ctx)
    }

    /** Wraps an upload stream; every ~2MB it re-asks whether this transfer may still exist. */
    class GuardedInputStream(private val ctx: Context, private val inner: InputStream) : InputStream() {
        private var sinceCheck = 0L

        private fun gate(bytes: Int) {
            sinceCheck += bytes
            if (sinceCheck >= 2L * 1024 * 1024) {
                sinceCheck = 0
                if (!uploadsAllowed(ctx)) throw MeteredBlocked()
            }
        }

        override fun read(): Int {
            val b = inner.read()
            if (b >= 0) gate(1)
            return b
        }

        override fun read(b: ByteArray, off: Int, len: Int): Int {
            val n = inner.read(b, off, len)
            if (n > 0) gate(n)
            return n
        }

        override fun available(): Int = inner.available()
        override fun close() = inner.close()
    }
}
