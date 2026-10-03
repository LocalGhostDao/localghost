package com.localghost.app.net

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class BoxKnowsTest {
    @Test fun theBoxsNumbersAreNotSearchedFor() {
        listOf("btc price?", "how is ethereum doing today", "ETH", "top 50 cryptos", "biggest coins", "how is crypto doing now",
            "100 euros in pounds", "gbp to ron exchange rate", "convert 50 dollars to eur").forEach {
            assertTrue(it, BoxKnows.covers(it))
        }
        listOf("where is the nearest pharmacy", "who won the match yesterday", "is bitcoin a good idea for a pension in theory",
            "news about the rail strike in france").forEach {
            assertFalse(it, BoxKnows.covers(it))
        }
    }

    // The weather where the phone is never goes to the web (the web would need the position); a
    // named place may, its name says nothing about where the phone is.
    @Test fun theWeatherHereIsTheBoxs() {
        listOf("what's the weather like today?", "is it going to rain tomorrow", "do I need an umbrella this afternoon",
            "how hot is it right now", "weather for the weekend", "will it snow tonight at 8").forEach {
            assertTrue(it, BoxKnows.weatherHere(it)); assertTrue(it, BoxKnows.covers(it))
        }
        listOf("what's the weather in Rome today", "forecast for Cluj-Napoca this weekend", "is it raining in São Paulo").forEach {
            assertFalse(it, BoxKnows.weatherHere(it)); assertFalse(it, BoxKnows.covers(it))
        }
        assertFalse(BoxKnows.weatherHere("windows update"))
    }
}
