package com.localghost.app.net

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

    private const val CADENCE_MS = 15 * 60 * 1000L

    private fun JSONObject.optStringOrNull(key: String): String? =
        if (has(key) && !isNull(key)) getString(key) else null

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
            BoxHttp.postJson(ctx, "/v1/unlock", JSONObject().put("pin", pin))
        } catch (e: Exception) {
            emit(UnlockSnapshot.failed("could not reach the box: ${e.message}"))
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
    }

    /** Back-compat one-shot: unlocks and resolves when the stream reaches done. */
    suspend fun submitPin(ctx: Context, pin: String): Session {
        var ok = false
        submitPinStreaming(ctx, pin).collect { snap ->
            if (snap.done) ok = true
        }
        return Session(ok = ok)
    }

    /** Poll the box for the current unlock stage states, mapping the JSON to the app enums. */
    private fun pollUnlock(ctx: Context): Map<UnlockStage, StageState> {
        val resp = BoxHttp.getJson(ctx, "/v1/unlock/poll")
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

    /**
     * Enrollment result. On success the box has registered this device against a persona and
     * returned a device token; the caller persists baseUrl + token via BoxConfig.
     * STUB: validates inputs shallowly and echoes a fake token. Real (ghost.secd): the phone
     * generates a keypair (DeviceIdentity), sends a CSR + the pairing code to baseUrl, the box
     * mounts the persona the code selects, signs the cert, and returns the device token.
     */
    data class Enrollment(
        val ok: Boolean,
        val deviceToken: String = "",
        val deviceCertPem: String? = null, // box-issued device cert, delivered on enroll
        val deviceKeyPem: String? = null,  // its PKCS8 private key (box-generated, stored via DeviceCert)
        val error: String? = null,
    )

    suspend fun enroll(
        baseUrl: String,
        pairingCode: String,
        deviceName: String,
        certFingerprint: String,
    ): Enrollment {
        if (baseUrl.isBlank() || pairingCode.isBlank()) {
            return Enrollment(ok = false, error = "box address and pairing code are required")
        }
        // Bootstrap call: pin the box server cert by the fingerprint from the QR (BoxTrust) and POST
        // the pairing code. No device cert is presented yet (this is the call that gets us one).
        // nginx allows /v1/enroll without a client cert; every other route requires it. The box
        // validates the code via its rate-limited gate and returns a device token (and, in the
        // box-generates model, the device cert/key). The caller stores cert/key via DeviceCert.store
        // and token/fingerprint/baseUrl via BoxConfig; thereafter every call presents the device
        // cert for mTLS, which nginx checks at the handshake before any account/PIN.
        return try {
            val body = JSONObject()
                .put("pairingCode", pairingCode)
                .put("deviceName", deviceName)
            val resp = BoxHttp.enroll(baseUrl, certFingerprint, body)
            if (resp.optBoolean("ok", false)) {
                Enrollment(
                    ok = true,
                    deviceToken = resp.optString("deviceToken", ""),
                    deviceCertPem = resp.optStringOrNull("deviceCertPem"),
                    deviceKeyPem = resp.optStringOrNull("deviceKeyPem"),
                )
            } else {
                Enrollment(ok = false, error = resp.optString("error", "enrolment refused"))
            }
        } catch (e: Exception) {
            Enrollment(ok = false, error = "could not reach the box: ${e.message}")
        }
    }

    /** Cheap reachability check: mTLS GET /v1/health. Routing uses this to decide box vs on-phone
     *  model. Returns false (not enrolled / unreachable) rather than throwing. */
    suspend fun reachable(ctx: Context): Boolean = try {
        BoxHttp.getJson(ctx, "/v1/health").optBoolean("ok", false)
    } catch (e: Exception) {
        false
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
        delay(400)
        emit(ChatChunk.Memories(listOf(
            "Galapagos dive log - Isabela",
            "Photos: Rome with Cristina, April",
            "Voice note: boat idea, last week",
        )))
        val att = if (attachments.isEmpty()) "" else "I can see your ${attachments.size} attachment(s) too. "
        val reach = if (caps.reachBeyondBox) "(reaching beyond the box for this one) " else ""
        val reply = reach + att + "Drawing on your memories: about \"$prompt\" — here is what I can piece " +
            "together from your synced life. (Stubbed; ghost.synthd will retrieve and generate.)"
        for (word in reply.split(" ")) { emit(ChatChunk.Token("$word ")); delay(35) }
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

    suspend fun daemonStatuses(@Suppress("UNUSED_PARAMETER") ctx: Context): List<DaemonStatus> {
        delay(120)
        return listOf(
            DaemonStatus("ghost.framed", "extracts moments from photos & video",
                DaemonStatus.State.WORKING, "8,421 photos · 142 videos", "now"),
            DaemonStatus("ghost.voiced", "captures & transcribes voice notes",
                DaemonStatus.State.LISTENING, "63 notes transcribed", "2m ago"),
            DaemonStatus("ghost.cued", "surfaces reflections from your life",
                DaemonStatus.State.IDLE, "next sweep in 12m", "12m ago"),
            DaemonStatus("ghost.shadowd", "watches for manipulation in messages",
                DaemonStatus.State.IDLE, "nothing flagged", "1h ago"),
            DaemonStatus("ghost.watchd", "keeps the fleet honest",
                DaemonStatus.State.WORKING, "all daemons healthy", "now"),
        )
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
    suspend fun nextCameraCommand(kind: MediaKind): Command {
        delay(200); return Command.SyncCamera(kind, Cursor.BEGINNING)
    }

    suspend fun ingest(
        @Suppress("UNUSED_PARAMETER") kind: MediaKind,
        @Suppress("UNUSED_PARAMETER") name: String,
        body: InputStream,
    ): Boolean {
        val buf = ByteArray(64 * 1024); while (true) { if (body.read(buf) < 0) break }; return true
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
                            sha256 = o.optStringOrNull("sha256"),
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
        @Suppress("UNUSED_PARAMETER") att: Attachment,
        body: java.io.InputStream,
    ): Boolean {
        val buf = ByteArray(64 * 1024); while (true) { if (body.read(buf) < 0) break }; return true
    }

    suspend fun report(@Suppress("UNUSED_PARAMETER") result: CommandResult) {}
}
