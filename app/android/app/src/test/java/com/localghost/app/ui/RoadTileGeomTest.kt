package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The phone's side of the road tile format, driven by the BOX'S OWN output: road_fixture.lgr and
 * road_index_fixture.bin are written by internal/roadtiles' encoder (its fixture test fails if the
 * encoder changes), so a drift in either codec breaks one of the two tests rather than the map.
 */
class RoadTileGeomTest {
    private fun res(name: String): ByteArray = javaClass.getResourceAsStream(name)!!.readBytes()

    @Test fun decodesTheBoxTile() {
        val d = RoadTileGeom.decode(res("/road_fixture.lgr"))!!
        assertEquals(0, d.level); assertEquals(2061, d.x); assertEquals(1344, d.y)
        assertEquals(6, d.pieces.size)
        val p0 = d.pieces[0]
        assertEquals(3, p0.cls); assertEquals("Șoseaua Colentina", p0.name)
        assertTrue(p0.flags and RoadTileGeom.FLAG_ONEWAY != 0)
        assertTrue(p0.flags and RoadTileGeom.FLAG_NAMED != 0)
        assertEquals(listOf(0, 32768, 20000, 33000, RoadTileGeom.Q, 34000), p0.pts.toList())
        val p1 = d.pieces[1]
        assertEquals(9, p1.cls); assertEquals("", p1.name); assertEquals(0, p1.flags)
        assertEquals(listOf(100, 100, 500, 900), p1.pts.toList())
        val p2 = d.pieces[2]
        assertEquals(6, p2.cls); assertEquals("Strada Mică", p2.name)
        assertTrue(p2.flags and RoadTileGeom.FLAG_BRIDGE != 0)
        assertEquals(6, p2.pts.size)
        // the water in the same tile: a pond as a ring, its island a hole, a river as a line
        val lake = d.pieces[3]
        assertEquals(RoadTileGeom.CLASS_WATER_AREA, lake.cls); assertEquals("Lacul Tei", lake.name); assertEquals(8, lake.pts.size)
        assertTrue(RoadTileGeom.isWater(lake.cls)); assertFalse(RoadTileGeom.isWater(p2.cls))
        val island = d.pieces[4]
        assertTrue(island.flags and RoadTileGeom.FLAG_HOLE != 0)
        assertEquals(RoadTileGeom.CLASS_RIVER, d.pieces[5].cls); assertEquals("Colentina", d.pieces[5].name)
        // what shows when: rivers with the major tiles, streams with the streets
        assertTrue(RoadTileGeom.showsWater(RoadTileGeom.CLASS_RIVER, RoadTileGeom.MAJOR_PXZ))
        assertFalse(RoadTileGeom.showsWater(RoadTileGeom.CLASS_STREAM, RoadTileGeom.FINE_PXZ))
        assertFalse(RoadTileGeom.showsWater(RoadTileGeom.CLASS_STREAM, RoadTileGeom.FINE_PXZ * 3))
        assertTrue(RoadTileGeom.showsWater(RoadTileGeom.CLASS_STREAM, RoadTileGeom.minPxzFor(6)))
        assertFalse(RoadTileGeom.showsWater(6, RoadTileGeom.FINE_PXZ * 3))
    }

    @Test fun refusesGarbageAndShortTiles() {
        assertNull(RoadTileGeom.decode(ByteArray(12)))
        assertNull(RoadTileGeom.decode("not a road tile".toByteArray()))
        val whole = res("/road_fixture.lgr")
        // a tile cut anywhere inside a piece is refused rather than half-decoded
        assertNull(RoadTileGeom.decode(whole.copyOf(whole.size - 3)))
        assertNull(RoadTileGeom.decode(whole.copyOf(20)))
    }

    @Test fun readsTheBoxIndex() {
        val ix = RoadTileGeom.index(res("/road_index_fixture.bin"))!!
        assertTrue(ix.hasFine(2061, 1344)); assertFalse(ix.hasFine(2062, 1344)); assertFalse(ix.hasFine(2061, 1345))
        assertTrue(ix.hasMajor(206, 134)); assertFalse(ix.hasMajor(207, 134)); assertFalse(ix.hasMajor(206, 135))
        assertNull(RoadTileGeom.index(null))
        assertNull(RoadTileGeom.index(ByteArray(10)))
        assertNull(RoadTileGeom.index(ByteArray(4 + 360 * 180 + 3600 * 1800 / 8))) // right size, wrong magic
    }

    @Test fun keysTellTheGridsApart() {
        for ((l, x, y) in listOf(Triple(0, 2061, 1344), Triple(1, 206, 134), Triple(0, 0, 0), Triple(0, 3599, 1799), Triple(1, 359, 179))) {
            val k = RoadTileGeom.key(l, x, y)
            assertEquals(l, RoadTileGeom.levelOf(k)); assertEquals(x, RoadTileGeom.xOf(k)); assertEquals(y, RoadTileGeom.yOf(k))
        }
        assertTrue(RoadTileGeom.key(0, 206, 134) != RoadTileGeom.key(1, 206, 134))
    }

    @Test fun cellsForAWindowUseTheIndex() {
        val ix = RoadTileGeom.index(res("/road_index_fixture.bin"))!!
        // a street-level window inside the fine cell at lon 26.1, lat 44.4
        assertEquals(listOf(RoadTileGeom.key(0, 2061, 1344)), RoadTileGeom.cellsFor(ix, 0, 26.11, 26.15, 44.41, 44.45))
        // a window over several fine cells still finds only the one with a tile
        assertEquals(listOf(RoadTileGeom.key(0, 2061, 1344)), RoadTileGeom.cellsFor(ix, 0, 26.05, 26.35, 44.35, 44.55))
        // the major grid around Bucharest
        assertEquals(listOf(RoadTileGeom.key(1, 206, 134)), RoadTileGeom.cellsFor(ix, 1, 25.5, 27.5, 43.5, 45.5))
        // the sea: nothing
        assertTrue(RoadTileGeom.cellsFor(ix, 0, -30.0, -29.8, 40.0, 40.2).isEmpty())
        // too wide a window for the level: empty, not thousands of requests
        assertTrue(RoadTileGeom.cellsFor(ix, 0, 20.0, 30.0, 40.0, 50.0).isEmpty())
        assertTrue(RoadTileGeom.cellsFor(ix, 1, -180.0, 180.0, -90.0, 90.0).isEmpty())
    }

    @Test fun simplifyKeepsTheEnds() {
        val r = intArrayOf(0, 0, 5, 5, 10, 10, 1000, 1000, 1005, 1005, 2000, 2000)
        assertEquals(listOf(0, 0, 1000, 1000, 2000, 2000), RoadTileGeom.simplify(r, 120).toList())
        assertEquals(r.toList(), RoadTileGeom.simplify(r, 0).toList())
        val two = intArrayOf(0, 0, 65535, 65535)
        assertEquals(two.toList(), RoadTileGeom.simplify(two, 1000).toList())
    }

    // the classes a zoom draws, as the map's screen sees them: px is pxz, cls 1..9
    private fun drawn(px: Float) = (1..9).filter { RoadTileGeom.emphasisFor(it, px) != null }

    @Test fun classesByZoom() {
        val major = RoadTileGeom.MAJOR_PXZ; val fine = RoadTileGeom.FINE_PXZ
        // the country: motorways and trunk roads only
        assertEquals(listOf(1, 2), drawn(major))
        // a region: the primaries come in
        assertEquals(listOf(1, 2, 3), drawn(major * 3f))
        // a city 25 km across a phone 1,080 pixels wide in London (Vlad's first screenshot), and
        // in to 17 km: no streets, no secondaries yet
        assertEquals(listOf(1, 2, 3), drawn(fine * 0.7f))
        assertEquals(listOf(1, 2, 3), drawn(fine - 1f))
        // a district 12 km across (the second): the secondaries, and still no streets
        assertEquals(listOf(1, 2, 3, 4), drawn(fine * 1.5f))
        assertEquals(listOf(1, 2, 3, 4, 5), drawn(fine * 2f))
        // the streets from about 4 km across, tracks and paths closer, service roads last
        assertEquals(listOf(1, 2, 3, 4, 5, 6), drawn(fine * 4f))
        assertEquals(listOf(1, 2, 3, 4, 5, 6, 8, 9), drawn(fine * 8f))
        // zoomed right in: everything
        assertEquals((1..9).toList(), drawn(fine * 10f))
        assertEquals((1..9).toList(), drawn(MapPick.MAX_ZOOM))
        // water is no road, and a class the box does not cut is never drawn
        assertNull(RoadTileGeom.emphasisFor(RoadTileGeom.CLASS_RIVER, fine * 100f))
        assertNull(RoadTileGeom.emphasisFor(0, fine * 100f))
        assertNull(RoadTileGeom.emphasisFor(3, Float.NaN))
    }

    @Test fun aClassFadesInOverTwoDoublings() {
        val m = RoadTileGeom.minPxzFor(6)
        assertNull(RoadTileGeom.emphasisFor(6, m * 0.99f))
        assertEquals(0f, RoadTileGeom.emphasisFor(6, m)!!, 1e-6f)
        assertEquals(0.5f, RoadTileGeom.emphasisFor(6, m * 2f)!!, 1e-4f)
        assertEquals(1f, RoadTileGeom.emphasisFor(6, m * 4f)!!, 1e-4f)
        assertEquals(1f, RoadTileGeom.emphasisFor(6, m * 400f)!!, 0f)
        // rising all the way, never past 1
        var last = -1f
        var px = m
        while (px < m * 8f) {
            val e = RoadTileGeom.emphasisFor(6, px)!!
            assertTrue(e >= last && e <= 1f); last = e; px *= 1.1f
        }
        // the bigger the road the earlier it comes in
        for (c in 1..5) assertTrue(RoadTileGeom.minPxzFor(c) <= RoadTileGeom.minPxzFor(c + 1))
    }

    @Test fun namesWaitForTheirRoadInFull() {
        val fine = RoadTileGeom.FINE_PXZ
        // nothing named before NAME_PXZ, whatever the class
        assertFalse(RoadTileGeom.namesShow(1, RoadTileGeom.NAME_PXZ - 1f))
        assertTrue(RoadTileGeom.namesShow(1, RoadTileGeom.NAME_PXZ))
        assertTrue(RoadTileGeom.namesShow(3, RoadTileGeom.NAME_PXZ))
        // a district 6 km across: the main roads named, the streets not
        assertFalse(RoadTileGeom.namesShow(6, RoadTileGeom.NAME_PXZ))
        assertFalse(RoadTileGeom.namesShow(5, RoadTileGeom.NAME_PXZ))
        // a street's name once the street is drawn in full
        assertFalse(RoadTileGeom.namesShow(6, fine * 15f))
        assertTrue(RoadTileGeom.namesShow(6, fine * 16f))
        // every class: named from the later of its road in full and NAME_PXZ, not a step before
        for (c in 1..9) {
            val from = maxOf(RoadTileGeom.minPxzFor(c) * 4f, RoadTileGeom.NAME_PXZ)
            assertFalse(RoadTileGeom.namesShow(c, from * 0.99f))
            assertTrue(RoadTileGeom.namesShow(c, from))
        }
    }
}
