package com.localghost.app.phrases

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The walk with levels and known phrases, on a pack built in code (unit tests cannot open the
 * asset packs; the scratch harness in the session notes runs the same checks over all eighteen).
 */
class PhraseEngineTest {
    private fun p(id: String, level: Int, weight: Int, situation: Situation = Situation.POLITE, slots: Set<Slot> = emptySet()) =
        Phrase(id, id, id, id, slots = slots, weight = weight, situation = situation, level = level)

    private val pack = PhrasePack(
        lang = "xx", name = "Test", nativeName = "Test", countries = listOf("XX"), ttsTag = "xx", lateHours = false,
        scriptNote = "", genderedSpeech = false, emergency = emptyList(),
        phrases = listOf(
            p("good_morning", 1, 10, Situation.GREETINGS, setOf(Slot.MORNING)),
            p("good_evening", 1, 10, Situation.GREETINGS, setOf(Slot.EVENING)),
            p("coffee", 1, 9, Situation.CAFE, setOf(Slot.MORNING)),
            p("thanks", 1, 9),
            p("please", 1, 6),
            p("bill", 1, 10, Situation.RESTAURANT, setOf(Slot.EVENING)),
            p("help", 1, 10, Situation.EMERGENCY),
            p("freddo", 2, 7, Situation.CAFE, setOf(Slot.MORNING)),
            p("numbers", 2, 7, Situation.NUMBERS),
            p("left_right", 2, 6, Situation.TRANSPORT),
            p("too_expensive", 2, 5, Situation.SHOP),
            p("wifi", 2, 5, Situation.HOTEL),
            p("my_name", 3, 6, Situation.SMALLTALK),
            p("i_like", 3, 5, Situation.OPINION),
            p("siga_siga", 4, 6, Situation.LOCAL),
        ),
    )

    @Test fun nothingKnownIsBandOneAndAThinSlotBorrows() {
        assertEquals(1, PhraseEngine.band(pack, emptySet()))
        val walk = PhraseEngine.order(pack, Slot.MORNING)
        assertEquals("good_morning", walk[0].id)
        assertTrue(walk.none { it.situation == Situation.EMERGENCY })
        // Three level-1 cards in the morning is under MIN_DUE, so level 2 joins , and no further.
        assertTrue(walk.none { it.level > 2 })
        assertTrue(walk.count { it.level == 2 } == 5)
        assertEquals(listOf("good_morning", "coffee", "thanks", "freddo", "numbers", "please", "left_right", "too_expensive", "wifi"), walk.map { it.id })
        // The evening slot has enough at level 1 (with the any-time cards) only if MIN_DUE is met;
        // here it is not either, so the same borrow applies, still never past band + 1 when that suffices.
        assertTrue(PhraseEngine.order(pack, Slot.EVENING).none { it.level > 2 })
    }

    @Test fun bandClimbsAtSixtyPercent() {
        // Level 1 has six learnable phrases (help is emergency); four known clears 60%, three does not.
        assertEquals(1, PhraseEngine.band(pack, setOf("coffee", "thanks", "please")))
        assertEquals(2, PhraseEngine.band(pack, setOf("coffee", "thanks", "please", "bill")))
        assertEquals(2, PhraseEngine.band(pack, setOf("coffee", "thanks", "please", "good_morning", "good_evening", "bill")))
        // Level 2 has five; all known plus level 1 -> band 3; level 3 both known -> 4.
        val l12 = setOf("coffee", "thanks", "please", "good_morning", "good_evening", "bill", "freddo", "numbers", "left_right", "too_expensive", "wifi")
        assertEquals(3, PhraseEngine.band(pack, l12))
        assertEquals(4, PhraseEngine.band(pack, l12 + setOf("my_name", "i_like")))
    }

    @Test fun knownCardsLeaveTheWalkAndReturnAsReviews() {
        val known = setOf("coffee", "thanks", "please", "good_morning", "good_evening", "bill")
        val walk = PhraseEngine.order(pack, Slot.MORNING, known, 0)
        assertEquals("good_morning", walk[0].id) // the greeting leads even when known
        val body = walk.drop(1)
        // Due cards are the level-2 ones (band 2), heaviest first, with a review after every 4.
        assertEquals("freddo", body[0].id)
        val reviews = body.filter { it.id in known }
        val due = body.filter { it.id !in known }
        assertTrue(due.all { it.level >= 2 })
        assertTrue(reviews.isNotEmpty())
        assertTrue("each known card at most once", reviews.map { it.id }.toSet().size == reviews.size)
        assertEquals(body.indexOfFirst { it.id in known }, PhraseEngine.REVIEW_EVERY)
    }

    @Test fun dayKeyShiftsTheReviewNotTheDue() {
        val known = setOf("coffee", "thanks", "please", "good_evening", "bill")
        val a = PhraseEngine.order(pack, Slot.MORNING, known, 0)
        val b = PhraseEngine.order(pack, Slot.MORNING, known, 1)
        assertEquals(a.filter { it.id !in known }.map { it.id }, b.filter { it.id !in known }.map { it.id })
        val ra = a.drop(1).first { it.id in known }.id
        val rb = b.drop(1).first { it.id in known }.id
        assertTrue(ra != rb)
    }

    @Test fun everythingKnownIsTheReviewWalk() {
        val all = pack.phrases.filter { it.situation != Situation.EMERGENCY }.map { it.id }.toSet()
        val walk = PhraseEngine.order(pack, Slot.EVENING, all, 3)
        assertEquals("good_evening", walk[0].id)
        assertEquals(pack.phrases.count { it.inSlot(Slot.EVENING) && it.situation != Situation.EMERGENCY }, walk.size)
        assertEquals(Levels.MAX, PhraseEngine.band(pack, all))
    }

    @Test fun progressCountsPerLevel() {
        val prog = PhraseEngine.progress(pack, setOf("coffee", "freddo", "help"))
        assertEquals(1 to 6, prog[1])
        assertEquals(1 to 5, prog[2])
        assertEquals(0 to 2, prog[3])
        assertEquals(0 to 1, prog[4])
    }

    @Test fun pickThreadsKnownThrough() {
        val known = setOf("coffee", "thanks", "please", "good_morning", "good_evening", "bill")
        val pick = PhraseEngine.pick(pack, false, 5, 0, 0, known, 0)
        assertEquals(Slot.MORNING, pick.slot)
        assertEquals("good_morning", pick.phrase?.id)
        assertEquals("freddo", pick.next?.id)
    }
}
