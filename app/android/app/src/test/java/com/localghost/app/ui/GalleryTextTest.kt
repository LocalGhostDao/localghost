package com.localghost.app.ui

import org.junit.Assert.assertEquals
import org.junit.Test
import java.time.ZoneId

class GalleryTextTest {
    @Test fun durations() {
        assertEquals("", GalleryText.duration(0.0))
        assertEquals("0:07", GalleryText.duration(7.3))
        assertEquals("1:42", GalleryText.duration(101.6))
        assertEquals("1:03:05", GalleryText.duration(3785.0))
    }

    @Test fun sizes() {
        assertEquals("", GalleryText.size(0))
        assertEquals("813 KB", GalleryText.size(833_000)) // 813.48 KB, rounded
        assertEquals("814 KB", GalleryText.size(833_536))
        assertEquals("3.2 MB", GalleryText.size(3_355_000))
        assertEquals("1.4 GB", GalleryText.size(1_503_238_553))
    }

    @Test fun headingAndWhen() {
        assertEquals("> PHOTO", GalleryText.heading("photo", 0.0))
        assertEquals("> VIDEO · 0:42", GalleryText.heading("video", 42.0))
        assertEquals("> VIDEO", GalleryText.heading("video", 0.0))
        assertEquals("Sat 24 Oct 2026, 00:22 · 3.2 MB", GalleryText.taken(1792801320, 3_355_000, ZoneId.of("UTC")))
        assertEquals("3.2 MB", GalleryText.taken(0, 3_355_000))
    }

    @Test fun groupsInOrder() {
        val tags = listOf("cliff", "cristina", "sea", "hike", "waterfall")
        val by = mapOf("object" to listOf("cliff", "waterfall"), "people" to listOf("cristina"), "activity" to listOf("hike"))
        val g = GalleryText.groups(tags, by)
        assertEquals(listOf("people", "activity", "object", "other"), g.map { it.first })
        assertEquals(listOf("sea"), g.last().second) // uncategorised lands in other
        assertEquals(listOf("tags" to tags), GalleryText.groups(tags, emptyMap()))
        assertEquals(emptyList<Pair<String, List<String>>>(), GalleryText.groups(emptyList(), by))
    }

    @Test fun regroupAfterAnEdit() {
        val by = mapOf("people" to listOf("cristina"), "object" to listOf("cliff"))
        assertEquals(mapOf("people" to listOf("cristina")), GalleryText.regroup(by, listOf("cristina")))
        assertEquals(mapOf("people" to listOf("cristina"), "object" to listOf("cliff"), "other" to listOf("sunset")),
            GalleryText.regroup(by, listOf("cristina", "cliff", "sunset")))
    }
}
