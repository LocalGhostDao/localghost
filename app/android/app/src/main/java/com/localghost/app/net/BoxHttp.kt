package com.localghost.app.net

import android.content.Context
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import com.localghost.app.security.BoxConfig
import com.localghost.app.security.SessionStore
import org.json.JSONObject
import java.io.BufferedReader
import java.net.HttpURLConnection
import java.net.URL
import javax.net.ssl.HttpsURLConnection

/**
 * The real HTTP transport to ghost.secd, over the pinned mTLS channel. No third-party HTTP library
 * (consistent with the app's zero-dependency stance): plain HttpsURLConnection with the socket
 * factory from BoxTrust (pins the box self-signed cert by fingerprint) presenting the device cert
 * from DeviceCert. org.json is built into Android, so no JSON dependency either.
 *
 * Every call needs the box config (baseUrl + pinned fingerprint) and the enrolled device identity.
 * If the device is not enrolled, calls fail fast , the app should be on the setup screen then.
 */
object BoxHttp {

    class NotEnrolled : Exception("device is not enrolled")

    // THE HANDSHAKE CACHE , the 40k-photo discovery. Building a fresh SSLContext per request does
    // not just cost the build (an Android Keystore round-trip each time): a NEW factory instance
    // defeats HTTP keep-alive entirely , the connection pool keys on the factory , so EVERY photo
    // paid a full mTLS handshake, and a LAN that could move the file in 200ms spent seconds
    // shaking hands. One factory, built once, invalidated only when enrolment changes: one
    // handshake per session, keep-alive for the other 39,999.
    @Volatile private var cachedFactory: javax.net.ssl.SSLSocketFactory? = null
    @Volatile private var cachedKey: String = ""

    private fun factoryFor(ctx: Context, cfg: BoxConfig.Config): javax.net.ssl.SSLSocketFactory {
        val key = cfg.baseUrl + "|" + cfg.certFingerprint
        cachedFactory?.let { if (cachedKey == key) return it }
        synchronized(this) {
            cachedFactory?.let { if (cachedKey == key) return it }
            val km = DeviceCert.keyManager(ctx) ?: throw NotEnrolled()
            val f = BoxTrust.socketFactory(cfg.certFingerprint, km)
            cachedFactory = f
            cachedKey = key
            return f
        }
    }

    private fun open(ctx: Context, path: String, method: String): HttpsURLConnection {
        val cfg = BoxConfig.read(ctx) ?: throw NotEnrolled()
        val conn = URL(cfg.baseUrl.trimEnd('/') + path).openConnection() as HttpsURLConnection
        conn.sslSocketFactory = factoryFor(ctx, cfg)
        // The fingerprint pin is the ENTIRE trust decision (BoxTrust): hostname/SAN checking against a
        // self-signed pinned cert adds nothing , no CA exists to mis-issue for a different host , and it
        // actively breaks the moment the box's LAN address changes (DHCP hands it a new IP, or the user
        // types a hostname where the cert has an IP). Without this, every call would die on hostname
        // verification with a perfectly matching pin.
        conn.hostnameVerifier = javax.net.ssl.HostnameVerifier { _, _ -> true }
        conn.requestMethod = method
        conn.connectTimeout = 10_000
        conn.readTimeout = 30_000
        // Bearer is the SESSION token minted at PIN unlock (SessionStore), NOT the enrolment token:
        // the box validates the session token per request and it expires in 2 days. The device mTLS
        // client cert (set on the socket factory above) is the separate, durable identity that gates
        // the channel. No session yet (pre-unlock) -> no bearer; those calls are unauthenticated and
        // the box answers appears-down, which is correct.
        SessionStore.read(ctx)?.let { conn.setRequestProperty("Authorization", "Bearer ${it.token}") }
        return conn
    }

    /** GET returning the parsed JSON object. Runs on Dispatchers.IO , HttpsURLConnection blocks, and
     *  on the main thread that is an instant NetworkOnMainThreadException before the request is sent. */
    /** GET raw bytes (thumbnails). Null on any failure , gallery cells just stay empty. */
    suspend fun getBytes(ctx: Context, path: String): ByteArray? = withContext(Dispatchers.IO) {
        try {
            val conn = open(ctx, path, "GET")
            if (conn.responseCode != 200) { conn.disconnect(); return@withContext null }
            conn.inputStream.use { it.readBytes() }
        } catch (e: Exception) {
            null
        }
    }

    /** GET with ETag revalidation: 304 -> (null, sameEtag) , caller keeps its cache. */
    suspend fun getBytesEtag(ctx: Context, path: String, etag: String?): Pair<ByteArray?, String?> =
        withContext(Dispatchers.IO) {
            try {
                val conn = open(ctx, path, "GET")
                if (!etag.isNullOrEmpty()) conn.setRequestProperty("If-None-Match", etag)
                when (conn.responseCode) {
                    304 -> { conn.disconnect(); Pair(null, etag) }
                    200 -> Pair(conn.inputStream.use { it.readBytes() }, conn.getHeaderField("ETag"))
                    else -> { conn.disconnect(); Pair(null, null) }
                }
            } catch (e: Exception) { Pair(null, null) }
        }

    suspend fun getJson(ctx: Context, path: String): JSONObject = withContext(Dispatchers.IO) {
        val conn = open(ctx, path, "GET")
        readJson(conn)
    }

    /**
     * POST a raw byte stream (a photo, exactly as shot) and return the HTTP status code. Chunked
     * streaming mode , a phone photo is multi-MB and must never be buffered whole in RAM; 64KB chunks
     * flow from the MediaStore stream straight onto the mTLS socket. The box (secd) spools the same
     * bytes to disk without parsing them; parsing happens in ghost.framed behind the front door.
     */
    suspend fun postStream(ctx: Context, path: String, body: java.io.InputStream, contentType: String = "application/octet-stream", takenAtMs: Long = 0): Int = withContext(Dispatchers.IO) {
        val conn = open(ctx, path, "POST")
        conn.doOutput = true
        conn.setChunkedStreamingMode(64 * 1024)
        conn.setRequestProperty("Content-Type", contentType)
        // Taken-timestamp hint: the box uses it as the fallback taken time for media whose bytes
        // carry none (videos have no EXIF). A HINT, not trusted identity , content hash stays the id.
        if (takenAtMs > 0) conn.setRequestProperty("X-Ghost-Taken", takenAtMs.toString())
        conn.outputStream.use { out -> body.copyTo(out, 64 * 1024) }
        val code = conn.responseCode
        conn.disconnect()
        code
    }

    /** POST a JSON body, returning the parsed JSON response. On Dispatchers.IO (see getJson). */
    suspend fun postJson(ctx: Context, path: String, body: JSONObject, readTimeoutMs: Int = 30_000): JSONObject = withContext(Dispatchers.IO) {
        val conn = open(ctx, path, "POST")
        conn.readTimeout = readTimeoutMs
        conn.doOutput = true
        conn.setRequestProperty("Content-Type", "application/json")
        conn.outputStream.use { it.write(body.toString().toByteArray()) }
        readJson(conn)
    }

    /**
     * Open a raw byte stream for a GET, with an optional Range start offset so a model download can
     * resume. The caller owns the stream and must close it. The underlying connection is closed when
     * the stream is exhausted/closed by the caller's use{} block.
     */
    suspend fun openStream(ctx: Context, path: String, offset: Long): java.io.InputStream = withContext(Dispatchers.IO) {
        val conn = open(ctx, path, "GET")
        if (offset > 0) {
            conn.setRequestProperty("Range", "bytes=$offset-")
        }
        val code = conn.responseCode
        if (code !in 200..299) {
            conn.disconnect()
            throw java.io.IOException("box returned HTTP $code for $path")
        }
        conn.inputStream
    }

    /** POST JSON and stream the response line by line to [onLine] (return false to stop early).
     *  For the chat event stream: long read timeout BETWEEN chunks (a deep-think can pause for
     *  minutes before the first token on CPU), and disconnect on early stop so the box sees the
     *  hang-up and cancels generation. */
    suspend fun postStreamLines(
        ctx: Context, path: String, body: JSONObject,
        onLine: (String) -> Boolean,
    ): Unit = withContext(Dispatchers.IO) {
        val conn = open(ctx, path, "POST")
        conn.readTimeout = 6 * 60_000
        conn.doOutput = true
        conn.setRequestProperty("Content-Type", "application/json")
        conn.setRequestProperty("Accept", "text/event-stream")
        try {
            conn.outputStream.use { it.write(body.toString().toByteArray()) }
            val code = conn.responseCode
            if (code !in 200..299) {
                android.util.Log.w("LocalGhost", "HTTP $code on $path")
                throw java.io.IOException("HTTP $code")
            }
            conn.inputStream.bufferedReader().useLines { lines ->
                for (line in lines) {
                    if (line.isBlank()) continue
                    if (!onLine(line)) break
                }
            }
        } finally {
            conn.disconnect()
        }
    }

    private fun readJson(conn: HttpsURLConnection): JSONObject {
        return try {
            val code = conn.responseCode
            if (code !in 200..299) {
                // Named visibility for every failing call. Behavior is UNCHANGED (callers like the
                // unlock poll rely on parse-whatever-came-back), but before this line a 503 surfaced
                // only as downstream JSON-parse noise , the HTTP code, the actual fact, was nowhere.
                android.util.Log.w("LocalGhost", "HTTP $code on ${conn.url.path}")
            }
            val stream = if (code in 200..299) conn.inputStream else conn.errorStream
            // 204 No Content and other empty bodies parse as an empty object , JSONObject("") throws,
            // which made SUCCESSFUL tag edits report failure and roll back their optimistic UI update.
            val text = (stream?.bufferedReader()?.use(BufferedReader::readText) ?: "{}").ifBlank { "{}" }
            JSONObject(text)
        } finally {
            conn.disconnect()
        }
    }
}
