package com.localghost.app.sync

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class HealthVerdictTest {
    @Test fun theVerdictNamesTheWall() {
        assertEquals("Health Connect is not available on this phone", HealthVerdict.of(false, 0, emptyList(), true))
        assertTrue(HealthVerdict.of(true, 15, emptyList(), true).startsWith("no Health Connect permission"))
        val empty = listOf("steps: nothing in Health Connect", "sleep: nothing in Health Connect")
        assertTrue(HealthVerdict.of(true, 0, empty, true).contains("Samsung Health is installed but not sharing"))
        assertTrue(HealthVerdict.of(true, 0, empty, false).contains("no app writes"))
        assertTrue(HealthVerdict.of(true, 0, listOf("steps: 300 record(s)", "sleep: not readable (x)"), true).startsWith("1 type(s) not readable"))
        assertTrue(HealthVerdict.of(true, 0, listOf("steps: 300 record(s)", "sleep: nothing in Health Connect"), true).startsWith("1 type(s) empty"))
        assertTrue(HealthVerdict.of(true, 2, listOf("steps: 300 record(s)"), true).startsWith("2 permission(s)"))
        assertTrue(HealthVerdict.of(true, 0, listOf("steps: 300 record(s)"), true).startsWith("Health Connect reads"))
    }
}
