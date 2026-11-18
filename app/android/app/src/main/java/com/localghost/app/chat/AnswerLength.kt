package com.localghost.app.chat

import com.localghost.app.net.WebSearch

/**
 * HOW MUCH ANSWER: the chat's third switch, beside incognito and web (16 November 2026, Vlad: "if
 * I ask about something or some place I want to choose if it's short answer, medium or long and on
 * long I want research with internet and local sources").
 *
 *  - short: the answer in one to three sentences; one search at most, two pages read.
 *  - medium: what the chat always did.
 *  - long: the story. The phone runs every search the box planned and reads twice the pages, and
 *    searches in auto although the box judged the question needed nothing from outside (when the
 *    box still wrote searches for it); the box brings more of the person's memories and more of
 *    its Wikipedia (ghost.synthd length.go).
 *
 * What a long answer does NOT override: web OFF (the person's own choice, said in the trail), a
 * plan the box withheld because its searches carried the person's own details, what the box
 * keeps itself (prices, the news), and the follow-up and first-person guards. Pure, for the tests.
 */
object AnswerLength {
    const val SHORT = "short"
    const val MEDIUM = "medium"
    const val LONG = "long"

    /** The stored word, medium for anything else. */
    fun of(s: String?): String = when (s) {
        SHORT, LONG -> s
        else -> MEDIUM
    }

    /** The switch's next position: short → medium → long → short. */
    fun next(s: String?): String = when (of(s)) {
        SHORT -> MEDIUM
        MEDIUM -> LONG
        else -> SHORT
    }

    /** How hard the phone searches for this length. */
    fun depth(s: String?): WebSearch.Depth = when (of(s)) {
        SHORT -> WebSearch.Depth.QUICK
        LONG -> WebSearch.Depth.DEEP
        else -> WebSearch.Depth.NORMAL
    }

    /**
     * Whether the phone searches the web before asking the box. [planSearch] is the box's plan's
     * verdict (null: no plan in time), [planQueries] how many searches it wrote, [withheld] why it
     * held them back (the person's own details), [looksFresh] the phone's own word rule. The
     * guards after this (the box's own numbers, a personal question with no plan, a follow-up
     * that may not borrow) still apply to what this returns.
     */
    fun wantWeb(mode: String, length: String?, planSearch: Boolean?, planQueries: Int, withheld: String, looksFresh: Boolean): Boolean {
        val long = of(length) == LONG
        return when {
            mode == "on" -> true
            mode != "auto" -> false // off is off, whatever the length
            planSearch == true -> true
            planSearch == false -> long && planQueries > 0 && withheld.isBlank()
            else -> looksFresh || long
        }
    }

    /** A line for the trail when a long answer could not search, "" when it searched or was not long. */
    fun noSearchNote(mode: String, length: String?, searched: Boolean, planSearch: Boolean?, planQueries: Int): String = when {
        of(length) != LONG || searched -> ""
        mode == "off" -> "web is off , the long answer draws on the box's own sources"
        planSearch == false && planQueries == 0 -> "the box planned no search for this , the long answer draws on its own sources"
        else -> ""
    }

    /** What the phone's own model is told about the length (no box); "" for medium. */
    fun phoneInstruction(s: String?): String = when (of(s)) {
        SHORT -> "Answer in one to three sentences."
        LONG -> "Give a full answer: the answer first, then the background and the details worth knowing."
        else -> ""
    }

    /** The phone's model's room for the answer. */
    fun phoneMaxTokens(s: String?): Int = when (of(s)) {
        SHORT -> 160
        LONG -> 900
        else -> 320
    }

    /** The word under the switch's icon. */
    fun label(s: String?): String = of(s)
}
