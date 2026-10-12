package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class WeatherTextTest {
    private fun h(at: String, t: Double, rain: Int = 0, mm: Double = 0.0, code: Int = 1, wind: Double = 5.0, spread: Double = 0.0) =
        WeatherHour(at, t, rain, mm, code, wind, spread)

    @Test fun theRows() {
        assertEquals("14:00  18°  30%  0.4 mm  12 km/h  partly cloudy", WeatherText.hourRow(h("2026-10-13T14:00", 18.2, 30, 0.4, 2, 12.0)))
        assertEquals("03:00  9° ±2  mostly clear", WeatherText.hourRow(h("2026-10-13T03:00", 9.0, 0, 0.0, 1, 5.0, 4.0)))
        assertEquals("Tuesday 13 Oct  18/9  rain  60%  2.4 mm  ↑07:38 ↓18:41",
            WeatherText.dayRow("2026-10-13", 18.0, 9.0, 61, 60, 2.4, "07:38", "18:41"))
        assertEquals("Tuesday 13 Oct  18/9", WeatherText.dayRow("2026-10-13", 18.0, 9.0, -1, 0))
    }

    @Test fun theDays() {
        assertEquals("today", WeatherText.dayName("2026-10-12", "2026-10-12"))
        assertEquals("tomorrow", WeatherText.dayName("2026-10-13", "2026-10-12"))
        assertEquals("Wednesday 14 Oct", WeatherText.dayName("2026-10-14", "2026-10-12"))
        val g = WeatherText.byDay(listOf(h("2026-10-12T22:00", 1.0), h("2026-10-12T23:00", 1.0), h("2026-10-13T00:00", 1.0)))
        assertEquals(listOf("2026-10-12", "2026-10-13"), g.map { it.first })
        assertEquals(2, g[0].second.size)
    }

    @Test fun theSpreadAndTheSources() {
        assertEquals("", WeatherText.spreadLine(listOf(h("2026-10-12T22:00", 1.0))))
        assertEquals("the models agree to within 1°", WeatherText.spreadLine(listOf(h("2026-10-12T22:00", 1.0, spread = 1.0))))
        assertEquals("the models differ by up to 4°, most at Tue 03:00",
            WeatherText.spreadLine(listOf(h("2026-10-12T22:00", 1.0, spread = 1.0), h("2026-10-13T03:00", 1.0, spread = 4.0))))
        assertEquals(listOf("ICON-D2, the 09Z run of 2026-10-10", "IFS, the 06Z run of 2026-10-10"),
            WeatherText.sourceRows("icon-d2 2026-10-10 09Z · ifs 2026-10-10 06Z"))
        assertTrue(WeatherText.sourceRows("")[0].startsWith("pulled from the weather service"))
        assertTrue(WeatherText.modelNote("ICON-D2").startsWith("DWD, 2.2 km"))
        assertEquals("", WeatherText.modelNote("x"))
    }

    @Test fun theAge() {
        assertEquals("computed on the box 42 min ago", WeatherText.ageLine(1000, 3500, 3520, true))
        assertEquals("pulled by the box 2 h ago · this phone's copy from 5 min ago", WeatherText.ageLine(1000, 8000, 8300, false))
        assertEquals("not computed on the box yet", WeatherText.ageLine(0, 0, 100, true))
    }
}
