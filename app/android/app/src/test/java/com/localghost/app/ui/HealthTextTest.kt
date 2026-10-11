package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.LocalDate

class HealthTextTest {
    private val days = listOf("2026-10-04", "2026-10-05", "2026-10-06", "2026-10-07", "2026-10-08", "2026-10-09", "2026-10-10")
    private val steps = listOf(8000.0, 12000.0, 9000.0, 15000.0, 11000.0, 7000.0, 15909.0)

    @Test fun valuesInTheirUnits() {
        assertEquals("15,909", HealthText.fmt("steps", 15909.0))
        assertEquals("7h12m", HealthText.fmt("sleep_minutes", 432.0))
        assertEquals("61 bpm", HealthText.fmt("resting_hr", 61.4))
        assertEquals("97%", HealthText.fmt("spo2_pct", 97.2))
        assertEquals("1.3 km", HealthText.fmt("distance_km", 1.26))
        assertEquals("+1,204", HealthText.fmtDelta("steps", 1204.0))
        assertEquals("−0h20m", HealthText.fmtDelta("sleep_minutes", -20.0))
        assertEquals("±0", HealthText.fmtDelta("steps", 0.0))
    }

    @Test fun theWindowCutsByDate() {
        val (d, v) = HealthText.window(days, steps, "2026-10-08")
        assertEquals(listOf("2026-10-08", "2026-10-09", "2026-10-10"), d)
        assertEquals(listOf(11000.0, 7000.0, 15909.0), v)
        assertEquals(days to steps, HealthText.window(days, steps, ""))
        assertEquals("2026-10-04", HealthText.since(LocalDate.of(2026, 10, 10), 7))
        assertEquals("", HealthText.since(LocalDate.of(2026, 10, 10), 0))
    }

    @Test fun theSummaryNamesTheDays() {
        val s = HealthText.summary(days, steps)!!
        assertEquals(7, s.n)
        assertEquals(15909.0, s.latest, 0.0)
        assertEquals("2026-10-10", s.latestDay)
        assertEquals(7000.0, s.min, 0.0)
        assertEquals("2026-10-09", s.minDay)
        assertEquals(15909.0, s.max, 0.0)
        assertEquals("2026-10-10", s.maxDay)
        assertEquals(11000.0, s.median, 0.0)
        assertEquals(77909.0, s.total, 0.0)
        assertNull(HealthText.summary(emptyList(), emptyList()))
        val lines = HealthText.statLines("steps", s, "7-day")
        assertEquals("latest 15,909 · 2026-10-10", lines[0])
        assertTrue(lines.last().startsWith("total 77,909 over 7 days"))
        assertTrue(HealthText.statLines("resting_hr", s, "7-day").last().contains("with a reading"))
    }

    @Test fun theTrendCountsByCalendar() {
        // the last 3 days against the 3 before: (11000+7000+15909)/3 vs (12000+9000+15000)/3
        val t = HealthText.trend(days, steps, LocalDate.of(2026, 10, 10), 3)!!
        assertEquals(-5.8, t, 0.1)
        assertEquals("last 3 days −6% on the 3 before", HealthText.trendLine(days, steps, LocalDate.of(2026, 10, 10), 3))
        // a window too short for the period before says nothing rather than a number from one day
        assertEquals("", HealthText.trendLine(days.takeLast(2), steps.takeLast(2), LocalDate.of(2026, 10, 10), 3))
    }

    @Test fun theWeekdays() {
        // 2026-10-04 is a Sunday
        val m = HealthText.weekdayMeans(days, steps)
        assertEquals(12000.0, m[0]!!, 0.0) // Monday the 5th
        assertEquals(8000.0, m[6]!!, 0.0)  // Sunday the 4th
        assertEquals("most on Saturdays (15,909), least on Fridays (7,000)", HealthText.weekdayLine("steps", m))
        assertEquals("", HealthText.weekdayLine("steps", listOf(1.0, null, null, null, null, null, null)))
    }

    @Test fun theSmallWords() {
        assertEquals("+1,204 on the 30-day mean of 14,705", HealthText.againstMean("steps", 15909.0, 14705.0, "30-day"))
        assertEquals(90.0, HealthText.sleepEfficiency(480.0, 48.0)!!, 0.0)
        assertNull(HealthText.sleepEfficiency(0.0, 0.0))
        assertEquals("steps 15,909 · +1,204 on the 30-day mean", HealthText.dayLine("steps", 15909.0, 14705.0))
        assertEquals("weight 80.2 kg", HealthText.dayLine("weight_kg", 80.2, null))
        assertEquals("52..134 bpm · lowest 03:40 · highest 18:05 · 3 readings",
            HealthText.samplesLine(listOf(1L to 60.0, 2L to 52.0, 3L to 134.0)) { if (it == 2L) "03:40" else "18:05" })
        assertEquals("no heart rate readings kept for the day", HealthText.samplesLine(emptyList()) { "" })
        assertEquals("7 days · 2026-10-04 to 2026-10-10 · 3 kinds", HealthText.windowLine(days, 3, HealthText.windows[0]))
        assertEquals("no days in this window", HealthText.windowLine(emptyList(), 0, HealthText.windows[0]))
    }

    @Test fun everyMetricHasAGroup() {
        HealthText.metrics.forEach { assertTrue(it.key, it.group in HealthText.groups) }
        assertEquals("steps", HealthText.label("steps"))
        assertEquals("deep sleep", HealthText.label("sleep_deep_minutes"))
        assertEquals("an unknown one", HealthText.label("an_unknown_one"))
        assertEquals(0, HealthText.order("steps"))
        assertEquals(99, HealthText.order("an_unknown_one"))
    }
}
