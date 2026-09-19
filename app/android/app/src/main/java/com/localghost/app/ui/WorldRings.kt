package com.localghost.app.ui

import android.content.Context
import androidx.compose.ui.graphics.Path
import java.io.File
import java.nio.ByteBuffer
import java.nio.ByteOrder
import kotlin.math.PI
import kotlin.math.abs
import kotlin.math.cos
import kotlin.math.ln
import kotlin.math.tan

/**
 * The landmass, prepared ONCE for drawing. The map draws Natural Earth's 10m countries file ,
 * roughly 550,000 vertices in ~4,000 rings, 24MB of GeoJSON , and it used to (a) parse that with
 * org.json on the main thread at every open (a second of freeze and ~100MB of boxed Doubles), and
 * (b) rebuild a Path per ring with a projection call per vertex ON EVERY PAN AND PINCH EVENT. The
 * phone was doing half a million lineTo calls sixty times a second to move a coastline two pixels.
 *
 * Now: a byte scanner pulls the coordinate pairs straight out of the JSON (no tree, no boxing),
 * the projected rings are cached as a flat binary keyed on the box's ETag (later opens skip the
 * JSON entirely), three detail levels are precomputed by a tolerance filter, and every ring is a
 * pre-built Path in MAP UNITS relative to its own origin. A frame is then one translate+scale per
 * visible ring and a drawPath , no per-vertex work at all , with rings culled by bounding box.
 */

internal const val WORLD_UNITS = 1024.0

internal fun mercXD(lon: Double): Double = (lon + 180.0) / 360.0 * WORLD_UNITS
internal fun mercYD(lat: Double): Double {
    val l = lat.coerceIn(-85.05, 85.05) * PI / 180.0
    return (1.0 - ln(tan(l) + 1.0 / cos(l)) / PI) / 2.0 * WORLD_UNITS
}

/** One ring at one detail level: a Path in map units RELATIVE to (ox, oy) , the ring's own origin,
 *  which keeps float precision at street zoom (a continent-sized absolute coordinate has ~1e-4 map
 *  units of slop; scaled 250,000x that is thirty pixels of jitter) , plus an absolute bbox so a
 *  frame skips everything off-screen without touching a vertex. */
internal class Ring(
    val ox: Float, val oy: Float,
    val minX: Float, val minY: Float, val maxX: Float, val maxY: Float,
    val path: Path, val vertices: Int,
)

/** Raw projected vertices, absolute map units , the cacheable form. */
internal class RawRing(val xs: FloatArray, val ys: FloatArray)

/** The world at three detail levels, coarse to raw. Level tolerances are in map units; the
 *  drawing side picks the level whose tolerance is under a pixel at the current scale. */
internal class World(val levels: List<List<Ring>>, val vertices: IntArray) {
    companion object {
        val TOLERANCES = floatArrayOf(0.5f, 0.05f, 0f)
        /** Pick the coarsest level still under ~a pixel of error at pxz screen px per map unit. */
        fun levelFor(pxz: Float): Int = when {
            pxz < 2f -> 0
            pxz < 20f -> 1
            else -> 2
        }
    }
}

internal object WorldRings {
    private const val MAGIC = 0x4C475731 // "LGW1"

    /** Load one world cut: binary cache when its ETag matches, else scan the GeoJSON and write the
     *  cache. name keys the cache per cut ("110m", "default"), so the small and the big world each
     *  keep their own. Call OFF the main thread. Null when there is no such world file at all. */
    fun load(ctx: Context, geojson: File?, etag: String, name: String = "default"): World? {
        val prefs = ctx.getSharedPreferences("ghost_geo", Context.MODE_PRIVATE)
        val cache = File(ctx.filesDir, "world-$name.rings")
        val tagKey = "world_rings_etag_$name"
        val cached: List<RawRing>? =
            if (cache.exists() && etag.isNotEmpty() && prefs.getString(tagKey, null) == etag)
                runCatching { readCache(cache) }.getOrNull()
            else null
        val raw: List<RawRing> = cached ?: run {
            if (geojson == null || !geojson.exists()) return null
            val scanned = runCatching { scanGeoJson(geojson.readBytes()) }.getOrNull() ?: return null
            runCatching {
                writeCache(cache, scanned)
                prefs.edit().putString(tagKey, etag).apply()
            }
            scanned
        }
        val levels = ArrayList<List<Ring>>(World.TOLERANCES.size)
        val counts = IntArray(World.TOLERANCES.size)
        for ((li, tol) in World.TOLERANCES.withIndex()) {
            val out = ArrayList<Ring>(raw.size)
            var n = 0
            for (r in raw) {
                val s = simplify(r, tol) ?: continue
                out.add(build(s, closed = true))
                n += s.xs.size
            }
            levels.add(out)
            counts[li] = n
        }
        return World(levels, counts)
    }

    /** Build a drawable ring (or an open track when closed=false) from absolute map-unit vertices. */
    fun build(r: RawRing, closed: Boolean): Ring {
        var minX = Float.MAX_VALUE; var minY = Float.MAX_VALUE
        var maxX = -Float.MAX_VALUE; var maxY = -Float.MAX_VALUE
        for (i in r.xs.indices) {
            val x = r.xs[i]; val y = r.ys[i]
            if (x < minX) minX = x; if (x > maxX) maxX = x
            if (y < minY) minY = y; if (y > maxY) maxY = y
        }
        val path = Path()
        path.moveTo(r.xs[0] - minX, r.ys[0] - minY)
        for (i in 1 until r.xs.size) path.lineTo(r.xs[i] - minX, r.ys[i] - minY)
        if (closed) path.close()
        return Ring(minX, minY, minX, minY, maxX, maxY, path, r.xs.size)
    }

    /** Sequential distance filter: keep a vertex only once it has moved at least tol from the last
     *  kept one. Not Douglas-Peucker, and it does not need to be , the tolerance is chosen to sit
     *  under a screen pixel, so what it drops was never going to be visible. Rings that collapse
     *  below a triangle at this scale (small islands at world zoom) are dropped entirely: no draw
     *  call for a dot nobody can see. */
    private fun simplify(r: RawRing, tol: Float): RawRing? {
        if (r.xs.size < 3) return null
        if (tol <= 0f) return r
        val n = r.xs.size
        val xs = FloatArray(n); val ys = FloatArray(n)
        var lx = r.xs[0]; var ly = r.ys[0]
        xs[0] = lx; ys[0] = ly
        var k = 1
        for (i in 1 until n) {
            val x = r.xs[i]; val y = r.ys[i]
            if (abs(x - lx) >= tol || abs(y - ly) >= tol) {
                xs[k] = x; ys[k] = y; k++
                lx = x; ly = y
            }
        }
        if (k < 3) return null
        return RawRing(xs.copyOf(k), ys.copyOf(k))
    }

    // --- binary cache: MAGIC, ringCount, then per ring: n, n×x, n×y (little-endian floats) ---

    private fun writeCache(f: File, rings: List<RawRing>) {
        var bytes = 8
        for (r in rings) bytes += 4 + r.xs.size * 8
        val buf = ByteBuffer.allocate(bytes).order(ByteOrder.LITTLE_ENDIAN)
        buf.putInt(MAGIC).putInt(rings.size)
        for (r in rings) {
            buf.putInt(r.xs.size)
            buf.asFloatBuffer().put(r.xs); buf.position(buf.position() + r.xs.size * 4)
            buf.asFloatBuffer().put(r.ys); buf.position(buf.position() + r.ys.size * 4)
        }
        val tmp = File(f.parentFile, f.name + ".part")
        tmp.writeBytes(buf.array())
        if (!tmp.renameTo(f)) { tmp.delete(); throw IllegalStateException("rings cache rename failed") }
    }

    private fun readCache(f: File): List<RawRing>? {
        val buf = ByteBuffer.wrap(f.readBytes()).order(ByteOrder.LITTLE_ENDIAN)
        if (buf.remaining() < 8 || buf.getInt() != MAGIC) return null
        val count = buf.getInt()
        if (count < 0 || count > 1_000_000) return null
        val out = ArrayList<RawRing>(count)
        repeat(count) {
            if (buf.remaining() < 4) return null
            val n = buf.getInt()
            if (n < 0 || buf.remaining() < n * 8) return null
            val xs = FloatArray(n); val ys = FloatArray(n)
            buf.asFloatBuffer().get(xs); buf.position(buf.position() + n * 4)
            buf.asFloatBuffer().get(ys); buf.position(buf.position() + n * 4)
            out.add(RawRing(xs, ys))
        }
        return out
    }

    // --- the byte scanner ---

    /**
     * Pull every coordinate ring out of a GeoJSON byte array without building a JSON tree. The
     * grammar we rely on: a position is an array that begins with a number; a ring is the array
     * that directly contains positions; strings are skipped whole (property values may contain
     * brackets). Works for Polygon and MultiPolygon alike and ignores properties, because a
     * property array of numbers would need to be an array OF ARRAYS of numbers to be mistaken for
     * a ring, and Natural Earth has none. Rings under three vertices are dropped.
     */
    fun scanGeoJson(b: ByteArray): List<RawRing> {
        val out = ArrayList<RawRing>(4096)
        var xs = FloatArray(1024); var ys = FloatArray(1024); var k = 0
        var depth = 0
        var ringDepth = -1
        var i = 0
        val n = b.size
        fun flush() {
            if (k >= 3) out.add(RawRing(xs.copyOf(k), ys.copyOf(k)))
            k = 0
        }
        while (i < n) {
            val c = b[i].toInt()
            when (c) {
                '"'.code -> { // skip a string literal, honouring escapes
                    i++
                    while (i < n && b[i].toInt() != '"'.code) { if (b[i].toInt() == '\\'.code) i++; i++ }
                    i++
                }
                '['.code -> {
                    depth++
                    var j = i + 1
                    while (j < n && (b[j].toInt() == ' '.code || b[j].toInt() == '\n'.code || b[j].toInt() == '\r'.code || b[j].toInt() == '\t'.code)) j++
                    val nc = if (j < n) b[j].toInt() else 0
                    if (nc == '-'.code || (nc >= '0'.code && nc <= '9'.code)) {
                        // a position: [lon, lat, ...]
                        if (ringDepth != depth - 1) { flush(); ringDepth = depth - 1 }
                        val p1 = parseNumber(b, j); val lon = p1.first; j = p1.second
                        while (j < n && b[j].toInt() != ','.code && b[j].toInt() != ']'.code) j++
                        var lat = 0.0
                        if (j < n && b[j].toInt() == ','.code) {
                            j++
                            while (j < n && b[j].toInt() == ' '.code) j++
                            val p2 = parseNumber(b, j); lat = p2.first; j = p2.second
                        }
                        while (j < n && b[j].toInt() != ']'.code) j++ // altitude etc.
                        if (k == xs.size) { xs = xs.copyOf(k * 2); ys = ys.copyOf(k * 2) }
                        xs[k] = mercXD(lon).toFloat(); ys[k] = mercYD(lat).toFloat(); k++
                        depth--
                        i = j + 1
                    } else {
                        i++
                    }
                }
                ']'.code -> {
                    if (depth == ringDepth) { flush(); ringDepth = -1 }
                    depth--
                    i++
                }
                else -> i++
            }
        }
        flush()
        return out
    }

    /** Parse a JSON number starting at i; returns (value, index after it). Sign, integer part,
     *  fraction, optional exponent. */
    private fun parseNumber(b: ByteArray, start: Int): Pair<Double, Int> {
        var i = start
        val n = b.size
        var neg = false
        if (i < n && b[i].toInt() == '-'.code) { neg = true; i++ }
        var v = 0.0
        while (i < n && b[i] >= '0'.code.toByte() && b[i] <= '9'.code.toByte()) { v = v * 10 + (b[i] - '0'.code.toByte()); i++ }
        if (i < n && b[i].toInt() == '.'.code) {
            i++
            var scale = 0.1
            while (i < n && b[i] >= '0'.code.toByte() && b[i] <= '9'.code.toByte()) { v += (b[i] - '0'.code.toByte()) * scale; scale *= 0.1; i++ }
        }
        if (i < n && (b[i].toInt() == 'e'.code || b[i].toInt() == 'E'.code)) {
            i++
            var eneg = false
            if (i < n && (b[i].toInt() == '-'.code || b[i].toInt() == '+'.code)) { eneg = b[i].toInt() == '-'.code; i++ }
            var e = 0
            while (i < n && b[i] >= '0'.code.toByte() && b[i] <= '9'.code.toByte()) { e = e * 10 + (b[i] - '0'.code.toByte()); i++ }
            v *= Math.pow(10.0, (if (eneg) -e else e).toDouble())
        }
        return Pair(if (neg) -v else v, i)
    }
}
