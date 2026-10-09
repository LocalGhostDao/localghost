package com.localghost.app.ui

/**
 * WHEN THE SCREEN SHOWS ITS GLASS. The phosphor look is an easter egg, not a coat of paint: once
 * in a while, on a page change, the screen does one small thing for a second or two (a sweep
 * down the page, a wash of scanlines, the heading typing itself in) and then is plain again.
 * Never twice inside [quietMs], and only one page change in four once that quiet has passed,
 * so a session sees one every few minutes at most. Pure: the roll comes from the caller.
 */
object CrtMood {
    enum class Effect { NONE, SWEEP, WASH, TYPE }

    const val quietMs = 90_000L

    /** The effect for a page change at [nowMs], given the last effect's time and a random [roll]. */
    fun pick(nowMs: Long, lastMs: Long, roll: Int): Effect {
        if (lastMs > 0 && nowMs - lastMs < quietMs) return Effect.NONE
        return when (((roll % 12) + 12) % 12) {
            0 -> Effect.SWEEP
            1 -> Effect.WASH
            2 -> Effect.TYPE
            else -> Effect.NONE
        }
    }

    /** How long each effect holds the screen, in ms. */
    fun holdMs(e: Effect): Long = when (e) {
        Effect.SWEEP -> 320L
        Effect.WASH -> 1_600L
        Effect.TYPE -> 1_200L
        Effect.NONE -> 0L
    }
}
