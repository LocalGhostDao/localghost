package com.localghost.app.net

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class QueryGuardTest {
    @Test fun ordinary_searches_pass() {
        for (q in listOf("boat survey cost Greece", "US election results 2024", "World War I causes", "iPhone 16 price",
            "flight BA 2490 status", "corfu population", "best pizza dough recipe")) assertTrue(q, QueryGuard.ok(q))
    }

    @Test fun the_first_person_does_not_leave() {
        for (q in listOf("my boat survey Lefkada", "how much did I pay for a survey", "restaurants near our flat",
            "I'm looking for a dentist", "what should i do about mine")) assertFalse(q, QueryGuard.ok(q))
    }

    @Test fun addresses_and_long_numbers_do_not_leave() {
        assertFalse(QueryGuard.ok("email nikos@example.gr surveyor"))
        assertFalse(QueryGuard.ok("call 0030 2645 022 911"))
        assertFalse(QueryGuard.ok("who owns +44 20 7946 0958"))
        assertFalse(QueryGuard.ok(""))
        assertFalse(QueryGuard.ok("x".repeat(200)))
    }

    @Test fun filter_keeps_the_order_of_what_may_leave() {
        assertEquals(listOf("a b c", "d e f"), QueryGuard.filter(listOf("a b c", "my thing", "d e f")))
    }
}
