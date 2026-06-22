package com.localghost.app.local

import android.content.Context
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.channels.awaitClose
import kotlinx.coroutines.flow.Flow
import kotlinx.coroutines.flow.callbackFlow
import kotlinx.coroutines.flow.flowOn
import java.io.File

/**
 * On-phone fallback model. Used only when the box is unreachable (auto) or when the user forces
 * local mode (manual). It has NO access to the life-index — that lives on the box — so it answers
 * generic questions with limited context. This is the lifeboat, not the engine.
 *
 * Swappable seam, like BoxClient: this wraps llama.cpp via JNI, but the rest of the app only
 * sees LocalModel.state and LocalModel.generate().
 */
object LocalModel {

    enum class State { ABSENT, NOT_LOADED, LOADING, READY, FAILED }

    @Volatile var state: State = State.ABSENT
        private set

    private val native = NativeLlama()
    @Volatile private var handle: Long = 0L

    /** The active GGUF in app storage (downloaded, never bundled). Null if none installed. */
    fun modelFile(ctx: Context): File? = ModelStore.activeFile(ctx)

    fun isModelPresent(ctx: Context): Boolean = modelFile(ctx) != null

    /** Idempotent load. Returns true when READY. Safe to call before each generate(). */
    suspend fun ensureLoaded(ctx: Context): Boolean {
        if (state == State.READY) return true
        if (!NativeLlama.ensureLibrary()) { state = State.ABSENT; return false }
        val file = modelFile(ctx) ?: run { state = State.ABSENT; return false }
        state = State.LOADING
        val threads = Runtime.getRuntime().availableProcessors().coerceIn(2, 6)
        handle = native.nativeLoad(file.absolutePath, /*nCtx=*/2048, threads)
        state = if (handle != 0L) State.READY else State.FAILED
        return state == State.READY
    }

    /** Stream a reply token-by-token. [shouldContinue] lets the STOP button cancel.
     *  nativeGenerate blocks and calls back synchronously, so we run it inside callbackFlow
     *  and trySend each piece as it arrives. */
    fun generate(prompt: String, maxTokens: Int = 512, shouldContinue: () -> Boolean): Flow<String> =
        callbackFlow {
            val h = handle
            if (h == 0L) { close(); return@callbackFlow }
            native.nativeGenerate(h, prompt, maxTokens) { piece ->
                val ok = trySend(piece).isSuccess
                ok && shouldContinue()      // false cancels native generation
            }
            close()
            awaitClose { }
        }.flowOn(Dispatchers.Default)

    fun unload() {
        if (handle != 0L) { native.nativeFree(handle); handle = 0L }
        state = if (state == State.ABSENT) State.ABSENT else State.NOT_LOADED
    }
}
