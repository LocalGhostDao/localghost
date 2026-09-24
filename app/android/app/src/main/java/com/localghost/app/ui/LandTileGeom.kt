package com.localghost.app.ui

import kotlin.math.abs
import kotlin.math.floor

/**
 * THE COAST AT FULL DETAIL, one degree at a time. The base map is Natural Earth's 10m countries:
 * right for a continent, a smudge for an island. The box cuts OpenStreetMap's land polygons into a
 * 360×180 grid of one-degree cells (internal/landtiles on the box); this side fetches the index
 * once (a byte per cell: 0 water, 1 coast, 2 land) and then only the coast tiles under the
 * viewport, and only once zoomed in past what the 10m file can show. A tile is uint16 pairs,
 * 1/65535 of a degree , under two metres , relative to its cell's corner.
 *
 * Two things the cutting leaves that drawing must respect: every artificial edge lies exactly on a
 * cell border (the box only ever cuts on whole degrees), so the COASTLINE stroke skips edges that
 * run along a border and the tile seams never show; and the FILL is all of a tile's rings in one
 * Path under the non-zero rule, so a lake (a ring the other way round) stays water.
 */
internal object LandTileGeom {
    const val Q = 65535
    const val COLS = 360
    const val ROWS = 180
    const val WATER = 0
    const val COAST = 1
    const val LAND = 2
    private const val TILE_MAGIC = 0x4C475431 // "LGT1"
    const val INDEX_MAGIC = 0x4C474931 // "LGI1"

    /** Zoomed in this far (screen px per map unit), the 10m base is coarser than a pixel on coasts. */
    const val TILE_PXZ = 150f

    /** One decoded tile: cell and rings of quantised (x, y) pairs, Ints 0..65535. */
    class Decoded(val x: Int, val y: Int, val rings: List<IntArray>)

    fun decode(b: ByteArray): Decoded? {
        fun u16(o: Int) = (b[o].toInt() and 0xff) or ((b[o + 1].toInt() and 0xff) shl 8)
        fun u32(o: Int) = u16(o) or (u16(o + 2) shl 16)
        if (b.size < 12 || u32(0) != TILE_MAGIC) return null
        val x = u16(4); val y = u16(6)
        val count = u32(8)
        if (count < 0 || count > 10_000_000) return null
        var off = 12
        val rings = ArrayList<IntArray>(count)
        repeat(count) {
            if (off + 4 > b.size) return null
            val n = u32(off); off += 4
            if (n < 0 || off + 4 * n > b.size) return null
            val r = IntArray(2 * n)
            for (k in 0 until 2 * n) r[k] = u16(off + 2 * k)
            off += 4 * n
            rings.add(r)
        }
        return Decoded(x, y, rings)
    }

    /** The index behind its magic: 64,800 cell states, or null. */
    fun index(b: ByteArray?): ByteArray? {
        if (b == null || b.size != 4 + COLS * ROWS) return null
        val m = (b[0].toInt() and 0xff) or ((b[1].toInt() and 0xff) shl 8) or ((b[2].toInt() and 0xff) shl 16) or ((b[3].toInt() and 0xff) shl 24)
        return if (m == INDEX_MAGIC) b.copyOfRange(4, b.size) else null
    }

    private fun onBorder(qx: Int, qy: Int) = qx == 0 || qx == Q || qy == 0 || qy == Q

    /** An edge that runs along the cell border: both ends on the same side. Never coastline. */
    fun borderEdge(ax: Int, ay: Int, bx: Int, by: Int): Boolean =
        (ax == 0 && bx == 0) || (ax == Q && bx == Q) || (ay == 0 && by == 0) || (ay == Q && by == Q)

    /** Distance filter in quantised units, keeping every border vertex so border runs stay border
     *  runs (and the stroke rule above still recognises them) whatever the tolerance. */
    fun simplify(r: IntArray, tolQ: Int): IntArray {
        if (tolQ <= 0 || r.size < 8) return r
        val out = IntArray(r.size)
        var k = 0
        var lx = r[0]; var ly = r[1]
        out[k++] = lx; out[k++] = ly
        var i = 2
        while (i < r.size) {
            val x = r[i]; val y = r[i + 1]
            if (onBorder(x, y) || abs(x - lx) >= tolQ || abs(y - ly) >= tolQ) {
                out[k++] = x; out[k++] = y; lx = x; ly = y
            }
            i += 2
        }
        return if (k >= 6) out.copyOf(k) else IntArray(0)
    }

    /** The coastline of a ring as runs of consecutive coastal edges (the ring is closed: the last
     *  point joins the first). Each run is a list of point indices into the ring. */
    fun coastRuns(r: IntArray): List<IntArray> {
        val n = r.size / 2
        if (n < 2) return emptyList()
        val coastal = BooleanArray(n) { i -> val j = (i + 1) % n; !borderEdge(r[2 * i], r[2 * i + 1], r[2 * j], r[2 * j + 1]) }
        if (coastal.all { it }) return listOf(IntArray(n + 1) { it % n })
        // start right after a border edge so no run wraps
        val start = (coastal.indexOfFirst { !it } + 1) % n
        val runs = ArrayList<IntArray>()
        var cur = ArrayList<Int>()
        for (s in 0 until n) {
            val i = (start + s) % n
            if (coastal[i]) {
                if (cur.isEmpty()) cur.add(i)
                cur.add((i + 1) % n)
            } else if (cur.isNotEmpty()) {
                runs.add(cur.toIntArray()); cur = ArrayList()
            }
        }
        if (cur.isNotEmpty()) runs.add(cur.toIntArray())
        return runs
    }

    /** Tolerances in quantised units for the three levels, and the level for a zoom. At TILE_PXZ a
     *  pixel is ~260 m; at ten times that ~26 m; past a hundred times, every point. */
    val TOL_Q = intArrayOf(160, 16, 0)
    fun levelFor(pxz: Float): Int = when {
        pxz < TILE_PXZ * 10 -> 0
        pxz < TILE_PXZ * 100 -> 1
        else -> 2
    }

    /** The cells (x, y) covering a lon/lat window, clamped to the grid; empty when too many. */
    fun cellsFor(lon0: Double, lon1: Double, latLo: Double, latHi: Double, max: Int = 48): List<Int> {
        val x0 = (floor(lon0).toInt() + 180).coerceIn(0, COLS - 1)
        val x1 = (floor(lon1).toInt() + 180).coerceIn(0, COLS - 1)
        val y0 = (floor(latLo).toInt() + 90).coerceIn(0, ROWS - 1)
        val y1 = (floor(latHi).toInt() + 90).coerceIn(0, ROWS - 1)
        if ((x1 - x0 + 1) * (y1 - y0 + 1) > max) return emptyList()
        val out = ArrayList<Int>()
        for (y in y0..y1) for (x in x0..x1) out.add(y * COLS + x)
        return out
    }
}
