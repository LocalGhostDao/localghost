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

data class PendingNotification(val daemonId: String, val title: String, val body: String)

/** A PIN's behaviour when entered at the lock screen. */
enum class PinBehaviour { MOUNT_REAL, MOUNT_DECOY, WIPE }

/** A PIN belonging to the CURRENTLY MOUNTED persona. Digits are never returned by the box
 *  after creation — only a masked hint and the behaviour. */
data class PinEntry(val id: String, val hint: String, val behaviour: PinBehaviour, val label: String)

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

    data class Session(val ok: Boolean)

    /** Cheap reachability check. STUB returns true; real: mTLS ping to the box. Routing
     *  uses this to decide box vs on-phone model. */
    suspend fun reachable(): Boolean { delay(60); return boxReachableStub }

    /** test seam: flip to simulate the box being down. */
    @Volatile var boxReachableStub: Boolean = true

    suspend fun submitPin(@Suppress("UNUSED_PARAMETER") pin: String): Session {
        delay(1500); return Session(ok = true)
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
    suspend fun personaPins(@Suppress("UNUSED_PARAMETER") ctx: Context): List<PinEntry> {
        delay(150)
        return listOf(
            PinEntry("p1", "••••", PinBehaviour.MOUNT_REAL, "primary"),
            PinEntry("p2", "••••••", PinBehaviour.MOUNT_DECOY, "hand-over"),
            PinEntry("p3", "••••••", PinBehaviour.WIPE, "burn"),
        )
    }

    /** Add a pin to the mounted persona. STUB returns true. Real: ghost.secd derives + stores. */
    suspend fun addPin(
        @Suppress("UNUSED_PARAMETER") pin: String,
        @Suppress("UNUSED_PARAMETER") behaviour: PinBehaviour,
        @Suppress("UNUSED_PARAMETER") label: String,
    ): Boolean { delay(400); return true }

    /** Remove a pin from the mounted persona. STUB true. (Cannot remove the last MOUNT_REAL.) */
    suspend fun removePin(@Suppress("UNUSED_PARAMETER") id: String): Boolean { delay(300); return true }

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
    suspend fun availableModels(@Suppress("UNUSED_PARAMETER") ctx: Context): List<PhoneModel> {
        delay(150)
        return listOf(
            PhoneModel("gemma4-e2b-q4", "Gemma 4 E2B (Q4_K_M)",
                "small, on-phone · generic questions only", 2_600_000_000L, null),
            PhoneModel("qwen25-1_5b-q4", "Qwen2.5 1.5B (Q4_K_M)",
                "tiny, fast on phone", 1_100_000_000L, null),
        )
    }

    /**
     * Stream a model's bytes FROM THE BOX, resumable via an offset. Returns an InputStream the
     * caller writes to disk. STUB returns an empty stream of the right length-ish; real: mTLS
     * GET /models/{id} with Range. The phone writes to filesDir/models and tracks it locally.
     */
    suspend fun downloadModel(
        @Suppress("UNUSED_PARAMETER") id: String,
        @Suppress("UNUSED_PARAMETER") offset: Long,
    ): java.io.InputStream {
        delay(100)
        // STUB: a tiny placeholder stream so the flow works end-to-end without the box.
        return java.io.ByteArrayInputStream(ByteArray(0))
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
    suspend fun changePin(
        @Suppress("UNUSED_PARAMETER") oldPin: String,
        @Suppress("UNUSED_PARAMETER") newPin: String,
    ): Boolean {
        delay(800); return true
    }

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
