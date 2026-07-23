package com.localghost.app.sync

import android.content.Context
import com.localghost.app.net.BoxClient
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

class SyncEngine(private val ctx: Context) {

    interface Progress {
        fun onStart(kind: MediaKind, total: Int, totalBytes: Long)
        fun onItemStart(kind: MediaKind, name: String, index: Int, total: Int, size: Long)
        fun onItemBytes(kind: MediaKind, read: Long, size: Long, runBytesSent: Long, speedBps: Double, etaSeconds: Long)
        fun onItemDone(kind: MediaKind, sent: Int, total: Int)
        fun onDone(result: CommandResult)
    }

    suspend fun runCamera(kind: MediaKind, progress: Progress) {
        when (val cmd = BoxClient.nextCameraCommand(ctx, kind)) {
            is Command.SyncCamera -> exec(cmd, progress)
            else -> { progress.onStart(kind, 0, 0L); progress.onDone(CommandResult(Stream.CAMERA, kind, 0, 0)) }
        }
    }

    private suspend fun exec(cmd: Command.SyncCamera, progress: Progress) {
        // Cursor came FROM THE BOX in nextCameraCommand , the single authority, ts+id per kind.
        // The phone persists nothing; there is no local cursor to diverge, lose, or fast-forward.
        val after = cmd.after
        val (total, totalBytes) = withContext(Dispatchers.IO) { CameraReader.count(ctx, cmd.kind, after) }
        // The one line that explains a "0 items" sync: was the camera roll EMPTY after the cursor
        // (query/permission/cursor issue), or full but nothing CONFIRMED (upload issue)? Both look
        // identical on screen without this.
        android.util.Log.i("LocalGhost", "sync ${cmd.kind}: $total items ($totalBytes bytes) after cursor (${after.dateTaken},${after.id})")
        progress.onStart(cmd.kind, total, totalBytes)
        val meter = SyncMeter(totalBytes)
        var index = 0
        var curSize = 0L
        var doneBytes = 0L   // bytes from items already CONFIRMED sent this run
        var curRead = 0L
        var sawFailure = false // once an item fails, freeze the cursor so the gap is retried next run
        // The run's confirmed position , IN MEMORY ONLY. The box is the persistent cursor: reported
        // every 25 confirmations (a crash loses at most 25 items of position, re-covered by the hash
        // dedup in seconds) and once more at the end of the run.
        var confirmedTs = 0L
        var confirmedId = 0L
        var sinceReport = 0
        val confirm: (CameraReader.Item) -> Unit = { item ->
            if (!sawFailure) {
                confirmedTs = item.dateTaken; confirmedId = item.id
                if (++sinceReport >= 25) {
                    sinceReport = 0
                    kotlinx.coroutines.runBlocking { BoxClient.reportCursor(ctx, cmd.kind, confirmedTs, confirmedId) }
                }
            }
        }
        val result = withContext(Dispatchers.IO) {
            CameraReader.syncFrom(
                ctx, cmd.kind, after,
                shouldAbort = {
                    // Paused by the person, or off Wi-Fi without the mobile-sync setting , either
                    // way, between items is the clean place to stop.
                    com.localghost.app.settings.AppSettings.syncPaused(ctx) ||
                        !NetGuard.uploadsAllowed(ctx)
                },
                checkHave = { hashes ->
                    // One round trip per group of 40. Empty set on ANY failure , uncertainty uploads.
                    kotlinx.coroutines.runBlocking { BoxClient.framesHave(ctx, hashes) }
                },
                onSkipExisting = { item -> confirm(item) },
                send = { item, stream ->
                    // Mid-file enforcement: the guard re-checks the network every 2MB, so losing
                    // Wi-Fi kills the transfer within 2MB instead of letting a 2GB video finish
                    // on mobile data. The failure holds the cursor; Wi-Fi resumes it.
                    val guarded = NetGuard.GuardedInputStream(ctx, stream)
                    val ok = kotlinx.coroutines.runBlocking { BoxClient.ingest(ctx, cmd.kind, item.name, guarded, item.dateTaken) }
                    if (!ok) sawFailure = true
                    // Advance the cursor only while the run is still a CONTIGUOUS success streak. Items
                    // upload oldest-first; if #11 failed, we must not advance past it even though #12+
                    // succeed, or #11 would fall behind the cursor and never retry. Once anything has
                    // failed this run, stop advancing , the next run resumes at the first gap.
                    if (ok) confirm(item)
                    ok
                },
                onItemStart = { item ->
                    index++; curSize = item.size; curRead = 0L
                    progress.onItemStart(cmd.kind, item.name, index, total, item.size)
                },
                onBytes = { read ->
                    curRead = read
                    val (bps, eta) = meter.update(doneBytes + read)
                    progress.onItemBytes(cmd.kind, read, curSize, doneBytes + read, bps, eta)
                },
                onProgress = { sent -> doneBytes += curSize; progress.onItemDone(cmd.kind, sent, total) },
            )
        }
        android.util.Log.i("LocalGhost", "sync ${cmd.kind} finished: ${result.itemsSent} confirmed of $total")
        BoxClient.report(result)
        // Final position report , the box's cursor now reflects this run; the next run (any device
        // state, even a fresh install) resumes from here.
        if (confirmedTs > 0) kotlinx.coroutines.runBlocking { BoxClient.reportCursor(ctx, cmd.kind, confirmedTs, confirmedId) }
        progress.onDone(result)
    }
}
