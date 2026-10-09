package com.localghost.app.ui

import kotlin.math.PI
import kotlin.math.sin

/** The geometry of the pull drawing (PullCanvas): where the sources, the box and the packets
 *  sit. Pure, so the tests read it. */
object PullModel {
    data class Node(val x: Float, val y: Float)

    /** The sources' anchors, spread evenly down the left; one in the middle when alone. */
    fun sources(n: Int, w: Float, h: Float): List<Node> {
        if (n <= 0) return emptyList()
        val x = w * 0.14f
        return (0 until n).map { i -> Node(x, h * (i + 1f) / (n + 1f)) }
    }

    fun box(w: Float, h: Float): Node = Node(w * 0.86f, h / 2f)

    /** Where source i's packet is at phase t (0..1 along its line), eased so it leaves slowly
     *  and arrives slowly, on a line that bows towards the middle. */
    fun packet(from: Node, to: Node, t: Float): Node {
        val e = (1 - Math.cos(PI * t.coerceIn(0f, 1f).toDouble()).toFloat()) / 2f
        val x = from.x + (to.x - from.x) * e
        val mid = (from.y + to.y) / 2f
        val y = from.y + (to.y - from.y) * e - (to.y - from.y) * 0.25f * sin(PI * e.toDouble()).toFloat()
        return Node(x, if (from.y == to.y) mid else y)
    }

    /** Each source's packet phase at clock c (seconds), offset so they do not arrive together;
     *  slower when steady. */
    fun phase(i: Int, n: Int, c: Float, steady: Boolean): Float {
        val period = if (steady) 6f else 1.8f
        val off = if (n <= 1) 0f else i.toFloat() / n
        return ((c / period + off) % 1f + 1f) % 1f
    }
}
