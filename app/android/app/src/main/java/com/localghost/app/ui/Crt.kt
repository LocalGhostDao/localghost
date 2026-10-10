package com.localghost.app.ui

import androidx.compose.animation.core.Animatable
import androidx.compose.animation.core.LinearEasing
import androidx.compose.animation.core.tween
import androidx.compose.foundation.Canvas
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.TileMode
import com.localghost.app.ui.theme.TerminalGreen
import com.localghost.app.ui.theme.Void

/**
 * THE SCREEN. The app is a terminal on a small green screen: a Pip-Boy with a journal in it,
 * and the journal and the assistant are one thing, so the look is one voice. The glass shows
 * itself the way the unlock's rings and the scanner's aperture do: now and then, for a moment,
 * not all the time (CrtMood says when). Three small things, each drawing only, none taking a
 * touch: a wash of scanlines with a vignette that fades in and out; a stray scanline that shows for a
 * blink somewhere on the glass, once or twice, like interference caught; a heading that types
 * itself in. SETTINGS › SCREEN turns the lot off.
 */
object CrtState {
    /** The effect under way, set by the shell on a page change; NONE between. */
    var effect by androidx.compose.runtime.mutableStateOf(CrtMood.Effect.NONE)
}

/** The wash: scanlines and a vignette that fade in and out over the hold. */
@Composable
fun CrtWash(playing: Boolean, modifier: Modifier = Modifier) {
    val alpha = remember { Animatable(0f) }
    LaunchedEffect(playing) {
        if (playing) {
            alpha.snapTo(0f)
            alpha.animateTo(1f, tween(durationMillis = 350, easing = LinearEasing))
            alpha.animateTo(0f, tween(durationMillis = 900, easing = LinearEasing))
        } else alpha.snapTo(0f)
    }
    val a = alpha.value
    if (a <= 0.01f) return
    Canvas(modifier) {
        // scanlines: a tile of about 3 px at any density, the lower half a shade darker, repeated
        // down the screen
        val tile = (3f * density).coerceAtLeast(2f)
        val lines = Brush.verticalGradient(
            0f to Color.Transparent, 0.5f to Color.Transparent, 0.5f to Color.Black.copy(alpha = 0.14f * a), 1f to Color.Black.copy(alpha = 0.14f * a),
            startY = 0f, endY = tile, tileMode = TileMode.Repeated)
        drawRect(lines)
        // the vignette: clear in the middle, the corners a little darker
        val r = maxOf(size.width, size.height) * 0.75f
        drawRect(Brush.radialGradient(0.55f to Color.Transparent, 1f to Void.copy(alpha = 0.5f * a), center = Offset(size.width / 2f, size.height / 2f), radius = r))
        // a faint green cast on the glass
        drawRect(TerminalGreen.copy(alpha = 0.04f * a))
    }
}

/** The line: a stray scanline that shows for a blink somewhere on the glass, once or twice,
 *  the way a tube catches interference, then is gone. Where it falls is random; it never sweeps
 *  the page and never sits at an edge. Plays once when [playing] turns true. */
@Composable
fun CrtLine(playing: Boolean, modifier: Modifier = Modifier) {
    // the line's height as a fraction of the glass; below zero is no line
    var at by remember { androidx.compose.runtime.mutableFloatStateOf(-1f) }
    LaunchedEffect(playing) {
        if (!playing) { at = -1f; return@LaunchedEffect }
        val r = kotlin.random.Random
        for ((y, onMs, offMs) in CrtMood.lineBlinks(r.nextFloat(), r.nextFloat())) {
            at = y
            kotlinx.coroutines.delay(onMs)
            at = -1f
            kotlinx.coroutines.delay(offMs)
        }
    }
    val y = at
    if (y < 0f) return
    Canvas(modifier) {
        val py = size.height * y
        val halo = 6f * density
        // a soft band, the line itself, and a brighter stub at a random run of the width
        drawRect(Brush.verticalGradient(0f to Color.Transparent, 0.5f to TerminalGreen.copy(alpha = 0.18f), 1f to Color.Transparent, startY = py - halo, endY = py + halo),
            topLeft = Offset(0f, py - halo), size = Size(size.width, halo * 2f))
        drawRect(TerminalGreen.copy(alpha = 0.7f), topLeft = Offset(0f, py), size = Size(size.width, 1.5f * density))
        val stub = size.width * 0.18f
        val x0 = (size.width - stub) * ((y * 7.31f) % 1f)
        drawRect(TerminalGreen, topLeft = Offset(x0, py - 0.5f * density), size = Size(stub, 2.5f * density))
    }
}

/** A label that types itself in when [enabled], else shows whole: the terminal writing the heading. */
@Composable
fun TypedText(text: String, color: Color, style: androidx.compose.ui.text.TextStyle, modifier: Modifier = Modifier, enabled: Boolean = true) {
    var shown by remember(text) { mutableIntStateOf(if (enabled) 0 else text.length) }
    LaunchedEffect(text, enabled) {
        if (!enabled) { shown = text.length; return@LaunchedEffect }
        // the roll lands after the page is up, so a heading already whole starts again
        shown = 0
        val step = (240L / text.length.coerceAtLeast(1)).coerceIn(8L, 40L)
        while (shown < text.length) {
            kotlinx.coroutines.delay(step)
            shown++
        }
    }
    androidx.compose.material3.Text(text.take(shown) + (if (shown < text.length) "▌" else ""), color = color, style = style, modifier = modifier)
}
