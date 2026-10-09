package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Test

class SourcesTextTest {
    @Test fun theJobLine() {
        assertEquals("fetching the maps from the mirror · 12 min so far", SourcesText.job("maps", 1000, 0, true, 0, 1000 + 12 * 60))
        assertEquals("fetched Wikipedia in 1 h 03 min", SourcesText.job("wiki", 1000, 1000 + 3780, false, 0, 9999))
        assertEquals("the fetch of the speech engine stopped after 40 s (exit 1)", SourcesText.job("speech", 1000, 1040, false, 1, 9999))
    }

    @Test fun theRegionAroundAPosition() {
        assertEquals("36:67,-21:20", SourcesText.region(51.5, -0.1))
        assertEquals("74:90,159:180", SourcesText.region(89.0, 179.0))
        assertEquals("-90:-74,-180:-159", SourcesText.region(-89.0, -179.0))
    }

    @Test fun marks() {
        assertEquals("●", SourcesText.mark("ready"))
        assertEquals("◐", SourcesText.mark("importing"))
        assertEquals("○", SourcesText.mark("missing"))
        assertEquals("?", SourcesText.mark("whatever"))
    }
}
