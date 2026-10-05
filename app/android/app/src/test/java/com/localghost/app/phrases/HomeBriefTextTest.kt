package com.localghost.app.phrases

import com.localghost.app.phrases.HomeBriefText.Price
import com.localghost.app.phrases.HomeBriefText.Story
import org.junit.Assert.assertEquals
import org.junit.Test

class HomeBriefTextTest {
    private val now = 1_790_900_000L

    @Test fun theMostToldStoriesOfTheDayFirst() {
        val stories = listOf(
            Story(1, "Lone story", "", listOf("BBC"), now - 600, 1),
            Story(2, "Minister resigns", "The minister resigned on Tuesday.", listOf("BBC", "The Guardian", "FT"), now - 7200, 3),
            Story(3, "Rates held", "Rates held", listOf("FT", "Economist"), now - 300, 2),
            Story(4, "Old news", "", listOf("BBC", "DW", "NPR", "FT"), now - 3 * 86400, 4),
            Story(5, "Rates held again", "", listOf("BBC", "NPR"), now - 60, 2),
        )
        val picked = HomeBriefText.pick(stories, now, n = 3)
        assertEquals(listOf(2L, 5L, 3L), picked.map { it.id })
        val cards = HomeBriefText.cards(stories, now, n = 3)
        assertEquals("news:2", cards[0].id)
        assertEquals("The minister resigned on Tuesday.", cards[0].summary)
        assertEquals("", cards[2].summary) // the summary that only repeats the title is dropped
        assertEquals("BBC · The Guardian · FT · 2 h ago", cards[0].outlets)
        assertEquals("A · B · C · 2 more · 5 min ago", HomeBriefText.outlets(listOf("A", "B", "C", "D", "E"), now - 300, now))
    }

    @Test fun pricesAndChip() {
        val p = listOf(Price("ETH", 3250.0, -0.42, now), Price("BTC", 65000.4, 1.234, now), Price("SOL", 150.0, 2.0, now))
        assertEquals("BTC 65,000 +1.2% · ETH 3,250 -0.4%", HomeBriefText.prices(p))
        assertEquals("BTC 65.0k", HomeBriefText.chip(p))
        // the widget's big line: one coin a line, always in view
        assertEquals("BTC 65,000 +1.2%\nETH 3,250 -0.4%", HomeBriefText.stacked(HomeBriefText.prices(p)))
        assertEquals("BTC 65,000", HomeBriefText.stacked("BTC 65,000"))
        assertEquals("BTC 65,000", HomeBriefText.prices(listOf(Price("BTC", 65000.0, null, now))))
        assertEquals("", HomeBriefText.prices(emptyList()))
        assertEquals("", HomeBriefText.chip(emptyList()))
    }

    @Test fun pulledOpen() {
        val cards = listOf(
            HomeBriefText.Card("news:1", "One", "Summary one.", "BBC · 1 h ago"),
            HomeBriefText.Card("news:2", "Two", "", "FT · 2 h ago"),
            HomeBriefText.Card("news:3", "Three", "", "DW · 3 h ago"),
        )
        assertEquals("Summary one.\nBBC · 1 h ago\n\nBTC 65,000 +1.2%  ·  14:05\n\nnext   Two\nthen   Three",
            HomeBriefText.expanded(cards, 0, "BTC 65,000 +1.2%", "14:05"))
        assertEquals("FT · 2 h ago\n\nnext   Three\nthen   One", HomeBriefText.expanded(cards, 1, "", ""))
    }
}
