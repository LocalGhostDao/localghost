package com.localghost.app.debug

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertNull
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The crash report is shown before the gate (second audit, 12 Nov 2026): it says where, and
 * never carries what was being read.
 */
class CrashHandlerTest {
    @Test fun aMessageIsOneShortLine() {
        assertNull(CrashHandler.oneLine(null))
        assertNull(CrashHandler.oneLine("  \n "))
        assertEquals("Value x at 3 of type String", CrashHandler.oneLine("Value x\n at 3\tof type   String"))
        val long = CrashHandler.oneLine("Unterminated string at character 9 of " + "the whole note the person wrote ".repeat(40))!!
        assertTrue(long.length < 200)
        assertTrue(long.endsWith("characters)"))
    }

    @Test fun theTraceNamesTypesAndFramesAndClipsWhatTheyCarried() {
        val secret = "dear diary, " + "the thing nobody may read ".repeat(30)
        val inner = IllegalStateException("could not parse: $secret")
        val t = RuntimeException("while reading\n$secret", inner)
        val out = CrashHandler.trace(t)
        assertTrue(out.startsWith("java.lang.RuntimeException: while reading dear diary"))
        assertTrue(out.contains("Caused by: java.lang.IllegalStateException: could not parse: dear diary"))
        assertTrue(out.contains("\tat com.localghost.app.debug.CrashHandlerTest"))
        // each message is cut: the report holds a line's worth of it, not the note
        assertTrue(out.lines().none { it.length > 400 })
        assertFalse(out.contains(secret))
        // a cause that is itself, and a long chain, both end
        val chain = generateSequence(Exception("0")) { Exception("n", it) }.take(30).last()
        assertEquals(6, CrashHandler.trace(chain).lines().count { !it.startsWith("\t") && it.isNotEmpty() })
    }
}
