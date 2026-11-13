package com.localghost.app.qr

/**
 * WHAT THE SCANNER SAYS when it can see a code and cannot read it: move closer, move back, hold
 * steady. Pure, so the tests hold it; the screen feeds it one frame at a time and shows the words.
 *
 * It replaces a counter the screen kept that went up on a frame that sampled a grid and decoded
 * nothing, and came down only on a decode. So the hint, once earned, stayed on the screen after
 * the code had left it ("move closer , the code is too small to read", while pointing at the
 * ceiling), and at the next opening of the scanner too, since the counter outlived the screen.
 * And the frames that most need "move closer" never counted: a code so far away that two finders
 * show and no grid can be sampled.
 */
object ScanCoach {
    enum class Hint { CLOSER, BACK, STEADY }

    /** [streak]: frames in a row that saw a code and read nothing; [blind]: frames in a row that
     *  saw nothing at all; [modulePx]: the last module size measured, 0 for none yet. */
    data class State(val streak: Int = 0, val blind: Int = 0, val modulePx: Double = 0.0)

    /** Frames of seeing-and-not-reading before a hint (about a second at the in-view rate). */
    const val AFTER = 6
    /** Frames of seeing nothing before the hint goes: one blurred frame is not the code leaving. */
    const val FORGET = 2
    /** Under this many pixels a module the code is too far to read. Over [TOO_BIG] an enrolment
     *  code (57 to 61 modules and its margin) no longer fits the frame's 720 pixels; the old
     *  line was nine, from when a code that large did not read for another reason. */
    const val TOO_SMALL = 3.0
    const val TOO_BIG = 11.0

    /**
     * One frame on. [finders] is how many finder patterns the frame showed, [sampled] whether a
     * grid was sampled from them, [decoded] whether it read, [modulePx] the module size the frame
     * measured (0 when it measured none). A single finder is not a code in view: one 1:1:3:1:1
     * coincidence turns up in any textured room.
     */
    fun next(s: State, finders: Int, sampled: Boolean, decoded: Boolean, modulePx: Double): State = when {
        decoded -> State()
        sampled || finders >= 2 -> State(s.streak + 1, 0, if (modulePx > 0.0) modulePx else s.modulePx)
        s.blind + 1 >= FORGET -> State()
        else -> s.copy(blind = s.blind + 1)
    }

    /** The hint the state has earned, or null. */
    fun hint(s: State): Hint? = when {
        s.streak < AFTER -> null
        s.modulePx > 0.0 && s.modulePx < TOO_SMALL -> Hint.CLOSER
        s.modulePx > TOO_BIG -> Hint.BACK
        else -> Hint.STEADY
    }

    fun words(h: Hint): String = when (h) {
        Hint.CLOSER -> "move closer , the code is too small to read"
        Hint.BACK -> "move back a little , the code does not fit the frame"
        Hint.STEADY -> "hold steady and tap the code to focus"
    }
}
