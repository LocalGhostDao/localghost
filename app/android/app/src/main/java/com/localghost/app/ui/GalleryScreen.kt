package com.localghost.app.ui

import android.graphics.BitmapFactory
import androidx.compose.foundation.Image
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.GridItemSpan
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.graphics.asImageBitmap
import androidx.compose.ui.layout.ContentScale
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import androidx.compose.ui.window.DialogProperties
import com.localghost.app.net.BoxClient
import com.localghost.app.ui.theme.*
import kotlinx.coroutines.launch

/**
 * GALLERY , a grid of what the BOX has archived (not the phone's own roll: this is the proof your
 * copies exist). Newest first, paged 60 at a time, thumbnails streamed from /v1/frames/thumb (webp
 * when the box has cwebp installed, jpeg otherwise). A video's thumbnail is a frame the box grabbed
 * (ffmpeg, one second in); the cell wears a green frame, ▶ and the running time, so a clip is
 * told from a photo at a glance. A tap opens the frame's card: the picture at its own shape, who
 * and what the box saw in it as tags by group, and the tags editable.
 */
@Composable
fun GalleryScreen() {
    val ctx = LocalContext.current
    var frames by remember { mutableStateOf(listOf<BoxClient.GalleryFrame>()) }
    var query by remember { mutableStateOf("") }
    var results by remember { mutableStateOf<List<BoxClient.GalleryFrame>?>(null) }
    var onlyVideos by rememberSaveable { mutableStateOf(false) }
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
                // a tag the person added sits in "other" until the box's categoriser sees it; a
                // removed one leaves its group
                val fix: (BoxClient.GalleryFrame) -> BoxClient.GalleryFrame = { x ->
                    if (x.hash == hash) x.copy(tags = newTags, tagsByCategory = GalleryText.regroup(x.tagsByCategory, newTags)) else x
                }
                frames = frames.map(fix)
                results = results?.map(fix)
                selected = selected?.let(fix)
            },
        )
    }
    var loading by remember { mutableStateOf(true) }
    var endReached by remember { mutableStateOf(false) }
    var failed by remember { mutableStateOf(false) } // the box did not answer: not the same as an empty archive
    val thumbs = remember { mutableStateMapOf<String, android.graphics.Bitmap?>() }

    suspend fun loadPage() {
        loading = true
        val before = frames.lastOrNull()?.takenAt ?: 0L
        val page = BoxClient.framesList(ctx, before)
        when {
            page == null -> failed = true
            page.isEmpty() -> { endReached = true; failed = false }
            else -> { frames = frames + page; failed = false }
        }
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
        BasicTextField(
            value = query, onValueChange = { query = it }, singleLine = true,
            keyboardOptions = KeyboardOptions(imeAction = ImeAction.Search),
            keyboardActions = KeyboardActions(onSearch = {}),
            textStyle = MaterialTheme.typography.bodyMedium.copy(color = GhostText),
            cursorBrush = SolidColor(TerminalGreen),
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
        Spacer(Modifier.height(8.dp))

        val all = results ?: frames
        val shown = if (onlyVideos) all.filter { it.kind == "video" } else all
        Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
            // the one filter: clips only (within the page or the search), lit when on
            Text("[ ▶ videos ]",
                color = if (onlyVideos) Void else TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.border(1.dp, TerminalGreen, RectangleShape)
                    .background(if (onlyVideos) TerminalGreen else Void)
                    .clickable { onlyVideos = !onlyVideos }.padding(horizontal = 8.dp, vertical = 4.dp))
            Spacer(Modifier.weight(1f))
            Text(when {
                results != null -> "${shown.size} result${if (shown.size == 1) "" else "s"} for \"${query.trim()}\""
                onlyVideos -> "${shown.size} of ${all.size} loaded"
                else -> "${all.size} loaded"
            }, color = TerminalDim, style = MaterialTheme.typography.labelSmall)
        }
        Spacer(Modifier.height(8.dp))
        if (shown.isEmpty() && !loading) {
            Text(when {
                results != null -> "! nothing matches , try fewer words"
                failed -> "! the box did not answer , is it unlocked?"
                onlyVideos -> "! no videos in what is loaded , LOAD MORE pages further back"
                else -> "! nothing archived yet , run a sync"
            }, color = if (failed && results == null) Warning else TerminalDim, style = MaterialTheme.typography.bodyMedium)
        }

        LazyVerticalGrid(
            columns = GridCells.Fixed(3),
            horizontalArrangement = Arrangement.spacedBy(4.dp),
            verticalArrangement = Arrangement.spacedBy(4.dp),
            modifier = Modifier.weight(1f)
        ) {
            items(shown, key = { it.hash }) { frame ->
                // Fetch each thumb once; null means tried-and-missing (a fetch failure, or a clip
                // the box has no ffmpeg for).
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
                GalleryCell(frame, thumbs[frame.hash]) { selected = frame }
            }
            if (!endReached && results == null) {
                item(span = { GridItemSpan(3) }) {
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

/** One cell of the grid. A photo is its thumbnail. A video is its grabbed frame inside a green
 *  frame, with ▶ and the running time on a dark strip at the foot, so the eye finds the clips. */
@Composable
private fun GalleryCell(frame: BoxClient.GalleryFrame, bmp: android.graphics.Bitmap?, onOpen: () -> Unit) {
    val video = frame.kind == "video"
    Box(
        Modifier.aspectRatio(1f).background(VoidLighter)
            .then(if (video) Modifier.border(1.5.dp, TerminalGreen, RectangleShape) else Modifier)
            .clickable { onOpen() },
        contentAlignment = Alignment.Center
    ) {
        when {
            bmp != null -> Image(
                bitmap = bmp.asImageBitmap(), contentDescription = null,
                contentScale = ContentScale.Crop, modifier = Modifier.fillMaxSize())
            video -> Text("▶", color = TerminalGreen, style = MaterialTheme.typography.titleLarge)
            else -> Text("·", color = TerminalDim)
        }
        if (video) {
            val d = GalleryText.duration(frame.duration)
            Row(
                Modifier.align(Alignment.BottomStart).fillMaxWidth()
                    .background(Brush.verticalGradient(listOf(Color.Transparent, Color(0xCC000000))))
                    .padding(horizontal = 6.dp, vertical = 4.dp),
                verticalAlignment = Alignment.CenterVertically
            ) {
                Text("▶", color = TerminalGreen, style = MaterialTheme.typography.labelSmall)
                if (d.isNotEmpty()) {
                    Spacer(Modifier.width(4.dp))
                    Text(d, color = TerminalGreen, style = MaterialTheme.typography.labelSmall)
                }
            }
        }
    }
}

/** Frame detail , the picture's card: the picture at its own shape (a tap opens it full screen, or
 *  plays the clip), the heading with the kind and the running time, the name, when and how big,
 *  where, what the box saw, and the TAGS by group, each a chip with its ✕, plus a line to add one.
 *  Tag removal tombstones on the box (the model never re-proposes it); adds are source 'user'.
 *  Edits apply optimistically and roll back on failure , the box remains the truth. */
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
    val video = frame.kind == "video"
    Dialog(onDismissRequest = onDismiss, properties = DialogProperties(usePlatformDefaultWidth = false)) {
        Column(
            Modifier.fillMaxWidth().padding(horizontal = 14.dp, vertical = 24.dp)
                .background(Void).border(1.dp, if (video) TerminalGreen else TerminalDim).padding(16.dp)
                .verticalScroll(rememberScrollState())
        ) {
            var full by remember(frame.hash) { mutableStateOf(false) }
            if (full) {
                if (video) VideoPlayer(frame.hash, onDismiss = { full = false })
                else ImageViewer(frame.hash,
                    caption = if (frame.description.isNotBlank()) frame.description else frame.name,
                    onDismiss = { full = false })
            }
            Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                Text(GalleryText.heading(frame.kind, frame.duration), color = TerminalGreen,
                    style = MaterialTheme.typography.titleMedium, modifier = Modifier.weight(1f))
                Text("[ ✕ ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { onDismiss() }.padding(start = 8.dp))
            }
            Spacer(Modifier.height(10.dp))
            // the picture at its own shape, between 4:5 and 16:9 so a panorama does not become a
            // sliver and a portrait does not fill the phone
            val b = bmp
            val ratio = if (b != null && b.height > 0) (b.width.toFloat() / b.height).coerceIn(0.8f, 1.78f) else 1.33f
            Box(Modifier.fillMaxWidth().aspectRatio(ratio).background(VoidLighter)
                    .clickable { full = true },
                contentAlignment = Alignment.Center) {
                if (b != null) Image(bitmap = b.asImageBitmap(), contentDescription = null,
                    contentScale = ContentScale.Fit, modifier = Modifier.fillMaxSize())
                else Text(if (video) "▶" else "·", color = TerminalGreen, style = MaterialTheme.typography.titleLarge)
                if (video) Text("[ ▶ PLAY ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.align(Alignment.BottomEnd).padding(8.dp)
                        .background(Color(0xCC000000)).border(1.dp, TerminalGreen, RectangleShape)
                        .padding(horizontal = 10.dp, vertical = 6.dp))
            }
            Text(GalleryText.hint(frame.kind), color = TerminalDim,
                style = MaterialTheme.typography.labelSmall, modifier = Modifier.padding(top = 4.dp))
            Spacer(Modifier.height(12.dp))
            Text(
                if (frame.name.isNotBlank()) frame.name else "(unnamed , tags pending)",
                color = GhostText, style = MaterialTheme.typography.bodyLarge)
            Text(GalleryText.taken(frame.takenAt, frame.bytes), color = GhostTextDim, style = MaterialTheme.typography.bodySmall)
            if (frame.place.isNotBlank()) {
                Spacer(Modifier.height(4.dp))
                Text("⌖ ${frame.place}", color = TerminalDim, style = MaterialTheme.typography.bodySmall)
            }
            if (frame.description.isNotBlank()) {
                Spacer(Modifier.height(10.dp))
                Text(frame.description, color = GhostText, style = MaterialTheme.typography.bodySmall)
            }
            Spacer(Modifier.height(14.dp))
            SectionLabel("TAGS")
            Spacer(Modifier.height(6.dp))
            val groups = GalleryText.groups(frame.tags, frame.tagsByCategory)
            if (groups.isEmpty()) {
                Text("none yet , the tag pass runs after captioning", color = GhostTextDim,
                    style = MaterialTheme.typography.bodySmall)
            }
            groups.forEach { (category, tags) ->
                if (groups.size > 1 || category != "tags") {
                    Text(category, color = TerminalDim, style = MaterialTheme.typography.labelSmall,
                        modifier = Modifier.padding(top = 6.dp, bottom = 4.dp))
                }
                TagChips(tags) { tag ->
                    val before = frame.tags
                    onTagsChanged(frame.hash, before - tag) // optimistic
                    scope.launch {
                        if (!BoxClient.frameTag(ctx, frame.hash, tag, add = false))
                            onTagsChanged(frame.hash, before) // rollback , box is the truth
                    }
                }
            }
            Spacer(Modifier.height(12.dp))
            fun add() {
                val t = newTag.trim()
                if (t.length >= 2 && t !in frame.tags) {
                    val before = frame.tags
                    onTagsChanged(frame.hash, before + t) // optimistic
                    newTag = ""
                    scope.launch {
                        if (!BoxClient.frameTag(ctx, frame.hash, t, add = true))
                            onTagsChanged(frame.hash, before)
                    }
                }
            }
            Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
                BasicTextField(
                    value = newTag, onValueChange = { newTag = it.lowercase().take(24) }, singleLine = true,
                    keyboardOptions = KeyboardOptions(imeAction = ImeAction.Done),
                    keyboardActions = KeyboardActions(onDone = { add() }),
                    textStyle = MaterialTheme.typography.bodyMedium.copy(color = GhostText),
                    cursorBrush = SolidColor(TerminalGreen),
                    modifier = Modifier.weight(1f),
                    decorationBox = { inner ->
                        Row(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).padding(10.dp)) {
                            Text("# ", color = TerminalDim, style = MaterialTheme.typography.bodyMedium)
                            Box(Modifier.weight(1f)) {
                                if (newTag.isEmpty()) Text("add a tag", color = TerminalDim, style = MaterialTheme.typography.bodyMedium)
                                inner()
                            }
                        }
                    })
                Spacer(Modifier.width(8.dp))
                GhostButton("ADD", onClick = { add() }, enabled = newTag.trim().length >= 2)
            }
            Spacer(Modifier.height(12.dp))
            GhostButton("CLOSE", onClick = onDismiss, modifier = Modifier.fillMaxWidth())
        }
    }
}

/** Tags as chips that wrap: "# tag" with its ✕, in the terminal's green on the void. */
@OptIn(androidx.compose.foundation.layout.ExperimentalLayoutApi::class)
@Composable
private fun TagChips(tags: List<String>, onRemove: (String) -> Unit) {
    FlowRow(horizontalArrangement = Arrangement.spacedBy(6.dp), verticalArrangement = Arrangement.spacedBy(6.dp)) {
        tags.forEach { tag ->
            Row(Modifier.border(1.dp, TerminalDim, RectangleShape).padding(start = 8.dp, top = 4.dp, bottom = 4.dp, end = 4.dp),
                verticalAlignment = Alignment.CenterVertically) {
                Text("# $tag", color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
                Text(" ✕", color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { onRemove(tag) }.padding(horizontal = 4.dp))
            }
        }
    }
}
