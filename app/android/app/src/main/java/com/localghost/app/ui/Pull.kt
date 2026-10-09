package com.localghost.app.ui

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableFloatStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.runtime.withFrameNanos
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.nativeCanvas
import androidx.compose.ui.unit.dp
import com.localghost.app.ui.theme.*
import kotlin.math.PI
import kotlin.math.sin

/**
 * THE PULL, drawn: the places an integration draws from on the left, the box on the right, and
 * the data as packets running the lines between them. While the page is still asking the box
 * the packets stream; once the answer is in they slow to a breath, and a place that is off
 * sends none. The same picture on every integration's page, so a glance says what is flowing
 * and from where. The geometry is PullModel (PullModel.kt), pure, so the tests can read it.
 */
@Composable
fun PullCanvas(fromStates: List<String>, steady: Boolean, modifier: Modifier = Modifier) {
    // a free clock in seconds, one frame at a time
    var clock by remember { mutableFloatStateOf(0f) }
    LaunchedEffect(Unit) {
        val t0 = withFrameNanos { it }
        while (true) withFrameNanos { now -> clock = (now - t0) / 1_000_000_000f }
    }
    val n = fromStates.size.coerceAtMost(8)
    Canvas(modifier.fillMaxWidth().height((72 + 18 * n.coerceAtLeast(1)).dp)) {
        val w = size.width
        val h = size.height
        val px = density // dp to px: the drawing is sized in dp, the screen in px
        val dot = 4f * px
        val ring = 14f * px
        val halo = 22f * px
        val srcs = PullModel.sources(n, w, h)
        val box = PullModel.box(w, h)
        // the lines, dim, bowing like the packets' path
        srcs.forEachIndexed { i, s ->
            val off = fromStates[i] == "off"
            val path = Path()
            path.moveTo(s.x, s.y)
            var t = 0f
            while (t <= 1f) { val p = PullModel.packet(s, box, t); path.lineTo(p.x, p.y); t += 0.05f }
            drawPath(path, if (off) GhostBorder else TerminalDim.copy(alpha = 0.6f), style = Stroke(width = 1.5f * px))
            // the source's node
            drawCircle(if (off) GhostBorder else TerminalGreen, radius = dot, center = Offset(s.x, s.y))
            if (!off) {
                // the packet and its tail
                val ph = PullModel.phase(i, n, clock, steady)
                for (k in 0..4) {
                    val tt = ph - k * 0.03f
                    if (tt < 0f) continue
                    val p = PullModel.packet(s, box, tt)
                    drawCircle(TerminalGreen.copy(alpha = (1f - k * 0.2f) * (if (steady) 0.6f else 1f)), radius = if (k == 0) dot else dot * 0.6f, center = Offset(p.x, p.y))
                }
            }
        }
        // the box: a ring that breathes when steady, and fills while pulling
        val breathe = if (steady) 0.75f + 0.25f * sin(clock * 1.2f) else 1f
        drawCircle(TerminalGreen.copy(alpha = 0.18f * breathe), radius = halo, center = Offset(box.x, box.y))
        drawCircle(TerminalGreen, radius = ring, center = Offset(box.x, box.y), style = Stroke(width = 2f * px, cap = StrokeCap.Round))
        if (!steady) {
            // a sweep while asking
            val a = (clock * 2f) % (2f * PI.toFloat())
            drawArc(TerminalGreen, startAngle = Math.toDegrees(a.toDouble()).toFloat(), sweepAngle = 90f, useCenter = false,
                topLeft = Offset(box.x - halo, box.y - halo), size = androidx.compose.ui.geometry.Size(halo * 2f, halo * 2f), style = Stroke(width = 2f * px))
        }
        val paint = android.graphics.Paint().apply {
            color = android.graphics.Color.argb(255, 0x33, 0xFF, 0x00); textSize = 11f * px
            textAlign = android.graphics.Paint.Align.CENTER; typeface = android.graphics.Typeface.MONOSPACE
        }
        drawContext.canvas.nativeCanvas.drawText("the box", box.x, box.y + halo + 14f * px, paint)
    }
}
