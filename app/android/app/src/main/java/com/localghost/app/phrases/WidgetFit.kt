package com.localghost.app.phrases

/**
 * THE WIDGET FITS ITS CELL. The launcher says how high and how wide the widget was placed or
 * dragged (OPTION_APPWIDGET_MIN_HEIGHT / _WIDTH, dp); the words grow to fill it rather than sit
 * in a corner of a tall one. The look's sizes (SETTINGS on the widget: small, medium, large)
 * are the size at the widget's own two rows; a taller widget scales the phrase, its sound and
 * its meaning by how much higher it is than the lines it shows need, held back by the width
 * (a line that grows past the cell is cut, not read), never past [MAX]. The small lines (the
 * head, the prices and the story, the weather, the buttons) grow by a quarter of that at most: they are
 * the dim lines under the phrase, and the long ones run out of width first. Pure, so the
 * tests can read it.
 */
object WidgetFit {
    /** The height (dp) the widget's lines take at scale one: the short widget's head, brief,
     *  word, sound, meaning and buttons; the tall widget's with the weather line too. */
    const val SHORT_NEED_DP = 125
    const val TALL_NEED_DP = 145

    /** The width (dp) the widget's longest lines are laid out for at scale one. */
    const val WIDTH_NEED_DP = 280

    /** The phrase's factor: height over what the lines need, held back by the width, in
     *  [1, MAX]. One when the launcher said nothing. */
    fun scale(heightDp: Int, widthDp: Int, tall: Boolean): Float {
        if (heightDp <= 0) return 1f
        val need = if (tall) TALL_NEED_DP else SHORT_NEED_DP
        val byHeight = heightDp.toFloat() / need
        val byWidth = if (widthDp > 0) widthDp.toFloat() / WIDTH_NEED_DP else byHeight
        return minOf(byHeight, byWidth).coerceIn(1f, MAX)
    }

    /** The small lines' factor for a phrase factor [scale]: a quarter of the growth, at most
     *  [SMALL_MAX]. */
    fun smallScale(scale: Float): Float = (1f + (scale - 1f) * 0.25f).coerceIn(1f, SMALL_MAX)

    const val MAX = 1.6f
    const val SMALL_MAX = 1.15f

    /** The sizes (sp: local, say, en, the small lines) scaled, rounded to whole sp. */
    fun sizes(base: IntArray, scale: Float): IntArray = IntArray(base.size) { i ->
        Math.round(base[i] * (if (i == 3) smallScale(scale) else scale))
    }
}
