package com.localghost.app.qr

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotNull
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * THE WHOLE SCAN ON RENDERED FRAMES: a real code (the round-trip test's version 11 and version 1
 * grids) drawn into a 1280x720 frame the way a camera would see it on a monitor, at a size, an
 * angle, a blur and a noise, then put through exactly what the analyser runs per frame
 * ([QrSampler.sampleCandidates], then [QrMatrixDecode.decode] as the judge). Until 13 November 2026
 * nothing tested the two halves together on a picture, and the scanner read big codes only in a
 * narrow band of distances. Each case here is one that failed on most or all of fourteen frames
 * before, with the reason it failed:
 *
 *  - the row scan tried only every third dark run as a finder's start, so a finder with a dark
 *    page to its left was never found (two finders of three, the commonest near-miss);
 *  - a dense code's own data throws up dozens of finder-shaped coincidences, and three of them
 *    often made a neater right angle than the real corners, so the real triple was never sampled;
 *  - a turned code's module size was measured on a slant, a version or four out;
 *  - a speck of data taken for the alignment pattern skewed the bottom-right corner.
 *
 * Counted over fourteen frames each on the bench this file grew out of (before, after): version 11
 * at 8 px a module 3 and 14, at 10 px 0 and 13, at 3 px turned 20 degrees 1 and 12, at 6 px turned
 * 45 degrees 0 and 13, at 3 px blurred 2 and 14; an empty room cost 55 ms a frame and costs 15.
 */
class QrScanEndToEndTest {
    private val fix = QrDecodeRoundTripTest()
    private val w = 1280
    private val h = 720
    private val v11Text = "localghost-v11-live-decode-test"

    /** A frame: the code at the centre, [modulePx] a module, turned [deg], on a dim textured page
     *  that reads dark beside the code's light margin, as a terminal's background does. */
    private fun frame(grid: Array<BooleanArray>?, modulePx: Double, deg: Double, blur: Int, noise: Int, seed: Int): IntArray {
        val lum = IntArray(w * h)
        val n = grid?.size ?: 0
        val rad = Math.toRadians(deg); val co = Math.cos(rad); val si = Math.sin(rad)
        val half = n / 2.0
        for (y in 0 until h) for (x in 0 until w) {
            val bg = 110 + (30 * Math.sin(x / 97.0) * Math.cos(y / 61.0)).toInt()
            var acc = 0
            for (sy in 0..2) for (sx in 0..2) { // nine samples a pixel: edges are grey, as a lens makes them
                val px = x + (sx + 0.5) / 3.0 - w / 2.0; val py = y + (sy + 0.5) / 3.0 - h / 2.0
                val c = Math.floor((px * co + py * si) / modulePx + half).toInt()
                val r = Math.floor((-px * si + py * co) / modulePx + half).toInt()
                acc += when {
                    grid == null -> bg
                    r in 0 until n && c in 0 until n -> if (grid[r][c]) 45 else 215
                    r in -4 until n + 4 && c in -4 until n + 4 -> 215 // the quiet zone
                    else -> bg
                }
            }
            lum[y * w + x] = acc / 9
        }
        var out = lum
        repeat(if (blur > 0) 2 else 0) { out = box(out, blur) }
        var s = seed
        if (noise > 0) for (i in out.indices) {
            s = s * 1103515245 + 12345
            out[i] = (out[i] + ((s ushr 16) % (2 * noise + 1)) - noise).coerceIn(0, 255)
        }
        return out
    }

    private fun box(src: IntArray, r: Int): IntArray {
        val tmp = IntArray(src.size); val dst = IntArray(src.size); val win = 2 * r + 1
        for (y in 0 until h) {
            var sum = 0
            for (x in -r..r) sum += src[y * w + x.coerceIn(0, w - 1)]
            for (x in 0 until w) {
                tmp[y * w + x] = sum / win
                sum += src[y * w + (x + r + 1).coerceIn(0, w - 1)] - src[y * w + (x - r).coerceIn(0, w - 1)]
            }
        }
        for (x in 0 until w) {
            var sum = 0
            for (y in -r..r) sum += tmp[y.coerceIn(0, h - 1) * w + x]
            for (y in 0 until h) {
                dst[y * w + x] = sum / win
                sum += tmp[(y + r + 1).coerceIn(0, h - 1) * w + x] - tmp[(y - r).coerceIn(0, h - 1) * w + x]
            }
        }
        return dst
    }

    /** The camera path for one frame: the candidates, then the decoder as the judge. */
    private fun scan(lum: IntArray): String? {
        val (cands, _) = QrSampler.sampleCandidates(lum, w, h)
        for (c in cands) {
            val t = try { QrMatrixDecode.decode(c.grid, c.conf) } catch (e: Exception) { null }
            if (t != null) return t
        }
        return null
    }

    /** A new scan of the same scene, frame after frame as the camera gives them (the noise
     *  differs each time): the frame it first read on, or -1 when [frames] were not enough. */
    private fun readsWithin(frames: Int, grid: Array<BooleanArray>, want: String, modulePx: Double, deg: Double, blur: Int = 0, noise: Int = 3): Int {
        QrSampler.reset()
        for (i in 0 until frames) {
            val t = scan(frame(grid, modulePx, deg, blur, noise, 1000 + i * 7919))
            if (t != null) { assertEquals("a code read as something it is not", want, t); return i }
        }
        return -1
    }

    @Test fun aDenseCodeReadsWhereItUsedNotTo() {
        val v11 = fix.grid(fix.enrolV11M)
        // large in the frame: 8 and 10 pixels a module (0 of 14 at ten, before)
        assertTrue("v11 at 8 px a module", readsWithin(3, v11, v11Text, 8.0, 0.0) >= 0)
        assertTrue("v11 at 10 px a module", readsWithin(3, v11, v11Text, 10.0, 0.0) >= 0)
        // small and turned
        assertTrue("v11 at 3 px, turned 20 degrees", readsWithin(3, v11, v11Text, 3.0, 20.0) >= 0)
        // on its corner: the module size used to be measured on the slant
        assertTrue("v11 at 6 px, turned 45 degrees", readsWithin(3, v11, v11Text, 6.0, 45.0) >= 0)
        // soft focus at the edge of what the lens resolves
        assertTrue("v11 at 3 px, blurred", readsWithin(3, v11, v11Text, 3.0, 6.0, blur = 1, noise = 5) >= 0)
    }

    @Test fun aSmallCodeStillReads() {
        val v1 = fix.grid(fix.helloV1M)
        assertTrue("v1 at 4 px", readsWithin(3, v1, "hello", 4.0, 12.0, noise = 4) >= 0)
        assertTrue("v1 at 16 px", readsWithin(3, v1, "hello", 16.0, 12.0, noise = 4) >= 0)
    }

    @Test fun allThreeFindersAreFoundOnADarkPage() {
        // the regression itself: on a dark page every line through the top-left finder reads
        // [page][quiet zone][ring]..., and the ring was never tried as a pattern's start
        QrSampler.reset()
        val (cands, diag) = QrSampler.sampleCandidates(frame(fix.grid(fix.enrolV11M), 8.0, 0.0, 0, 3, 1000), w, h)
        assertTrue("no grid: ${diag.note}", cands.isNotEmpty())
        // a candidate is built on the three real corners (424,144 856,144 424,576), not on data
        fun real(f: List<QrSampler.FinderPoint>): Boolean {
            fun near(x: Int, y: Int) = f.any { Math.abs(it.x - x) <= 3 && Math.abs(it.y - y) <= 3 }
            return near(424, 144) && near(856, 144) && near(424, 576)
        }
        assertTrue("no candidate stands on the real finders: ${cands.map { it.finders }}", cands.any { real(it.finders) })
    }

    @Test fun anEmptyRoomReadsAsNothingAndRemembersNothing() {
        QrSampler.reset()
        for (i in 0 until 3) assertNull("a code read out of noise", scan(frame(null, 0.0, 0.0, 0, 14, 77 + i)))
        // and a scan that follows starts clean: what the last one learnt is gone
        QrSampler.reset()
        assertEquals(0, QrSampler.ScanGeom.findersSeen)
        assertEquals(0.0, QrSampler.ScanGeom.moduleLenPx, 0.0)
        assertNull(QrSampler.ScanGeom.corners)
        assertNotNull(QrSampler.sampleCandidates(frame(null, 0.0, 0.0, 0, 4, 5), w, h).second)
    }
}
