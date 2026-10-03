package com.localghost.app.ui

import androidx.compose.animation.core.animateFloat
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.gestures.detectTransformGestures
import androidx.compose.foundation.border
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Slider
import androidx.compose.material3.SliderDefaults
import androidx.compose.material3.Text
import androidx.compose.ui.Alignment
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.PathEffect
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.drawscope.withTransform
import androidx.compose.ui.draw.clipToBounds
import androidx.compose.ui.graphics.nativeCanvas
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.graphics.drawscope.clipRect
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.unit.dp
import com.localghost.app.net.BoxClient
import com.localghost.app.ui.theme.*
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch
import kotlinx.coroutines.withContext
import kotlin.math.PI
import kotlin.math.atan
import kotlin.math.sinh

/**
 * The map, SELF-DRAWN , no tile servers, no map SDK, because every tile fetch ships the viewport
 * (and therefore where your photos are) to a third party, which is the one thing this product does
 * not do. Instead: Web Mercator on a Compose Canvas; landmass from the operator-provided Natural
 * Earth GeoJSON on the box (/v1/geo/world , public domain, optional); your photo locations from
 * the box's level-of-detail feed as dots and counted clusters. No streets, no labels beyond the
 * tapped dot's place string , an outline world with YOUR data on it, fully offline, and it says so.
 *
 * DRAWING BUDGET. The world is ~550k vertices. It is parsed off the main thread once per world
 * file (binary-cached after that, see WorldRings), held as pre-built Paths at three detail levels,
 * and a frame costs one transform + one drawPath per VISIBLE ring at the level that is under a
 * pixel of error , tens of draw calls at world zoom, a handful at street zoom, and zero per-vertex
 * work anywhere. The camera is Double: at 250,000x a Float map unit is thirty pixels wide, and the
 * map goes to a million (a screen about thirty metres across, MapPick.MAX_ZOOM).
 */

private const val WORLD = 1024f // == WORLD_UNITS, the Float twin for screen-space arithmetic

// A MAP, not a wireframe: the sea a deep blue-black, the land a shade lighter and greener, the coast
// the dim phosphor line it always was. Dark on purpose , the dots and the day's trail are the bright
// things on this screen, and the land is where they sit.
private val MapWater = androidx.compose.ui.graphics.Color(0xFF0A1620)
// The trail of a day that is not lit: amber, so it reads as a path over land and coast alike , in
// the coast's own dim green it vanished into the coastline. Every trail sits on a dark halo.
private val MapTrail = androidx.compose.ui.graphics.Color(0xFFD9A441)
private val MapLand = androidx.compose.ui.graphics.Color(0xFF16211A)

private fun invMercX(x: Double): Double = x / WORLD_UNITS * 360.0 - 180.0
private fun invMercY(y: Double): Double {
    val n = PI - 2.0 * PI * (y / WORLD_UNITS)
    return 180.0 / PI * atan(sinh(n))
}

/** A photo cell projected ONCE to map units when it arrives , the draw loop only scales. */
private class Dot(val x: Double, val y: Double, val cell: BoxClient.GeoCell)

/**
 * WHAT THE MAP LAST SHOWED, for the next open to draw at once. The box's data (photo cells, the
 * day tracks, the newest photo) stays in memory only, like the rest of an unlocked session, and
 * goes on lock ([clearMapMemory] from the app's teardown); the place names are public and are
 * kept with it for the same process.
 */
private object MapMemory {
    @Volatile var cells: List<BoxClient.GeoCell> = emptyList()
    @Volatile var labels: List<BoxClient.GeoLabel> = emptyList()
    @Volatile var tracks: List<Track>? = null
    @Volatile var newest: BoxClient.GeoCell? = null
    @Volatile var questions: Map<String, List<com.localghost.app.net.TrailQuestion>> = emptyMap()
}

/** One "were you there?" in the day panel: the question, then [ NO, DELETE IT ] and [ YES, KEEP IT ]. */
@Composable
private fun TrailQuestionCard(q: com.localghost.app.net.TrailQuestion, clock: (Long) -> String, busy: Boolean, onAnswer: (Boolean) -> Unit) {
    Column(Modifier.fillMaxWidth().padding(vertical = 6.dp).border(1.dp, GhostBorder, RectangleShape).padding(10.dp)) {
        Text(q.text(clock), color = GhostText, style = MaterialTheme.typography.labelMedium)
        Row(Modifier.padding(top = 8.dp)) {
            if (busy) {
                Text("…", color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
            } else {
                Text("[ NO, DELETE IT ]", color = Warning, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { onAnswer(false) }.padding(end = 16.dp, top = 4.dp, bottom = 4.dp))
                Text("[ YES, KEEP IT ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { onAnswer(true) }.padding(top = 4.dp, bottom = 4.dp))
            }
        }
    }
}

/** A fix picked on the map, zoomed right in (MapPick): where it is, and once the box has said,
 *  every fix a delete would take ([run]). [state]: 0 asking the box, 1 ready, 2 the box did not
 *  answer, 3 deleting. */
private data class FixPick(val ts: Long, val x: Double, val y: Double, val phone: Boolean,
                           val run: LongArray? = null, val state: Int = 0)

/** The picked fix under the map: what a delete takes, then [ DELETE ] and [ CANCEL ]. */
@Composable
private fun FixPickCard(f: FixPick, clock: (Long) -> String, onDelete: () -> Unit, onRetry: () -> Unit, onCancel: () -> Unit) {
    Column(Modifier.fillMaxWidth().padding(vertical = 6.dp).border(1.dp, Warning, RectangleShape).padding(10.dp)) {
        val text = when (f.state) {
            0 -> "The fix at ${clock(f.ts)}. Asking the box which fixes are at this spot…"
            2 -> "The fix at ${clock(f.ts)}. The box did not answer, nothing was deleted."
            3 -> MapPick.describe(f.ts, f.run, clock) + " Deleting…"
            else -> MapPick.describe(f.ts, f.run, clock) + " Delete " + (if ((f.run?.size ?: 1) > 1) "them" else "it") + " for good?"
        }
        Text(text, color = GhostText, style = MaterialTheme.typography.labelMedium)
        Row(Modifier.padding(top = 8.dp)) {
            if (f.state == 1) Text("[ DELETE ]", color = Warning, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable { onDelete() }.padding(end = 16.dp, top = 4.dp, bottom = 4.dp))
            if (f.state == 2) Text("[ TRY AGAIN ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable { onRetry() }.padding(end = 16.dp, top = 4.dp, bottom = 4.dp))
            if (f.state != 3) Text("[ CANCEL ]", color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable { onCancel() }.padding(top = 4.dp, bottom = 4.dp))
        }
    }
}

/** The app's lock teardown: the map forgets the box's data it kept for a quick reopen. */
fun clearMapMemory() {
    MapMemory.cells = emptyList(); MapMemory.labels = emptyList(); MapMemory.tracks = null; MapMemory.newest = null
    MapMemory.questions = emptyMap()
}

/**
 * WHERE THE MAP WAS LAST LOOKING (centre and zoom), kept on the phone: the map reopens there, not
 * on a guess, so the tiles it needs are the ones already on disk. Sealed like the trail
 * ([com.localghost.app.sync.TrailKeys]): where a person looks at a map says where they have been,
 * so it opens only while the app is unlocked, and a locked app reopens on the default view.
 */
private object MapCamera {
    private const val PREFS = "lg_map_camera"
    class View(val cx: Double, val cy: Double, val zoom: Float)

    fun load(ctx: android.content.Context): View? {
        val p = ctx.getSharedPreferences(PREFS, android.content.Context.MODE_PRIVATE)
        val v = if (p.contains("cx")) {
            // kept in the clear before 30 Sep 2026: read once, then gone (sealed at the next save)
            val old = View(Double.fromBits(p.getLong("cx", 0)), Double.fromBits(p.getLong("cy", 0)), p.getFloat("zoom", 1f))
            p.edit().remove("cx").remove("cy").remove("zoom").apply()
            old
        } else {
            val text = com.localghost.app.sync.TrailKeys.opener()?.open(p.getString("sealed", "") ?: "") ?: return null
            val f = text.split(' ')
            val cx = f.getOrNull(0)?.toDoubleOrNull() ?: return null
            val cy = f.getOrNull(1)?.toDoubleOrNull() ?: return null
            val zoom = f.getOrNull(2)?.toFloatOrNull() ?: return null
            View(cx, cy, zoom)
        }
        return if (v.cx.isFinite() && v.cy.isFinite() && v.cx in 0.0..WORLD_UNITS && v.cy in 0.0..WORLD_UNITS && v.zoom in 1f..MapPick.MAX_ZOOM) v else null
    }

    fun save(ctx: android.content.Context, cx: Double, cy: Double, zoom: Float) {
        val pub = com.localghost.app.sync.TrailKeys.publicKey(ctx)
        val e = ctx.getSharedPreferences(PREFS, android.content.Context.MODE_PRIVATE).edit()
        if (pub != null) e.putString("sealed", com.localghost.app.sync.TrailSeal.seal(pub, "$cx $cy $zoom")) else e.remove("sealed")
        e.apply()
    }
}

/** One day's movement in map units (Double , it is stroked in screen space, per vertex, so it can
 *  keep a real 2.5px width at any zoom; framed already Douglas-Peucker'd it, so a day is tens to a
 *  few hundred points, never the half-million the landmass is). bbox for culling. [times] is a
 *  clock per vertex when the box supplied one (empty otherwise); [phone] marks the part of a day
 *  the phone holds and the box has not seen yet (the spool waiting for a sync, or no box at all). */
private class Track(val day: String, val xs: DoubleArray, val ys: DoubleArray, val times: LongArray, val distanceM: Double, val phone: Boolean,
                    val minX: Double, val minY: Double, val maxX: Double, val maxY: Double, val glitches: Int = 0, val line: String = "",
                    val alts: IntArray = IntArray(0), val climbM: Double = 0.0, val highM: Double = 0.0) {
    val n: Int get() = xs.size
    val hasTimes: Boolean get() = times.size == xs.size && xs.isNotEmpty()
    /** the ground's height under each vertex (the box's elevation tiles), Int.MIN_VALUE where unknown */
    val hasAlts: Boolean get() = alts.size == xs.size && xs.isNotEmpty()
}

private fun trackOf(day: String, lat: DoubleArray, lon: DoubleArray, times: LongArray, distanceM: Double, phone: Boolean, glitches: Int = 0, line: String = "",
                    alts: IntArray = IntArray(0), climbM: Double = 0.0, highM: Double = 0.0): Track {
    val xs = DoubleArray(lat.size); val ys = DoubleArray(lat.size)
    var minX = Double.MAX_VALUE; var minY = Double.MAX_VALUE; var maxX = -Double.MAX_VALUE; var maxY = -Double.MAX_VALUE
    for (i in lat.indices) {
        val x = mercXD(lon[i]); val y = mercYD(lat[i])
        xs[i] = x; ys[i] = y
        if (x < minX) minX = x; if (x > maxX) maxX = x
        if (y < minY) minY = y; if (y > maxY) maxY = y
    }
    return Track(day, xs, ys, times, distanceM, phone, minX, minY, maxX, maxY, glitches, line, alts, climbM, highM)
}

/** THE DAY ROUTE projected once: each move's path in map units, each stay's centre. The box told
 *  the day as stays and moves (internal/dayroute); a walk's path runs along the streets where the
 *  box has them, a ride's between its fixes. */
private class RouteMoveXY(val m: BoxClient.RouteMove, val xs: DoubleArray, val ys: DoubleArray)
private class RouteStayXY(val s: BoxClient.RouteStay, val x: Double, val y: Double)
private class RouteXY(val day: String, val moves: List<RouteMoveXY>, val stays: List<RouteStayXY>, val r: BoxClient.DayRoute)

private fun routeOf(r: BoxClient.DayRoute): RouteXY = RouteXY(r.day,
    r.moves.map { m -> RouteMoveXY(m, DoubleArray(m.lat.size) { mercXD(m.lon[it]) }, DoubleArray(m.lat.size) { mercYD(m.lat[it]) }) },
    r.stays.map { s -> RouteStayXY(s, mercXD(s.lon), mercYD(s.lat)) }, r)

/** "08:45–09:15" or, for a stay that runs from the first fix of the day, "until 08:00". */
private fun span(from: Long, to: Long): String = if (from == to) clock(from) else clock(from) + "–" + clock(to)
private val MapRide = androidx.compose.ui.graphics.Color(0xFF7FB2E5)

private fun trackOf(pts: List<Pair<Double, Double>>): Track =
    trackOf("", DoubleArray(pts.size) { pts[it].first }, DoubleArray(pts.size) { pts[it].second }, LongArray(0), 0.0, false)

private val dayKeyFmt = java.text.SimpleDateFormat("yyyy-MM-dd", java.util.Locale.US).apply { timeZone = java.util.TimeZone.getTimeZone("UTC") }
private fun dayKeyOf(ts: Long): String = dayKeyFmt.format(java.util.Date(ts * 1000))

/** Distance over the phone's own points, the box's rule (jitter under 15m not counted). */
private fun distanceOf(pts: List<com.localghost.app.sync.LocationLog.Point>): Double {
    var total = 0.0
    val out = FloatArray(1)
    for (i in 1 until pts.size) {
        android.location.Location.distanceBetween(pts[i - 1].lat, pts[i - 1].lon, pts[i].lat, pts[i].lon, out)
        if (out[0] >= 15f) total += out[0]
    }
    return total
}

/** The zoom at which the view's short side spans about 2×[radiusKm] at [lat] (Mercator stretches
 *  the north-south scale by 1/cos(lat), so the same kilometres are more map units up north). */
private fun zoomForRadiusKm(radiusKm: Double, lat: Double): Float {
    val degLat = 2 * radiusKm / 111.0
    val units = degLat * (WORLD_UNITS / 360.0) / kotlin.math.cos(lat * PI / 180).coerceAtLeast(0.2)
    return (WORLD_UNITS / units).toFloat().coerceIn(1f, MapPick.MAX_ZOOM)
}

/** The camera never leaves the map: zoom stops where the world fills the view's short side, and
 *  the centre is held so no edge of the world comes inside the screen (when the whole world is
 *  narrower than the view on an axis, it sits centred on that axis). */
private fun clampCamera(cx: Double, cy: Double, zoom: Float, viewW: Float, viewH: Float): Triple<Double, Double, Float> {
    val z = zoom.coerceIn(1f, MapPick.MAX_ZOOM)
    if (viewW <= 0f || viewH <= 0f) return Triple(cx, cy, z)
    val pxz = (minOf(viewW, viewH) / WORLD) * z
    val halfW = viewW / 2.0 / pxz; val halfH = viewH / 2.0 / pxz
    val x = if (halfW >= WORLD_UNITS / 2) WORLD_UNITS / 2 else cx.coerceIn(halfW, WORLD_UNITS - halfW)
    val y = if (halfH >= WORLD_UNITS / 2) WORLD_UNITS / 2 else cy.coerceIn(halfH, WORLD_UNITS - halfH)
    return Triple(x, y, z)
}

/** "today", "yesterday", else "Thu 18 Sep", for a UTC day key. */
private fun dayLabel(day: String, todayKey: String, yesterdayKey: String): String = when (day) {
    todayKey -> "today"
    yesterdayKey -> "yesterday"
    else -> runCatching {
        java.text.SimpleDateFormat("EEE d MMM", java.util.Locale.US).format(dayKeyFmt.parse(day)!!)
    }.getOrDefault(day)
}

private fun km(m: Double): String = if (m < 950) "${m.toInt()} m" else "%.1f km".format(java.util.Locale.US, m / 1000)

private val clockFmt = java.text.SimpleDateFormat("HH:mm", java.util.Locale.US)
private fun clock(ts: Long): String = clockFmt.format(java.util.Date(ts * 1000))

private fun ago(sec: Long): String = when {
    sec < 90 -> "just now"
    sec < 3600 -> "${sec / 60} min ago"
    sec < 2 * 86400 -> "${sec / 3600} h ago"
    else -> "${sec / 86400} days ago"
}

/** One point of a day for the scrubber: map units and the clock (0 when the box gave none). */
private class TP(val x: Double, val y: Double, val ts: Long, val alt: Int = Int.MIN_VALUE)

private fun dayPoints(dayTracks: List<Track>): List<TP> {
    val out = ArrayList<TP>()
    for (t in dayTracks.sortedBy { if (it.phone) 1 else 0 }) for (i in 0 until t.n) out.add(TP(t.xs[i], t.ys[i], if (t.hasTimes) t.times[i] else 0L, if (t.hasAlts) t.alts[i] else Int.MIN_VALUE))
    return out
}

/**
 * THE PHONE'S OWN PART OF THE TRAIL. The box draws what it has been sent; the phone keeps the last
 * two days itself (LocationLog.recent), so today is on the map before any sync and without any
 * box. Per UTC day: the points the box's track does not reach yet (after its last clock, or all of
 * them when the box has none) become one more track, marked phone, drawn as the continuation.
 */
private fun phoneTracks(ctx: android.content.Context, box: List<Track>): List<Track> {
    val raw = com.localghost.app.sync.LocationLog.recent(ctx)
    if (raw.isEmpty()) return emptyList()
    // The glitch rules run over the whole two days at once (a spike at midnight is judged by its
    // neighbours in the other day), then the survivors are split per day. The count of what fell
    // is per day too, from the raw points, so the panel can say "2 glitches ignored" for the right day.
    val cleaned = com.localghost.app.sync.TrailClean.clean(raw)
    val keptByDay = cleaned.kept.groupBy { dayKeyOf(it.ts) }
    val out = ArrayList<Track>()
    for ((day, rawPts) in raw.groupBy { dayKeyOf(it.ts) }) {
        val pts = keptByDay[day] ?: emptyList()
        val have = box.firstOrNull { it.day == day }
        val lastBox = if (have != null && have.hasTimes) have.times.last() else if (have != null) Long.MAX_VALUE else 0L
        val mine = pts.filter { it.ts > lastBox }.sortedBy { it.ts }
        val glitches = rawPts.count { it.ts > lastBox } - mine.size
        if (mine.isEmpty()) continue
        out.add(trackOf(day, DoubleArray(mine.size) { mine[it].lat }, DoubleArray(mine.size) { mine[it].lon },
            LongArray(mine.size) { mine[it].ts }, distanceOf(mine), phone = true, glitches = glitches))
    }
    return out
}

@Composable
fun MapScreen(openDay: String = "", onDayShown: () -> Unit = {}) {
    val ctx = LocalContext.current
    val density = LocalDensity.current.density
    var world by remember { mutableStateOf<World?>(null) }
    var worldNote by remember { mutableStateOf("") }
    var picked by remember { mutableStateOf<BoxClient.GeoCell?>(null) }
    var viewer by remember { mutableStateOf<String?>(null) } // hash open full-screen
    var tracks by remember { mutableStateOf<List<Track>>(MapMemory.tracks ?: emptyList()) }
    // the box's "were you there?" per day (framed/questions.go), and the one being answered
    var questions by remember { mutableStateOf(MapMemory.questions) }
    var answering by remember { mutableStateOf<com.localghost.app.net.TrailQuestion?>(null) }
    // which of the lit day's questions the card under the map shows (a "?" on the map picks one)
    var askAt by remember { mutableIntStateOf(0) }
    // ONE FIX, PICKED: zoomed right in, a tap on a fix of the lit day offers to delete it (MapPick)
    var fixPick by remember { mutableStateOf<FixPick?>(null) }
    var fixNote by remember { mutableStateOf("") }
    // THE TRAIL PANEL: which day is lit, and where along it the scrubber sits (0..1).
    var trailOpen by remember { mutableStateOf(false) }
    // ONE DAY AT A TIME: the map draws the newest day (today when there is one) and nothing else;
    // ‹ and › step back and forward through the days, the strip picks one, "all days" lays every
    // day of the last sixty over the map as it used to. Eleven days of lines at once was a knot.
    var trailDay by remember { mutableStateOf<String?>(null) }
    var showAll by remember { mutableStateOf(false) }
    var frameTick by remember { mutableIntStateOf(0) } // bumped by a pick or a step: frame that day
    var scrub by remember { mutableStateOf(1f) }
    // LIVE WHILE THE MAP IS OPEN: the quarter-hour fix is for the trail; with the map on screen the
    // phone's position is asked for every few seconds (LocationLog.follow), so the dot is where the
    // phone is now and its circle is this fix's own accuracy, not a quarter of an hour's. Stopped
    // when the map goes or the app leaves the screen, started again when it is back.
    var liveFix by remember { mutableStateOf<com.localghost.app.sync.LocationLog.Point?>(null) }
    val lifecycleOwner = androidx.lifecycle.compose.LocalLifecycleOwner.current
    DisposableEffect(lifecycleOwner) {
        var stop: (() -> Unit)? = null
        val observer = androidx.lifecycle.LifecycleEventObserver { _, event ->
            when (event) {
                androidx.lifecycle.Lifecycle.Event.ON_RESUME -> if (stop == null) stop = com.localghost.app.sync.LocationLog.follow(ctx) { liveFix = it }
                androidx.lifecycle.Lifecycle.Event.ON_PAUSE -> { stop?.invoke(); stop = null }
                else -> {}
            }
        }
        lifecycleOwner.lifecycle.addObserver(observer)
        onDispose { lifecycleOwner.lifecycle.removeObserver(observer); stop?.invoke(); stop = null }
    }
    val storedFix = remember(tracks) { com.localghost.app.sync.LocationLog.newest(ctx) }
    val lastFix = liveFix ?: storedFix
    val nowSec = remember(tracks) { System.currentTimeMillis() / 1000 }
    val todayKey = remember(nowSec) { dayKeyOf(nowSec) }
    val yesterdayKey = remember(nowSec) { dayKeyOf(nowSec - 86400) }
    // One row per day, newest first: the box's track and the phone's continuation added up.
    val days = remember(tracks) {
        tracks.groupBy { it.day }.entries.filter { it.key.isNotEmpty() }
            .map { (d, ts) -> Triple(d, ts.sumOf { it.distanceM }, ts) }
            .sortedByDescending { it.first }
    }
    // the newest day is the one shown until another is picked (and again if the picked one is gone)
    LaunchedEffect(days) {
        if (days.isNotEmpty() && days.none { it.first == trailDay }) trailDay = days.first().first
    }
    // a notification's day ("your week in frames"): lit and framed once the days are in, then let go
    LaunchedEffect(days, openDay) {
        if (openDay.isEmpty() || days.isEmpty()) return@LaunchedEffect
        if (days.any { it.first == openDay }) {
            trailDay = openDay
            showAll = false
            scrub = 1f
            frameTick++
        }
        onDayShown()
    }
    fun stepDay(older: Boolean) {
        val i = days.indexOfFirst { it.first == trailDay }
        val j = if (i < 0) 0 else if (older) i + 1 else i - 1
        if (j in days.indices) { trailDay = days[j].first; showAll = false; scrub = 1f; frameTick++ }
    }
    // The lit day as one time-ordered list of points (the box's line, then the phone's continuation).
    val dayPts = remember(trailDay, tracks) { trailDay?.let { d -> dayPoints(tracks.filter { it.day == d }) } ?: emptyList() }
    // THE DAY ROUTE of the lit day: fetched when a day is picked (and again when the tracks refresh,
    // since today's route grows as fixes land); null while loading, on a box without routes, or
    // for a day the box has not told. Drawn over the raw track; listed under the strip.
    var route by remember { mutableStateOf<RouteXY?>(null) }
    LaunchedEffect(trailDay, tracks) {
        val d = trailDay
        if (d == null) { route = null; return@LaunchedEffect }
        if (route?.day != d) route = null
        val r = BoxClient.dayRoute(ctx, d)
        route = if (r != null && r.day == d) routeOf(r) else null
    }
    val scrubAt = remember(dayPts, scrub) {
        if (dayPts.isEmpty()) null else dayPts[(scrub * (dayPts.size - 1)).toInt().coerceIn(0, dayPts.size - 1)]
    }
    // THE COAST AT FULL DETAIL: the box's one-degree land tiles (LandTiles.kt), the index loaded
    // with the world, a tile fetched only when zoomed in over its cell. tileTick bumps when one
    // lands, which is what redraws the canvas.
    var tileIndex by remember { mutableStateOf<ByteArray?>(null) }
    val tileCache = remember { LandTileCache() }
    var tileTick by remember { mutableIntStateOf(0) }
    // THE ROADS (RoadTiles.kt): the same shape as the coast, two grids, a tile fetched only when
    // zoomed in over its cell; major roads from the coast's zoom, streets from ten times closer.
    var roadIndex by remember { mutableStateOf<RoadTileGeom.Index?>(null) }
    val roadCache = remember { RoadTileCache() }
    val mapScope = rememberCoroutineScope()
    // the days drawn, measured, told and asked about again, after a delete on the box
    suspend fun reloadTracks() {
        val batch = BoxClient.geoDayTracks(ctx, 60) ?: return
        val loaded = batch.filter { it.n >= 2 }.map { t -> trackOf(t.day, t.lat, t.lon, t.times, t.distanceM, phone = false, glitches = t.glitches, line = t.line,
            alts = t.alts, climbM = t.climbM, highM = t.highM) }
        tracks = loaded + phoneTracks(ctx, loaded)
        MapMemory.tracks = tracks
        questions = batch.filter { it.questions.isNotEmpty() }.associate { it.day to it.questions }
        MapMemory.questions = questions
    }
    // A FIX PICKED: ask the box (dry) which fixes a delete takes, so the card can say before the
    // person decides. A fix the phone has not sent yet goes to the box first, or the box would not
    // know it (and would get it later, after the delete).
    fun askFix(f: FixPick) {
        fixPick = f.copy(state = 0, run = null)
        mapScope.launch {
            if (f.phone) withContext(Dispatchers.IO) { runCatching { com.localghost.app.sync.LocationLog.flush(ctx) } }
            val r = withContext(Dispatchers.IO) { BoxClient.trailForget(ctx, f.ts, dry = true) }
            if (fixPick?.ts == f.ts) fixPick = f.copy(run = r?.ts, state = if (r != null && r.ts.isNotEmpty()) 1 else 2)
        }
    }
    fun deleteFix(f: FixPick) {
        fixPick = f.copy(state = 3)
        mapScope.launch {
            val r = withContext(Dispatchers.IO) { BoxClient.trailForget(ctx, f.ts, dry = false) }
            if (r == null) { if (fixPick?.ts == f.ts) fixPick = f.copy(state = 2); return@launch }
            withContext(Dispatchers.IO) { com.localghost.app.sync.LocationLog.forget(ctx, r.ts.toSet() + f.ts) }
            fixPick = null
            val n = r.ts.size
            fixNote = "deleted $n fix${if (n == 1) "" else "es"} , the day is drawn again without " + (if (n == 1) "it" else "them")
            reloadTracks()
        }
    }
    LaunchedEffect(trailDay) { fixPick = null; askAt = 0 }
    LaunchedEffect(fixNote) { if (fixNote.isNotEmpty()) { kotlinx.coroutines.delay(5000); fixNote = "" } }
    var loadNote by remember { mutableStateOf("loading…") }
    var cells by remember { mutableStateOf(MapMemory.cells) }
    var level by remember { mutableStateOf(3) }
    var newest by remember { mutableStateOf(MapMemory.newest) }
    // Projected once per cells change , 800 points at most, but it keeps the draw lambda to
    // multiply-adds and nothing else.
    val dots = remember(cells) { cells.map { Dot(mercXD(it.lon), mercYD(it.lat), it) } }

    LaunchedEffect(Unit) {
        // OPEN FROM THE PHONE, THEN ASK THE BOX. Everything the map needs to draw is on the phone
        // after the first open (the world cut, the coast and road indexes, the tiles, and in memory
        // the last view's dots and days), so it is drawn from there at once. The box is asked
        // afterwards, each part on its own, and only what changed is swapped in. It used to be
        // one line of round trips (newest photo, cut list, two world revalidations, two indexes,
        // sixty days of tracks) before the first tile could draw.
        launch {
            val li = withContext(Dispatchers.IO) { BoxClient.landTileIndexOnPhone(ctx) }
            val ri = withContext(Dispatchers.IO) { RoadTileGeom.index(BoxClient.roadTileIndexOnPhone(ctx)) }
            if (tileIndex == null) tileIndex = li
            if (roadIndex == null) roadIndex = ri
            BoxClient.landTileIndex(ctx)?.let { if (!it.contentEquals(tileIndex)) tileIndex = it }
            RoadTileGeom.index(BoxClient.roadTileIndex(ctx))?.let { roadIndex = it }
        }
        launch {
            BoxClient.newestGeoFrame(ctx)?.let { newest = it; MapMemory.newest = it }
        }
        launch {
            // DAY TRACKS, one round trip. /v1/geo/tracks hands back the newest sixty days of polylines
            // (with a clock per vertex and the day's distance, from boxes that write them) in a single
            // answer; a box that predates it (null) gets the old days-then-one-per-day walk.
            val batch = BoxClient.geoDayTracks(ctx, 60)
            val loaded = ArrayList<Track>()
            if (batch != null) {
                for (t in batch) if (t.n >= 2) loaded.add(trackOf(t.day, t.lat, t.lon, t.times, t.distanceM, phone = false, glitches = t.glitches, line = t.line,
                    alts = t.alts, climbM = t.climbM, highM = t.highM))
                questions = batch.filter { it.questions.isNotEmpty() }.associate { it.day to it.questions }
                MapMemory.questions = questions
            } else {
                val days = BoxClient.geoDays(ctx, 14) ?: emptyList()
                for (d in days) {
                    val pts = BoxClient.geoDayTrack(ctx, d) ?: continue
                    if (pts.size >= 2) loaded.add(trackOf(pts).let { trackOf(d, DoubleArray(pts.size) { pts[it].first }, DoubleArray(pts.size) { pts[it].second }, LongArray(0), 0.0, false) })
                }
            }
            if (batch == null && loaded.isEmpty() && MapMemory.tracks != null) return@launch // box away: keep what is drawn
            tracks = loaded + phoneTracks(ctx, loaded)
            MapMemory.tracks = tracks
        }
        // THE WORLD, SMALL FIRST. The box lists its landmass cuts (/v1/geo/world/index): open on
        // the smallest (a 110m world is under a megabyte, scanned in a blink), draw it, then load
        // the largest and swap it in once it is ready , the coastline sharpens under your thumb
        // instead of the screen waiting on 24MB. Everything off the main thread; the dots and the
        // camera never wait for landmass at all. A box that predates the index (null) or has only
        // the plain world.geojson gets the single-file path it always had. With a cut already on
        // the phone, the largest one there is drawn first and the box only revalidates it.
        fun note(w: World?, label: String) = if (w == null) "no landmass file on the box"
            else "landmass $label ${w.levels[2].size} rings · ${w.vertices[2] / 1000}k/${w.vertices[1] / 1000}k/${w.vertices[0] / 1000}k pts by zoom"
        var drawn: Pair<String, String>? = null // the cut on screen and its ETag
        suspend fun show(res: String, file: java.io.File?, etag: String) {
            if (file == null || drawn == (res to etag)) return
            val w = withContext(Dispatchers.Default) { WorldRings.load(ctx, file, etag, res.ifEmpty { "default" }) } ?: return
            world = w; worldNote = note(w, res); drawn = res to etag
        }
        val known = BoxClient.worldIndexOnPhone(ctx)
        val onPhone = (known ?: emptyList()).sortedByDescending { it.bytes }.map { it.res } + ""
        for (res in onPhone) {
            val (file, etag) = withContext(Dispatchers.IO) { BoxClient.worldGeoJsonOnPhone(ctx, res) }
            if (file != null) { show(res, file, etag); break }
        }
        val listed = BoxClient.worldIndex(ctx)
        // no answer (the box away, or one without the list) and a world already drawn from the
        // phone: keep it, rather than swap in the plain cut
        if (listed == null && world != null) return@LaunchedEffect
        val cuts = listed ?: emptyList()
        if (cuts.isEmpty()) {
            val (file, etag) = BoxClient.worldGeoJsonFile(ctx, "")
            show("", file, etag)
            if (world == null) worldNote = note(null, "")
        } else {
            val small = cuts.minByOrNull { it.bytes } ?: cuts[0]
            val big = cuts.maxByOrNull { it.bytes } ?: small
            if (world == null && big.res != small.res) {
                val (file, etag) = BoxClient.worldGeoJsonFile(ctx, small.res)
                show(small.res, file, etag)
            }
            val (file, etag) = BoxClient.worldGeoJsonFile(ctx, big.res)
            show(big.res, file, etag)
        }
    }
    // One reusable Path for the per-frame track strokes (reset per track, never reallocated).
    val trackPath = remember { androidx.compose.ui.graphics.Path() }

    // Camera: centre in map units + zoom (screen px per map unit = min(w,h)/WORLD * zoom).
    // REOPENS WHERE IT WAS LEFT (MapCamera): the last view's tiles are the ones on the phone
    val lastView = remember { MapCamera.load(ctx) }
    var cx by remember { mutableStateOf(lastView?.cx ?: (WORLD_UNITS / 2)) }
    var cy by remember { mutableStateOf(lastView?.cy ?: (WORLD_UNITS / 2)) }
    var zoom by remember { mutableStateOf(lastView?.zoom ?: 1f) }
    var worldFallback by remember { mutableStateOf(lastView != null) }
    // OPENS WHERE YOU ARE, about a hundred kilometres around , like any map on a phone. The
    // phone's last fix is known at once (prefs), so the first frame is already here; without a
    // fix ever taken, the newest photo at the same span; without either, the world.
    var openerDone by remember { mutableStateOf(lastView != null) }
    val startFix = remember { com.localghost.app.sync.LocationLog.newest(ctx) }
    LaunchedEffect(Unit) {
        val f = startFix ?: return@LaunchedEffect
        if (!openerDone && f.lat.isFinite() && f.lon.isFinite() && f.lat > -90.0 && f.lat < 90.0 && f.lon >= -180.0 && f.lon <= 180.0) {
            openerDone = true
            worldFallback = true // placed on purpose: the never-blank rule stays out of it
            cx = mercXD(f.lon); cy = mercYD(f.lat)
            zoom = zoomForRadiusKm(100.0, f.lat)
        }
    }
    LaunchedEffect(newest, cells) {
        val nw = newest
        if (nw != null && !openerDone &&
            nw.lat.isFinite() && nw.lon.isFinite() &&
            nw.lat > -90.0 && nw.lat < 90.0 && nw.lon >= -180.0 && nw.lon <= 180.0) {
            openerDone = true
            cx = mercXD(nw.lon); cy = mercYD(nw.lat)
            zoom = zoomForRadiusKm(100.0, nw.lat)
        } else if (cells.isNotEmpty() && zoom <= 1f && !openerDone) {
            // Skew fallback: no newest endpoint , fit everything, like the map used to.
            val xs = cells.map { mercXD(it.lon) }; val ys = cells.map { mercYD(it.lat) }
            cx = (xs.min() + xs.max()) / 2.0; cy = (ys.min() + ys.max()) / 2.0
            val span = maxOf(xs.max() - xs.min(), ys.max() - ys.min(), 4.0)
            zoom = (WORLD_UNITS / span * 0.6).toFloat().coerceIn(1f, 400f)
        }
    }
    // Picking a day frames it: centre on its bbox, zoom to fit with a margin, and never further in
    // than a street , a day spent at one table is a dot, not a 250,000x view of a paving slab.
    LaunchedEffect(frameTick) {
        if (frameTick == 0) return@LaunchedEffect // the day shown at open does not move the camera
        val d = trailDay ?: return@LaunchedEffect
        val ts = tracks.filter { it.day == d }
        if (ts.isEmpty()) return@LaunchedEffect
        val minX = ts.minOf { it.minX }; val maxX = ts.maxOf { it.maxX }
        val minY = ts.minOf { it.minY }; val maxY = ts.maxOf { it.maxY }
        cx = (minX + maxX) / 2.0; cy = (minY + maxY) / 2.0
        val span = maxOf(maxX - minX, maxY - minY, 0.02)
        zoom = (WORLD_UNITS / span * 0.7).toFloat().coerceIn(1f, 40000f)
        openerDone = true
    }
    // Viewport size from layout, not from inside the draw pass (writing state during draw is a
    // redraw loop waiting to happen).
    var viewW by remember { mutableStateOf(1000f) }
    var viewH by remember { mutableStateOf(1000f) }
    // THE LEVEL-OF-DETAIL LOOP. Zoom decides the tier, the viewport decides the bbox, and postgres
    // does the aggregating. Debounced so a pinch does not fire twenty queries on its way to rest.
    // THE GOOGLE MAPS TRICKS: (1) MARGIN FETCH , 2x the viewport, so small pans cost nothing;
    // (2) STALE-WHILE-REVALIDATE , old points keep drawing until new ones arrive; (3) REFETCH ON
    // ESCAPE ONLY , a new request when the view leaves the fetched margin or zoom moves ~1.6x.
    var fetchedLatMin by remember { mutableStateOf(999.0) }
    var fetchedLatMax by remember { mutableStateOf(-999.0) }
    var fetchedLonMin by remember { mutableStateOf(999.0) }
    var fetchedLonMax by remember { mutableStateOf(-999.0) }
    var fetchedSpan by remember { mutableStateOf(0.0) }
    // THE NAMES: what the box's GeoNames rows call what is under the view , countries at world
    // zoom, then regions and capitals, cities, towns, villages as the view narrows. Fetched with
    // the same escape rule as the dots (below), ranked by the box, thinned on screen by a collision
    // pass at draw time. Projected once per fetch.
    class Label(val x: Double, val y: Double, val name: String, val kind: String)
    var labels by remember { mutableStateOf(MapMemory.labels.map { Label(mercXD(it.lon), mercYD(it.lat), it.name, it.kind) }) }
    LaunchedEffect(cx, cy, zoom, viewW, viewH) {
        kotlinx.coroutines.delay(160)
        MapCamera.save(ctx, cx, cy, zoom)
        val lvl = when {
            zoom < 20f -> 0
            zoom < 200f -> 1
            zoom < 3000f -> 2
            else -> 3
        }
        val pxz = (minOf(viewW, viewH) / WORLD).toDouble() * zoom
        val halfW = (viewW / 2.0) / pxz
        val halfH = (viewH / 2.0) / pxz
        val latTop = invMercY(cy - halfH); val latBot = invMercY(cy + halfH)
        val lonL = invMercX(cx - halfW); val lonR = invMercX(cx + halfW)
        val vLatMin = minOf(latTop, latBot); val vLatMax = maxOf(latTop, latBot)
        val vLonMin = minOf(lonL, lonR); val vLonMax = maxOf(lonL, lonR)
        val vSpan = maxOf(vLatMax - vLatMin, vLonMax - vLonMin)
        val inside = vLatMin >= fetchedLatMin && vLatMax <= fetchedLatMax &&
            vLonMin >= fetchedLonMin && vLonMax <= fetchedLonMax
        val zoomStable = fetchedSpan > 0 && vSpan > fetchedSpan / 3.2 && vSpan < fetchedSpan * 1.6
        if (inside && zoomStable && cells.isNotEmpty()) return@LaunchedEffect
        val mLat = (vLatMax - vLatMin) / 2.0
        val mLon = (vLonMax - vLonMin) / 2.0
        // GARBAGE IN -> THE WORLD, NOT ANTARCTICA: any non-finite or out-of-range corner means
        // the honest request is the whole world.
        var q0 = vLatMin - mLat; var q1 = vLatMax + mLat
        var q2 = vLonMin - mLon; var q3 = vLonMax + mLon
        val sane = q0.isFinite() && q1.isFinite() && q2.isFinite() && q3.isFinite() &&
            q0 >= -90.0 && q1 <= 90.0 && q2 >= -180.0 && q3 <= 180.0 && q0 < q1 && q2 < q3
        if (!sane) { q0 = -90.0; q1 = 90.0; q2 = -180.0; q3 = 180.0 }
        level = lvl
        // the names for this view, in parallel with the dots: how many depends on how much room
        // the screen has , a world view shows its forty countries, a town view every village
        val labelJob = launch {
            val want = when {
                vSpan > 40 -> 40
                vSpan > 8 -> 70
                else -> 120
            }
            val got = BoxClient.geoLabels(ctx, q0, q1, q2, q3, want) ?: return@launch
            labels = got.map { Label(mercXD(it.lon), mercYD(it.lat), it.name, it.kind) }
            MapMemory.labels = got
        }
        val lod = BoxClient.framesGeoLod(ctx, lvl, q0, q1, q2, q3)
        labelJob.join()
        if (lod != null) {
            fetchedLatMin = vLatMin - mLat; fetchedLatMax = vLatMax + mLat
            fetchedLonMin = vLonMin - mLon; fetchedLonMax = vLonMax + mLon
            fetchedSpan = vSpan
        }
        cells = when {
            lod != null && lod.isNotEmpty() -> lod
            lod != null && lvl > 0 -> lod // a genuinely empty local view is a real answer
            cells.isNotEmpty() -> cells
            else -> {
                // VERSION SKEW SHIELD , a new app against a box without the LOD endpoints must
                // still show a map. Slower, but dots beat blankness until the operator redeploys.
                (BoxClient.framesGeo(ctx) ?: emptyList()).map {
                    BoxClient.GeoCell(it.lat, it.lon, 1, it.hash, it.takenAt)
                }
            }
        }
        // NEVER-BLANK RULE , an empty LOCAL view zoomed-in falls back to the whole world once.
        // Only on a box without map tiles: with the coast and the roads drawn, a view with no
        // photos in it is still a map, and the opener put it where the phone is on purpose.
        if (cells.isEmpty() && zoom > 4f && !worldFallback && tileIndex == null && roadIndex == null) {
            worldFallback = true
            fetchedSpan = 0.0
            cx = WORLD_UNITS / 2; cy = WORLD_UNITS / 2; zoom = 1f
            return@LaunchedEffect
        }
        if (lod != null) MapMemory.cells = cells
        loadNote = when {
            cells.isEmpty() -> "no geotagged photos anywhere yet , they appear as photos with GPS sync"
            else -> cells.sumOf { it.n }.toString() + " photos · detail " + (lvl + 1) + "/4"
        }
    }
    // The names' paints: a country in capitals, wide and dim; a capital or city bright; a town
    // small; every one on a dark halo so it reads over land, coast and trail alike.
    class NamePaint(val fill: android.graphics.Paint, val halo: android.graphics.Paint)
    fun namePaint(sizeSp: Float, argb: Int, bold: Boolean, spacing: Float): NamePaint {
        val f = android.graphics.Paint().apply {
            color = argb; textSize = sizeSp * density; textAlign = android.graphics.Paint.Align.CENTER
            typeface = android.graphics.Typeface.create(android.graphics.Typeface.MONOSPACE, if (bold) android.graphics.Typeface.BOLD else android.graphics.Typeface.NORMAL)
            isAntiAlias = true; letterSpacing = spacing
        }
        val h = android.graphics.Paint(f).apply {
            color = android.graphics.Color.argb(0xE0, 0x0A, 0x16, 0x20); style = android.graphics.Paint.Style.STROKE; strokeWidth = 3f * density
        }
        return NamePaint(f, h)
    }
    val namePaints = remember {
        mapOf(
            "C" to namePaint(12f, android.graphics.Color.argb(0xFF, 0xA0, 0xA0, 0xA0), true, 0.25f),
            "R" to namePaint(10.5f, android.graphics.Color.argb(0xFF, 0x90, 0x90, 0x90), false, 0.15f),
            "X" to namePaint(12f, android.graphics.Color.argb(0xFF, 0xE0, 0xE0, 0xE0), true, 0f),
            "P" to namePaint(10.5f, android.graphics.Color.argb(0xFF, 0xC8, 0xC8, 0xC8), false, 0f),
        )
    }
    // ROAD PAINTS: a colour and a width in screen pixels per class; the casing (a darker, wider
    // stroke under the fill) is what makes a road read as a road on the land fill. Motorways
    // warm, primaries pale, streets a shade above the land, paths dashed.
    class RoadPaint(val fill: androidx.compose.ui.graphics.Color, val casing: androidx.compose.ui.graphics.Color, val width: Float, val dashed: Boolean)
    val roadPaints = remember {
        // Color(0xAARRGGBB) is Compose's top-level factory function, not the Color companion; a
        // local alias of the class name cannot be called
        val c = { argb: Long -> androidx.compose.ui.graphics.Color(argb) }
        arrayOf(
            null,
            RoadPaint(c(0xFFE8A24A), c(0xFF6B4A1A), 4.5f, false), // 1 motorway
            RoadPaint(c(0xFFE0B36A), c(0xFF5E4A22), 4f, false),   // 2 trunk
            RoadPaint(c(0xFFD8D0A0), c(0xFF4F4A30), 3.5f, false), // 3 primary
            RoadPaint(c(0xFFC4C4B4), c(0xFF44443A), 3f, false),   // 4 secondary
            RoadPaint(c(0xFFA8AAA0), c(0xFF3A3C36), 2.5f, false), // 5 tertiary
            RoadPaint(c(0xFF8C9088), c(0xFF30332E), 2f, false),   // 6 residential
            RoadPaint(c(0xFF6E736C), c(0xFF2A2C28), 1.5f, false), // 7 service
            RoadPaint(c(0xFF7A6A50), c(0xFF2A2420), 1.5f, true),  // 8 track
            RoadPaint(c(0xFF6A7A6A), c(0xFF243024), 1f, true),    // 9 path
        )
    }
    val roadNamePaint = remember {
        android.graphics.Paint().apply {
            color = android.graphics.Color.argb(0xFF, 0xE0, 0xE0, 0xD8); textSize = 10f * density
            typeface = android.graphics.Typeface.MONOSPACE; isAntiAlias = true
            textAlign = android.graphics.Paint.Align.CENTER
        }
    }
    val roadNameHalo = remember {
        android.graphics.Paint(roadNamePaint).apply {
            color = android.graphics.Color.argb(0xD0, 0x0A, 0x16, 0x20); style = android.graphics.Paint.Style.STROKE; strokeWidth = 3f * density
        }
    }
    // "you", left of nothing and right of the dot: its own paints, left-aligned.
    val youPaint = remember {
        android.graphics.Paint().apply {
            color = android.graphics.Color.argb(0xFF, 0xF0, 0xF0, 0xE8); textSize = 11f * density
            typeface = android.graphics.Typeface.MONOSPACE; isAntiAlias = true; isFakeBoldText = true
            textAlign = android.graphics.Paint.Align.LEFT
        }
    }
    val youHalo = remember {
        android.graphics.Paint(youPaint).apply {
            color = android.graphics.Color.argb(0xE0, 0x0A, 0x16, 0x20); style = android.graphics.Paint.Style.STROKE; strokeWidth = 3.5f * density
        }
    }
    // One Paint for every cluster label, not one per label per frame.
    val askPaint = remember {
        android.graphics.Paint().apply {
            color = android.graphics.Color.rgb(0xFF, 0x8A, 0x8A); textSize = 13f * density
            typeface = android.graphics.Typeface.create(android.graphics.Typeface.MONOSPACE, android.graphics.Typeface.BOLD)
            textAlign = android.graphics.Paint.Align.CENTER; isAntiAlias = true
        }
    }
    val labelPaint = remember {
        android.graphics.Paint().apply {
            color = android.graphics.Color.rgb(0x39, 0xFF, 0x14)
            textSize = 11f * density
            textAlign = android.graphics.Paint.Align.CENTER
            typeface = android.graphics.Typeface.MONOSPACE
            isAntiAlias = true
        }
    }
    Column(Modifier.fillMaxSize()) {
        Text("> MAP", color = TerminalGreen, style = MaterialTheme.typography.titleMedium,
            modifier = Modifier.padding(16.dp))
        Row {
            Text("[ where I am ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable {
                    // back to the phone, a town's worth around it; twice narrows to the streets
                    val f = liveFix ?: com.localghost.app.sync.LocationLog.newest(ctx)
                    if (f != null) {
                        val close = kotlin.math.abs(cx - mercXD(f.lon)) < 0.01 && kotlin.math.abs(cy - mercYD(f.lat)) < 0.01 && zoom >= zoomForRadiusKm(12.0, f.lat) * 0.9f
                        cx = mercXD(f.lon); cy = mercYD(f.lat)
                        zoom = zoomForRadiusKm(if (close) 1.5 else 12.0, f.lat)
                        worldFallback = true // a view on purpose is never "empty", never snaps back to the world
                    }
                }.padding(vertical = 4.dp))
            Spacer(Modifier.width(16.dp))
            Text("[ the world ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable {
                    // The hatch , whatever the camera got into, one tap is the whole world again.
                    cx = WORLD_UNITS / 2; cy = WORLD_UNITS / 2; zoom = 1f; worldFallback = false
                }.padding(vertical = 4.dp))
        }
        // The tiles the view needs, asked for as the camera settles on them (equal lists do not
        // re-fire, so a pan within the same cells costs nothing).
        val pxzNow = if (viewW > 0f && viewH > 0f) (minOf(viewW, viewH) / WORLD) * zoom else 0f
        val wantTiles = remember(tileIndex, cx, cy, pxzNow, viewW, viewH) {
            val idx = tileIndex
            if (idx == null || pxzNow < LandTileGeom.TILE_PXZ) emptyList()
            else LandTileGeom.cellsFor(invMercX(cx - viewW / 2.0 / pxzNow), invMercX(cx + viewW / 2.0 / pxzNow),
                invMercY(cy + viewH / 2.0 / pxzNow), invMercY(cy - viewH / 2.0 / pxzNow))
                .filter { idx[it].toInt() == LandTileGeom.COAST }
        }
        LaunchedEffect(wantTiles) { if (wantTiles.isNotEmpty()) tileCache.ensure(mapScope, ctx, wantTiles) { tileTick++ } }
        val wantRoads = remember(roadIndex, cx, cy, pxzNow, viewW, viewH) {
            val idx = roadIndex
            if (idx == null || pxzNow < RoadTileGeom.MAJOR_PXZ) emptyList()
            else {
                val lon0 = invMercX(cx - viewW / 2.0 / pxzNow); val lon1 = invMercX(cx + viewW / 2.0 / pxzNow)
                val latLo = invMercY(cy + viewH / 2.0 / pxzNow); val latHi = invMercY(cy - viewH / 2.0 / pxzNow)
                val major = RoadTileGeom.cellsFor(idx, 1, lon0, lon1, latLo, latHi)
                if (pxzNow >= RoadTileGeom.FINE_PXZ) major + RoadTileGeom.cellsFor(idx, 0, lon0, lon1, latLo, latHi) else major
            }
        }
        LaunchedEffect(wantRoads) { if (wantRoads.isNotEmpty()) roadCache.ensure(mapScope, ctx, wantRoads) { tileTick++ } }
        // The note says what the coast is doing, so "the map does not work" has a line to quote:
        // no index (the box has no tiles, or the phone never got the index), not zoomed in yet,
        // or tiles wanted / here / failed, with the last failure's reason.
        @Suppress("UNUSED_VARIABLE") val tilesTick = tileTick
        val coastNote = when {
            tileIndex == null -> " · coast: no tile index from the box"
            pxzNow < LandTileGeom.TILE_PXZ -> " · coast © OpenStreetMap contributors (zoom in for detail)"
            else -> {
                val here = wantTiles.count { tileCache.get(it) != null }
                " · coast © OpenStreetMap contributors · tiles ${wantTiles.size} wanted, $here here" +
                    (if (tileCache.failures > 0) ", ${tileCache.failures} failed: ${tileCache.lastError}" else "")
            }
        } + when {
            roadIndex == null -> " · roads: no tile index from the box"
            wantRoads.isEmpty() -> " · roads: index ok" + (if (pxzNow < RoadTileGeom.MAJOR_PXZ) " (zoom in)" else " (no tiles under the view)")
            else -> " · roads ${wantRoads.size} wanted, ${wantRoads.count { roadCache.get(it) != null }} here" +
                (if (roadCache.failures > 0) ", ${roadCache.failures} failed: ${roadCache.lastError}" else "")
        }
        // What the map says about itself: in DEBUG MODE (settings) everything , the photo count and
        // detail level, the landmass file's ring and point counts, the coast tiles' state , and
        // otherwise only what a person needs: nothing while it has photos to show, the one line
        // that says why when it has none.
        val debug = remember { com.localghost.app.settings.AppSettings.debugMode(ctx) }
        // YOU ARE HERE breathes: a slow pulse on the halo, so the eye finds it on a busy map
        val pulse by androidx.compose.animation.core.rememberInfiniteTransition(label = "you").animateFloat(
            initialValue = 0f, targetValue = 1f, label = "pulse",
            animationSpec = androidx.compose.animation.core.infiniteRepeatable(
                androidx.compose.animation.core.tween(1600, easing = androidx.compose.animation.core.LinearEasing),
                androidx.compose.animation.core.RepeatMode.Restart))
        val mapNote = if (debug) loadNote + (if (worldNote.isNotEmpty()) " · " + worldNote else "") + coastNote
            else if (cells.isEmpty()) loadNote else ""
        if (mapNote.isNotEmpty()) Text(mapNote,
            color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.padding(horizontal = 16.dp))
        Box(Modifier.weight(1f).fillMaxWidth().padding(12.dp).background(Void)) {
            // CAMERA SANITY , a NaN centre poisons every draw AND every gesture. Checked on each
            // composition; garbage snaps back to the world.
            if (!cx.isFinite() || !cy.isFinite() || !zoom.isFinite() || zoom <= 0f) {
                cx = WORLD_UNITS / 2; cy = WORLD_UNITS / 2; zoom = 1f; worldFallback = false
            }
            // clipToBounds: Compose does not clip a Canvas to its own box, and a filled continent
            // at street zoom is a rectangle the size of the screen , it painted over the title, the
            // note and the trail panel. Strokes never showed it; fills did.
            Canvas(Modifier.fillMaxSize().clipToBounds()
                .onSizeChanged { viewW = it.width.toFloat(); viewH = it.height.toFloat() }
                .pointerInput(Unit) {
                    detectTransformGestures { centroid, pan, gz, _ ->
                        if (!cx.isFinite() || !cy.isFinite() || !zoom.isFinite()) {
                            cx = WORLD_UNITS / 2; cy = WORLD_UNITS / 2; zoom = 1f
                        }
                        // zoom about the finger centroid, then pan , standard camera algebra;
                        // then the camera is held on the map (zoom-out stops at the world, no
                        // edge of it comes inside the screen)
                        val newZoom = (zoom * gz).coerceIn(1f, MapPick.MAX_ZOOM)
                        val sw = size.width.toDouble(); val sh = size.height.toDouble()
                        val scale = minOf(sw, sh) / WORLD_UNITS
                        val pxOld = scale * zoom; val pxNew = scale * newZoom
                        val wx = cx + (centroid.x - sw / 2) / pxOld
                        val wy = cy + (centroid.y - sh / 2) / pxOld
                        var ncx = wx - (centroid.x - sw / 2) / pxNew
                        var ncy = wy - (centroid.y - sh / 2) / pxNew
                        ncx -= pan.x / pxNew; ncy -= pan.y / pxNew
                        val (kx, ky, kz) = clampCamera(ncx, ncy, newZoom, sw.toFloat(), sh.toFloat())
                        cx = kx; cy = ky; zoom = kz
                        picked = null
                    }
                }
                .pointerInput(dots) {
                    detectTapGestures { tap ->
                        val sw = size.width.toDouble(); val sh = size.height.toDouble()
                        val pxz = (minOf(sw, sh) / WORLD_UNITS) * zoom
                        val tapR = MapPick.TAP_DP * density
                        val litDay = trailDay?.takeIf { !showAll }
                        // a "?" on the map: its question comes up under the map
                        val qs = litDay?.let { questions[it] } ?: emptyList()
                        val qi = qs.indices.minByOrNull { i ->
                            val px = (mercXD(qs[i].lon) - cx) * pxz + sw / 2 - tap.x
                            val py = (mercYD(qs[i].lat) - cy) * pxz + sh / 2 - tap.y
                            px * px + py * py
                        }
                        if (qi != null) {
                            val px = (mercXD(qs[qi].lon) - cx) * pxz + sw / 2 - tap.x
                            val py = (mercYD(qs[qi].lat) - cy) * pxz + sh / 2 - tap.y
                            if (px * px + py * py <= tapR * tapR) { askAt = qi; picked = null; return@detectTapGestures }
                        }
                        // ZOOMED RIGHT IN on the lit day: a tap on a fix picks it (MapPick)
                        if (litDay != null && MapPick.canPick(MapPick.metresPerPx(invMercY(cy), pxz))) {
                            var hit: FixPick? = null; var hitD = Double.MAX_VALUE
                            for (t in tracks) {
                                if (t.day != litDay || !t.hasTimes) continue
                                val i = MapPick.nearest(t.xs, t.ys, t.times, cx, cy, pxz, sw, sh, tap.x.toDouble(), tap.y.toDouble(), tapR)
                                if (i < 0) continue
                                val px = (t.xs[i] - cx) * pxz + sw / 2 - tap.x
                                val py = (t.ys[i] - cy) * pxz + sh / 2 - tap.y
                                val dd = px * px + py * py
                                if (dd < hitD) { hitD = dd; hit = FixPick(t.times[i], t.xs[i], t.ys[i], t.phone) }
                            }
                            if (hit != null) { picked = null; askFix(hit); return@detectTapGestures }
                        }
                        var best: BoxClient.GeoCell? = null; var bestD = 44.0 * 44.0
                        for (d in dots) {
                            val px = (d.x - cx) * pxz + sw / 2
                            val py = (d.y - cy) * pxz + sh / 2
                            val dd = (px - tap.x) * (px - tap.x) + (py - tap.y) * (py - tap.y)
                            if (dd < bestD) { bestD = dd; best = d.cell }
                        }
                        picked = best
                        // A CLUSTER tap dives in (centre it and zoom a tier); a single photo opens.
                        best?.let { b ->
                            if (b.n > 1) {
                                cx = mercXD(b.lon); cy = mercYD(b.lat)
                                zoom = (zoom * 6f).coerceAtMost(MapPick.MAX_ZOOM)
                            }
                        }
                    }
                }) {
                val sw = size.width; val sh = size.height
                @Suppress("UNUSED_VARIABLE") val tilesLanded = tileTick // read, so a landed tile redraws
                // THE SEA first: the canvas is water, the land is painted on it.
                drawRect(MapWater)
                val pxzD = (minOf(sw, sh) / WORLD).toDouble() * zoom
                val pxz = pxzD.toFloat()
                // Screen origin of map coordinate (0,0), in Double , the one subtraction that
                // must not happen in Float at street zoom.
                val ox = -cx * pxzD + sw / 2.0
                val oy = -cy * pxzD + sh / 2.0
                fun sx(x: Double) = (x * pxzD + ox).toFloat()
                fun sy(y: Double) = (y * pxzD + oy).toFloat()
                // Visible window in map units, with a margin, for bbox culling.
                val vx0 = cx - (sw / 2.0 + 32) / pxzD; val vx1 = cx + (sw / 2.0 + 32) / pxzD
                val vy0 = cy - (sh / 2.0 + 32) / pxzD; val vy1 = cy + (sh / 2.0 + 32) / pxzD
                // graticule every 15 degrees , the projection's skeleton, for the debug switch
                // only: a map is land, water and where you are, not a grid
                if (debug) {
                    var lon = -180.0
                    while (lon <= 180.0) {
                        val x = sx(mercXD(lon))
                        if (x in -2f..sw + 2f) drawLine(GhostBorder, Offset(x, 0f), Offset(x, sh), 1f)
                        lon += 15.0
                    }
                    var lat = -75.0
                    while (lat <= 75.0) {
                        val y = sy(mercYD(lat))
                        if (y in -2f..sh + 2f) drawLine(GhostBorder, Offset(0f, y), Offset(sw, y), 1f)
                        lat += 15.0
                    }
                }
                // PRE-BUILT PATHS UNDER A TRANSFORM. Each ring's Path is in map units relative to
                // its own origin; the canvas is translated to that origin's screen position
                // (computed in Double) and scaled by pxz. Stroke width is given in map units so
                // it stays ~1.5 screen px; past a few hundred px per unit a hairline (width 0,
                // always exactly one pixel, and the cheapest stroke Skia has) takes over, because
                // a stroke of 1e-6 map units has no float precision left to be a stroke with.
                fun drawRings(rings: List<Ring>, color: androidx.compose.ui.graphics.Color, widthPx: Float) {
                    val stroke = Stroke(width = if (pxz > 400f) 0f else widthPx / pxz)
                    for (r in rings) {
                        if (r.maxX < vx0 || r.minX > vx1 || r.maxY < vy0 || r.minY > vy1) continue
                        withTransform({
                            translate(sx(r.ox.toDouble()), sy(r.oy.toDouble()))
                            scale(pxz, pxz, pivot = Offset.Zero)
                        }) {
                            drawPath(r.path, color, style = stroke)
                        }
                    }
                }
                fun fillRings(rings: List<Ring>, color: androidx.compose.ui.graphics.Color) {
                    for (r in rings) {
                        if (r.maxX < vx0 || r.minX > vx1 || r.maxY < vy0 || r.minY > vy1) continue
                        withTransform({
                            translate(sx(r.ox.toDouble()), sy(r.oy.toDouble()))
                            scale(pxz, pxz, pivot = Offset.Zero)
                        }) { drawPath(r.path, color) }
                    }
                }
                // THE BASE: Natural Earth land filled over the sea, its outline the coast (and the
                // borders), at the detail level that is under a pixel of error right now.
                fun drawBase() {
                    world?.let { w ->
                        val lv = w.levels[World.levelFor(pxz)]
                        fillRings(lv, MapLand)
                        drawRings(lv, TerminalDim, 1.5f)
                    }
                }
                // THE DETAIL: zoomed in past what the base can show, every one-degree cell under the
                // view is drawn from the box's land tiles , sea left as sea, solid land filled, a
                // coast cell from its tile (the base, clipped to the cell, until the tile lands).
                val idx = tileIndex
                val tileCells = if (idx != null && pxz >= LandTileGeom.TILE_PXZ)
                    LandTileGeom.cellsFor(invMercX(vx0), invMercX(vx1), invMercY(vy1), invMercY(vy0)) else emptyList()
                if (tileCells.isEmpty()) drawBase() else {
                    val lv = LandTileGeom.levelFor(pxz)
                    val coastStroke = Stroke(width = if (pxz > 400f) 0f else 1.5f / pxz)
                    for (key in tileCells) {
                        val lon0 = key % LandTileGeom.COLS - 180.0; val lat0 = key / LandTileGeom.COLS - 90.0
                        val left = sx(mercXD(lon0)); val right = sx(mercXD(lon0 + 1))
                        val top = sy(mercYD(lat0 + 1)); val bottom = sy(mercYD(lat0))
                        when (idx!![key].toInt()) {
                            LandTileGeom.LAND -> drawRect(MapLand, Offset(left, top), androidx.compose.ui.geometry.Size(right - left, bottom - top))
                            LandTileGeom.COAST -> {
                                val t = tileCache.get(key)
                                if (t != null) {
                                    withTransform({
                                        translate(sx(t.ox), sy(t.oy))
                                        scale(pxz, pxz, pivot = Offset.Zero)
                                    }) {
                                        drawPath(t.fill[lv], MapLand)
                                        drawPath(t.coast[lv], TerminalDim, style = coastStroke)
                                    }
                                } else clipRect(left, top, right, bottom) { drawBase() }
                            }
                            else -> {} // sea
                        }
                    }
                }
                // THE ROADS, over the land and under the trails: for every road tile under the view
                // (major tiles always, street tiles from FINE_PXZ), the classes this zoom shows, each
                // class one Path per tile, casing first then fill, screen-space widths (given in map
                // units, so widthPx / pxz). Street names along their middle segment at NAME_PXZ.
                if (roadIndex != null && pxz >= RoadTileGeom.MAJOR_PXZ && wantRoads.isNotEmpty()) {
                    val maxCls = RoadTileGeom.maxClassFor(pxz)
                    val tiles = wantRoads.mapNotNull { k ->
                        // a street tile's major roads are already in the major tile: draw only
                        // classes above MajorMax from level 0 where a major tile covers the cell
                        roadCache.get(k)?.let { it to RoadTileGeom.levelOf(k) }
                    }
                    for (pass in 0..1) for (cls in 9 downTo 1) {
                        if (cls > maxCls) continue
                        val rp = roadPaints[cls] ?: continue
                        val w = if (pass == 0) rp.width + 2f else rp.width
                        val stroke = Stroke(width = w / pxz, cap = androidx.compose.ui.graphics.StrokeCap.Round, join = androidx.compose.ui.graphics.StrokeJoin.Round,
                            pathEffect = if (rp.dashed && pass == 1) PathEffect.dashPathEffect(floatArrayOf(6f / pxz, 4f / pxz)) else null)
                        for ((t, lvl) in tiles) {
                            if (lvl == 0 && cls <= 4) continue // the major tile already drew these
                            val path = t.paths[RoadTiles.levelFor(pxz, lvl)][cls] ?: continue
                            withTransform({
                                translate(sx(t.ox), sy(t.oy))
                                scale(pxz, pxz, pivot = Offset.Zero)
                            }) { drawPath(path, if (pass == 0) rp.casing else rp.fill, style = stroke) }
                        }
                    }
                    if (pxz >= RoadTileGeom.NAME_PXZ) {
                        // names: each named road once, on its middle segment, rotated to it, the
                        // biggest roads first, a claimed strip per name so they never cross
                        val nc = drawContext.canvas.nativeCanvas
                        val claimed = ArrayList<FloatArray>()
                        var drawn = 0
                        val minLenPx = 60f * density
                        outer@ for ((t, lvl) in tiles) {
                            if (lvl != 0) continue
                            for (nm in t.names) {
                                if (nm.cls > maxCls) continue
                                val ax = sx(t.ox + nm.x0); val ay = sy(t.oy + nm.y0)
                                val bx = sx(t.ox + nm.x1); val by = sy(t.oy + nm.y1)
                                val mx = (ax + bx) / 2; val my = (ay + by) / 2
                                if (mx < 0f || mx > sw || my < 0f || my > sh) continue
                                val textW = roadNamePaint.measureText(nm.name)
                                val roadPx = nm.lengthQ * 0.1 / RoadTileGeom.Q * 2.84 * pxz // quantised length → degrees → map units → px
                                if (roadPx < textW || roadPx < minLenPx) continue
                                val r = floatArrayOf(mx - textW / 2 - 6f, my - 12f * density, mx + textW / 2 + 6f, my + 12f * density)
                                if (claimed.any { it[0] < r[2] && r[0] < it[2] && it[1] < r[3] && r[1] < it[3] }) continue
                                claimed.add(r)
                                var ang = Math.toDegrees(kotlin.math.atan2((by - ay).toDouble(), (bx - ax).toDouble())).toFloat()
                                if (ang > 90f) ang -= 180f else if (ang < -90f) ang += 180f
                                nc.save()
                                nc.rotate(ang, mx, my)
                                nc.drawText(nm.name, mx, my - 4f * density, roadNameHalo)
                                nc.drawText(nm.name, mx, my - 4f * density, roadNamePaint)
                                nc.restore()
                                if (++drawn >= 80) break@outer
                            }
                        }
                    }
                }
                // day tracks , movement under the moments, drawn OVER the coastline and stroked in
                // SCREEN space per vertex, so the line stays 2.5px wide from the world view to the
                // street. Affordable because framed simplified each day already; culled by bbox.
                // The lit day (the trail panel's pick) is green and wider; the phone's own part of
                // a day, not yet on the box, is dashed with a dot per fix so the quarter-hour
                // rhythm shows; every other day is the dim thread it always was.
                val routeShown = route != null && route?.day == trailDay
                for (t in tracks) {
                    if (!showAll && t.day != trailDay) continue
                    if (t.maxX < vx0 || t.minX > vx1 || t.maxY < vy0 || t.minY > vy1) continue
                    // with the day's route on screen the raw line steps back to a thin thread
                    val lit = t.day.isNotEmpty() && t.day == trailDay && !routeShown
                    val colour = if (lit || t.phone) TerminalGreen else MapTrail
                    val width = if (lit) 3.5f else 2.5f
                    if (t.n >= 2) {
                        trackPath.reset()
                        trackPath.moveTo(sx(t.xs[0]), sy(t.ys[0]))
                        for (i in 1 until t.n) trackPath.lineTo(sx(t.xs[i]), sy(t.ys[i]))
                        // the halo first: a dark edge either side, so the line stands off the land
                        // fill and the coast strokes whatever colour is under it
                        drawPath(trackPath, MapWater, style = Stroke(width = width + 2.5f))
                        drawPath(trackPath, colour, style = Stroke(width = width,
                            pathEffect = if (t.phone) PathEffect.dashPathEffect(floatArrayOf(7f, 7f)) else null))
                    }
                    if (t.phone || lit) for (i in 0 until t.n) {
                        val x = sx(t.xs[i]); val y = sy(t.ys[i])
                        if (x < -8f || x > sw + 8f || y < -8f || y > sh + 8f) continue
                        drawCircle(colour, radius = if (t.phone) 3f else 2f, center = Offset(x, y))
                    }
                }
                // THE DAY ROUTE: the lit day as the box told it. Walks along the streets in green, a
                // dark halo under them; rides as dashed blue chords; each stay a ring with its name
                // and hours, drawn last so a name never hides under a line. A stay's name claims its
                // rectangle (and the place names below skip what the stays claimed): two stays a
                // street apart keep their rings, and the later name is left off rather than drawn
                // through the first.
                val claimedLabels = ArrayList<FloatArray>()
                route?.takeIf { routeShown }?.let { rt ->
                    for (mv in rt.moves) {
                        if (mv.xs.size < 2) continue
                        trackPath.reset()
                        trackPath.moveTo(sx(mv.xs[0]), sy(mv.ys[0]))
                        for (i in 1 until mv.xs.size) trackPath.lineTo(sx(mv.xs[i]), sy(mv.ys[i]))
                        val walk = mv.m.mode == "walk"
                        drawPath(trackPath, MapWater, style = Stroke(width = if (walk) 6.5f else 5f))
                        drawPath(trackPath, if (walk) TerminalGreen else MapRide, style = Stroke(width = if (walk) 4f else 2.5f,
                            pathEffect = if (walk) null else PathEffect.dashPathEffect(floatArrayOf(10f, 6f))))
                    }
                    val nc = drawContext.canvas.nativeCanvas
                    for (st in rt.stays) {
                        val x = sx(st.x); val y = sy(st.y)
                        if (x < -40f || x > sw + 40f || y < -40f || y > sh + 40f) continue
                        drawCircle(MapWater, radius = 9f, center = Offset(x, y))
                        drawCircle(GhostText, radius = 7f, center = Offset(x, y), style = Stroke(width = 2.5f))
                        drawCircle(if (st.s.photos > 0) TerminalGreen else GhostTextDim, radius = 3f, center = Offset(x, y))
                        if (pxz >= LandTileGeom.TILE_PXZ) {
                            val name = when {
                                st.s.name.isEmpty() -> span(st.s.from, st.s.to)
                                st.s.kind == "near" -> "near ${st.s.name} · ${span(st.s.from, st.s.to)}"
                                else -> "${st.s.name} · ${span(st.s.from, st.s.to)}"
                            }
                            val w = roadNamePaint.measureText(name) + 6f * density
                            val h = roadNamePaint.textSize + 4f * density
                            if (MapPick.claim(claimedLabels, floatArrayOf(x - w / 2, y - 12f - h, x + w / 2, y - 12f + 3f * density))) {
                                nc.drawText(name, x, y - 12f, roadNameHalo)
                                nc.drawText(name, x, y - 12f, roadNamePaint)
                            }
                        }
                    }
                }
                // WERE YOU THERE? on the map: a "?" where each of the lit day's questions points,
                // the one the card under the map shows ringed twice
                val litQs = trailDay?.takeIf { !showAll }?.let { d -> questions[d] } ?: emptyList()
                litQs.forEachIndexed { qi, q ->
                    val x = sx(mercXD(q.lon)); val y = sy(mercYD(q.lat))
                    if (x < -30f || x > sw + 30f || y < -30f || y > sh + 30f) return@forEachIndexed
                    val on = qi == askAt.coerceIn(0, litQs.size - 1)
                    drawCircle(MapWater, radius = 12f * density, center = Offset(x, y))
                    drawCircle(Warning, radius = 10f * density, center = Offset(x, y), style = Stroke(width = (if (on) 3f else 2f) * density))
                    if (on) drawCircle(Warning.copy(alpha = 0.5f), radius = 15f * density, center = Offset(x, y), style = Stroke(width = 1.5f * density))
                    drawContext.canvas.nativeCanvas.drawText("?", x, y + 4.5f * density, askPaint)
                }
                // THE FIXES, PICKABLE: zoomed right in on the lit day (MapPick), every fix with a
                // clock is a ring to tap; the picked one is red, with its clock
                val mppNow = MapPick.metresPerPx(invMercY(cy), pxzD)
                if (trailDay != null && !showAll && MapPick.canPick(mppNow)) {
                    for (t in tracks) {
                        if (t.day != trailDay || !t.hasTimes) continue
                        for (i in 0 until t.n) {
                            val x = sx(t.xs[i]); val y = sy(t.ys[i])
                            if (x < -20f || x > sw + 20f || y < -20f || y > sh + 20f) continue
                            drawCircle(MapWater, radius = 8f * density, center = Offset(x, y))
                            drawCircle(if (t.phone) TerminalGreen else GhostText, radius = 6f * density, center = Offset(x, y), style = Stroke(width = 2f * density))
                        }
                    }
                }
                fixPick?.let { f ->
                    val x = sx(f.x); val y = sy(f.y)
                    drawCircle(MapWater, radius = 14f * density, center = Offset(x, y))
                    drawCircle(Warning, radius = 12f * density, center = Offset(x, y), style = Stroke(width = 2.5f * density))
                    drawCircle(Warning, radius = 4f * density, center = Offset(x, y))
                    val nc = drawContext.canvas.nativeCanvas
                    nc.drawText(clock(f.ts), x, y - 18f * density, roadNameHalo)
                    nc.drawText(clock(f.ts), x, y - 18f * density, roadNamePaint)
                }
                // The scrubber's point on the lit day, with its clock.
                scrubAt?.takeIf { trailOpen }?.let { at ->
                    val x = sx(at.x); val y = sy(at.y)
                    drawCircle(GhostText, radius = 7f, center = Offset(x, y), style = Stroke(width = 2.5f))
                    if (at.ts > 0) drawContext.canvas.nativeCanvas.drawText(clock(at.ts), x, y - 12f, labelPaint)
                }
                // THE NAMES, ranked by the box, thinned here: each label claims a rectangle on
                // screen and a lower-ranked one that would overlap it is skipped , so a town never
                // sits on top of its country's name, and a crowded coast shows the few that fit.
                // A country name is drawn in capitals. Under the dots: a photo is the point.
                if (labels.isNotEmpty()) {
                    val claimed = claimedLabels
                    val nc = drawContext.canvas.nativeCanvas
                    for (l in labels) {
                        val x = sx(l.x); val y = sy(l.y)
                        if (x < -40f || x > sw + 40f || y < -20f || y > sh + 20f) continue
                        val p = namePaints[l.kind] ?: namePaints["P"]!!
                        val text = if (l.kind == "C") l.name.uppercase() else l.name
                        val w = p.fill.measureText(text) + 8f * density
                        val h = p.fill.textSize + 6f * density
                        val r = floatArrayOf(x - w / 2, y - h, x + w / 2, y + 2f * density)
                        if (claimed.any { it[0] < r[2] && r[0] < it[2] && it[1] < r[3] && r[1] < it[3] }) continue
                        claimed.add(r)
                        nc.drawText(text, x, y - 4f * density, p.halo)
                        nc.drawText(text, x, y - 4f * density, p.fill)
                        // a dot where the place is, so the name has an address
                        if (l.kind != "C" && l.kind != "R") drawCircle(GhostTextDim, radius = 2f * density, center = Offset(x, y))
                    }
                }
                // photo dots , the point of the whole screen. Pre-aggregated by the box: n == 1
                // is a photo, n > 1 is a cell with a count.
                for (d in dots) {
                    val x = sx(d.x); val y = sy(d.y)
                    if (x < -24f || x > sw + 24f || y < -24f || y > sh + 24f) continue
                    val p = d.cell
                    if (p.n <= 1) {
                        drawCircle(TerminalGreen, radius = 5f, center = Offset(x, y))
                    } else {
                        val r = (9f + 3.2f * kotlin.math.ln(p.n.toFloat())).coerceAtMost(26f)
                        drawCircle(TerminalGreen.copy(alpha = 0.22f), radius = r, center = Offset(x, y))
                        drawCircle(TerminalGreen, radius = r, center = Offset(x, y), style = Stroke(width = 1.5f))
                        drawContext.canvas.nativeCanvas.drawText(
                            if (p.n > 999) "999+" else p.n.toString(), x, y + 4f, labelPaint)
                    }
                }
                picked?.let { p ->
                    val x = sx(mercXD(p.lon)); val y = sy(mercYD(p.lat))
                    drawCircle(TerminalGreen, radius = 10f, center = Offset(x, y), style = Stroke(2f))
                }
            }
            // YOU ARE HERE, on its own small canvas over the map, so the pulse redraws this layer
            // sixty times a second and not the land, the roads and the trails under it. The fix's
            // error circle when it is wider than the dot at this zoom, a pulse ring that grows and
            // fades, a white ring with a green heart, and the word beside it with how old the fix
            // is , faded when the fix is hours old: the map says where the phone WAS.
            lastFix?.let { f ->
                if (viewW > 0f && viewH > 0f) Canvas(Modifier.fillMaxSize().clipToBounds()) {
                    val sw = size.width; val sh = size.height
                    val pxzD = (minOf(sw, sh) / WORLD).toDouble() * zoom
                    val x = ((mercXD(f.lon) - cx) * pxzD + sw / 2.0).toFloat()
                    val y = ((mercYD(f.lat) - cy) * pxzD + sh / 2.0).toFloat()
                    if (x > -60f && x < sw + 60f && y > -40f && y < sh + 40f) {
                        val age = nowSec - f.ts
                        val fresh = age < 2 * 3600
                        val tone = if (fresh) TerminalGreen else GhostTextDim
                        if (f.acc > 0f) {
                            // metres → map units at this latitude → px
                            val unitsPerM = (WORLD_UNITS / 360.0) / 111_320.0 / kotlin.math.cos(f.lat * PI / 180).coerceAtLeast(0.2)
                            val rPx = (f.acc * unitsPerM * pxzD).toFloat()
                            if (rPx > 14f) {
                                drawCircle(tone.copy(alpha = 0.10f), radius = rPx.coerceAtMost(sw), center = Offset(x, y))
                                drawCircle(tone.copy(alpha = 0.35f), radius = rPx.coerceAtMost(sw), center = Offset(x, y), style = Stroke(width = 1f))
                            }
                        }
                        val d = density
                        drawCircle(tone.copy(alpha = 0.45f * (1f - pulse)), radius = (10f + 16f * pulse) * d, center = Offset(x, y))
                        drawCircle(MapWater, radius = 9f * d, center = Offset(x, y))
                        drawCircle(GhostText, radius = 8f * d, center = Offset(x, y), style = Stroke(width = 2.5f * d))
                        drawCircle(tone, radius = 5f * d, center = Offset(x, y))
                        val label = if (fresh) "you" else "you, ${ago(age)}"
                        val nc = drawContext.canvas.nativeCanvas
                        val tx = x + 14f * d; val ty = y + 4f * d
                        nc.drawText(label, tx, ty, youHalo)
                        nc.drawText(label, tx, ty, youPaint)
                    }
                }
            }
            // what a delete took (no hint before it: a person zoomed right in taps a fix anyway)
            val hint = fixNote
            if (hint.isNotEmpty()) Text(hint, color = GhostText, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.align(Alignment.TopStart).padding(8.dp).background(Void.copy(alpha = 0.8f)).padding(horizontal = 8.dp, vertical = 4.dp))
        }
        // THE TRAIL , where this phone has been, by day. One line closed; open, a strip of days
        // with their distance (a dot after the label means part of it is still only on the phone),
        // and for the lit day a scrubber that walks the line with a clock.
        Column(Modifier.fillMaxWidth().padding(horizontal = 16.dp)) {
            val shownIdx = days.indexOfFirst { it.first == trailDay }
            val shownM = if (shownIdx >= 0) days[shownIdx].second else 0.0
            Row(verticalAlignment = Alignment.CenterVertically,
                modifier = Modifier.fillMaxWidth().clickable { trailOpen = !trailOpen }.padding(vertical = 6.dp)) {
                Text("◎ trail", color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
                Spacer(Modifier.width(8.dp))
                if (days.isEmpty()) {
                    Text("no points yet , a fix every quarter hour once location is allowed (settings › location trail)",
                        color = GhostTextDim, style = MaterialTheme.typography.labelMedium, modifier = Modifier.weight(1f))
                } else {
                    // ‹ an older day · the day shown · a newer day ›
                    val older = shownIdx < days.size - 1
                    val newer = shownIdx > 0
                    Text("‹", color = if (older) TerminalGreen else TerminalDim, style = MaterialTheme.typography.titleMedium,
                        modifier = Modifier.clickable(enabled = older) { stepDay(older = true) }.padding(horizontal = 10.dp, vertical = 2.dp))
                    Text(
                        (if (showAll) "all ${days.size} days" else trailDay?.let { dayLabel(it, todayKey, yesterdayKey) + " " + km(shownM) } ?: "") +
                            (lastFix?.let { " · last fix ${ago(nowSec - it.ts)}" } ?: ""),
                        color = GhostTextDim, style = MaterialTheme.typography.labelMedium, modifier = Modifier.weight(1f))
                    Text("›", color = if (newer) TerminalGreen else TerminalDim, style = MaterialTheme.typography.titleMedium,
                        modifier = Modifier.clickable(enabled = newer) { stepDay(older = false) }.padding(horizontal = 10.dp, vertical = 2.dp))
                }
                Text(if (trailOpen) "▴" else "▾", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
            }
            // THE PICKED FIX, open or closed panel alike: what a delete takes, and the choice
            fixPick?.let { f ->
                FixPickCard(f, clock = { clock(it) }, onDelete = { deleteFix(f) }, onRetry = { askFix(f) }, onCancel = { fixPick = null })
            }
            // WERE YOU THERE? The box's questions about the lit day, under the map whether the panel
            // is open or not (a "?" on the map marks each): no deletes the points on the box (and on
            // this phone) for good, yes keeps them and it never asks again. One at a time.
            val dayQs = trailDay?.takeIf { !showAll && fixPick == null }?.let { questions[it] } ?: emptyList()
            if (dayQs.isNotEmpty()) {
                val qi = askAt.coerceIn(0, dayQs.size - 1)
                val q = dayQs[qi]
                if (dayQs.size > 1) Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("question ${qi + 1} of ${dayQs.size}", color = GhostTextDim, style = MaterialTheme.typography.labelMedium, modifier = Modifier.weight(1f))
                    Text("next ›", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.clickable { askAt = (qi + 1) % dayQs.size }.padding(horizontal = 10.dp, vertical = 2.dp))
                }
                TrailQuestionCard(q, clock = { clock(it) }, busy = answering == q) { keep ->
                    answering = q
                    mapScope.launch {
                        val done = withContext(Dispatchers.IO) { BoxClient.trailAnswer(ctx, q, keep) }
                        if (done != null) {
                            if (!keep) withContext(Dispatchers.IO) { com.localghost.app.sync.LocationLog.forget(ctx, q.ts.toSet()) }
                            questions = questions.mapValues { (_, l) -> l.filter { it != q } }.filterValues { it.isNotEmpty() }
                            MapMemory.questions = questions
                            // the day drawn, measured and told again without those points
                            if (!keep) reloadTracks()
                        }
                        answering = null
                    }
                }
            }
            if (trailOpen && days.isNotEmpty()) {
                Row(Modifier.horizontalScroll(rememberScrollState())) {
                    days.forEach { (d, m, ts) ->
                        val on = d == trailDay
                        val label = dayLabel(d, todayKey, yesterdayKey) + " " + km(m) + (if (ts.any { it.phone }) " ·" else "")
                        Text(label, color = if (on) Void else TerminalGreen, style = MaterialTheme.typography.labelMedium,
                            modifier = Modifier.padding(end = 8.dp, bottom = 6.dp)
                                .border(1.dp, TerminalGreen, RectangleShape)
                                .background(if (on) TerminalGreen else Void)
                                .clickable { trailDay = d; showAll = false; scrub = 1f; frameTick++ }
                                .padding(horizontal = 10.dp, vertical = 6.dp))
                    }
                    // every day at once, the shown one lit, the rest the dim amber thread
                    Text("all days", color = if (showAll) Void else TerminalGreen, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.padding(end = 8.dp, bottom = 6.dp)
                            .border(1.dp, TerminalGreen, RectangleShape)
                            .background(if (showAll) TerminalGreen else Void)
                            .clickable { showAll = !showAll }
                            .padding(horizontal = 10.dp, vertical = 6.dp))
                }
                trailDay?.let { d ->
                    val ts = tracks.filter { it.day == d }
                    val waiting = ts.filter { it.phone }.sumOf { it.n }
                    val glitches = ts.sumOf { it.glitches }
                    val timed = dayPts.filter { it.ts > 0 }
                    Text(
                        dayLabel(d, todayKey, yesterdayKey) + " · " + km(ts.sumOf { it.distanceM }) +
                            (if (timed.isNotEmpty()) " · ${clock(timed.first().ts)} → ${clock(timed.last().ts)}" else "") +
                            " · ${dayPts.size} points" + (if (waiting > 0) " · $waiting waiting for the box" else "") +
                            (if (glitches > 0) " · $glitches glitch${if (glitches > 1) "es" else ""} ignored" else ""),
                        color = GhostText, style = MaterialTheme.typography.labelMedium)
                    // the ground under the day (the box's elevation tiles): climbed, and the highest point
                    val climb = ts.sumOf { it.climbM }
                    val high = ts.maxOfOrNull { it.highM } ?: 0.0
                    if (climb >= 20 || high > 0) Text(MapText.heights(climb, high), color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                    // the day as the box tells it: one line, then the stays with their hours and the
                    // moves with how far and how (along the streets, or straight lines)
                    route?.takeIf { it.day == d }?.let { rt ->
                        if (rt.r.line.isNotEmpty()) Text(rt.r.line, color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
                        val walks = rt.r.moves.count { it.mode == "walk" }
                        val routedHops = rt.r.moves.filter { it.mode == "walk" }.sumOf { it.routed }
                        val walkHops = rt.r.moves.filter { it.mode == "walk" }.sumOf { it.hops }
                        if (walks > 0) Text(
                            "on foot ${km(rt.r.walkM)}" + (if (walkHops > 0) " · $routedHops of $walkHops stretches along the streets" else "") +
                                (if (rt.r.rideM >= 1000) " · by road ${km(rt.r.rideM)}" else ""),
                            color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                        rt.r.stays.forEach { st ->
                            val what = when {
                                st.name.isEmpty() -> "a stop"
                                st.kind == "near" -> "near ${st.name}"
                                st.kind.isEmpty() || st.kind == "spot" -> st.name
                                else -> "${st.name} (${st.kind})"
                            }
                            Text("${span(st.from, st.to)}  $what" + (if (st.photos > 0) " · ${st.photos} photo${if (st.photos > 1) "s" else ""}" else ""),
                                color = GhostText, style = MaterialTheme.typography.labelMedium)
                        }
                        if (rt.r.note.isNotEmpty()) Text(rt.r.note, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                    }
                    if (dayPts.size >= 2) {
                        Slider(value = scrub, onValueChange = { scrub = it },
                            colors = SliderDefaults.colors(thumbColor = TerminalGreen, activeTrackColor = TerminalGreen, inactiveTrackColor = VoidLighter))
                        scrubAt?.let { at ->
                            Text((if (at.ts > 0) clock(at.ts) + " · " else "") + "%.5f, %.5f".format(java.util.Locale.US, invMercY(at.y), invMercX(at.x)) +
                                (if (at.alt != Int.MIN_VALUE) " · ${at.alt} m up" else ""),
                                color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                        }
                    }
                }
            }
        }
        viewer?.let { h -> ImageViewer(h, onDismiss = { viewer = null }) }
        picked?.let { p ->
            Row(Modifier.padding(horizontal = 16.dp, vertical = 8.dp)) {
                Text(
                    (if (p.n > 1) "${p.n} photos here" else "%.5f, %.5f".format(p.lat, p.lon)) +
                        (if (p.takenAt > 0) "  ·  " + java.text.SimpleDateFormat("MMM d, yyyy", java.util.Locale.US)
                            .format(java.util.Date(p.takenAt * 1000)) else ""),
                    color = GhostText, style = MaterialTheme.typography.labelMedium)
                if (p.n == 1 && p.hash.isNotEmpty()) {
                    Text("  [ open ]", color = TerminalGreen,
                        style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.clickable { viewer = p.hash })
                }
            }
        }
    }
}
