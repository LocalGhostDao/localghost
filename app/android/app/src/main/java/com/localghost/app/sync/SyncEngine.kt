package com.localghost.app.sync

import android.content.Context
import com.localghost.app.net.BoxClient
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

class SyncEngine(private val ctx: Context) {

    interface Progress {
        fun onStart(kind: MediaKind, total: Int)
        fun onItemStart(kind: MediaKind, name: String, index: Int, total: Int, size: Long)
        fun onItemBytes(kind: MediaKind, read: Long, size: Long)
        fun onItemDone(kind: MediaKind, sent: Int, total: Int)
        fun onDone(result: CommandResult)
    }

    suspend fun runCamera(kind: MediaKind, progress: Progress) {
        when (val cmd = BoxClient.nextCameraCommand(kind)) {
            is Command.SyncCamera -> exec(cmd, progress)
            else -> { progress.onStart(kind, 0); progress.onDone(CommandResult(Stream.CAMERA, kind, 0, 0)) }
        }
    }

    private suspend fun exec(cmd: Command.SyncCamera, progress: Progress) {
        val total = withContext(Dispatchers.IO) { CameraReader.count(ctx, cmd.kind, cmd.after) }
        progress.onStart(cmd.kind, total)
        var index = 0
        var curSize = 0L
        val result = withContext(Dispatchers.IO) {
            CameraReader.syncFrom(
                ctx, cmd.kind, cmd.after,
                send = { item, stream ->
                    kotlinx.coroutines.runBlocking { BoxClient.ingest(cmd.kind, item.name, stream) }
                },
                onItemStart = { item ->
                    index++; curSize = item.size
                    progress.onItemStart(cmd.kind, item.name, index, total, item.size)
                },
                onBytes = { read -> progress.onItemBytes(cmd.kind, read, curSize) },
                onProgress = { sent -> progress.onItemDone(cmd.kind, sent, total) },
            )
        }
        BoxClient.report(result)
        progress.onDone(result)
    }
}
