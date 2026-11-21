package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class VoiceAskTextTest {
    @Test fun theLineWhileItListens() {
        assertEquals("● 0:00 listening · ask, it stops when you do", VoiceAskText.listening(0, heard = false, speaking = false))
        assertEquals("● 0:04 hearing you · ■ stops it now", VoiceAskText.listening(4, heard = true, speaking = true))
        assertEquals("● 1:05 a pause · it stops in a moment, or carry on", VoiceAskText.listening(65, heard = true, speaking = false))
    }

    @Test fun howItEnded() {
        assertEquals("nothing heard · tap the microphone, then ask", VoiceAskText.ended("nothing", ""))
        assertEquals("! the microphone stopped (code -3)", VoiceAskText.ended("", "the microphone stopped (code -3)"))
        assertEquals("", VoiceAskText.ended("dropped", ""))
    }

    @Test fun whileTheBoxWorks() {
        assertEquals("your box is working out the words… · ✕ drops it", VoiceAskText.working(0))
        assertEquals("your box is working out the words · 7s · ✕ drops it", VoiceAskText.working(7))
    }

    @Test fun theWordsAreInTheField() {
        assertEquals("heard in 4 s · read it, then send", VoiceAskText.heardIn(3600, false))
        assertEquals("heard in under a second · read it, then send", VoiceAskText.heardIn(400, false))
        assertEquals("heard · read it, then send", VoiceAskText.heardIn(0, false))
        assertTrue(VoiceAskText.heardIn(9000, true).endsWith("stopped at two minutes, the most a question holds"))
        assertTrue(VoiceAskText.isHeardLine(VoiceAskText.heardIn(3600, false)))
        assertFalse(VoiceAskText.isHeardLine(VoiceAskText.NOT_WORDS))
        assertFalse(VoiceAskText.isHeardLine(VoiceAskText.working(3)))
    }

    @Test fun thePhonesLanguages() {
        assertEquals("en,ro", VoiceAskText.langs(listOf("en-GB", "ro-RO", "en-US")))
        assertEquals("fil,en", VoiceAskText.langs(listOf("fil-PH", "EN")))
        assertEquals("", VoiceAskText.langs(listOf("", "x", "12-34", "toolong")))
        assertEquals("aa,bb,cc,dd,ee", VoiceAskText.langs(listOf("aa", "bb", "cc", "dd", "ee", "ff")))
    }
}
