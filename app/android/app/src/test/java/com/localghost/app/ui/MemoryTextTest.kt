package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Test

class MemoryTextTest {
    @Test fun theDayAMemoryOpens() {
        assertEquals("2026-10-03", MemoryText.dayOf("day:2026-10-03"))
        assertEquals("2026-09-28", MemoryText.dayOf("outing:2026-09-28"))
        assertEquals("", MemoryText.dayOf(""))
        assertEquals("", MemoryText.dayOf("chat:12"))
        assertEquals("", MemoryText.dayOf("day:soon"))
    }

    @Test fun theKindAndTheOrigin() {
        assertEquals("AN OUTING", MemoryText.kindLabel("outing"))
        assertEquals("A DAY", MemoryText.kindLabel("episode"))
        assertEquals("A MEMORY", MemoryText.kindLabel("distilled"))
        assertEquals("WRITTEN BY ME", MemoryText.kindLabel("user"))
        assertEquals("yours", MemoryText.origin("user", null, ""))
        assertEquals("noticed by your box", MemoryText.origin("insight", null, ""))
        assertEquals("from your photos · 24 photos · 3 days", MemoryText.origin("outing", "24 photos · 3 days", ""))
        assertEquals("from your photos", MemoryText.origin("outing", null, ""))
        assertEquals("a day, from your trail and photos · home → Greenwich → home", MemoryText.origin("day", null, "home → Greenwich → home"))
        assertEquals("a day, from your trail and photos", MemoryText.origin("day", null, ""))
    }
}
