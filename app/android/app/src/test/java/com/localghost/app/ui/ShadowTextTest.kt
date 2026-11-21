package com.localghost.app.ui

import org.json.JSONObject
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.time.ZoneId

class ShadowTextTest {
    private val utc = ZoneId.of("UTC")

    // what GET /v1/shadow answers, cut down: two entries, four detectors, a change, one model
    private val fleetJson = """{
      "catalogue": [
        {"n":1,"slug":"gaslighting","name":"Gaslighting","origin":["PEOPLE"],"need":["AUTONOMY","COGNITION"],"level":"STRUCTURAL","danger":"CRITICAL",
         "post":"p","tells":"What you recorded.","does":"From your own account only.","detectors":["told-it-did-not-happen"]},
        {"n":7,"slug":"manufactured-consent","name":"Manufactured consent","origin":["PEOPLE"],"need":["BELONGING"],"level":"HABITUAL","danger":"HIGH",
         "new":true,"post":"p","tells":"t","does":"Not built. It needs a classifier."}
      ],
      "detectors": [
        {"id":"cold-read","entry":29,"name":"read cold","reads":"a saved answer","unit":"how far the two answers must differ","baseline":1,"min":0.5,"max":1,"step":0.5,
         "announce":true,"model":true,"threshold":1,"fired":2,"lastFired":1795000000},
        {"id":"praise-openers","entry":29,"name":"opens with praise","reads":"how answers begin","unit":"the share","baseline":0.25,"min":0.1,"max":0.5,"step":0.05,
         "announce":true,"threshold":0.3,"mutedUntil":1795100000,"fired":0},
        {"id":"caved","entry":29,"name":"gave way when pushed","reads":"a chat","unit":"one such exchange","baseline":1,"min":1,"max":1,"announce":true,"model":true,"threshold":1,"fired":1,"lastFired":1795000000},
        {"id":"told-it-did-not-happen","entry":1,"name":"told it did not happen","reads":"your own notes","unit":"such sentences in thirty days","baseline":3,"min":2,"max":8,"step":1,
         "announce":false,"threshold":3,"fired":0},
        {"id":"from-a-newer-box","entry":99,"name":"something new","reads":"x","unit":"y","baseline":1,"min":1,"max":3,"step":1,"announce":true,"threshold":1,"fired":0}
      ],
      "nextReset": 1800000000, "resetDays": 90, "maxMuteHours": 72,
      "changes": [{"at":1795000000,"detector":"praise-openers","what":"threshold","old":"0.25","new":"0.3","quieter":true}],
      "arbiter": {"has":false,"up":false,"servedBy":"the main model","main":"gemma-4-12b"}
    }"""

    @Test fun theFleet() {
        val f = ShadowText.parseFleet(JSONObject(fleetJson))
        assertEquals(2, f.catalogue.size)
        assertTrue(f.catalogue[0].built)
        assertFalse(f.catalogue[1].built)
        assertEquals("PEOPLE · AUTONOMY · COGNITION · STRUCTURAL · CRITICAL", ShadowText.tags(f.catalogue[0]))
        assertEquals("PEOPLE · BELONGING · HABITUAL · HIGH · NEW", ShadowText.tags(f.catalogue[1]))
        assertEquals("5 detectors over 1 of 2 patterns. The other 1 are listed with why not.", ShadowText.builtCount(f))
        assertEquals(listOf("THE BOX'S OWN ANSWERS", "WHAT YOU WROTE ABOUT OTHER PEOPLE", "OTHER"), ShadowText.sections(f).map { it.first })
        assertEquals(listOf("cold-read", "praise-openers", "caved"), ShadowText.sections(f)[0].second.map { it.id })
        assertEquals("Every threshold goes back to its baseline every 90 days; next on 15 January 2027. A mute lasts 3 days at most. Neither is a setting.",
            ShadowText.resetLine(f, utc))
        assertEquals("18 Nov  opens with praise: threshold 0.25 to 0.3  (quieter)", ShadowText.changeLine(f.changes[0], "opens with praise", utc))
    }

    @Test fun thresholdsMoveInTheBoxsSteps() {
        val f = ShadowText.parseFleet(JSONObject(fleetJson))
        val (cold, praise, caved, told) = f.detectors
        // at the top of its bounds there is no quieter; one step louder is "partly"
        assertNull(ShadowText.stepped(cold, up = true))
        assertEquals(0.5, ShadowText.stepped(cold, up = false)!!, 1e-9)
        assertEquals(0.35, ShadowText.stepped(praise, up = true)!!, 1e-9)
        assertEquals(0.25, ShadowText.stepped(praise, up = false)!!, 1e-9)
        // a fixed detector has nothing to move
        assertFalse(caved.tunable)
        assertNull(ShadowText.stepped(caved, up = true))
        assertEquals("fixed: one such exchange", ShadowText.thresholdLine(caved))
        assertEquals("speaks at 1 (the baseline; 0.5 to 1)", ShadowText.thresholdLine(cold))
        assertEquals("speaks at 0.3 (baseline 0.25; 0.1 to 0.5)", ShadowText.thresholdLine(praise))
        assertEquals(4.0, ShadowText.stepped(told, up = true)!!, 1e-9)
        assertEquals(2.0, ShadowText.stepped(told, up = false)!!, 1e-9)
        // a box that sends no step (an older one) offers no buttons, not a guessed step
        val old = ShadowText.parseFleet(JSONObject("""{"detectors":[{"id":"sessions","entry":18,"name":"n","baseline":3,"min":2,"max":10,"threshold":3}]}""")).detectors[0]
        assertFalse(old.tunable)
        assertEquals("0.9", ShadowText.num(0.9)); assertEquals("3", ShadowText.num(3.0)); assertEquals("0.25", ShadowText.num(0.25))
    }

    @Test fun whereItsWordsGo() {
        val f = ShadowText.parseFleet(JSONObject(fleetJson))
        val (cold, praise, _, told) = f.detectors
        assertEquals("sent to this phone when it speaks", ShadowText.sentLine(cold))
        // what a person wrote about someone is never put on a lock screen
        assertEquals("kept here for you to find, never sent", ShadowText.sentLine(told))
        assertEquals("spoken 2 times, last 18 Nov", ShadowText.spokenLine(cold, utc))
        assertEquals("has not spoken", ShadowText.spokenLine(praise, utc))
        assertEquals("muted until 19 Nov, 14:53. It goes on recording; what it sees is kept and marked.", ShadowText.mutedLine(praise, 1795000000, utc))
        assertEquals("", ShadowText.mutedLine(praise, 1795100001, utc))
    }

    @Test fun whichModelReads() {
        val one = ShadowText.parseFleet(JSONObject(fleetJson)).arbiter
        assertEquals("the cold read is by gemma-4-12b, with nothing about you", ShadowText.readBy(one))
        assertTrue(ShadowText.readByNote(one).contains("not a second opinion"))
        val two = ShadowText.Arbiter(true, "Qwen3-4B-Q4_K_M", true, "", "Qwen3-4B-Q4_K_M", "gemma-4-12b")
        assertEquals("the cold read is by Qwen3-4B-Q4_K_M, a second model beside gemma-4-12b", ShadowText.readBy(two))
        assertEquals("", ShadowText.readByNote(two))
        val down = ShadowText.Arbiter(true, "Qwen3-4B-Q4_K_M", false, "died after 3s up", "nothing yet", "gemma-4-12b")
        assertEquals("the cold read is waiting for Qwen3-4B-Q4_K_M, which is not up: died after 3s up", ShadowText.readBy(down))
        // an older box says nothing about it
        assertEquals("the cold read is by the box's model", ShadowText.readBy(null))
    }

    @Test fun aChatsTrail() {
        val t = ShadowText.parseTrail(JSONObject("""{"chat":7,"reading":false,"asked":{"since":1795000000,"done":1795000090,"read":1},
          "trail":[
            {"msgId":11,"at":1795000000000,"asked":["What do you want from it?"],"given":["searched your memories"],"reasoning":"r","opensWithPraise":true,
             "read":{"kind":"judgement","verdict":"no","sided":"yes","cold":"It may have been fine.","coldModel":"gemma-4-12b","sameModel":true,
                     "conduct":[{"kind":"caved","quote":"You are right, it is fine.","msgId":11,"after":"are you sure?"}]}},
            {"msgId":13,"at":1795000100000,"asked":[],"given":[],"read":{"kind":"other","sameModel":true}},
            {"msgId":15,"at":1795000200000,"asked":[],"given":[]}
          ]}"""))
        assertEquals(7L, t.chat)
        assertEquals(3, t.entries.size)
        val first = t.entries[0]
        assertTrue(first.praise)
        assertEquals("read cold: a different conclusion (by the same model, with nothing about you)", ShadowText.readLine(first.read))
        assertEquals("gave way when pushed", ShadowText.findingTitle(first.read!!.conduct[0].kind))
        assertEquals("looked at; not a question of judgement, so not read cold", ShadowText.readLine(t.entries[1].read))
        assertEquals("not read yet", ShadowText.readLine(t.entries[2].read))
        assertEquals("read 18 Nov, 11:08: 1 answer", ShadowText.askedLine(t, 1795000100, utc))
        val other = ShadowText.Read("personal", "partly", "no", "c", "Qwen3-4B", false, emptyList())
        assertEquals("read cold: the same direction, a different weight (by Qwen3-4B)", ShadowText.readLine(other))
    }

    @Test fun aReadThatWasAskedFor() {
        fun trail(extra: String) = ShadowText.parseTrail(JSONObject("""{"chat":7,"trail":[]$extra}"""))
        assertEquals("", ShadowText.askedLine(trail(""), 0, utc))
        assertTrue(ShadowText.askedLine(trail(""","reading":true,"asked":{"since":1795000000,"read":0}"""), 1795000010, utc).startsWith("the arbiter is reading this chat now"))
        assertEquals("the model was busy with a person.",
            ShadowText.askedLine(trail(""","asked":{"since":1795000000,"done":1795000005,"read":0,"why":"the model was busy with a person."}"""), 1795000010, utc))
        assertEquals("asked 18 Nov, 11:06: there was nothing left to read",
            ShadowText.askedLine(trail(""","asked":{"since":1795000000,"done":1795000005,"read":0}"""), 1795000010, utc))
    }

    @Test fun whereALinkLands() {
        assertEquals(12L, ShadowText.chatOf("chat:12"))
        assertEquals(0L, ShadowText.chatOf("cold-read"))
        assertEquals(0L, ShadowText.chatOf("chat:x"))
        val obs = ShadowText.parseObservations(JSONObject("""{"observations":[{"id":3,"detector":"cold-read","subject":"chat:7/msg:11","at":1795000000,"title":"t","body":"b","muted":true}]}"""))
        assertEquals(1, obs.size)
        assertTrue(obs[0].muted)
        assertEquals("chat:7/msg:11", obs[0].subject)
    }
}
