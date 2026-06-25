package com.localghost.app.security

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Tests the lock-on-background decision that MainActivity.onStop delegates to AuthGate. Every test
 * here guards the real code path: MainActivity calls exactly authGate.expectResult(),
 * authGate.onStop(crashShowing), and authGate.onResume(). These are the transitions that have had
 * bugs (picker/rotation wrongly relocking, crash being replaced by the lock).
 *
 * "Returns true" means MainActivity locks to the gate and tears down the cached box state, so a
 * return to the app demands biometric + PIN again.
 */
class AuthGateTest {

    @Test fun normal_background_locks_and_tears_down() {
        val g = AuthGate()
        assertTrue("a plain background must lock", g.onStop(crashShowing = false))
    }

    @Test fun in_app_picker_does_not_lock() {
        val g = AuthGate()
        g.expectResult()                                  // we launched an image/file picker
        assertFalse("picker backgrounding must keep the session", g.onStop(crashShowing = false))
    }

    @Test fun picker_guard_is_one_shot_cleared_on_resume() {
        val g = AuthGate()
        g.expectResult()
        assertFalse(g.onStop(crashShowing = false))        // first stop: kept (picker)
        g.onResume()                                       // back from picker
        assertTrue("a later real background must lock", g.onStop(crashShowing = false))
    }

    @Test fun crash_screen_survives_background() {
        val g = AuthGate()
        assertFalse("crash must not be replaced by the lock", g.onStop(crashShowing = true))
    }

    @Test fun picker_guard_wins_even_if_crash_flag_false() {
        // If we set expectResult, it suppresses the lock regardless of crash state.
        val g = AuthGate()
        g.expectResult()
        assertFalse(g.onStop(crashShowing = false))
    }

    @Test fun guard_does_not_persist_without_resume_is_actually_one_call() {
        // onStop does not itself clear the guard; only onResume does. Two stops in a row while a
        // picker is in flight both keep the session (defensive: the OS can deliver odd orderings).
        val g = AuthGate()
        g.expectResult()
        assertFalse(g.onStop(crashShowing = false))
        assertFalse(g.onStop(crashShowing = false))
        g.onResume()
        assertTrue(g.onStop(crashShowing = false))
    }

    @Test fun fresh_gate_has_no_guard_set() {
        val g = AuthGate()
        assertFalse(g.expectingResult)
        assertTrue(g.onStop(crashShowing = false))
    }
}
