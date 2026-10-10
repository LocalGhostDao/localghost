package com.localghost.app.phrases

import org.junit.Assert.assertArrayEquals
import org.junit.Assert.assertEquals
import org.junit.Test

class WidgetFitTest {
    @Test fun theWordsGrowWithTheCell() {
        // the two-row widget, or no word from the launcher: the look's own sizes
        assertEquals(1f, WidgetFit.scale(0, false))
        assertEquals(1f, WidgetFit.scale(110, false))
        assertEquals(1f, WidgetFit.scale(90, false))
        // three rows, the extra lines in: a little bigger; half the screen: much bigger
        assertEquals(1f, WidgetFit.scale(200, true))
        assertEquals(1.5f, WidgetFit.scale(300, true), 0.01f)
        assertEquals(WidgetFit.MAX, WidgetFit.scale(900, true))
        // a short widget pulled taller before the extra lines come grows too
        assertEquals(1.5f, WidgetFit.scale(165, false), 0.01f)
    }

    @Test fun sizesRoundToWholeSp() {
        assertArrayEquals(intArrayOf(22, 13, 12, 11), WidgetFit.sizes(intArrayOf(22, 13, 12, 11), 1f))
        assertArrayEquals(intArrayOf(33, 20, 18, 17), WidgetFit.sizes(intArrayOf(22, 13, 12, 11), 1.5f))
    }
}
