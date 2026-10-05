package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class WikiTextTest {
    @Test fun theStateLine() {
        assertEquals("Wikipedia, 2026-06 · 6.9 million articles, 11.2 million redirects, on your box",
            WikiText.state("ready", "Wikipedia, 2026-06", 6_900_000, 11_200_000, 0, 0, ""))
        assertEquals("importing: 42% of the file read, 2.1 million articles in so far · the chat uses it once it is all in",
            WikiText.state("importing", "", 2_100_000, 0, 42, 100, ""))
        assertEquals("the import stopped: disk full · tried again every minute", WikiText.state("failed", "", 0, 0, 0, 0, "disk full"))
        assertEquals("1,234", WikiText.millions(1234))
        assertEquals("a redirect", WikiText.how("redirect"))
    }

    @Test fun aBodyIntoParts() {
        val parts = WikiText.parts("== History ==\nBuilt in 1889.\nCriticised.\n\n== Design ==\n330 metres tall.")
        assertEquals(2, parts.size)
        assertEquals("History", parts[0].heading)
        assertEquals("Built in 1889.\nCriticised.", parts[0].text)
        assertEquals("Design", parts[1].heading)
        assertEquals(0, WikiText.parts("").size)
    }

    @Test fun saysHowLongIsLeft() {
        assertEquals("40 minutes", WikiText.left(40))
        assertEquals("1 minute", WikiText.left(1))
        assertEquals("3 hours", WikiText.left(170))
        assertEquals("22 hours", WikiText.left(1320))
        assertEquals("2 days", WikiText.left(3000))
        assertTrue(WikiText.state("importing", "", 28877, 65710, 120000, 19707096, "", 1320).contains("about 22 hours to go"))
        assertFalse(WikiText.state("importing", "", 1, 1, 1, 100, "").contains("to go"))
    }
}
