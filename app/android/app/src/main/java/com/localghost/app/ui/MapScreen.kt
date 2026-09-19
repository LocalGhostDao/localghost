package com.localghost.app.ui

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.gestures.detectTransformGestures
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.drawscope.withTransform
import androidx.compose.ui.graphics.nativeCanvas
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.onSizeChanged
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.platform.LocalDensity
import androidx.compose.ui.unit.dp
import com.localghost.app.net.BoxClient
import com.localghost.app.ui.theme.*
import kotlinx.coroutines.Dispatchers
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
 * work anywhere. The camera is Double: at 250,000x a Float map unit is thirty pixels wide.
 */

private const val WORLD = 1024f // == WORLD_UNITS, the Float twin for screen-space arithmetic

private fun invMercX(x: Double): Double = x / WORLD_UNITS * 360.0 - 180.0
private fun invMercY(y: Double): Double {
    val n = PI - 2.0 * PI * (y / WORLD_UNITS)
    return 180.0 / PI * atan(sinh(n))
}

/** A photo cell projected ONCE to map units when it arrives , the draw loop only scales. */
private class Dot(val x: Double, val y: Double, val cell: BoxClient.GeoCell)

/** One day's movement in map units (Double , it is stroked in screen space, per vertex, so it can
 *  keep a real 2.5px width at any zoom; framed already Douglas-Peucker'd it, so a day is tens to a
 *  few hundred points, never the half-million the landmass is). bbox for culling. */
private class Track(val xs: DoubleArray, val ys: DoubleArray, val minX: Double, val minY: Double, val maxX: Double, val maxY: Double)

private fun trackOf(pts: List<Pair<Double, Double>>): Track {
    val xs = DoubleArray(pts.size); val ys = DoubleArray(pts.size)
    var minX = Double.MAX_VALUE; var minY = Double.MAX_VALUE; var maxX = -Double.MAX_VALUE; var maxY = -Double.MAX_VALUE
    for (i in pts.indices) {
        val x = mercXD(pts[i].second); val y = mercYD(pts[i].first)
        xs[i] = x; ys[i] = y
        if (x < minX) minX = x; if (x > maxX) maxX = x
        if (y < minY) minY = y; if (y > maxY) maxY = y
    }
    return Track(xs, ys, minX, minY, maxX, maxY)
}

@Composable
fun MapScreen() {
    val ctx = LocalContext.current
    val density = LocalDensity.current.density
    var world by remember { mutableStateOf<World?>(null) }
    var worldNote by remember { mutableStateOf("") }
    var picked by remember { mutableStateOf<BoxClient.GeoCell?>(null) }
    var viewer by remember { mutableStateOf<String?>(null) } // hash open full-screen
    var tracks by remember { mutableStateOf<List<Track>>(emptyList()) }
    var loadNote by remember { mutableStateOf("loading…") }
    var cells by remember { mutableStateOf<List<BoxClient.GeoCell>>(emptyList()) }
    var level by remember { mutableStateOf(3) }
    var newest by remember { mutableStateOf<BoxClient.GeoCell?>(null) }
    // Projected once per cells change , 800 points at most, but it keeps the draw lambda to
    // multiply-adds and nothing else.
    val dots = remember(cells) { cells.map { Dot(mercXD(it.lon), mercYD(it.lat), it) } }

    LaunchedEffect(Unit) {
        newest = BoxClient.newestGeoFrame(ctx)
        // THE WORLD, SMALL FIRST. The box lists its landmass cuts (/v1/geo/world/index): open on
        // the smallest (a 110m world is under a megabyte, scanned in a blink), draw it, then load
        // the largest and swap it in once it is ready , the coastline sharpens under your thumb
        // instead of the screen waiting on 24MB. Everything off the main thread; the dots and the
        // camera never wait for landmass at all. A box that predates the index (null) or has only
        // the plain world.geojson gets the single-file path it always had.
        val cuts = BoxClient.worldIndex(ctx) ?: emptyList()
        fun note(w: World?, label: String) = if (w == null) "no landmass file on the box"
            else "landmass $label ${w.levels[2].size} rings · ${w.vertices[2] / 1000}k/${w.vertices[1] / 1000}k/${w.vertices[0] / 1000}k pts by zoom"
        suspend fun loadCut(res: String): World? {
            val (file, etag) = BoxClient.worldGeoJsonFile(ctx, res)
            return withContext(Dispatchers.Default) { WorldRings.load(ctx, file, etag, res.ifEmpty { "default" }) }
        }
        if (cuts.isEmpty()) {
            val w = loadCut("")
            world = w; worldNote = note(w, "")
        } else {
            val small = cuts.minByOrNull { it.bytes } ?: cuts[0]
            val big = cuts.maxByOrNull { it.bytes } ?: small
            val sw = loadCut(small.res)
            world = sw; worldNote = note(sw, small.res)
            if (big.res != small.res) {
                val bw = loadCut(big.res)
                if (bw != null) { world = bw; worldNote = note(bw, big.res) }
            }
        }
        // DAY TRACKS, one round trip. /v1/geo/tracks hands back the newest fortnight of polylines
        // in a single answer; a box that predates it (null) gets the old days-then-one-per-day walk.
        val batch = BoxClient.geoTracks(ctx, 14)
        val loaded = ArrayList<Track>()
        if (batch != null) {
            for ((_, pts) in batch) if (pts.size >= 2) loaded.add(trackOf(pts))
        } else {
            val days = BoxClient.geoDays(ctx, 14) ?: emptyList()
            for (d in days) {
                val pts = BoxClient.geoDayTrack(ctx, d) ?: continue
                if (pts.size >= 2) loaded.add(trackOf(pts))
            }
        }
        tracks = loaded
    }
    // One reusable Path for the per-frame track strokes (reset per track, never reallocated).
    val trackPath = remember { androidx.compose.ui.graphics.Path() }

    // Camera: centre in map units + zoom (screen px per map unit = min(w,h)/WORLD * zoom).
    var cx by remember { mutableStateOf(WORLD_UNITS / 2) }
    var cy by remember { mutableStateOf(WORLD_UNITS / 2) }
    var zoom by remember { mutableStateOf(1f) }
    var worldFallback by remember { mutableStateOf(false) }
    // OPENS ON THE NEWEST PHOTO, close in , the map answers "where was I last" before it answers
    // "where have I ever been". Pan/pinch out from there; the whole archive is one gesture away.
    var openerDone by remember { mutableStateOf(false) }
    LaunchedEffect(newest, cells) {
        val nw = newest
        if (nw != null && !openerDone &&
            nw.lat.isFinite() && nw.lon.isFinite() &&
            nw.lat > -90.0 && nw.lat < 90.0 && nw.lon >= -180.0 && nw.lon <= 180.0) {
            openerDone = true
            cx = mercXD(nw.lon); cy = mercYD(nw.lat)
            zoom = 6000f
        } else if (cells.isNotEmpty() && zoom <= 1f) {
            // Skew fallback: no newest endpoint , fit everything, like the map used to.
            val xs = cells.map { mercXD(it.lon) }; val ys = cells.map { mercYD(it.lat) }
            cx = (xs.min() + xs.max()) / 2.0; cy = (ys.min() + ys.max()) / 2.0
            val span = maxOf(xs.max() - xs.min(), ys.max() - ys.min(), 4.0)
            zoom = (WORLD_UNITS / span * 0.6).toFloat().coerceIn(1f, 400f)
        }
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
    LaunchedEffect(cx, cy, zoom, viewW, viewH) {
        kotlinx.coroutines.delay(160)
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
        val lod = BoxClient.framesGeoLod(ctx, lvl, q0, q1, q2, q3)
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
        if (cells.isEmpty() && zoom > 4f && !worldFallback) {
            worldFallback = true
            fetchedSpan = 0.0
            cx = WORLD_UNITS / 2; cy = WORLD_UNITS / 2; zoom = 1f
            return@LaunchedEffect
        }
        loadNote = when {
            cells.isEmpty() -> "no geotagged photos anywhere yet , they appear as photos with GPS sync"
            else -> cells.sumOf { it.n }.toString() + " photos · detail " + (lvl + 1) + "/4"
        }
    }
    // One Paint for every cluster label, not one per label per frame.
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
        Text("[ reset view ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.clickable {
                // The hatch , whatever the camera got into, one tap is the whole world again.
                cx = WORLD_UNITS / 2; cy = WORLD_UNITS / 2; zoom = 1f; worldFallback = false
            }.padding(vertical = 4.dp))
        Text(loadNote + (if (worldNote.isNotEmpty()) " · " + worldNote else ""),
            color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.padding(horizontal = 16.dp))
        Box(Modifier.weight(1f).fillMaxWidth().padding(12.dp).background(Void)) {
            // CAMERA SANITY , a NaN centre poisons every draw AND every gesture. Checked on each
            // composition; garbage snaps back to the world.
            if (!cx.isFinite() || !cy.isFinite() || !zoom.isFinite() || zoom <= 0f) {
                cx = WORLD_UNITS / 2; cy = WORLD_UNITS / 2; zoom = 1f; worldFallback = false
            }
            Canvas(Modifier.fillMaxSize()
                .onSizeChanged { viewW = it.width.toFloat(); viewH = it.height.toFloat() }
                .pointerInput(Unit) {
                    detectTransformGestures { centroid, pan, gz, _ ->
                        if (!cx.isFinite() || !cy.isFinite() || !zoom.isFinite()) {
                            cx = WORLD_UNITS / 2; cy = WORLD_UNITS / 2; zoom = 1f
                        }
                        // zoom about the finger centroid, then pan , standard camera algebra
                        val newZoom = (zoom * gz).coerceIn(0.8f, 250000f)
                        val sw = size.width.toDouble(); val sh = size.height.toDouble()
                        val scale = minOf(sw, sh) / WORLD_UNITS
                        val pxOld = scale * zoom; val pxNew = scale * newZoom
                        val wx = cx + (centroid.x - sw / 2) / pxOld
                        val wy = cy + (centroid.y - sh / 2) / pxOld
                        cx = wx - (centroid.x - sw / 2) / pxNew
                        cy = wy - (centroid.y - sh / 2) / pxNew
                        zoom = newZoom
                        cx -= pan.x / pxNew; cy -= pan.y / pxNew
                        picked = null
                    }
                }
                .pointerInput(dots) {
                    detectTapGestures { tap ->
                        val sw = size.width.toDouble(); val sh = size.height.toDouble()
                        val pxz = (minOf(sw, sh) / WORLD_UNITS) * zoom
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
                                zoom = (zoom * 6f).coerceAtMost(250000f)
                            }
                        }
                    }
                }) {
                val sw = size.width; val sh = size.height
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
                // graticule every 15 degrees , always drawn, the honest skeleton of the projection
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
                // landmass outlines at the detail level that is under a pixel of error right now
                world?.let { w -> drawRings(w.levels[World.levelFor(pxz)], TerminalDim, 1.5f) }
                // day tracks , movement under the moments, drawn OVER the coastline and stroked in
                // SCREEN space per vertex, so the line stays 2.5px wide from the world view to the
                // street. Affordable because framed simplified each day already; culled by bbox.
                for (t in tracks) {
                    if (t.maxX < vx0 || t.minX > vx1 || t.maxY < vy0 || t.minY > vy1) continue
                    trackPath.reset()
                    trackPath.moveTo(sx(t.xs[0]), sy(t.ys[0]))
                    for (i in 1 until t.xs.size) trackPath.lineTo(sx(t.xs[i]), sy(t.ys[i]))
                    drawPath(trackPath, TerminalDim, style = Stroke(width = 2.5f))
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
