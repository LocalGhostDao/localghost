package com.localghost.app.local

import android.content.Context
import java.io.File

/**
 * Models live in the app's private storage: filesDir/models/<id>.gguf. Never bundled, never
 * leaves the device. One model is "active" (what LocalModel loads).
 */
object ModelStore {
    private fun dir(ctx: Context): File = File(ctx.filesDir, "models").apply { mkdirs() }

    fun file(ctx: Context, id: String): File = File(dir(ctx), "$id.gguf")
    fun partFile(ctx: Context, id: String): File = File(dir(ctx), "$id.gguf.part")

    fun isPresent(ctx: Context, id: String): Boolean = file(ctx, id).let { it.exists() && it.length() > 0 }

    fun installed(ctx: Context): List<String> =
        dir(ctx).listFiles { f -> f.extension == "gguf" }?.map { it.nameWithoutExtension } ?: emptyList()

    fun delete(ctx: Context, id: String) { file(ctx, id).delete(); partFile(ctx, id).delete() }

    // active model id, persisted in prefs
    private fun prefs(ctx: Context) = ctx.getSharedPreferences("lg_models", Context.MODE_PRIVATE)
    fun activeId(ctx: Context): String? = prefs(ctx).getString("active", null)
    fun setActive(ctx: Context, id: String?) = prefs(ctx).edit().putString("active", id).apply()

    /** Path the active model resolves to, or null if none installed. */
    fun activeFile(ctx: Context): File? {
        val id = activeId(ctx) ?: installed(ctx).firstOrNull() ?: return null
        val f = file(ctx, id)
        return if (f.exists()) f else null
    }
}
