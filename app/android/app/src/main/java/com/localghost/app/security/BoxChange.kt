package com.localghost.app.security

/**
 * A SCANNED CODE FOR ANOTHER BOX. Enrolment is one scan: the code carries the box's address, the
 * pin of its certificate and this phone's key, and the phone used to take whatever code the
 * camera saw the moment it read it, a phone already paired included. Someone who gets the
 * owner to scan their code ("scan this to fix the connection") would have had the phone paired
 * with their box in two seconds: the next PIN typed goes to it, and so does everything the
 * phone sends from then on (second audit, 12 Nov 2026).
 *
 * So a phone that is paired takes a code for the SAME box as before (a new key for the phone,
 * the pin unchanged) and stops at a code for a different one: it says which box the code is
 * for and which the phone is paired with, and pairs only when the person says ENROL.
 *
 * Pure, so the tests read it.
 */
object BoxChange {
    private fun pin(fp: String): String = fp.uppercase().filter { it in '0'..'9' || it in 'A'..'F' }

    /** Whether [scannedPin] is another box's than [pairedPin]. A phone with no pairing (or an
     *  old one with no pin kept) has nothing to compare with: false. */
    fun isAnotherBox(pairedPin: String, scannedPin: String): Boolean {
        val was = pin(pairedPin)
        return was.isNotEmpty() && was != pin(scannedPin)
    }

    /** The host of a box address ("https://lgs.example.com:8443" gives "lgs.example.com"). */
    fun host(baseUrl: String): String =
        runCatching { java.net.URI(baseUrl.trim()).host }.getOrNull()?.takeIf { it.isNotBlank() } ?: "the box it has"

    /** What the set-up screen says before the person chooses. */
    fun notice(pairedUrl: String, scannedHost: String): String =
        "This code is for another box (" + scannedHost.ifBlank { "no address" } + "), not the one this phone is paired with (" +
            host(pairedUrl) + "). ENROL pairs the phone with the new box: your PIN and everything the phone sends from then on go to it. " +
            "If you did not set that box up yourself, go back."
}
