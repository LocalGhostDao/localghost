package com.localghost.app.net

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Assert.assertNull
import org.junit.Assert.assertNotNull
import org.junit.Test

/**
 * Guards the enrollment-link parser. This is the QR trust anchor, so the tests pin that it refuses
 * anything without a host, code, AND fingerprint rather than enrolling insecurely.
 */
class EnrollLinkTest {

    @Test fun parsesFullLink() {
        val link = EnrollLink.parse(
            "localghost://enroll?host=192.168.1.20&port=8443&code=ABCD-1234&fp=ab:cd:ef&name=box")
        assertNotNull(link)
        link!!
        assertEquals("192.168.1.20", link.host)
        assertEquals(8443, link.port)
        assertEquals("ABCD-1234", link.code)
        assertEquals("box", link.boxName)
        assertEquals("https://192.168.1.20:8443", link.baseUrl())
    }

    @Test fun defaultsPortWhenAbsent() {
        val link = EnrollLink.parse("localghost://enroll?host=box.local&code=X1&fp=aa:bb")
        assertNotNull(link)
        assertEquals(8443, link!!.port)
    }

    @Test fun normalisesFingerprint() {
        val link = EnrollLink.parse("localghost://enroll?host=h&code=c&fp=abcdef")
        assertEquals("AB:CD:EF", link!!.certFingerprint)
    }

    @Test fun rejectsMissingFingerprint() {
        assertNull(EnrollLink.parse("localghost://enroll?host=h&code=c"))
    }

    @Test fun rejectsMissingCode() {
        assertNull(EnrollLink.parse("localghost://enroll?host=h&fp=aa:bb"))
    }

    @Test fun rejectsMissingHost() {
        assertNull(EnrollLink.parse("localghost://enroll?code=c&fp=aa:bb"))
    }

    @Test fun rejectsWrongScheme() {
        assertNull(EnrollLink.parse("https://enroll?host=h&code=c&fp=aa"))
        assertNull(EnrollLink.parse("random text"))
        assertNull(EnrollLink.parse(""))
    }

    @Test fun rejectsBadPort() {
        assertNull(EnrollLink.parse("localghost://enroll?host=h&code=c&fp=aa&port=99999"))
    }

    @Test fun v1LinkHasNoCertOrKey() {
        val link = EnrollLink.parse("localghost://enroll?host=h&code=c&fp=aa:bb")
        assertNotNull(link)
        assertNull(link!!.deviceCertPem)
        assertNull(link.deviceKeyPem)
    }

    @Test fun v2LinkCarriesCertAndKey() {
        // base64url of "CERT-PEM" and "KEY-PEM" (no padding), as the box would encode them.
        val certB64 = java.util.Base64.getUrlEncoder().withoutPadding()
            .encodeToString("CERT-PEM".toByteArray())
        val keyB64 = java.util.Base64.getUrlEncoder().withoutPadding()
            .encodeToString("KEY-PEM".toByteArray())
        val link = EnrollLink.parse(
            "localghost://enroll?v=2&host=h&port=8443&code=c&fp=aa:bb&cert=$certB64&key=$keyB64")
        assertNotNull(link)
        assertEquals(2, link!!.version)
        assertEquals("CERT-PEM", link.deviceCertPem)
        assertEquals("KEY-PEM", link.deviceKeyPem)
    }

    @Test fun v3LinkCarriesDerAndWrapsItAsPem() {
        // 70 DER bytes, so the PEM body spans two 64-column lines , the wrap must be exact for the
        // keystore's PEM reader downstream.
        val der = ByteArray(70) { (it * 7).toByte() }
        val b64url = java.util.Base64.getUrlEncoder().withoutPadding().encodeToString(der)
        val link = EnrollLink.parse(
            "localghost://enroll?v=3&host=h&port=8443&fp=aa:bb&certder=$b64url&keyder=$b64url")
        assertNotNull(link)
        assertEquals(3, link!!.version)
        val std = java.util.Base64.getEncoder().encodeToString(der)
        val expectCert = "-----BEGIN CERTIFICATE-----\n" + std.substring(0, 64) + "\n" + std.substring(64) +
            "\n-----END CERTIFICATE-----\n"
        assertEquals(expectCert, link.deviceCertPem)
        assertEquals("-----BEGIN PRIVATE KEY-----\n" + std.substring(0, 64) + "\n" + std.substring(64) +
            "\n-----END PRIVATE KEY-----\n", link.deviceKeyPem)
        // The PEM body decodes back to the same DER.
        val body = link.deviceCertPem!!.lines().filter { !it.startsWith("-----") }.joinToString("")
        assertArrayEquals(der, java.util.Base64.getDecoder().decode(body))
    }

    @Test fun v4LinkIsOutdatedNotMalformed() {
        val r = EnrollLink.parseResult("localghost://enroll?v=4&host=h&fp=aa&certder=QUJD")
        assertTrue(r is EnrollLink.Result.Outdated)
    }
}
