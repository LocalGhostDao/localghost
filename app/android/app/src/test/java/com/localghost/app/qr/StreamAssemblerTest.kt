package com.localghost.app.qr

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test
import java.util.Base64

/**
 * The app side of the LGQR2 contract, driven by the BOX'S OWN output: lgqr2_fixture.txt is written
 * by internal/pair/qrstream_test.go (UPDATE_FIXTURES=1) and copied here, so a format change on
 * one side fails the other. 1243-byte payload, 113-byte blocks: K=11 data + M=6 parity frames.
 */
class StreamAssemblerTest {

    private val payload: ByteArray
    private val frames: List<String>
    private val k = 11

    init {
        val lines = javaClass.getResourceAsStream("/lgqr2_fixture.txt")!!.bufferedReader().readLines()
        payload = Base64.getDecoder().decode(lines[0].removePrefix("payload "))
        frames = lines.drop(1).map { String(Base64.getDecoder().decode(it.removePrefix("frame ")), Charsets.ISO_8859_1) }
        assertEquals(17, frames.size)
    }

    @Test
    fun anyKFramesRebuildThePayload() {
        val rnd = java.util.Random(7)
        repeat(200) { trial ->
            val a = StreamAssembler()
            var result: ByteArray? = null
            var offered = 0
            for (i in frames.indices.shuffled(rnd)) {
                assertTrue(a.isFrame(frames[i]))
                offered++
                val r = a.offer(frames[i])
                if (r != null) { result = r; break }
                assertEquals("progress after $offered", offered to k, a.progress())
            }
            assertEquals("trial $trial: done after exactly K distinct frames", k, offered)
            assertArrayEquals(payload, result)
        }
    }

    @Test
    fun duplicatesDoNotCount() {
        val a = StreamAssembler()
        repeat(5) { assertNull(a.offer(frames[3])) }
        assertEquals(1 to k, a.progress())
        assertEquals(17, a.totalFrames())
        assertEquals(setOf(3), a.capturedIdx())
        assertTrue(!a.lastOfferWasNew)
    }

    @Test
    fun corruptedBodyIsRefusedByItsOwnCrc() {
        val a = StreamAssembler()
        val bad = frames[2].dropLast(1) + (frames[2].last().code xor 0x55).toChar()
        assertNull(a.offer(bad))
        assertEquals(0 to 0, a.progress())
    }

    @Test
    fun parityFramesCountLikeDataFrames() {
        val a = StreamAssembler()
        var r: ByteArray? = null
        for (i in listOf(16, 15, 14, 13, 12, 11, 0, 1, 2, 3, 4)) r = a.offer(frames[i])
        assertNotNull(r)
        assertArrayEquals(payload, r)
    }

    @Test
    fun kMinusOneIsNotEnough() {
        val a = StreamAssembler()
        for (i in 0 until k - 1) assertNull(a.offer(frames[i]))
        assertEquals(k - 1 to k, a.progress())
    }

    @Test
    fun foreignFrameIsOutvoted() {
        val a = StreamAssembler()
        for (i in 0 until 6) a.offer(frames[i])
        val body = "ab"
        val c = java.util.zip.CRC32().also { it.update(body.toByteArray()) }.value and 0xffff
        assertNull(a.offer("LGQR2 0 2 2 4 deadbeef %04x %s".format(c, body)))
        assertEquals(6 to k, a.progress())
        var r: ByteArray? = null
        for (i in 6 until 11) r = a.offer(frames[i])
        assertArrayEquals(payload, r)
    }

    @Test
    fun malformedHeadersAreIgnored() {
        val a = StreamAssembler()
        assertNull(a.offer("LGQR2 x 1 1 1 00000000 0000 z"))
        assertNull(a.offer("LGQR2 5 2 2 4 deadbeef 0000 ab")) // idx outside K+M
        assertNull(a.offer("LGQR1 1 2 abcdef12 chunk"))         // the old scheme is not ours
        assertEquals(0 to 0, a.progress())
    }
}
