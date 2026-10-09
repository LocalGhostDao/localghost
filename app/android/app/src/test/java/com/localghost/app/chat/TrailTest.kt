package com.localghost.app.chat

import org.junit.Assert.assertEquals
import org.junit.Test

class TrailTest {
    @Test fun aStatusLineBecomesAStep() {
        assertEquals("searching the web on this phone (Brave) for: ferry Corfu Albania", Trail.line("searching the web on this phone (Brave) for: ferry Corfu Albania…"))
        assertEquals("3 found, 2 read on this phone", Trail.line("3 found, 2 read on this phone , asking your box…"))
        assertEquals("no web search , the box has this (the box's own numbers)", Trail.line("no web search , the box has this (the box's own numbers)"))
        assertEquals("", Trail.line("asking your box…"))
        assertEquals("", Trail.line("your box's model is ready , asking your box…"))
        assertEquals("the box's Wikipedia: Kassiopi", Trail.line("the box's Wikipedia: Kassiopi"))
    }
}
