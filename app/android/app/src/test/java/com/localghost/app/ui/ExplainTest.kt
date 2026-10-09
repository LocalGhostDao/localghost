package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertTrue
import org.junit.Test

class ExplainTest {
    @Test fun everyPanelHasItsExplanation() {
        for (k in listOf("prices", "crypto", "news", "wikipedia", "weather", "maps", "memories", "checkin", "health", "gallery", "sources", "speech")) {
            val t = Explain.of(k)
            assertNotNull("no explanation for $k", t)
            assertTrue("$k is thin", t!!.paragraphs.size >= 2 && t.paragraphs.all { it.length > 40 })
            assertTrue("$k has an em dash", t.paragraphs.none { it.contains("—") })
        }
        assertEquals(Explain.of("prices"), Explain.of("crypto"))
    }
}
