package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import com.localghost.app.net.BoxClient
import com.localghost.app.ui.theme.*

/**
 * Full-screen video player. The box speaks mTLS + bearer, which no stock media player can , so the
 * bytes are fetched through the authenticated channel into the app's cache and played as a local
 * file (VideoView, no new dependencies). The cache file dies with the dialog: the phone remains a
 * window, not a second archive.
 */
@Composable
fun VideoPlayer(hash: String, onDismiss: () -> Unit) {
    val ctx = LocalContext.current
    var file by remember(hash) { mutableStateOf<java.io.File?>(null) }
    var failed by remember(hash) { mutableStateOf(false) }
    LaunchedEffect(hash) {
        val bytes = BoxClient.frameOriginal(ctx, hash)
        if (bytes == null) { failed = true; return@LaunchedEffect }
        val f = java.io.File(ctx.cacheDir, "play-$hash.tmp")
        runCatching { f.writeBytes(bytes) }
            .onFailure { failed = true }
            .onSuccess { file = f }
    }
    DisposableEffect(hash) {
        onDispose { file?.delete() }
    }
    Dialog(onDismissRequest = onDismiss,
        properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Box(Modifier.fillMaxSize().background(Color.Black), contentAlignment = Alignment.Center) {
            when {
                file != null -> AndroidView(
                    factory = { c ->
                        android.widget.VideoView(c).apply {
                            setVideoPath(file!!.absolutePath)
                            setMediaController(android.widget.MediaController(c).also { it.setAnchorView(this) })
                            setOnPreparedListener { mp -> mp.isLooping = false; start() }
                            setOnErrorListener { _, _, _ -> failed = true; true }
                        }
                    },
                    modifier = Modifier.fillMaxSize())
                failed -> Text("! could not play this video (codec or fetch)",
                    color = TerminalDim, style = MaterialTheme.typography.bodyMedium)
                else -> Column(horizontalAlignment = Alignment.CenterHorizontally) {
                    Text("fetching from your box…", color = GhostTextDim,
                        style = MaterialTheme.typography.bodyMedium)
                    Spacer(Modifier.height(12.dp))
                    LinearProgressIndicator(color = TerminalGreen,
                        modifier = Modifier.fillMaxWidth(0.5f))
                }
            }
            Text("[ close ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.align(Alignment.TopEnd).padding(16.dp).clickable { onDismiss() })
        }
    }
}
