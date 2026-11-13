package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertNotEquals
import org.junit.Test

class MapViewCellsTest {
    private val street = RoadTileGeom.FINE_PXZ * 2
    private val town = LandTileGeom.TILE_PXZ * 2

    @Test fun aPanInsideTheSameCellsChangesNothing() {
        // a street view over Woolwich, panned a few hundred metres: the same key, so no list is
        // built again and nothing is asked of the tile caches
        val a = MapViewCells.key(0.051, 0.079, 51.481, 51.497, street)
        val b = MapViewCells.key(0.053, 0.081, 51.482, 51.498, street)
        assertEquals(a, b)
        // across a tenth of a degree: the streets' range moves, the one-degree range does not
        val c = MapViewCells.key(0.081, 0.109, 51.482, 51.498, street)
        assertNotEquals(a, c)
        assertEquals(a.land, c.land)
        assertNotEquals(a.fine, c.fine)
        // across a whole degree: both
        val d = MapViewCells.key(0.98, 1.02, 51.482, 51.498, street)
        assertNotEquals(a.land, d.land)
    }

    @Test fun theZoomThresholdsAreTheOnlyZoomThatMatters() {
        // zooming within a band over the same cells: the same key
        assertEquals(MapViewCells.key(0.0, 0.5, 51.0, 51.4, town), MapViewCells.key(0.0, 0.5, 51.0, 51.4, town * 1.7f))
        // past the streets' threshold the fine range joins
        val t = MapViewCells.key(0.0, 0.5, 51.0, 51.4, town)
        val s = MapViewCells.key(0.0, 0.5, 51.0, 51.4, street)
        assertEquals(1, t.band); assertEquals(3, s.band)
        assertEquals(0L, t.fine)
        assertNotEquals(0L, s.fine)
        // under the first threshold nothing is wanted, wherever the view is: one key for all of it
        val w = LandTileGeom.TILE_PXZ / 2
        assertEquals(MapViewCells.key(-170.0, 170.0, -80.0, 80.0, w), MapViewCells.key(10.0, 20.0, 40.0, 50.0, w))
        assertEquals(MapViewCells.Key(0, 0, 0), MapViewCells.key(0.0, 1.0, 0.0, 1.0, Float.NaN))
    }

    @Test fun theRangeIsTheOneTheTileListsUse() {
        // the key must change exactly when LandTileGeom.cellsFor's answer does: same cells, same range
        fun cells(lon0: Double, lon1: Double, lat0: Double, lat1: Double) = LandTileGeom.cellsFor(lon0, lon1, lat0, lat1)
        val views = listOf(
            doubleArrayOf(-0.4, 0.6, 51.2, 51.9), doubleArrayOf(-0.39, 0.61, 51.21, 51.91), doubleArrayOf(-1.01, 0.6, 51.2, 51.9),
            doubleArrayOf(179.2, 179.9, -45.3, -44.8), doubleArrayOf(-180.0, -179.5, 89.2, 89.9), doubleArrayOf(0.0, 1.0, 0.0, 1.0))
        for (a in views) for (b in views) {
            val sameCells = cells(a[0], a[1], a[2], a[3]) == cells(b[0], b[1], b[2], b[3])
            val sameRange = MapViewCells.range(a[0], a[1], a[2], a[3], 1.0) == MapViewCells.range(b[0], b[1], b[2], b[3], 1.0)
            assertEquals("${a.toList()} against ${b.toList()}", sameCells, sameRange)
        }
        // out of the world: clamped, as the lists are, not wrapped
        assertEquals(MapViewCells.range(-180.0, 179.9, -90.0, 89.9, 1.0), MapViewCells.range(-500.0, 500.0, -200.0, 200.0, 1.0))
        // the four numbers survive the packing at the fine grid's far corner
        val r = MapViewCells.range(179.95, 179.99, 89.95, 89.99, 0.1)
        assertEquals(3599L, r and 0xFFF); assertEquals(3599L, (r shr 12) and 0xFFF)
        assertEquals(1799L, (r shr 24) and 0xFFF); assertEquals(1799L, (r shr 36) and 0xFFF)
    }
}
