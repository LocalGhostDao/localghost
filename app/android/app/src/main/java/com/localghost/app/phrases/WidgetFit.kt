package com.localghost.app.phrases

/**
 * THE WIDGET FITS ITS CELL. The launcher says how high the widget was placed or dragged
 * (OPTION_APPWIDGET_MIN_HEIGHT, dp); the words grow to fill it rather than sit in a corner of a
 * tall one. The look's sizes (SETTINGS on the widget: small, medium, large) are the size at the
 * widget's own two rows; a taller widget scales them by how much higher it is than the lines it
 * shows need, up to about double. Pure, so the tests can read it.
 */
object WidgetFit {
    /** The height (dp) the widget's lines take at scale one: the short widget's five lines, the
     *  tall widget's lines with the foot and the weather and what comes next under them. */
    const val SHORT_NEED_DP = 110
    const val TALL_NEED_DP = 200

    /** The factor the text sizes are multiplied by for a widget [heightDp] high. One at the
     *  two-row height or below; more as it grows; never past [MAX]. */
    fun scale(heightDp: Int, tall: Boolean): Float {
        if (heightDp <= 0) return 1f
        val need = if (tall) TALL_NEED_DP else SHORT_NEED_DP
        return (heightDp.toFloat() / need).coerceIn(1f, MAX)
    }

    const val MAX = 2f

    /** The sizes (sp) scaled, rounded to whole sp. */
    fun sizes(base: IntArray, scale: Float): IntArray = IntArray(base.size) { i -> Math.round(base[i] * scale) }
}
