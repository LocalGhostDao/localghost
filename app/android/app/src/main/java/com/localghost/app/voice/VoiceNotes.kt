package com.localghost.app.voice

import android.content.Context
import com.localghost.app.net.BoxHttp
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.sync.Mutex
import kotlinx.coroutines.sync.withLock
import kotlinx.coroutines.withContext
import org.json.JSONObject
import java.io.File
import java.util.UUID

/**
 * THE PHONE'S SIDE OF A VOICE NOTE , a saved take waits in noBackupFilesDir/voice/pending as
 * <id>.wav + <id>.json until the box has it (POST /v1/voice answers 202, or 200 when it already
 * had it), then both are deleted: the phone keeps no copy of what the box holds. Retried when the
 * app opens and by the quarter-hourly poll, so a note made away from home goes when the box is
 * reachable. The id is made here, so a retry after a lost answer is the same note on the box.
 */
object VoiceNotes {
    data class Pending(val id: String, val kind: String, val day: String, val takenAt: Long,
                       val durationMs: Long, val file: File)

    private val uploading = Mutex()

    private fun root(ctx: Context) = File(ctx.noBackupFilesDir, "voice")
    fun takesDir(ctx: Context) = File(root(ctx), "takes").apply { mkdirs() }
    /** Questions asked aloud in the chat: recorded here, heard once by the box, deleted. Nothing
     *  in this folder is ever queued or uploaded as a note. */
    fun asksDir(ctx: Context) = File(root(ctx), "asks").apply { mkdirs() }

    /** A question the app died holding (or left behind) is not a note: it goes. The one being
     *  recorded or heard now is left alone. */
    fun sweepAsks(ctx: Context) {
        val active = VoiceCapture.activeId()
        asksDir(ctx).listFiles()?.forEach { f ->
            if (f.name.substringBefore('.') != active && System.currentTimeMillis() - f.lastModified() > 60_000) f.delete()
        }
    }
    private fun pendingDir(ctx: Context) = File(root(ctx), "pending").apply { mkdirs() }

    fun newId(): String = UUID.randomUUID().toString().replace("-", "")

    /** A take the person saved: into the queue, with what the box needs to file it. */
    fun enqueue(ctx: Context, take: VoiceCapture.Take, kind: String, day: String): Pending {
        val dst = File(pendingDir(ctx), "${take.id}.wav")
        if (!take.file.renameTo(dst)) {
            take.file.copyTo(dst, overwrite = true)
            take.file.delete()
        }
        val p = Pending(take.id, kind, day, take.startedAt, take.durationMs, dst)
        writeMeta(ctx, p)
        return p
    }

    private fun writeMeta(ctx: Context, p: Pending) {
        val tmp = File(pendingDir(ctx), "${p.id}.json.tmp")
        tmp.writeText(JSONObject().put("id", p.id).put("kind", p.kind).put("day", p.day)
            .put("taken", p.takenAt).put("dur", p.durationMs).toString())
        tmp.renameTo(File(pendingDir(ctx), "${p.id}.json"))
    }

    /** What waits for the box, newest first. */
    fun pending(ctx: Context): List<Pending> = pendingDir(ctx).listFiles { f -> f.name.endsWith(".json") }
        ?.mapNotNull { f ->
            try {
                val o = JSONObject(f.readText())
                val id = o.optString("id")
                val wav = File(pendingDir(ctx), "$id.wav")
                if (id.isEmpty() || !wav.exists()) null
                else Pending(id, o.optString("kind", "journal"), o.optString("day"), o.optLong("taken"), o.optLong("dur"), wav)
            } catch (_: Exception) { null }
        }?.sortedByDescending { it.takenAt } ?: emptyList()

    /** The audio of a note still on the phone (pending, or the take in hand), for playback. */
    fun localFile(ctx: Context, id: String): File? {
        File(pendingDir(ctx), "$id.wav").let { if (it.exists()) return it }
        VoiceCapture.state.value.take?.let { if (it.id == id && it.file.exists()) return it.file }
        return null
    }

    /** Drop a note that never reached the box. */
    fun deleteLocal(ctx: Context, id: String) {
        File(pendingDir(ctx), "$id.wav").delete()
        File(pendingDir(ctx), "$id.json").delete()
    }

    /**
     * A take whose app died before it was saved or discarded (the process killed mid-note, the
     * phone restarted): the words are kept, as a note of the day it was made. The take being
     * recorded, or held on the card, is left alone.
     */
    fun recoverOrphans(ctx: Context) {
        val active = VoiceCapture.activeId()
        val fmt = java.text.SimpleDateFormat("yyyy-MM-dd", java.util.Locale.US)
        takesDir(ctx).listFiles { f -> f.name.endsWith(".wav") }?.forEach { f ->
            val id = f.name.removeSuffix(".wav")
            if (id == active || System.currentTimeMillis() - f.lastModified() < 2 * 60_000) return@forEach
            runCatching { VoiceWav.finish(f) }
            val dur = VoiceWav.durationMs(f)
            if (dur < 700 || id.length != 32) { f.delete(); return@forEach }
            val started = f.lastModified() - dur
            enqueue(ctx, VoiceCapture.Take(id, f, started, dur), "journal", fmt.format(java.util.Date(started)))
            android.util.Log.i("LocalGhost", "voice: recovered an unsaved note ($dur ms) into the queue")
        }
    }

    /** Send what waits. Returns how many the box took; the rest stay for the next try. */
    suspend fun uploadPending(ctx: Context): Int = uploading.withLock { withContext(Dispatchers.IO) {
        sweepAsks(ctx)
        recoverOrphans(ctx)
        var sent = 0
        for (p in pending(ctx).sortedBy { it.takenAt }) {
            val code = try {
                BoxHttp.postFile(ctx, "/v1/voice", p.file, "audio/wav", mapOf(
                    "X-Ghost-Voice-Id" to p.id,
                    "X-Ghost-Voice-Kind" to p.kind,
                    "X-Ghost-Voice-Day" to p.day,
                    "X-Ghost-Taken" to p.takenAt.toString()))
            } catch (e: Exception) {
                android.util.Log.i("LocalGhost", "voice: box not reachable, ${pending(ctx).size} note(s) wait (${e.message})")
                break // the box is not there: the rest wait too
            }
            if (code in 200..299) {
                deleteLocal(ctx, p.id)
                sent++
            } else {
                android.util.Log.w("LocalGhost", "voice: upload of ${p.id.take(8)} answered HTTP $code; kept for the next try")
                if (code == 503) break // locked or not enrolled: nothing else will go either
            }
        }
        sent
    } }
}
