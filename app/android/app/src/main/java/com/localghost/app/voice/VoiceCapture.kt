package com.localghost.app.voice

import android.annotation.SuppressLint
import android.content.Context
import android.media.AudioFormat
import android.media.AudioRecord
import android.media.MediaRecorder
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.MutableStateFlow
import kotlinx.coroutines.flow.StateFlow
import kotlinx.coroutines.flow.distinctUntilChanged
import kotlinx.coroutines.flow.map
import java.io.BufferedOutputStream
import java.io.File
import java.io.FileOutputStream

/**
 * THE RECORDER , one voice note at a time, process-wide, so the note survives the check-in card
 * scrolling out of the list or the person switching tabs. The microphone goes straight into a WAV
 * in the app's no-backup folder (VoiceNotes.takesDir); nothing is sent until the person saves it,
 * and nothing leaves for anywhere but their own box.
 *
 * The screen is kept on while recording (the card sets it): Android gives a background app silence
 * from the microphone, so a note spoken with the phone locked would be a note of nothing. Twenty
 * minutes is the most one note holds.
 */
object VoiceCapture {
    data class Take(val id: String, val file: File, val startedAt: Long, val durationMs: Long)

    data class State(
        val recording: Boolean = false,
        val paused: Boolean = false,   // recording, the microphone open, nothing written until resume
        val elapsedMs: Long = 0,
        val level: Float = 0f,     // 0..1, the last tenth of a second's loudest sample, smoothed
        val take: Take? = null,    // a finished recording, not yet saved or discarded
        val error: String = "",
    )

    /** The state without the ten-a-second parts (the level, the millisecond clock): what a
     *  form or a button around the recorder needs, so it recomposes on a change of take or of
     *  recording, not ten times a second. [elapsedS] moves once a second. */
    data class Gist(val recording: Boolean = false, val paused: Boolean = false, val elapsedS: Long = 0, val take: Take? = null, val error: String = "")

    /** The gist as a flow that only moves when the gist does. */
    val gist: Flow<Gist> get() = _state.map { it.gist() }.distinctUntilChanged()

    const val MAX_MS = 20 * 60 * 1000L
    private const val MIN_MS = 700L

    private val _state = MutableStateFlow(State())
    val state: StateFlow<State> get() = _state

    @Volatile private var stopFlag = false
    @Volatile private var pauseFlag = false
    @Volatile private var recordingId: String? = null

    /** The id being recorded right now (recoverOrphans leaves that file alone). */
    fun activeId(): String? = recordingId ?: _state.value.take?.id

    /** Start a note. The caller has the RECORD_AUDIO permission. A take not yet saved is dropped. */
    @SuppressLint("MissingPermission")
    fun start(ctx: Context): Boolean {
        if (_state.value.recording) return true
        _state.value.take?.file?.delete()
        val rate = VoiceWav.RATE
        val min = AudioRecord.getMinBufferSize(rate, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT)
        if (min <= 0) {
            _state.value = State(error = "this phone's microphone will not record at 16 kHz")
            return false
        }
        val rec = try {
            AudioRecord(MediaRecorder.AudioSource.MIC, rate, AudioFormat.CHANNEL_IN_MONO,
                AudioFormat.ENCODING_PCM_16BIT, maxOf(min, rate)) // at least half a second of buffer
        } catch (e: Exception) {
            _state.value = State(error = "the microphone would not open (${e.message})")
            return false
        }
        if (rec.state != AudioRecord.STATE_INITIALIZED) {
            rec.release()
            _state.value = State(error = "the microphone is busy (a call, or another app recording)")
            return false
        }
        val id = VoiceNotes.newId()
        val file = File(VoiceNotes.takesDir(ctx), "$id.wav")
        val started = System.currentTimeMillis()
        stopFlag = false
        pauseFlag = false
        recordingId = id
        _state.value = State(recording = true)
        Thread({
            var err = ""
            try {
                BufferedOutputStream(FileOutputStream(file), 64 * 1024).use { out ->
                    out.write(VoiceWav.header(rate, 1, 16, 0))
                    rec.startRecording()
                    val buf = ShortArray(rate / 10)
                    val bytes = ByteArray(buf.size * 2)
                    var total = 0L
                    var level = 0f
                    var lastPub = -1000L
                    while (!stopFlag) {
                        val n = rec.read(buf, 0, buf.size)
                        if (n < 0) { err = "the microphone stopped (code $n)"; break }
                        if (n == 0) continue
                        if (pauseFlag) {
                            // paused: the microphone is read and dropped (a closed buffer would
                            // overflow), the clock stands, the level falls to nothing
                            if (!_state.value.paused || _state.value.level > 0f) _state.value = _state.value.copy(paused = true, level = 0f)
                            continue
                        }
                        if (_state.value.paused) _state.value = _state.value.copy(paused = false)
                        val peak = VoiceWav.pack(buf, n, bytes)
                        out.write(bytes, 0, n * 2)
                        total += n * 2
                        level = maxOf(peak / 32768f, level * 0.75f)
                        val ms = total * 1000 / (rate * 2)
                        if (ms - lastPub >= 100) {
                            lastPub = ms
                            _state.value = _state.value.copy(elapsedMs = ms, level = level)
                        }
                        if (ms >= MAX_MS) break
                    }
                }
            } catch (e: Exception) {
                err = "recording failed: ${e.message}"
            } finally {
                runCatching { rec.stop() }
                rec.release()
            }
            runCatching { VoiceWav.finish(file) }
            val dur = VoiceWav.durationMs(file)
            recordingId = null
            _state.value = if (dur < MIN_MS) {
                file.delete()
                State(error = err.ifEmpty { "too short to keep" })
            } else {
                State(take = Take(id, file, started, dur), error = err)
            }
        }, "voice-capture").start()
        return true
    }

    fun stop() { stopFlag = true }

    /** Hold the note: the clock stands, nothing is written, the microphone stays open; [resume]
     *  carries on in the same take. A stop while paused keeps what was said before the pause. */
    fun pause() { if (_state.value.recording) pauseFlag = true }
    fun resume() { pauseFlag = false }

    /** The take is kept by the caller (VoiceNotes.enqueue moved the file): the recorder is free. */
    fun taken() { _state.value = State() }

    fun discard() {
        _state.value.take?.file?.delete()
        _state.value = State()
    }
}

/** The state's gist (VoiceCapture.Gist). */
fun VoiceCapture.State.gist() = VoiceCapture.Gist(recording, paused, elapsedMs / 1000, take, error)
