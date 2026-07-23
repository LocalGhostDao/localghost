package com.localghost.app.net

import android.content.Context
import java.io.BufferedReader
import java.io.InputStreamReader
import java.io.OutputStream
import java.net.ServerSocket
import java.net.Socket
import java.net.URLEncoder

/**
 * The LOOPBACK VIDEO PROXY. No Android media player can speak the box's mTLS + bearer, so the
 * player talks plain HTTP to 127.0.0.1 and this proxy forwards each request over the authenticated
 * channel , same pinned cert, same client key, same cached factory as every other call. The
 * critical passthrough is the Range header: ranged GETs are how players seek (probe the moov atom
 * at the file's tail, jump to minute 7, resume after pause), and the box answers 206 with
 * Content-Range, so scrubbing works against the archive directly , no download, no copy, RAM
 * footprint of one 512KB buffer.
 *
 * Threat notes: binds 127.0.0.1 only (never a LAN interface); serves only /v1/frames/original for
 * an allow-listed hash set (a local port is same-device attack surface , another app could
 * connect, so the proxy refuses any hash the player did not register); Connection: close per
 * request (players reconnect per range, and one-shot connections keep the parser trivial); dies
 * with the player dialog.
 */
class VideoProxy(private val appCtx: Context) {
    private var server: ServerSocket? = null
    @Volatile private var running = false
    private val allowed = java.util.Collections.synchronizedSet(HashSet<String>())

    /** Start (idempotent) and return the port. */
    fun start(): Int {
        server?.let { if (!it.isClosed) return it.localPort }
        val ss = ServerSocket(0, 8, java.net.InetAddress.getLoopbackAddress())
        server = ss
        running = true
        Thread({
            while (running) {
                val sock = try { ss.accept() } catch (_: Exception) { break }
                Thread({ handle(sock) }, "lg-vproxy-conn").start()
            }
        }, "lg-vproxy").start()
        return ss.localPort
    }

    /** Register a hash and get the player-facing URL for it. */
    fun urlFor(hash: String): String {
        allowed.add(hash)
        return "http://127.0.0.1:${start()}/v?hash=" + URLEncoder.encode(hash, "UTF-8")
    }

    fun stop() {
        running = false
        runCatching { server?.close() }
        server = null
        allowed.clear()
    }

    private fun handle(sock: Socket) {
        sock.use { s ->
            try {
                s.soTimeout = 15000
                val rd = BufferedReader(InputStreamReader(s.getInputStream(), Charsets.ISO_8859_1))
                val reqLine = rd.readLine() ?: return
                var range: String? = null
                while (true) {
                    val line = rd.readLine() ?: break
                    if (line.isEmpty()) break
                    if (line.startsWith("Range:", ignoreCase = true)) {
                        range = line.substringAfter(':').trim()
                    }
                }
                val out = s.getOutputStream()
                val parts = reqLine.split(' ')
                if (parts.size < 2 || parts[0] != "GET") { deny(out, 405, "method"); return }
                val hash = parts[1].substringAfter("hash=", "").substringBefore('&')
                    .let { java.net.URLDecoder.decode(it, "UTF-8") }
                if (hash.isEmpty() || hash !in allowed) { deny(out, 403, "unregistered"); return }

                val conn = BoxHttp.openAuthed(appCtx, "/v1/frames/original?hash=$hash", "GET")
                if (range != null) conn.setRequestProperty("Range", range)
                val code = conn.responseCode
                if (code != 200 && code != 206) { conn.disconnect(); deny(out, 502, "box $code"); return }

                val sb = StringBuilder()
                sb.append("HTTP/1.1 ").append(code)
                    .append(if (code == 206) " Partial Content" else " OK").append("\r\n")
                for (hName in listOf("Content-Type", "Content-Length", "Content-Range", "Accept-Ranges", "Last-Modified")) {
                    conn.getHeaderField(hName)?.let { sb.append(hName).append(": ").append(it).append("\r\n") }
                }
                sb.append("Connection: close\r\n\r\n")
                out.write(sb.toString().toByteArray(Charsets.ISO_8859_1))

                conn.inputStream.use { ins ->
                    val buf = ByteArray(512 * 1024)
                    while (true) {
                        val n = ins.read(buf)
                        if (n < 0) break
                        out.write(buf, 0, n)
                    }
                }
                out.flush()
                conn.disconnect()
            } catch (e: Exception) {
                // A player abandoning a range mid-read (seeks do this constantly) is normal life,
                // not an error worth a log line each.
            }
        }
    }

    private fun deny(out: OutputStream, code: Int, why: String) {
        runCatching {
            out.write("HTTP/1.1 $code $why\r\nConnection: close\r\nContent-Length: 0\r\n\r\n"
                .toByteArray(Charsets.ISO_8859_1))
            out.flush()
        }
    }
}
