package com.localghost.app.ui

import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.gestures.detectTransformGestures
import androidx.compose.foundation.layout.*
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.graphics.graphicsLayer
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
 * Full-screen photo viewer with pinch-zoom and pan , fetched on open, never cached to disk (the
 * archive is the box's job; the phone is a window, not a second copy). Double-tap toggles fit/3x,
 * single tap dismisses, drag pans while zoomed.
 *
 * SMALL FIRST. It used to fetch the ORIGINAL, the preview AND the thumb before showing anything ,
 * three round trips, the first of them a 5-12 MB file decoded at 12 megapixels , so every tap on a
 * photo was a black screen for seconds. Now: the thumb (a few KB, mostly already in the gallery's
 * cache) is on screen within a frame, the box's 1600px preview replaces it a moment later, and the
 * original is fetched only when zoomed in past 1.5x or asked for with [ full ], decoded to at most
 * ~4000px on its long edge so a 48-megapixel shot does not become a 200 MB bitmap.
 */
@Composable
fun ImageViewer(hash: String, caption: String = "", onDismiss: () -> Unit) {
    val ctx = LocalContext.current
    var bmp by remember(hash) { mutableStateOf<android.graphics.Bitmap?>(null) }
    var failed by remember(hash) { mutableStateOf(false) }
    var quality by remember(hash) { mutableStateOf("") }
    var wantFull by remember(hash) { mutableStateOf(false) }
    var fullState by remember(hash) { mutableStateOf("") } // "", "loading", "shown", "failed"
    LaunchedEffect(hash) {
        // decodes off the main thread: a 1600px JPEG is tens of milliseconds, a sampled original
        // a few hundred, and the screen keeps animating either way
        val thumb = BoxClient.frameThumb(ctx, hash)?.let { b -> withContext(Dispatchers.Default) { decodeUpright(b) } }
        if (thumb != null && bmp == null) { bmp = thumb; quality = "thumb" }
        val preview = BoxClient.framePreview(ctx, hash)?.let { b -> withContext(Dispatchers.Default) { decodeUpright(b) } }
        if (preview != null) { bmp = preview; quality = "preview" }
        if (bmp == null) {
            // nothing derived on the box (a frame its pipeline could not read): the original is
            // the only picture there is
            wantFull = true
        }
    }
    LaunchedEffect(hash, wantFull) {
        if (!wantFull || fullState == "shown" || fullState == "loading") return@LaunchedEffect
        fullState = "loading"
        val full = BoxClient.frameOriginal(ctx, hash)?.let { b -> withContext(Dispatchers.Default) { decodeUpright(b, maxEdge = 4096) } }
        if (full != null) { bmp = full; quality = "original"; fullState = "shown" }
        else { fullState = "failed"; if (bmp == null) failed = true }
    }
    var scale by remember(hash) { mutableStateOf(1f) }
    var offX by remember(hash) { mutableStateOf(0f) }
    var offY by remember(hash) { mutableStateOf(0f) }
    LaunchedEffect(scale) { if (scale > 1.5f) wantFull = true }
    Dialog(onDismissRequest = onDismiss,
        properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Box(Modifier.fillMaxSize().background(Color.Black), contentAlignment = Alignment.Center) {
            when {
                bmp != null -> Image(
                    bitmap = bmp!!.asImageBitmap(),
                    contentDescription = null,
                    contentScale = ContentScale.Fit,
                    modifier = Modifier.fillMaxSize()
                        .graphicsLayer(scaleX = scale, scaleY = scale,
                            translationX = offX, translationY = offY)
                        .pointerInput(hash) {
                            detectTransformGestures { _, pan, gz, _ ->
                                scale = (scale * gz).coerceIn(1f, 12f)
                                if (scale > 1f) { offX += pan.x; offY += pan.y }
                                else { offX = 0f; offY = 0f }
                            }
                        }
                        .pointerInput(hash) {
                            detectTapGestures(
                                onDoubleTap = {
                                    if (scale > 1.2f) { scale = 1f; offX = 0f; offY = 0f } else scale = 3f
                                },
                                onTap = { if (scale <= 1.05f) onDismiss() })
                        })
                failed -> Text("! could not load this photo from the box",
                    color = TerminalDim, style = MaterialTheme.typography.bodyMedium)
                else -> Text("loading…", color = GhostTextDim,
                    style = MaterialTheme.typography.bodyMedium)
            }
            if (caption.isNotBlank() && scale <= 1.05f) {
                Text(caption, color = GhostTextDim,
                    style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.align(Alignment.BottomStart)
                        .background(Color(0xCC000000)).padding(12.dp))
            }
            Text("[ close ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.align(Alignment.TopEnd).padding(16.dp).clickable { onDismiss() })
            if (bmp != null && scale <= 1.05f) {
                val line = when {
                    fullState == "shown" -> "original"
                    fullState == "loading" -> "$quality · fetching the original…"
                    fullState == "failed" -> "$quality · the original would not load"
                    else -> "$quality · [ full ]"
                }
                Text(line, color = TerminalDim, style = MaterialTheme.typography.labelSmall,
                    modifier = Modifier.align(Alignment.TopStart).padding(16.dp)
                        .clickable { if (fullState == "") wantFull = true })
            }
        }
    }
}

/** Decode honouring EXIF orientation , BitmapFactory ignores the tag, so ORIGINALS (unlike the
 *  box's pre-uprighted thumbs) arrived sideways. The framework ExifInterface reads the tag from
 *  the bytes (no extra dependency); one Matrix applies rotation and mirroring. */
private fun decodeUpright(bytes: ByteArray, maxEdge: Int = 0): android.graphics.Bitmap? {
    val opts = android.graphics.BitmapFactory.Options()
    if (maxEdge > 0) {
        // measure first, then decode at the largest power-of-two subsample that keeps the long
        // edge under maxEdge , the decoder skips the pixels it drops, so this is faster, not
        // just smaller
        opts.inJustDecodeBounds = true
        android.graphics.BitmapFactory.decodeByteArray(bytes, 0, bytes.size, opts)
        var sample = 1
        while (maxOf(opts.outWidth, opts.outHeight) / (sample * 2) >= maxEdge) sample *= 2
        opts.inJustDecodeBounds = false
        opts.inSampleSize = sample
    }
    val b = android.graphics.BitmapFactory.decodeByteArray(bytes, 0, bytes.size, opts) ?: return null
    val o = try {
        android.media.ExifInterface(java.io.ByteArrayInputStream(bytes))
            .getAttributeInt(android.media.ExifInterface.TAG_ORIENTATION,
                android.media.ExifInterface.ORIENTATION_NORMAL)
    } catch (_: Exception) { android.media.ExifInterface.ORIENTATION_NORMAL }
    if (o == android.media.ExifInterface.ORIENTATION_NORMAL || o == 0) return b
    val mx = android.graphics.Matrix()
    when (o) {
        android.media.ExifInterface.ORIENTATION_ROTATE_90 -> mx.postRotate(90f)
        android.media.ExifInterface.ORIENTATION_ROTATE_180 -> mx.postRotate(180f)
        android.media.ExifInterface.ORIENTATION_ROTATE_270 -> mx.postRotate(270f)
        android.media.ExifInterface.ORIENTATION_FLIP_HORIZONTAL -> mx.postScale(-1f, 1f)
        android.media.ExifInterface.ORIENTATION_FLIP_VERTICAL -> mx.postScale(1f, -1f)
        android.media.ExifInterface.ORIENTATION_TRANSPOSE -> { mx.postRotate(90f); mx.postScale(-1f, 1f) }
        android.media.ExifInterface.ORIENTATION_TRANSVERSE -> { mx.postRotate(270f); mx.postScale(-1f, 1f) }
        else -> return b
    }
    return try {
        android.graphics.Bitmap.createBitmap(b, 0, 0, b.width, b.height, mx, true)
    } catch (_: OutOfMemoryError) { b } // a sideways photo beats a crash
}
