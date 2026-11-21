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
 *
 * A QUESTION ASKED ALOUD (the chat's microphone) is the same recorder told it is an ask, and four
 * things differ. It listens through the source Android tunes for recognition. It stops by itself
 * when the person has finished (VoiceEndpoint), or after eight seconds of nobody speaking, or at
 * two minutes. What it keeps is cut to what was said. And its file is in a folder of its own
 * (VoiceNotes.asksDir) that nothing ever uploads: a question is heard once and gone, and a take
 * the app died holding is deleted when it is found, never filed as a note.
 */
object VoiceCapture {
    /** [ask]: a question for the chat, not a note. The check-in's card never shows one. */
    data class Take(val id: String, val file: File, val startedAt: Long, val durationMs: Long, val ask: Boolean = false)

    data class State(
        val recording: Boolean = false,
        val paused: Boolean = false,   // recording, the microphone open, nothing written until resume
        val elapsedMs: Long = 0,
        val level: Float = 0f,     // 0..1, the last tenth of a second's loudest sample, smoothed
        val take: Take? = null,    // a finished recording, not yet saved or discarded
        val error: String = "",
        // the take this state is about, recording or just ended ("" when the recorder is free);
        // whoever started it knows its own by this, whatever else has used the recorder since
        val id: String = "",
        val ask: Boolean = false,  // it is a question for the chat
        val heard: Boolean = false, // an ask: someone has spoken
        val speaking: Boolean = false, // an ask: they are speaking now
        // an ask that ended with no take: "nothing" (nobody spoke) or "dropped" (abandoned)
        val why: String = "",
    )

    /** The state without the ten-a-second parts (the level, the millisecond clock): what a
     *  form or a button around the recorder needs, so it recomposes on a change of take or of
     *  recording, not ten times a second. [elapsedS] moves once a second. */
    data class Gist(val recording: Boolean = false, val paused: Boolean = false, val elapsedS: Long = 0, val take: Take? = null, val error: String = "",
                    val id: String = "", val ask: Boolean = false, val heard: Boolean = false, val speaking: Boolean = false, val why: String = "")

    /** The gist as a flow that only moves when the gist does. */
    val gist: Flow<Gist> get() = _state.map { it.gist() }.distinctUntilChanged()

    const val MAX_MS = 20 * 60 * 1000L
    /** The most a question holds. The box takes four minutes of WAV; a question is not that long. */
    const val ASK_MAX_MS = 2 * 60 * 1000L
    private const val MIN_MS = 700L

    private val _state = MutableStateFlow(State())
    val state: StateFlow<State> get() = _state

    @Volatile private var stopFlag = false
    @Volatile private var pauseFlag = false
    @Volatile private var dropFlag = false
    @Volatile private var recordingId: String? = null

    /** The id being recorded right now (recoverOrphans leaves that file alone). */
    fun activeId(): String? = recordingId ?: _state.value.take?.id

    /** The microphone for a question: the source tuned for recognition where the phone has one
     *  that opens, the plain microphone where it has not. */
    @SuppressLint("MissingPermission")
    private fun open(source: Int, rate: Int, size: Int): AudioRecord? = try {
        AudioRecord(source, rate, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT, size)
            .let { if (it.state == AudioRecord.STATE_INITIALIZED) it else { it.release(); null } }
    } catch (_: Exception) { null }

    /** Why a question cannot be asked aloud just now, or "" when it can: the recorder is one, and
     *  a voice note being recorded, or recorded and not yet saved, is not given up for a question. */
    fun cannotAsk(): String {
        val st = _state.value
        return when {
            st.recording && !st.ask -> "a voice note is being recorded in CHECK-IN"
            st.take != null && st.take.ask.not() -> "a voice note is waiting in CHECK-IN: save it or drop it first"
            else -> ""
        }
    }

    /** Start a note, or with [ask] a question for the chat. The caller has the RECORD_AUDIO
     *  permission. A note's take not yet saved is dropped by a new note, never by a question:
     *  a question started while a note waits to be saved is refused, and says why. */
    @SuppressLint("MissingPermission")
    fun start(ctx: Context, ask: Boolean = false): Boolean {
        if (_state.value.recording) return _state.value.ask == ask // already going: true only for whoever it is going for
        if (ask && cannotAsk().isNotEmpty()) return false
        _state.value.take?.file?.delete()
        val rate = VoiceWav.RATE
        val min = AudioRecord.getMinBufferSize(rate, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT)
        if (min <= 0) {
            _state.value = State(error = "this phone's microphone will not record at 16 kHz")
            return false
        }
        val size = maxOf(min, rate) // at least half a second of buffer
        val rec = (if (ask) open(MediaRecorder.AudioSource.VOICE_RECOGNITION, rate, size) else null)
            ?: open(MediaRecorder.AudioSource.MIC, rate, size)
        if (rec == null) {
            _state.value = State(error = "the microphone is busy (a call, or another app recording)")
            return false
        }
        val id = VoiceNotes.newId()
        val file = File(if (ask) VoiceNotes.asksDir(ctx) else VoiceNotes.takesDir(ctx), "$id.wav")
        val started = System.currentTimeMillis()
        stopFlag = false
        pauseFlag = false
        dropFlag = false
        recordingId = id
        _state.value = State(recording = true, id = id, ask = ask)
        val ear = if (ask) VoiceEndpoint() else null
        val most = if (ask) ASK_MAX_MS else MAX_MS
        Thread({
            var err = ""
            var ended = ""  // an ask: why it stopped by itself
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
                        // a question: has the person finished, or has nobody spoken
                        val verdict = if (ear != null && n == buf.size) ear.feed(VoiceWav.rms(buf, n)) else VoiceEndpoint.Verdict.GO_ON
                        if (ms - lastPub >= 100) {
                            lastPub = ms
                            _state.value = _state.value.copy(elapsedMs = ms, level = level, heard = ear?.heard == true, speaking = ear?.speaking == true)
                        }
                        if (verdict == VoiceEndpoint.Verdict.ENDED) { ended = "silence"; break }
                        if (verdict == VoiceEndpoint.Verdict.NOTHING) { ended = "nothing"; break }
                        if (ms >= most) { ended = "max"; break }
                    }
                }
            } catch (e: Exception) {
                err = "recording failed: ${e.message}"
            } finally {
                runCatching { rec.stop() }
                rec.release()
            }
            runCatching { VoiceWav.finish(file) }
            if (ear != null) {
                // A QUESTION: kept only if someone spoke, and cut to what they said. Stopped by
                // hand before the ear was sure (a word or two, quickly) it is kept whole: the box
                // decides what is in it.
                val whole = VoiceWav.durationMs(file)
                val keep = ear.keep(whole)
                val state = when {
                    dropFlag -> { file.delete(); State(id = id, ask = true, why = "dropped") }
                    err.isNotEmpty() -> { file.delete(); State(id = id, ask = true, error = err) }
                    keep == null && (ended == "nothing" || whole < MIN_MS) -> { file.delete(); State(id = id, ask = true, why = "nothing") }
                    else -> {
                        if (keep != null) runCatching { VoiceWav.trim(file, keep.first, keep.second) }
                        State(take = Take(id, file, started, VoiceWav.durationMs(file), ask = true), id = id, ask = true, heard = keep != null, why = ended)
                    }
                }
                recordingId = null
                _state.value = state
                return@Thread
            }
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

    /** Give up the question [id]: recording, it stops and leaves no take; held, the take goes.
     *  Anything else the recorder is doing (a note, another question) is left alone. */
    fun dropAsk(id: String) {
        if (id.isEmpty()) return
        if (recordingId == id) { dropFlag = true; stopFlag = true; return }
        val st = _state.value
        if (st.id == id && st.ask && !st.recording) {
            st.take?.file?.delete()
            _state.value = State()
        }
    }

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
fun VoiceCapture.State.gist() = VoiceCapture.Gist(recording, paused, elapsedMs / 1000, take, error, id, ask, heard, speaking, why)
