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
        assertEquals(CrtMood.Effect.SWEEP, CrtMood.pick(now, 0, 12))
        assertEquals(CrtMood.Effect.WASH, CrtMood.pick(now, 0, 13))
        assertEquals(CrtMood.Effect.TYPE, CrtMood.pick(now, 0, -10)) // a negative roll is a roll
        assertEquals(CrtMood.Effect.NONE, CrtMood.pick(now, 0, 5))
    }

    @Test fun eachEffectHoldsForAMoment() {
        assertTrue(CrtMood.holdMs(CrtMood.Effect.WASH) > CrtMood.holdMs(CrtMood.Effect.SWEEP))
        assertEquals(0L, CrtMood.holdMs(CrtMood.Effect.NONE))
    }
}
