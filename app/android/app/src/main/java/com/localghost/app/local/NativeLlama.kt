package com.localghost.app.local

/**
 * Raw JNI binding to llama.cpp. One-to-one with llama_jni.cpp. Not used directly by the app —
 * LocalModel wraps it with coroutines/Flow and lifecycle.
 */
internal class NativeLlama {
    /** Token callback. Return false to cancel generation (STOP). */
    fun interface TokenCallback { fun onToken(piece: String): Boolean }

    external fun nativeLoad(modelPath: String, nCtx: Int, nThreads: Int): Long
    external fun nativeFree(handle: Long)
    external fun nativeGenerate(handle: Long, prompt: String, maxTokens: Int, callback: TokenCallback)

    companion object {
        @Volatile private var loaded = false
        /** Loads liblocalghost_llm.so. Throws UnsatisfiedLinkError if the native build is absent. */
        fun ensureLibrary(): Boolean = try {
            if (!loaded) { System.loadLibrary("localghost_llm"); loaded = true }
            true
        } catch (e: Throwable) { false }
    }
}
