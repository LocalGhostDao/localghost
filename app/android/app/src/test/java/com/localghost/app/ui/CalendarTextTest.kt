package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNotEquals
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.LocalDate
import java.time.ZoneId
import java.time.ZoneOffset

class CalendarTextTest {
    private val london = ZoneId.of("Europe/London")
    private val d16 = LocalDate.of(2026, 10, 16)
    private fun at(d: LocalDate, h: Int, m: Int = 0) = d.atTime(h, m).atZone(london).toEpochSecond()
    private fun utcDay(d: LocalDate) = d.atStartOfDay(ZoneOffset.UTC).toEpochSecond()

    private val lunch = CalendarText.Event(1, 3, "Personal", "vlad@example.org", "Lunch with Paul", "Dishoom", "bring the tickets",
        at(d16, 12), at(d16, 13), false, "Europe/London", "confirmed", "accepted", true, "vlad@example.org",
        listOf(CalendarText.Attendee("Paul", "paul@example.org", "accepted"), CalendarText.Attendee("", "vlad@example.org", "accepted"),
            CalendarText.Attendee("Ana", "ana@example.org", "tentative")))
    private val trip = CalendarText.Event(2, 3, "Personal", "", "Romania", "", "", utcDay(d16.plusDays(1)), utcDay(d16.plusDays(4)), true,
        "UTC", "confirmed", "", false, "", emptyList())
    private val late = CalendarText.Event(3, 4, "Work", "", "Deploy", "", "", at(d16, 23), at(d16.plusDays(1), 1), false, "", "cancelled", "", true, "", emptyList())

    @Test fun storeNumbers() {
        assertEquals("cancelled", CalendarText.eventStatus(2))
        assertEquals("tentative", CalendarText.answer(4))
        assertEquals("", CalendarText.answer(null))
        assertFalse(CalendarText.busy(1))
        assertTrue(CalendarText.busy(null))
        assertEquals("#ff8800", CalendarText.colour(0xffff8800.toInt()))
    }

    @Test fun uploadRoundTrip() {
        val body = CalendarText.upload("phone", "Europe/London", 3600, 1, 2, listOf(CalendarText.CalInfo(3, "Personal", "vlad@example.org", "com.google", "#ff8800", true, 2)),
            listOf(trip, lunch))
        val evs = CalendarText.parseEvents(body.optJSONArray("events"))
        assertEquals(listOf(1L, 2L), evs.map { it.id }) // sorted by start
        assertEquals(lunch, evs[0])
        assertEquals(trip, evs[1])
        assertEquals(1, body.optJSONArray("calendars")!!.length())
        val same = CalendarText.upload("phone", "Europe/London", 3600, 5, 9, emptyList(), listOf(lunch, trip))
        val other = CalendarText.upload("phone", "Europe/London", 3600, 5, 9, emptyList(), listOf(lunch.copy(title = "Lunch with Ana"), trip))
        assertEquals(CalendarText.fingerprint(CalendarText.upload("phone", "Europe/London", 0, 1, 2, emptyList(), listOf(trip, lunch))), CalendarText.fingerprint(same))
        assertNotEquals(CalendarText.fingerprint(same), CalendarText.fingerprint(other))
    }

    @Test fun lines() {
        assertEquals("12:00–13:00", CalendarText.time(lunch, london, d16))
        assertEquals("Dishoom · with Paul, Ana · Personal", CalendarText.detail(lunch))
        assertEquals("all day, 2 of 3", CalendarText.time(trip, london, d16.plusDays(2)))
        assertEquals("23:00–Sat 17 Oct 01:00", CalendarText.time(late, london, d16))
        assertEquals("cancelled", CalendarText.mark(late))
        assertFalse(late.live)
        assertEquals("(no title)", CalendarText.title(trip.copy(title = " ")))
    }

    @Test fun agendaAndDays() {
        val g = CalendarText.agenda(listOf(late, trip, lunch), london)
        assertEquals(listOf(d16, d16.plusDays(1), d16.plusDays(2), d16.plusDays(3)), g.map { it.first })
        assertEquals(listOf(1L, 3L), g[0].second.map { it.id })
        assertEquals(listOf(2L), g[1].second.map { it.id })
        val clipped = CalendarText.agenda(listOf(late, trip, lunch), london, d16.plusDays(1), d16.plusDays(3))
        assertEquals(2, clipped.size)
        assertEquals(listOf(3L, 2L), CalendarText.onDay(listOf(late, trip, lunch), d16.plusDays(1), london).map { it.id }.sortedDescending())
        assertEquals(listOf(1L, 3L), CalendarText.onDay(listOf(late, trip, lunch), d16, london).map { it.id })
        val (a, b) = CalendarText.dayBounds(d16, london)
        assertEquals(utcDay(d16) - 3600, a)
        assertEquals(utcDay(d16.plusDays(1)), b)
        assertEquals("TOMORROW · Saturday 17 October", CalendarText.header(d16.plusDays(1), d16))
    }

    @Test fun whenNext() {
        assertEquals("now, until 13:00", CalendarText.whenNext(lunch, at(d16, 12, 30), london))
        assertEquals("in 40 min", CalendarText.whenNext(lunch, at(d16, 11, 20), london))
        assertEquals("at 12:00", CalendarText.whenNext(lunch, at(d16, 8), london))
        assertEquals("tomorrow 12:00", CalendarText.whenNext(lunch, at(d16.minusDays(1), 20), london))
    }

    @Test fun synced() {
        assertEquals("the box has no calendar from this phone yet", CalendarText.synced(null, 100))
        val m = CalendarText.Meta(1000, 0, 0, "Europe/London", 412, 0, listOf(CalendarText.CalInfo(1, "a", "", "", "", true, 1)))
        assertEquals("synced 5 min ago · 412 events from 1 calendar · a year back and a year ahead", CalendarText.synced(m, 1300))
    }
}
