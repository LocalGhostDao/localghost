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
     * is showing (pass crashShowing = true so the lock doesn't replace it).
     *
     * Matches the original MainActivity.onStop exactly:
     *   if (expectingResult) keep session
     *   else if (crash showing) keep crash
     *   else lock + tearDown
     */
    fun onStop(crashShowing: Boolean): Boolean {
        if (expectingResult) return false
        if (crashShowing) return false
        return true
    }

    /** App returned to the foreground. The picker guard is consumed here (one-shot). */
    fun onResume() { expectingResult = false }
}
