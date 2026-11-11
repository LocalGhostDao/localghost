package com.localghost.app.security

/**
 * The lock-on-background decision, extracted from MainActivity so it can be unit-tested without a
 * device. This is the part that has actually had bugs (an in-app picker backgrounding the app and
 * wrongly relocking it, or a rotation doing the same). The biometric and PIN transitions live in
 * MainActivity and are simple direct state changes; they are not modelled here.
 *
 * The rule: when the app goes to the background, lock and tear down the cached box-fed state,
 * UNLESS we launched an in-app picker/camera ourselves (expectResult), or a crash screen is
 * showing (which must survive backgrounding rather than being replaced by the lock).
 */
class AuthGate {

    /** True while a picker/camera we launched is foregrounded; suppresses the lock-on-stop. */
    var expectingResult: Boolean = false
        private set

    /** Call just before launching an in-app picker so the ensuing onStop doesn't lock us out. */
    fun expectResult() { expectingResult = true }

    /**
     * App went to the background. Returns true if the caller must lock (go to the gate) and tear
     * down the cached state. Returns false when an in-app picker is in flight, or a crash screen
     * is showing, or the user is mid-setup (pass keepCurrentScreen = true so the lock doesn't
     * replace those screens).
     *
     * Matches the original MainActivity.onStop exactly:
     *   if (expectingResult) keep session
     *   else if (crash showing) keep crash
     *   else lock + tearDown
     */
    fun onStop(keepCurrentScreen: Boolean, nowMs: Long = 0L): Boolean {
        if (expectingResult) {
            // the first stop a picker excused starts the clock: see staleOnStart
            if (excusedAtMs == 0L) excusedAtMs = nowMs
            return false
        }
        if (keepCurrentScreen) return false
        return true
    }

    /** When the first stop excused by a picker happened (elapsed-realtime ms), 0 when none is. */
    private var excusedAtMs: Long = 0L

    /**
     * The app is coming back. True when it was away under a picker's excuse for longer than the
     * grace: the caller locks as onStop would have. A picker is a minute's errand; the excuse
     * used to last until the process died, so a phone put down inside the file picker came back
     * unlocked hours later (the audit's first run found it, the second found it still there).
     * The excuse's clock is cleared either way.
     */
    fun staleOnStart(nowMs: Long, graceMs: Long = PICKER_GRACE_MS): Boolean {
        val since = excusedAtMs
        excusedAtMs = 0L
        if (since == 0L) return false
        if (nowMs - since <= graceMs) return false
        expectingResult = false
        return true
    }

    /**
     * The screen went dark. Whatever was excused is no longer: a phone whose screen is off is a
     * phone put down. Returns true when the caller must lock (the same exceptions as onStop for
     * the screens that are kept).
     */
    fun onScreenOff(keepCurrentScreen: Boolean): Boolean {
        expectingResult = false
        excusedAtMs = 0L
        return !keepCurrentScreen
    }

    /** App returned to the foreground. The picker guard is consumed here (one-shot). */
    fun onResume() { expectingResult = false }

    companion object {
        /** How long a picker or the camera may keep the app away before coming back means the gate. */
        const val PICKER_GRACE_MS = 120_000L

        /**
         * Whether the current screen must survive backgrounding rather than being replaced by the
         * lock. Two reasons a screen is kept: it is PRE-ENROLMENT (setup or the QR scan that feeds it,
         * where the user is not enrolled against a box yet, so locking to the biometric gate is wrong
         * and, for scan, the camera permission dialog backgrounds the app and would otherwise lock the
         * user out mid-setup), or a CRASH screen is showing (which must not be replaced by the lock).
         *
         * Pure and flag-based (not the Screen type, which is private to MainActivity) so it is unit
         * testable. MainActivity maps its current screen to these flags and passes the result to
         * onStop as keepCurrentScreen.
         */
        fun keepForScreen(preEnrolment: Boolean, crashShowing: Boolean): Boolean =
            preEnrolment || crashShowing
    }
}
