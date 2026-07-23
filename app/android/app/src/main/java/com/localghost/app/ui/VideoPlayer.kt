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
@androidx.annotation.OptIn(androidx.media3.common.util.UnstableApi::class)
@Composable
fun VideoPlayer(hash: String, onDismiss: () -> Unit) {
    val ctx = LocalContext.current
    // TRUE STREAMING, IN-PROCESS , ExoPlayer reads through BoxDataSource: the player's byte
    // requests are function calls into the authenticated channel, seeks are Range requests, and
    // there is no socket, no proxy, no cleartext , the attack surface is not small, it is absent.
    // Fallback ladder: exo stream -> disk-streamed download (VideoView) -> honest failure.
    var file by remember(hash) { mutableStateOf<java.io.File?>(null) }
    var failed by remember(hash) { mutableStateOf(false) }
    var fetched by remember(hash) { mutableStateOf(0L) }
    var fallingBack by remember(hash) { mutableStateOf(false) }
    val exo = remember(hash) {
        androidx.media3.exoplayer.ExoPlayer.Builder(ctx).build().apply {
            setMediaSource(
                androidx.media3.exoplayer.source.ProgressiveMediaSource.Factory(
                    com.localghost.app.net.BoxDataSource.Factory(ctx.applicationContext))
                    .createMediaSource(androidx.media3.common.MediaItem.fromUri("box://frame/" + hash)))
            addListener(object : androidx.media3.common.Player.Listener {
                override fun onPlayerError(error: androidx.media3.common.PlaybackException) {
                    fallingBack = true
                }
            })
            prepare()
            playWhenReady = true
        }
    }
    LaunchedEffect(fallingBack) {
        if (!fallingBack) return@LaunchedEffect
        runCatching { exo.stop() }
        val f = java.io.File(ctx.cacheDir, "play-$hash.tmp")
        if (BoxClient.frameOriginalToFile(ctx, hash, f, onBytes = { fetched = it })) file = f
        else failed = true
    }
    DisposableEffect(hash) {
        onDispose { runCatching { exo.release() }; file?.delete() }
    }
    Dialog(onDismissRequest = onDismiss,
        properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Box(Modifier.fillMaxSize().background(Color.Black), contentAlignment = Alignment.Center) {
            when {
                !fallingBack && !failed -> AndroidView(
                    factory = { c ->
                        androidx.media3.ui.PlayerView(c).apply {
                            player = exo
                            useController = true
                        }
                    },
                    modifier = Modifier.fillMaxSize())
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
                    Text("fetching from your box… %.1f MB".format(fetched / 1048576.0),
                        color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
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
