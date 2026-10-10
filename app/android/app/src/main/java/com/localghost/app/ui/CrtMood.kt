package com.localghost.app.ui

/**
 * WHEN THE SCREEN SHOWS ITS GLASS. The phosphor look is an easter egg, not a coat of paint: once
 * in a while, on a page change, the screen does one small thing for a second or two (a stray
 * line blinking somewhere on the glass, a wash of scanlines, the heading typing itself in) and
 * then is plain again.
 * Never twice inside [quietMs], and only one page change in four once that quiet has passed,
 * so a session sees one every few minutes at most. Pure: the roll comes from the caller.
 */
object CrtMood {
    enum class Effect { NONE, LINE, WASH, TYPE }

    const val quietMs = 90_000L

    /** The effect for a page change at [nowMs], given the last effect's time and a random [roll]. */
    fun pick(nowMs: Long, lastMs: Long, roll: Int): Effect {
        if (lastMs > 0 && nowMs - lastMs < quietMs) return Effect.NONE
        return when (((roll % 12) + 12) % 12) {
            0 -> Effect.LINE
            1 -> Effect.WASH
            2 -> Effect.TYPE
            else -> Effect.NONE
        }
    }

    /** One blink of the stray line: where (a fraction of the glass's height), how long it
     *  shows, how long before the next. */
    data class Blink(val y: Float, val onMs: Long, val offMs: Long)

    /** The line's blinks for two random draws in [0, 1): a first blink somewhere in the middle
     *  three fifths of the glass (never an edge, where it reads as a border), a second a little
     *  under it, and a third, shorter, elsewhere, only on the second draw's say-so. */
    fun lineBlinks(r1: Float, r2: Float): List<Blink> {
        val y1 = 0.2f + 0.6f * r1
        val out = arrayListOf(Blink(y1, 90L, 70L), Blink((y1 + 0.03f + 0.05f * r2).coerceAtMost(0.85f), 140L, 110L))
        if (r2 > 0.5f) out.add(Blink(0.2f + 0.6f * ((r1 + 0.37f) % 1f), 60L, 0L))
        return out
    }

    /** How long each effect holds the screen, in ms. */
    fun holdMs(e: Effect): Long = when (e) {
        Effect.LINE -> 600L
        Effect.WASH -> 1_600L
        Effect.TYPE -> 1_200L
        Effect.NONE -> 0L
    }
}
