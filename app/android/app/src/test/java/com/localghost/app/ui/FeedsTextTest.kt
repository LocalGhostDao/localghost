package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class FeedsTextTest {
    @Test fun readsTheBoxReport() {
        val r = FeedsText.parse(org.json.JSONObject("""{"at":1000,"state":"flaky","summary":"1 wants a look: exchanges flaky",
            "sections":[{"id":"prices","title":"Prices","state":"ok","line":"50 of 50 symbols","ageS":12,"every":"every minute",
              "rows":[{"k":"BTC","v":"65,000 USD"}]},
              {"id":"venues","title":"Exchanges","state":"flaky","line":"6 of 7 answering","ageS":-1,
              "rows":[{"k":"okx","v":"failing: HTTP 429","state":"failing"}]}]}"""))!!
        assertEquals("flaky", r.state)
        assertEquals(2, r.sections.size)
        assertEquals("every minute", r.sections[0].every)
        assertEquals("", r.sections[0].rows[0].state)
        assertEquals("failing", r.sections[1].rows[0].state)
        assertEquals(-1L, r.sections[1].ageS)
        assertNull(FeedsText.parse(org.json.JSONObject("""{"ok":true}""")))
    }

    @Test fun marksTonesAndAges() {
        assertEquals("●", FeedsText.mark("ok"))
        assertEquals("✕", FeedsText.mark("failing"))
        assertEquals(FeedsText.Tone.QUIET, FeedsText.tone("filling"))
        assertEquals(FeedsText.Tone.WARN, FeedsText.tone("late"))
        assertEquals(FeedsText.Tone.BAD, FeedsText.tone("failing"))
        assertEquals("ALL WELL", FeedsText.word("ok"))
        assertEquals("", FeedsText.age(-1))
        assertEquals("12 s", FeedsText.age(12))
        assertEquals("2 min", FeedsText.age(90))
        assertEquals("2 h", FeedsText.age(5400))
        assertEquals("3 days", FeedsText.age(3 * 86400))
        val s = FeedsText.Section("prices", "Prices", "ok", "", 12, "", emptyList())
        assertEquals("42 s", FeedsText.ageNow(s, 1000, 1030)) // the report is half a minute old
        assertEquals("", FeedsText.ageNow(s.copy(ageS = -1), 1000, 1030))
        val r = FeedsText.Report(1000, "ok", "all well", listOf(s))
        assertEquals("", FeedsText.staleness(r, 1060))
        assertEquals("the box's last report is 5 min old", FeedsText.staleness(r, 1300))
    }

    @Test fun foldedUnderTheDaemons() {
        fun sec(id: String, st: String) = FeedsText.Section(id, id, st, "", 12, "", emptyList())
        val r = FeedsText.Report(1000, "flaky", "2 want a look", listOf(sec("prices", "ok"), sec("market", "flaky"), sec("daily", "flaky"), sec("history", "filling")))
        assertEquals("data feeds · 2 want a look", FeedsText.foldLine(r, false))
        assertEquals("data feeds · 1 wants a look", FeedsText.foldLine(r.copy(sections = r.sections.take(2)), false))
        assertEquals("data feeds · filling", FeedsText.foldLine(FeedsText.Report(1000, "filling", "", listOf(sec("history", "filling"))), false))
        assertEquals("data feeds · all well", FeedsText.foldLine(FeedsText.Report(1000, "ok", "", listOf(sec("prices", "ok"))), false))
        assertEquals("data feeds · reading…", FeedsText.foldLine(null, false))
        assertEquals("archive pipeline · 99% at the latest stage · 26 damaged", FeedsText.pipelineLine(33025, 32965, 26, true, false))
        assertEquals("archive pipeline · all at the latest stage", FeedsText.pipelineLine(10, 10, 0, true, false))
        assertEquals("archive pipeline · not reported by this build", FeedsText.pipelineLine(0, 0, 0, false, true))
    }

    @Test fun aFeedsLine() {
        val now = 1_000_000L
        assertEquals("answered 40 min ago · 32 entries", FeedsText.feedLine(true, now - 2400, now - 2400, "ok", 32, 0, now))
        assertEquals("failed 3 times: 404 · last answered 2 days ago", FeedsText.feedLine(true, now - 2 * 86400, now - 100, "404", 0, 3, now))
        assertEquals("off · last answered 2 h ago", FeedsText.feedLine(false, now - 7200, now - 7200, "ok", 5, 0, now))
        assertEquals("not fetched yet", FeedsText.feedLine(true, 0, 0, "", 0, 0, now))
    }
}
