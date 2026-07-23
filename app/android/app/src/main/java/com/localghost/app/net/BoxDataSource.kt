package com.localghost.app.net

import android.content.Context
import android.net.Uri
import androidx.media3.common.C
import androidx.media3.common.util.UnstableApi
import androidx.media3.datasource.DataSource
import androidx.media3.datasource.DataSpec
import androidx.media3.datasource.TransferListener
import java.io.InputStream

/**
 * The IN-PROCESS video source. ExoPlayer asks for byte ranges; this class answers them over the
 * box's authenticated channel , same pinned cert, same client key, same cached factory as every
 * other call. Seeking is open() with a new position; the box answers 206 from the archive. There
 * is no socket, no proxy, no cleartext anywhere: the player's reads are function calls inside this
 * process, which is the smallest possible attack surface , none.
 *
 * The URI convention is box://frame/<hash>; only the hash is read from it.
 */
@UnstableApi
class BoxDataSource(private val appCtx: Context) : DataSource {

    class Factory(private val appCtx: Context) : DataSource.Factory {
        override fun createDataSource(): DataSource = BoxDataSource(appCtx)
    }

    private var conn: javax.net.ssl.HttpsURLConnection? = null
    private var stream: InputStream? = null
    private var uri: Uri? = null
    private var remaining: Long = C.LENGTH_UNSET.toLong()

    override fun addTransferListener(transferListener: TransferListener) {
        // Bandwidth meters are welcome to exist elsewhere; this source has nothing to report.
    }

    override fun open(dataSpec: DataSpec): Long {
        close()
        uri = dataSpec.uri
        val hash = dataSpec.uri.lastPathSegment
            ?: throw java.io.IOException("box uri carries no hash")
        val c = BoxHttp.openAuthed(appCtx, "/v1/frames/original?hash=$hash", "GET")
        // ExoPlayer's position/length map directly onto an HTTP Range , this line IS seeking.
        if (dataSpec.position > 0 || dataSpec.length != C.LENGTH_UNSET.toLong()) {
            val end = if (dataSpec.length != C.LENGTH_UNSET.toLong())
                (dataSpec.position + dataSpec.length - 1).toString() else ""
            c.setRequestProperty("Range", "bytes=${dataSpec.position}-$end")
        }
        val code = c.responseCode
        if (code != 200 && code != 206) {
            c.disconnect()
            throw java.io.IOException("box answered $code")
        }
        if (code == 200 && dataSpec.position > 0) {
            // A box without Range support (old secd) answers 200-from-zero; skipping to position
            // keeps playback CORRECT while seeks stay slow until the operator redeploys.
            var toSkip = dataSpec.position
            val ins = c.inputStream
            val buf = ByteArray(256 * 1024)
            while (toSkip > 0) {
                val n = ins.read(buf, 0, minOf(buf.size.toLong(), toSkip).toInt())
                if (n < 0) throw java.io.IOException("stream ended before position")
                toSkip -= n
            }
            stream = ins
        } else {
            stream = c.inputStream
        }
        conn = c
        val contentLen = c.getHeaderField("Content-Length")?.toLongOrNull()
        remaining = when {
            dataSpec.length != C.LENGTH_UNSET.toLong() -> dataSpec.length
            contentLen != null -> contentLen
            else -> C.LENGTH_UNSET.toLong()
        }
        return remaining
    }

    override fun read(target: ByteArray, offset: Int, length: Int): Int {
        if (length == 0) return 0
        if (remaining == 0L) return C.RESULT_END_OF_INPUT
        val cap = if (remaining != C.LENGTH_UNSET.toLong())
            minOf(length.toLong(), remaining).toInt() else length
        val n = stream?.read(target, offset, cap) ?: return C.RESULT_END_OF_INPUT
        if (n < 0) return C.RESULT_END_OF_INPUT
        if (remaining != C.LENGTH_UNSET.toLong()) remaining -= n
        return n
    }

    override fun getUri(): Uri? = uri

    override fun close() {
        runCatching { stream?.close() }
        runCatching { conn?.disconnect() }
        stream = null
        conn = null
        remaining = C.LENGTH_UNSET.toLong()
    }
}
