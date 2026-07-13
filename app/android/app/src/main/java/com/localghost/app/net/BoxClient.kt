package com.localghost.app.net
import com.localghost.app.security.BoxConfig
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.flow.flowOn
import com.localghost.app.security.SessionStore

import android.content.Context
import com.localghost.app.chat.Attachment
import com.localghost.app.chat.Message
import com.localghost.app.notify.NotifyState
import com.localghost.app.sync.Command
import com.localghost.app.sync.CommandResult
import com.localghost.app.sync.Cursor
import com.localghost.app.sync.MediaKind
import kotlinx.coroutines.delay
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.flow
import java.io.InputStream
import org.json.JSONObject

data class PendingNotification(val daemonId: String, val title: String, val body: String)

/** A saved conversation. Lives on the box (synthd); the phone lists + loads, holds the active
 *  one in memory only. */
data class Conversation(val id: String, val title: String, val updatedLabel: String, val messageCount: Int)

/** A device enrolled against this persona. Sync state is per-device. */
data class DeviceInfo(
    val id: String, val name: String, val thisDevice: Boolean,
    val lastSync: String, val photos: Int, val videos: Int,
)

/** Settings, owned by the box (persona-scoped), reflected on the phone. */
data class BoxSettings(val allowMobileSync: Boolean, val notificationsMuted: Boolean)

/** A capability the chat turn may use. reachBeyondBox is the only one that leaves the box. */
data class ChatCapabilities(
    val reachBeyondBox: Boolean = false,
    val daemons: Set<String> = setOf("ghost.synthd"),   // synthd always; others opt-in
)

/** An external source the BOX connects to. The box holds the credentials; the phone never
 *  sees tokens — it only triggers enrollment and sees status. */
/** A phone-runnable model the box is offering. The box stores many; it advertises only the
 *  ones small enough to run on the phone. Bytes come from the box, never the open internet. */
data class PhoneModel(
    val id: String, val name: String, val detail: String, val sizeBytes: Long, val sha256: String?,
)

data class Connector(
    val id: String, val name: String, val connected: Boolean, val detail: String,
)

/** A daemon in the always-on harness, with its live state. */
data class DaemonStatus(
    val id: String,
    val role: String,        // what it does, one line
    val state: State,
    val detail: String,      // e.g. "1,240 photos processed"
    val lastRun: String,     // human relative time
) {
    enum class State { WORKING, IDLE, LISTENING, ERROR }
}

/** One extracted memory in the life-model. */
data class MemoryEntry(
    val id: String,
    val daemonId: String,    // which daemon produced it
    val title: String,
    val summary: String,
    val whenLabel: String,   // "today", "2 days ago"
)

/** A high-level snapshot of how much of the user's life the box has modelled. */
data class LifeContext(
    val memories: Int,
    val photos: Int,
    val videos: Int,
    val voiceNotes: Int,
    val lastUpdated: String,
)

/**
 * The phone's single point of contact with the daemon fleet over mTLS. Fully stubbed —
 * this is the ONLY file that changes when the box lands. The stubs return believable
 * data so every surface feels alive before ghost.secd exists.
 */
object BoxClient {
    /** Application context for calls made from context-free seams (the chat Flow). Set once at app
     *  start; application context only, never an Activity , no leak. */
    @Volatile var appCtx: android.content.Context? = null

    private const val CADENCE_MS = 15 * 60 * 1000L

    data class Session(val ok: Boolean)

    /**
     * Submit a PIN and stream unlock progress by polling the box once a second. Emits a snapshot
     * immediately (RESOLVE running), then a snapshot per poll until the account is open (done) or a
     * stage errors (failed). A HOT account returns every stage complete on the first poll, so the
     * flow emits one full snapshot and finishes , fast. A COLD account returns the stages finished so
     * far, so the flow emits a growing snapshot each second until READY.
     *
     * The poll response is identical in shape for every account; this code cannot and does not infer
     * whether the opened account is real or a decoy. The behaviour the PIN triggered (real/decoy/wipe)
     * happened on the box; here we only render progress.
     *
     * Seam: pollUnlock is the real GET against ghost.secd over the mTLS channel. The stub below drives
     * a believable cold sequence so the UI and tests are exercisable without the box.
     */
    fun submitPinStreaming(ctx: Context, pin: String): Flow<UnlockSnapshot> = flow {
        emit(UnlockSnapshot.initial())
        // Start the unlock on the box: POST the PIN. The box resolves it (real/decoy/wipe) and runs
        // the stage stream; we then poll once a second and render progress. This code cannot infer
        // whether the opened account is real or a decoy: the poll shape is identical for every
        // account. Whatever the PIN triggered happened on the box.
        try {
            val resp = BoxHttp.postJson(ctx, "/v1/unlock", JSONObject().put("pin", pin))
            // A correct PIN returns a fresh session token + its expiry. Persist both so the app can
            // carry the token (foreground + notification poller) and know when to prompt a re-unlock.
            // A wrong PIN / failed unlock returns no token; leave any prior session in place to expire.
            val tok = resp.optString("token", "")
            if (tok.isNotBlank()) {
                val expIso = resp.optString("expiresAt", "")
                val expSec = parseRfc3339ToEpochSec(expIso)
                if (expSec > 0) SessionStore.write(ctx, tok, expSec)
            }
        } catch (e: Exception) {
            // Diagnostic: include the URL actually being contacted and the exception class, so a
            // "cannot reach" failure says WHICH host/port failed and HOW (connection refused vs TLS
            // handshake vs not-enrolled) instead of a generic dead-end.
            val target = BoxConfig.read(ctx)?.baseUrl ?: "<not enrolled>"
            val kind = e.javaClass.simpleName
            emit(UnlockSnapshot.failed("could not reach $target [$kind: ${e.message}]"))
            return@flow
        }
        while (true) {
            val states = try {
                pollUnlock(ctx)
            } catch (e: Exception) {
                emit(UnlockSnapshot.failed("lost contact with the box: ${e.message}"))
                return@flow
            }
            val snap = UnlockSnapshot.from(states)
            emit(snap)
            if (snap.done || snap.failed != null) break
            delay(1000) // poll cadence: once a second
        }
    }.flowOn(Dispatchers.IO) // ALL blocking HttpsURLConnection I/O off the main thread (else
    //                          NetworkOnMainThreadException before the request even leaves the phone)

    /** Back-compat one-shot: unlocks and resolves when the stream reaches done. */
    suspend fun submitPin(ctx: Context, pin: String): Session {
        var ok = false
        submitPinStreaming(ctx, pin).collect { snap ->
            if (snap.done) ok = true
        }
        return Session(ok = ok)
    }

    /** Poll the box for the current unlock stage states, mapping the JSON to the app enums. */
    private suspend fun pollUnlock(ctx: Context): Map<UnlockStage, StageState> {
        val resp = BoxHttp.getJson(ctx, "/v1/unlock/poll")
        // THE KEY EXCHANGE HAPPENS HERE, not on the unlock POST. The box issues the session token once,
        // on the poll that reports a successful unlock (token + expiresAt ride alongside the stages).
        // This parse used to read ONLY the stages and drop the token on the floor , the box unlocked,
        // the app never had a session, and every authenticated call after (status, settings, uploads)
        // hit the appears-down 503. Capture and persist it the moment it appears.
        val tok = resp.optString("token", "")
        if (tok.isNotBlank()) {
            val expSec = parseRfc3339ToEpochSec(resp.optString("expiresAt", ""))
            if (expSec > 0) {
                SessionStore.write(ctx, tok, expSec)
                android.util.Log.i("LocalGhost", "session stored from unlock poll, expires epoch $expSec")
            } else {
                android.util.Log.w("LocalGhost", "unlock poll carried a token but expiresAt did not parse: '${resp.optString("expiresAt", "")}'")
            }
        }
        val out = mutableMapOf<UnlockStage, StageState>()
        val arr = resp.optJSONArray("stages") ?: return out
        for (i in 0 until arr.length()) {
            val o = arr.getJSONObject(i)
            val stage = stageFromName(o.optString("stage")) ?: continue
            out[stage] = stateFromName(o.optString("state"))
        }
        return out
    }

    private fun stageFromName(n: String): UnlockStage? = when (n) {
        "RESOLVE" -> UnlockStage.RESOLVE
        "UNSEAL" -> UnlockStage.UNSEAL
        "MOUNT" -> UnlockStage.MOUNT
        "START_DB" -> UnlockStage.START_DB
        "START_CACHE" -> UnlockStage.START_CACHE
        "DAEMONS" -> UnlockStage.DAEMONS
        "MODEL" -> UnlockStage.MODEL
        "READY" -> UnlockStage.READY
        else -> null
    }

    private fun stateFromName(n: String): StageState = when (n) {
        "RUNNING" -> StageState.RUNNING
        "SKIPPED" -> StageState.SKIPPED
        "COMPLETE" -> StageState.COMPLETE
        "ERRORED" -> StageState.ERRORED
        else -> StageState.PENDING
    }

    /** Cheap reachability check: mTLS GET /v1/health. Routing uses this to decide box vs on-phone
     *  model. Returns false (not enrolled / unreachable) rather than throwing. */
    suspend fun reachable(ctx: Context): Boolean = try {
        BoxHttp.getJson(ctx, "/v1/health").optBoolean("ok", false)
    } catch (e: Exception) {
        false
    }

    /** Lock the box: ask ghost.secd to spin the account down , stop its Postgres/Redis, unmount and
     *  luksClose the volume (the key leaves the kernel), and revoke the session. After this every call
     *  appears down until the next PIN unlock. Returns the ordered teardown steps the box reported (so
     *  the app can show the spin-down), or an empty list on any error , the caller still locks the app. */
    suspend fun lock(ctx: Context): List<StageProgress> = try {
        val resp = BoxHttp.postJson(ctx, "/v1/lock", org.json.JSONObject())
        // The box revoked the session server-side; drop the local copy so nothing carries a dead token.
        SessionStore.clear(ctx)
        val arr = resp.optJSONArray("steps") ?: org.json.JSONArray()
        val out = mutableListOf<StageProgress>()
        for (i in 0 until arr.length()) {
            val o = arr.getJSONObject(i)
            val stage = UnlockStage.fromName(o.optString("stage")) ?: continue
            out.add(StageProgress(stage, stateFromName(o.optString("state"))))
        }
        out
    } catch (e: Exception) {
        emptyList()
    }

    // --- CHAT (RAG over the life-model) ---
    sealed interface ChatChunk {
        data class Memories(val ids: List<String>) : ChatChunk
        data class Token(val text: String) : ChatChunk
        data object Done : ChatChunk
    }

    fun chat(
        @Suppress("UNUSED_PARAMETER") history: List<Message>,
        prompt: String,
        @Suppress("UNUSED_PARAMETER") convId: String?,
        @Suppress("UNUSED_PARAMETER") attachments: List<Attachment> = emptyList(),
        @Suppress("UNUSED_PARAMETER") caps: ChatCapabilities = ChatCapabilities(),
    ): Flow<ChatChunk> = flow {
        // REAL end-to-end: the box's own model answers , app -> secd -> ghost.synthd (the retrieval
        // seam, a passthrough until the memory index exists , NO context is injected yet, so answers
        // come from the model alone) -> ghost.oracled -> llama-server on the encrypted volume.
        // Non-streaming v1: the full answer arrives, then renders word-by-word. NO fake memories ,
        // the Memories chunk returns when synthd actually retrieves some.
        val ctx = appCtx ?: run { emit(ChatChunk.Token("(app context missing)")); emit(ChatChunk.Done); return@flow }
        val think = com.localghost.app.settings.AppSettings.thinkLevel(ctx)
        val body = org.json.JSONObject().put("prompt", prompt).put("think", think)
        val resp = try {
            // Deep-think on CPU is legitimately MINUTES , the default 30s read timeout was killing
            // real answers mid-generation and reporting "box did not answer" for a working model.
            BoxHttp.postJson(ctx, "/v1/chat", body, readTimeoutMs = 6 * 60_000)
        } catch (e: Exception) {
            android.util.Log.w("LocalGhost", "chat failed: ${e.message}")
            emit(ChatChunk.Token("The box did not answer , it may be locked, or the model is still loading. Check Box Status."))
            emit(ChatChunk.Done)
            return@flow
        }
        // TRANSPARENCY: the box tells us exactly what context it injected and why , show it before
        // the answer, the way the old stub faked it, except now every line is real. Empty array =
        // the model answered from its own knowledge alone, and we say nothing rather than pretend.
        val ctxArr = resp.optJSONArray("context")
        if (ctxArr != null && ctxArr.length() > 0) {
            val memories = (0 until ctxArr.length()).mapNotNull { i ->
                val o = ctxArr.optJSONObject(i) ?: return@mapNotNull null
                val when_ = o.optString("when", "")
                val src = o.optString("source", "archive")
                val snip = o.optString("snippet", "")
                if (snip.isBlank()) null
                else (if (when_.isNotBlank()) "$when_ , " else "") + "$src: $snip"
            }
            if (memories.isNotEmpty()) emit(ChatChunk.Memories(memories))
        }
        val out = resp.optString("output", "")
        if (out.isBlank()) {
            emit(ChatChunk.Token("The model returned nothing , check Box Status for ghost.oracled."))
        } else {
            for (word in out.split(" ")) { emit(ChatChunk.Token("$word ")); delay(18) }
        }
        emit(ChatChunk.Done)
    }

    // --- conversations (history, stored on the box) ---

    suspend fun conversations(@Suppress("UNUSED_PARAMETER") ctx: Context): List<Conversation> {
        delay(150)
        return listOf(
            Conversation("c1", "Rome trip recap", "2h ago", 8),
            Conversation("c2", "boat refit budget", "yesterday", 14),
            Conversation("c3", "diving log questions", "3 days ago", 5),
        )
    }

    /** Load a conversation's messages. STUB returns a tiny transcript. */
    suspend fun loadConversation(@Suppress("UNUSED_PARAMETER") id: String): List<Message> {
        delay(120)
        return listOf(
            Message(Message.Role.USER, "what did I do in Rome?"),
            Message(Message.Role.GHOST,
                "You spent an evening near the river in Trastevere, then a long dinner.",
                memoriesUsed = listOf("Rome, with Cristina, April")),
        )
    }

    /** Create a new (empty) conversation, returns its id. */
    suspend fun createConversation(@Suppress("UNUSED_PARAMETER") ctx: Context): String {
        delay(80); return "c" + System.currentTimeMillis()
    }

    suspend fun deleteConversation(@Suppress("UNUSED_PARAMETER") id: String) { delay(80) }

    // --- harness status ---
    suspend fun lifeContext(@Suppress("UNUSED_PARAMETER") ctx: Context): LifeContext {
        delay(120)
        return LifeContext(memories = 1284, photos = 8421, videos = 142, voiceNotes = 63,
            lastUpdated = "just now")
    }

    // Static role copy: what each daemon is FOR. The server's /v1/status reports live health per
    // service but not this human description, so it lives app-side and is merged with the live state.
    private val daemonRoles = mapOf(
        "ghost.framed" to "extracts moments from photos & video",
        "ghost.voiced" to "captures & transcribes voice notes",
        "ghost.noted" to "keeps your notes and writing",
        "ghost.cued" to "surfaces reflections from your life",
        "ghost.synthd" to "builds the life-model from what the box sees",
        "ghost.tallyd" to "keeps count of what matters",
        "ghost.shadowd" to "watches for manipulation in messages",
        "ghost.watchd" to "keeps the fleet honest",
    )

    // daemonStatuses reads the REAL supervisor status from /v1/status and maps each service onto the
    // UI's DaemonStatus. The server owns state (up/degraded/failed), restart count and last error;
    // the role text is merged from daemonRoles. A service the server reports that we have no role for
    // still shows, with its name as the role, so a new daemon is never invisible.
    suspend fun daemonStatuses(ctx: Context): List<DaemonStatus> {
        val resp = BoxHttp.getJson(ctx, "/v1/status")
        val services = resp.optJSONArray("services") ?: return emptyList()
        val out = ArrayList<DaemonStatus>(services.length())
        for (i in 0 until services.length()) {
            val s = services.getJSONObject(i)
            val name = s.optString("name")
            val code = s.optInt("code", 0)
            val state = s.optString("state", "")           // up / restarting / backoff / down
            val restarts = s.optInt("restarts", 0)
            val lastErr = s.optString("lastErr", "")
            out.add(
                DaemonStatus(
                    id = name,
                    role = daemonRoles[name] ?: name,
                    state = mapDaemonState(code, state),
                    detail = daemonDetail(code, restarts, lastErr),
                    lastRun = "",   // watchd sample time will fill this once watchd writes metrics
                )
            )
        }
        return out
    }

    // mapDaemonState turns the supervisor's health code + lifecycle state into the UI enum. Code 2
    // (failed) or a backoff/down lifecycle is ERROR; code 1 (degraded) shows as IDLE (up, but not
    // fully healthy); code 0 up is WORKING. This is deliberately conservative: anything not clearly
    // healthy reads as not-WORKING so the screen never over-reassures.
    private fun mapDaemonState(code: Int, state: String): DaemonStatus.State = when {
        code >= 2 || state == "backoff" || state == "down" -> DaemonStatus.State.ERROR
        code == 1 -> DaemonStatus.State.IDLE
        state == "restarting" -> DaemonStatus.State.IDLE
        else -> DaemonStatus.State.WORKING
    }

    private fun daemonDetail(code: Int, restarts: Int, lastErr: String): String {
        val parts = ArrayList<String>()
        if (lastErr.isNotBlank()) parts.add(lastErr)
        if (restarts > 0) parts.add("restarted ${restarts}×")
        if (parts.isEmpty()) parts.add(if (code == 0) "healthy" else "degraded")
        return parts.joinToString(" · ")
    }

    // --- memories (life-model timeline) ---
    suspend fun memories(@Suppress("UNUSED_PARAMETER") ctx: Context): List<MemoryEntry> {
        delay(150)
        return listOf(
            MemoryEntry("m1", "ghost.framed", "Morning dive, Isabela",
                "Early boat out past the bay; sea lions near the rocks. Bright, calm water.", "today"),
            MemoryEntry("m2", "ghost.voiced", "Voice note — boat idea",
                "Thinking through a refit; sketched a rough budget out loud on the walk home.", "2 days ago"),
            MemoryEntry("m3", "ghost.framed", "Rome, with Cristina",
                "Evening near the river, long dinner, the light everyone photographs.", "3 weeks ago"),
            MemoryEntry("m4", "ghost.cued", "A pattern worth noting",
                "You return to the water whenever a big decision is near. Worth sitting with.", "last month"),
        )
    }

    // --- notifications ---
    suspend fun pollPending(ctx: Context): List<PendingNotification> {
        delay(150)
        if (NotifyState.isMuted(ctx)) return emptyList()
        val now = System.currentTimeMillis()
        if (now - NotifyState.lastPostedAt(ctx) < CADENCE_MS) return emptyList()
        NotifyState.setLastPostedAt(ctx, now)
        return listOf(
            PendingNotification("ghost.watchd", "Dog check",
                "Paul please don't get another dog, 10 is enough."),
            PendingNotification("ghost.cued", "Reflection waiting",
                "A question is ready when you have a moment."),
        )
    }

    // --- sync ---
    /** Which of these content hashes the box already has. EMPTY on any failure , the caller then
     *  uploads everything, because skipping on uncertainty is how photos get silently lost, while
     *  uploading a duplicate costs only bandwidth (the box dedups by the same hash). */
    suspend fun framesHave(ctx: Context, hashes: List<String>): Set<String> = try {
        val body = org.json.JSONObject().put("hashes", org.json.JSONArray(hashes))
        val r = BoxHttp.postJson(ctx, "/v1/frames/exists", body)
        val arr = r.optJSONArray("have") ?: return emptySet()
        (0 until arr.length()).mapNotNull { arr.optString(it).takeIf { h -> h.isNotBlank() } }.toSet()
    } catch (e: Exception) {
        android.util.Log.w("LocalGhost", "frames/exists failed (will upload everything): ${e.message}")
        emptySet()
    }

    /** One gallery entry from the box's archive. */
    data class GalleryFrame(val hash: String, val takenAt: Long, val kind: String, val bytes: Long)

    /** Page the archive newest-first. before=0 for the first page; pass the last row's takenAt to
     *  continue. Empty list on failure or end of archive. */
    suspend fun framesList(ctx: Context, before: Long = 0, limit: Int = 60): List<GalleryFrame> = try {
        val r = BoxHttp.getJson(ctx, "/v1/frames/list?before=$before&limit=$limit")
        val arr = r.optJSONArray("frames") ?: return emptyList()
        (0 until arr.length()).mapNotNull { i ->
            val o = arr.optJSONObject(i) ?: return@mapNotNull null
            GalleryFrame(o.optString("hash"), o.optLong("takenAt"), o.optString("kind"), o.optLong("bytes"))
        }
    } catch (e: Exception) {
        android.util.Log.w("LocalGhost", "frames/list failed: ${e.message}")
        emptyList()
    }

    /** One thumbnail's bytes (webp or jpeg). Null when the frame has none (videos) or on failure. */
    suspend fun frameThumb(ctx: Context, hash: String): ByteArray? =
        BoxHttp.getBytes(ctx, "/v1/frames/thumb?hash=$hash")

    /** The box's "where was I": newest taken_at per kind already archived. The sync seeds its local
     *  cursor from this, so a killed or reinstalled app resumes from what the box HAS instead of
     *  re-offering the whole roll. Returns (photoMs, videoMs); (0,0) on any failure , caller falls
     *  back to the local cursor alone. Box stores seconds; the cursor speaks millis, hence *1000. */
    suspend fun framesLatest(ctx: Context): Pair<Long, Long> = try {
        val r = BoxHttp.getJson(ctx, "/v1/frames/latest")
        (r.optLong("photoTakenAt", 0) * 1000) to (r.optLong("videoTakenAt", 0) * 1000)
    } catch (e: Exception) {
        android.util.Log.w("LocalGhost", "frames/latest unavailable: ${e.message}")
        0L to 0L
    }

    /** What to sync next. The cursor is LOCAL (SyncCursor prefs): the box does not yet track per-device
     *  positions, so the phone remembers where it got to and never re-sends the whole camera roll. */
    suspend fun nextCameraCommand(ctx: Context, kind: MediaKind): Command =
        Command.SyncCamera(kind, com.localghost.app.sync.SyncCursor.get(ctx, kind))

    /** REAL upload: stream the photo bytes to secd's spool endpoint over the pinned mTLS channel.
     *  202 Accepted = spooled for ghost.framed. The box ignores the name on purpose (it trusts only
     *  the bytes + their EXIF); it is kept here for progress display. */
    suspend fun ingest(
        ctx: Context,
        @Suppress("UNUSED_PARAMETER") kind: MediaKind,
        name: String,
        body: InputStream,
        takenAtMs: Long = 0,
    ): Boolean = try {
        val code = BoxHttp.postStream(ctx, "/v1/frames/upload", body, "image/*", takenAtMs)
        if (code != 202) {
            // The box answered but refused. 503 = appears-down (bad session / locked / not enrolled
            // right); anything else is unexpected. Named in logcat so a failing sync says WHY.
            android.util.Log.w("LocalGhost", "ingest $name: box answered HTTP $code (expected 202)")
        }
        code == 202
    } catch (e: Exception) {
        // Do NOT eat this. Cursor does not advance (retried next run), but the reason is in logcat.
        android.util.Log.w("LocalGhost", "ingest $name failed: ${e.javaClass.simpleName}: ${e.message}")
        false
    }

    // --- persona / pins (scoped to the mounted persona) ---

    /**
     * PINs for the CURRENTLY MOUNTED persona only. The box cannot return another persona's
     * pins — it lacks the keys while this persona is mounted. STUB returns a sample set.
     */
    // --- devices (per-device sync state, deduped index) ---

    suspend fun devices(@Suppress("UNUSED_PARAMETER") ctx: Context): List<DeviceInfo> {
        delay(150)
        return listOf(
            DeviceInfo("d1", "this phone", true, "just now", 8421, 142),
            DeviceInfo("d2", "old pixel", false, "3 days ago", 5102, 88),
        )
    }

    // --- settings (box-owned, persona-scoped; phone caches for offline) ---

    suspend fun settings(@Suppress("UNUSED_PARAMETER") ctx: Context): BoxSettings {
        delay(120); return BoxSettings(allowMobileSync = false, notificationsMuted = false)
    }

    suspend fun setSettings(
        @Suppress("UNUSED_PARAMETER") ctx: Context,
        @Suppress("UNUSED_PARAMETER") s: BoxSettings,
    ): Boolean { delay(200); return true }

    // --- on-phone models (served by the box) ---

    /** Models the box offers for the phone to run. STUB. Real: mTLS GET to the box registry. */
    suspend fun availableModels(ctx: Context): List<PhoneModel> {
        // The box advertises the phone-runnable models it serves from its unencrypted shared model
        // area (no account needed). mTLS GET to the registry; the phone downloads and runs locally.
        return try {
            val resp = BoxHttp.getJson(ctx, "/v1/models")
            val arr = resp.optJSONArray("models") ?: return emptyList()
            buildList {
                for (i in 0 until arr.length()) {
                    val o = arr.getJSONObject(i)
                    add(
                        PhoneModel(
                            id = o.optString("id"),
                            name = o.optString("name"),
                            detail = o.optString("detail"),
                            sizeBytes = o.optLong("sizeBytes"),
                            sha256 = o.optString("sha256", null),
                        )
                    )
                }
            }
        } catch (e: Exception) {
            emptyList() // box unreachable: no box-served models; on-phone models still work
        }
    }

    /**
     * Stream a model's bytes FROM THE BOX, resumable via an offset. Returns an InputStream the
     * caller writes to disk. STUB returns an empty stream of the right length-ish; real: mTLS
     * GET /models/{id} with Range. The phone writes to filesDir/models and tracks it locally.
     */
    suspend fun downloadModel(
        ctx: Context,
        id: String,
        offset: Long,
    ): java.io.InputStream {
        // mTLS GET /v1/models/{id}; Range header makes the download resumable from offset. The
        // caller writes to filesDir/models and verifies the SHA-256 from the catalogue after.
        return BoxHttp.openStream(ctx, "/v1/models/$id", offset)
    }

    // --- chat capabilities + connectors (box-side) ---

    /** Daemons the box can expose to chat as optional tools (synthd is implicit/always). */
    suspend fun availableChatDaemons(@Suppress("UNUSED_PARAMETER") ctx: Context): List<String> {
        delay(80)
        return listOf("ghost.framed", "ghost.voiced", "ghost.shadowd")
    }

    /** Connectors the box knows about, with connection status. STUB. */
    suspend fun connectors(@Suppress("UNUSED_PARAMETER") ctx: Context): List<Connector> {
        delay(150)
        return listOf(
            Connector("gmail", "Gmail", false, "read mail into your index"),
            Connector("gdrive", "Google Drive", false, "index your documents"),
            Connector("gcal", "Calendar", false, "your schedule as context"),
        )
    }

    /**
     * Begin connecting an external source. Real flow: the box runs OAuth and stores the token;
     * the phone only kicks it off and polls status. STUB flips to connected.
     */
    suspend fun connect(@Suppress("UNUSED_PARAMETER") id: String): Boolean { delay(600); return true }
    suspend fun disconnect(@Suppress("UNUSED_PARAMETER") id: String): Boolean { delay(300); return true }

    // --- data control ---

    /**
     * Pull the full index/memories from the box as JSON. STUB returns a representative
     * dump. Real: authenticated GET against the box; the daemons serialise their index.
     */
    suspend fun exportJson(@Suppress("UNUSED_PARAMETER") ctx: Context): String {
        delay(500)
        return """
{
  "export_version": 1,
  "source": "localghost.box",
  "generated": "${'$'}{System.currentTimeMillis()}",
  "life_context": { "memories": 1284, "photos": 8421, "videos": 142, "voice_notes": 63 },
  "memories": [
    { "id": "m1", "daemon": "ghost.framed", "when": "today", "title": "Morning dive, Isabela" },
    { "id": "m2", "daemon": "ghost.voiced", "when": "2 days ago", "title": "Voice note - boat idea" }
  ],
  "note": "stub export - ghost.secd will return the full signed index"
}
""".trimIndent()
    }

    /**
     * Destroy the persona's wrapping key on the box (crypto-erase) and clear local state.
     * STUB: returns true. Real: authenticated wipe command; the box destroys the key slot.
     */
    suspend fun wipeEverything(@Suppress("UNUSED_PARAMETER") ctx: Context): Boolean {
        delay(800); return true
    }

    /**
     * Change the PIN. On the box this re-derives the persona key under a new PIN, which
     * cannot preserve the old data - the old wrapping key is destroyed. So changing the
     * PIN wipes. STUB: returns true. Real: ghost.secd re-keys and crypto-erases the old slot.
     */
    /**
     * Ingest a chat attachment to the box index using the SAME raw-bytes path as camera
     * sync, so the content hash matches and dedup links them — the same item attached in
     * chat and later swept by camera sync becomes one memory, extracted once.
     */
    suspend fun ingestAttachment(
        ctx: Context,
        att: Attachment,
        body: java.io.InputStream,
    ): Boolean = try {
        // Same endpoint, same raw bytes as camera sync , so the content hash matches on the box and
        // framed dedups the chat copy against the camera-swept copy into ONE memory.
        val code = BoxHttp.postStream(ctx, "/v1/frames/upload", body, "application/octet-stream")
        if (code != 202) android.util.Log.w("LocalGhost", "attachment ${att.name}: box answered HTTP $code")
        code == 202
    } catch (e: Exception) {
        android.util.Log.w("LocalGhost", "attachment ${att.name} failed: ${e.javaClass.simpleName}: ${e.message}")
        false
    }

    suspend fun report(@Suppress("UNUSED_PARAMETER") result: CommandResult) {}
    /** Parse an RFC3339 UTC timestamp (as the box emits) to epoch seconds, or 0 on failure. */
    private fun parseRfc3339ToEpochSec(iso: String): Long {
        if (iso.isBlank()) return 0
        return try {
            java.time.Instant.parse(iso).epochSecond
        } catch (e: Exception) {
            0
        }
    }

}
