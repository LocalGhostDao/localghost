package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class CrtMoodTest {
    @Test fun quietAfterOneAndOneInFourAfterThat() {
        val now = 1_000_000L
        // inside the quiet: nothing, whatever the roll
        for (r in 0 until 24) assertEquals(CrtMood.Effect.NONE, CrtMood.pick(now, now - 10_000, r))
        // the quiet passed (or never an effect): three rolls in twelve do something
        var some = 0
        for (r in 0 until 12) if (CrtMood.pick(now, now - CrtMood.quietMs, r) != CrtMood.Effect.NONE) some++
        assertEquals(3, some)
        assertEquals(CrtMood.Effect.LINE, CrtMood.pick(now, 0, 12))
        assertEquals(CrtMood.Effect.WASH, CrtMood.pick(now, 0, 13))
        assertEquals(CrtMood.Effect.TYPE, CrtMood.pick(now, 0, -10)) // a negative roll is a roll
        assertEquals(CrtMood.Effect.NONE, CrtMood.pick(now, 0, 5))
    }

    @Test fun theLineBlinksInTheMiddleOfTheGlassAndIsGoneWithinTheHold() {
        for (r1 in listOf(0f, 0.5f, 0.99f)) for (r2 in listOf(0f, 0.49f, 0.51f, 0.99f)) {
            val b = CrtMood.lineBlinks(r1, r2)
            assertTrue(b.size in 2..3)
            for (x in b) assertTrue("y ${x.y}", x.y >= 0.2f && x.y <= 0.85f)
            assertTrue(b.sumOf { it.onMs + it.offMs } <= CrtMood.holdMs(CrtMood.Effect.LINE))
        }
        assertEquals(3, CrtMood.lineBlinks(0.3f, 0.9f).size)
        assertEquals(2, CrtMood.lineBlinks(0.3f, 0.1f).size)
    }

    @Test fun eachEffectHoldsForAMoment() {
        assertTrue(CrtMood.holdMs(CrtMood.Effect.WASH) > CrtMood.holdMs(CrtMood.Effect.LINE))
        assertEquals(0L, CrtMood.holdMs(CrtMood.Effect.NONE))
    }
}
