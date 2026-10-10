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
import kotlinx.coroutines.channels.trySendBlocking
import kotlinx.coroutines.flow.flow
import java.io.InputStream
import org.json.JSONObject

data class PendingNotification(val daemonId: String, val title: String, val body: String,
    val id: Long = 0, val kind: String = "message", val seen: Boolean = false, val created: Long = 0,
    /** where a tap goes (NotifLink): "map:<day>", "memories:<id>", "news", "status"; "" for by kind */
    val link: String = "",
    /** an ask: the choices offered, the one taken ("" until then) */
    val options: List<String> = emptyList(), val answer: String = "")

/** A saved conversation. Lives on the box (synthd); the phone lists + loads, holds the active
 *  one in memory only. */
data class Conversation(val id: String, val title: String, val updatedLabel: String, val messageCount: Int)

/** A device enrolled against this persona. Sync state is per-device. */
data class DeviceInfo(
    val id: String, val name: String, val thisDevice: Boolean,
    val lastSync: String, val photos: Int, val videos: Int,
    // Epoch seconds straight from the box's cursor rows , 0 means "never".
    val lastSyncTs: Long = 0, val lastPhotoTs: Long = 0, val lastVideoTs: Long = 0,
    val model: String = "", val frames: Long = 0,
)

/** Settings, owned by the box (persona-scoped), reflected on the phone. */

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
        var run = ""
        try {
            val resp = BoxHttp.postJson(ctx, "/v1/unlock", JSONObject().put("pin", pin))
            // the box names this unlock; only a poll that names it gets the session token
            run = resp.optString("run", "")
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
            val (states, model) = try {
                pollUnlock(ctx, run)
            } catch (e: Exception) {
                emit(UnlockSnapshot.failed("lost contact with the box: ${e.message}"))
                return@flow
            }
            val snap = UnlockSnapshot.from(states, model)
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
    private suspend fun pollUnlock(ctx: Context, run: String): Pair<Map<UnlockStage, StageState>, ModelLoad?> {
        val resp = BoxHttp.getJson(ctx, "/v1/unlock/poll" + if (run.isNotEmpty()) "?run=" + java.net.URLEncoder.encode(run, "UTF-8") else "")
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
        val model = ModelLoad.fromJson(resp.optJSONObject("model"))
        val arr = resp.optJSONArray("stages") ?: return out to model
        for (i in 0 until arr.length()) {
            val o = arr.getJSONObject(i)
            val stage = stageFromName(o.optString("stage")) ?: continue
            out[stage] = stateFromName(o.optString("state"))
        }
        return out to model
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

    /** The box model's state (GET /v1/model): ready, or loading and how far. It loads after the
     *  unlock, so chat asks before it sends. Null on any error or from a box without the route
     *  (chat then sends at once, as it always did). */
    suspend fun modelStatus(ctx: Context): ModelWait.Box? = try {
        ModelWait.Box.fromJson(BoxHttp.getJson(ctx, "/v1/model"))
    } catch (e: kotlinx.coroutines.CancellationException) {
        throw e
    } catch (e: Exception) {
        null
    }

    /** The phone's trail key as the box's vault keeps it (GET /v1/trail/key): [have] false when the
     *  box has none for this phone. Null when the box could not be asked. */
    class TrailKeyAnswer(val have: Boolean, val pub: ByteArray?, val priv: ByteArray?)

    suspend fun trailKeyGet(ctx: Context): TrailKeyAnswer? = try {
        val o = BoxHttp.getJson(ctx, "/v1/trail/key")
        if (!o.optBoolean("have", false)) TrailKeyAnswer(false, null, null)
        else {
            val pub = com.localghost.app.sync.TrailSeal.unb64(o.optString("public"))?.takeIf { it.size == 32 }
            val priv = com.localghost.app.sync.TrailSeal.unb64(o.optString("private"))?.takeIf { it.size == 32 }
            if (pub == null || priv == null) null else TrailKeyAnswer(true, pub, priv)
        }
    } catch (e: kotlinx.coroutines.CancellationException) {
        throw e
    } catch (e: Exception) {
        null
    }

    /** Hands the phone's trail key to the box's vault (POST /v1/trail/key). True when kept. */
    suspend fun trailKeyPut(ctx: Context, pub: ByteArray, priv: ByteArray): Boolean = try {
        BoxHttp.postJson(ctx, "/v1/trail/key", org.json.JSONObject()
            .put("public", com.localghost.app.sync.TrailSeal.b64(pub))
            .put("private", com.localghost.app.sync.TrailSeal.b64(priv))).optBoolean("ok", false)
    } catch (e: kotlinx.coroutines.CancellationException) {
        throw e
    } catch (e: Exception) {
        false
    }

    /** A certificate for a key the phone made itself (POST /v1/device/rekey): [spki] its public key,
     *  [sig] ECDSA-SHA256 over "localghost rekey v1\n" + spki. The certificate's PEM, or null. */
    suspend fun deviceRekey(ctx: Context, spki: ByteArray, sig: ByteArray): String? = try {
        val r = BoxHttp.postJson(ctx, "/v1/device/rekey", org.json.JSONObject()
            .put("spki", java.util.Base64.getEncoder().encodeToString(spki))
            .put("sig", java.util.Base64.getEncoder().encodeToString(sig)))
        r.optString("cert", "").takeIf { r.optBoolean("ok", false) && it.contains("BEGIN CERTIFICATE") }
    } catch (e: kotlinx.coroutines.CancellationException) {
        throw e
    } catch (e: Exception) {
        null
    }

    /** Over the new certificate: the box retires the one it replaced. */
    suspend fun deviceRekeyConfirm(ctx: Context): Boolean = try {
        BoxHttp.postJson(ctx, "/v1/device/rekey/confirm", org.json.JSONObject()).optBoolean("ok", false)
    } catch (e: kotlinx.coroutines.CancellationException) {
        throw e
    } catch (e: Exception) {
        false
    }

    /** A release the box keeps on its shelf (GET /v1/update "shelf"): one with [set] can go back on. */
    class Kept(val version: String, val name: String, val commit: String, val date: String, val go: String,
               val changes: Int, val at: Long, val set: Boolean) {
        val label: String get() = if (name.isNotBlank()) "$name $version" else version
    }

    /** What the box runs and a release on trial (GET /v1/update); null from a box that predates it.
     *  [commit], [builtAt] and [go] come from a 0.0.5 box, "" before. */
    class UpdateStatus(val version: String, val trialVersion: String, val trialPrev: String, val trialState: String, val trialReason: String,
                       val name: String = "", val commit: String = "", val builtAt: String = "", val go: String = "",
                       val shelf: List<Kept> = emptyList()) {
        /** "wisp 0.0.1", or the version alone (a build from source has no name). */
        val label: String get() = if (name.isNotBlank()) "$name $version" else version
    }

    suspend fun updateStatus(ctx: Context): UpdateStatus? = try {
        val o = BoxHttp.getJson(ctx, "/v1/update")
        if (!o.has("version")) null
        else {
            val t = o.optJSONObject("trial") ?: org.json.JSONObject()
            val shelf = ArrayList<Kept>()
            o.optJSONArray("shelf")?.let { a ->
                for (i in 0 until a.length()) {
                    val k = a.optJSONObject(i) ?: continue
                    shelf.add(Kept(k.optString("version", ""), k.optString("name", ""), k.optString("commit", ""), k.optString("date", ""),
                        k.optString("go", ""), k.optInt("changes", 0), k.optLong("at", 0L), k.optBoolean("set", false)))
                }
            }
            UpdateStatus(o.optString("version", ""), t.optString("version", ""), t.optString("prev", ""),
                t.optString("state", ""), t.optString("reason", ""), o.optString("name", ""),
                o.optString("commit", ""), o.optString("builtAt", ""), o.optString("go", ""), shelf)
        }
    } catch (e: kotlinx.coroutines.CancellationException) {
        throw e
    } catch (e: Exception) {
        null
    }

    /** A release from the box's shelf back on (POST /v1/update/switch): ok, or why not. */
    suspend fun updateSwitch(ctx: Context, version: String): Pair<Boolean, String> = try {
        val r = BoxHttp.postJson(ctx, "/v1/update/switch", org.json.JSONObject().put("version", version), readTimeoutMs = 6 * 60_000)
        if (r.optBoolean("ok", false)) true to r.optString("version", "") else false to r.optString("why", "the box said no")
    } catch (e: kotlinx.coroutines.CancellationException) {
        throw e
    } catch (e: Exception) {
        false to (e.message ?: "the box did not answer")
    }

    /** One file of the server set to the box (POST /v1/update/file?name=). */
    suspend fun updateUpload(ctx: Context, name: String, file: java.io.File): Boolean = try {
        BoxHttp.postFile(ctx, "/v1/update/file?name=" + java.net.URLEncoder.encode(name, "UTF-8"), file,
            "application/octet-stream", emptyMap()) == 200
    } catch (e: kotlinx.coroutines.CancellationException) {
        throw e
    } catch (e: Exception) {
        false
    }

    /** Verify, put on, restart (POST /v1/update/apply): ok and the version, or false and why not. */
    suspend fun updateApply(ctx: Context): Pair<Boolean, String> = try {
        val r = BoxHttp.postJson(ctx, "/v1/update/apply", org.json.JSONObject(), readTimeoutMs = 6 * 60_000)
        if (r.optBoolean("ok", false)) true to r.optString("version", "") else false to r.optString("why", "the box said no")
    } catch (e: kotlinx.coroutines.CancellationException) {
        throw e
    } catch (e: Exception) {
        false to (e.message ?: "the box did not answer")
    }

    /** The earlier build back (POST /v1/update/rollback). */
    suspend fun updateRollback(ctx: Context): Pair<Boolean, String> = try {
        val r = BoxHttp.postJson(ctx, "/v1/update/rollback", org.json.JSONObject())
        if (r.optBoolean("ok", false)) true to "" else false to r.optString("why", "the box said no")
    } catch (e: kotlinx.coroutines.CancellationException) {
        throw e
    } catch (e: Exception) {
        false to (e.message ?: "the box did not answer")
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
        data class ChatId(val id: Long) : ChatChunk
        data class Reasoning(val text: String) : ChatChunk
        data class Token(val text: String) : ChatChunk
        /** A line from the box about the wait itself ("reading 1,300 words of context on the CPU ,
         *  about 35s before the first word"), shown until the first word arrives. */
        data class Status(val text: String) : ChatChunk
        /** What the box drew on for the answer, in a few lines (the trail under the answer). */
        data class Steps(val lines: List<String>) : ChatChunk
        /** The box read the findings, found them thin, and asks for these searches before it
         *  answers (one more round; the caller re-asks with round 2). The stream ends after it. */
        data class More(val queries: List<String>, val why: String) : ChatChunk
        data object Done : ChatChunk
    }

    fun chat(
        incognito: Boolean = false,
        chatId: Long = 0L,
        history: List<Message>,
        prompt: String,
        @Suppress("UNUSED_PARAMETER") convId: String?,
        @Suppress("UNUSED_PARAMETER") attachments: List<Attachment> = emptyList(),
        @Suppress("UNUSED_PARAMETER") caps: ChatCapabilities = ChatCapabilities(),
        imageB64: String = "",
        web: org.json.JSONArray? = null, // what the phone found on the web for this question; the box adds it as labelled context
        here: Pair<Double, Double>? = null, // the phone's last fix when recent: "near here" and "the weather" mean somewhere, against the box's own map data and its daily weather pull
        need: String = "",                 // what the box's model said the question needs (chatPlan); the box ranks the pages' paragraphs against it
        round: Int = 0,                    // 1 on the first ask with findings, 2 after the box asked for more
        spare: List<String> = emptyList(), // the plan's searches not run yet, for the box to ask for
    ): Flow<ChatChunk> = kotlinx.coroutines.flow.channelFlow {
        // REAL STREAMING end-to-end: app -> secd -> ghost.synthd (context injection + transparency)
        // -> ghost.oracled -> llama-server, tokens flowing back as they generate. Event protocol,
        // one JSON per "data:" line: {"context":[...]} first (always, empty when nothing injected),
        // {"t":"..."} per token, {"done":true,"model":x} last. NO fake word-splitting , what you see
        // appearing is the model generating, and closing the chat cancels generation on the box.
        val ctx = appCtx ?: run { send(ChatChunk.Token("(app context missing)")); send(ChatChunk.Done); return@channelFlow }
        val think = com.localghost.app.settings.AppSettings.thinkLevel(ctx)
        // THE CONVERSATION SO FAR rides along , the box uses its own persisted copy when the chat
        // has one (chatId != 0) and this copy when it does not (incognito, or a chat that never
        // persisted). Without it every turn was a one-shot: the model never saw the last answer.
        // The trailing entry is the prompt itself (the caller appends before sending); drop it.
        // Bounded here too: the last dozen turns, text only , attachments stay with their turn.
        val prior = history.filter { it.text.isNotBlank() }
            .let { if (it.isNotEmpty() && it.last().role == Message.Role.USER && it.last().text == prompt) it.dropLast(1) else it }
            .takeLast(12)
        val historyJson = org.json.JSONArray().apply {
            prior.forEach { m ->
                put(org.json.JSONObject()
                    .put("role", if (m.role == Message.Role.USER) "user" else "assistant")
                    .put("content", m.text.take(4000)))
            }
        }
        try {
            BoxHttp.postStreamLines(ctx, "/v1/chat",
                org.json.JSONObject().put("prompt", prompt).put("think", think)
                    .put("incognito", incognito).put("chatId", chatId)
                    .apply { if (historyJson.length() > 0) put("history", historyJson) }
                    .apply { if (imageB64.isNotBlank()) put("imageB64", imageB64) }
                    .apply { if (web != null && web.length() > 0) put("web", web) }
                    .apply { if (need.isNotBlank()) put("need", need) }
                    .apply { if (round > 0) put("round", round) }
                    .apply { if (spare.isNotEmpty()) put("spare", org.json.JSONArray(spare)) }
                    .apply { if (here != null) put("here", org.json.JSONObject().put("lat", here.first).put("lon", here.second)) }) { line ->
                if (!line.startsWith("data: ")) return@postStreamLines true
                val o = try { org.json.JSONObject(line.removePrefix("data: ")) } catch (_: Exception) { return@postStreamLines true }
                when {
                    o.has("context") -> {
                        val arr = o.optJSONArray("context")
                        if (arr != null && arr.length() > 0) {
                            val mems = (0 until arr.length()).mapNotNull { i ->
                                val c = arr.optJSONObject(i) ?: return@mapNotNull null
                                val when_ = c.optString("when", "")
                                val snip = c.optString("snippet", "")
                                if (snip.isBlank()) null
                                else (if (when_.isNotBlank()) "$when_ , " else "") + c.optString("source", "archive") + ": " + snip
                            }
                            if (mems.isNotEmpty()) channel.trySendBlocking(ChatChunk.Memories(mems))
                        }
                        o.optString("note").takeIf { it.isNotBlank() }?.let { channel.trySendBlocking(ChatChunk.Status(it)) }
                        o.optJSONArray("steps")?.let { a ->
                            val steps = (0 until a.length()).mapNotNull { i -> a.optString(i).takeIf { it.isNotBlank() } }
                            if (steps.isNotEmpty()) channel.trySendBlocking(ChatChunk.Steps(steps))
                        }
                        // the chat's id at once (a box from 30 Sep 2026): an app closed mid-answer
                        // reopens on this chat, and the box has kept writing the answer into it
                        o.optLong("chatId", 0L).takeIf { it > 0 }?.let { channel.trySendBlocking(ChatChunk.ChatId(it)) }
                        true
                    }
                    // {"more":{"queries":[...]}} asks for a second search; the closing
                    // {"done":true,"more":true} is not that (its "more" is a flag) and ends the stream below
                    o.optJSONObject("more") != null -> {
                        val m = o.optJSONObject("more")
                        val qs = m?.optJSONArray("queries")?.let { a -> (0 until a.length()).map { a.optString(it) }.filter { it.isNotBlank() } } ?: emptyList()
                        if (qs.isNotEmpty()) channel.trySendBlocking(ChatChunk.More(qs, m?.optString("why") ?: ""))
                        true
                    }
                    o.has("r") -> { channel.trySendBlocking(ChatChunk.Reasoning(o.optString("r"))); true }
                    o.has("t") -> { channel.trySendBlocking(ChatChunk.Token(o.optString("t"))); true }
                    o.optBoolean("done") -> {
                        val cid = o.optLong("chatId", 0L)
                        if (cid > 0) channel.trySendBlocking(ChatChunk.ChatId(cid))
                        false
                    }
                    else -> true
                }
            }
        } catch (e: Exception) {
            android.util.Log.w("LocalGhost", "chat failed: ${e.message}")
            send(ChatChunk.Token("The box did not answer , it may be locked, or the model is still loading. Check Box Status."))
        }
        send(ChatChunk.Done)
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
            val liveDetail = s.optString("detail", "")
            out.add(
                DaemonStatus(
                    id = name,
                    role = daemonRoles[name] ?: name,
                    state = mapDaemonState(code, state),
                    detail = listOf(liveDetail, daemonDetail(code, restarts, lastErr))
                        .filter { it.isNotBlank() }.joinToString(" · "),
                    lastRun = "",   // watchd sample time will fill this once watchd writes metrics
                )
            )
        }
        // Datastores ride the same list. These are LIVE probes from the box (SELECT 1 / PING), so a
        // Postgres that is wedged-but-running shows FAILED here with the actual error text , the
        // named visibility that was previously only mystery query errors three screens away.
        resp.optJSONObject("host")?.let { h ->
            val cores = h.optInt("cores", 0)
            val load1 = h.optDouble("load1", -1.0)
            if (load1 >= 0 && cores > 0) out.add(DaemonStatus(
                id = "cpu",
                role = "load average vs $cores cores",
                state = if (load1 > cores) DaemonStatus.State.ERROR else DaemonStatus.State.WORKING,
                detail = "load %.1f / %d".format(load1, cores) + if (load1 > cores) " , saturated" else "",
                lastRun = "",
            ))
            val memT = h.optDouble("memTotalGB", -1.0)
            val memU = h.optDouble("memUsedGB", -1.0)
            if (memT > 0 && memU >= 0) out.add(DaemonStatus(
                id = "memory",
                role = "system RAM",
                state = if (memU / memT > 0.92) DaemonStatus.State.ERROR else DaemonStatus.State.WORKING,
                detail = "%.1f GB used of %.1f".format(memU, memT),
                lastRun = "",
            ))
            val gpu = h.optJSONObject("gpu")
            if (gpu != null) {
                val vu = gpu.optDouble("vramUsedGB", 0.0)
                val vt = gpu.optDouble("vramTotalGB", 0.0)
                val util = gpu.optDouble("util", 0.0)
                out.add(DaemonStatus(
                    id = "gpu",
                    role = "the model's silicon",
                    state = if (vt > 0 && vu / vt > 0.97) DaemonStatus.State.ERROR else DaemonStatus.State.WORKING,
                    detail = "%.1f/%.1f GB VRAM · %d%% busy".format(vu, vt, util.toInt()),
                    lastRun = "",
                ))
            } else {
                // Absent means not visible , driver missing or exec failed , which on a box that
                // SHOULD have a 4070 is itself a red flag worth a row, not silence.
                out.add(DaemonStatus(
                    id = "gpu",
                    role = "the model's silicon",
                    state = DaemonStatus.State.ERROR,
                    detail = "not visible (driver? nvidia-smi missing?) , model likely on CPU",
                    lastRun = "",
                ))
            }
            val rootFree = h.optDouble("rootFreeGB", -1.0)
            if (rootFree >= 0) out.add(DaemonStatus(
                id = "system disk",
                role = "the OS partition (/var lives here , logs die when it fills)",
                state = if (rootFree < 2.0) DaemonStatus.State.ERROR else DaemonStatus.State.WORKING,
                detail = "%.1f GB free".format(rootFree) + if (rootFree < 2.0) " , CRITICALLY LOW" else "",
                lastRun = "",
            ))
        }
        resp.optJSONObject("volume")?.let { v ->
            val free = v.optDouble("freeGB", -1.0)
            val total = v.optDouble("totalGB", -1.0)
            if (free >= 0 && total > 0) out.add(
                DaemonStatus(
                    id = "volume",
                    role = "the encrypted ground everything stands on",
                    state = if (free < 2.0) DaemonStatus.State.ERROR else DaemonStatus.State.WORKING,
                    detail = "%.1f GB free of %.1f".format(free, total) + if (free < 2.0) " , CRITICALLY LOW" else "",
                    lastRun = "",
                )
            )
        }
        val stores = resp.optJSONArray("datastores")
        if (stores != null) for (i in 0 until stores.length()) {
            val d = stores.getJSONObject(i)
            val name = d.optString("name")
            val failed = d.optString("state") != "ok"
            out.add(
                DaemonStatus(
                    id = name,
                    role = if (name == "postgres") "the durable memory (frames, chats, cursors)"
                           else "the fast memory (mirrors, queues)",
                    state = if (failed) DaemonStatus.State.ERROR else DaemonStatus.State.WORKING,
                    detail = if (failed) d.optString("detail", "unreachable") else "answering",
                    lastRun = "",
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
    /** REAL notifications from the box , /v1/notifications is a per-device push cursor (the box
     *  advances it, so each notification is offered to this phone exactly once). The fakes that
     *  lived here are dead; if the list is empty, the box genuinely has nothing to say. */
    suspend fun pollPending(ctx: Context): List<PendingNotification> {
        if (NotifyState.isMuted(ctx)) return emptyList()
        val now = System.currentTimeMillis()
        if (now - NotifyState.lastPostedAt(ctx) < CADENCE_MS) return emptyList()
        return try {
            val r = BoxHttp.getJson(ctx, "/v1/notifications")
            HomeCache.putSnap(ctx, r.optJSONObject("home")) // home's numbers ride along
            val a = r.optJSONArray("notifications") ?: return emptyList()
            val out = (0 until a.length()).mapNotNull { i ->
                val o = a.optJSONObject(i) ?: return@mapNotNull null
                PendingNotification(o.optString("service", "ghost.secd"),
                    o.optString("title"), o.optString("body"),
                    o.optLong("id"), o.optString("kind", "message"), link = o.optString("link"))
            }
            if (out.isNotEmpty()) NotifyState.setLastPostedAt(ctx, now)
            out
        } catch (_: Exception) { emptyList() }
    }

    /** The notification HISTORY (GET /v1/notifications/list): everything the box's daemons said,
     *  newest first, seen or not. Reading it consumes nothing; the push cursor (pollPending) is
     *  separate. This is what the NOTIFICATIONS screen shows; it used to show the push cursor,
     *  which the phone's own pollers had already used up, so the screen was always empty. */
    suspend fun notificationHistory(ctx: Context): List<PendingNotification>? = try {
        val r = BoxHttp.getJson(ctx, "/v1/notifications/list")
        val a = r.optJSONArray("notifications") ?: org.json.JSONArray()
        (0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            PendingNotification(o.optString("service", "ghost.secd"), o.optString("title"), o.optString("body"),
                o.optLong("id"), o.optString("kind", "message"), o.optBoolean("seen", false), o.optLong("created", 0L),
                o.optString("link"),
                o.optJSONArray("options")?.let { a -> (0 until a.length()).map { a.optString(it) }.filter { it.isNotEmpty() } } ?: emptyList(),
                o.optString("answer"))
        }
    } catch (_: Exception) { null }

    /** Answer an ask (POST /v1/notifications/answer {id, answer}): one of its options. */
    suspend fun notificationAnswer(ctx: Context, id: Long, answer: String): Boolean = try {
        BoxHttp.postJson(ctx, "/v1/notifications/answer", org.json.JSONObject().put("id", id).put("answer", answer)).optBoolean("ok", false)
    } catch (_: Exception) { false }

    suspend fun notificationSeen(ctx: Context, id: Long): Boolean = try {
        BoxHttp.postJson(ctx, "/v1/notifications/seen", org.json.JSONObject().put("id", id)).optBoolean("ok", false)
    } catch (_: Exception) { false }

    suspend fun notificationDelete(ctx: Context, id: Long): Boolean = try {
        BoxHttp.postJson(ctx, "/v1/notifications/delete", org.json.JSONObject().put("id", id)).optBoolean("ok", false)
    } catch (_: Exception) { false }

    // --- sync ---
    /** Rewind THIS device's sync cursors on the box , the next run re-offers everything from the
     *  beginning and hash dedup archives only the gap. */
    suspend fun resetSync(ctx: Context): Boolean = try {
        BoxHttp.postJson(ctx, "/v1/sync/reset", org.json.JSONObject()).optBoolean("reset", false)
    } catch (_: Exception) { false }

    /** The box's authoritative cursor per kind , the ONLY cursor. NULL when the box did not give
     *  one: unreachable, locked, its database busy, or any answer without the cursor in it. The run
     *  then does not start. It used to be (0,0) "on any failure": on 29 Sep 2026 the box answered
     *  503 for a few minutes during a stuck unlock, every phone run started from the beginning of
     *  the camera roll, and (the existence check failing too) uploaded all of it again. Only the
     *  box saying "none" (src "none", a new device or after a reset) means the beginning. */
    suspend fun getCursor(ctx: Context, kind: MediaKind): com.localghost.app.sync.Cursor? = try {
        val r = BoxHttp.getJson(ctx, "/v1/sync/cursor")
        val o = r.optJSONObject(kind.wire)
        if (o == null || !o.has("src") || !o.has("ts")) {
            android.util.Log.w("LocalGhost", "cursor: the box gave no cursor for ${kind.wire} (down, locked or busy); not syncing this run")
            null
        } else {
            val src = o.optString("src")
            val ts = o.optLong("ts")
            // The src is the visible proof of the box's datastore roundtrip: "redis" = the mirror fast
            // path answered, "postgres" = the durable fallback, "frames" = content-derived only,
            // "none" = the box has nothing for this device (the one honest reason to start at 0).
            android.util.Log.i("LocalGhost", "resume ${kind.wire}: ts=$ts id=${o.optLong("id")} via $src")
            com.localghost.app.sync.Cursor(ts, o.optLong("id"))
        }
    } catch (e: Exception) {
        android.util.Log.w("LocalGhost", "cursor fetch failed, not syncing this run: ${e.message}")
        null
    }

    /** Report the confirmed sync position , the box's per-device memory of where we got to. */
    suspend fun reportCursor(ctx: Context, kind: MediaKind, ts: Long, id: Long) {
        try {
            BoxHttp.postJson(ctx, "/v1/sync/cursor",
                org.json.JSONObject().put("kind", kind.wire).put("ts", ts).put("id", id))
        } catch (e: Exception) {
            android.util.Log.w("LocalGhost", "cursor report failed (box will learn next run): ${e.message}")
        }
    }

    /** Which of these content hashes the box already has. NULL when the box did not answer the
     *  question (unreachable, 503, an answer without "have"): the run stops there and tries again
     *  later. Nothing is skipped on uncertainty (the cursor never passes an unconfirmed photo) and
     *  nothing is uploaded blind: "upload everything on failure" re-sent a whole camera roll to a
     *  box that was only busy (29 Sep 2026). */
    /** photo or video for each of [hashes] the box knows (POST /v1/frames/kinds); empty when the
     *  box does not answer or predates the endpoint (everything then opens as a photo). */
    suspend fun frameKinds(ctx: Context, hashes: List<String>): Map<String, String> = try {
        val body = org.json.JSONObject().put("hashes", org.json.JSONArray().apply { hashes.take(200).forEach { put(it) } })
        val k = BoxHttp.postJson(ctx, "/v1/frames/kinds", body).optJSONObject("kinds")
        k?.keys()?.asSequence()?.associateWith { k.optString(it) } ?: emptyMap()
    } catch (_: Exception) { emptyMap() }

    suspend fun framesHave(ctx: Context, hashes: List<String>): Set<String>? = try {
        if (hashes.isEmpty()) return emptySet() // nothing to ask , skip the round trip entirely
        val body = org.json.JSONObject().put("hashes", org.json.JSONArray(hashes))
        val r = BoxHttp.postJson(ctx, "/v1/frames/exists", body)
        if (!r.has("have")) {
            android.util.Log.w("LocalGhost", "frames/exists: no answer from the box; stopping this sync run")
            return null
        }
        val arr = r.optJSONArray("have") ?: return emptySet()
        (0 until arr.length()).mapNotNull { arr.optString(it).takeIf { h -> h.isNotBlank() } }.toSet()
    } catch (e: Exception) {
        android.util.Log.w("LocalGhost", "frames/exists failed; stopping this sync run: ${e.message}")
        null
    }

    /** One gallery entry from the box's archive. */
    data class GalleryFrame(
        val hash: String, val takenAt: Long, val kind: String, val bytes: Long,
        val name: String = "",           // derived on the box: date + first tags; empty until tagged
        val tags: List<String> = emptyList(),
        val place: String = "",          // reverse-geocoded hierarchy; "" until geo data lands
        val description: String = "",    // the caption's SCENE section
    )

    /** Page the archive newest-first. before=0 for the first page; pass the last row's takenAt to
     *  continue. Empty list on failure or end of archive. */
    /** A page of the archive, newest first; null when the box did not answer (an empty list is a real end). */
    suspend fun framesList(ctx: Context, before: Long = 0, limit: Int = 60): List<GalleryFrame>? = try {
        val r = BoxHttp.getJson(ctx, "/v1/frames/list?before=$before&limit=$limit")
        val arr = r.optJSONArray("frames") ?: return emptyList()
        (0 until arr.length()).mapNotNull { i ->
            val o = arr.optJSONObject(i) ?: return@mapNotNull null
            val tagsArr = o.optJSONArray("tags")
            GalleryFrame(
                o.optString("hash"), o.optLong("takenAt"), o.optString("kind"), o.optLong("bytes"),
                name = o.optString("name", ""),
                place = o.optString("place", ""),
                description = o.optString("description", ""),
                tags = if (tagsArr == null) emptyList()
                       else (0 until tagsArr.length()).mapNotNull { t -> tagsArr.optString(t).takeIf { it.isNotBlank() } },
            )
        }
    } catch (e: Exception) {
        android.util.Log.w("LocalGhost", "frames/list failed: ${e.message}")
        null
    }

    /** Conversations persisted on the box , list, search, keyset paging. */
    data class BoxChat(val id: Long, val title: String, val updatedAt: Long, val messages: Long)
    suspend fun boxChats(ctx: Context, q: String = "", beforeUpdated: Long = 0L, limit: Int = 20): List<BoxChat>? = try {
        var path = "/v1/chats?limit=$limit"
        if (beforeUpdated > 0) path += "&before=$beforeUpdated"
        if (q.isNotBlank()) path += "&q=" + java.net.URLEncoder.encode(q, "UTF-8")
        val r = BoxHttp.getJson(ctx, path)
        val a = r.optJSONArray("chats") ?: return emptyList()
        (0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            BoxChat(o.optLong("id"), o.optString("title"), o.optLong("updatedAt"), o.optLong("messages"))
        }
    } catch (e: Exception) {
        android.util.Log.w("LocalGhost", "chats list failed: ${e.message}"); null
    }

    /** One saved message. An answer carries its [reasoning] (the model's thinking), the web
     *  [sources] it drew on, and [state] "writing" while the box is still producing it (the app was
     *  closed on it), "stopped" when it ended early, "" when whole. */
    data class BoxChatMsg(
        val id: Long, val role: String, val content: String,
        val reasoning: String = "", val sources: List<WebSearch.Hit> = emptyList(), val state: String = "",
    )

    /** The web sources as the box saved them beside an answer, in their [n] order. */
    internal fun parseSources(a: org.json.JSONArray?): List<WebSearch.Hit> {
        if (a == null) return emptyList()
        return (0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            val url = o.optString("url")
            if (url.isBlank() && o.optString("title").isBlank()) null
            else o.optInt("n", i + 1) to WebSearch.Hit(o.optString("title"), url, "", kind = o.optString("kind").ifBlank { "page" })
        }.sortedBy { it.first }.map { it.second }
    }

    /** STOP: ends the answer the box is writing for this chat (closing the app does not). */
    suspend fun chatStop(ctx: Context, chatId: Long): Boolean = try {
        BoxHttp.postJson(ctx, "/v1/chat/stop", org.json.JSONObject().put("chatId", chatId)).optBoolean("stopped")
    } catch (e: Exception) { android.util.Log.w("LocalGhost", "chat stop: ${e.message}"); false }
    /** One conversation's history, newest first as served; callers reverse for display. */
    /** Place + name + tag search (AND per term) , the location search over the archive. */
    suspend fun framesSearch(ctx: Context, q: String): List<GalleryFrame>? = try {
        val r = BoxHttp.getJson(ctx, "/v1/frames/search?q=" + java.net.URLEncoder.encode(q, "UTF-8"))
        val a = r.optJSONArray("frames") ?: return emptyList()
        (0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            GalleryFrame(o.optString("hash"), o.optLong("takenAt"), o.optString("kind"),
                bytes = o.optLong("bytes"), name = o.optString("name"),
                place = o.optString("place"))
        }
    } catch (e: Exception) { android.util.Log.w("LocalGhost", "frames search: ${e.message}"); null }

    /** Ship day-batched health metrics to the box (tallyd ingests). */
    suspend fun healthUpload(ctx: Context, days: Map<String, Map<String, Double>>,
                             samples: List<Triple<String, Long, Double>> = emptyList()): Boolean = try {
        val arr = org.json.JSONArray()
        days.forEach { (day, metrics) ->
            val mo = org.json.JSONObject(); metrics.forEach { (k, v) -> mo.put(k, v) }
            arr.put(org.json.JSONObject().put("day", day).put("metrics", mo))
        }
        val sarr = org.json.JSONArray()
        samples.forEach { (m, ts, v) ->
            sarr.put(org.json.JSONObject().put("metric", m).put("ts", ts).put("value", v))
        }
        BoxHttp.postJson(ctx, "/v1/health/upload",
            org.json.JSONObject().put("days", arr).put("samples", sarr)); true
    } catch (_: Exception) { false }

    data class HealthSeries(val metric: String, val days: List<String>, val values: List<Double>)

    suspend fun healthStats(ctx: Context, days: Int = 30): List<HealthSeries>? = try {
        val r = BoxHttp.getJson(ctx, "/v1/health/stats?days=$days")
        val a = r.optJSONArray("series") ?: return emptyList()
        (0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            val ds = o.optJSONArray("days") ?: org.json.JSONArray()
            val vs = o.optJSONArray("values") ?: org.json.JSONArray()
            HealthSeries(o.optString("metric"),
                (0 until ds.length()).map { ds.optString(it) },
                (0 until vs.length()).map { vs.optDouble(it) })
        }
    } catch (_: Exception) { null }

    /** One check-in. [preselected] is what the app ticked from the day before the person looked;
     *  [voice] the note recorded with it (status "missing" until the phone has sent it); [voices]
     *  every note said to it, that one first, then the ones added through the day. */
    data class CheckinRow(val day: String, val feelings: String, val why: String,
                          val preselected: String = "", val voice: VoiceNoteRow? = null,
                          val voices: List<VoiceNoteRow> = emptyList())

    suspend fun checkins(ctx: Context, days: Int = 30): List<CheckinRow>? = try {
        val r = BoxHttp.getJson(ctx, "/v1/checkins?days=$days")
        val a = r.optJSONArray("checkins") ?: org.json.JSONArray()
        (0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            val voice = o.optJSONObject("voice")?.let { voiceRow(it) }
            val voices = o.optJSONArray("voices")?.let { a -> (0 until a.length()).mapNotNull { j -> a.optJSONObject(j)?.let { voiceRow(it) } } }
                ?: listOfNotNull(voice) // a box from before 0.0.5 names the first alone
            CheckinRow(o.optString("day"), o.optString("feelings"), o.optString("why"),
                o.optString("preselected"), voice, voices)
        }
    } catch (_: Exception) { null }

    /** A voice note as the box keeps it. status: pending (waiting to be transcribed), done, failed,
     *  missing (named by a check-in, not on the box). */
    data class VoiceNoteRow(val id: String, val kind: String, val day: String, val takenAt: Long,
                            val durationMs: Long, val status: String, val transcript: String,
                            val lang: String, val error: String)

    private fun voiceRow(o: org.json.JSONObject) = VoiceNoteRow(o.optString("id"), o.optString("kind"),
        o.optString("day"), o.optLong("taken_at"), o.optLong("duration_ms"), o.optString("status"),
        o.optString("transcript"), o.optString("lang"), o.optString("error"))

    /** Why a note waits, as the box last said: [working] the note being transcribed now; [why] no
     *  speech engine (etc.); [running] false when ghost.voiced is not running. Null: not asked yet. */
    data class VoiceQueue(val running: Boolean, val why: String, val working: String)

    @Volatile var voiceQueue: VoiceQueue? = null
        private set

    /** The newest voice notes on the box, with their transcripts. Null when the box did not answer. */
    suspend fun voiceNotes(ctx: Context, n: Int = 60): List<VoiceNoteRow>? = try {
        val r = BoxHttp.getJson(ctx, "/v1/voice/notes?n=$n")
        voiceQueue = r.optJSONObject("queue")?.let { VoiceQueue(it.optBoolean("running"), it.optString("why"), it.optString("working")) }
        val a = r.optJSONArray("notes") ?: org.json.JSONArray()
        (0 until a.length()).mapNotNull { i -> a.optJSONObject(i)?.let { voiceRow(it) } }
    } catch (_: Exception) { null }

    /** A question asked aloud (POST /v1/voice/ask): the WAV to the box, the words back. The box keeps
     *  nothing of it. ok false with [why] when the box cannot hear (no speech engine, out of reach);
     *  ok true with "" when nothing was said that it could hear. */
    data class Heard(val ok: Boolean, val text: String, val why: String)

    suspend fun voiceAsk(ctx: Context, wav: java.io.File): Heard = try {
        val r = BoxHttp.postFileJson(ctx, "/v1/voice/ask", wav, "audio/wav", readTimeoutMs = 3 * 60_000)
        if (r.optBoolean("ok")) Heard(true, r.optString("text", "").trim(), "")
        else Heard(false, "", r.optString("why", "the box said no"))
    } catch (e: kotlinx.coroutines.CancellationException) {
        throw e
    } catch (e: Exception) {
        Heard(false, "", e.message ?: "the box did not answer")
    }

    /** Delete a note on the box: its audio, its transcript and its journal entry. */
    suspend fun voiceDelete(ctx: Context, id: String): Boolean = try {
        BoxHttp.postJson(ctx, "/v1/voice/delete", org.json.JSONObject().put("id", id)).optBoolean("ok")
    } catch (_: Exception) { false }

    data class DaySummary(val photos: Int, val videos: Int, val places: List<String>, val notes: List<String>,
        val steps: Int = 0, val sleepMinutes: Int = 0, val exerciseMinutes: Int = 0,
        val suggested: List<String> = emptyList())

    /** What a day looked like , today so far by default (the check-in prefill), or the day the
     *  bounds give (the DAY page). Bounds are the PHONE's day. */
    suspend fun daySummary(ctx: Context, bounds: Pair<Long, Long>? = null): DaySummary? = try {
        val cal = java.util.Calendar.getInstance()
        cal.set(java.util.Calendar.HOUR_OF_DAY, 0); cal.set(java.util.Calendar.MINUTE, 0)
        cal.set(java.util.Calendar.SECOND, 0); cal.set(java.util.Calendar.MILLISECOND, 0)
        val start = bounds?.first ?: (cal.timeInMillis / 1000)
        val end = bounds?.second ?: (start + 86400)
        val r = BoxHttp.getJson(ctx, "/v1/day/summary?start=$start&end=$end")
        fun arr(k: String): List<String> { val x = r.optJSONArray(k) ?: return emptyList()
            return (0 until x.length()).map { x.optString(it) } }
        DaySummary(r.optInt("photos"), r.optInt("videos"), arr("places"), arr("notes"),
            r.optInt("steps"), r.optInt("sleep_minutes"), r.optInt("exercise_minutes"), arr("suggested"))
    } catch (_: Exception) { null }

    /** One day as the box tells it (GET /v1/day): the summary synthd wrote from the photos, the
     *  trail, the health sync, the voice notes and the check-in. [build] asks the box to write it
     *  NOW (the check-in just landed): the box waits for the check-in to land in the journal and
     *  the model writes the day, a minute or two, so the read timeout is long. */
    data class DayStory(val day: String, val title: String, val summary: String, val writtenBy: String, val builtAt: Long, val checkedIn: Boolean)

    suspend fun dayStory(ctx: Context, day: String, build: Boolean = false): DayStory? = try {
        val r = BoxHttp.getJson(ctx, "/v1/day?d=$day" + (if (build) "&build=1" else ""), readTimeoutMs = if (build) 300_000 else 20_000)
        val d = r.optJSONObject("day") ?: org.json.JSONObject()
        DayStory(d.optString("day", day), d.optString("title"), d.optString("summary"), d.optString("writtenBy"), d.optLong("builtAt"), r.optBoolean("checkedIn", false))
    } catch (_: Exception) { null }

    /** A day's photos and videos, oldest first (the archive's newest-first pages read back from
     *  the day's end until its start; 600 at most). */
    suspend fun dayFrames(ctx: Context, start: Long, end: Long): List<GalleryFrame>? {
        val out = ArrayList<GalleryFrame>()
        var before = end
        while (out.size < 600) {
            val page = framesList(ctx, before, 200) ?: return null
            if (page.isEmpty()) break
            for (f in page) if (f.takenAt in start until end) out.add(f)
            val oldest = page.minOf { it.takenAt }
            if (oldest < start || page.size < 200) break
            before = oldest
        }
        return out.sortedBy { it.takenAt }
    }

    data class OtdYear(val year: Int, val yearsAgo: Int, val narrative: String,
        val places: List<String>, val photos: List<String>, val notes: List<String>,
        val title: String = "", val line: String = "")

    /** THE BOX'S WIKIPEDIA, in its database once the mirror's file is imported (GET /v1/wiki). The
     *  state and the edition always; with a phrase, the articles it names, surest first; with an
     *  idx, one article whole. Nothing leaves the box. */
    data class WikiHit(val idx: Long, val title: String, val lead: String, val disamb: Boolean, val how: String)
    data class WikiArticle(val idx: Long, val title: String, val lead: String, val body: String, val disamb: Boolean)
    data class Wiki(val state: String, val edition: String, val articles: Long, val redirects: Long, val imported: Long,
                    val entries: Long, val file: String, val error: String, val hits: List<WikiHit>, val article: WikiArticle?,
                    val leftMinutes: Long = 0, val readers: Int = 0, val startedAt: Long = 0, val doneAt: Long = 0,
                    val skipped: Long = 0, val bytes: Long = 0, val indexed: Boolean = false, val likeness: Boolean = false,
                    val answers: Int = 0)

    // --- SOURCES: what the box draws on, and the fetches from the mirror ---
    data class Source(val id: String, val name: String, val state: String, val line: String, val detail: String,
                      val action: String, val label: String, val open: String, val bytes: Long,
                      val from: List<SourceFrom> = emptyList())
    /** One place an integration draws from: a feed, an exchange, a service, a data set, the mirror. */
    data class SourceFrom(val name: String, val role: String, val state: String)
    data class FetchJob(val step: String, val region: String, val startedAt: Long, val endedAt: Long, val running: Boolean,
                        val exit: Int, val last: String)
    data class Sources(val sources: List<Source>, val job: FetchJob?)

    private fun jobOf(j: org.json.JSONObject?): FetchJob? = j?.let {
        FetchJob(it.optString("step"), it.optString("region"), it.optLong("startedAt"), it.optLong("endedAt"),
            it.optBoolean("running"), it.optInt("exit"), it.optString("last"))
    }

    suspend fun sources(ctx: Context): Sources? = try {
        val r = BoxHttp.getJson(ctx, "/v1/sources", readTimeoutMs = 20_000)
        val a = r.optJSONArray("sources") ?: org.json.JSONArray()
        Sources((0 until a.length()).mapNotNull { i -> a.optJSONObject(i)?.let { o ->
            val fa = o.optJSONArray("from")
            val from = if (fa == null) emptyList() else (0 until fa.length()).mapNotNull { k -> fa.optJSONObject(k)?.let { f ->
                SourceFrom(f.optString("name"), f.optString("role"), f.optString("state")) } }
            Source(o.optString("id"), o.optString("name"), o.optString("state"), o.optString("line"), o.optString("detail"),
                o.optString("action"), o.optString("label"), o.optString("open"), o.optLong("bytes"), from)
        } }, jobOf(r.optJSONObject("job")))
    } catch (e: kotlinx.coroutines.CancellationException) { throw e } catch (_: Exception) { null }

    /** Start update.sh <step> on the box, from the mirror. ok, or why not. */
    suspend fun sourcesFetch(ctx: Context, step: String, region: String = ""): Pair<Boolean, String> = try {
        val r = BoxHttp.postJson(ctx, "/v1/sources/fetch", org.json.JSONObject().put("step", step).put("region", region))
        if (r.optBoolean("ok")) true to "" else false to r.optString("why", "the box said no")
    } catch (e: kotlinx.coroutines.CancellationException) { throw e } catch (e: Exception) { false to (e.message ?: "the box did not answer") }

    data class Feed(val id: String, val name: String, val url: String, val enabled: Boolean, val lastFetch: Long, val lastOk: Long,
                    val lastStatus: String, val lastItems: Int, val failures: Int)

    private fun feedsOf(r: org.json.JSONObject): List<Feed>? {
        val news = r.optJSONObject("news") ?: return null
        val a = news.optJSONArray("feeds") ?: return emptyList()
        return (0 until a.length()).mapNotNull { i -> a.optJSONObject(i)?.let { o ->
            Feed(o.optString("id"), o.optString("name"), o.optString("url"), o.optBoolean("enabled"), o.optLong("lastFetch"),
                o.optLong("lastOk"), o.optString("lastStatus"), o.optInt("lastItems"), o.optInt("failures"))
        } }
    }

    suspend fun newsFeeds(ctx: Context): List<Feed>? = try {
        feedsOf(BoxHttp.getJson(ctx, "/v1/news/feeds", readTimeoutMs = 15_000))
    } catch (e: kotlinx.coroutines.CancellationException) { throw e } catch (_: Exception) { null }

    /** Add (name, url), remove (id) or switch (id, on) a feed; the list after, or null with why. */
    suspend fun newsFeedsChange(ctx: Context, body: org.json.JSONObject): Pair<List<Feed>?, String> = try {
        val r = BoxHttp.postJson(ctx, "/v1/news/feeds", body)
        if (r.optBoolean("ok")) feedsOf(r) to "" else null to r.optString("why", "the box said no")
    } catch (e: kotlinx.coroutines.CancellationException) { throw e } catch (e: Exception) { null to (e.message ?: "the box did not answer") }

    /** The phone's account of Health Connect (sync/HealthSync.kt's HealthDiag), kept on the box. */
    suspend fun healthDiag(ctx: Context, report: org.json.JSONObject): Boolean = try {
        BoxHttp.postJson(ctx, "/v1/health/diag", report).optBoolean("ok")
    } catch (e: kotlinx.coroutines.CancellationException) { throw e } catch (_: Exception) { false }

    // --- THE MET OFFICE key for the box's own forecast (SETTINGS › YOUR BOX) ---
    private fun metOfficeOf(o: JSONObject): com.localghost.app.ui.MetOfficeState {
        val m = o.optJSONObject("metoffice") ?: JSONObject()
        val model = m.optJSONObject("model") ?: JSONObject()
        val h = o.optJSONObject("home") ?: JSONObject()
        return com.localghost.app.ui.MetOfficeState(m.optBoolean("set"), m.optString("order"), model.optString("run"), model.optString("skipped"), model.optString("error"),
            h.optBoolean("known"), h.optString("near"), h.optString("region"), h.optInt("nights"), h.optString("note"))
    }

    suspend fun metOffice(ctx: Context): com.localghost.app.ui.MetOfficeState? = try {
        val r = BoxHttp.getJson(ctx, "/v1/weather/metoffice", readTimeoutMs = 15_000)
        if (r.optBoolean("ok")) metOfficeOf(r) else null
    } catch (e: kotlinx.coroutines.CancellationException) { throw e } catch (_: Exception) { null }

    /** Set the key and the order (a key of "" forgets them); the state after, or null with why. */
    suspend fun metOfficeSet(ctx: Context, key: String, order: String): Pair<com.localghost.app.ui.MetOfficeState?, String> = try {
        val r = BoxHttp.postJson(ctx, "/v1/weather/metoffice", JSONObject().put("key", key).put("order", order))
        if (r.optBoolean("ok")) metOfficeOf(r) to "" else null to r.optString("why", "the box said no")
    } catch (e: kotlinx.coroutines.CancellationException) { throw e } catch (e: Exception) { null to (e.message ?: "the box did not answer") }

    // --- THE WEATHER where the phone is, from the box's daily pull ---
    data class WeatherDay(val date: String, val code: Int, val maxC: Double, val minC: Double, val rainPct: Int)
    data class Weather(val ok: Boolean, val place: String, val country: String, val tempC: Double, val feelsC: Double, val code: Int,
                       val windKmh: Double, val humidity: Int, val days: List<WeatherDay>, val fetchedAt: Long, val places: Long,
                       val text: String, val noGeo: Boolean, val distanceKm: Double,
                       val hours: List<com.localghost.app.ui.WeatherHour> = emptyList(), val utcOffset: Int = 0,
                       /** the model runs the box's own forecast came from ("icon-eu 2026-10-10 09Z · ifs …"); "" for a pulled one */
                       val source: String = "",
                       /** the forecast computed at the phone's own fix (the place is the nearest town's name) */
                       val here: Boolean = false) {
        /** The conditions now: the pull's own when it is fresh, else the hour of the forecast the
         *  clock is in at the place (the pull is once in sixteen hours; its "now" is its own). */
        fun current(nowS: Long): com.localghost.app.ui.WeatherHour? = com.localghost.app.ui.WeatherHours.current(hours, fetchedAt, nowS, utcOffset)
        /** The temperature to show now: from the hour when the pull is old, else the pull's. */
        fun tempNow(nowS: Long): Double = current(nowS)?.tempC ?: tempC
        fun codeNow(nowS: Long): Int = current(nowS)?.code ?: code
    }

    suspend fun weather(ctx: Context, lat: Double, lon: Double): Weather? = try {
        val r = BoxHttp.getJson(ctx, "/v1/weather?lat=$lat&lon=$lon", readTimeoutMs = 15_000)
        val t = r.optJSONObject("table")
        val f = r.optJSONObject("forecast")
        val now = f?.optJSONObject("now")
        val place = f?.optJSONObject("place")
        val days = f?.optJSONArray("days")?.let { a -> (0 until a.length()).mapNotNull { i -> a.optJSONObject(i)?.let { d ->
            WeatherDay(d.optString("date"), d.optInt("code"), d.optDouble("maxC"), d.optDouble("minC"), d.optInt("rainPct")) } } } ?: emptyList()
        val hours = f?.optJSONArray("hours")?.let { a -> (0 until a.length()).mapNotNull { i -> a.optJSONObject(i)?.let { h ->
            com.localghost.app.ui.WeatherHour(h.optString("at"), h.optDouble("tempC"), h.optInt("rainPct", -1), h.optDouble("precipMm"), h.optInt("code"), h.optDouble("windKmh")) } } } ?: emptyList()
        Weather(r.optBoolean("ok"), place?.optString("name") ?: "", place?.optString("country") ?: "",
            now?.optDouble("tempC") ?: Double.NaN, now?.optDouble("feelsC") ?: Double.NaN, now?.optInt("code") ?: -1,
            now?.optDouble("windKmh") ?: 0.0, now?.optInt("humidity") ?: 0, days, f?.optLong("fetchedAt") ?: (t?.optLong("fetchedAt") ?: 0L),
            t?.optLong("places") ?: 0L, r.optString("text"), r.optBoolean("noGeo"), 0.0, hours, f?.optInt("utcOffset") ?: 0, f?.optString("source") ?: "", f?.optBoolean("here") ?: false)
    } catch (e: kotlinx.coroutines.CancellationException) { throw e } catch (_: Exception) { null }

    suspend fun wiki(ctx: Context, q: String = "", idx: Long = 0, n: Int = 8): Wiki? = try {
        val qs = ArrayList<String>()
        if (q.isNotBlank()) qs.add("q=" + java.net.URLEncoder.encode(q.trim(), "UTF-8") + "&n=$n")
        if (idx > 0) qs.add("idx=$idx")
        val r = BoxHttp.getJson(ctx, "/v1/wiki" + (if (qs.isEmpty()) "" else "?" + qs.joinToString("&")), readTimeoutMs = 30_000)
        val hits = r.optJSONArray("hits")?.let { a -> (0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            WikiHit(o.optLong("idx"), o.optString("title"), o.optString("lead"), o.optBoolean("disamb"), o.optString("how"))
        } } ?: emptyList()
        val art = r.optJSONObject("article")?.let { o ->
            WikiArticle(o.optLong("idx"), o.optString("title"), o.optString("lead"), o.optString("body"), o.optBoolean("disamb"))
        }
        Wiki(r.optString("state"), r.optString("edition"), r.optLong("articles"), r.optLong("redirects"), r.optLong("imported"),
            r.optLong("entries"), r.optString("file"), r.optString("error"), hits, art, r.optLong("leftMinutes"),
            r.optInt("readers"), r.optLong("startedAt"), r.optLong("doneAt"), r.optLong("skipped"), r.optLong("bytes"),
            r.optBoolean("indexed"), r.optBoolean("likeness"), r.optInt("answers"))
    } catch (e: Exception) { android.util.Log.w("LocalGhost", "wiki: ${e.message}"); null }

    /** The On This Day retrospective , read from the box's prebuilt day summaries (no model at
     *  request time); years the backfill has not reached yet come with photos and places only. */
    suspend fun onThisDay(ctx: Context): List<OtdYear>? = try {
        val r = BoxHttp.getJson(ctx, "/v1/onthisday")
        val a = r.optJSONArray("years") ?: return emptyList()
        (0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            fun arr(k: String): List<String> { val x = o.optJSONArray(k) ?: return emptyList()
                return (0 until x.length()).map { x.optString(it) } }
            OtdYear(o.optInt("year"), o.optInt("years_ago"), o.optString("narrative"),
                arr("places"), arr("photos"), arr("notes"), o.optString("title"), o.optString("line"))
        }
    } catch (e: Exception) { android.util.Log.w("LocalGhost", "onthisday: ${e.message}"); null }

    /** Send text into the journal: secd drops it in noted's inbox; noted ingests on its next tick. */
    suspend fun noteAdd(ctx: Context, text: String): Boolean = try {
        BoxHttp.postJson(ctx, "/v1/notes", org.json.JSONObject().put("text", text)); true
    } catch (_: Exception) { false }

    /** One memory row. [meta] is the structured detail synthd computed for a machine-assembled
     *  memory (kind "outing": photos, days, place, tags, covers, distanceM, away); null otherwise.
     *  [ref] is what a day's or an outing's memory was made from ("day:2026-10-03"), "" otherwise
     *  (and on a box from before 0.0.5). */
    data class MemRow(val id: Long, val title: String, val body: String, val kind: String, val createdAt: Long,
                      val meta: org.json.JSONObject? = null, val ref: String = "", val edited: Boolean = false) {
        val covers: List<String> get() = meta?.optJSONArray("covers")?.let { a -> (0 until a.length()).map { a.optString(it) }.filter { it.isNotEmpty() } } ?: emptyList()
        /** The trip or the outing this memory is a part of ("trip:2026-09-12", "outing:2026-10-02"), "" for none:
         *  the list shows the whole in its place, and the whole's page lists its parts. */
        val partOf: String get() = meta?.optString("part_of") ?: ""
        /** A trip's line: "8 days · 160 photos · 53 km"; null for anything but a trip. */
        val tripLine: String? get() = if (kind != "trip") null else meta?.let { m ->
            val parts = ArrayList<String>()
            val days = m.optInt("days"); val photos = m.optInt("photos")
            if (days > 0) parts.add("$days day${if (days == 1) "" else "s"}")
            if (photos > 0) parts.add("$photos photo${if (photos == 1) "" else "s"}")
            val km = m.optDouble("distanceM", 0.0)
            if (km >= 950) parts.add(if (km < 10000) "%.1f km".format(java.util.Locale.US, km / 1000) else "${(km / 1000).toInt()} km")
            parts.joinToString(" · ").ifEmpty { null }
        }
        /** The line under the title for a trip or an outing, null for the rest. */
        val summaryLine: String? get() = if (kind == "trip") tripLine else outingLine
        val outingLine: String? get() = meta?.let { m ->
            val photos = m.optInt("photos"); val days = m.optInt("days")
            if (photos == 0) return@let null
            val parts = ArrayList<String>()
            parts.add("$photos photo${if (photos == 1) "" else "s"}")
            if (days > 1) parts.add("$days days")
            val km = m.optDouble("distanceM", 0.0)
            if (km >= 950) parts.add(if (km < 10000) "%.1f km".format(java.util.Locale.US, km / 1000) else "${(km / 1000).toInt()} km")
            if (m.optBoolean("away")) parts.add("a trip")
            parts.joinToString(" · ")
        }
    }

    /** The memories feed: whole memories first (every trip, person and fact), then the parts folded
     *  under a trip, newest first within each, [limit] rows. */
    suspend fun memoriesList(ctx: Context, limit: Int = 600): List<MemRow>? = memoriesAt(ctx, "/v1/memories?limit=$limit")

    /** The parts folded under one whole (a trip's outings and days), oldest first, for its page. */
    suspend fun memoriesParts(ctx: Context, ref: String): List<MemRow>? = memoriesAt(ctx, "/v1/memories?part_of=" + java.net.URLEncoder.encode(ref, "UTF-8"))

    private suspend fun memoriesAt(ctx: Context, path: String): List<MemRow>? = try {
        val r = BoxHttp.getJson(ctx, path)
        val a = r.optJSONArray("memories") ?: return emptyList()
        (0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            MemRow(o.optLong("id"), o.optString("title"), o.optString("body"),
                o.optString("kind"), o.optLong("created_at"), o.optJSONObject("meta"), o.optString("ref"), o.optBoolean("edited"))
        }
    } catch (e: Exception) { android.util.Log.w("LocalGhost", "memories: ${e.message}"); null }

    /** WHAT THE PHOTOS SAY YOU LIKE , synthd's taste: the tags that recur across the days with a
     *  camera out (share = the fraction of those days), folded onto the fixed interests. */
    data class Like(val tag: String, val category: String, val share: Double, val days: Int)
    data class Interest(val name: String, val weight: Double, val tags: List<String>)
    data class Taste(val summary: String, val days: Int, val photos: Int, val likes: List<Like>, val interests: List<Interest>, val note: String)

    suspend fun taste(ctx: Context): Taste? = try {
        val r = BoxHttp.getJson(ctx, "/v1/taste")
        val likes = r.optJSONArray("likes")?.let { a -> (0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            Like(o.optString("tag"), o.optString("category"), o.optDouble("share", 0.0), o.optInt("days"))
        } } ?: emptyList()
        val interests = r.optJSONArray("interests")?.let { a -> (0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            val tags = o.optJSONArray("tags")?.let { t -> (0 until t.length()).map { t.optString(it) } } ?: emptyList()
            Interest(o.optString("name"), o.optDouble("weight", 0.0), tags)
        } } ?: emptyList()
        Taste(r.optString("summary"), r.optInt("days"), r.optInt("photos"), likes, interests, r.optString("note"))
    } catch (e: Exception) { android.util.Log.w("LocalGhost", "taste: ${e.message}"); null }

    /** PLACES NEAR A POSITION THAT FIT THE TASTE , from the box's own geo data, ranked, with the
     *  reason and how many of your photos lie within a kilometre (0 = new to you). */
    data class Suggestion(val name: String, val kind: String, val interest: String, val distanceKm: Double, val bearing: String,
                          val why: String, val beenThere: Int, val lat: Double, val lon: Double)
    data class Nearby(val suggestions: List<Suggestion>, val note: String, val km: Double)

    suspend fun nearby(ctx: Context, lat: Double, lon: Double, km: Int = 15): Nearby? = try {
        val r = BoxHttp.getJson(ctx, "/v1/nearby?lat=${"%.5f".format(java.util.Locale.US, lat)}&lon=${"%.5f".format(java.util.Locale.US, lon)}&km=$km")
        val list = r.optJSONArray("suggestions")?.let { a -> (0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            Suggestion(o.optString("name"), o.optString("kind"), o.optString("interest"), o.optDouble("distanceKm", 0.0),
                o.optString("bearing"), o.optString("why"), o.optInt("beenThere"), o.optDouble("lat", 0.0), o.optDouble("lon", 0.0))
        } } ?: emptyList()
        Nearby(list, r.optString("note"), r.optDouble("km", km.toDouble()))
    } catch (e: Exception) { android.util.Log.w("LocalGhost", "nearby: ${e.message}"); null }

    suspend fun memoryAdd(ctx: Context, title: String, body: String): Boolean = try {
        BoxHttp.postJson(ctx, "/v1/memories/add", org.json.JSONObject().put("title", title).put("body", body)); true
    } catch (_: Exception) { false }

    suspend fun memoryEdit(ctx: Context, id: Long, title: String, body: String): Boolean = try {
        BoxHttp.postJson(ctx, "/v1/memories/edit", org.json.JSONObject().put("id", id).put("title", title).put("body", body)); true
    } catch (_: Exception) { false }

    suspend fun memoryDelete(ctx: Context, id: Long): Boolean = try {
        BoxHttp.postJson(ctx, "/v1/memories/delete", org.json.JSONObject().put("id", id)); true
    } catch (_: Exception) { false }

    data class StatsFreshness(val stale: Boolean, val ageSeconds: Long)

    /** The sampler's freshness , stale means the WATCHER is down and status rows show the past. */
    suspend fun statsFreshness(ctx: Context): StatsFreshness? = try {
        val r = BoxHttp.getJson(ctx, "/v1/services/summary")
        StatsFreshness(r.optBoolean("stale", false), r.optLong("age_seconds", -1))
    } catch (_: Exception) { null }

    data class GeoPoint(val hash: String, val lat: Double, val lon: Double, val takenAt: Long, val place: String)

    /** GPS-bearing frames as dots for the map. Optional bbox; capped server-side. */
    suspend fun framesGeo(ctx: Context): List<GeoPoint>? = try {
        val r = BoxHttp.getJson(ctx, "/v1/frames/geo")
        val a = r.optJSONArray("points") ?: return emptyList()
        (0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            GeoPoint(o.optString("hash"), o.optDouble("lat"), o.optDouble("lon"),
                o.optLong("taken_at"), o.optString("place"))
        }
    } catch (e: Exception) { android.util.Log.w("LocalGhost", "frames geo: ${e.message}"); null }

    /** One stage of the archive pipeline: how many photos and videos have it, of how many. */
    data class Stage(val done: Int, val total: Int) {
        val left: Int get() = (total - done).coerceAtLeast(0)
        val pct: Int get() = if (total <= 0) 100 else (done * 100L / total).toInt()
    }
    data class Queue(val pending: Int, val parked: Int)
    /** framed's own stock-take, as it publishes it while running and after. */
    data class Converge(
        val running: Boolean, val startedAt: Long, val finishedAt: Long,
        val toDo: Int, val done: Int, val summary: String,
        val rederived: Int, val notified: Int, val unrenderable: Int,
    )
    data class Pipeline(
        val version: Int, val photos: Int, val videos: Int, val total: Int,
        val derived: Stage, val previewed: Stage, val described: Stage, val titled: Stage,
        val tagged: Stage, val atLatest: Stage,
        val caption: Queue, val tag: Queue, val embed: Queue,
        val describedLastHour: Int, val describedLastDay: Int, val lastDescribedAt: Long,
        val etaSeconds: Long, val converge: Converge?, val convergeUpdatedAt: Long, val now: Long,
        /** Damaged photos framed moved out of the archive into frames/damaged, for the owner to delete. */
        val damaged: Int = 0,
    )

    /** GET /v1/pipeline: the stage-by-stage progress the Box Status screen draws. Null when the
     *  box has never heard of it (older build) or is down. */
    suspend fun pipeline(ctx: Context): Pipeline? = try {
        val r = BoxHttp.getJson(ctx, "/v1/pipeline")
        if (!r.has("total")) null else {
            fun stage(k: String) = r.optJSONObject(k)?.let { Stage(it.optInt("done"), it.optInt("total")) } ?: Stage(0, 0)
            fun queue(k: String) = r.optJSONObject(k)?.let { Queue(it.optInt("pending"), it.optInt("parked")) } ?: Queue(0, 0)
            val cv = r.optJSONObject("converge")?.let { c ->
                val rep = c.optJSONObject("report")
                Converge(c.optBoolean("running"), c.optLong("startedAt"), c.optLong("finishedAt"),
                    c.optInt("toDo"), c.optInt("done"), c.optString("summary"),
                    rep?.optInt("rederived") ?: 0, rep?.optInt("notified") ?: 0, rep?.optInt("unrenderable") ?: 0)
            }
            Pipeline(
                r.optInt("pipelineVersion"), r.optInt("photos"), r.optInt("videos"), r.optInt("total"),
                stage("derived"), stage("previewed"), stage("described"), stage("titled"),
                stage("tagged"), stage("atLatest"),
                queue("caption"), queue("tag"), queue("embed"),
                r.optInt("describedLastHour"), r.optInt("describedLastDay"), r.optLong("lastDescribedAt"),
                r.optLong("etaSeconds", -1), cv, r.optLong("convergeUpdatedAt"), r.optLong("now"),
                r.optInt("damaged"),
            )
        }
    } catch (e: Exception) { android.util.Log.w("LocalGhost", "pipeline: ${e.message}"); null }

    /** GET /v1/feeds/status: how each feed the box pulls in is doing (prices, exchanges, history,
     *  CRYPTO50, ECB, rank list, daily candles, news), judged by the box. Null when the box is down
     *  or older than the report. */
    suspend fun feedsStatus(ctx: Context): com.localghost.app.ui.FeedsText.Report? = try {
        com.localghost.app.ui.FeedsText.parse(BoxHttp.getJson(ctx, "/v1/feeds/status"))
    } catch (e: Exception) { android.util.Log.w("LocalGhost", "feeds status: ${e.message}"); null }

    /** Per-daemon drill-in rows for the Box Status detail screens. */
    /** The drill-in rows; key marks the rows the box wants read first (the rest fold behind "more"). */
    suspend fun daemonSummary(ctx: Context, name: String): List<com.localghost.app.ui.DaemonRows.Row>? = try {
        val r = BoxHttp.getJson(ctx, "/v1/daemon/summary?name=$name")
        val a = r.optJSONArray("rows") ?: org.json.JSONArray()
        (0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            com.localghost.app.ui.DaemonRows.Row(o.optString("k"), o.optString("v"), o.optBoolean("key", false))
        }
    } catch (_: Exception) { null }

    // --- what the phone fetches for the box: news feeds and market tickers (sync/BoxFetch) ---

    /** The network this phone is on, as the box wants to hear it: wifi, mobile or none. */
    fun netKind(ctx: Context): String {
        val cm = ctx.getSystemService(Context.CONNECTIVITY_SERVICE) as? android.net.ConnectivityManager ?: return "none"
        val caps = cm.getNetworkCapabilities(cm.activeNetwork) ?: return "none"
        return when {
            caps.hasTransport(android.net.NetworkCapabilities.TRANSPORT_WIFI) || caps.hasTransport(android.net.NetworkCapabilities.TRANSPORT_ETHERNET) -> "wifi"
            caps.hasTransport(android.net.NetworkCapabilities.TRANSPORT_CELLULAR) -> "mobile"
            else -> "none"
        }
    }

    /** Tell the box the network (/v1/phone/net): on Wi-Fi this phone fetches for it, else the box does. */
    suspend fun reportNet(ctx: Context): Boolean = try {
        BoxHttp.postJson(ctx, "/v1/phone/net", org.json.JSONObject().put("net", netKind(ctx)), 10_000).optBoolean("ok")
    } catch (_: Exception) { false }

    data class FetchSource(val id: String, val name: String, val url: String, val every: Int)
    data class FetchList(val feeds: List<FetchSource>, val rates: List<FetchSource>, val feedsEvery: Int)

    /** What the box wants fetched (/v1/fetch/list); null when unreachable. */
    suspend fun fetchList(ctx: Context): FetchList? = try {
        val r = BoxHttp.getJson(ctx, "/v1/fetch/list")
        fun arr(k: String, every: Int): List<FetchSource> {
            val a = r.optJSONArray(k) ?: return emptyList()
            return (0 until a.length()).mapNotNull { i ->
                val o = a.optJSONObject(i) ?: return@mapNotNull null
                FetchSource(o.optString("id"), o.optString("name"), o.optString("url"), o.optInt("every", every))
            }
        }
        val fe = r.optInt("feedsEvery", 120)
        FetchList(arr("feeds", fe), arr("rates", 60), fe)
    } catch (_: Exception) { null }

    /** One fetched body for the box: the id, the HTTP status (0 when the fetch failed), the error, the body. */
    fun fetchedJson(fetchedAt: Long, key: String, rows: List<Fetched>): org.json.JSONObject {
        val arr = org.json.JSONArray()
        rows.forEach { f ->
            arr.put(org.json.JSONObject().apply {
                put("id", f.id); put("status", f.status)
                if (f.error.isNotEmpty()) put("error", f.error)
                if (f.body.isNotEmpty()) put("body", f.body)
                if (f.tookMs > 0) put("tookMs", f.tookMs) // for the box's fetch log
            })
        }
        return org.json.JSONObject().apply { put("fetchedAt", fetchedAt); put(key, arr) }
    }
    data class Fetched(val id: String, val status: Int, val error: String, val body: String, val tookMs: Long = 0)

    suspend fun postNewsFetched(ctx: Context, fetchedAt: Long, rows: List<Fetched>): Boolean = try {
        BoxHttp.postJson(ctx, "/v1/news/fetched", fetchedJson(fetchedAt, "feeds", rows), 60_000).optBoolean("ok")
    } catch (_: Exception) { false }

    suspend fun postRatesFetched(ctx: Context, fetchedAt: Long, rows: List<Fetched>): Boolean = try {
        BoxHttp.postJson(ctx, "/v1/rates/fetched", fetchedJson(fetchedAt, "sources", rows), 60_000).optBoolean("ok")
    } catch (_: Exception) { false }

    data class NewsItem(val feed: String, val outlet: String, val title: String, val link: String, val summary: String, val published: Long)
    data class NewsStory(val id: Long, val title: String, val summary: String, val sources: Int, val firstSeen: Long, val lastSeen: Long, val items: List<NewsItem>)
    /** [brief]: the day's news as one "- " point per story, written on the box from the most-told
     *  stories' summaries ("" before the first); [briefStories] are the stories the points tell,
     *  in order. A story's [NewsStory.summary] is a lead and points the same way (NewsText.told). */
    data class News(val stories: List<NewsStory>, val lastFetch: Long, val lastDigest: Long, val brief: String = "", val briefAt: Long = 0,
                    val briefStories: List<Long> = emptyList())

    /** The stories since a time (/v1/news); null when unreachable. [keep]: home's read, kept on
     *  the phone so home opens on it next time (HomeCache). */
    suspend fun news(ctx: Context, since: Long = 0, keep: Boolean = false): News? = try {
        val r = BoxHttp.getJson(ctx, "/v1/news" + (if (since > 0) "?since=$since" else ""))
        val n = newsFrom(r)
        if (keep && r.has("stories")) HomeCache.putNews(ctx, r)
        n
    } catch (_: Exception) { null }

    /** /v1/news's answer read (also the copy HomeCache keeps). */
    fun newsFrom(r: org.json.JSONObject): News {
        val a = r.optJSONArray("stories") ?: org.json.JSONArray()
        return News((0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            val ia = o.optJSONArray("items") ?: org.json.JSONArray()
            NewsStory(o.optLong("id"), o.optString("title"), o.optString("summary"), o.optInt("sources"),
                o.optLong("firstSeen"), o.optLong("lastSeen"),
                (0 until ia.length()).mapNotNull { j ->
                    val it = ia.optJSONObject(j) ?: return@mapNotNull null
                    NewsItem(it.optString("feed"), it.optString("outlet"), it.optString("title"), it.optString("link"), it.optString("summary"), it.optLong("published"))
                })
        }, r.optLong("lastFetch"), r.optLong("lastDigest"), r.optString("brief"), r.optLong("briefAt"),
            r.optJSONArray("briefStories")?.let { b -> (0 until b.length()).map { b.optLong(it) } } ?: emptyList())
    }

    /** [supply]: Coinbase's circulating supply, so the cap can follow the box's own price. */
    data class CoinRow(val rank: Int, val symbol: String, val name: String, val priceUsd: Double, val marketCap: Double, val change24: Double, val supply: Double = 0.0)
    /** The box's USD price of one symbol and how it was made. */
    data class IndexRow(val symbol: String, val price: Double, val at: Long, val n: Int, val spread: Double, val used: String, val change24: Double? = null,
                        /** markets blended (a venue can have several) and the currencies they were converted from */
                        val markets: Int = 0, val paths: String = "")
    /** The market index: one number for crypto as a whole (the fifty largest, weighted by last month's volume). */
    data class Market(val code: String, val value: Double, val dayChange: Double, val constituents: Int, val priced: Int, val month: String)
    data class Rates(val fxDay: String, val fx: Map<String, Double>, val index: List<IndexRow>, val btcUsd: Double, val btcAt: Long, val btcN: Int, val btcSpread: Double, val btcUsed: String, val ranksAt: Long, val ranks: List<CoinRow>, val ranksSource: String, val days: Int, val fxDays: Int, val market: Market?)

    /** The box's market numbers (/v1/rates); null when unreachable. [keep]: kept on the phone for
     *  home and CRYPTO to open on (HomeCache). */
    suspend fun rates(ctx: Context, keep: Boolean = false): Rates? = try {
        val r = BoxHttp.getJson(ctx, "/v1/rates")
        val out = ratesFrom(r)
        if (keep && r.has("index")) HomeCache.putRates(ctx, r)
        out
    } catch (_: Exception) { null }

    /** /v1/rates's answer read (also the copy HomeCache keeps). */
    fun ratesFrom(r: org.json.JSONObject): Rates {
        val fx = HashMap<String, Double>()
        r.optJSONObject("fx")?.let { o -> o.keys().forEach { k -> fx[k] = o.optDouble(k) } }
        val ra = r.optJSONArray("ranks") ?: org.json.JSONArray()
        val index = ArrayList<IndexRow>()
        r.optJSONObject("index")?.let { o ->
            o.keys().forEach { sym ->
                val row = o.optJSONObject(sym) ?: return@forEach
                index.add(IndexRow(sym, row.optDouble("price", 0.0), row.optLong("at"), row.optInt("n"), row.optDouble("spread", 0.0), row.optString("used"),
                    if (row.optBoolean("hasChange")) row.optDouble("change24", 0.0) else null, row.optInt("markets"), row.optString("paths")))
            }
        }
        index.sortBy { it.symbol }
        return Rates(r.optString("fxDay"), fx, index, r.optDouble("btcUsd", 0.0), r.optLong("btcAt"), r.optInt("btcN"), r.optDouble("btcSpread", 0.0), r.optString("btcUsed"),
            r.optLong("ranksAt"), (0 until ra.length()).mapNotNull { i ->
                val o = ra.optJSONObject(i) ?: return@mapNotNull null
                CoinRow(o.optInt("rank"), o.optString("symbol"), o.optString("name"), o.optDouble("priceUsd", 0.0), o.optDouble("marketCap", 0.0), o.optDouble("change24", 0.0),
                    o.optDouble("supply", 0.0))
            }, r.optString("ranksSource"), r.optInt("days"), r.optInt("fxDays"),
            r.optJSONObject("market")?.let { m -> Market(m.optString("code"), m.optDouble("value", 0.0), m.optDouble("dayChange", 0.0), m.optInt("constituents"), m.optInt("priced"), m.optString("month")) })
    }

    /** Home as it stands (/v1/home): the prices, CRYPTO50, the brief, the top stories and FOR YOU;
     *  kept (HomeCache). Null when unreachable. */
    suspend fun home(ctx: Context): HomeData.Snap? = try {
        HomeCache.putSnap(ctx, BoxHttp.getJson(ctx, "/v1/home"))
    } catch (_: Exception) { null }

    /** The fast lane's last pass: when (unix ms; 0 when quiet), and each coin's price, 24-hour
     *  change and how many exchanges went in. */
    data class Fast(val at: Long, val prices: Map<String, Pair<Double, Double?>>, val venues: Map<String, Int>)

    /** BTC, ETH and SOL as of the last five seconds (/v1/rates/fast, from the box's Redis, made from
     *  every exchange the box follows); null when unreachable. */
    suspend fun fast(ctx: Context): Fast? = try {
        val r = BoxHttp.getJson(ctx, "/v1/rates/fast")
        val prices = HashMap<String, Pair<Double, Double?>>()
        val venues = HashMap<String, Int>()
        r.optJSONObject("index")?.let { o ->
            o.keys().forEach { sym ->
                val row = o.optJSONObject(sym) ?: return@forEach
                val p = row.optDouble("price", 0.0)
                if (p > 0) {
                    prices[sym] = p to (if (row.optBoolean("hasChange")) row.optDouble("change24", 0.0) else null)
                    venues[sym] = row.optInt("n")
                }
            }
        }
        Fast(r.optLong("at"), prices, venues)
    } catch (_: Exception) { null }

    /** One market's part in a coin's price: its price in the quote currency and in dollars, its
     *  24-hour volume in the coin, how old, its share of the price, or why it was left out. */
    data class CoinMarket(val exchange: String, val quote: String, val price: Double, val usd: Double, val volume: Double,
                          val ageS: Long, val weight: Double, val out: String)
    /** A coin's page (/v1/coins/info). */
    data class CoinPage(val symbol: String, val name: String, val rank: Int, val description: String, val color: String,
                        val website: String, val whitepaper: String, val listPrice: Double, val marketCap: Double, val supply: Double,
                        val volume24: Double, val change24: Double, val index: IndexRow?, val markets: List<CoinMarket>,
                        /** what the box wrote about it from what it read, when, and from which sources ("Wikipedia, Coinbase, solana.com") */
                        val written: String = "", val writtenFrom: String = "", val writtenAt: Long = 0)

    suspend fun coinPage(ctx: Context, symbol: String): CoinPage? = try {
        val r = BoxHttp.getJson(ctx, "/v1/coins/info?symbol=" + java.net.URLEncoder.encode(symbol, "UTF-8"))
        val ix = r.optJSONObject("index")?.let { o ->
            IndexRow(symbol, o.optDouble("price", 0.0), o.optLong("at"), o.optInt("n"), o.optDouble("spread", 0.0), o.optString("used"),
                if (o.optBoolean("hasChange")) o.optDouble("change24", 0.0) else null, o.optInt("markets"), o.optString("paths"))
        }
        val ma = r.optJSONArray("markets") ?: org.json.JSONArray()
        CoinPage(r.optString("symbol", symbol), r.optString("name", symbol), r.optInt("rank"), r.optString("description"), r.optString("color"),
            r.optString("website"), r.optString("whitepaper"), r.optDouble("listPrice", 0.0), r.optDouble("marketCap", 0.0),
            r.optDouble("supply", 0.0), r.optDouble("volume24", 0.0), r.optDouble("change24", 0.0), ix,
            (0 until ma.length()).mapNotNull { i ->
                val o = ma.optJSONObject(i) ?: return@mapNotNull null
                CoinMarket(o.optString("exchange"), o.optString("quote"), o.optDouble("price", 0.0), o.optDouble("usd", 0.0),
                    o.optDouble("volume", 0.0), o.optLong("ageS"), o.optDouble("weight", 0.0), o.optString("out"))
            }, r.optString("written"), r.optString("writtenFrom"), r.optLong("writtenAt"))
    } catch (_: Exception) { null }

    /** One point of a price series: when (unix s) and the close in dollars. */
    data class PricePoint(val t: Long, val close: Double)

    /** A coin's price every minute (res "1m", up to a week) or hour ("1h", up to thirty days), oldest first. */
    suspend fun priceSeries(ctx: Context, code: String, res: String, hours: Int): List<PricePoint>? = try {
        val r = BoxHttp.getJson(ctx, "/v1/rates/series?code=" + java.net.URLEncoder.encode(code, "UTF-8") + "&res=$res&hours=$hours")
        val a = r.optJSONArray("points") ?: org.json.JSONArray()
        (0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            val c = o.optDouble("c", 0.0)
            if (c > 0) PricePoint(o.optLong("ts"), c) else null
        }
    } catch (_: Exception) { null }

    /** A coin's daily closes, oldest first (the box's daily index, back through the years). */
    suspend fun priceDays(ctx: Context, code: String, days: Int): List<PricePoint>? = try {
        val r = BoxHttp.getJson(ctx, "/v1/rates/history?code=" + java.net.URLEncoder.encode(code, "UTF-8") + "&days=$days")
        val a = r.optJSONArray("days") ?: org.json.JSONArray()
        val fmt = java.text.SimpleDateFormat("yyyy-MM-dd", java.util.Locale.US).apply { timeZone = java.util.TimeZone.getTimeZone("UTC") }
        (0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            val c = o.optDouble("close", 0.0)
            val t = runCatching { fmt.parse(o.optString("day"))!!.time / 1000 }.getOrNull() ?: return@mapNotNull null
            if (c > 0) PricePoint(t, c) else null
        }.sortedBy { it.t }
    } catch (_: Exception) { null }

    /** A week of hourly closes for every coin, for CRYPTO's rows (/v1/rates/sparks). */
    suspend fun sparks(ctx: Context): Map<String, List<Double>>? = try {
        val r = BoxHttp.getJson(ctx, "/v1/rates/sparks")
        val out = HashMap<String, List<Double>>()
        r.optJSONObject("sparks")?.let { o ->
            o.keys().forEach { k ->
                val a = o.optJSONArray(k) ?: return@forEach
                out[k] = (0 until a.length()).map { a.optDouble(it, 0.0) }.filter { it > 0 }
            }
        }
        out
    } catch (_: Exception) { null }

    /** The note about me and my people (/v1/about): what it says, the name it gives, and how many
     *  memories the box made from it; pending while the box has not read this version. */
    data class About(val text: String, val updatedAt: Long, val name: String, val me: Int, val people: Int, val pending: Boolean)

    suspend fun about(ctx: Context): About? = try {
        val r = BoxHttp.getJson(ctx, "/v1/about")
        About(r.optString("text"), r.optLong("updatedAt"), r.optString("name"), r.optInt("me"), r.optInt("people"), r.optBoolean("pending"))
    } catch (_: Exception) { null }

    suspend fun saveAbout(ctx: Context, text: String): About? = try {
        val r = BoxHttp.postJson(ctx, "/v1/about", org.json.JSONObject().put("text", text))
        About(r.optString("text"), r.optLong("updatedAt"), r.optString("name"), r.optInt("me"), r.optInt("people"), r.optBoolean("pending"))
    } catch (_: Exception) { null }

    /** What "write the brief now" did: written, or why not, and the brief as it stands. */
    data class BriefNow(val written: Boolean, val why: String, val brief: String, val briefAt: Long, val briefStories: List<Long>)

    /** The day's brief written now (/v1/news/brief, up to two minutes on the box); null when unreachable. */
    suspend fun writeBrief(ctx: Context): BriefNow? = try {
        val r = BoxHttp.postJson(ctx, "/v1/news/brief", org.json.JSONObject(), 180_000)
        BriefNow(r.optBoolean("written"), r.optString("why"), r.optString("brief"), r.optLong("briefAt"),
            r.optJSONArray("briefStories")?.let { b -> (0 until b.length()).map { b.optLong(it) } } ?: emptyList())
    } catch (_: Exception) { null }

    /** One country as the box lists it: the tiles it holds for it, and their size on disk. */
    data class CountryRow(val code: String, val name: String, val streets: Int, val major: Int, val coast: Int, val bytes: Long)
    data class Countries(val rows: List<CountryRow>, val roads: Boolean, val land: Boolean)

    /** Every country with what the box holds for it (/v1/geo/countries); null when unreachable. */
    suspend fun countries(ctx: Context): Countries? = try {
        val r = BoxHttp.getJson(ctx, "/v1/geo/countries")
        val a = r.optJSONArray("countries") ?: org.json.JSONArray()
        Countries((0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            CountryRow(o.optString("code"), o.optString("name"), o.optInt("streets"), o.optInt("major"),
                o.optInt("coast"), o.optLong("bytes"))
        }, r.optBoolean("roads"), r.optBoolean("land"))
    } catch (_: Exception) { null }

    /** One country's tiles as index keys (/v1/geo/country?code=); null when unreachable or unknown. */
    internal suspend fun countryCells(ctx: Context, code: String): com.localghost.app.local.MapPlan.Country? = try {
        val r = BoxHttp.getJson(ctx, "/v1/geo/country?code=" + java.net.URLEncoder.encode(code, "UTF-8"))
        fun ints(k: String): IntArray {
            val a = r.optJSONArray(k) ?: return IntArray(0)
            return IntArray(a.length()) { a.optInt(it) }
        }
        if (!r.has("code")) null
        else com.localghost.app.local.MapPlan.Country(r.optString("code"), r.optString("name"),
            ints("fine"), ints("majorKeys"), ints("coastKeys"), r.optLong("bytes"))
    } catch (_: Exception) { null }

    /** The country a point is in, from the box's own Natural Earth polygons (/v1/geo/at): the
     *  two-letter code, or null (at sea, no box to ask, no answer). The lock-screen phrases follow
     *  it (LocationLog.geocode); no geocoder outside the box ever sees a fix. */
    suspend fun countryAt(ctx: Context, lat: Double, lon: Double): String? = try {
        val r = BoxHttp.getJson(ctx, "/v1/geo/at?lat=%.4f&lon=%.4f".format(java.util.Locale.US, lat, lon))
        r.optString("country").takeIf { it.length in 2..3 }
    } catch (_: Exception) { null }

    data class GeoCell(val lat: Double, val lon: Double, val n: Int, val hash: String, val takenAt: Long)

    /** A name on the map. kind: C country, R region, X capital, P any other populated place. */
    data class GeoLabel(val name: String, val lat: Double, val lon: Double, val kind: String, val pop: Long)

    /** The names the box would draw for a view, best first (its own GeoNames rows, ranked at
     *  import). Null when the box has no such endpoint; empty when it has one and no ranks yet. */
    suspend fun geoLabels(ctx: Context, minLat: Double, maxLat: Double, minLon: Double, maxLon: Double, n: Int): List<GeoLabel>? = try {
        val r = BoxHttp.getJson(ctx, "/v1/geo/labels?minlat=$minLat&maxlat=$maxLat&minlon=$minLon&maxlon=$maxLon&n=$n")
        if (!r.has("labels")) null else {
            val a = r.optJSONArray("labels") ?: org.json.JSONArray()
            (0 until a.length()).mapNotNull { i ->
                val o = a.optJSONObject(i) ?: return@mapNotNull null
                val lat = o.optDouble("lat", Double.NaN); val lon = o.optDouble("lon", Double.NaN)
                if (!lat.isFinite() || !lon.isFinite()) null
                else GeoLabel(o.optString("name"), lat, lon, o.optString("k", "P"), o.optLong("pop"))
            }
        }
    } catch (e: Exception) { null }

    /** Level-of-detail map feed , postgres aggregates per zoom tier (0 continent .. 3 raw). */
    suspend fun framesGeoLod(ctx: Context, level: Int,
                             minLat: Double, maxLat: Double, minLon: Double, maxLon: Double): List<GeoCell>? = try {
        val r = BoxHttp.getJson(ctx,
            "/v1/frames/geo/lod?level=$level&minlat=$minLat&maxlat=$maxLat&minlon=$minLon&maxlon=$maxLon")
        // No "points" key at all = a box that has never heard of this endpoint (503 body parsed
        // into an empty object) , that is NULL (unknown), not an empty answer. The distinction is
        // what lets the version-skew shield fire.
        if (!r.has("points")) return null
        val a = r.optJSONArray("points") ?: org.json.JSONArray()
        (0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            GeoCell(o.optDouble("lat"), o.optDouble("lon"), o.optInt("n", 1),
                o.optString("hash", ""), o.optLong("takenAt"))
        }
    } catch (_: Exception) { null }

    /** The newest geotagged frame , where the map opens. */
    suspend fun newestGeoFrame(ctx: Context): GeoCell? = try {
        val o = BoxHttp.getJson(ctx, "/v1/frames/newest")
        if (o.optDouble("lat", 0.0) == 0.0 && o.optDouble("lon", 0.0) == 0.0) null
        else GeoCell(o.optDouble("lat"), o.optDouble("lon"), 1, o.optString("hash", ""), o.optLong("takenAt"))
    } catch (_: Exception) { null }

    /** Which day tracks exist on the box, newest first. */
    suspend fun geoDays(ctx: Context, limit: Int = 30): List<String>? = try {
        val r = BoxHttp.getJson(ctx, "/v1/geo/days?limit=$limit")
        val a = r.optJSONArray("days") ?: return emptyList()
        (0 until a.length()).map { a.optString(it) }
    } catch (_: Exception) { null }

    /** Ask the box's model what a question needs from the web before the phone searches
     *  (/v1/chat/plan). Null when the box cannot say (old box, model busy, {"ok":false}) , the
     *  phone then plans by itself, as it always did. Bounded to a few seconds: a plan that takes
     *  longer than the search it saves is not worth waiting for. */
    /** The box's plan and its speed. [plan] is null when the box could not plan (old box, model on
     *  its CPU, no answer in time) , the phone then plans by itself; [box] is null when the box
     *  said nothing about its speed, which the phone reads as "slow or far". */
    class PlanAnswer(val plan: WebSearch.Plan?, val box: WebSearch.BoxSpeed?)

    suspend fun chatPlan(ctx: Context, prompt: String, history: List<Message>, timeoutMs: Long = 9000): PlanAnswer =
        kotlinx.coroutines.withTimeoutOrNull(timeoutMs) {
            try {
                val hist = org.json.JSONArray().apply {
                    history.filter { it.text.isNotBlank() }.takeLast(4).forEach { m ->
                        put(org.json.JSONObject().put("role", if (m.role == Message.Role.USER) "user" else "assistant").put("content", m.text.take(600)))
                    }
                }
                val r = BoxHttp.postJson(ctx, "/v1/chat/plan", org.json.JSONObject().put("prompt", prompt).apply { if (hist.length() > 0) put("history", hist) })
                val box = r.optJSONObject("box")?.let { b ->
                    WebSearch.BoxSpeed(b.optBoolean("known"), b.optBoolean("onGPU"), b.optDouble("promptTPS", 0.0), b.optDouble("genTPS", 0.0))
                }
                if (!r.optBoolean("ok", false)) PlanAnswer(null, box)
                else {
                    val qs = r.optJSONArray("queries")?.let { a -> (0 until a.length()).map { a.optString(it) }.filter { it.isNotBlank() } } ?: emptyList()
                    PlanAnswer(WebSearch.Plan(r.optBoolean("search", true), r.optString("need", ""), r.optString("shape", "prose"), r.optBoolean("fresh", false), qs, box,
                        r.optString("boxHas", "")), box)
                }
            } catch (e: Exception) {
                android.util.Log.w("LocalGhost", "chat plan: ${e.message}"); PlanAnswer(null, null)
            }
        } ?: PlanAnswer(null, null)

    /** The newest N day tracks in ONE round trip, each an ordered list of lat/lon pairs. Null when
     *  the box predates /v1/geo/tracks (the caller falls back to days + one fetch per day). */
    suspend fun geoTracks(ctx: Context, limit: Int = 14): List<Pair<String, List<Pair<Double, Double>>>>? = try {
        val r = BoxHttp.getJson(ctx, "/v1/geo/tracks?limit=$limit")
        if (!r.has("tracks")) null
        else {
            val a = r.optJSONArray("tracks") ?: org.json.JSONArray()
            (0 until a.length()).mapNotNull { i ->
                val o = a.optJSONObject(i) ?: return@mapNotNull null
                val c = o.optJSONArray("coords") ?: return@mapNotNull null
                val pts = (0 until c.length()).mapNotNull { j ->
                    val p = c.optJSONArray(j) ?: return@mapNotNull null
                    Pair(p.optDouble(0), p.optDouble(1)) // already [lat, lon]
                }
                Pair(o.optString("day", ""), pts)
            }
        }
    } catch (_: Exception) { null }

    /** One day of the trail as the box has it: the simplified line with, from boxes at or past the
     *  times build, a clock per vertex and the day's distance over the raw points. */
    data class DayTrack(val day: String, val lat: DoubleArray, val lon: DoubleArray, val times: LongArray, val distanceM: Double, val glitches: Int = 0,
                        val line: String = "", val walkM: Double = 0.0, val rideM: Double = 0.0, val stays: Int = 0,
                        val questions: List<TrailQuestion> = emptyList(),
                        /** the ground's height under each point in metres (Int.MIN_VALUE where the box
                         *  has no tile), empty without the elevation tiles; the day's climb and descent,
                         *  highest and lowest */
                        val alts: IntArray = IntArray(0), val climbM: Double = 0.0, val descentM: Double = 0.0,
                        val highM: Double = 0.0, val lowM: Double = 0.0) {
        val n: Int get() = lat.size
        val hasAlts: Boolean get() = alts.size == lat.size && lat.isNotEmpty()
        val hasTimes: Boolean get() = times.size == lat.size && lat.isNotEmpty()
    }

    /** A stay of the day route: time spent in one place, named by the box when it can. */
    data class RouteStay(val name: String, val kind: String, val lat: Double, val lon: Double, val from: Long, val to: Long, val fixes: Int, val photos: Int)

    /** A move of the day route: "walk" along the streets where the box has them (routed hops), or
     *  "ride" as straight lines between the fixes. [lat]/[lon] is the path to draw. */
    data class RouteMove(val mode: String, val from: Long, val to: Long, val meters: Double, val chordM: Double, val routed: Int, val hops: Int,
                         val lat: DoubleArray, val lon: DoubleArray, val photos: Int, val kmh: Double)

    /** The day told as stays and moves (/v1/geo/route; internal/dayroute on the box). */
    data class DayRoute(val day: String, val stays: List<RouteStay>, val moves: List<RouteMove>, val walkM: Double, val rideM: Double,
                        val fixes: Int, val photos: Int, val steps: Double, val note: String, val line: String)

    /** One day's route; null when the box has not told that day (404) or predates the route. */
    suspend fun dayRoute(ctx: Context, day: String): DayRoute? = try {
        val r = BoxHttp.getJson(ctx, "/v1/geo/route?d=$day")
        if (!r.has("stays") && !r.has("moves")) null
        else {
            val st = r.optJSONArray("stays") ?: org.json.JSONArray()
            val stays = (0 until st.length()).mapNotNull { i ->
                val o = st.optJSONObject(i) ?: return@mapNotNull null
                RouteStay(o.optString("name", ""), o.optString("kind", ""), o.optDouble("lat"), o.optDouble("lon"),
                    o.optLong("from"), o.optLong("to"), o.optInt("fixes"), o.optInt("photos"))
            }
            val mv = r.optJSONArray("moves") ?: org.json.JSONArray()
            val moves = (0 until mv.length()).mapNotNull { i ->
                val o = mv.optJSONObject(i) ?: return@mapNotNull null
                val p = o.optJSONArray("path") ?: org.json.JSONArray()
                val lat = DoubleArray(p.length()); val lon = DoubleArray(p.length())
                for (j in 0 until p.length()) {
                    val q = p.optJSONArray(j) ?: return@mapNotNull null
                    lat[j] = q.optDouble(0); lon[j] = q.optDouble(1)
                }
                RouteMove(o.optString("mode", "walk"), o.optLong("from"), o.optLong("to"), o.optDouble("meters", 0.0), o.optDouble("chordM", 0.0),
                    o.optInt("routed"), o.optInt("hops"), lat, lon, o.optInt("photos"), o.optDouble("kmh", 0.0))
            }
            DayRoute(r.optString("day", day), stays, moves, r.optDouble("walkM", 0.0), r.optDouble("rideM", 0.0),
                r.optInt("fixes"), r.optInt("photos"), r.optDouble("steps", 0.0), r.optString("note", ""), r.optString("line", ""))
        }
    } catch (_: Exception) { null }

    /** The newest [limit] day tracks with their times and distances; null on a box that predates
     *  /v1/geo/tracks (the map then walks the per-day feed as before). */
    suspend fun geoDayTracks(ctx: Context, limit: Int = 60): List<DayTrack>? = try {
        val r = BoxHttp.getJson(ctx, "/v1/geo/tracks?limit=$limit")
        if (!r.has("tracks")) null
        else {
            val a = r.optJSONArray("tracks") ?: org.json.JSONArray()
            (0 until a.length()).mapNotNull { i ->
                val o = a.optJSONObject(i) ?: return@mapNotNull null
                val c = o.optJSONArray("coords") ?: return@mapNotNull null
                val lat = DoubleArray(c.length()); val lon = DoubleArray(c.length())
                for (j in 0 until c.length()) {
                    val p = c.optJSONArray(j) ?: return@mapNotNull null
                    lat[j] = p.optDouble(0); lon[j] = p.optDouble(1)
                }
                val t = o.optJSONArray("times")
                val times = if (t != null && t.length() == c.length()) LongArray(t.length()) { t.optLong(it) } else LongArray(0)
                DayTrack(o.optString("day", ""), lat, lon, times, o.optDouble("distanceM", 0.0).let { if (it.isNaN()) 0.0 else it }, o.optInt("glitches", 0),
                    o.optString("line", ""), o.optDouble("walkM", 0.0), o.optDouble("rideM", 0.0), o.optInt("stays", 0),
                    TrailQuestion.listFrom(o.optJSONArray("questions")),
                    o.optJSONArray("alts")?.takeIf { it.length() == c.length() }?.let { al ->
                        IntArray(al.length()) { j -> if (al.isNull(j)) Int.MIN_VALUE else al.optInt(j) }
                    } ?: IntArray(0),
                    o.optDouble("climbM", 0.0), o.optDouble("descentM", 0.0), o.optDouble("highM", 0.0), o.optDouble("lowM", 0.0))
            }
        }
    } catch (_: Exception) { null }

    /** The answer to a trail question (POST /v1/geo/trail/answer): [keep] true remembers the yes,
     *  false deletes the points on the box. The number deleted, or null when the box did not take it. */
    suspend fun trailAnswer(ctx: Context, q: TrailQuestion, keep: Boolean): Int? = try {
        val ts = org.json.JSONArray().apply { q.ts.forEach { put(it) } }
        val r = BoxHttp.postJson(ctx, "/v1/geo/trail/answer", org.json.JSONObject()
            .put("from", q.from).put("to", q.to).put("ts", ts).put("keep", keep), readTimeoutMs = 45_000)
        if (r.optBoolean("ok", false)) r.optInt("deleted", 0) else null
    } catch (e: kotlinx.coroutines.CancellationException) {
        throw e
    } catch (e: Exception) {
        null
    }

    /** The fixes a delete of the fix at [ts] takes (POST /v1/geo/trail/forget): it and its
     *  neighbours in time at the same spot (framed/forget.go). [dry] only asks which. */
    class Forgotten(val ts: LongArray, val deleted: Int)

    suspend fun trailForget(ctx: Context, ts: Long, dry: Boolean): Forgotten? = try {
        val r = BoxHttp.postJson(ctx, "/v1/geo/trail/forget", org.json.JSONObject()
            .put("ts", ts).put("dry", dry), readTimeoutMs = 45_000)
        if (r.optBoolean("ok", false)) {
            val a = r.optJSONArray("ts")
            Forgotten(LongArray(a?.length() ?: 0) { a!!.optLong(it) }, r.optInt("deleted", 0))
        } else null
    } catch (e: kotlinx.coroutines.CancellationException) {
        throw e
    } catch (e: Exception) {
        null
    }

    /** One day's track as ordered lat/lon pairs, pulled from framed's GeoJSON LineStrings. */
    suspend fun geoDayTrack(ctx: Context, day: String): List<Pair<Double, Double>>? = try {
        val gj = BoxHttp.getJson(ctx, "/v1/geo/day?d=$day")
        val out = ArrayList<Pair<Double, Double>>()
        val feats = gj.optJSONArray("features") ?: return emptyList()
        for (i in 0 until feats.length()) {
            val geom = feats.optJSONObject(i)?.optJSONObject("geometry") ?: continue
            if (geom.optString("type") != "LineString") continue
            val coords = geom.optJSONArray("coordinates") ?: continue
            for (j in 0 until coords.length()) {
                val pt = coords.optJSONArray(j) ?: continue
                out.add(Pair(pt.optDouble(1), pt.optDouble(0))) // GeoJSON is lon,lat
            }
        }
        out
    } catch (_: Exception) { null }

    /** The operator-provided Natural Earth GeoJSON, or null (map draws graticule-only, by design). */
    /** The world map, CACHED , only the landmass (never photos, never locations): stored once in
     *  filesDir, revalidated by ETag each open. Box unreachable = cached world still draws ,
     *  the map works offline; a 304 costs zero bytes; a new file on the box replaces the cache. */
    suspend fun worldGeoJson(ctx: Context): org.json.JSONObject? = try {
        val cache = java.io.File(ctx.filesDir, "world.geojson")
        val prefs = ctx.getSharedPreferences("ghost_geo", Context.MODE_PRIVATE)
        val (fresh, newTag) = BoxHttp.getBytesEtag(ctx, "/v1/geo/world", prefs.getString("world_etag", null))
        if (fresh != null) {
            cache.writeBytes(fresh)
            prefs.edit().putString("world_etag", newTag ?: "").apply()
        }
        if (cache.exists()) org.json.JSONObject(cache.readText()) else null
    } catch (e: Exception) {
        val cache = java.io.File(ctx.filesDir, "world.geojson")
        if (cache.exists()) runCatching { org.json.JSONObject(cache.readText()) }.getOrNull() else null
    }

    private suspend fun worldGeoJsonDirect(ctx: Context): org.json.JSONObject? = try {
        BoxHttp.getJson(ctx, "/v1/geo/world")
    } catch (_: Exception) { null }

    /** One landmass cut on the box: res is "" for world.geojson, else the token in world-<res>.geojson. */
    data class WorldCut(val res: String, val bytes: Long, val etag: String)

    /** The landmass cuts the box holds, smallest first (the index endpoint sorts). Null when the box
     *  did not answer (offline, or a box that predates the index); an empty list means the box
     *  answered and has no world file. */
    suspend fun worldIndex(ctx: Context): List<WorldCut>? = try {
        val r = BoxHttp.getJson(ctx, "/v1/geo/world/index")
        if (!r.has("cuts")) null
        else {
            // kept, so the next open knows the cuts before the box answers (worldIndexOnPhone)
            ctx.getSharedPreferences("ghost_geo", Context.MODE_PRIVATE).edit().putString("world_index", r.toString()).apply()
            parseCuts(r)
        }
    } catch (_: Exception) { null }

    private fun parseCuts(r: org.json.JSONObject): List<WorldCut> {
        val a = r.optJSONArray("cuts") ?: org.json.JSONArray()
        return (0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            WorldCut(o.optString("res", ""), o.optLong("bytes"), o.optString("etag", ""))
        }
    }

    // --- THE MAP FROM THE PHONE'S DISK, no network: what the map draws the moment it opens, before
    // the box is asked whether anything changed (the network versions below revalidate) ---

    /** The world cuts the box listed last time, or null. */
    fun worldIndexOnPhone(ctx: Context): List<WorldCut>? = runCatching {
        val s = ctx.getSharedPreferences("ghost_geo", Context.MODE_PRIVATE).getString("world_index", null) ?: return null
        parseCuts(org.json.JSONObject(s))
    }.getOrNull()

    /** A world cut already on the phone, with the ETag it came under; (null, "") when there is none. */
    fun worldGeoJsonOnPhone(ctx: Context, res: String = ""): Pair<java.io.File?, String> {
        val key = if (res.isEmpty()) "default" else res
        val cache = java.io.File(ctx.filesDir, "world-$key.geojson")
        val tag = ctx.getSharedPreferences("ghost_geo", Context.MODE_PRIVATE).getString("world_etag_$key", "") ?: ""
        return Pair(if (cache.exists()) cache else null, tag)
    }

    /** The coast index on the phone, or null. */
    fun landTileIndexOnPhone(ctx: Context): ByteArray? {
        val f = java.io.File(java.io.File(ctx.filesDir, "landtiles"), "index.bin")
        return if (f.exists()) com.localghost.app.ui.LandTileGeom.index(runCatching { f.readBytes() }.getOrNull()) else null
    }

    /** The road index on the phone (raw, as [roadTileIndex] returns it), or null. */
    fun roadTileIndexOnPhone(ctx: Context): ByteArray? {
        val f = java.io.File(java.io.File(ctx.filesDir, "roadtiles"), "index.bin")
        return if (f.exists()) runCatching { f.readBytes() }.getOrNull() else null
    }

    /** One world cut ON DISK plus the ETag it was fetched under, revalidated against the box and
     *  never parsed here. The map reads it with its own byte scanner (org.json on a 24MB GeoJSON is
     *  a second of main-thread freeze and ~100MB of boxed Doubles), and caches the projected rings
     *  keyed on this ETag so the JSON is walked once per world file, not once per open. Box
     *  unreachable = whatever is cached, tag included; nothing cached = (null, "") and the map
     *  draws graticule + dots, by design. res "" is the plain world.geojson every box has had. */
    suspend fun worldGeoJsonFile(ctx: Context, res: String = ""): Pair<java.io.File?, String> {
        val key = if (res.isEmpty()) "default" else res
        val cache = java.io.File(ctx.filesDir, "world-$key.geojson")
        val prefs = ctx.getSharedPreferences("ghost_geo", Context.MODE_PRIVATE)
        val tagKey = "world_etag_$key"
        try {
            val path = if (res.isEmpty()) "/v1/geo/world" else "/v1/geo/world?res=$res"
            val (fresh, newTag) = BoxHttp.getBytesEtag(ctx, path, prefs.getString(tagKey, null))
            if (fresh != null) {
                cache.writeBytes(fresh)
                prefs.edit().putString(tagKey, newTag ?: "").apply()
            }
        } catch (_: Exception) { /* offline: the cached world still draws */ }
        return Pair(if (cache.exists()) cache else null, prefs.getString(tagKey, "") ?: "")
    }

    /** THE HIGH-RESOLUTION COAST's index: 64,800 one-degree cells, 0 water / 1 coast / 2 land,
     *  ETag-revalidated and cached on the phone; null when the box has no tiles (the map keeps its
     *  10m base). A new index means a new cut: the cached tiles are dropped with the old one. */
    suspend fun landTileIndex(ctx: Context): ByteArray? {
        val dir = java.io.File(ctx.filesDir, "landtiles").apply { mkdirs() }
        val cache = java.io.File(dir, "index.bin")
        val prefs = ctx.getSharedPreferences("ghost_geo", Context.MODE_PRIVATE)
        val old = prefs.getString("landtiles_etag", null)
        try {
            val (fresh, tag) = BoxHttp.getBytesEtag(ctx, "/v1/geo/landtiles/index", if (cache.exists()) old else null)
            if (fresh != null && com.localghost.app.ui.LandTileGeom.index(fresh) != null) {
                if (tag != old) dir.listFiles()?.forEach { if (it.name.endsWith(".lgt")) it.delete() }
                cache.writeBytes(fresh)
                prefs.edit().putString("landtiles_etag", tag ?: "").apply()
            } else if (fresh != null) {
                android.util.Log.w("LocalGhost", "land tile index: ${fresh.size} bytes, not an index (want 64804 behind LGI1)")
            } else if (tag == null) {
                android.util.Log.i("LocalGhost", "land tile index: none from the box (204 = no tiles cut yet)")
            }
        } catch (_: Exception) { /* offline: the cached index still draws */ }
        return if (cache.exists()) com.localghost.app.ui.LandTileGeom.index(runCatching { cache.readBytes() }.getOrNull()) else null
    }

    /** One coast tile, from the phone's disk when it has it, else from the box (then kept). The
     *  cache is trimmed to ~200 MB, oldest first , the places you look at stay. */
    suspend fun landTile(ctx: Context, x: Int, y: Int): ByteArray? {
        val dir = java.io.File(ctx.filesDir, "landtiles").apply { mkdirs() }
        val f = java.io.File(dir, "%03d_%03d.lgt".format(java.util.Locale.US, x, y))
        if (f.exists()) return runCatching { f.setLastModified(System.currentTimeMillis()); f.readBytes() }.getOrNull()
        val b = BoxHttp.getBytes(ctx, "/v1/geo/landtile?x=$x&y=$y") ?: return null
        runCatching {
            f.writeBytes(b)
            val tiles = dir.listFiles { g -> g.name.endsWith(".lgt") } ?: emptyArray()
            var total = tiles.sumOf { it.length() }
            // ~200 MB, oldest first; the size picked in SETTINGS when maps are downloaded ahead
            val cap = com.localghost.app.local.MapPrefetch.capBytes(ctx, 200L * 1024 * 1024)
            if (total > cap) for (g in tiles.sortedBy { it.lastModified() }) {
                if (total <= cap * 3 / 4) break
                total -= g.length(); g.delete()
            }
        }
        return b
    }

    /** THE ROADS' index: which one-degree cells have a major-road tile and which tenth-of-a-degree
     *  cells a street tile; ETag-revalidated, cached; null when the box has no road tiles. */
    suspend fun roadTileIndex(ctx: Context): ByteArray? {
        val dir = java.io.File(ctx.filesDir, "roadtiles").apply { mkdirs() }
        val cache = java.io.File(dir, "index.bin")
        val prefs = ctx.getSharedPreferences("ghost_geo", Context.MODE_PRIVATE)
        val old = prefs.getString("roadtiles_etag", null)
        try {
            val (fresh, tag) = BoxHttp.getBytesEtag(ctx, "/v1/geo/roadtiles/index", if (cache.exists()) old else null)
            if (fresh != null && com.localghost.app.ui.RoadTileGeom.index(fresh) != null) {
                if (tag != old) dir.listFiles()?.forEach { if (it.name.endsWith(".lgr")) it.delete() }
                cache.writeBytes(fresh)
                prefs.edit().putString("roadtiles_etag", tag ?: "").apply()
            } else if (fresh != null) {
                android.util.Log.w("LocalGhost", "road tile index: ${fresh.size} bytes, not an index")
            } else if (tag == null) {
                android.util.Log.i("LocalGhost", "road tile index: none from the box (204 = no road tiles cut yet)")
            }
        } catch (_: Exception) { }
        return if (cache.exists()) runCatching { cache.readBytes() }.getOrNull() else null
    }

    /** One road tile (level 1 = a one-degree cell of major roads, 0 = a tenth-of-a-degree cell of
     *  every road), from disk when the phone has it, else from the box; ~400 MB kept. */
    suspend fun roadTile(ctx: Context, level: Int, x: Int, y: Int): ByteArray? {
        val dir = java.io.File(ctx.filesDir, "roadtiles").apply { mkdirs() }
        val f = java.io.File(dir, "%d_%04d_%04d.lgr".format(java.util.Locale.US, level, x, y))
        if (f.exists()) return runCatching { f.setLastModified(System.currentTimeMillis()); f.readBytes() }.getOrNull()
        val b = BoxHttp.getBytes(ctx, "/v1/geo/roadtile?l=$level&x=$x&y=$y") ?: return null
        runCatching {
            f.writeBytes(b)
            val tiles = dir.listFiles { g -> g.name.endsWith(".lgr") } ?: emptyArray()
            var total = tiles.sumOf { it.length() }
            val cap = com.localghost.app.local.MapPrefetch.capBytes(ctx, 400L * 1024 * 1024)
            if (total > cap) for (g in tiles.sortedBy { it.lastModified() }) {
                if (total <= cap * 3 / 4) break
                total -= g.length(); g.delete()
            }
        }
        return b
    }

    /** Rename a persisted chat , the person's title outranks the derived one, permanently. */
    suspend fun renameChat(ctx: Context, id: Long, title: String): Boolean = try {
        BoxHttp.postJson(ctx, "/v1/chats/rename", org.json.JSONObject().put("id", id).put("title", title))
        true
    } catch (e: Exception) { android.util.Log.w("LocalGhost", "rename chat: ${e.message}"); false }

    /** Delete a persisted chat and its messages , real deletion on the box, rows gone. */
    suspend fun deleteChat(ctx: Context, id: Long): Boolean = try {
        BoxHttp.postJson(ctx, "/v1/chats/delete", org.json.JSONObject().put("id", id))
        true
    } catch (e: Exception) { android.util.Log.w("LocalGhost", "delete chat: ${e.message}"); false }

    suspend fun boxChatMessages(ctx: Context, chatId: Long, beforeId: Long = 0L, limit: Int = 100): List<BoxChatMsg>? = try {
        var path = "/v1/chats/messages?id=$chatId&limit=$limit"
        if (beforeId > 0) path += "&before=$beforeId"
        val r = BoxHttp.getJson(ctx, path)
        val a = r.optJSONArray("messages") ?: return emptyList()
        (0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            BoxChatMsg(o.optLong("id"), o.optString("role"), o.optString("content"),
                reasoning = o.optString("reasoning"), sources = parseSources(o.optJSONArray("sources")),
                state = o.optString("state"))
        }
    } catch (e: Exception) {
        android.util.Log.w("LocalGhost", "chat load failed: ${e.message}"); null
    }

    /** One tracked target's ring buffers: 10s samples (~100 min), minute samples (24h), day blob.
     *  Series come newest-first as stored; the charts reverse for left-to-right time. */
    data class StatPoint(val t: Long, val c: Int, val v: Double)
    data class ServiceStats(val s10: List<StatPoint>, val s1m: List<StatPoint>, val day: String)
    suspend fun serviceStats(ctx: Context, name: String): ServiceStats? = try {
        val r = BoxHttp.getJson(ctx, "/v1/services/detail?name=" + java.net.URLEncoder.encode(name, "UTF-8"))
        fun arr(key: String): List<StatPoint> {
            val a = r.optJSONArray(key) ?: return emptyList()
            return (0 until a.length()).mapNotNull { i ->
                val o = a.optJSONObject(i) ?: return@mapNotNull null
                StatPoint(o.optLong("t"), o.optInt("c"), o.optDouble("v", 0.0))
            }
        }
        val day = r.optJSONObject("day")?.let { d ->
            buildString {
                val up = d.optDouble("uptimePct", -1.0)
                if (up >= 0) append("uptime %.1f%%".format(up))
                val avg = d.optDouble("avgV", Double.NaN)
                if (!avg.isNaN()) append("  ·  avg %.2f".format(avg))
                val worst = d.optString("worstDetail", "")
                if (worst.isNotBlank()) append("\nworst: $worst")
            }
        } ?: "(24h averages compute after 30 min of samples)"
        ServiceStats(arr("s10"), arr("s1m"), day)
    } catch (e: Exception) {
        android.util.Log.w("LocalGhost", "service stats failed: ${e.message}"); null
    }

    /** A user tag correction. Removes become TOMBSTONES on the box , the model can never re-propose
     *  a tag a human rejected. True on success. */
    suspend fun frameTag(ctx: Context, hash: String, tag: String, add: Boolean): Boolean = try {
        BoxHttp.postJson(ctx, "/v1/frames/tag",
            org.json.JSONObject().put("hash", hash).put("tag", tag).put("action", if (add) "add" else "remove"))
        true
    } catch (e: Exception) {
        android.util.Log.w("LocalGhost", "tag ${if (add) "add" else "remove"} failed: ${e.message}")
        false
    }

    /** One thumbnail's bytes (webp or jpeg). Null when the frame has none (videos) or on failure. */
    /** Stream the untouched original to a FILE , the video path (any size, bounded RAM). */
    suspend fun frameOriginalToFile(ctx: Context, hash: String, dest: java.io.File,
                                    onBytes: ((Long) -> Unit)? = null): Boolean =
        BoxHttp.getToFile(ctx, "/v1/frames/original?hash=$hash", dest, onBytes)

    /** The UNTOUCHED archived original , full quality, mime-typed by the box. Multi-MB (images ,
     *  videos use frameOriginalToFile; this path is RAM-capped at 64MB). */
    suspend fun frameOriginal(ctx: Context, hash: String): ByteArray? =
        BoxHttp.getBytes(ctx, "/v1/frames/original?hash=$hash")

    /** The big derived JPEG , full-screen viewing and pinch-zoom. */
    suspend fun framePreview(ctx: Context, hash: String): ByteArray? =
        BoxHttp.getBytes(ctx, "/v1/frames/preview?hash=$hash")

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
    suspend fun nextCameraCommand(ctx: Context, kind: MediaKind): Command {
        // The cursor comes FROM THE BOX , the phone persists nothing. One authority, no split brain.
        // No cursor = no run (Idle): the next run asks again. Never "from the beginning" on a guess.
        val c = getCursor(ctx, kind) ?: return Command.Idle
        return Command.SyncCamera(kind, c)
    }

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

    /** Tell the box what this phone is , model is volunteered (Build.MODEL, a model string, not
     *  a serial or any hardware identifier), name is the person's choice. */
    suspend fun setDeviceName(ctx: Context, name: String, model: String,
                              stableId: String = "", device: String = ""): Boolean = try {
        val body = org.json.JSONObject().put("name", name).put("model", model)
        if (stableId.isNotEmpty()) body.put("stableId", stableId)
        if (device.isNotEmpty()) body.put("device", device)
        BoxHttp.postJson(ctx, "/v1/devices/name", body).optBoolean("ok", false)
    } catch (_: Exception) { false }

    /** The continuity key , a hash of the app-scoped Android ID. It survives uninstall,
     *  reinstall and every debug rebuild (same signing key), where a client certificate does
     *  not; and because Android scopes this value per signing key, it identifies the phone TO
     *  THIS APP ONLY and cannot correlate the person anywhere else. Hashed before it leaves the
     *  phone: the box stores a digest, never the platform value. */
    @android.annotation.SuppressLint("HardwareIds")
    fun stableId(ctx: Context): String = try {
        val raw = android.provider.Settings.Secure.getString(
            ctx.contentResolver, android.provider.Settings.Secure.ANDROID_ID) ?: ""
        if (raw.isEmpty()) "" else java.security.MessageDigest.getInstance("SHA-256")
            .digest(("localghost:" + raw).toByteArray())
            .joinToString("") { "%02x".format(it) }.take(32)
    } catch (_: Exception) { "" }

    /** The enrolled phones, from the box's cursor rows , REAL now: this screen used to display
     *  two invented devices with invented counts, which is exactly the kind of confident fiction
     *  the rest of the system refuses to print. */
    suspend fun devices(ctx: Context): List<DeviceInfo> {
        // getJson answers an EMPTY object when the box is unreachable (the appears-down shield),
        // so there is nothing to elvis , an absent "devices" array is the same "nothing to show".
        val a = BoxHttp.getJson(ctx, "/v1/devices").optJSONArray("devices") ?: return emptyList()
        return (0 until a.length()).mapNotNull { i ->
            val o = a.optJSONObject(i) ?: return@mapNotNull null
            val key = o.optString("device", "")
            DeviceInfo(
                id = key,
                name = o.optString("name", "").ifEmpty {
                    o.optString("model", "").ifEmpty {
                        if (o.optBoolean("thisDevice")) "this phone" else "phone " + key.take(6)
                    }
                },
                thisDevice = o.optBoolean("thisDevice"),
                lastSync = "",
                photos = 0, videos = 0,
                lastSyncTs = o.optLong("updatedAt"),
                lastPhotoTs = o.optLong("photoTs"),
                lastVideoTs = o.optLong("videoTs"),
                model = o.optString("model", ""),
                frames = o.optLong("frames"),
            )
        }
    }

    // --- settings (box-owned, persona-scoped; phone caches for offline) ---


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
                            // optString's fallback is typed String , "" IS the absent value here,
                            // and the field is only ever compared, never parsed.
                            sha256 = o.optString("sha256", ""),
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

    /**
     * Destroy the persona's wrapping key on the box (crypto-erase) and clear local state.
     * STUB: returns true. Real: authenticated wipe command; the box destroys the key slot.
     */

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
