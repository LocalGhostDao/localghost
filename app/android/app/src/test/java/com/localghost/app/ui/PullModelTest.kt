package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

class PullModelTest {
    @Test fun theSourcesSpreadDownTheLeftAndTheBoxSitsRight() {
        val s = PullModel.sources(3, 100f, 80f)
        assertEquals(3, s.size)
        assertEquals(14f, s[0].x, 0.01f)
        assertEquals(20f, s[0].y, 0.01f)
        assertEquals(40f, s[1].y, 0.01f)
        assertEquals(60f, s[2].y, 0.01f)
        assertTrue(PullModel.sources(0, 100f, 80f).isEmpty())
        val b = PullModel.box(100f, 80f)
        assertEquals(86f, b.x, 0.01f)
        assertEquals(40f, b.y, 0.01f)
    }

    @Test fun aPacketLeavesItsSourceAndArrivesAtTheBox() {
        val from = PullModel.Node(14f, 20f)
        val to = PullModel.Node(86f, 40f)
        val a = PullModel.packet(from, to, 0f)
        val z = PullModel.packet(from, to, 1f)
        assertEquals(from.x, a.x, 0.01f); assertEquals(from.y, a.y, 0.01f)
        assertEquals(to.x, z.x, 0.01f); assertEquals(to.y, z.y, 0.01f)
        // half way across, and bowed off the straight line
        val m = PullModel.packet(from, to, 0.5f)
        assertEquals(50f, m.x, 0.01f)
        assertTrue(m.y < 30f)
    }

    @Test fun thePhasesAreSpreadAndSlowWhenSteady() {
        val p0 = PullModel.phase(0, 4, 0f, false)
        val p1 = PullModel.phase(1, 4, 0f, false)
        assertEquals(0f, p0, 0.001f)
        assertEquals(0.25f, p1, 0.001f)
        // one second on: a quarter of the way when pulling, a sixth when steady
        assertEquals(1f / 1.8f, PullModel.phase(0, 1, 1f, false), 0.001f)
        assertEquals(1f / 6f, PullModel.phase(0, 1, 1f, true), 0.001f)
        // always inside [0, 1)
        for (c in listOf(-3f, 0f, 7.3f, 1000f)) {
            val p = PullModel.phase(2, 5, c, true)
            assertTrue(p >= 0f && p < 1f)
        }
    }
}
