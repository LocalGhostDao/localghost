package com.localghost.app.ui

/**
 * THE GALLERY'S PAGES. The archive is read a page at a time, newest first, each page starting
 * after the last frame of the one before. Pure, for the tests.
 *
 * Two rules. The cursor is that frame's time AND its hash: by the time alone, a page that ended
 * inside a burst (several photos in one second) lost the rest of the burst, because the next page
 * began strictly before that second. And no frame is ever listed twice: the grid is keyed by
 * hash, and Compose stops the app on a key used twice.
 */
object GalleryPaging {
    /** Whether [s] is a frame's hash as the box gives it: hex, of a digest's length. Anything
     *  else is not sent as a cursor. */
    fun isHash(s: String): Boolean = s.length in 16..128 && s.all { it in '0'..'9' || it in 'a'..'f' }

    /** The hashes of [page] that are not already in [have], and once each. */
    fun fresh(have: List<String>, page: List<String>): Set<String> {
        val seen = have.toHashSet()
        val out = LinkedHashSet<String>()
        for (h in page) if (seen.add(h)) out.add(h)
        return out
    }
}
