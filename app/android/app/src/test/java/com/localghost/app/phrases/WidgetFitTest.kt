package com.localghost.app.phrases

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Test

class WidgetFitTest {
    @Test fun theWordsGrowWithTheCellAndAreHeldByItsWidth() {
        // the two-row widget, or no word from the launcher: the look's own sizes
        assertEquals(1f, WidgetFit.scale(0, 0, false))
        assertEquals(1f, WidgetFit.scale(125, 300, false))
        assertEquals(1f, WidgetFit.scale(90, 300, false))
        // a half-screen widget four columns wide: the width holds it to a little over one
        assertEquals(330f / 280f, WidgetFit.scale(290, 330, true), 0.01f)
        // a wide tablet cell: the height decides, never past the cap
        assertEquals(1.45f, WidgetFit.scale(210, 800, true), 0.01f)
        assertEquals(WidgetFit.MAX, WidgetFit.scale(900, 900, true))
        // no width from the launcher: the height alone
        assertEquals(1.5f, WidgetFit.scale(188, 0, false), 0.01f)
    }

    @Test fun theSmallLinesBarelyGrow() {
        assertEquals(1f, WidgetFit.smallScale(1f))
        assertEquals(1.1f, WidgetFit.smallScale(1.4f), 0.001f)
        assertEquals(WidgetFit.SMALL_MAX, WidgetFit.smallScale(1.6f), 0.001f)
        assertEquals(WidgetFit.SMALL_MAX, WidgetFit.smallScale(1.8f), 0.001f)
    }

    @Test fun sizesRoundToWholeSp() {
        assertArrayEquals(intArrayOf(22, 13, 12, 11), WidgetFit.sizes(intArrayOf(22, 13, 12, 11), 1f))
        // the phrase at 1.4, the small lines at 1.1
        assertArrayEquals(intArrayOf(31, 18, 17, 12), WidgetFit.sizes(intArrayOf(22, 13, 12, 11), 1.4f))
    }
}
