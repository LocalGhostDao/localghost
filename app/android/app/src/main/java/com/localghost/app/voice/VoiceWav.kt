package com.localghost.app.voice

import java.io.File
import java.io.RandomAccessFile

/**
 * The file a voice note is: a plain PCM WAV, 16 kHz, mono, 16-bit, which is what whisper.cpp on the
 * box reads without converting anything (1.9 MB a minute). The header is written first with zero
 * sizes and finished when the recording stops; a note whose app died mid-way is finished when it
 * is found again (VoiceNotes.recoverOrphans), and the box reads an unfinished one to its end anyway.
 */
object VoiceWav {
    const val RATE = 16000
    const val HEADER = 44

    fun header(rate: Int, channels: Int, bits: Int, dataLen: Long): ByteArray {
        val h = ByteArray(HEADER)
        fun ascii(at: Int, s: String) { for (i in s.indices) h[at + i] = s[i].code.toByte() }
        fun le32(at: Int, v: Long) { for (i in 0..3) h[at + i] = ((v shr (8 * i)) and 0xff).toByte() }
        fun le16(at: Int, v: Int) { h[at] = (v and 0xff).toByte(); h[at + 1] = ((v shr 8) and 0xff).toByte() }
        ascii(0, "RIFF"); le32(4, 36 + dataLen); ascii(8, "WAVE"); ascii(12, "fmt ")
        le32(16, 16); le16(20, 1); le16(22, channels); le32(24, rate.toLong())
        le32(28, rate.toLong() * channels * bits / 8); le16(32, channels * bits / 8); le16(34, bits)
        ascii(36, "data"); le32(40, dataLen)
        return h
    }

    /** Write the real sizes into a header written with zeros. */
    fun finish(file: File) {
        val data = (file.length() - HEADER).coerceAtLeast(0)
        RandomAccessFile(file, "rw").use { raf ->
            fun le32(v: Long) { for (i in 0..3) raf.write(((v shr (8 * i)) and 0xff).toInt()) }
            raf.seek(4); le32(36 + data)
            raf.seek(40); le32(data)
        }
    }

    fun durationMs(file: File, rate: Int = RATE): Long =
        (file.length() - HEADER).coerceAtLeast(0) * 1000 / (rate * 2)

    /** How loud a stretch of samples is: the root of the mean square, 0..1 of full scale. A
     *  cup put down has a high peak and a low one of these; a voice has both. */
    fun rms(samples: ShortArray, n: Int): Float {
        if (n <= 0) return 0f
        var sum = 0.0
        for (i in 0 until n) { val s = samples[i].toDouble(); sum += s * s }
        return (Math.sqrt(sum / n) / 32768.0).toFloat()
    }

    /** Cut a finished WAV down to [fromMs, toMs) of itself, in place: what was said, without the
     *  room at both ends. Whole samples only; a cut that would leave nothing leaves the file alone. */
    fun trim(file: File, fromMs: Long, toMs: Long, rate: Int = RATE) {
        val total = (file.length() - HEADER).coerceAtLeast(0)
        val from = (fromMs.coerceAtLeast(0) * rate / 1000 * 2).coerceAtMost(total)
        val to = (toMs * rate / 1000 * 2).coerceIn(from, total)
        if (to - from < 2 || (from == 0L && to == total)) return
        val keep = ByteArray((to - from).toInt())
        RandomAccessFile(file, "r").use { raf -> raf.seek(HEADER + from); raf.readFully(keep) }
        val tmp = File(file.parentFile, file.name + ".cut")
        tmp.outputStream().use { out -> out.write(header(rate, 1, 16, keep.size.toLong())); out.write(keep) }
        if (!tmp.renameTo(file)) { tmp.copyTo(file, overwrite = true); tmp.delete() }
    }

    /** Little-endian samples into bytes; returns the loudest sample's magnitude (0..32768). */
    fun pack(samples: ShortArray, n: Int, out: ByteArray): Int {
        var peak = 0
        for (i in 0 until n) {
            val s = samples[i].toInt()
            out[2 * i] = (s and 0xff).toByte()
            out[2 * i + 1] = ((s shr 8) and 0xff).toByte()
            val a = if (s < 0) -s else s
            if (a > peak) peak = a
        }
        return peak
    }
}
