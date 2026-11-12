package com.localghost.app.security

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/** A paired phone does not take another box's code on sight (second audit, 12 Nov 2026). */
class BoxChangeTest {
    private val a = "AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89"
    private val b = "11:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89:AB:CD:EF:01:23:45:67:89"

    @Test fun theSameBoxAgainIsNotAnotherBox() {
        assertFalse(BoxChange.isAnotherBox(a, a))
        assertFalse(BoxChange.isAnotherBox(a, a.lowercase().replace(":", ""))) // the same pin, written differently
        assertFalse(BoxChange.isAnotherBox("", a))   // not paired: the first scan
        assertFalse(BoxChange.isAnotherBox("  ", a))
    }

    @Test fun aDifferentPinIsAnotherBox() {
        assertTrue(BoxChange.isAnotherBox(a, b))
        assertTrue(BoxChange.isAnotherBox(a, ""))    // a code with no pin is not this box's
        assertTrue(BoxChange.isAnotherBox(a, a.dropLast(2)))
    }

    @Test fun theNoticeNamesBothBoxes() {
        val n = BoxChange.notice("https://lgs.example.com:8443", "203.0.113.9")
        assertTrue(n, n.contains("203.0.113.9") && n.contains("lgs.example.com") && n.contains("ENROL"))
        assertEquals("lgs.example.com", BoxChange.host(" https://lgs.example.com:8443/ "))
        assertEquals("192.168.1.100", BoxChange.host("https://192.168.1.100"))
        assertEquals("the box it has", BoxChange.host("not a url"))
        assertEquals("the box it has", BoxChange.host(""))
    }
}
