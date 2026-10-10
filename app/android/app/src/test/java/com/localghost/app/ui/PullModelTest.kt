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

    @Test fun eachIntegrationDrawsInItsOwnManner() {
        assertEquals(PullModel.Style.PAGES, PullModel.styleOf("wikipedia"))
        assertEquals(PullModel.Style.BURST, PullModel.styleOf("news"))
        assertEquals(PullModel.Style.TICKER, PullModel.styleOf("crypto"))
        assertEquals(PullModel.Style.DROPS, PullModel.styleOf("weather"))
        assertEquals(PullModel.Style.TILES, PullModel.styleOf("maps"))
        assertEquals(PullModel.Style.WAVE, PullModel.styleOf("speech"))
        assertEquals(PullModel.Style.PLAIN, PullModel.styleOf("something"))
        val from = PullModel.Node(14f, 40f)
        val to = PullModel.Node(86f, 40f)
        // a drop sags below the thread on its way, and still arrives
        val plain = PullModel.packet(from, to, 0.6f)
        val drop = PullModel.packet(from, to, 0.6f, PullModel.Style.DROPS)
        assertTrue(drop.y > plain.y + 1f)
        assertEquals(to.y, PullModel.packet(from, to, 1f, PullModel.Style.DROPS).y, 0.01f)
        // a tick jogs, the same way every time for the same packet, within bounds
        val t1 = PullModel.packet(from, to, 0.5f, PullModel.Style.TICKER, 1, 0)
        val t2 = PullModel.packet(from, to, 0.5f, PullModel.Style.TICKER, 1, 0)
        assertEquals(t1.y, t2.y, 0.0001f)
        assertTrue(Math.abs(t1.y - plain.y) <= 72f * 0.04f + 0.01f)
        for (e in listOf(0f, 0.3f, 0.7f, 1f)) assertTrue(Math.abs(PullModel.tick(2, 3, e)) <= 1f)
        // bursts: the news sends three close together, the rest one
        assertEquals(3, PullModel.burst(PullModel.Style.BURST).size)
        assertEquals(listOf(0f), PullModel.burst(PullModel.Style.PAGES))
        // the page never vanishes while turning; the tiles go round nine cells; the wave is small
        for (c in listOf(0f, 0.3f, 1.57f, 4f)) assertTrue(PullModel.pageFlip(c) in 0.15f..1f)
        assertEquals(0, PullModel.tileSlot(0f)); assertEquals(8, PullModel.tileSlot(8.9f)); assertEquals(0, PullModel.tileSlot(9.2f))
        for (u in listOf(0f, 0.5f, 1f)) assertTrue(Math.abs(PullModel.wave(u, 1.3f)) <= 0.035f)
        assertEquals(0f, PullModel.wave(0f, 2f), 0.0001f)
    }
}
