package com.localghost.app.checkin

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class FeelingsTest {
    @Test fun everyOldFeelingIsStillOffered() {
        // the twelve the card offered before 29 Sep 2026: past check-ins keep meaning something
        for (f in listOf("calm", "happy", "energised", "grateful", "focused", "proud",
                "tired", "stressed", "anxious", "restless", "low", "lonely")) {
            assertTrue(f, f in Feelings.all)
        }
        assertEquals("no feeling in two groups", Feelings.all.size, Feelings.all.toSet().size)
        // and every guess the box can make (hw.DayContext) is a feeling here
        for (f in listOf("tired", "rested", "energised", "calm", "curious", "stressed")) assertTrue(f, f in Feelings.all)
    }

    @Test fun usualCountsAcrossCheckins() {
        val u = Feelings.usual(listOf("calm, tired", "tired", "(unspecified)", "Happy, tired, calm", ""))
        assertEquals(listOf("tired", "calm", "happy"), u)
    }

    @Test fun quickRowIsGuessesThenUsualThenCommon() {
        val q = Feelings.quick(listOf("rested", "curious"), listOf("calm", "rested", "proud"))
        assertEquals(listOf("rested", "curious", "calm", "proud", "happy", "tired", "stressed", "focused"), q)
        assertEquals(Feelings.QUICK, q.size)
    }

    @Test fun preselectAndToggle() {
        assertEquals(listOf("tired", "rested"), Feelings.preselect(listOf("tired", "rested", "energised")))
        var p = Feelings.preselect(listOf("tired"))
        p = Feelings.toggle(p, "tired")                // untick the guess
        assertEquals(emptyList<String>(), p)
        for (f in listOf("a", "b", "c", "d", "e")) p = Feelings.toggle(p, f)
        assertEquals(listOf("a", "b", "c", "d"), p)    // four at most
    }

    @Test fun checkinTextKeepsTheLinesTheBoxParses() {
        val t = Feelings.checkinText("2026-09-29", listOf("calm", "tired"), listOf("tired"), "  a long swim ",
            "0123456789abcdef0123456789abcdef", 72_400)
        assertEquals("Daily check-in 2026-09-29\nFeeling: calm, tired\nPreselected: tired\nWhy: a long swim\n" +
            "Voice: 0123456789abcdef0123456789abcdef 1:12", t)
        assertEquals("Daily check-in 2026-09-29\nFeeling: (unspecified)",
            Feelings.checkinText("2026-09-29", emptyList(), emptyList(), "", null, 0))
    }

    @Test fun theToneOfADay() {
        assertEquals("easy", Feelings.groupOf("calm"))
        assertEquals("", Feelings.groupOf("sideways"))
        assertEquals(listOf("calm", "tired"), Feelings.picks("Calm, tired"))
        assertEquals(emptyList<String>(), Feelings.picks("(unspecified)"))
        // the box guessed tired and it was left standing; the person added calm: the day is calm
        assertEquals("easy", Feelings.tone("tired, calm", "tired"))
        assertEquals("heavy", Feelings.tone("tired", "tired"))      // only the guess: it still counts
        assertEquals("", Feelings.tone("(unspecified)", ""))
        assertEquals("▲", Feelings.mark("bright")); assertEquals("▼", Feelings.mark("heavy"))
        assertEquals("·", Feelings.mark(""))
    }

    @Test fun theStripIsTheLastTwoWeeksOldestFirst() {
        val s = Feelings.strip("2026-10-04", mapOf("2026-10-03" to "easy", "2026-09-21" to "tense"), 14)
        assertEquals(14, s.size)
        assertEquals("2026-09-21", s.first().day)
        assertEquals("2026-10-04", s.last().day)
        assertEquals(Feelings.Cell("2026-10-03", "easy", true), s[12])
        assertEquals(Feelings.Cell("2026-10-04", "", false), s[13])
        assertEquals(Feelings.Cell("2026-09-21", "tense", true), s[0])
        // across a month end and a leap day
        assertEquals("2026-02-28", Feelings.strip("2026-03-01", emptyMap(), 2).first().day)
        assertEquals("2024-02-29", Feelings.strip("2024-03-01", emptyMap(), 2).first().day)
        assertEquals("4", Feelings.dayNumber("2026-10-04"))
        assertEquals("S", Feelings.weekdayInitial("2026-10-04"))   // a Sunday
        assertEquals("F", Feelings.weekdayInitial("2026-10-02"))
        assertEquals("Fri 2 Oct", Feelings.shortDay("2026-10-02"))
    }

    @Test fun whatRecurs() {
        assertEquals("tired ×3 · calm ×2 · happy ×1",
            Feelings.recurring(listOf("calm, tired", "tired", "(unspecified)", "Happy, tired, calm")))
        assertEquals("", Feelings.recurring(listOf("calm", "(unspecified)")))   // one check-in says nothing yet
        assertEquals("calm ×2", Feelings.recurring(listOf("calm", "calm"), 3))
    }

    @Test fun rowsFitTheWidth() {
        val rows = Feelings.rows(listOf("disappointed", "overwhelmed", "calm", "low", "sad", "tired", "happy"))
        for (r in rows) assertTrue(r.toString(), r.sumOf { it.length + 3 } <= 30 || r.size == 1)
        assertEquals(7, rows.sumOf { it.size })
        assertEquals(listOf("disappointed", "overwhelmed"), rows[0])
    }
}
