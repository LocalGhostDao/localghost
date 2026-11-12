package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class MemoryKindsTest {
    @Test fun aPartShowsUnderItsWholeNotUnderAll() {
        assertTrue(MemoryKinds.shown("all", "trip", ""))
        assertTrue(MemoryKinds.shown("all", "day", ""))
        assertFalse(MemoryKinds.shown("all", "day", "trip:2026-09-12"))
        assertFalse(MemoryKinds.shown("all", "outing", "trip:2026-09-12"))
        assertTrue(MemoryKinds.shown("days", "day", "trip:2026-09-12"))      // the chip for the kind still lists it
        assertTrue(MemoryKinds.shown("outings", "outing", "trip:2026-09-12"))
        assertTrue(MemoryKinds.shown("trips", "trip", ""))
        assertFalse(MemoryKinds.shown("trips", "outing", ""))
        assertEquals(mapOf("all" to 3, "trips" to 1, "days" to 1, "outings" to 1), MemoryKinds.counts(listOf("trip", "day", "outing")).filterValues { it > 0 })
    }

    @Test fun anAnchorIsNeverOneTapDown() {
        // a day folded under a trip or a month is shown by the whole; marked as an anchor it shows on its own too
        assertFalse(MemoryKinds.shown("all", "day", "month:2026-08"))
        assertTrue(MemoryKinds.shown("all", "day", "month:2026-08", anchor = true))
        assertTrue(MemoryKinds.shown("all", "day", "trip:2026-09-12", anchor = true))
        // the anchors chip holds what the person marked, whatever kind it is, and nothing else
        assertTrue(MemoryKinds.shown("anchors", "day", "trip:2026-09-12", anchor = true))
        assertTrue(MemoryKinds.shown("anchors", "user", "", anchor = true))
        assertFalse(MemoryKinds.shown("anchors", "user", ""))
        assertFalse(MemoryKinds.matches("anchors", "trip"))
        // an anchor still shows under its own kind
        assertTrue(MemoryKinds.shown("days", "day", "", anchor = true))
    }

    @Test fun monthsHaveAChipAndTheAnchorsAreCounted() {
        assertTrue(MemoryKinds.shown("months", "month", ""))
        assertTrue(MemoryKinds.shown("all", "month", ""))
        assertFalse(MemoryKinds.shown("months", "day", "month:2026-08"))
        val c = MemoryKinds.counts(listOf("month", "day", "day", "user"), anchors = 2)
        assertEquals(4, c["all"]); assertEquals(2, c["anchors"]); assertEquals(1, c["months"]); assertEquals(2, c["days"])
        assertEquals(0, MemoryKinds.counts(listOf("day"))["anchors"])
        // the anchors come second, straight after "all": the person's own marks before the box's sorting
        assertEquals(listOf("all", "anchors"), MemoryKinds.all.take(2).map { it.id })
    }
}
