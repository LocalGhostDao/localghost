package com.localghost.app.security

import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * Tests the lock-on-background decision that MainActivity.onStop delegates to AuthGate. Every test
 * here guards the real code path: MainActivity calls exactly authGate.expectResult(),
 * authGate.onStop(keepCurrentScreen), and authGate.onResume(). These are the transitions that have had
 * bugs (picker/rotation wrongly relocking, crash being replaced by the lock).
 *
 * "Returns true" means MainActivity locks to the gate and tears down the cached box state, so a
 * return to the app demands biometric + PIN again.
 */
class AuthGateTest {

    @Test fun normal_background_locks_and_tears_down() {
        val g = AuthGate()
        assertTrue("a plain background must lock", g.onStop(keepCurrentScreen = false))
    }

    @Test fun in_app_picker_does_not_lock() {
        val g = AuthGate()
        g.expectResult()                                  // we launched an image/file picker
        assertFalse("picker backgrounding must keep the session", g.onStop(keepCurrentScreen = false))
    }

    @Test fun picker_guard_is_one_shot_cleared_on_resume() {
        val g = AuthGate()
        g.expectResult()
        assertFalse(g.onStop(keepCurrentScreen = false))        // first stop: kept (picker)
        g.onResume()                                       // back from picker
        assertTrue("a later real background must lock", g.onStop(keepCurrentScreen = false))
    }

    @Test fun crash_screen_survives_background() {
        val g = AuthGate()
        assertFalse("crash must not be replaced by the lock", g.onStop(keepCurrentScreen = true))
    }

    @Test fun picker_guard_wins_even_if_crash_flag_false() {
        // If we set expectResult, it suppresses the lock regardless of crash state.
        val g = AuthGate()
        g.expectResult()
        assertFalse(g.onStop(keepCurrentScreen = false))
    }

    @Test fun guard_does_not_persist_without_resume_is_actually_one_call() {
        // onStop does not itself clear the guard; only onResume does. Two stops in a row while a
        // picker is in flight both keep the session (defensive: the OS can deliver odd orderings).
        val g = AuthGate()
        g.expectResult()
        assertFalse(g.onStop(keepCurrentScreen = false))
        assertFalse(g.onStop(keepCurrentScreen = false))
        g.onResume()
        assertTrue(g.onStop(keepCurrentScreen = false))
    }

    @Test fun fresh_gate_has_no_guard_set() {
        val g = AuthGate()
        assertFalse(g.expectingResult)
        assertTrue(g.onStop(keepCurrentScreen = false))
    }
    // --- keepForScreen: the screen-to-keep mapping MainActivity.onStop uses. These pin Bug 2 (a
    // mid-setup background , including the camera permission dialog on the scan screen , wrongly
    // re-prompting for a fingerprint) so it cannot regress. ---

    @Test fun keepForScreen_preEnrolment_keeps_session() {
        // setup and the QR scan that feeds it are pre-enrolment; backgrounding must NOT lock.
        assertTrue("pre-enrolment screens survive backgrounding",
            AuthGate.keepForScreen(preEnrolment = true, crashShowing = false))
    }

    @Test fun keepForScreen_crash_keeps_session() {
        assertTrue("crash must not be replaced by the lock",
            AuthGate.keepForScreen(preEnrolment = false, crashShowing = true))
    }

    @Test fun keepForScreen_normal_screen_locks() {
        // an enrolled, in-use screen (not pre-enrolment, no crash) must lock on background.
        assertFalse("a normal screen must lock",
            AuthGate.keepForScreen(preEnrolment = false, crashShowing = false))
    }

    @Test fun a_picker_that_comes_back_within_its_grace_keeps_the_session() {
        val g = AuthGate()
        g.expectResult()
        assertFalse(g.onStop(keepCurrentScreen = false, nowMs = 1_000))
        assertFalse(g.staleOnStart(nowMs = 1_000 + AuthGate.PICKER_GRACE_MS))
    }

    @Test fun a_picker_left_open_past_its_grace_means_the_gate() {
        // the phone put down inside the file picker: hours later the app comes back locked
        val g = AuthGate()
        g.expectResult()
        assertFalse(g.onStop(keepCurrentScreen = false, nowMs = 1_000))
        assertFalse(g.onStop(keepCurrentScreen = false, nowMs = 50_000)) // the clock runs from the first stop
        assertTrue(g.staleOnStart(nowMs = 1_001 + AuthGate.PICKER_GRACE_MS))
        assertFalse("the excuse is spent", g.expectingResult)
        assertFalse("and asked once", g.staleOnStart(nowMs = 9_999_999))
    }

    @Test fun no_excuse_no_stale_start() {
        val g = AuthGate()
        assertTrue(g.onStop(keepCurrentScreen = false, nowMs = 1_000))
        assertFalse(g.staleOnStart(nowMs = 9_999_999))
    }

    @Test fun the_screen_going_dark_locks_even_under_a_picker() {
        val g = AuthGate()
        g.expectResult()
        assertTrue(g.onScreenOff(keepCurrentScreen = false))
        assertFalse(g.expectingResult)
        assertFalse("setup, the scan and a crash screen are kept", g.onScreenOff(keepCurrentScreen = true))
    }
}
