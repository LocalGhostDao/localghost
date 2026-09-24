package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The phone's side of the land tile format, driven by the BOX'S OWN output: tile_fixture.lgt is
 * written by internal/landtiles' encoder (its fixture test fails if the encoder changes), so a
 * drift in either codec breaks one of the two tests rather than the map.
 */
class LandTileGeomTest {
    private fun fixture(): ByteArray = javaClass.getResourceAsStream("/tile_fixture.lgt")!!.readBytes()

    @Test fun decodesTheBoxTile() {
        val d = LandTileGeom.decode(fixture())!!
        assertEquals(200, d.x); assertEquals(129, d.y); assertEquals(2, d.rings.size)
        assertEquals(listOf(6554, 6554, 6554, 19660, 19661, 19660, 19661, 6554), d.rings[0].toList())
    }

    @Test fun coastSkipsTheCellBorder() {
        val d = LandTileGeom.decode(fixture())!!
        assertEquals(listOf(listOf(0, 1, 2, 3, 0)), LandTileGeom.coastRuns(d.rings[0]).map { it.toList() })
        assertEquals(listOf(listOf(1, 2, 3)), LandTileGeom.coastRuns(d.rings[1]).map { it.toList() })
    }

    @Test fun simplifyKeepsBorderVertices() {
        val zig = intArrayOf(0, 100, 10, 101, 20, 100, 30, 101, 40, 100, 1000, 1000, 1000, 0, 0, 0)
        val pts = LandTileGeom.simplify(zig, 160).toList().chunked(2)
        assertTrue(pts.contains(listOf(0, 0)) && pts.contains(listOf(1000, 0)))
        assertTrue(!pts.contains(listOf(10, 101)))
    }

    @Test fun cellsAndLevels() {
        assertEquals(listOf(129 * 360 + 200), LandTileGeom.cellsFor(20.1, 20.3, 39.1, 39.3))
        assertEquals(4, LandTileGeom.cellsFor(19.5, 20.5, 38.5, 39.5).size)
        assertTrue(LandTileGeom.cellsFor(-10.0, 40.0, 30.0, 60.0).isEmpty())
        assertEquals(0, LandTileGeom.levelFor(150f)); assertEquals(2, LandTileGeom.levelFor(20000f))
        assertNull(LandTileGeom.decode(ByteArray(12)))
        assertNull(LandTileGeom.index(ByteArray(10)))
    }
}
