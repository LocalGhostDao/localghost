package com.localghost.app.chat

import com.localghost.app.net.WebSearch
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class AnswerLengthTest {
    @Test fun theSwitchCycles() {
        assertEquals("medium", AnswerLength.of(null))
        assertEquals("medium", AnswerLength.of("essay"))
        assertEquals("medium", AnswerLength.next("short"))
        assertEquals("long", AnswerLength.next("medium"))
        assertEquals("short", AnswerLength.next("long"))
        assertEquals("long", AnswerLength.next(null)) // an unknown word is medium, and medium's next is long
    }

    @Test fun theDepthFollowsTheLength() {
        assertEquals(WebSearch.Depth.QUICK, AnswerLength.depth("short"))
        assertEquals(WebSearch.Depth.NORMAL, AnswerLength.depth("medium"))
        assertEquals(WebSearch.Depth.DEEP, AnswerLength.depth("long"))
        assertTrue(WebSearch.Depth.DEEP.fetchTop >= 2 * WebSearch.Depth.NORMAL.fetchTop)
        assertTrue(WebSearch.Depth.QUICK.firstOnly)
        assertFalse(WebSearch.Depth.NORMAL.firstOnly)
    }

    @Test fun mediumDecidesAsTheChatAlwaysDid() {
        assertTrue(AnswerLength.wantWeb("on", "medium", false, 0, "", false))
        assertFalse(AnswerLength.wantWeb("off", "medium", true, 3, "", true))
        assertTrue(AnswerLength.wantWeb("auto", "medium", true, 3, "", false))
        assertFalse(AnswerLength.wantWeb("auto", "medium", false, 3, "", true)) // the plan's no is final in auto
        assertTrue(AnswerLength.wantWeb("auto", "medium", null, 0, "", true))   // no plan: the phone's word rule
        assertFalse(AnswerLength.wantWeb("auto", "medium", null, 0, "", false))
    }

    @Test fun longResearchesButKeepsTheGuards() {
        // the box said nothing outside was needed but wrote its searches: a long answer runs them
        assertTrue(AnswerLength.wantWeb("auto", "long", false, 3, "", false))
        // no plan: a long answer searches although the words look settled (the guards after still apply)
        assertTrue(AnswerLength.wantWeb("auto", "long", null, 0, "", false))
        // off is the person's own choice
        assertFalse(AnswerLength.wantWeb("off", "long", true, 3, "", true))
        // the box held its searches back because they carried the person's own details
        assertFalse(AnswerLength.wantWeb("auto", "long", false, 3, "the searches it would take carry your own details", false))
        // the box wrote no searches at all: nothing vetted to send
        assertFalse(AnswerLength.wantWeb("auto", "long", false, 0, "", false))
        // short never adds a search of its own
        assertFalse(AnswerLength.wantWeb("auto", "short", false, 3, "", false))
    }

    @Test fun whyALongAnswerDidNotSearch() {
        assertEquals("", AnswerLength.noSearchNote("auto", "medium", false, false, 0))
        assertEquals("", AnswerLength.noSearchNote("auto", "long", true, true, 3))
        assertTrue(AnswerLength.noSearchNote("off", "long", false, null, 0).startsWith("web is off"))
        assertTrue(AnswerLength.noSearchNote("auto", "long", false, false, 0).contains("planned no search"))
    }

    @Test fun thePhonesModelIsToldToo() {
        assertEquals("", AnswerLength.phoneInstruction("medium"))
        assertTrue(AnswerLength.phoneInstruction("short").contains("one to three sentences"))
        assertTrue(AnswerLength.phoneMaxTokens("long") > AnswerLength.phoneMaxTokens("medium"))
        assertTrue(AnswerLength.phoneMaxTokens("short") < AnswerLength.phoneMaxTokens("medium"))
        assertEquals(320, AnswerLength.phoneMaxTokens("medium")) // what the lifeboat always had
    }
}
