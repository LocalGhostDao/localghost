package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Test

class NotifLinkTest {
    @Test fun theBoxsLinkWins() {
        assertEquals(NotifLink.Target("map", "2026-09-28"), NotifLink.resolve("map:2026-09-28", "ghost.framed", "highlight"))
        assertEquals(NotifLink.Target("memories", "4182"), NotifLink.resolve("memories:4182", "ghost.cued", "reflection"))
        assertEquals(NotifLink.Target("memories", "near"), NotifLink.resolve("memories:near", "ghost.cued", "nearby"))
        assertEquals(NotifLink.Target("news"), NotifLink.resolve("news", "ghost.synthd", "news"))
        assertEquals(NotifLink.Target("status"), NotifLink.resolve("status", "ghost.watchd", "down"))
        // a day or an id that is not one is dropped, the place stays
        assertEquals(NotifLink.Target("map", ""), NotifLink.resolve("map:yesterday", "", ""))
        assertEquals(NotifLink.Target("day", "2025-10-02"), NotifLink.resolve("day:2025-10-02", "ghost.framed", "highlight"))
        assertEquals(NotifLink.Target("day", ""), NotifLink.resolve("day:soon", "", ""))
        assertEquals(NotifLink.Target("notification", "42"), NotifLink.resolve("notification:42", "", ""))
        assertEquals(NotifLink.Target("notification", ""), NotifLink.resolve("notification:x", "", ""))
        assertEquals(NotifLink.Target("memories", ""), NotifLink.resolve("memories:", "", ""))
    }

    @Test fun anOlderNotificationGoesByItsKind() {
        assertEquals(NotifLink.Target("map"), NotifLink.resolve("", "ghost.framed", "highlight"))
        assertEquals(NotifLink.Target("memories"), NotifLink.resolve("", "ghost.cued", "reflection"))
        assertEquals(NotifLink.Target("memories", "near"), NotifLink.resolve("", "ghost.cued", "nearby"))
        assertEquals(NotifLink.Target("checkin"), NotifLink.resolve("", "ghost.secd", "checkin"))  // a 0.0.4 box's reminder
        assertEquals(NotifLink.Target("checkin"), NotifLink.resolve("checkin", "ghost.secd", "checkin"))
        assertEquals(NotifLink.Target("news"), NotifLink.resolve("", "ghost.synthd", "news"))
        assertEquals(NotifLink.Target("status"), NotifLink.resolve("", "ghost.shadowd", "observation"))
        assertEquals(NotifLink.Target(""), NotifLink.resolve("", "ghost.noted", "message"))
    }

    @Test fun theShadesExtra() {
        assertEquals("map:2026-09-28", NotifLink.nav("map:2026-09-28", "ghost.framed", "highlight"))
        assertEquals("checkin", NotifLink.nav("", "ghost.secd", "checkin"))
        assertEquals("checkin", NotifLink.nav("checkin", "ghost.secd", "checkin"))
        assertEquals("memories:42", NotifLink.nav("memories:42", "ghost.cued", "reflection"))
        assertEquals("notifications", NotifLink.nav("", "ghost.noted", "message"))
        assertEquals("open on MAP ›", NotifText.opens(NotifLink.Target("map", "2026-09-28")))
        assertEquals("open near you ›", NotifText.opens(NotifLink.Target("memories", "near")))
        assertEquals("open the memory ›", NotifText.opens(NotifLink.Target("memories", "42")))
        assertEquals("open in MEMORIES ›", NotifText.opens(NotifLink.Target("memories", "")))
        assertEquals("open CHECK-IN ›", NotifText.opens(NotifLink.Target("checkin")))
    }

    @Test fun thePage() {
        assertEquals("cued · something nearby", NotifPage.who("ghost.cued", "nearby"))
        assertEquals("noted", NotifPage.who("ghost.noted", "message"))
        assertEquals("day", NotifPage.shows(NotifLink.Target("day", "2026-09-28")))
        assertEquals("day", NotifPage.shows(NotifLink.Target("map", "2026-09-28")))
        assertEquals("memory", NotifPage.shows(NotifLink.Target("memories", "42")))
        assertEquals("near", NotifPage.shows(NotifLink.Target("memories", "near")))
        assertEquals("", NotifPage.shows(NotifLink.Target("", "")))
        assertEquals("2026-09-28", NotifPage.dayOf(NotifLink.Target("map", "2026-09-28")))
        assertEquals(42L, NotifPage.memoryOf(NotifLink.Target("memories", "42")))
        assertEquals(0L, NotifPage.memoryOf(NotifLink.Target("memories", "near")))
        assertEquals("the whole day ›", NotifPage.goes(NotifLink.Target("day", "2026-09-28")))
        assertEquals("the memory ›", NotifPage.goes(NotifLink.Target("memories", "42")))
        assertEquals("checkin", NotifPage.shows(NotifLink.Target("checkin")))
        assertEquals("CHECK-IN ›", NotifPage.goes(NotifLink.Target("checkin")))
    }

    @Test fun theDigestIsPoints() {
        // the box's digest since 0.0.7: a bullet a story
        assertEquals(listOf("Rates held at 4%. · 3 outlets", "A lone story"),
            NotifPage.points("news", "• Rates held at 4%. · 3 outlets\n• A lone story"))
        // an older digest, bare lines: still one story a line
        assertEquals(listOf("Rates held at 4%. (3 outlets)", "A lone story"),
            NotifPage.points("news", "Rates held at 4%. (3 outlets)\nA lone story"))
        assertEquals(listOf("one story"), NotifPage.points("news", "one story"))
        // another kind's body stays prose unless every line is marked
        assertEquals(emptyList<String>(), NotifPage.points("reflection", "A year ago today.\nYou were by the sea."))
        assertEquals(listOf("a", "b"), NotifPage.points("observation", "- a\n- b"))
        assertEquals("Rates held at 4%. · 3 outlets", NotifPage.firstLine("• Rates held at 4%. · 3 outlets\n• A lone story"))
        assertEquals("plain", NotifPage.firstLine("plain"))
        assertEquals("Rates held at 4%. · 3 outlets … and 1 more", NotifPage.preview("news", "• Rates held at 4%. · 3 outlets\n• A lone story"))
        assertEquals("A year ago today.\nYou were by the sea.", NotifPage.preview("reflection", "A year ago today.\nYou were by the sea."))
        assertEquals("you answered: yes", NotifPage.askLine(listOf("yes", "no"), "yes"))
        assertEquals("waiting for your answer", NotifPage.askLine(listOf("yes", "no"), ""))
        assertEquals("", NotifPage.askLine(emptyList(), ""))
    }
}
