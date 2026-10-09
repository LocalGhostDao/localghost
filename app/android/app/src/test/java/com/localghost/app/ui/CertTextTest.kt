package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Test

class CertTextTest {
    @Test fun theLine() {
        val h = 3600_000L
        val d = 24 * h
        assertEquals("this phone's key: made 3 h ago, good for 13 more days · renewed once a day while the phone talks to the box",
            CertText.line(1_000_000_000L, 1_000_000_000L + 14 * d - 2 * h, 1_000_000_000L + 3 * h))
        assertEquals("this phone's key: made 20 days ago, ran out · renewed once a day while the phone talks to the box",
            CertText.line(0L, 14 * d, 20 * d))
        assertEquals("this phone's key: made 5 min ago, good for 13 more days · renewed once a day while the phone talks to the box",
            CertText.line(0L, 14 * d, 5 * 60_000L))
        assertEquals("this phone's key: made 12 days ago, good for 30 more h · renewed once a day while the phone talks to the box",
            CertText.line(0L, 14 * d, 14 * d - 30 * h))
    }
}
