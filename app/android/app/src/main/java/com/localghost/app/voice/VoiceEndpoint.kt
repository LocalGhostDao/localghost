package com.localghost.app.voice

/**
 * WHEN A QUESTION ASKED ALOUD HAS ENDED. The recorder feeds this the loudness of each tenth of a
 * second (root mean square, 0..1 of full scale) and it says: go on, they have finished, or nobody
 * spoke. It is what lets the chat's microphone stop by itself, and what keeps the room's hum at
 * both ends of the take from reaching the box, where a speech model handed silence invents words.
 *
 * Speech is whatever stands clear of the room. The room is tracked as it goes: it falls at once
 * when a quieter moment comes and rises slowly, so a fan that starts mid-question becomes the
 * room in a while and a person talking does not (speech has gaps; a fan has none). A take that
 * opens on a steady sound (a car, an extractor) has that sound taken as the room within a
 * second: speech moves from one tenth of a second to the next, a hum does not. A voice also has
 * to stand clear of how loud this take has been, so a busy cafe behind someone speaking up reads
 * as the pause it is.
 *
 * It errs toward stopping late. A take cut short costs a second tap (the words are added to
 * what is in the field); a take that runs on costs a tap on stop.
 *
 * Pure: numbers in, a verdict out; no Android in here, so it runs in a plain JVM test.
 */
class VoiceEndpoint(private val chunkMs: Int = 100) {
    enum class Verdict { GO_ON, ENDED, NOTHING }

    companion object {
        /** Below this nothing is speech: about -42 dB of full scale, under a quiet voice at arm's length. */
        const val FLOOR = 0.008f
        /** Speech is this many times the room. */
        const val OVER_ROOM = 3.0f
        /** And at least this share of the loudest this take has been. */
        const val OF_LOUDEST = 0.15f
        /** A sound that keeps within this share of itself from one tenth to the next is steady. */
        const val STEADY = 0.25f
        /** Steady for this long before anyone has spoken: it is the room. */
        const val STEADY_MS = 800
        /** This much speech in a row is someone talking, not a cup put down. */
        const val SPEECH_MS = 200
        /** Quiet for this long after speech: they have finished. Long enough to draw breath and think. */
        const val TAIL_MS = 1800
        /** Nothing said in this long from the start: stop, and say so. */
        const val NOTHING_MS = 8000
        /** Kept before the first word and after the last when the take is cut to what was said. */
        const val LEAD_MS = 300
        const val TRAIL_MS = 500
    }

    /** The room, as loud as it has lately been with nobody speaking. */
    var room = FLOOR / OVER_ROOM
        private set
    /** Someone has spoken in this take. */
    var heard = false
        private set
    /** They are speaking now. */
    var speaking = false
        private set
    /** Where speech began and where it last stopped, in milliseconds of the take; -1 before any. */
    var firstMs = -1L
        private set
    var lastMs = -1L
        private set

    private var loudest = 0f
    private var run = 0   // milliseconds of speech in a row
    private var quiet = 0 // milliseconds of quiet in a row
    private var at = 0L   // milliseconds fed so far
    private var runStart = 0L
    private var prev = -1f
    private var steady = 0      // milliseconds the level has held still
    private var lively = false  // a sound that moves has been heard: a voice, not a hum

    /** One more tenth of a second. */
    fun feed(rms: Float): Verdict {
        val level = if (rms.isNaN() || rms < 0f) 0f else rms
        val bar = maxOf(FLOOR, room * OVER_ROOM, loudest * OF_LOUDEST)
        val voiced = level >= bar
        // the room: down at once, up slowly (one part in a hundred each tenth of a second)
        room = if (level < room) (room + level) / 2 else minOf(level, room * 1.01f)
        if (room < FLOOR / (OVER_ROOM * 4)) room = FLOOR / (OVER_ROOM * 4)
        // how loud the take has been, fading: a shout at the start is not the measure a minute on
        loudest = maxOf(level, loudest * 0.995f)
        val moved = prev >= 0f && kotlin.math.abs(level - prev) > STEADY * maxOf(level, prev)
        steady = if (prev >= 0f && !moved) steady + chunkMs else 0
        prev = level
        if (voiced && run > 0 && moved) lively = true
        if (!lively && steady >= STEADY_MS && level > room) {
            // what the take opened on has not moved: it is the room, and nobody has spoken yet
            room = level
            heard = false; speaking = false; firstMs = -1; lastMs = -1; run = 0
            at += chunkMs
            return if (at >= NOTHING_MS) Verdict.NOTHING else Verdict.GO_ON
        }
        val start = at
        at += chunkMs
        if (voiced) {
            if (run == 0) runStart = start
            run += chunkMs
            quiet = 0
            if (run >= SPEECH_MS) {
                if (!heard) { heard = true; firstMs = runStart }
                speaking = true
            }
            // once someone has spoken, any sound counts toward where they stopped: a last short
            // word after a breath ("...take it?") is one tenth of a second, and must not be cut
            if (heard) lastMs = at
        } else {
            run = 0
            quiet += chunkMs
            speaking = false
        }
        return when {
            heard && quiet >= TAIL_MS -> Verdict.ENDED
            !heard && at >= NOTHING_MS -> Verdict.NOTHING
            else -> Verdict.GO_ON
        }
    }

    /** The part of the take to keep, in milliseconds: what was said, with a little before and
     *  after; null when nobody spoke. */
    fun keep(totalMs: Long): Pair<Long, Long>? {
        if (!heard || firstMs < 0) return null
        val from = (firstMs - LEAD_MS).coerceAtLeast(0)
        val to = (lastMs + TRAIL_MS).coerceAtMost(totalMs)
        return if (to > from) from to to else null
    }
}
