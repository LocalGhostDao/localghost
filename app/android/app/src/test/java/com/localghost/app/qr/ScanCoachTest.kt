package com.localghost.app.qr

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Test

class ScanCoachTest {
    private fun run(s: ScanCoach.State, n: Int, finders: Int, sampled: Boolean, px: Double = 0.0): ScanCoach.State {
        var st = s
        repeat(n) { st = ScanCoach.next(st, finders, sampled, false, px) }
        return st
    }

    @Test fun aHintIsEarnedBySeeingAndNotReading() {
        var s = run(ScanCoach.State(), ScanCoach.AFTER - 1, finders = 3, sampled = true, px = 2.4)
        assertNull("one frame short of a hint", ScanCoach.hint(s))
        s = ScanCoach.next(s, 3, true, false, 2.4)
        assertEquals(ScanCoach.Hint.CLOSER, ScanCoach.hint(s))
        // a code so far that two finders show and no grid samples counts too: it is the one that most needs it
        s = run(ScanCoach.State(), ScanCoach.AFTER, finders = 2, sampled = false, px = 0.0)
        assertEquals("no size measured: say the thing that is always true", ScanCoach.Hint.STEADY, ScanCoach.hint(s))
        // a size measured once is kept through frames that measure none
        s = run(ScanCoach.next(ScanCoach.State(), 3, true, false, 2.0), ScanCoach.AFTER, finders = 2, sampled = false)
        assertEquals(ScanCoach.Hint.CLOSER, ScanCoach.hint(s))
    }

    @Test fun theHintGoesWhenTheCodeDoes() {
        val earned = run(ScanCoach.State(), ScanCoach.AFTER + 3, finders = 3, sampled = true, px = 2.4)
        assertEquals(ScanCoach.Hint.CLOSER, ScanCoach.hint(earned))
        // one blurred frame is not the code leaving
        val blink = ScanCoach.next(earned, 0, false, false, 0.0)
        assertEquals(ScanCoach.Hint.CLOSER, ScanCoach.hint(blink))
        // back in view: the blink is forgotten, the streak goes on
        assertEquals(earned.streak + 1, ScanCoach.next(blink, 3, true, false, 2.4).streak)
        // gone for two frames: nothing is said, and nothing is remembered of the old code
        val gone = run(earned, ScanCoach.FORGET, finders = 0, sampled = false)
        assertNull(ScanCoach.hint(gone))
        assertEquals(ScanCoach.State(), gone)
        // a single finder is a coincidence in a textured room, not a code in view
        assertEquals(ScanCoach.State(), run(earned, ScanCoach.FORGET, finders = 1, sampled = false))
        // and a decode clears everything at once
        assertEquals(ScanCoach.State(), ScanCoach.next(earned, 3, true, true, 2.4))
    }

    @Test fun whichHint() {
        fun at(px: Double) = ScanCoach.hint(ScanCoach.State(streak = ScanCoach.AFTER, modulePx = px))
        assertEquals(ScanCoach.Hint.CLOSER, at(2.9))
        assertEquals(ScanCoach.Hint.STEADY, at(3.0))
        // a v11 code at ten pixels a module reads now (it did not, and the hint used to say "too big" from nine)
        assertEquals(ScanCoach.Hint.STEADY, at(10.0))
        assertEquals(ScanCoach.Hint.BACK, at(11.5))
        assertEquals(ScanCoach.Hint.STEADY, at(0.0))
        for (h in ScanCoach.Hint.values()) assert(!ScanCoach.words(h).contains("—"))
    }
}
