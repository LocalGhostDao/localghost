package com.localghost.app.sync

import org.junit.Assert.assertEquals
import org.junit.Test

class StepsMergeTest {
    private fun e(start: Long, end: Long, n: Long, dev: String) = StepsMerge.Entry(start, end, n, dev)

    @Test fun oneDeviceIsItsSum() {
        val es = listOf(e(0, 600, 100, "watch"), e(600, 1200, 50, "watch"), e(1200, 1800, 0, "watch"))
        assertEquals(150.0, StepsMerge.merged(es), 0.001)
    }

    @Test fun twoDevicesTakeTheLargerPerBin() {
        // the same ten minutes counted by both: the watch's 100 wins over the phone's 80; the
        // next ten only the phone counted (the watch was charging)
        val es = listOf(e(0, 600, 100, "watch"), e(0, 600, 80, "phone"), e(600, 1200, 40, "phone"))
        assertEquals(140.0, StepsMerge.merged(es), 0.001)
        assertEquals(mapOf("phone" to 120L, "watch" to 100L), StepsMerge.byDevice(es))
    }

    @Test fun aLongRecordIsSpreadOverItsBins() {
        // an hour of 600 steps from the phone against the watch's per-ten-minute counts: the
        // phone's 100 a bin never beats the watch's 120, so the merge is the watch's 720
        val es = ArrayList<StepsMerge.Entry>()
        es.add(e(0, 3600, 600, "phone"))
        for (b in 0 until 6) es.add(e(b * 600L, (b + 1) * 600L, 120, "watch"))
        assertEquals(720.0, StepsMerge.merged(es), 0.001)
        // and with the watch silent for the last twenty minutes, the phone's share of those bins counts
        val es2 = es.filter { it.device == "phone" || it.start < 2400 }
        assertEquals(480.0 + 200.0, StepsMerge.merged(es2), 0.001)
    }

    @Test fun aZeroLengthRecordCountsWhole() {
        assertEquals(7.0, StepsMerge.merged(listOf(e(100, 100, 7, "watch"))), 0.001)
        assertEquals(0.0, StepsMerge.merged(emptyList()), 0.001)
    }
}
