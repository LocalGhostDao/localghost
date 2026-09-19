package com.localghost.app.phrases

import android.content.Context
import android.speech.tts.TextToSpeech
import java.util.Locale

/**
 * Says a phrase aloud through the phone's own TextToSpeech engine. On-device voices where the
 * phone has them installed; nothing here talks to a network, and nothing here is ours to leak.
 * One engine per process, warmed on first use; a request that lands before the engine is ready
 * is queued and spoken the moment it is. Slow mode is for learning the shape of a sentence, and
 * for the second attempt when the waiter did not catch the first.
 */
object PhraseSpeaker {
    private var tts: TextToSpeech? = null
    private var ready = false
    private var pending: (() -> Unit)? = null

    fun say(ctx: Context, text: String, localeTag: String, slow: Boolean = false) {
        val app = ctx.applicationContext
        val job = { speak(text, localeTag, slow) }
        val engine = tts
        if (engine != null && ready) {
            job()
            return
        }
        pending = job
        if (engine == null) {
            tts = TextToSpeech(app) { status ->
                ready = status == TextToSpeech.SUCCESS
                if (ready) pending?.invoke() else android.util.Log.w("LocalGhost", "phrases: TTS engine unavailable ($status)")
                pending = null
            }
        }
    }

    private fun speak(text: String, localeTag: String, slow: Boolean) {
        val engine = tts ?: return
        val loc = Locale.forLanguageTag(localeTag)
        val avail = engine.setLanguage(loc)
        if (avail == TextToSpeech.LANG_MISSING_DATA || avail == TextToSpeech.LANG_NOT_SUPPORTED) {
            android.util.Log.w("LocalGhost", "phrases: no TTS voice for $localeTag (install it in system TTS settings)")
        }
        engine.setSpeechRate(if (slow) 0.6f else 0.95f)
        engine.speak(text, TextToSpeech.QUEUE_FLUSH, null, "ghost.phrased")
    }

    /** Whether a voice for this language is installed , the screen says so instead of failing quietly. */
    fun hasVoice(localeTag: String): Boolean {
        val engine = tts ?: return true // unknown until warmed; assume yes rather than nag
        if (!ready) return true
        val r = engine.isLanguageAvailable(Locale.forLanguageTag(localeTag))
        return r != TextToSpeech.LANG_MISSING_DATA && r != TextToSpeech.LANG_NOT_SUPPORTED
    }

    fun shutdown() {
        tts?.shutdown()
        tts = null
        ready = false
    }
}
