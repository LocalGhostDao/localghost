package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

class WeatherHoursTest {
    private val hours = (0 until 48).map { i ->
        val day = if (i < 24) "2026-10-10" else "2026-10-11"
        WeatherHour("%sT%02d:00".format(day, i % 24), 8.0 + (i % 24) * 0.5, if (i in 15..20) 60 else 0, 0.0, 2, 10.0)
    }
    // 2026-10-10 12:30 UTC
    private val noonUtc = 1791635400L

    @Test fun theHourTheClockIsInAtThePlace() {
        assertEquals("2026-10-10T12:00", WeatherHours.keyAt(noonUtc, 0))
        assertEquals("2026-10-10T14:00", WeatherHours.keyAt(noonUtc, 7200))
        assertEquals("2026-10-10T07:00", WeatherHours.keyAt(noonUtc, -5 * 3600))
        assertEquals(14, WeatherHours.indexNow(hours, noonUtc, 7200))
        assertEquals(-1, WeatherHours.indexNow(hours, noonUtc + 3 * 86400, 7200))
    }

    @Test fun theCurrentIsTheHourOnceThePullIsOld() {
        assertNull("a fresh pull speaks for itself", WeatherHours.current(hours, noonUtc - 600, noonUtc, 7200))
        val c = WeatherHours.current(hours, noonUtc - 8 * 3600, noonUtc, 7200)
        assertEquals("2026-10-10T14:00", c?.at)
        assertEquals(15.0, c!!.tempC, 0.001)
        assertNull(WeatherHours.current(emptyList(), 0L, noonUtc, 0))
    }

    @Test fun theNextDayFromThisHour() {
        val n = WeatherHours.next(hours, noonUtc, 7200, 24)
        assertEquals(24, n.size)
        assertEquals("2026-10-10T14:00", n.first().at)
        assertEquals("2026-10-11T13:00", n.last().at)
        assertEquals(listOf(0, 6, 12, 18), WeatherHours.labelled(n))
        assertEquals("14", WeatherHours.hourOf(n.first().at))
        assertTrue(WeatherHours.next(hours, noonUtc + 5 * 86400, 0).isEmpty())
    }

    @Test fun theLineSitsBetweenTheColdestAndTheWarmest() {
        assertEquals(0f, WeatherHours.y(20.0, 8.0, 20.0), 0.001f)
        assertEquals(1f, WeatherHours.y(8.0, 8.0, 20.0), 0.001f)
        assertEquals(0.5f, WeatherHours.y(14.0, 8.0, 20.0), 0.001f)
        assertEquals(0.5f, WeatherHours.y(9.0, 9.0, 9.2), 0.001f)
    }
}
