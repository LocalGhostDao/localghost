package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class WikiTextTest {
    @Test fun theStateLine() {
        assertEquals("Wikipedia, 2026-06 · 6.9 million articles, 11.2 million redirects, on your box",
            WikiText.state("ready", "Wikipedia, 2026-06", 6_900_000, 11_200_000, 0, 0, ""))
        assertEquals("importing: 42% of the file read, 2.1 million articles in so far · a title search finds what is in already",
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

    @Test fun statsLines() {
        val lines = WikiText.stats(6_900_000, 11_200_000, 25L shl 30, 1_791_200_000, 1_791_215_000, 1204, true, true, 37, "ready")
        assertEquals(4, lines.size)
        assertEquals("6.9 million articles · 11.2 million redirects · 25.0 GB in the database", lines[0])
        assertTrue(lines[1].startsWith("imported ") && lines[1].contains("(took 4 hours)") && lines[1].endsWith("1,204 entries skipped"))
        assertEquals("search: by title and prefix, the words of a lead, and close spellings", lines[2])
        assertEquals("answered 37 chat questions since the box started", lines[3])
        val building = WikiText.stats(10, 2, 0, 0, 0, 0, false, false, 0, "ready")
        assertTrue(building[1].contains("being built"))
        assertEquals(1, WikiText.stats(10, 2, 0, 1, 0, 0, false, false, 0, "importing").size)
        assertTrue(WikiText.state("importing", "", 1, 1, 50, 100, "", 30, 4).contains("(4 readers)"))
    }
}
