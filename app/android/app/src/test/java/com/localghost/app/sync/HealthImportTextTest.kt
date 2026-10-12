package com.localghost.app.sync

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class HealthImportTextTest {
    @Test fun theScanLines() {
        val s = HealthImportText.Scan("samsung-health-1.zip", listOf(
            HealthImportText.Table("tracker.heart_rate", 120000, "2024-01-02", "2026-10-12", "heart rate: the day's mean, lowest and highest, and the readings thinned to five minutes", ""),
            HealthImportText.Table("food_info", 3, "", "", "not read", ""),
            HealthImportText.Table("sleep", 900, "2024-01-02", "2026-10-11", "sleep minutes, under the day the night ends", "")), 1010, "")
        val l = HealthImportText.lines(s)
        assertEquals("tracker.heart_rate: 120,000 rows · 2024-01-02..2026-10-12 · heart rate: the day's mean, lowest and highest, and the readings thinned to five minutes", l[0])
        assertEquals("not read (1): food_info", l[2])
        assertEquals("1010 days named across the tables the box reads", l[3])
    }

    @Test fun theProgressLines() {
        assertTrue(HealthImportText.progressLine(HealthImportText.Progress("reading", "sleep", 3, 1200, 0, 0, 0, "", "", "")).startsWith("reading sleep · 3 tables, 1,200 rows"))
        assertEquals("done · 1,010 days from 2024-01-02 to 2026-10-12, 150,000 heart rate readings, in 34 months",
            HealthImportText.progressLine(HealthImportText.Progress("done", "", 9, 0, 1010, 34, 150000, "2024-01-02", "2026-10-12", "")))
        assertEquals("! failed: not a zip", HealthImportText.progressLine(HealthImportText.Progress("failed", "", 0, 0, 0, 0, 0, "", "", "not a zip")))
    }
}
