package com.localghost.app.voice

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.io.File
import kotlin.math.PI
import kotlin.math.sin

class VoiceEndpointTest {
    private val go = VoiceEndpoint.Verdict.GO_ON
    private val ended = VoiceEndpoint.Verdict.ENDED
    private val nothing = VoiceEndpoint.Verdict.NOTHING

    /** Speech as the recorder hears it: loud and never the same twice running. */
    private fun speech(i: Int, at: Float = 0.08f) = at * (0.55f + 0.45f * ((i * 7) % 5) / 4f)

    /** Feed a run of tenths; the first verdict that is not "go on", and after how many tenths. */
    private fun run(ep: VoiceEndpoint, levels: List<Float>): Pair<VoiceEndpoint.Verdict, Int> {
        levels.forEachIndexed { i, l -> val v = ep.feed(l); if (v != go) return v to i + 1 }
        return go to levels.size
    }

    @Test fun aQuestionInAQuietRoomEndsWhenThePersonDoes() {
        val ep = VoiceEndpoint()
        val room = List(5) { 0.0015f }               // half a second before they speak
        val said = List(30) { speech(it) }           // three seconds of question
        val after = List(40) { 0.0015f }
        val (v, n) = run(ep, room + said + after)
        assertEquals(ended, v)
        // 0.5 s of room, 3 s said, then the tail: 1.8 s of quiet and not a tenth sooner
        assertEquals(5 + 30 + VoiceEndpoint.TAIL_MS / 100, n)
        assertTrue(ep.heard)
        assertEquals(500L, ep.firstMs)
        assertEquals(3500L, ep.lastMs)
        // what is kept: a little before the first word, a little after the last
        assertEquals(200L to 4000L, ep.keep(5300))
    }

    @Test fun aPauseToThinkDoesNotEndIt() {
        val ep = VoiceEndpoint()
        val levels = List(10) { speech(it) } + List(15) { 0.002f } + List(10) { speech(it) }  // a second and a half of thought
        assertEquals(go, run(ep, levels).first)
        assertTrue(ep.speaking)
        assertEquals(3500L, ep.lastMs)
        assertEquals(ended, run(ep, List(18) { 0.002f }).first)
    }

    @Test fun nobodySpeaking() {
        val ep = VoiceEndpoint()
        val (v, n) = run(ep, List(100) { 0.002f })
        assertEquals(nothing, v)
        assertEquals(VoiceEndpoint.NOTHING_MS / 100, n)
        assertFalse(ep.heard)
        assertNull(ep.keep(8000))
    }

    @Test fun aCupPutDownIsNotSpeech() {
        val ep = VoiceEndpoint()
        // one loud tenth of a second, twice, apart: never two in a row
        val levels = List(80) { i -> if (i == 10 || i == 30) 0.2f else 0.002f }
        assertEquals(nothing, run(ep, levels).first)
        assertFalse(ep.heard)
    }

    @Test fun aSteadyHumIsTheRoomAndAVoiceOverItIsHeard() {
        // a car: loud from the first tenth, and it does not move. It is not taken for speech...
        val ep = VoiceEndpoint()
        assertEquals(go, run(ep, List(20) { 0.03f + 0.001f * (it % 2) }).first)
        assertFalse("a hum was taken for a voice", ep.heard)
        assertTrue(ep.room > 0.02f)
        // ...a voice over it is, and the take ends when the voice does, with the hum still there
        val (v, n) = run(ep, List(20) { speech(it, 0.25f) } + List(30) { 0.03f + 0.001f * (it % 2) })
        assertEquals(ended, v)
        assertEquals(20 + VoiceEndpoint.TAIL_MS / 100, n)
        assertEquals(2000L, ep.firstMs)
        // and a hum with nobody over it is "nothing heard", at the usual eight seconds
        val alone = VoiceEndpoint()
        val (v2, n2) = run(alone, List(100) { 0.03f })
        assertEquals(nothing, v2)
        assertEquals(VoiceEndpoint.NOTHING_MS / 100, n2)
    }

    @Test fun aLastShortWordAfterABreathIsKept() {
        // "...should I take" [four tenths of breath] "it?" : the last word is one tenth long
        val ep = VoiceEndpoint()
        val levels = List(12) { speech(it) } + List(4) { 0.002f } + listOf(0.07f) + List(18) { 0.002f }
        assertEquals(ended, run(ep, levels).first)
        assertEquals(1700L, ep.lastMs)
        assertEquals(0L to 2200L, ep.keep(3500))
    }

    @Test fun speakingFromTheFirstTenth() {
        val ep = VoiceEndpoint()
        assertEquals(ended, run(ep, List(15) { speech(it) } + List(20) { 0.002f }).first)
        assertEquals(0L, ep.firstMs)
        assertEquals(0L to 2000L, ep.keep(3300))
    }

    @Test fun aQuietVoiceInAQuietRoom() {
        // at arm's length: three times the floor is all there is
        val ep = VoiceEndpoint()
        val (v, _) = run(ep, List(5) { 0.001f } + List(20) { speech(it, 0.025f) } + List(20) { 0.001f })
        assertEquals(ended, v)
        assertTrue(ep.heard)
    }

    @Test fun loudnessAndTheCut() {
        val rate = VoiceWav.RATE
        val tone = ShortArray(rate) { (sin(2 * PI * 440 * it / rate) * 16384).toInt().toShort() } // half scale
        assertEquals(0.3535f, VoiceWav.rms(tone, tone.size), 0.002f)
        assertEquals(0f, VoiceWav.rms(ShortArray(100), 100), 0f)
        assertEquals(0f, VoiceWav.rms(tone, 0), 0f)
        val f = File.createTempFile("voicecut", ".wav")
        try {
            // three seconds: each second is its own value, so the cut can be told
            val bytes = ByteArray(3 * rate * 2)
            for (i in 0 until 3 * rate) { val v = (i / rate + 1) * 1000; bytes[2 * i] = (v and 0xff).toByte(); bytes[2 * i + 1] = (v shr 8).toByte() }
            f.writeBytes(VoiceWav.header(rate, 1, 16, 0) + bytes)
            VoiceWav.finish(f)
            VoiceWav.trim(f, 1000, 2000)
            assertEquals(1000L, VoiceWav.durationMs(f))
            val out = f.readBytes()
            assertEquals(44 + rate * 2, out.size)
            // the header says one second, and every sample is the middle second's
            assertEquals(rate * 2, (out[40].toInt() and 0xff) or ((out[41].toInt() and 0xff) shl 8) or ((out[42].toInt() and 0xff) shl 16))
            assertEquals(2000, (out[44].toInt() and 0xff) or ((out[45].toInt() and 0xff) shl 8))
            assertEquals(2000, (out[out.size - 2].toInt() and 0xff) or ((out[out.size - 1].toInt() and 0xff) shl 8))
            // a cut that is the whole file, or nothing, leaves it alone
            VoiceWav.trim(f, 0, 5000); assertEquals(1000L, VoiceWav.durationMs(f))
            VoiceWav.trim(f, 900, 900); assertEquals(1000L, VoiceWav.durationMs(f))
        } finally {
            f.delete()
        }
    }
}
