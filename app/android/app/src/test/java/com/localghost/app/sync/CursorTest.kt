package com.localghost.app.sync

import org.junit.Assert.assertEquals
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * The sync walks the camera roll by each picture's own date. One dated in the future must not
 * carry the cursor past everything taken before that date (12 Nov 2026).
 */
class CursorTest {
    private val now = 1_794_000_000_000L // 12 Nov 2026

    /** What the roll's query offers: after the cursor by (date, id), as CameraReader selects. */
    private fun offered(c: Cursor, date: Long, id: Long) = date > c.dateTaken || (date == c.dateTaken && id > c.id)

    @Test fun anOrdinaryPictureMovesTheCursorToItself() {
        assertEquals(Cursor(now - 60_000, 41), Cursor.afterConfirmed(now - 60_000, 41, now))
        assertEquals(Cursor(now, 42), Cursor.afterConfirmed(now, 42, now))
    }

    @Test fun aPictureDatedNextYearDoesNotTakeTheCursorThere() {
        val nextYear = now + 365L * 86_400_000
        val c = Cursor.afterConfirmed(nextYear, 500, now)
        assertEquals(Cursor(now, 500), c)
        // a picture taken an hour later is still offered; with the date taken as it stood, it was not
        assertTrue(offered(c, now + 3_600_000, 501))
        assertTrue(!offered(Cursor(nextYear, 500), now + 3_600_000, 501))
        // the future-dated one is offered again (and skipped by hash), not lost
        assertTrue(offered(c, nextYear, 500))
    }

    @Test fun aKeptPositionInTheFutureStartsTheRollAgain() {
        assertEquals(Cursor(now - 5_000, 7), Cursor.fromBox(now - 5_000, 7, now))
        assertEquals(Cursor(now + 3_600_000, 7), Cursor.fromBox(now + 3_600_000, 7, now)) // a clock an hour out is not a broken cursor
        assertEquals(Cursor.BEGINNING, Cursor.fromBox(now + 2 * 86_400_000L, 7, now))
        assertEquals(Cursor.BEGINNING, Cursor.fromBox(0, 0, now))
    }
}
