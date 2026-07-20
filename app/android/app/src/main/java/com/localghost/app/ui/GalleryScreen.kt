package com.localghost.app.ui

import android.graphics.BitmapFactory
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.border
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.rememberCoroutineScope
import kotlinx.coroutines.launch
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import com.localghost.app.net.BoxClient
import com.localghost.app.ui.theme.*

/**
 * GALLERY , a grid of what the BOX has archived (not the phone's own roll: this is the proof your
 * copies exist). Newest first, paged 60 at a time, thumbnails streamed from /v1/frames/thumb (webp
 * when the box has cwebp installed, jpeg otherwise). Videos show a play glyph until video
 * thumbnailing (a frame grab) exists box-side.
 */
@Composable
fun GalleryScreen() {
    val ctx = LocalContext.current
    var frames by remember { mutableStateOf(listOf<BoxClient.GalleryFrame>()) }
    var query by remember { mutableStateOf("") }
    var results by remember { mutableStateOf<List<BoxClient.GalleryFrame>?>(null) }
    // The location-and-tags search , "waterfall vancouver island" over place + name + tags, AND per
    // term, served by /v1/frames/search. Empty query returns to the paged archive.
    LaunchedEffect(query) {
        val q = query.trim()
        if (q.length < 2) { results = null; return@LaunchedEffect }
        kotlinx.coroutines.delay(350) // debounce typing
        results = BoxClient.framesSearch(ctx, q)
    }
    var selected by remember { mutableStateOf<BoxClient.GalleryFrame?>(null) }

    selected?.let { f ->
        FrameDetailDialog(
            frame = f,
            onDismiss = { selected = null },
            onTagsChanged = { hash, newTags ->
                frames = frames.map { if (it.hash == hash) it.copy(tags = newTags) else it }
                selected = selected?.let { s2 -> if (s2.hash == hash) s2.copy(tags = newTags) else s2 }
            },
        )
    }
    var loading by remember { mutableStateOf(true) }
    var endReached by remember { mutableStateOf(false) }
    val thumbs = remember { mutableStateMapOf<String, android.graphics.Bitmap?>() }

    suspend fun loadPage() {
        loading = true
        val before = frames.lastOrNull()?.takenAt ?: 0L
        val page = BoxClient.framesList(ctx, before)
        if (page.isEmpty()) endReached = true else frames = frames + page
        loading = false
    }

    LaunchedEffect(Unit) { loadPage() }

    Column(Modifier.fillMaxSize().padding(horizontal = 20.dp)) {
        Spacer(Modifier.height(16.dp))
        Text("> GALLERY", color = TerminalGreen, style = MaterialTheme.typography.titleMedium)
        Spacer(Modifier.height(4.dp))
        Text(
            "What is archived on your box , copies streamed from the encrypted volume, newest first.",
            color = TerminalDim, style = MaterialTheme.typography.bodySmall)
        Spacer(Modifier.height(10.dp))
        // Location + tags search. Terminal-styled inline field; blank returns to the paged archive.
        androidx.compose.foundation.text.BasicTextField(
            value = query, onValueChange = { query = it }, singleLine = true,
            keyboardOptions = androidx.compose.foundation.text.KeyboardOptions(
                imeAction = androidx.compose.ui.text.input.ImeAction.Search),
            textStyle = MaterialTheme.typography.bodyMedium.copy(color = GhostText),
            cursorBrush = androidx.compose.ui.graphics.SolidColor(TerminalGreen),
            decorationBox = { inner ->
                Row(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).padding(10.dp),
                    verticalAlignment = Alignment.CenterVertically) {
                    Text("⌕ ", color = TerminalDim, style = MaterialTheme.typography.bodyMedium)
                    Box(Modifier.weight(1f)) {
                        if (query.isEmpty()) Text("place, tags , e.g. waterfall vancouver island",
                            color = TerminalDim, style = MaterialTheme.typography.bodyMedium)
                        inner()
                    }
                    if (query.isNotEmpty()) Text("✕", color = GhostTextDim,
                        modifier = Modifier.clickable { query = "" }.padding(start = 6.dp))
                }
            })
        Spacer(Modifier.height(12.dp))

        val shown = results ?: frames
        if (results != null) {
            Text("${shown.size} result${if (shown.size == 1) "" else "s"} for \"${query.trim()}\"",
                color = TerminalDim, style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(6.dp))
        }
        if (shown.isEmpty() && !loading) {
            Text(if (results != null) "! nothing matches , try fewer words"
                 else "! nothing archived yet , run a sync",
                color = TerminalDim, style = MaterialTheme.typography.bodyMedium)
        }

        LazyVerticalGrid(
            columns = GridCells.Fixed(3),
            horizontalArrangement = Arrangement.spacedBy(4.dp),
            verticalArrangement = Arrangement.spacedBy(4.dp),
            modifier = Modifier.weight(1f)
        ) {
            items(shown, key = { it.hash }) { frame ->
                // Fetch each thumb once; null means tried-and-missing (video or fetch failure).
                LaunchedEffect(frame.hash) {
                    if (!thumbs.containsKey(frame.hash)) {
                        // Cap the in-memory cache , a multi-thousand-photo archive would otherwise
                        // accumulate every decoded bitmap and OOM. Evicting arbitrary old entries is
                        // fine: scrolled-back-to cells just refetch (thumbs are small and local).
                        if (thumbs.size > 240) {
                            thumbs.keys.take(60).forEach { thumbs.remove(it) }
                        }
                        val bytes = BoxClient.frameThumb(ctx, frame.hash)
                        thumbs[frame.hash] =
                            bytes?.let { BitmapFactory.decodeByteArray(it, 0, it.size) }
                    }
                }
                Box(
                    Modifier.aspectRatio(1f).background(VoidLighter)
                        .clickable { selected = frame },
                    contentAlignment = Alignment.Center
                ) {
                    val bmp = thumbs[frame.hash]
                    when {
                        bmp != null -> Image(
                            bitmap = bmp.asImageBitmap(), contentDescription = null,
                            contentScale = ContentScale.Crop, modifier = Modifier.fillMaxSize())
                        frame.kind == "video" -> Text("▶", color = TerminalGreen,
                            style = MaterialTheme.typography.titleLarge)
                        else -> Text("·", color = TerminalDim)
                    }
                }
            }
            if (!endReached && results == null) {
                item(span = { androidx.compose.foundation.lazy.grid.GridItemSpan(3) }) {
                    var wantMore by remember { mutableStateOf(false) }
                    LaunchedEffect(wantMore) { if (wantMore) { loadPage(); wantMore = false } }
                    Box(Modifier.fillMaxWidth().padding(vertical = 12.dp),
                        contentAlignment = Alignment.Center) {
                        GhostButton(if (loading) "LOADING…" else "LOAD MORE",
                            onClick = { if (!loading) wantMore = true }, enabled = !loading)
                    }
                }
            }
        }
    }
}


/** Frame detail , the picture's identity card: derived name, date, and the TAGS, editable. Tag
 *  removal tombstones on the box (the model never re-proposes it); adds are source 'user'. Edits
 *  apply optimistically and roll back on failure , the box remains the truth. */
@Composable
private fun FrameDetailDialog(
    frame: BoxClient.GalleryFrame,
    onDismiss: () -> Unit,
    onTagsChanged: (String, List<String>) -> Unit,
) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var newTag by remember { mutableStateOf("") }
    var bmp by remember { mutableStateOf<android.graphics.Bitmap?>(null) }
    LaunchedEffect(frame.hash) {
        bmp = BoxClient.frameThumb(ctx, frame.hash)?.let { BitmapFactory.decodeByteArray(it, 0, it.size) }
    }
    androidx.compose.ui.window.Dialog(onDismissRequest = onDismiss) {
        Column(
            Modifier.background(Void).border(1.dp, TerminalDim).padding(16.dp)
                .verticalScroll(rememberScrollState())
        ) {
            Box(Modifier.fillMaxWidth().aspectRatio(1f).background(VoidLighter),
                contentAlignment = Alignment.Center) {
                val b = bmp
                if (b != null) Image(bitmap = b.asImageBitmap(), contentDescription = null,
                    contentScale = ContentScale.Fit, modifier = Modifier.fillMaxSize())
                else Text(if (frame.kind == "video") "▶" else "·", color = TerminalGreen)
            }
            Spacer(Modifier.height(12.dp))
            Text(
                if (frame.name.isNotBlank()) frame.name else "(unnamed , tags pending)",
                color = GhostText, style = MaterialTheme.typography.bodyLarge)
            Text(
                java.text.SimpleDateFormat("yyyy-MM-dd HH:mm", java.util.Locale.US)
                    .format(java.util.Date(frame.takenAt * 1000)) + " · ${frame.kind}",
                color = GhostTextDim, style = MaterialTheme.typography.bodySmall)
            if (frame.place.isNotBlank()) {
                Spacer(Modifier.height(4.dp))
                Text("⌖ ${frame.place}", color = TerminalDim,
                    style = MaterialTheme.typography.bodySmall)
            }
            Spacer(Modifier.height(12.dp))
            Text("TAGS", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
            Spacer(Modifier.height(6.dp))
            if (frame.tags.isEmpty()) {
                Text("none yet , the tag pass runs after captioning", color = GhostTextDim,
                    style = MaterialTheme.typography.bodySmall)
            }
            frame.tags.forEach { tag ->
                Row(Modifier.fillMaxWidth().padding(vertical = 2.dp)) {
                    Text("# $tag", color = TerminalGreen, modifier = Modifier.weight(1f),
                        style = MaterialTheme.typography.bodyMedium)
                    Text("✕", color = Warning, style = MaterialTheme.typography.bodyMedium,
                        modifier = Modifier.clickable {
                            val before = frame.tags
                            onTagsChanged(frame.hash, before - tag) // optimistic
                            scope.launch {
                                if (!BoxClient.frameTag(ctx, frame.hash, tag, add = false))
                                    onTagsChanged(frame.hash, before) // rollback , box is the truth
                            }
                        })
                }
            }
            Spacer(Modifier.height(10.dp))
            Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                OutlinedTextField(
                    value = newTag, onValueChange = { newTag = it.lowercase().take(24) },
                    modifier = Modifier.weight(1f), singleLine = true,
                    placeholder = { Text("add a tag", color = GhostTextDim) })
                Spacer(Modifier.width(8.dp))
                GhostButton("ADD", onClick = {
                    val t = newTag.trim()
                    if (t.length >= 2) {
                        val before = frame.tags
                        onTagsChanged(frame.hash, before + t) // optimistic
                        newTag = ""
                        scope.launch {
                            if (!BoxClient.frameTag(ctx, frame.hash, t, add = true))
                                onTagsChanged(frame.hash, before)
                        }
                    }
                })
            }
            Spacer(Modifier.height(12.dp))
            GhostButton("CLOSE", onClick = onDismiss, modifier = Modifier.fillMaxWidth())
        }
    }
}
