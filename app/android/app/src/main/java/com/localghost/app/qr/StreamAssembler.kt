package com.localghost.app.qr

import java.util.zip.CRC32

/**
 * Reassembles an erasure-coded enrolment QR set (LGQR2), the app-side mirror of the box's
 * internal/pair/qrstream.go , a contract, so the two files stay in lockstep and the box's test
 * fixture (lgqr2_fixture.txt) is decoded by the unit test here.
 *
 * The box splits the link into K data blocks and adds M parity blocks from a Cauchy matrix over
 * GF(256): ANY K distinct frames rebuild the link. So a frame the camera missed never has to come
 * round again; the next frame, whichever it is, counts just the same. Frame format, an ASCII
 * header then the block's raw bytes (byte-mode QR, read as ISO-8859-1, which maps bytes 1:1):
 *
 *     LGQR2 <idx> <K> <M> <len> <crc32> <pcrc> <body>
 *
 * idx 0..K-1 are data blocks, K..K+M-1 parity; crc32 is over the whole payload and, with K, M
 * and len, identifies the set; pcrc is the low 16 bits of the body's own CRC-32, so a garbled
 * read of one frame is dropped instead of poisoning the set. Frames are bucketed by identity and
 * the most-voted identity wins, exactly as [FrameAssembler] does for LGQR1.
 */
class StreamAssembler {

    private data class Ident(val k: Int, val m: Int, val len: Int, val crc: Long)

    private val partsByIdent = HashMap<Ident, HashMap<Int, ByteArray>>()
    private val identVotes = HashMap<Ident, Int>()

    /** True if [text] is an erasure-coded enrolment frame (and thus should be fed to [offer]). */
    fun isFrame(text: String): Boolean = text.startsWith("$MAGIC ")

    /** Distinct frames held (capped at K) and K , "5 of 8" in the UI. (0, 0) before any valid frame. */
    fun progress(): Pair<Int, Int> {
        val best = winning() ?: return 0 to 0
        return minOf(partsByIdent[best]?.size ?: 0, best.k) to best.k
    }

    /** Frame indices held so far (0-based), for the per-frame marks in the UI. */
    fun capturedIdx(): Set<Int> = winning()?.let { partsByIdent[it]?.keys?.toSet() } ?: emptySet()

    /** How many frames the box rotates in total (K+M), 0 before any valid frame. */
    fun totalFrames(): Int = winning()?.let { it.k + it.m } ?: 0

    private fun winning(): Ident? = identVotes.maxByOrNull { it.value }?.key

    /** Whether the most recent [offer] added a frame the set did not already have. */
    var lastOfferWasNew: Boolean = false
        private set

    fun reset() {
        partsByIdent.clear()
        identVotes.clear()
        lastOfferWasNew = false
    }

    /**
     * Offers one decoded frame. Returns the rebuilt payload once K distinct frames of the winning
     * set are held and its CRC verifies; null while more are needed. A frame whose own CRC fails,
     * or that is malformed, is ignored (returns null, nothing recorded).
     */
    fun offer(text: String): ByteArray? {
        lastOfferWasNew = false
        val f = text.split(" ", limit = 8)
        if (f.size != 8 || f[0] != MAGIC) return null
        val idx = f[1].toIntOrNull() ?: return null
        val k = f[2].toIntOrNull() ?: return null
        val m = f[3].toIntOrNull() ?: return null
        val len = f[4].toIntOrNull() ?: return null
        val crc = f[5].toLongOrNull(16) ?: return null
        val pcrc = f[6].toLongOrNull(16) ?: return null
        if (idx < 0 || k < 1 || m < 0 || len < 1 || idx >= k + m || k + m > 255) return null
        val body = f[7].toByteArray(Charsets.ISO_8859_1)
        val bs = (len + k - 1) / k
        if (body.size != bs) return null
        if (crc32(body) and 0xffffL != pcrc) return null

        val ident = Ident(k, m, len, crc)
        identVotes[ident] = (identVotes[ident] ?: 0) + 1
        val bucket = partsByIdent.getOrPut(ident) { HashMap() }
        if (!bucket.containsKey(idx)) {
            bucket[idx] = body
            lastOfferWasNew = true
        }
        val best = winning() ?: return null
        val parts = partsByIdent[best] ?: return null
        if (parts.size < best.k) return null
        val payload = solve(best, parts) ?: return null
        if (crc32(payload) != best.crc) {
            // K frames that do not rebuild the payload: one of them lied past its own 16-bit CRC
            // (one in 65k). Drop this identity's parts and collect afresh rather than sit on it.
            partsByIdent.remove(best)
            identVotes.remove(best)
            return null
        }
        return payload
    }

    /** Gauss-Jordan over GF(256) on the K rows held: unit rows for data frames, Cauchy rows for
     *  parity frames, the bodies carried alongside as the right-hand side. */
    private fun solve(id: Ident, parts: Map<Int, ByteArray>): ByteArray? {
        val k = id.k
        val bs = (id.len + k - 1) / k
        val rows = ArrayList<IntArray>(k)
        val rhs = ArrayList<ByteArray>(k)
        for (idx in 0 until k + id.m) {
            if (rows.size == k) break
            val body = parts[idx] ?: continue
            val row = IntArray(k)
            if (idx < k) row[idx] = 1 else for (i in 0 until k) row[i] = cauchy(k, idx - k, i)
            rows.add(row)
            rhs.add(body.copyOf())
        }
        if (rows.size < k) return null
        for (col in 0 until k) {
            var piv = -1
            for (r in col until k) if (rows[r][col] != 0) { piv = r; break }
            if (piv < 0) return null
            rows[col] = rows[piv].also { rows[piv] = rows[col] }
            rhs[col] = rhs[piv].also { rhs[piv] = rhs[col] }
            val inv = GaloisField.inverse(rows[col][col])
            for (c in 0 until k) rows[col][c] = GaloisField.mul(rows[col][c], inv)
            for (t in 0 until bs) rhs[col][t] = GaloisField.mul(rhs[col][t].toInt() and 0xff, inv).toByte()
            for (r in 0 until k) {
                if (r == col || rows[r][col] == 0) continue
                val fct = rows[r][col]
                for (c in 0 until k) rows[r][c] = rows[r][c] xor GaloisField.mul(fct, rows[col][c])
                val src = rhs[col]; val dst = rhs[r]
                for (t in 0 until bs) dst[t] = (dst[t].toInt() xor GaloisField.mul(fct, src[t].toInt() and 0xff)).toByte()
            }
        }
        val out = ByteArray(k * bs)
        for (i in 0 until k) System.arraycopy(rhs[i], 0, out, i * bs, bs)
        return out.copyOf(id.len)
    }

    private fun cauchy(k: Int, j: Int, i: Int): Int = GaloisField.inverse((k + j) xor i)

    private fun crc32(b: ByteArray): Long = CRC32().also { it.update(b) }.value

    companion object {
        const val MAGIC = "LGQR2"
    }
}
