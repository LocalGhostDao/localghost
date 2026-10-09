package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Test

class HomeTextTest {
    private val now = 1_790_900_000L

    @Test fun homeShowsBtcAndEthOnly() {
        val coins = listOf(HomeText.liveOnly("SOL", 117.6, 2.0), HomeText.liveOnly("ETH", 2691.59, -1.2), HomeText.liveOnly("BTC", 84497.68, 0.94))
        assertEquals(listOf("BTC", "ETH"), HomeText.pinned(coins).map { it.symbol })
        assertEquals("84,498", HomeText.money(84497.68))
        assertEquals("117.60", HomeText.money(117.6))
        assertEquals("0.2512", HomeText.money(0.25123))
        assertEquals("+0.9%", HomeText.change(0.94))
        assertEquals("-1.2%", HomeText.change(-1.2))
        assertEquals("", HomeText.change(null))
        assertEquals("1.69T", HomeText.cap(1.69e12))
        assertEquals("320.0B", HomeText.cap(3.2e11))
    }

    @Test fun cryptoListTakesTheBoxsOwnPrice() {
        val ranks = listOf(
            HomeText.Coin(2, "ETH", "Ethereum", 2690.0, -1.0, 3.2e11),
            HomeText.Coin(1, "BTC", "Bitcoin", 84000.0, 0.5, 1.69e12),
            HomeText.Coin(3, "XRP", "XRP", 1.49, 2.0, 8.9e10),
        )
        val live = mapOf("BTC" to (84497.68 to 0.94), "ETH" to (2691.59 to null))
        val rows = HomeText.merge(ranks, live, n = 2)
        assertEquals(listOf("BTC", "ETH"), rows.map { it.symbol })
        assertEquals(84497.68, rows[0].usd, 1e-9)
        assertEquals(0.94, rows[0].change24!!, 1e-9)
        assertEquals(-1.0, rows[1].change24!!, 1e-9) // the box had no change for ETH: the list's
    }

    @Test fun briefAge() {
        assertEquals("", HomeText.written(0, now))
        assertEquals("written just now", HomeText.written(now - 30, now))
        assertEquals("written 23 min ago", HomeText.written(now - 23 * 60, now))
        assertEquals("written 3 h ago", HomeText.written(now - 3 * 3600, now))
    }

    @Test fun capFollowsTheShownPriceTimesSupply() {
        val ranks = listOf(HomeText.Coin(1, "BTC", "Bitcoin", 84000.0, 0.5, 1.69e12, supply = 20_000_000.0),
            HomeText.Coin(2, "XRP", "XRP", 1.5, 2.0, 8.9e10))
        val rows = HomeText.merge(ranks, mapOf("BTC" to (85000.0 to 1.0)))
        assertEquals(1.7e12, rows[0].cap, 1.0)
        assertEquals(8.9e10, rows[1].cap, 0.0) // no supply: the list's own cap
    }

    @Test fun fastPricesSitOverTheMinute() {
        val live = mapOf("BTC" to (84000.0 to 0.5), "ETH" to (2690.0 to null))
        val fast = mapOf("BTC" to (84123.0 to 0.6), "SOL" to (0.0 to null))
        val m = HomeText.withFast(live, fast)
        assertEquals(84123.0, m.getValue("BTC").first, 0.0)
        assertEquals(2690.0, m.getValue("ETH").first, 0.0)
        assertEquals(false, m.containsKey("SOL"))
    }

    @Test fun briefPointsOpenTheirStories() {
        val p = HomeText.points("- The minister resigned on Tuesday.\n- Storms closed 40 schools.", listOf(11L, 12L))
        assertEquals(listOf("The minister resigned on Tuesday.", "Storms closed 40 schools."), p.map { it.text })
        assertEquals(listOf(11L, 12L), p.map { it.story })
        // the points and stories do not pair up: no story to open, NEWS opens instead
        assertEquals(listOf<Long?>(null, null), HomeText.points("- one point here\n- and another", listOf(11L)).map { it.story })
        // a brief from before the points is one point
        val old = HomeText.points("The minister resigned. Storms closed 40 schools.", listOf(11L, 12L))
        assertEquals(1, old.size)
        assertEquals(null, old[0].story)
        assertEquals(0, HomeText.points("", emptyList()).size)
    }

    @Test fun pricesSayWhenAndFromHowMany() {
        assertEquals("updated just now · 6 exchanges", HomeText.pricesLine(now - 1, now, 6))
        assertEquals("updated 4 s ago", HomeText.pricesLine(now - 4, now, 1))
        assertEquals("updated 2 min ago · 5 exchanges", HomeText.pricesLine(now - 125, now, 5))
        assertEquals("", HomeText.pricesLine(0, now, 0))
        assertEquals("the box did not answer", HomeText.briefNot(""))
    }

    @Test fun theWeatherWords() {
        assertEquals("24° feels 27 · partly cloudy · wind 25 km/h", HomeText.weatherNow(24.2, 26.8, 2, 25.0))
        assertEquals("18°", HomeText.weatherNow(18.0, 18.5, 1000, 5.0))
        assertEquals("", HomeText.weatherNow(Double.NaN, 1.0, 0, 0.0))
        assertEquals("Thu 28/19 rain 60%", HomeText.weatherDay("2026-10-08", 28.0, 19.0, 61, 60))
        assertEquals("Fri 20/12", HomeText.weatherDay("2026-10-09", 20.0, 12.0, 1000, 10))
        assertEquals("Kassiopi, GR · nearest of 3,000 places the box pulls daily · pulled 3 h ago", HomeText.weatherSource("Kassiopi", "GR", 1000, 3000, 1000 + 3 * 3600))
    }
}
