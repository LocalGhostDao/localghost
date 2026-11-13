package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Test

class MemoryTextTest {
    @Test fun theDayAMemoryOpens() {
        assertEquals("2026-10-03", MemoryText.dayOf("day:2026-10-03"))
        assertEquals("2026-09-28", MemoryText.dayOf("outing:2026-09-28"))
        assertEquals("", MemoryText.dayOf(""))
        assertEquals("", MemoryText.dayOf("chat:12"))
        assertEquals("", MemoryText.dayOf("day:soon"))
        assertEquals("2026-09-12", MemoryText.dayOf("trip:2026-09-12"))
        // the second and third outings to begin on a day carry a number after it
        assertEquals("2026-10-17", MemoryText.dayOf("outing:2026-10-17.2"))
        assertEquals("2026-10-17", MemoryText.dayOf("outing:2026-10-17.12"))
        assertEquals("", MemoryText.dayOf("outing:2026-10-17."))
        assertEquals("", MemoryText.dayOf("outing:2026-10-17.x"))
        assertEquals("", MemoryText.dayOf("day:2026-10-17.2"))
        assertEquals("day", MemoryText.partLabel("day"))
        assertEquals("outing", MemoryText.partLabel("outing"))
    }

    @Test fun theKindAndTheOrigin() {
        assertEquals("AN OUTING", MemoryText.kindLabel("outing"))
        assertEquals("A TRIP", MemoryText.kindLabel("trip"))
        assertEquals("a trip, from your outings and days · 8 days · 160 photos", MemoryText.origin("trip", "8 days · 160 photos", ""))
        assertEquals("a trip, from your outings and days", MemoryText.origin("trip", null, ""))
        assertEquals("A DAY", MemoryText.kindLabel("episode"))
        assertEquals("A MEMORY", MemoryText.kindLabel("distilled"))
        assertEquals("WRITTEN BY ME", MemoryText.kindLabel("user"))
        assertEquals("yours", MemoryText.origin("user", null, ""))
        assertEquals("noticed by your box", MemoryText.origin("insight", null, ""))
        assertEquals("from your photos · 24 photos · 3 days", MemoryText.origin("outing", "24 photos · 3 days", ""))
        assertEquals("from your photos", MemoryText.origin("outing", null, ""))
        assertEquals("a day, from your trail and photos · home → Greenwich → home", MemoryText.origin("day", null, "home → Greenwich → home"))
        assertEquals("a day, from your trail and photos", MemoryText.origin("day", null, ""))
    }

    @Test fun readAgainLines() {
        val day = { _: Long -> "9 November" }
        assertEquals("the box read the conversation again on 9 November and is not sure of this. It says: the boat sleeps four",
            MemoryText.doubtLine("chat:2", "the boat sleeps four", 1L, day))
        assertEquals("the box read the note again and could not find this in it", MemoryText.doubtLine("note:x", "", 0L, day))
        assertEquals("the voice note", MemoryText.recordName("voice:12"))
        assertEquals("read again on 9 November, as written", MemoryText.confirmedLine(1L, day))
        assertEquals(null, MemoryText.confirmedLine(0L, day))
        assertEquals("Cristina owns the boat. , the record says: it is Vlad's", MemoryText.factDoubt("Cristina owns the boat.", "it is Vlad's"))
        assertEquals("Cristina owns the boat. , not found in the record it came from", MemoryText.factDoubt("Cristina owns the boat.", "the record does not say this"))
    }
    @Test fun aMonthIsAWholeOfItsOwn() {
        assertEquals("A MONTH", MemoryText.kindLabel("month"))
        assertEquals("a month, counted from its days", MemoryText.origin("month", null, ""))
        assertEquals("", MemoryText.dayOf("month:2026-08")) // a month opens no single day
        assertEquals("THE DAYS AND THE OUTINGS OF IT", MemoryText.partsLabel("month"))
        assertEquals("THE OUTINGS AND THE DAYS OF IT", MemoryText.partsLabel("trip"))
        assertEquals("THE DAYS OF IT", MemoryText.partsLabel("outing"))
    }

    @Test fun whyTwoMemoriesAreKeptTogetherIsSaidFromThePagesSide() {
        // part and whole: the larger of the two is the whole, whichever side the page is on
        assertEquals("part of this", MemoryText.linkWhy("part", "trip", "day"))
        assertEquals("this is part of it", MemoryText.linkWhy("part", "day", "trip"))
        assertEquals("part of this", MemoryText.linkWhy("part", "month", "outing"))
        assertEquals("this is part of it", MemoryText.linkWhy("part", "outing", "month"))
        assertEquals("part of this", MemoryText.linkWhy("part", "outing", "day"))
        // a person and what names them
        assertEquals("names them", MemoryText.linkWhy("names", "person", "day"))
        assertEquals("named here", MemoryText.linkWhy("names", "day", "person"))
        assertEquals("named here", MemoryText.linkWhy("names", "user", "person"))
        // a place and what happened there
        assertEquals("was here", MemoryText.linkWhy("place", "place", "trip"))
        assertEquals("where this was", MemoryText.linkWhy("place", "trip", "place"))
        // a note and the day it was written; a person and the day something was said of them
        assertEquals("the day this was written", MemoryText.linkWhy("written", "distilled", "day"))
        assertEquals("written on this day", MemoryText.linkWhy("written", "day", "distilled"))
        assertEquals("something was said of them that day", MemoryText.linkWhy("written", "person", "day"))
        assertEquals("something was said of them this day", MemoryText.linkWhy("written", "day", "person"))
        // a trip and its month
        assertEquals("began in this month", MemoryText.linkWhy("month", "month", "trip"))
        assertEquals("the month it began in", MemoryText.linkWhy("month", "trip", "month"))
        // a reason this build does not know is still a reason, said plainly
        assertEquals("kept together", MemoryText.linkWhy("alike", "distilled", "distilled"))
        assertEquals("kept together", MemoryText.linkWhy("", "day", "day"))
    }

    @Test fun theAnchorSaysWhoMarkedIt() {
        // the two lines the page shows; the mark is the person's and the words say so
        assert(MemoryText.ANCHOR_LINE.contains("marked by you"))
        assert(MemoryText.FRONT_LINE.contains("every chat starts from it"))
        // the UI's dash is " , ", never an em dash
        for (s in listOf(MemoryText.ANCHOR_LINE, MemoryText.FRONT_LINE)) assert(!s.contains("\u2014")) { s }
        // and the explainer says who may mark one, in the memories topic
        val topic = Explain.of("memories")!!.paragraphs.joinToString(" ")
        assert(topic.contains("Only you can mark one")) { topic }
        assert(topic.contains("Nothing is deleted")) { topic }
    }
}
