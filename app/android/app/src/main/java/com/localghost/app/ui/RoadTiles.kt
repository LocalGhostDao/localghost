package com.localghost.app.ui

import android.content.Context
import androidx.compose.ui.graphics.Path
import java.util.concurrent.ConcurrentHashMap
import kotlinx.coroutines.CoroutineScope
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.sync.Semaphore
import kotlinx.coroutines.sync.withPermit

/** A named road for the label pass: its name, the midpoint segment in map units relative to the
 *  tile origin, and its class (so a motorway's name is drawn before a lane's). */
internal class RoadName(val name: String, val cls: Int, val x0: Float, val y0: Float, val x1: Float, val y1: Float, val lengthQ: Int)

/** A road tile ready to draw: its decoded pieces kept, and one Path per class per detail level
 *  built when that level is first wanted (the water's classes 10 to 13 among them, the areas'
 *  path closed and filled even-odd), in map units relative to (ox, oy), the cell's top-left, kept
 *  in Double like the coast. Building every level up front cost three simplifications of every
 *  piece for every tile that landed, most of them for zooms the map was not at: that, times the
 *  dozens of tiles a pan over a city brings in, was the heat. */
internal class RoadTile(val key: Int, val ox: Double, val oy: Double, private val decoded: RoadTileGeom.Decoded, val names: List<RoadName>, val points: Int) {
    private val levels = arrayOfNulls<Array<Path?>>(RoadTiles.TOL_Q.size)
    private val building = BooleanArray(RoadTiles.TOL_Q.size)

    /** The paths at this level when built, else the nearest level that is (coarser first), else
     *  null; [missing] is true when the wanted level is not there yet, so the caller can ask for it. */
    fun pathsNear(lv: Int): Pair<Array<Path?>?, Boolean> {
        synchronized(this) {
            levels[lv]?.let { return it to false }
            for (d in 1 until RoadTiles.TOL_Q.size) {
                levels.getOrNull(lv - d)?.let { return it to true }
                levels.getOrNull(lv + d)?.let { return it to true }
            }
            return null to true
        }
    }

    fun has(lv: Int): Boolean = synchronized(this) { levels[lv] != null }

    /** Claims the build of a level: true for the caller that should do it, false when it is
     *  built or being built. */
    fun claim(lv: Int): Boolean = synchronized(this) {
        if (levels[lv] != null || building[lv]) false else { building[lv] = true; true }
    }

    /** Builds one level's paths from the pieces. Off the main thread; the result is published at
     *  the end, so a draw in the meantime still sees the nearest level. */
    fun build(lv: Int) {
        val deg = if (decoded.level == 1) 1.0 else 0.1
        val lon0 = decoded.x * deg - 180.0; val lat0 = decoded.y * deg - 90.0
        fun px(q: Int) = (mercXD(lon0 + q * deg / RoadTileGeom.Q) - ox).toFloat()
        fun py(q: Int) = (mercYD(lat0 + q * deg / RoadTileGeom.Q) - oy).toFloat()
        val tol = RoadTiles.TOL_Q[lv]
        val paths = arrayOfNulls<Path>(RoadTileGeom.MAX_CLASS + 1)
        for (p in decoded.pieces) {
            if (p.cls !in 1..RoadTileGeom.MAX_CLASS || p.pts.size < 4) continue
            val area = p.cls == RoadTileGeom.CLASS_WATER_AREA
            if (area && p.pts.size < 6) continue
            // a ring keeps its shape at every level: a lake simplified to a sliver is worse than none
            val r = if (area) RoadTileGeom.simplify(p.pts, tol / 4) else RoadTileGeom.simplify(p.pts, tol)
            if (area && r.size < 6) continue
            val path = paths[p.cls] ?: Path().also { paths[p.cls] = it; if (area) it.fillType = androidx.compose.ui.graphics.PathFillType.EvenOdd }
            path.moveTo(px(r[0]), py(r[1]))
            var i = 2
            while (i < r.size) { path.lineTo(px(r[i]), py(r[i + 1])); i += 2 }
            if (area) path.close()
        }
        synchronized(this) { levels[lv] = paths; building[lv] = false }
    }
}

internal object RoadTiles {
    /** Tolerances in quantised units per detail level, picked by zoom relative to the tile's own
     *  grid: level 0 coarse (at the zoom the tile appears), 1 medium, 2 every point. */
    val TOL_Q = intArrayOf(120, 12, 0)
    fun levelFor(pxz: Float, tileLevel: Int): Int {
        val base = if (tileLevel == 1) RoadTileGeom.MAJOR_PXZ else RoadTileGeom.FINE_PXZ
        return when {
            pxz < base * 6 -> 0
            pxz < base * 40 -> 1
            else -> 2
        }
    }

    /** Build the tile from its bytes with the paths of one level ([lv], the zoom it is wanted at);
     *  the other levels are built when first wanted. Off the main thread. */
    fun build(b: ByteArray, lv: Int = 0): RoadTile? {
        val d = RoadTileGeom.decode(b) ?: return null
        val deg = if (d.level == 1) 1.0 else 0.1
        val lon0 = d.x * deg - 180.0; val lat0 = d.y * deg - 90.0
        val ox = mercXD(lon0); val oy = mercYD(lat0 + deg)
        fun px(q: Int) = (mercXD(lon0 + q * deg / RoadTileGeom.Q) - ox).toFloat()
        fun py(q: Int) = (mercYD(lat0 + q * deg / RoadTileGeom.Q) - oy).toFloat()
        val names = ArrayList<RoadName>()
        var points = 0
        for (p in d.pieces) {
            if (p.cls !in 1..RoadTileGeom.MAX_CLASS || p.pts.size < 4) continue
            val area = p.cls == RoadTileGeom.CLASS_WATER_AREA
            if (area && p.pts.size < 6) continue
            points += p.pts.size / 2
            if (p.name.isNotEmpty() && (p.flags and RoadTileGeom.FLAG_HOLE) == 0) {
                if (area) {
                    // a lake's name sits at the middle of its piece; the piece's width says whether it fits
                    var x0 = Int.MAX_VALUE; var x1 = Int.MIN_VALUE; var y0 = Int.MAX_VALUE; var y1 = Int.MIN_VALUE
                    var i = 0
                    while (i < p.pts.size) { x0 = minOf(x0, p.pts[i]); x1 = maxOf(x1, p.pts[i]); y0 = minOf(y0, p.pts[i + 1]); y1 = maxOf(y1, p.pts[i + 1]); i += 2 }
                    val mx = (x0 + x1) / 2; val my = (y0 + y1) / 2
                    names.add(RoadName(p.name, p.cls, px(x0), py(my), px(x1), py(my), x1 - x0))
                } else {
                    // the middle segment carries the name; its quantised length says whether the name fits
                    val n = p.pts.size / 2
                    val m = (n - 1) / 2
                    var lenQ = 0
                    var i = 0
                    while (i < p.pts.size - 2) { lenQ += kotlin.math.abs(p.pts[i + 2] - p.pts[i]) + kotlin.math.abs(p.pts[i + 3] - p.pts[i + 1]); i += 2 }
                    names.add(RoadName(p.name, p.cls, px(p.pts[2 * m]), py(p.pts[2 * m + 1]), px(p.pts[2 * m + 2]), py(p.pts[2 * m + 3]), lenQ))
                }
            }
        }
        names.sortBy { it.cls }
        val t = RoadTile(RoadTileGeom.key(d.level, d.x, d.y), ox, oy, d, names, points)
        if (t.claim(lv)) t.build(lv)
        return t
    }
}

/** The road tiles in memory: an LRU of built tiles, what is being fetched, failures that retry
 *  after a minute. Tiles on disk are the box client's business (a separate cache from the coast). */
internal class RoadTileCache(private val max: Int = 60) {
    private val built = object : LinkedHashMap<Int, RoadTile>(max, 0.75f, true) {
        override fun removeEldestEntry(eldest: MutableMap.MutableEntry<Int, RoadTile>?) = size > max
    }
    private val inFlight = ConcurrentHashMap.newKeySet<Int>()
    private val failed = ConcurrentHashMap<Int, Long>()
    private val gate = Semaphore(4)
    @Volatile var failures = 0
        private set
    @Volatile var lastError: String = ""
        private set

    private var scope: CoroutineScope? = null
    private var landed: (() -> Unit)? = null
    @Volatile private var tickPending = false

    @Synchronized fun get(key: Int): RoadTile? = built[key]
    @Synchronized private fun put(t: RoadTile) { built[t.key] = t }

    /** One tick for every tile that lands within a third of a second, not one each: the map's
     *  base is drawn again on a tick, and a pan over a city lands dozens of tiles in a second. */
    private fun tick() {
        val sc = scope ?: return
        if (tickPending) return
        tickPending = true
        sc.launch(Dispatchers.Main) {
            kotlinx.coroutines.delay(300)
            tickPending = false
            landed?.invoke()
        }
    }

    /** The paths of [t] at detail level [lv]: the level itself when built, else the nearest built
     *  level for this frame while [lv] is built off the main thread (a tick follows). */
    fun pathsFor(t: RoadTile, lv: Int): Array<Path?>? {
        val (paths, missing) = t.pathsNear(lv)
        if (missing && t.claim(lv)) {
            val sc = scope
            if (sc == null) { t.build(lv) } else sc.launch(Dispatchers.IO) { t.build(lv); tick() }
        }
        return paths
    }

    fun ensure(scope: CoroutineScope, ctx: Context, keys: List<Int>, pxz: Float, landed: () -> Unit) {
        this.scope = scope
        this.landed = landed
        val now = System.currentTimeMillis()
        for (key in keys) {
            if (get(key) != null) continue
            val f = failed[key]
            if (f != null && now - f < 60_000L) continue
            if (!inFlight.add(key)) continue
            scope.launch(Dispatchers.IO) {
                try {
                    gate.withPermit {
                        val l = RoadTileGeom.levelOf(key); val x = RoadTileGeom.xOf(key); val y = RoadTileGeom.yOf(key)
                        val bytes = com.localghost.app.net.BoxClient.roadTile(ctx, l, x, y)
                        val t = bytes?.let { RoadTiles.build(it, RoadTiles.levelFor(pxz, l)) }
                        if (t != null) {
                            failed.remove(key)
                            put(t)
                            tick()
                        } else {
                            failed[key] = System.currentTimeMillis()
                            failures++
                            lastError = if (bytes == null) "road tile $l/$x,$y: no answer from the box" else "road tile $l/$x,$y: undecodable"
                            android.util.Log.w("LocalGhost", lastError)
                        }
                    }
                } finally {
                    inFlight.remove(key)
                }
            }
        }
    }
}
