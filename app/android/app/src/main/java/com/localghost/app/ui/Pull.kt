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
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.graphics.nativeCanvas
import androidx.compose.ui.unit.dp
import com.localghost.app.ui.theme.*
import kotlin.math.PI
import kotlin.math.abs
import kotlin.math.sin

/**
 * THE PULL, drawn: the places an integration draws from on the left, the box on the right, and
 * the data as packets running the lines between them. While the page is still asking the box
 * the packets stream; once the answer is in they slow to a breath, and a place that is off
 * sends none. Each integration draws in its own manner (PullModel.Style): Wikipedia's pages
 * turn on their way and stack at the box, the news comes in bursts of lines, the market as a
 * ticker of bars jogging up and down, the weather falls in drops, the maps' tiles slot into a
 * grid beside the box, speech is a wave along the line. The geometry is PullModel (PullModel.kt),
 * pure, so the tests can read it.
 */
@Composable
fun PullCanvas(fromStates: List<String>, steady: Boolean, modifier: Modifier = Modifier, style: PullModel.Style = PullModel.Style.PLAIN) {
    // a free clock in seconds: every frame while the packets stream, a few times a second once
    // the picture is only breathing (a page left open is not a reason to render at the display's
    // rate)
    var clock by remember { mutableFloatStateOf(0f) }
    LaunchedEffect(steady) {
        val t0 = System.nanoTime() - (clock * 1_000_000_000f).toLong()
        while (true) {
            if (steady) { kotlinx.coroutines.delay(80); clock = (System.nanoTime() - t0) / 1_000_000_000f }
            else withFrameNanos { now -> clock = (now - t0) / 1_000_000_000f }
        }
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
        val dimLine = TerminalDim.copy(alpha = 0.6f)
        // THE LINES, each in the integration's manner: a wave for speech, a bowed thread for the
        // rest (a drop's sag, a ticker's jogs are the packets' own)
        srcs.forEachIndexed { i, s ->
            val off = fromStates[i] == "off"
            val path = Path()
            path.moveTo(s.x, s.y)
            var t = 0f
            while (t <= 1.0001f) {
                val p = PullModel.packet(s, box, t, if (style == PullModel.Style.DROPS) style else PullModel.Style.PLAIN)
                val y = if (style == PullModel.Style.WAVE && !off) p.y + PullModel.wave(t, clock) * (box.x - s.x) else p.y
                path.lineTo(p.x, y); t += 0.04f
            }
            drawPath(path, if (off) GhostBorder else dimLine, style = Stroke(width = (if (style == PullModel.Style.WAVE) 2f else 1.5f) * px))
            // the source's node
            drawCircle(if (off) GhostBorder else TerminalGreen, radius = dot, center = Offset(s.x, s.y))
            if (off) return@forEachIndexed
            // THE PACKETS, in the integration's manner
            val ph = PullModel.phase(i, n, clock, steady)
            val alpha = if (steady) 0.6f else 1f
            for ((k, b) in PullModel.burst(style).withIndex()) {
                val head = ph - b
                if (head < 0f) continue
                when (style) {
                    PullModel.Style.PAGES -> {
                        // a page on its way, turning: a square whose width flips
                        val p = PullModel.packet(s, box, head, style, i, k)
                        val pw = 7f * px * PullModel.pageFlip(clock + i)
                        drawRect(TerminalGreen.copy(alpha = alpha), topLeft = Offset(p.x - pw / 2f, p.y - 5f * px), size = Size(pw, 10f * px))
                        drawRect(Void, topLeft = Offset(p.x - pw / 2f + 1.5f * px, p.y - 2f * px), size = Size((pw - 3f * px).coerceAtLeast(0f), 1f * px))
                    }
                    PullModel.Style.BURST -> {
                        // a line of text: a short dash along the way
                        val p = PullModel.packet(s, box, head, style, i, k)
                        val q = PullModel.packet(s, box, (head - 0.025f).coerceAtLeast(0f), style, i, k)
                        drawLine(TerminalGreen.copy(alpha = alpha * (1f - k * 0.25f)), Offset(q.x, q.y), Offset(p.x, p.y), strokeWidth = 3f * px, cap = StrokeCap.Round)
                    }
                    PullModel.Style.TICKER -> {
                        // a bar for each tick of the walk behind the head, up green, down dim
                        for (j in 0..5) {
                            val tt = head - j * 0.045f
                            if (tt < 0f) continue
                            val p = PullModel.packet(s, box, tt, style, i, k)
                            val prev = PullModel.packet(s, box, (tt - 0.045f).coerceAtLeast(0f), style, i, k)
                            val up = p.y <= prev.y
                            drawLine(if (up) TerminalGreen.copy(alpha = alpha * (1f - j * 0.15f)) else GhostTextDim.copy(alpha = alpha * (1f - j * 0.15f)),
                                Offset(p.x, p.y - 4f * px), Offset(p.x, p.y + 4f * px), strokeWidth = 2.5f * px)
                        }
                    }
                    PullModel.Style.DROPS -> {
                        // a drop, longer as it falls, with a trail of small ones
                        for (j in 0..3) {
                            val tt = head - j * 0.05f
                            if (tt < 0f) continue
                            val p = PullModel.packet(s, box, tt, style, i, k)
                            val len = (3f + 5f * tt) * px * (if (j == 0) 1f else 0.5f)
                            drawLine(TerminalGreen.copy(alpha = alpha * (1f - j * 0.22f)), Offset(p.x, p.y - len / 2f), Offset(p.x, p.y + len / 2f), strokeWidth = (if (j == 0) 3f else 1.5f) * px, cap = StrokeCap.Round)
                        }
                    }
                    PullModel.Style.TILES -> {
                        // a tile on its way, square-cornered; the ones that landed sit in a grid
                        val p = PullModel.packet(s, box, head, style, i, k)
                        drawRect(TerminalGreen.copy(alpha = alpha), topLeft = Offset(p.x - 4f * px, p.y - 4f * px), size = Size(8f * px, 8f * px))
                    }
                    PullModel.Style.WAVE -> {
                        // a louder stretch of the wave travelling along it
                        val wp = Path()
                        var first = true
                        var u = (head - 0.12f).coerceAtLeast(0f)
                        while (u <= head) {
                            val p = PullModel.packet(s, box, u, PullModel.Style.PLAIN)
                            val y = p.y + PullModel.wave(u, clock) * (box.x - s.x) * 1.8f
                            if (first) { wp.moveTo(p.x, y); first = false } else wp.lineTo(p.x, y)
                            u += 0.01f
                        }
                        drawPath(wp, TerminalGreen.copy(alpha = alpha), style = Stroke(width = 2.5f * px, cap = StrokeCap.Round))
                    }
                    PullModel.Style.PLAIN -> {
                        // the packet and its tail
                        for (j in 0..4) {
                            val tt = head - j * 0.03f
                            if (tt < 0f) continue
                            val p = PullModel.packet(s, box, tt)
                            drawCircle(TerminalGreen.copy(alpha = (1f - j * 0.2f) * alpha), radius = if (j == 0) dot else dot * 0.6f, center = Offset(p.x, p.y))
                        }
                    }
                }
            }
        }
        // THE BOX: a ring that breathes when steady, and fills while pulling; beside it what the
        // integration gathers (the maps' tiles in a grid, Wikipedia's pages in a stack)
        val breathe = if (steady) 0.75f + 0.25f * sin(clock * 1.2f) else 1f
        drawCircle(TerminalGreen.copy(alpha = 0.18f * breathe), radius = halo, center = Offset(box.x, box.y))
        drawCircle(TerminalGreen, radius = ring, center = Offset(box.x, box.y), style = Stroke(width = 2f * px, cap = StrokeCap.Round))
        when (style) {
            PullModel.Style.TILES -> {
                val cell = 5f * px; val gap = 1.5f * px
                val slot = PullModel.tileSlot(clock)
                for (c in 0 until 9) {
                    val cx = box.x - (cell + gap) + (c % 3) * (cell + gap)
                    val cy = box.y - (cell + gap) + (c / 3) * (cell + gap)
                    val lit = if (steady) c <= slot else c < slot
                    drawRect(if (lit) TerminalGreen.copy(alpha = 0.9f) else TerminalDim.copy(alpha = 0.35f), topLeft = Offset(cx - cell / 2f, cy - cell / 2f), size = Size(cell, cell))
                }
            }
            PullModel.Style.PAGES -> {
                val stack = PullModel.tileSlot(clock) / 2 + 1
                for (c in 0 until stack) {
                    val y = box.y + 6f * px - c * 3f * px
                    drawLine(TerminalGreen.copy(alpha = 0.9f - c * 0.12f), Offset(box.x - 6f * px, y), Offset(box.x + 6f * px, y), strokeWidth = 1.5f * px)
                }
            }
            PullModel.Style.WAVE -> {
                // the box hearing: a dot that pulses with the wave
                drawCircle(TerminalGreen, radius = (3f + 2f * abs(sin(clock * 5f))) * px, center = Offset(box.x, box.y))
            }
            PullModel.Style.TICKER -> {
                val up = sin(clock * 0.7f) > 0f
                drawLine(if (up) TerminalGreen else GhostTextDim, Offset(box.x - 6f * px, box.y + (if (up) 4f else -4f) * px), Offset(box.x + 6f * px, box.y + (if (up) -4f else 4f) * px), strokeWidth = 2f * px, cap = StrokeCap.Round)
            }
            else -> {}
        }
        if (!steady) {
            // a sweep while asking
            val a = (clock * 2f) % (2f * PI.toFloat())
            drawArc(TerminalGreen, startAngle = Math.toDegrees(a.toDouble()).toFloat(), sweepAngle = 90f, useCenter = false,
                topLeft = Offset(box.x - halo, box.y - halo), size = Size(halo * 2f, halo * 2f), style = Stroke(width = 2f * px))
        }
        val paint = android.graphics.Paint().apply {
            color = android.graphics.Color.argb(255, 0x33, 0xFF, 0x00); textSize = 11f * px
            textAlign = android.graphics.Paint.Align.CENTER; typeface = android.graphics.Typeface.MONOSPACE
        }
        drawContext.canvas.nativeCanvas.drawText("the box", box.x, box.y + halo + 14f * px, paint)
    }
}
