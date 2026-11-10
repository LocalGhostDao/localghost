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
        assertEquals("2026-09-12", MemoryText.dayOf("trip:2026-09-12"))
        assertEquals("day", MemoryText.partLabel("day"))
        assertEquals("outing", MemoryText.partLabel("outing"))
    }

    @Test fun theKindAndTheOrigin() {
        assertEquals("AN OUTING", MemoryText.kindLabel("outing"))
        assertEquals("A TRIP", MemoryText.kindLabel("trip"))
        assertEquals("a trip, from your outings and days · 8 days · 160 photos", MemoryText.origin("trip", "8 days · 160 photos", ""))
        assertEquals("a trip, from your outings and days", MemoryText.origin("trip", null, ""))
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

    @Test fun readAgainLines() {
        val day = { _: Long -> "9 November" }
        assertEquals("the box read the conversation again on 9 November and is not sure of this. It says: the boat sleeps four",
            MemoryText.doubtLine("chat:2", "the boat sleeps four", 1L, day))
        assertEquals("the box read the note again and could not find this in it", MemoryText.doubtLine("note:x", "", 0L, day))
        assertEquals("the voice note", MemoryText.recordName("voice:12"))
        assertEquals("read again on 9 November, as written", MemoryText.confirmedLine(1L, day))
        assertEquals(null, MemoryText.confirmedLine(0L, day))
        assertEquals("Cristina owns the boat. , the record says: it is Vlad's", MemoryText.factDoubt("Cristina owns the boat.", "it is Vlad's"))
        assertEquals("Cristina owns the boat. , not found in the record it came from", MemoryText.factDoubt("Cristina owns the boat.", "the record does not say this"))
    }
}
