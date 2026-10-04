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
}
