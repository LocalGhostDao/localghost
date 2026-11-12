package com.localghost.app.local

import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Test

/** The box's id for a model names a file in the models directory, never a path out of it. */
class ModelStoreTest {
    @Test fun anIdIsAFileName() {
        assertEquals("gemma-3n-e4b_q4.v2", ModelStore.safeId("gemma-3n-e4b_q4.v2"))
        for (id in listOf("../../shared_prefs/lg_settings", "..", "a/b", "a\\b", "/etc/passwd", ".hidden", "a\u0000b", "")) {
            val s = ModelStore.safeId(id)
            assertFalse(id, s.contains('/') || s.contains('\\') || s.startsWith(".") || s.isEmpty() || s.contains('\u0000'))
        }
        assertEquals("model", ModelStore.safeId(""))
        assertEquals("model", ModelStore.safeId("..."))
        assertEquals(120, ModelStore.safeId("a".repeat(500)).length)
    }
}
