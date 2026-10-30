package com.localghost.app.ui

import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.interaction.collectIsDraggedAsState
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.pager.HorizontalPager
import androidx.compose.foundation.pager.rememberPagerState
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import com.localghost.app.net.BoxClient
import com.localghost.app.ui.theme.*
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext

/**
 * A strip of the box's photos and videos, full screen, one at a time (ON THIS DAY, an outing's
 * covers): swipe between them, and they turn by themselves every few seconds, a slideshow, until
 * a swipe or a tap pauses it. A video shows its picture with ▶ and plays in [VideoPlayer] when
 * tapped; [ zoom ] opens a photo in the pinch-zoom [ImageViewer]. Each picture comes as the box's
 * thumb at once, then its preview. Nothing is kept on the phone: it is a window, not a copy.
 */
@Composable
fun MediaSlideshow(hashes: List<String>, start: Int, title: String = "", onDismiss: () -> Unit) {
    if (hashes.isEmpty()) return
    val ctx = LocalContext.current
    var kinds by remember(hashes) { mutableStateOf<Map<String, String>>(emptyMap()) }
    LaunchedEffect(hashes) { kinds = BoxClient.frameKinds(ctx, hashes) }
    val pager = rememberPagerState(initialPage = start.coerceIn(0, hashes.size - 1)) { hashes.size }
    var playing by remember { mutableStateOf(hashes.size > 1) }
    var zoom by remember { mutableStateOf<String?>(null) }
    var video by remember { mutableStateOf<String?>(null) }
    // a swipe by hand takes over from the slideshow
    val dragged by pager.interactionSource.collectIsDraggedAsState()
    LaunchedEffect(dragged) { if (dragged) playing = false }
    LaunchedEffect(playing, pager.currentPage, zoom, video) {
        if (!playing || zoom != null || video != null || hashes.size < 2) return@LaunchedEffect
        kotlinx.coroutines.delay(SLIDE_MS)
        pager.animateScrollToPage((pager.currentPage + 1) % hashes.size)
    }

    Dialog(onDismissRequest = onDismiss, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        // the strip runs itself, so while it plays the screen stays on (the flag is the dialog
        // window's and goes with it); paused, the phone's own timeout applies again
        val view = androidx.compose.ui.platform.LocalView.current
        DisposableEffect(view, playing) {
            view.keepScreenOn = playing
            onDispose { view.keepScreenOn = false }
        }
        Box(Modifier.fillMaxSize().background(Color.Black)) {
            HorizontalPager(state = pager, modifier = Modifier.fillMaxSize(), key = { hashes[it] }) { i ->
                val h = hashes[i]
                SlidePage(h, isVideo = kinds[h] == "video",
                    onTap = { if (kinds[h] == "video") video = h else playing = !playing })
            }
            // top: where you are in the strip, the slideshow's switch, close
            Row(Modifier.fillMaxWidth().align(Alignment.TopStart).background(Color(0x99000000)).padding(horizontal = 16.dp, vertical = 12.dp),
                verticalAlignment = Alignment.CenterVertically) {
                Text("${pager.currentPage + 1} / ${hashes.size}" + if (title.isNotBlank()) "  ·  $title" else "",
                    color = GhostTextDim, style = MaterialTheme.typography.labelMedium, modifier = Modifier.weight(1f))
                if (hashes.size > 1) {
                    Text(if (playing) "[ ❚❚ pause ]" else "[ ▶ slideshow ]", color = TerminalGreen,
                        style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.clickable { playing = !playing }.padding(horizontal = 8.dp))
                }
                Text("[ close ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { onDismiss() })
            }
            // bottom: what this one opens into
            val cur = hashes.getOrNull(pager.currentPage)
            if (cur != null) {
                val isVideo = kinds[cur] == "video"
                Text(if (isVideo) "[ ▶ play the video ]" else "[ zoom ]", color = TerminalGreen,
                    style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.align(Alignment.BottomCenter).padding(24.dp)
                        .background(Color(0x99000000)).clickable { playing = false; if (isVideo) video = cur else zoom = cur }
                        .padding(horizontal = 12.dp, vertical = 8.dp))
            }
        }
    }
    zoom?.let { h -> ImageViewer(h, onDismiss = { zoom = null }) }
    video?.let { h -> VideoPlayer(h, onDismiss = { video = null }) }
}

private const val SLIDE_MS = 4_000L

/** One picture of the strip: the thumb at once, the preview when it lands; ▶ over a video. */
@Composable
private fun SlidePage(hash: String, isVideo: Boolean, onTap: () -> Unit) {
    val ctx = LocalContext.current
    var bmp by remember(hash) { mutableStateOf<android.graphics.Bitmap?>(null) }
    var failed by remember(hash) { mutableStateOf(false) }
    LaunchedEffect(hash) {
        val thumb = BoxClient.frameThumb(ctx, hash)?.let { b -> withContext(Dispatchers.Default) { android.graphics.BitmapFactory.decodeByteArray(b, 0, b.size) } }
        if (thumb != null) bmp = thumb
        val preview = BoxClient.framePreview(ctx, hash)?.let { b -> withContext(Dispatchers.Default) { android.graphics.BitmapFactory.decodeByteArray(b, 0, b.size) } }
        if (preview != null) bmp = preview
        if (bmp == null) failed = true
    }
    Box(Modifier.fillMaxSize().pointerInput(hash) { detectTapGestures(onTap = { onTap() }) }, contentAlignment = Alignment.Center) {
        when {
            bmp != null -> Image(bitmap = bmp!!.asImageBitmap(), contentDescription = null,
                contentScale = ContentScale.Fit, modifier = Modifier.fillMaxSize())
            failed && !isVideo -> Text("! this one would not load from the box", color = TerminalDim, style = MaterialTheme.typography.bodyMedium)
            !isVideo -> Text("loading…", color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
        }
        if (isVideo) {
            Text("▶", color = TerminalGreen, style = MaterialTheme.typography.displayMedium,
                modifier = Modifier.background(Color(0x99000000)).padding(horizontal = 22.dp, vertical = 6.dp))
        }
    }
}

/**
 * A row of thumbnails that opens into [MediaSlideshow] at the one tapped. Videos carry a ▶ (the
 * box is asked once per strip which of the hashes are videos).
 */
@Composable
fun ThumbStrip(hashes: List<String>, title: String = "", size: androidx.compose.ui.unit.Dp = 84.dp) {
    val ctx = LocalContext.current
    var open by remember(hashes) { mutableStateOf<Int?>(null) }
    var kinds by remember(hashes) { mutableStateOf<Map<String, String>>(emptyMap()) }
    LaunchedEffect(hashes) { kinds = BoxClient.frameKinds(ctx, hashes) }
    androidx.compose.foundation.lazy.LazyRow(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
        items(hashes.size, key = { hashes[it] }) { i ->
            val hash = hashes[i]
            var bmp by remember(hash) { mutableStateOf<android.graphics.Bitmap?>(null) }
            LaunchedEffect(hash) {
                bmp = BoxClient.frameThumb(ctx, hash)?.let { b ->
                    withContext(Dispatchers.Default) { android.graphics.BitmapFactory.decodeByteArray(b, 0, b.size) }
                }
            }
            // a video wears the same green frame as in the gallery, and its ▶ sits at the foot
            val video = kinds[hash] == "video"
            Box(Modifier.size(size).border(if (video) 1.5.dp else 1.dp, if (video) TerminalGreen else GhostBorder, androidx.compose.ui.graphics.RectangleShape)
                .clickable { open = i }, contentAlignment = Alignment.Center) {
                bmp?.let {
                    Image(bitmap = it.asImageBitmap(), contentDescription = null,
                        contentScale = ContentScale.Crop, modifier = Modifier.fillMaxSize())
                }
                if (video) {
                    Text("▶", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.align(Alignment.BottomStart).background(Color(0xCC000000)).padding(horizontal = 6.dp, vertical = 2.dp))
                }
            }
        }
    }
    open?.let { i -> MediaSlideshow(hashes, i, title, onDismiss = { open = null }) }
}
