package com.localghost.app.ui

import com.localghost.app.checkin.Feelings

/**
 * THE CHAT'S MICROPHONE, in words: the one line under the field that says what the microphone is
 * doing (listening, hearing you, the box working out the words, why nothing came of it), and the
 * languages the phone is set to, which the box is told so that a few seconds of speech are not
 * taken for a language you do not speak.
 *
 * Pure: no Android in here, so it runs in a plain JVM test.
 */
object VoiceAskText {
    /** While the microphone is open. Before anyone speaks it says what to do; once someone has,
     *  it says it hears them, and in a pause that it is about to stop. */
    fun listening(elapsedS: Long, heard: Boolean, speaking: Boolean): String {
        val clock = "● " + Feelings.clock(elapsedS * 1000)
        return when {
            !heard -> "$clock listening · ask, it stops when you do"
            speaking -> "$clock hearing you · ■ stops it now"
            else -> "$clock a pause · it stops in a moment, or carry on"
        }
    }

    /** The recording ended with nothing to send: why, or "" when there is nothing to say. */
    fun ended(why: String, error: String): String = when {
        error.isNotEmpty() -> "! $error"
        why == "nothing" -> "nothing heard · tap the microphone, then ask"
        else -> ""
    }

    /** While the box hears the take. */
    fun working(seconds: Int): String =
        "your box is working out the words" + (if (seconds >= 2) " · ${seconds}s" else "…") + " · ✕ drops it"

    const val NOT_WORDS = "your box heard no words in that · try again, a little closer"
    const val DROPPED = "dropped · nothing was kept"

    /** The words are in the field. [ms] is how long the box took (0 when it did not say);
     *  [atTheLimit]: the recording was stopped at two minutes. */
    fun heardIn(ms: Long, atTheLimit: Boolean): String {
        val took = if (ms > 0) "heard in " + (if (ms < 950) "under a second" else "${(ms + 500) / 1000} s") else "heard"
        return took + " · read it, then send" + if (atTheLimit) " · stopped at two minutes, the most a question holds" else ""
    }

    /** The "heard in…" line belongs to the words it came with: gone once they are sent or cleared. */
    fun isHeardLine(note: String): Boolean = note.startsWith("heard")

    /** The phone's languages as the box wants them: two or three letters each, lower case, in the
     *  phone's order, no more than five, no repeats ("en-GB", "ro-RO", "en-US" is "en,ro"). */
    fun langs(tags: List<String>): String {
        val out = ArrayList<String>()
        for (t in tags) {
            val l = t.trim().lowercase().substringBefore('-').substringBefore('_')
            if (l.length in 2..3 && l.all { it in 'a'..'z' } && l !in out) out.add(l)
            if (out.size == 5) break
        }
        return out.joinToString(",")
    }
}
