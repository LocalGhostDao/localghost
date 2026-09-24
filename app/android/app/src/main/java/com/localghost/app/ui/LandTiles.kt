package com.localghost.app.ui

import android.content.Context
import androidx.compose.ui.graphics.Path
import java.util.concurrent.ConcurrentHashMap
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withPermit
import kotlinx.coroutines.withContext

/** A tile ready to draw: per level a fill Path and a coastline Path, in map units relative to
 *  (ox, oy) , the cell's top-left in map units, kept in Double: at street zoom a Float origin is
 *  several pixels off, and a coast that sits beside the photos taken on it is the one thing a
 *  detailed map must not do. */
internal class LandTile(val key: Int, val ox: Double, val oy: Double, val fill: List<Path>, val coast: List<Path>, val points: Int)

internal object LandTiles {
    /** Build the drawable tile from its bytes. Off the main thread. */
    fun build(b: ByteArray): LandTile? {
        val d = LandTileGeom.decode(b) ?: return null
        val lon0 = d.x - 180.0; val lat0 = d.y - 90.0
        val ox = mercXD(lon0); val oy = mercYD(lat0 + 1.0)
        // projection per quantised coordinate: x is linear in lon; y is not, so a table per tile
        // would be 65,536 entries , cheaper to compute per vertex, once, here.
        fun px(q: Int) = (mercXD(lon0 + q / 65535.0) - ox).toFloat()
        fun py(q: Int) = (mercYD(lat0 + q / 65535.0) - oy).toFloat()
        val fills = ArrayList<Path>(3); val coasts = ArrayList<Path>(3)
        var points = 0
        for (tol in LandTileGeom.TOL_Q) {
            val fill = Path(); val coast = Path()
            for (raw in d.rings) {
                val r = LandTileGeom.simplify(raw, tol)
                if (r.size < 6) continue
                if (tol == 0) points += r.size / 2
                fill.moveTo(px(r[0]), py(r[1]))
                var i = 2
                while (i < r.size) { fill.lineTo(px(r[i]), py(r[i + 1])); i += 2 }
                fill.close()
                for (run in LandTileGeom.coastRuns(r)) {
                    coast.moveTo(px(r[2 * run[0]]), py(r[2 * run[0] + 1]))
                    for (k in 1 until run.size) coast.lineTo(px(r[2 * run[k]]), py(r[2 * run[k] + 1]))
                }
            }
            fills.add(fill); coasts.add(coast)
        }
        return LandTile(d.y * LandTileGeom.COLS + d.x, ox, oy, fills, coasts, points)
    }
}

/** The tiles in memory: a small LRU of built tiles, the set being fetched, and a callback when one
 *  lands so the map redraws. Tiles on disk are the box client's business. */
internal class LandTileCache(private val max: Int = 40) {
    private val built = object : LinkedHashMap<Int, LandTile>(max, 0.75f, true) {
        override fun removeEldestEntry(eldest: MutableMap.MutableEntry<Int, LandTile>?) = size > max
    }
    private val inFlight = ConcurrentHashMap.newKeySet<Int>()
    private val failed = ConcurrentHashMap.newKeySet<Int>()
    private val gate = Semaphore(4) // four tiles at a time: the box answers fast, the phone builds

    @Synchronized fun get(key: Int): LandTile? = built[key]
    @Synchronized private fun put(t: LandTile) { built[t.key] = t }

    /** Fetch and build what is missing, in scope (the screen's, so panning does not cancel it). */
    fun ensure(scope: CoroutineScope, ctx: Context, keys: List<Int>, landed: () -> Unit) {
        for (key in keys) {
            if (get(key) != null || failed.contains(key) || !inFlight.add(key)) continue
            scope.launch(Dispatchers.IO) {
                try {
                    gate.withPermit {
                        val bytes = com.localghost.app.net.BoxClient.landTile(ctx, key % LandTileGeom.COLS, key / LandTileGeom.COLS)
                        val t = bytes?.let { LandTiles.build(it) }
                        if (t != null) { put(t); withContext(Dispatchers.Main) { landed() } } else failed.add(key)
                    }
                } finally {
                    inFlight.remove(key)
                }
            }
        }
    }
}
