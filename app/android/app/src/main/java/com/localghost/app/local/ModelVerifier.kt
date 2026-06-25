package com.localghost.app.local

import java.io.InputStream
import java.security.MessageDigest

/**
 * The model integrity check, pulled out of ModelDownloadWorker so it can be unit-tested without
 * the filesystem or WorkManager. A downloaded model is only accepted if its SHA-256 matches the
 * hash the box published. Getting this wrong in either direction is bad: accept a corrupted model,
 * or reject a good one and loop forever re-downloading.
 *
 * Takes an InputStream rather than a File so tests can feed a ByteArrayInputStream. The Worker
 * passes file.inputStream().
 */
object ModelVerifier {

    /** SHA-256 of a stream as lowercase hex. Reads in 64 KB chunks; closes the stream. */
    fun sha256Hex(input: InputStream): String {
        val md = MessageDigest.getInstance("SHA-256")
        input.use { ins ->
            val buf = ByteArray(1 shl 16)
            while (true) {
                val n = ins.read(buf)
                if (n < 0) break
                md.update(buf, 0, n)
            }
        }
        return md.digest().joinToString("") { "%02x".format(it) }
    }

    /**
     * True if the stream's SHA-256 matches expected (case-insensitive hex). A null/blank expected
     * hash means the box published none, so there is nothing to check against and we do NOT treat
     * the file as verified; callers decide whether unverified is acceptable.
     */
    fun matches(input: InputStream, expected: String?): Boolean {
        if (expected.isNullOrBlank()) return false
        return sha256Hex(input).equals(expected.trim(), ignoreCase = true)
    }
}
