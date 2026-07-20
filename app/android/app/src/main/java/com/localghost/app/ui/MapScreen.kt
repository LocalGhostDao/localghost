package com.localghost.app.ui

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.gestures.detectTransformGestures
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.padding
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import com.localghost.app.net.BoxClient
import com.localghost.app.ui.theme.*
import kotlin.math.PI
import kotlin.math.atan
import kotlin.math.exp
import kotlin.math.ln
import kotlin.math.tan

/**
 * The map, SELF-DRAWN , no tile servers, no map SDK, because every tile fetch ships the viewport
 * (and therefore where your photos are) to a third party, which is the one thing this product does
 * not do. Instead: Web Mercator on a Compose Canvas; landmass from the operator-provided Natural
 * Earth GeoJSON on the box (/v1/geo/world , public domain, a few MB, optional); your photo
 * locations from /v1/frames/geo as dots that thin out by zoom. No streets, no labels beyond the
 * tapped dot's place string , an outline world with YOUR data on it, fully offline, and it says so.
 */

private const val WORLD = 1024f // world size in map units at zoom 1

private fun mercX(lon: Double): Float = ((lon + 180.0) / 360.0 * WORLD).toFloat()
private fun mercY(lat: Double): Float {
    val l = lat.coerceIn(-85.05, 85.05) * PI / 180.0
    return ((1.0 - ln(tan(l) + 1.0 / kotlin.math.cos(l)) / PI) / 2.0 * WORLD).toFloat()
}
private fun invLat(y: Float): Double {
    val n = PI - 2.0 * PI * y / WORLD
    return 180.0 / PI * atan(0.5 * (exp(n) - exp(-n)))
}
private fun invLon(x: Float): Double = x / WORLD * 360.0 - 180.0

/** One landmass ring, pre-projected to map units , parsed once, drawn every frame. */
private class Ring(val xs: FloatArray, val ys: FloatArray)

private fun parseWorld(gj: org.json.JSONObject): List<Ring> {
    val out = ArrayList<Ring>()
    val feats = gj.optJSONArray("features") ?: return out
    fun addRing(ring: org.json.JSONArray) {
        val n = ring.length()
        if (n < 3) return
        val xs = FloatArray(n); val ys = FloatArray(n)
        for (i in 0 until n) {
            val pt = ring.optJSONArray(i) ?: return
            xs[i] = mercX(pt.optDouble(0)); ys[i] = mercY(pt.optDouble(1))
        }
        out.add(Ring(xs, ys))
    }
    for (i in 0 until feats.length()) {
        val geom = feats.optJSONObject(i)?.optJSONObject("geometry") ?: continue
        val coords = geom.optJSONArray("coordinates") ?: continue
        when (geom.optString("type")) {
            "Polygon" -> for (r in 0 until coords.length()) coords.optJSONArray(r)?.let(::addRing)
            "MultiPolygon" -> for (p in 0 until coords.length()) {
                val poly = coords.optJSONArray(p) ?: continue
                for (r in 0 until poly.length()) poly.optJSONArray(r)?.let(::addRing)
            }
        }
    }
    return out
}

@Composable
fun MapScreen() {
    val ctx = LocalContext.current
    var rings by remember { mutableStateOf<List<Ring>>(emptyList()) }
    var points by remember { mutableStateOf<List<BoxClient.GeoPoint>>(emptyList()) }
    var picked by remember { mutableStateOf<BoxClient.GeoPoint?>(null) }
    var tracks by remember { mutableStateOf<List<List<Pair<Float, Float>>>>(emptyList()) }
    var loadNote by remember { mutableStateOf("loading…") }
    LaunchedEffect(Unit) {
        points = BoxClient.framesGeo(ctx) ?: emptyList()
        val world = BoxClient.worldGeoJson(ctx)
        rings = world?.let { runCatching { parseWorld(it) }.getOrDefault(emptyList()) } ?: emptyList()
        // DAY TRACKS , where you actually went, drawn under where you actually shot. Last 14 days
        // with tracks, pre-projected once; the volume of a fortnight's GPS is trivially drawable.
        val days = BoxClient.geoDays(ctx, 14) ?: emptyList()
        val loaded = ArrayList<List<Pair<Float, Float>>>()
        for (d in days) {
            val pts = BoxClient.geoDayTrack(ctx, d) ?: continue
            if (pts.size >= 2) loaded.add(pts.map { Pair(mercX(it.second), mercY(it.first)) })
        }
        tracks = loaded
        loadNote = when {
            points.isEmpty() -> "no geotagged photos yet , sync + reprocess fill this in"
            rings.isEmpty() -> "${points.size} photos · no base map on the box (drop Natural Earth at <volume>/geo/world.geojson)"
            else -> "${points.size} photos"
        }
    }
    // Camera: centre in map units + zoom (pixels per map unit factor). Starts framing the DATA
    // when there is any, the whole world otherwise.
    var cx by remember { mutableStateOf(WORLD / 2f) }
    var cy by remember { mutableStateOf(WORLD / 2f) }
    var zoom by remember { mutableStateOf(1f) }
    LaunchedEffect(points) {
        if (points.isNotEmpty()) {
            val xs = points.map { mercX(it.lon) }; val ys = points.map { mercY(it.lat) }
            cx = (xs.min() + xs.max()) / 2f; cy = (ys.min() + ys.max()) / 2f
            val span = maxOf(xs.max() - xs.min(), ys.max() - ys.min(), 4f)
            zoom = (WORLD / span * 0.6f).coerceIn(1f, 400f)
        }
    }
    Column(Modifier.fillMaxSize()) {
        Text("> MAP", color = TerminalGreen, style = MaterialTheme.typography.titleMedium,
            modifier = Modifier.padding(16.dp))
        Text(loadNote, color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.padding(horizontal = 16.dp))
        Box(Modifier.weight(1f).fillMaxWidth().padding(12.dp).background(Void)) {
            Canvas(Modifier.fillMaxSize()
                .pointerInput(Unit) {
                    detectTransformGestures { centroid, pan, gz, _ ->
                        // zoom about the finger centroid, then pan , standard camera algebra
                        val newZoom = (zoom * gz).coerceIn(0.8f, 2000f)
                        val sw = size.width.toFloat(); val sh = size.height.toFloat()
                        val scale = (minOf(sw, sh) / WORLD)
                        val px = { z: Float -> scale * z }
                        val wx = cx + (centroid.x - sw / 2f) / px(zoom)
                        val wy = cy + (centroid.y - sh / 2f) / px(zoom)
                        cx = wx - (centroid.x - sw / 2f) / px(newZoom)
                        cy = wy - (centroid.y - sh / 2f) / px(newZoom)
                        zoom = newZoom
                        cx -= pan.x / px(zoom); cy -= pan.y / px(zoom)
                        picked = null
                    }
                }
                .pointerInput(points) {
                    detectTapGestures { tap ->
                        val sw = size.width.toFloat(); val sh = size.height.toFloat()
                        val pxz = (minOf(sw, sh) / WORLD) * zoom
                        var best: BoxClient.GeoPoint? = null; var bestD = 40f * 40f
                        points.forEach { p ->
                            val sx = (mercX(p.lon) - cx) * pxz + sw / 2f
                            val sy = (mercY(p.lat) - cy) * pxz + sh / 2f
                            val d = (sx - tap.x) * (sx - tap.x) + (sy - tap.y) * (sy - tap.y)
                            if (d < bestD) { bestD = d; best = p }
                        }
                        picked = best
                    }
                }) {
                val sw = size.width; val sh = size.height
                val pxz = (minOf(sw, sh) / WORLD) * zoom
                fun sx(x: Float) = (x - cx) * pxz + sw / 2f
                fun sy(y: Float) = (y - cy) * pxz + sh / 2f
                // graticule every 15 degrees , always drawn, the honest skeleton of the projection
                var lon = -180.0
                while (lon <= 180.0) {
                    val x = sx(mercX(lon))
                    if (x in -2f..sw + 2f) drawLine(GhostBorder, Offset(x, 0f), Offset(x, sh), 1f)
                    lon += 15.0
                }
                var lat = -75.0
                while (lat <= 75.0) {
                    val y = sy(mercY(lat))
                    if (y in -2f..sh + 2f) drawLine(GhostBorder, Offset(0f, y), Offset(sw, y), 1f)
                    lat += 15.0
                }
                // day tracks , movement under the moments, dimmer than the dots they connect
                tracks.forEach { tr ->
                    val path = Path()
                    path.moveTo(sx(tr[0].first), sy(tr[0].second))
                    for (i in 1 until tr.size) path.lineTo(sx(tr[i].first), sy(tr[i].second))
                    drawPath(path, TerminalDim, style = Stroke(width = 2.5f))
                }
                // landmass outlines
                rings.forEach { r ->
                    val path = Path()
                    path.moveTo(sx(r.xs[0]), sy(r.ys[0]))
                    for (i in 1 until r.xs.size) path.lineTo(sx(r.xs[i]), sy(r.ys[i]))
                    path.close()
                    drawPath(path, TerminalDim, style = Stroke(width = 1.5f))
                }
                // photo dots , the point of the whole screen
                points.forEach { p ->
                    val x = sx(mercX(p.lon)); val y = sy(mercY(p.lat))
                    if (x in -8f..sw + 8f && y in -8f..sh + 8f) {
                        drawCircle(TerminalGreen, radius = 5f, center = Offset(x, y))
                    }
                }
                picked?.let { p ->
                    val x = sx(mercX(p.lon)); val y = sy(mercY(p.lat))
                    drawCircle(TerminalGreen, radius = 10f, center = Offset(x, y), style = Stroke(2f))
                }
            }
        }
        picked?.let { p ->
            Text(
                (if (p.place.isNotBlank()) p.place else "%.4f, %.4f".format(p.lat, p.lon)) +
                    "  ·  " + java.text.SimpleDateFormat("MMM d, yyyy", java.util.Locale.US)
                        .format(java.util.Date(p.takenAt * 1000)),
                color = GhostText, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.padding(horizontal = 16.dp, vertical = 8.dp))
        }
    }
}
