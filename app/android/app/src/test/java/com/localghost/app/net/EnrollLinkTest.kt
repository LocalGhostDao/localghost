package com.localghost.app.net

import org.junit.Assert.assertEquals
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
}
