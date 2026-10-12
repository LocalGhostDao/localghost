package com.localghost.app.sync

import android.content.Context
import android.net.Uri
import android.provider.DocumentsContract
import com.localghost.app.net.BoxClient
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import java.io.File
import java.util.zip.ZipEntry
import java.util.zip.ZipOutputStream

/**
 * THE SAMSUNG HEALTH DOWNLOAD, sent to the box. Samsung Health › Settings › Download personal
 * data writes a folder under Download/Samsung Health with one CSV per table (and a jsons folder
 * the box does not need). The person picks that folder; the phone zips its CSVs into a file in
 * its cache, sends it to the box, and the box scans it (every table, its rows and span, what
 * the import takes) before anything is written. Then the import runs on the box and this
 * follows it. The watch's years reach the box without Health Connect ever holding them.
 */
object HealthImport {
    data class Zipped(val file: File, val csvs: Int, val jsons: Int, val bytes: Long)

    /** Walks the picked tree for the tables (.csv) and their binning files (the .json files under jsons, the
     *  heart rate's per-minute readings) and zips them with their paths; null without a table.
     *  [onProgress] is told the count now and then: a download has thousands of small files. */
    suspend fun zipTree(ctx: Context, tree: Uri, onProgress: (Int) -> Unit = {}): Zipped? = withContext(Dispatchers.IO) {
        val out = File(ctx.cacheDir, "samsung-health-${System.currentTimeMillis()}.zip")
        var csvs = 0
        var jsons = 0
        var bytes = 0L
        ZipOutputStream(out.outputStream().buffered(1 shl 16)).use { zos ->
            fun walk(docId: String, depth: Int, prefix: String) {
                if (depth > 4) return
                val children = DocumentsContract.buildChildDocumentsUriUsingTree(tree, docId)
                val cur = ctx.contentResolver.query(children, arrayOf(
                    DocumentsContract.Document.COLUMN_DOCUMENT_ID, DocumentsContract.Document.COLUMN_DISPLAY_NAME,
                    DocumentsContract.Document.COLUMN_MIME_TYPE), null, null, null) ?: return
                cur.use { c ->
                    while (c.moveToNext()) {
                        val id = c.getString(0); val name = c.getString(1) ?: continue; val mime = c.getString(2) ?: ""
                        if (mime == DocumentsContract.Document.MIME_TYPE_DIR) { walk(id, depth + 1, "$prefix$name/"); continue }
                        val lower = name.lowercase()
                        val isCsv = lower.endsWith(".csv")
                        val isJson = lower.endsWith(".json")
                        if (!isCsv && !isJson) continue
                        val uri = DocumentsContract.buildDocumentUriUsingTree(tree, id)
                        val ins = try { ctx.contentResolver.openInputStream(uri) } catch (_: Exception) { null } ?: continue
                        zos.putNextEntry(ZipEntry(prefix + name))
                        ins.use { bytes += it.copyTo(zos, 1 shl 16) }
                        zos.closeEntry()
                        if (isCsv) csvs++ else jsons++
                        if ((csvs + jsons) % 50 == 0) onProgress(csvs + jsons)
                    }
                }
            }
            walk(DocumentsContract.getTreeDocumentId(tree), 0, "")
        }
        if (csvs == 0) { out.delete(); null } else Zipped(out, csvs, jsons, bytes)
    }

    // --- THE WATCHED FOLDER: a new download is imported on its own ---
    //
    // Samsung's only door to everything the watch records is the download, so the loop that
    // keeps the box current is "download now and then, and the app does the rest": the person
    // keeps the folder's permission once (the picker's tree, persisted), the six-hourly worker
    // looks for files newer than the last import, and a new download is zipped, sent and
    // imported without a tap. No library of anyone's; the phone reads its own storage.

    private const val PREFS = "lg_health"
    private const val KEY_TREE = "import_tree"
    private const val KEY_SEEN = "import_seen"   // the fingerprint of the download last imported
    private const val KEY_LAST = "import_last"   // when the watcher last imported (unix seconds)
    private const val KEY_NOTE = "import_note"   // the watcher's last word, for SETTINGS

    /** Keeps the folder for the watcher; false when the permission could not be kept. */
    fun watch(ctx: Context, tree: Uri): Boolean {
        try {
            ctx.contentResolver.takePersistableUriPermission(tree, android.content.Intent.FLAG_GRANT_READ_URI_PERMISSION)
        } catch (_: Exception) { return false }
        ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putString(KEY_TREE, tree.toString()).apply()
        return true
    }

    fun unwatch(ctx: Context) {
        val p = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        p.getString(KEY_TREE, null)?.let { t ->
            try { ctx.contentResolver.releasePersistableUriPermission(Uri.parse(t), android.content.Intent.FLAG_GRANT_READ_URI_PERMISSION) } catch (_: Exception) {}
        }
        p.edit().remove(KEY_TREE).remove(KEY_SEEN).remove(KEY_NOTE).apply()
    }

    data class Watch(val tree: Uri, val seen: String, val lastAt: Long, val note: String)

    fun watching(ctx: Context): Watch? {
        val p = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)
        val t = p.getString(KEY_TREE, null) ?: return null
        return Watch(Uri.parse(t), p.getString(KEY_SEEN, "") ?: "", p.getLong(KEY_LAST, 0L), p.getString(KEY_NOTE, "") ?: "")
    }

    /** Marks the download now in the folder as imported, so the watcher waits for the next. */
    fun markImported(ctx: Context, tree: Uri) {
        val fp = runCatching { fingerprint(ctx, tree) }.getOrDefault("")
        ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putString(KEY_SEEN, fp).putLong(KEY_LAST, System.currentTimeMillis() / 1000).apply()
    }

    /** The folder's download as a short string: the newest table's modified time, the count of
     *  tables, the newest's name. A new download changes it; the same one does not. */
    fun fingerprint(ctx: Context, tree: Uri): String {
        var newest = 0L
        var newestName = ""
        var n = 0
        fun walk(docId: String, depth: Int) {
            if (depth > 4) return
            val children = DocumentsContract.buildChildDocumentsUriUsingTree(tree, docId)
            val cur = ctx.contentResolver.query(children, arrayOf(
                DocumentsContract.Document.COLUMN_DOCUMENT_ID, DocumentsContract.Document.COLUMN_DISPLAY_NAME,
                DocumentsContract.Document.COLUMN_MIME_TYPE, DocumentsContract.Document.COLUMN_LAST_MODIFIED), null, null, null) ?: return
            cur.use { c ->
                while (c.moveToNext()) {
                    val id = c.getString(0); val name = c.getString(1) ?: continue; val mime = c.getString(2) ?: ""
                    if (mime == DocumentsContract.Document.MIME_TYPE_DIR) { if (!name.equals("jsons", true)) walk(id, depth + 1); continue }
                    if (!name.lowercase().endsWith(".csv")) continue
                    n++
                    val m = if (c.isNull(3)) 0L else c.getLong(3)
                    if (m > newest || (m == newest && name > newestName)) { newest = m; newestName = name }
                }
            }
        }
        walk(DocumentsContract.getTreeDocumentId(tree), 0)
        return "$newest|$n|$newestName"
    }

    /** The worker's look: a download newer than the last imported is zipped, sent and imported.
     *  Returns a line for the log, "" when there was nothing to do. */
    suspend fun autoImport(ctx: Context): String {
        val w = watching(ctx) ?: return ""
        val fp = try { fingerprint(ctx, w.tree) } catch (e: Exception) { note(ctx, "the folder could not be read (${e.message?.take(40)})"); return "" }
        if (fp.isEmpty() || fp == w.seen || fp.startsWith("0|0|")) return ""
        val z = zipTree(ctx, w.tree) ?: return ""
        val (sc, why) = send(ctx, z)
        if (sc == null) { note(ctx, "a new download was found but the box did not take it: $why"); return why }
        val (_, startWhy) = BoxClient.healthImportStart(ctx, sc.file)
        if (startWhy.isNotEmpty()) { note(ctx, "sent, not started: $startWhy"); return startWhy }
        markImported(ctx, w.tree)
        note(ctx, "a new download imported on ${java.text.SimpleDateFormat("yyyy-MM-dd HH:mm", java.util.Locale.UK).format(java.util.Date())} (${sc.tables.size} tables, ${sc.days} days)")
        return "imported ${sc.file}"
    }

    private fun note(ctx: Context, s: String) {
        ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit().putString(KEY_NOTE, s).apply()
    }

    /** Sends the zip and returns the box's scan, or the reason it could not. */
    suspend fun send(ctx: Context, z: Zipped): Pair<HealthImportText.Scan?, String> {
        val r = try { BoxClient.healthImportUpload(ctx, z.file) } catch (e: Exception) { return null to (e.message ?: "the box did not answer") }
        z.file.delete()
        if (r == null) return null to "the box did not answer"
        val file = r.optString("file")
        val sc = r.optJSONObject("scan") ?: return null to (r.optString("why").ifBlank { "spooled as $file, no scan came back" })
        val ta = sc.optJSONArray("tables")
        val tables = (0 until (ta?.length() ?: 0)).mapNotNull { i -> ta?.optJSONObject(i)?.let { t ->
            HealthImportText.Table(t.optString("name"), t.optInt("rows"), t.optString("from"), t.optString("to"), t.optString("read"), t.optString("err")) } }
        return HealthImportText.Scan(file, tables, sc.optInt("days"), sc.optString("note")) to ""
    }

}
