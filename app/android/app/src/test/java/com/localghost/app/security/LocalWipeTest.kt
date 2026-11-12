package com.localghost.app.security

import java.io.File
import java.nio.file.Files
import org.junit.Assert.assertEquals
import org.junit.Assert.assertFalse
import org.junit.Assert.assertTrue
import org.junit.Test

/**
 * What leaves the phone's storage when it forgets a box (second audit, 12 Nov 2026): the wipe
 * used to leave the session's caches and the voice notes not yet sent.
 */
class LocalWipeTest {
    private fun tree(): Triple<File, File, File> {
        val base = Files.createTempDirectory("wipe").toFile()
        val files = File(base, "files"); val noBackup = File(base, "no_backup"); val cache = File(base, "cache")
        fun put(dir: File, path: String) = File(dir, path).apply { parentFile!!.mkdirs(); writeText("x") }
        put(files, "home/news.json"); put(files, "home/weather.json"); put(files, "last_crash.txt")
        put(files, "location-trail.log"); put(files, "location-recent.log")
        put(files, "landtiles/001_002.lgt"); put(files, "models/small.gguf"); put(files, "world.geojson")
        put(noBackup, "voice/pending/1.wav"); put(noBackup, "voice/pending/1.json"); put(noBackup, "other.bin")
        put(cache, "capture_1.jpg"); put(cache, "play-abc.tmp"); put(cache, "voice-play/7.wav"); put(cache, "server-update/MANIFEST.txt")
        return Triple(files, noBackup, cache)
    }

    @Test fun aNewBoxKeepsWhatThePersonMade() {
        val (files, noBackup, cache) = tree()
        LocalWipe.files(files, null, cache, voiceToo = false)
        assertFalse(File(files, "home").exists())
        assertFalse(File(files, "last_crash.txt").exists())
        assertEquals(0, cache.listFiles()!!.size)
        // the person's own, and public data, stay
        assertTrue(File(noBackup, "voice/pending/1.wav").exists())
        for (kept in listOf("location-trail.log", "location-recent.log", "landtiles/001_002.lgt", "models/small.gguf", "world.geojson"))
            assertTrue(kept, File(files, kept).exists())
    }

    @Test fun theWipeTakesTheVoiceNotesToo() {
        val (files, noBackup, cache) = tree()
        val n = LocalWipe.files(files, noBackup, cache, voiceToo = true)
        assertFalse(File(noBackup, "voice").exists())
        assertTrue(File(noBackup, "other.bin").exists())
        assertFalse(File(files, "home").exists())
        assertEquals(0, cache.listFiles()!!.size)
        assertTrue(File(files, "location-trail.log").exists()) // sealed to the box: only it opens it
        assertEquals(7, n) // home, the crash report, four cache entries, voice
        // a second run finds nothing, and a missing cache directory is no error
        assertEquals(0, LocalWipe.files(files, noBackup, File(cache, "gone"), voiceToo = true))
    }

    @Test fun theSessionListNamesTheSession() {
        assertTrue(LocalWipe.SESSION_PREFS.contains("lg_session"))
        // settings, the connection and the trail's own state are not session data
        for (kept in listOf("lg_settings", "lg_location", "lg_models", "box_config"))
            assertFalse(kept, LocalWipe.SESSION_PREFS.contains(kept))
    }
}
