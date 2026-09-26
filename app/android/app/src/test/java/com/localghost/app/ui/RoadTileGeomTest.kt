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
        assertEquals(3, d.pieces.size)
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

    @Test fun classesByZoom() {
        assertEquals(4, RoadTileGeom.maxClassFor(RoadTileGeom.MAJOR_PXZ.toFloat()))
        assertEquals(4, RoadTileGeom.maxClassFor(RoadTileGeom.FINE_PXZ - 1f))
        assertEquals(6, RoadTileGeom.maxClassFor(RoadTileGeom.FINE_PXZ * 2f))
        assertEquals(7, RoadTileGeom.maxClassFor(RoadTileGeom.FINE_PXZ * 3f))
        assertEquals(9, RoadTileGeom.maxClassFor(RoadTileGeom.FINE_PXZ * 10f))
    }
}
