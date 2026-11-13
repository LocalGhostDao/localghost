package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

class GalleryPagingTest {
    @Test fun aFrameIsListedOnce() {
        val have = listOf("aa11", "bb22")
        // an older box pages by time alone and can hand the boundary frame back; a page can repeat one
        assertEquals(setOf("cc33", "dd44"), GalleryPaging.fresh(have, listOf("bb22", "cc33", "cc33", "dd44")))
        assertEquals(emptySet<String>(), GalleryPaging.fresh(have, listOf("aa11", "bb22")))
        assertEquals(listOf("cc33", "dd44"), GalleryPaging.fresh(emptyList(), listOf("cc33", "dd44")).toList()) // in the page's order
    }

    @Test fun onlyAHashIsSentAsACursor() {
        assertTrue(GalleryPaging.isHash("ecbdb04d50f3d592321f0f37e5e3e70f"))
        assertTrue(GalleryPaging.isHash("a".repeat(64)))
        assertFalse(GalleryPaging.isHash(""))
        assertFalse(GalleryPaging.isHash("ECBDB04D50F3D592321F0F37E5E3E70F")) // the box writes lower case
        assertFalse(GalleryPaging.isHash("ecbdb04d50f3d592321f0f37e5e3e70f&limit=9999"))
        assertFalse(GalleryPaging.isHash("../../etc/passwd"))
        assertFalse(GalleryPaging.isHash("abc"))
    }
}
