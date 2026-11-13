package com.localghost.app.ui

import kotlin.math.floor

/**
 * WHICH TILES A VIEW NEEDS, as a value that changes only when the answer does. Pure, for the tests.
 *
 * The map used to work the wanted tiles out in the screen's composition, keyed on the camera
 * itself: every frame of a pan or a pinch built the lists of coast and road cells again (and,
 * because the camera was read there, recomposed the whole screen around the map to do it). The
 * lists only change when the view's edge crosses a cell's edge. This is that crossing: the cell
 * ranges under the view on the one-degree grid (the coast tiles and the main roads) and the
 * tenth-of-a-degree grid (the streets), and which of the two zoom thresholds the view is past.
 * The camera is watched for a change of this key, off the composition; a pan inside the same
 * cells changes nothing and costs four subtractions.
 */
object MapViewCells {
    /** [land]: the one-degree cell range; [fine]: the tenth-of-a-degree one; [band]: bit 0 past
     *  the coast and main-road threshold, bit 1 past the streets'. */
    data class Key(val land: Long, val fine: Long, val band: Int)

    /** The cell range a lon/lat window covers on a grid of [deg] degrees a cell, clamped to the
     *  world: x0, x1, y0, y1 packed twelve bits each. The same arithmetic as the two cellsFor. */
    fun range(lon0: Double, lon1: Double, latLo: Double, latHi: Double, deg: Double): Long {
        val cols = (360 / deg).toInt(); val rows = (180 / deg).toInt()
        val x0 = floor((lon0 + 180) / deg).toInt().coerceIn(0, cols - 1)
        val x1 = floor((lon1 + 180) / deg).toInt().coerceIn(0, cols - 1)
        val y0 = floor((latLo + 90) / deg).toInt().coerceIn(0, rows - 1)
        val y1 = floor((latHi + 90) / deg).toInt().coerceIn(0, rows - 1)
        return x0.toLong() or (x1.toLong() shl 12) or (y0.toLong() shl 24) or (y1.toLong() shl 36)
    }

    /** The key for a view: its window in degrees and [pxz], screen pixels a map unit. Below the
     *  first threshold nothing is wanted whatever the window, so every such view has one key. */
    fun key(lon0: Double, lon1: Double, latLo: Double, latHi: Double, pxz: Float): Key {
        val band = (if (pxz >= LandTileGeom.TILE_PXZ) 1 else 0) or (if (pxz >= RoadTileGeom.FINE_PXZ) 2 else 0)
        if (band == 0 || !pxz.isFinite()) return Key(0, 0, 0)
        return Key(range(lon0, lon1, latLo, latHi, 1.0), if (band and 2 != 0) range(lon0, lon1, latLo, latHi, 0.1) else 0, band)
    }
}
