package com.localghost.app.ui

import kotlin.math.abs
import kotlin.math.floor
import kotlin.math.ln

/**
 * THE ROADS, in two grids. The box cuts OpenStreetMap's roads (internal/roadtiles) into major
 * roads per one-degree cell (level 1: motorway to secondary, what a screen 2.5° across should
 * show) and every road with its name per tenth-of-a-degree cell (level 0: streets). The index says
 * which cells have a tile. A piece is a class, flags, an optional name, and points quantised to
 * 1/65535 of the cell's span (two metres in a 1° cell, twenty centimetres in a 0.1° cell).
 *
 * Pure: bytes in, geometry out; no Android in here so it runs in a plain JVM test.
 */
internal object RoadTileGeom {
    const val Q = 65535
    private const val TILE_MAGIC = 0x31524C47 // "GLR1"
    private const val INDEX_MAGIC = 0x31584C47 // "GLX1"
    const val MAJOR_COLS = 360; const val MAJOR_ROWS = 180
    const val FINE_COLS = 3600; const val FINE_ROWS = 1800

    /** The major tiles are fetched from the same zoom the coast tiles are; the street tiles from
     *  ten times closer (about 0.25° across a phone, 17 km in London), names from thirty times.
     *  Which classes a zoom draws is each class's own (minPxzFor). */
    const val MAJOR_PXZ = LandTileGeom.TILE_PXZ
    const val FINE_PXZ = LandTileGeom.TILE_PXZ * 10
    const val NAME_PXZ = LandTileGeom.TILE_PXZ * 30

    const val FLAG_ONEWAY = 1; const val FLAG_NAMED = 2; const val FLAG_TUNNEL = 4; const val FLAG_BRIDGE = 8; const val FLAG_HOLE = 16

    /** THE WATER rides in the same tiles (the box cuts it with the roads): an area (a lake, a
     *  reservoir, a river's banks) as a ring to fill, rivers, canals and streams as lines. A
     *  tile's rings are filled even-odd, so an island inside a lake is a hole by itself. */
    const val CLASS_WATER_AREA = 10; const val CLASS_RIVER = 11; const val CLASS_CANAL = 12; const val CLASS_STREAM = 13
    const val MAX_CLASS = 13
    fun isWater(cls: Int) = cls in CLASS_WATER_AREA..CLASS_STREAM

    class Piece(val cls: Int, val flags: Int, val name: String, val pts: IntArray)
    class Decoded(val level: Int, val x: Int, val y: Int, val pieces: List<Piece>)

    fun decode(b: ByteArray): Decoded? {
        fun u16(o: Int) = (b[o].toInt() and 0xff) or ((b[o + 1].toInt() and 0xff) shl 8)
        fun u32(o: Int) = u16(o) or (u16(o + 2) shl 16)
        if (b.size < 13 || u32(0) != TILE_MAGIC) return null
        val level = b[4].toInt(); val x = u16(5); val y = u16(7)
        if (level !in 0..1) return null
        val count = u32(9)
        if (count < 0 || count > 10_000_000) return null
        var off = 13
        val pieces = ArrayList<Piece>(count)
        repeat(count) {
            if (off + 2 > b.size) return null
            val cls = b[off].toInt() and 0xff; val flags = b[off + 1].toInt() and 0xff
            off += 2
            var name = ""
            if (flags and FLAG_NAMED != 0) {
                if (off + 2 > b.size) return null
                val n = u16(off); off += 2
                if (off + n > b.size) return null
                name = String(b, off, n, Charsets.UTF_8); off += n
            }
            if (off + 2 > b.size) return null
            val n = u16(off); off += 2
            if (off + 4 * n > b.size) return null
            val pts = IntArray(2 * n)
            for (k in 0 until 2 * n) pts[k] = u16(off + 2 * k)
            off += 4 * n
            pieces.add(Piece(cls, flags, name, pts))
        }
        return Decoded(level, x, y, pieces)
    }

    /** The index: which major cells (a byte each) and fine cells (a bit each) have tiles. */
    class Index(private val major: ByteArray, private val fine: ByteArray) {
        fun hasMajor(x: Int, y: Int) = major[y * MAJOR_COLS + x].toInt() != 0
        fun hasFine(x: Int, y: Int): Boolean { val k = y * FINE_COLS + x; return (fine[k shr 3].toInt() shr (k and 7)) and 1 != 0 }
    }

    fun index(b: ByteArray?): Index? {
        val majorLen = MAJOR_COLS * MAJOR_ROWS; val fineLen = FINE_COLS * FINE_ROWS / 8
        if (b == null || b.size != 4 + majorLen + fineLen) return null
        val m = (b[0].toInt() and 0xff) or ((b[1].toInt() and 0xff) shl 8) or ((b[2].toInt() and 0xff) shl 16) or ((b[3].toInt() and 0xff) shl 24)
        if (m != INDEX_MAGIC) return null
        return Index(b.copyOfRange(4, 4 + majorLen), b.copyOfRange(4 + majorLen, b.size))
    }

    /** A cell key that tells the two grids apart: level in the top bit. */
    fun key(level: Int, x: Int, y: Int): Int = (level shl 30) or (y * (if (level == 1) MAJOR_COLS else FINE_COLS) + x)
    fun levelOf(key: Int) = key ushr 30
    fun xOf(key: Int): Int { val c = if (levelOf(key) == 1) MAJOR_COLS else FINE_COLS; return (key and 0x3FFFFFFF) % c }
    fun yOf(key: Int): Int { val c = if (levelOf(key) == 1) MAJOR_COLS else FINE_COLS; return (key and 0x3FFFFFFF) / c }

    /** The cells of a level covering a lon/lat window that HAVE a tile; empty when too many. */
    fun cellsFor(idx: Index, level: Int, lon0: Double, lon1: Double, latLo: Double, latHi: Double, max: Int = 48): List<Int> {
        val deg = if (level == 1) 1.0 else 0.1
        val cols = if (level == 1) MAJOR_COLS else FINE_COLS
        val rows = if (level == 1) MAJOR_ROWS else FINE_ROWS
        val x0 = floor((lon0 + 180) / deg).toInt().coerceIn(0, cols - 1)
        val x1 = floor((lon1 + 180) / deg).toInt().coerceIn(0, cols - 1)
        val y0 = floor((latLo + 90) / deg).toInt().coerceIn(0, rows - 1)
        val y1 = floor((latHi + 90) / deg).toInt().coerceIn(0, rows - 1)
        if ((x1 - x0 + 1) * (y1 - y0 + 1) > max) return emptyList()
        val out = ArrayList<Int>()
        for (y in y0..y1) for (x in x0..x1) {
            val has = if (level == 1) idx.hasMajor(x, y) else idx.hasFine(x, y)
            if (has) out.add(key(level, x, y))
        }
        return out
    }

    /** Distance filter in quantised units; the first and last points always stay. */
    fun simplify(r: IntArray, tolQ: Int): IntArray {
        if (tolQ <= 0 || r.size <= 4) return r
        val out = IntArray(r.size)
        var k = 0
        var lx = r[0]; var ly = r[1]
        out[k++] = lx; out[k++] = ly
        var i = 2
        while (i < r.size - 2) {
            val x = r[i]; val y = r[i + 1]
            if (abs(x - lx) >= tolQ || abs(y - ly) >= tolQ) { out[k++] = x; out[k++] = y; lx = x; ly = y }
            i += 2
        }
        out[k++] = r[r.size - 2]; out[k++] = r[r.size - 1]
        return out.copyOf(k)
    }

    /** Whether a water class shows at a zoom: areas, rivers and canals from the major tiles' zoom,
     *  streams from the zoom that shows the residential streets. */
    fun showsWater(cls: Int, pxz: Float): Boolean = when (cls) {
        CLASS_STREAM -> pxz >= minPxzFor(6)
        else -> isWater(cls) && pxz >= MAJOR_PXZ
    }

    /**
     * WHEN A ROAD COMES IN. Each class has its own zoom, and the closer the view the more classes
     * (18 November 2026, Vlad: "I want the highest level of detail at the lowest zoom but
     * otherwise it should show maybe less"). Every street used to come in at FINE_PXZ, a city
     * 17 km across drawn as a mesh with the trail lost in it. Across a phone 1,080 pixels wide in
     * London, roughly: motorways and trunk roads from the country, primaries from 60 km, the
     * secondaries from 17 km, tertiary from 9 km, the streets from 4 km, tracks and paths from 2 to
     * 3 km, service roads from 1.7 km. Class numbers as the box assigns them: 1 motorway, 2 trunk,
     * 3 primary, 4 secondary, 5 tertiary, 6 residential, 7 service, 8 track, 9 path.
     */
    fun minPxzFor(cls: Int): Float = when (cls) {
        1, 2 -> MAJOR_PXZ
        3 -> MAJOR_PXZ * 3f
        4 -> FINE_PXZ
        5 -> FINE_PXZ * 2f
        6 -> FINE_PXZ * 4f
        7 -> FINE_PXZ * 10f
        8 -> FINE_PXZ * 6f
        9 -> FINE_PXZ * 8f
        else -> Float.POSITIVE_INFINITY
    }

    /** How far in a class has come at a zoom: 0 where it starts (drawn faint and thin, no
     *  casing), rising to 1 two doublings of zoom later (drawn in full); below its zoom, null, not
     *  drawn at all. A class fades in rather than appearing whole at one pinch. */
    fun emphasisFor(cls: Int, pxz: Float): Float? {
        val m = minPxzFor(cls)
        if (!(pxz >= m)) return null
        return (ln(pxz / m) / ln(4f)).coerceIn(0f, 1f)
    }

    /** A road's name shows once the road is drawn in full, and never before NAME_PXZ: the
     *  primaries named from about 6 km across, the streets from about 1 km. */
    fun namesShow(cls: Int, pxz: Float): Boolean = pxz >= NAME_PXZ && pxz >= minPxzFor(cls) * 4f
}
