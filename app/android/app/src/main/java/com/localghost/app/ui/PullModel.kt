package com.localghost.app.ui

import kotlin.math.PI
import kotlin.math.abs
import kotlin.math.cos
import kotlin.math.sin

/** The geometry of the pull drawing (PullCanvas): where the sources, the box and the packets
 *  sit, and the STYLE each integration draws in: Wikipedia's pages turning on their way,
 *  the news in bursts of lines, the market as a ticker of bars, the weather falling as drops,
 *  the maps as tiles that slot into a grid beside the box, speech as a wave along the line.
 *  Pure, so the tests read it. */
object PullModel {
    data class Node(val x: Float, val y: Float)

    enum class Style { PAGES, BURST, TICKER, DROPS, TILES, WAVE, PLAIN }

    /** The style an integration draws in, by its id. */
    fun styleOf(id: String): Style = when (id) {
        "wikipedia" -> Style.PAGES
        "news" -> Style.BURST
        "crypto" -> Style.TICKER
        "weather" -> Style.DROPS
        "maps" -> Style.TILES
        "speech" -> Style.WAVE
        else -> Style.PLAIN
    }

    /** The sources' anchors, spread evenly down the left; one in the middle when alone. */
    fun sources(n: Int, w: Float, h: Float): List<Node> {
        if (n <= 0) return emptyList()
        val x = w * 0.14f
        return (0 until n).map { i -> Node(x, h * (i + 1f) / (n + 1f)) }
    }

    fun box(w: Float, h: Float): Node = Node(w * 0.86f, h / 2f)

    /** Where source i's packet is at phase t (0..1 along its line), eased so it leaves slowly
     *  and arrives slowly, on a line that bows towards the middle; a drop (DROPS) sags under
     *  its own weight instead, a tick (TICKER) jogs up and down on its way. [i] and [k] name
     *  the source and the packet, so a tick's jog is its own and the same every frame. */
    fun packet(from: Node, to: Node, t: Float, style: Style = Style.PLAIN, i: Int = 0, k: Int = 0): Node {
        val tt = t.coerceIn(0f, 1f)
        val e = (1 - cos(PI * tt.toDouble()).toFloat()) / 2f
        val x = from.x + (to.x - from.x) * e
        val mid = (from.y + to.y) / 2f
        val bow = (to.y - from.y) * 0.25f * sin(PI * e.toDouble()).toFloat()
        var y = if (from.y == to.y) mid else from.y + (to.y - from.y) * e - bow
        when (style) {
            Style.DROPS -> {
                // a sag, deepest two thirds along, of a tenth of the span, whatever the slope
                val span = abs(to.x - from.x)
                y += span * 0.10f * sin(PI * Math.pow(e.toDouble(), 1.5)).toFloat()
            }
            Style.TICKER -> y += tick(i, k, e) * abs(to.x - from.x) * 0.04f
            else -> {}
        }
        return Node(x, y)
    }

    /** A ticker's jog at [e] along the line: a walk of eight steps, each up or down by a hash
     *  of the source, the packet and the step, in [-1, 1]. */
    fun tick(i: Int, k: Int, e: Float): Float {
        val steps = 8
        val at = (e * steps).toInt().coerceIn(0, steps - 1)
        var v = 0f
        for (s in 0..at) {
            val h = (i * 73 + k * 31 + s * 17 + 11) % 7
            v += if (h < 3) -0.5f else if (h < 6) 0.5f else 0f
        }
        return v.coerceIn(-1f, 1f)
    }

    /** Each source's packet phase at clock c (seconds), offset so they do not arrive together;
     *  slower when steady. */
    fun phase(i: Int, n: Int, c: Float, steady: Boolean): Float {
        val period = if (steady) 6f else 1.8f
        val off = if (n <= 1) 0f else i.toFloat() / n
        return ((c / period + off) % 1f + 1f) % 1f
    }

    /** The packets on one line at once: a burst (the news) sends three close together, the
     *  rest one; the offsets are subtracted from the phase. */
    fun burst(style: Style): List<Float> = if (style == Style.BURST) listOf(0f, 0.07f, 0.14f) else listOf(0f)

    /** A page's apparent width as it turns on its way: |cos| of the clock, never quite gone. */
    fun pageFlip(c: Float): Float = 0.15f + 0.85f * abs(cos(c * 5f))

    /** The grid cell (0..8, a 3×3) the maps' packet lands in at clock c: round the grid, one a
     *  second, so the tiles fill and fill again. */
    fun tileSlot(c: Float): Int = ((c.toInt() % 9) + 9) % 9

    /** The wave's rise at [u] (0..1 along the line) and clock c: a sine that runs along the
     *  line, louder in the middle, for speech. In units of the line's length. */
    fun wave(u: Float, c: Float): Float {
        val envelope = sin(PI * u.coerceIn(0f, 1f).toDouble()).toFloat()
        return 0.035f * envelope * sin((u * 14f - c * 5f).toDouble()).toFloat()
    }
}
