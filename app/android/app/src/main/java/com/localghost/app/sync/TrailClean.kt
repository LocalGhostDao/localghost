package com.localghost.app.sync

import kotlin.math.PI
import kotlin.math.asin
import kotlin.math.cos
import kotlin.math.min
import kotlin.math.sin
import kotlin.math.sqrt

/**
 * Glitches in the trail , the phone's twin of the box's framed/clean.go, rule for rule and number
 * for number, so the two days the phone draws on its own and the days the box hands back agree.
 * A fix is sometimes not where the phone is (a cell-tower position, a stale fix from another
 * provider); on the map it is a spike, out to a point far away and straight back. The spool keeps
 * every fix; this is the view.
 *
 *  1. an IMPOSSIBLE hop , faster than anything a person rides , is dropped on its own.
 *  2. a FAST hop (faster than road or rail) the trail COMES BACK from within a few points and a
 *     short time is a spike; the points out there are dropped. A flight is a fast hop that does
 *     not come back.
 *  3. a LONE point reached and left at car speed or better between walking pace is a spike too.
 *
 * Hops under [MIN_JUMP_M] are never judged.
 */
object TrailClean {
    private const val MIN_JUMP_M = 300.0
    private const val MAX_SPEED_MS = 350.0
    private const val FAST_SPEED_MS = 90.0
    private const val LONE_LEG_MS = 12.0
    private const val SLOW_MS = 4.0
    private const val RETURN_FRAC = 0.34
    private const val RETURN_MAX_POINTS = 8
    private const val RETURN_MAX_S = 5400L

    class Cleaned(val kept: List<LocationLog.Point>, val dropped: Int)

    fun clean(pts: List<LocationLog.Point>): Cleaned {
        if (pts.size < 2) return Cleaned(pts, 0)
        val sorted = pts.sortedBy { it.ts } // stable: equal seconds keep their order
        val kept = ArrayList<LocationLog.Point>(sorted.size)
        kept.add(sorted[0])
        var dropped = 0
        var i = 1
        while (i < sorted.size) {
            val a = kept.last()
            val p = sorted[i]
            val d = haversineM(a, p)
            if (d < MIN_JUMP_M) { kept.add(p); i++; continue }
            val v = speedMS(a, p, d)
            if (v > MAX_SPEED_MS) { dropped++; i++; continue } // rule 1
            if (v > FAST_SPEED_MS) { // rule 2
                val j = returnsTo(sorted, i, a, d)
                if (j > 0) { dropped += j - i; i = j; continue }
            }
            if (i + 1 < sorted.size && v > LONE_LEG_MS) { // rule 3
                val n = sorted[i + 1]
                val back = haversineM(p, n)
                if (back >= MIN_JUMP_M && speedMS(p, n, back) > LONE_LEG_MS && haversineM(a, n) <= d * RETURN_FRAC &&
                    slowBefore(kept) && slowAfter(sorted, i + 1)) {
                    dropped++; i++; continue
                }
            }
            kept.add(p)
            i++
        }
        return Cleaned(kept, dropped)
    }

    private fun returnsTo(pts: List<LocationLog.Point>, i: Int, a: LocationLog.Point, d: Double): Int {
        var j = i + 1
        while (j < pts.size && j <= i + RETURN_MAX_POINTS) {
            if (pts[j].ts - pts[i].ts > RETURN_MAX_S) return 0
            if (haversineM(a, pts[j]) <= d * RETURN_FRAC) return j
            j++
        }
        return 0
    }

    private fun slowBefore(kept: List<LocationLog.Point>): Boolean {
        if (kept.size < 2) return true
        val a = kept[kept.size - 2]; val b = kept[kept.size - 1]
        val d = haversineM(a, b)
        return d < MIN_JUMP_M || speedMS(a, b, d) <= SLOW_MS
    }

    private fun slowAfter(pts: List<LocationLog.Point>, k: Int): Boolean {
        if (k + 1 >= pts.size) return true
        val d = haversineM(pts[k], pts[k + 1])
        return d < MIN_JUMP_M || speedMS(pts[k], pts[k + 1], d) <= SLOW_MS
    }

    private fun speedMS(a: LocationLog.Point, b: LocationLog.Point, d: Double): Double {
        val dt = b.ts - a.ts
        return if (dt <= 0) Double.POSITIVE_INFINITY else d / dt
    }

    /** Great-circle metres on a 6371 km sphere , the box's formula, not the OS's (an ellipsoid),
     *  so a hop that is 299 m here is 299 m there. */
    fun haversineM(a: LocationLog.Point, b: LocationLog.Point): Double {
        val r = 6371000.0
        val la1 = a.lat * PI / 180; val la2 = b.lat * PI / 180
        val dla = la2 - la1
        val dlo = (b.lon - a.lon) * PI / 180
        val h = sin(dla / 2) * sin(dla / 2) + cos(la1) * cos(la2) * sin(dlo / 2) * sin(dlo / 2)
        return 2 * r * asin(min(1.0, sqrt(h)))
    }
}
