package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Test

class SettingsTextTest {
    private fun mo(hasKey: Boolean, order: String = "", run: String = "", skipped: String = "", error: String = "",
                   known: Boolean = false, near: String = "", region: String = "", note: String = "") =
        MetOfficeState(hasKey, order, run, skipped, error, known, near, region, 0, note)

    @Test fun theMetOfficeLines() {
        assertEquals("the box did not answer", SettingsText.metOfficeLine(null))
        assertEquals("no key yet · the box forecasts from the free models (IFS, ICON, GFS) without it", SettingsText.metOfficeLine(mo(false)))
        assertEquals("key set · order my-order · not pulled yet", SettingsText.metOfficeLine(mo(true, "my-order")))
        assertEquals("key set · order my-order · last run 2026-10-10 06Z", SettingsText.metOfficeLine(mo(true, "my-order", run = "2026-10-10 06Z")))
        assertEquals("key set · order my-order · the phone is not in its region", SettingsText.metOfficeLine(mo(true, "my-order", skipped = "the phone is not in its region")))
        assertEquals("key set · order my-order · 404", SettingsText.metOfficeLine(mo(true, "my-order", run = "x", error = "404")))
        assertEquals("home, by the trail's nights: near London · give the order a region of about 50.5 to 52.5 N, -1.1 to 0.9 E",
            SettingsText.homeLine(mo(true, known = true, near = "London", region = "50.5 to 52.5 N, -1.1 to 0.9 E")))
        assertEquals("the box needs a few nights", SettingsText.homeLine(mo(true, note = "the box needs a few nights")))
    }
}
