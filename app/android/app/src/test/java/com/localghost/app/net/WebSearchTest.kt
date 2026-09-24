package com.localghost.app.net

import java.util.Calendar
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The parts of the phone-side web search that run without a network: the plan, both DuckDuckGo
 * parsers, the readability pass, and which tools a question earns. The formatting of the tool
 * answers (Open-Meteo, Frankfurter, Wikipedia JSON) needs org.json, which the plain unit-test
 * classpath does not carry; those are covered by the scratch harness in the session notes.
 */
class WebSearchTest {
    @Test fun modesAndFreshness() {
        assertTrue(WebSearch.looksFresh("what is the weather in Lisbon tomorrow?"))
        assertTrue(WebSearch.looksFresh("latest news about the Galaxy S26 battery"))
        assertFalse(WebSearch.looksFresh("show me the photos from the harbour"))
        assertFalse(WebSearch.looksFresh("hi"))
        assertTrue(WebSearch.shouldSearch("on", "hi"))
        assertFalse(WebSearch.shouldSearch("off", "latest news?"))
        assertTrue(WebSearch.shouldSearch("auto", "how much is a coffee in Tokyo?"))
    }

    @Test fun thePlan() {
        assertEquals("who won the Greek election", WebSearch.cleanQuery("hey, can you please tell me who won the Greek election?"))
        assertEquals("the opening hours of the Acropolis museum", WebSearch.cleanQuery("Find the opening hours of the Acropolis museum please"))
        val cal = Calendar.getInstance().apply { set(Calendar.YEAR, 2026) }
        val plan = WebSearch.plan("could you look up the latest Android version and what it adds?", cal)
        assertEquals("the latest Android version and what it adds", plan[0].text)
        assertTrue(plan[0].always)
        assertTrue(plan.any { !it.always && it.text == "latest android version adds" })
        assertTrue(plan.last().always && plan.last().text.endsWith(" 2026"))
        assertTrue(WebSearch.plan("android 2025 release notes?", cal).none { it.text.endsWith("2026") })
        assertEquals(1, WebSearch.plan("pastel de nata", cal).size)
        assertEquals(listOf("weather", "look", "like", "athens", "weekend"), WebSearch.terms("what does the weather look like in Athens this weekend?"))
    }

    private val ddg = """<div class="result"><h2 class="result__title"><a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fen.wikipedia.org%2Fwiki%2FPastel_de_nata&amp;rut=abc">Pastel de nata - <b>Wikipedia</b></a></h2>
      <a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fen.wikipedia.org%2Fwiki%2FPastel_de_nata&amp;rut=abc">Pastel de nata is a Portuguese egg custard tart pastry.</a></div>
      <div class="result"><h2 class="result__title"><a rel="nofollow" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fwww.example.com%2Fnata&amp;rut=def" class="result__a">Example nata</a></h2>
      <a class="result__snippet" href="x">Snippet two &amp; more.</a></div>
      <div class="result"><a class="result__a" href="https://duckduckgo.com/y.js?ad=1">Ad</a><a class="result__snippet">ad text</a></div>"""

    @Test fun duckDuckGoHtml() {
        val hits = WebSearch.parseDdgHtml(ddg)
        assertEquals(2, hits.size)
        assertEquals("https://en.wikipedia.org/wiki/Pastel_de_nata", hits[0].url)
        assertEquals("Pastel de nata - Wikipedia", hits[0].title)
        assertEquals("Pastel de nata is a Portuguese egg custard tart pastry.", hits[0].snippet)
        assertEquals("https://www.example.com/nata", hits[1].url) // attribute order does not matter
        assertEquals("Snippet two & more.", hits[1].snippet)
        assertEquals("example.com", hits[1].site)
        assertTrue(WebSearch.parseDdgHtml("<div class=\"anomaly-modal__title\">Unfortunately, bots use DuckDuckGo too.</div>").isEmpty())
        assertTrue(WebSearch.isBotCheck("<form id=\"challenge-form\">"))
        assertFalse(WebSearch.isBotCheck(ddg))
    }

    @Test fun duckDuckGoLite() {
        val lite = """<table><tr><td valign="top">1.&nbsp;</td><td><a rel="nofollow" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fwww.bbc.co.uk%2Fweather%2F264371&amp;rut=x" class='result-link'>Athens - BBC Weather</a></td></tr>
          <tr><td>&nbsp;</td><td class='result-snippet'>14-day weather forecast for <b>Athens</b>.</td></tr>
          <tr><td valign="top">2.&nbsp;</td><td><a rel="nofollow" href="https://www.accuweather.com/en/gr/athens/182536/weather-forecast/182536" class='result-link'>Athens, Attica, Greece Weather</a></td></tr>
          <tr><td>&nbsp;</td><td class='result-snippet'>Current weather in Athens.</td></tr></table>"""
        val lh = WebSearch.parseDdgLite(lite)
        assertEquals(2, lh.size)
        assertEquals("https://www.bbc.co.uk/weather/264371", lh[0].url)
        assertEquals("14-day weather forecast for Athens.", lh[0].snippet)
        assertEquals("accuweather.com", lh[1].site)
    }

    @Test fun readabilityExtract() {
        val page = """<!DOCTYPE html><html><head><meta charset="utf-8"><title>Pastel de nata: a history | Example</title>
          <meta name="description" content="How Lisbon&#39;s custard tart came to be.">
          <meta property="og:description" content="How Lisbon's custard tart came to be, from the monastery to Bel&eacute;m.">
          <meta property="article:published_time" content="2025-03-02T08:00:00+00:00">
          <style>p{}</style><script>var a=1;</script></head><body>
          <header><a href="/">Home</a> <a href="/login">Login</a></header>
          <nav><ul><li><a href="/a">Food</a></li><li><a href="/b">Travel</a></li></ul></nav>
          <div class="sidebar"><p>Related: <a href="/1">Ten best custard tarts in Lisbon and where to find them</a> <a href="/2">The story of Belém tower and the monastery next door</a></p></div>
          <article><h1>Pastel de nata: a history</h1><p>Short.</p>
          <p>The pastel de nata was created before the 18th century by Catholic monks at the Jer&oacute;nimos Monastery in Lisbon, and the recipe remains a secret to this day. It is a paragraph long enough to count, with several sentences of prose.</p>
          <p>Another paragraph about custard tarts and the bakery in Bel&eacute;m that sells thousands every day to visitors who queue around the block for them.</p>
          <p>Menu: <a href="/x">first</a> <a href="/y">second</a> <a href="/z">third link in a row here</a> and</p>
          <!-- a comment with <p>fake paragraph text that must not appear in the extract at all</p> -->
          </article><footer>copyright 2025 example</footer><script type="application/ld+json">{"datePublished":"2020-01-01"}</script></body></html>"""
        val ex = WebSearch.extract(page)
        assertEquals("Pastel de nata: a history | Example", ex.title)
        assertEquals("How Lisbon's custard tart came to be, from the monastery to Belém.", ex.description)
        assertEquals("2025-03-02T08:00:00+00:00", ex.published)
        assertEquals(2, ex.paragraphs.size)
        assertTrue(ex.paragraphs[0].startsWith("The pastel de nata was created") && ex.paragraphs[0].contains("Jerónimos"))
        assertTrue(ex.paragraphs.none { it.contains("Login") || it.contains("Ten best") || it.contains("fake paragraph") || it.contains("var a=1") || it.contains("copyright") })
        val excerpt = ex.excerptFor(WebSearch.terms("who created the pastel de nata in lisbon?"))
        assertTrue(excerpt, excerpt.startsWith("How Lisbon's custard tart came to be, from the monastery to Belém. — The pastel de nata was created"))
        val bare = """<html><body><div><p>Opening hours for the museum are nine to five every day except Monday, and last entry is half an hour before closing.</p></div>
          <time datetime="2024-11-05">5 Nov</time><script type="application/ld+json">{"@type":"Article","datePublished":"2024-11-04T10:00:00Z"}</script></body></html>"""
        val b = WebSearch.extract(bare)
        assertEquals(1, b.paragraphs.size)
        assertEquals("2024-11-04T10:00:00Z", b.published)
        assertEquals("", b.title)
        assertEquals("A & B 'c' A ó &unknownthing;", WebSearch.clean("A &amp; B &#39;c&#39; &#x41; &oacute; &unknownthing;"))
    }

    @Test fun whichToolsApply() {
        val t = WebSearch.Tools
        assertEquals("Athens", t.placeOf("what is the weather in Athens tomorrow"))
        assertNull(t.placeOf("weather forecast for tomorrow"))
        assertEquals("São Paulo", t.placeOf("is it raining in São Paulo right now?"))
        assertEquals("Paris", t.placeOf("will it rain in Paris this weekend"))
        assertNull(t.placeOf("how hot is it today"))
        assertEquals(1, t.forQuestion("what's the weather like today?", WebSearch.Here(37.98, 23.72)).size)
        assertEquals(0, t.forQuestion("what's the weather like today?", null).size)
        assertEquals(1, t.forQuestion("how much is 100 euros in pounds?", null).size)
        assertEquals(1, t.forQuestion("gbp to ron", null).size)
        assertEquals(2, t.forQuestion("100 dollars to euros and the weather in Rome", null).size)
        assertEquals(1, t.forQuestion("who is Nikos Kazantzakis?", null).size)
        assertEquals(0, t.forQuestion("what is the Acropolis museum opening time on sundays and holidays?", null).size)
        assertEquals(0, t.forQuestion("show me photos from Rome", null).size)
        assertEquals("thunderstorm", t.code(95))
        assertEquals("unknown", t.code(-1))
    }

    @Test fun braveApiJson() {
        val j = """{"web":{"results":[
          {"title":"Voutoumi <strong>Beach</strong> , Antipaxos","url":"https://www.example.gr/voutoumi","description":"The <strong>beach</strong> everyone photographs.","page_age":"2026-06-02T10:00:00"},
          {"title":"No url","url":"","description":"skipped"},
          {"title":"","url":"https://example.org/untitled","description":"<b>bold</b> text &amp; more"}
        ]}}"""
        val hits = WebSearch.parseBrave(j)
        assertEquals(2, hits.size)
        assertEquals("Voutoumi Beach , Antipaxos", hits[0].title)
        assertEquals("brave", hits[0].source)
        assertEquals("2026-06-02", hits[0].published)
        assertEquals("https://example.org/untitled", hits[1].title)
        assertEquals("bold text & more", hits[1].snippet)
        assertTrue(WebSearch.parseBrave("not json").isEmpty())
        assertTrue(WebSearch.Engine("brave", "k").brave)
        assertTrue(!WebSearch.Engine("brave", "").brave)
    }
}
