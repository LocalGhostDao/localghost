package com.localghost.app.net

import java.net.URLDecoder

/**
 * The self-contained enrollment descriptor a box puts in its QR. No server is involved in
 * discovery or trust: the QR carries everything the phone needs to reach the box on the LAN and
 * to pin its identity on first contact.
 *
 *   localghost://enroll?v=2&host=192.168.1.20&port=8443&fp=AB:CD:...&name=box&cert=<b64url>&key=<b64url>
 *
 * - v     : format version. Absent means 1 (the original code-only links). The phone understands up
 *           to CURRENT_VERSION; a higher number means the box is newer than the app, and parse
 *           returns a Result.Outdated so the scanner can say "update the app". v2 adds cert+key.
 * - host  : LAN address or .local name the phone can route to right now.
 * - port  : the box's mTLS port.
 * - code  : one-time pairing code the box is showing (the box marks the QR consumed after first use).
 * - fp    : the box's certificate SHA-256 fingerprint. The trust anchor the phone pins on first
 *           contact, so enrollment is safe even over a hostile network. This fingerprint IS the vouch.
 * - name  : optional human label for the box (prefills the suggested device/box name).
 * - cert  : the DEVICE certificate (client cert) the box issued for this phone, PEM, base64url-encoded.
 * - key   : that cert's PKCS8 PRIVATE KEY, PEM, base64url-encoded. The box generates the keypair (the
 *           phone does not) and delivers both here, screen-to-camera , so enrollment is one scan with
 *           NO network call. The box wipes its copy after showing the QR; the phone imports these into
 *           its encrypted store (DeviceCert) and presents the cert on every later call. base64url
 *           (RFC 4648, URL-safe alphabet) so the PEM survives the query string untouched.
 *
 * Remote access (away from home) is out of scope for the QR: the user types their own DDNS or VPN
 * host into the setup screen. The QR is the LAN/first-contact path.
 *
 * Parsing is pure (no android.net.Uri) so it can be unit-tested. cert/key are OPTIONAL at parse time
 * (a v1 link still parses) but REQUIRED to actually enrol , the enrol flow rejects a link without
 * them, so old code-only links cannot silently half-enrol.
 */
data class EnrollLink(
    val host: String,
    val port: Int,
    val code: String,
    val certFingerprint: String,
    val boxName: String = "",
    val version: Int = CURRENT_VERSION,
    // v2: the box-issued device cert + its private key, delivered in the QR. Null for a v1 link.
    val deviceCertPem: String? = null,
    val deviceKeyPem: String? = null,
) {
    /** The base URL the phone will store and connect to. */
    fun baseUrl(): String = "https://$host:$port"

    /** The outcome of parsing a scanned/typed payload. */
    sealed interface Result {
        data class Ok(val link: EnrollLink) : Result
        /** Looked like an enrol link but was malformed (missing required field, bad port). */
        object Malformed : Result
        /** A valid enrol link from a NEWER box than this app understands. Tell the user to update. */
        data class Outdated(val sawVersion: Int) : Result
        /** Not an enrol link at all (wrong scheme). */
        object NotEnroll : Result
    }

    companion object {
        const val PREFIX = "localghost://enroll?"
        /** The newest enrol-link version this app understands. Bump when the format changes. v2 = +cert/key. */
        const val CURRENT_VERSION = 2

        /**
         * Parse a scanned/typed payload, keeping the version distinction. Use this where the caller
         * wants to tell "newer box, update the app" apart from "garbage".
         */
        fun parseResult(raw: String): Result {
            val text = raw.trim()
            val lower = text.lowercase()
            if (!lower.startsWith(PREFIX)) return Result.NotEnroll
            val params = parseQuery(text.substring(PREFIX.length))

            // version first: absent = 1 (original codes). A higher version than we know means the box
            // is ahead of the app, which is a clean "update" message rather than a parse failure.
            val version = params["v"]?.trim()?.toIntOrNull() ?: 1
            if (version > CURRENT_VERSION) return Result.Outdated(version)

            val host = params["host"]?.trim().orEmpty()
            val code = params["code"]?.trim().orEmpty()
            val fp = params["fp"]?.trim().orEmpty()
            val port = params["port"]?.trim()?.toIntOrNull() ?: 8443
            val name = params["name"]?.trim().orEmpty()
            // v2: the device cert + key, base64url-encoded PEM. Optional at parse time (v1 links lack
            // them); the enrol flow requires them. Malformed base64 -> treated as absent, not a hard fail.
            val certPem = decodeB64Url(params["cert"]?.trim().orEmpty())
            val keyPem = decodeB64Url(params["key"]?.trim().orEmpty())

            if (host.isEmpty() || code.isEmpty() || fp.isEmpty()) return Result.Malformed
            if (port !in 1..65535) return Result.Malformed

            return Result.Ok(
                EnrollLink(
                    host = host, port = port, code = code,
                    certFingerprint = normaliseFp(fp), boxName = name, version = version,
                    deviceCertPem = certPem, deviceKeyPem = keyPem,
                )
            )
        }

        /** Parse a scanned/typed payload. Returns null if it isn't a valid, current enroll link. */
        fun parse(raw: String): EnrollLink? =
            (parseResult(raw) as? Result.Ok)?.link

        private fun parseQuery(query: String): Map<String, String> =
            query.split("&").mapNotNull { pair ->
                val i = pair.indexOf('=')
                if (i <= 0) return@mapNotNull null
                val k = pair.substring(0, i)
                val v = pair.substring(i + 1)
                runCatching {
                    URLDecoder.decode(k, "UTF-8") to URLDecoder.decode(v, "UTF-8")
                }.getOrNull()
            }.toMap()

        /** Normalise a fingerprint to uppercase colon-separated hex for stable comparison. */
        fun normaliseFp(fp: String): String =
            fp.uppercase().filter { it.isLetterOrDigit() }
                .chunked(2).joinToString(":")

        /** Decode a base64url (RFC 4648, URL-safe, optional padding) value to its UTF-8 string, or null
         *  if empty/invalid. Pure JVM (java.util.Base64) so it stays unit-testable. */
        private fun decodeB64Url(v: String): String? {
            if (v.isEmpty()) return null
            return runCatching {
                String(java.util.Base64.getUrlDecoder().decode(v.trimEnd('=')), Charsets.UTF_8)
            }.getOrNull()
        }
    }
}
