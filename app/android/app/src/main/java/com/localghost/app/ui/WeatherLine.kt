package com.localghost.app.ui

import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.runtime.Composable
import androidx.compose.runtime.remember
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.nativeCanvas
import androidx.compose.ui.unit.dp
import com.localghost.app.ui.theme.TerminalGreen

/** The next day's weather as a line: temperature across the hours, the chance of rain as bars
 *  along the bottom (darker the likelier), the hour under every sixth, the warmest and the
 *  coldest written at their points. Drawn, not charted: one glance says when it turns. */
@Composable
fun WeatherLine(hs: List<WeatherHour>, height: androidx.compose.ui.unit.Dp = 64.dp, band: Boolean = false) {
    // the band: the models' spread around the line, half either side, for a forecast the box computed
    val half = { h: WeatherHour -> if (band) h.spreadC / 2 else 0.0 }
    val lo = hs.minOf { it.tempC - half(it) }; val hi = hs.maxOf { it.tempC + half(it) }
    val tLo = hs.minOf { it.tempC }; val tHi = hs.maxOf { it.tempC }
    val iLo = hs.indexOfFirst { it.tempC == tLo }; val iHi = hs.indexOfFirst { it.tempC == tHi }
    val labels = remember(hs) { WeatherHours.labelled(hs) }
    androidx.compose.foundation.Canvas(Modifier.fillMaxWidth().height(height)) {
        val w = size.width; val h = size.height
        val top = 12f * density; val bottom = h - 16f * density // room for the labels under
        val barH = 6f * density
        val plotBottom = bottom - barH - 2f * density
        val step = if (hs.size > 1) w / (hs.size - 1) else w
        fun x(i: Int) = i * step
        fun y(t: Double) = top + WeatherHours.y(t, lo, hi) * (plotBottom - top)
        // the rain bars
        hs.forEachIndexed { i, hr ->
            if (hr.rainPct > 0) drawRect(TerminalGreen.copy(alpha = 0.15f + 0.6f * (hr.rainPct / 100f)),
                topLeft = androidx.compose.ui.geometry.Offset(x(i) - step / 2f + 1f, plotBottom + 2f * density),
                size = androidx.compose.ui.geometry.Size((step - 2f).coerceAtLeast(1f), barH))
        }
        // the band, drawn before the line
        if (band && hs.any { it.spreadC > 0 }) {
            val bp = androidx.compose.ui.graphics.Path()
            hs.forEachIndexed { i, hr -> if (i == 0) bp.moveTo(x(i), y(hr.tempC + hr.spreadC / 2)) else bp.lineTo(x(i), y(hr.tempC + hr.spreadC / 2)) }
            for (i in hs.indices.reversed()) bp.lineTo(x(i), y(hs[i].tempC - hs[i].spreadC / 2))
            bp.close()
            drawPath(bp, TerminalGreen.copy(alpha = 0.18f))
        }
        // the line
        val path = androidx.compose.ui.graphics.Path()
        hs.forEachIndexed { i, hr -> if (i == 0) path.moveTo(x(i), y(hr.tempC)) else path.lineTo(x(i), y(hr.tempC)) }
        drawPath(path, TerminalGreen, style = androidx.compose.ui.graphics.drawscope.Stroke(width = 2f * density))
        val paint = android.graphics.Paint().apply {
            color = android.graphics.Color.argb(255, 0x7a, 0x8a, 0x7a); textSize = 10f * density
            typeface = android.graphics.Typeface.MONOSPACE; textAlign = android.graphics.Paint.Align.CENTER
        }
        val nc = drawContext.canvas.nativeCanvas
        labels.forEach { i -> nc.drawText(WeatherHours.hourOf(hs[i].at), x(i).coerceIn(8f * density, w - 8f * density), h - 3f * density, paint) }
        // the warmest and the coldest, at their points
        val bright = android.graphics.Paint(paint).apply { color = android.graphics.Color.argb(255, 0xd0, 0xd0, 0xd0) }
        nc.drawText("%.0f°".format(java.util.Locale.UK, tHi), x(iHi).coerceIn(12f * density, w - 12f * density), (y(tHi) - 5f * density).coerceAtLeast(9f * density), bright)
        if (iLo != iHi) nc.drawText("%.0f°".format(java.util.Locale.UK, tLo), x(iLo).coerceIn(12f * density, w - 12f * density), (y(tLo) + 12f * density).coerceAtMost(plotBottom), bright)
    }
}

