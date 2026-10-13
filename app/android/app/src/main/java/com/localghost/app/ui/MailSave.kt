package com.localghost.app.ui

import android.content.ContentValues
import android.content.Context
import android.provider.MediaStore

/** Saving an attachment the box kept into the phone's Downloads, where the person opens it with
 *  whatever they trust. The app itself never opens a file from a mail. */
object MailSave {
    fun toDownloads(ctx: Context, name: String, mime: String, bytes: ByteArray): String {
        val safe = name.replace(Regex("[^A-Za-z0-9._ ()-]+"), "_").ifBlank { "attachment" }
        return try {
            val values = ContentValues().apply {
                put(MediaStore.Downloads.DISPLAY_NAME, safe)
                put(MediaStore.Downloads.MIME_TYPE, mime.ifBlank { "application/octet-stream" })
                put(MediaStore.Downloads.IS_PENDING, 1)
            }
            val resolver = ctx.contentResolver
            val uri = resolver.insert(MediaStore.Downloads.EXTERNAL_CONTENT_URI, values) ?: return "could not open Downloads"
            resolver.openOutputStream(uri)?.use { it.write(bytes) } ?: return "could not write the file"
            values.clear()
            values.put(MediaStore.Downloads.IS_PENDING, 0)
            resolver.update(uri, values, null, null)
            "saved to Downloads as $safe"
        } catch (e: Exception) {
            "not saved: " + (e.message ?: e.javaClass.simpleName)
        }
    }
}
