package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Test

class HealthStatusTest {
    private val now = 1_790_900_000L

    @Test fun saysWhereTheDataStands() {
        assertEquals("the box holds no day yet · this phone has not shipped yet", HealthStatus.line(null, 0, 0, "", "", "", now))
        assertEquals("the box holds days up to 2026-09-30 · last run 5 min ago: shipped 7 days, newest 2026-09-30 (skipped weight)",
            HealthStatus.line("2026-09-30", now - 300, 7, "2026-09-30", "", "weight", now))
        assertEquals("the box holds days up to 2026-09-28 · last try 2 h ago: box unreachable , is it unlocked?",
            HealthStatus.line("2026-09-28", now - 7200, 0, "", "box unreachable , is it unlocked?", "", now))
        assertEquals("the box holds days up to 2026-09-28 · last run just now: nothing found in Health Connect for the last week",
            HealthStatus.line("2026-09-28", now - 10, 0, "", "", "", now))
    }
}
