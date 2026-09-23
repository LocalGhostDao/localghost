package com.localghost.app.sync

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The phone's glitch rules against the SAME fixture the box's framed/clean_test.go reads
 * (trail_glitches.txt: one block per case, "ts lat lon [x]" per point, x = must be dropped).
 * If a rule or a number drifts on one side, one of the two tests fails, and the map would show
 * a spike on the phone's two days that the box's days do not have, or the other way round.
 */
class TrailCleanTest {
    private class Case(val name: String, val pts: List<LocationLog.Point>, val drop: Set<Int>)

    private fun cases(): List<Case> {
        val lines = javaClass.getResourceAsStream("/trail_glitches.txt")!!.bufferedReader().readLines()
        val out = ArrayList<Case>()
        var name = ""
        val pts = ArrayList<LocationLog.Point>()
        val drop = HashSet<Int>()
        fun flush() { if (pts.isNotEmpty()) out.add(Case(name, pts.toList(), drop.toSet())); pts.clear(); drop.clear() }
        for (raw in lines) {
            val l = raw.trim()
            when {
                l.isEmpty() -> flush()
                l.startsWith("#") -> name = l.removePrefix("#").substringBefore(":").trim()
                else -> {
                    val p = l.split(" ")
                    if (p.size > 3 && p[3] == "x") drop.add(pts.size)
                    pts.add(LocationLog.Point(p[0].toLong(), p[1].toDouble(), p[2].toDouble()))
                }
            }
        }
        flush()
        return out
    }

    @Test
    fun everyFixtureCaseGetsTheBoxsVerdict() {
        val cs = cases()
        assertTrue("fixture read", cs.size >= 5)
        for (c in cs) {
            val out = TrailClean.clean(c.pts)
            val wantKept = c.pts.filterIndexed { i, _ -> i !in c.drop }
            assertEquals("${c.name}: kept", wantKept.map { it.ts }, out.kept.map { it.ts })
            assertEquals("${c.name}: dropped", c.drop.size, out.dropped)
        }
    }

    @Test
    fun shortAndUnsortedInput() {
        assertEquals(0, TrailClean.clean(emptyList()).dropped)
        val one = listOf(LocationLog.Point(1, 39.15, 20.22))
        assertEquals(one, TrailClean.clean(one).kept)
        val unsorted = listOf(
            LocationLog.Point(1758001800, 39.1526, 20.22),
            LocationLog.Point(1758000900, 40.05, 20.22), // the spike, second in time
            LocationLog.Point(1758000000, 39.15, 20.22),
            LocationLog.Point(1758002700, 39.1539, 20.22))
        val out = TrailClean.clean(unsorted)
        assertEquals(1, out.dropped)
        assertEquals(listOf(1758000000L, 1758001800L, 1758002700L), out.kept.map { it.ts })
    }

    @Test
    fun haversineIsTheBoxsSphereNotTheOssEllipsoid() {
        // one degree of latitude on the 6371 km sphere: 111195 m (the box's TrackDistance test says the same)
        val d = TrailClean.haversineM(LocationLog.Point(0, 0.0, 0.0), LocationLog.Point(1, 1.0, 0.0))
        assertTrue("$d", d > 111190 && d < 111200)
    }
}
