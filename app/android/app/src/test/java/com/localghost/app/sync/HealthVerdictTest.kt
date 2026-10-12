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
        assertEquals("nothing in Health Connect for sleep, heart rate (${HealthVerdict.SAMSUNG_PATH}); 1 kind(s) read",
            HealthVerdict.of(true, 0, listOf("steps: 300 record(s) from com.sec.android.app.shealth", "sleep: nothing in Health Connect", "heart rate: nothing in Health Connect"), true))
        // Vlad's phone on 12 Oct 2026: steps from another app, every other kind empty, Samsung Health installed and silent
        assertEquals("Samsung Health writes nothing to Health Connect (what is there, another app wrote): ${HealthVerdict.SAMSUNG_PATH}",
            HealthVerdict.of(true, 0, listOf("steps: 3000+ record(s) from com.other.j3caaf", "sleep: nothing in Health Connect"), true, samsungWrites = false))
        assertEquals("Samsung Health writes nothing to Health Connect: ${HealthVerdict.SAMSUNG_PATH}",
            HealthVerdict.of(true, 0, listOf("steps: nothing in Health Connect", "sleep: nothing in Health Connect"), true, samsungWrites = false))
        assertTrue(HealthVerdict.of(true, 0, listOf("steps: 300 record(s)", "sleep: nothing in Health Connect"), false).contains("(no app writes them)"))
        assertTrue(HealthVerdict.of(true, 2, listOf("steps: 300 record(s)"), true).startsWith("2 permission(s)"))
        assertTrue(HealthVerdict.of(true, 0, listOf("steps: 300 record(s)"), true).startsWith("Health Connect reads"))
    }
}
