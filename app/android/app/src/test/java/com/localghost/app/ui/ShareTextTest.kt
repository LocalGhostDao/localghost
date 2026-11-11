package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class ShareTextTest {
    @Test fun short_text_is_shown_whole_with_its_size() {
        assertEquals("milk, eggs\n\n10 characters. Shared from another app.", ShareText.preview("milk, eggs"))
    }

    @Test fun long_text_is_cut_and_says_so() {
        val p = ShareText.preview("a".repeat(2000))
        assertTrue(p.startsWith("a".repeat(ShareText.SHOWN) + "…"))
        assertTrue(p.contains("2000 characters, the first 600 shown"))
    }

    @Test fun lines_are_counted_and_controls_do_not_hide() {
        val p = ShareText.preview("Daily check-in 2026-11-09\nFeeling: awful\u0000\u001b[2K")
        assertTrue(p.contains("2 lines"))
        assertFalse(p.contains("\u0000"))
        assertFalse(p.contains("\u001b"))
        assertTrue(p.startsWith("Daily check-in 2026-11-09\nFeeling: awful"))
    }
}
