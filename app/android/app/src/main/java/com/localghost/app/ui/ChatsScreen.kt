package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import com.localghost.app.net.BoxClient
import kotlinx.coroutines.launch
import androidx.compose.runtime.rememberCoroutineScope
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.localghost.app.net.Conversation
import com.localghost.app.ui.theme.*

@Composable
fun ChatsScreen(
    conversations: List<Conversation>,
    activeConvId: String?,
    onSelect: (String) -> Unit,
    onNew: () -> Unit,
    onDelete: (String) -> Unit,
    onOpenBoxChat: (Long) -> Unit = {},
) {
    var query by remember { mutableStateOf("") }
    // THE BOX'S conversations , everything synthd persisted, searched server-side (titles AND
    // message bodies), keyset-paged. The local list above it is this device's in-flight state;
    // the box list is the archive. Search debounces 350ms so typing does not strafe the API.
    val ctx = androidx.compose.ui.platform.LocalContext.current
    val scope = rememberCoroutineScope()
    var boxChats by remember { mutableStateOf<List<BoxClient.BoxChat>>(emptyList()) }
    var boxLoading by remember { mutableStateOf(false) }
    var boxExhausted by remember { mutableStateOf(false) }
    var boxFailed by remember { mutableStateOf(false) }
    LaunchedEffect(query) {
        kotlinx.coroutines.delay(350)
        boxLoading = true; boxFailed = false
        val page = BoxClient.boxChats(ctx, q = query.trim())
        boxLoading = false
        if (page == null) { boxFailed = true; return@LaunchedEffect }
        boxChats = page
        boxExhausted = page.size < 20
    }
    val filtered = remember(conversations, query) {
        if (query.isBlank()) conversations
        else conversations.filter { it.title.contains(query.trim(), ignoreCase = true) }
    }

    Column(Modifier.fillMaxSize().padding(horizontal = 20.dp).padding(top = 20.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            SectionLabel("CHATS")
            Spacer(Modifier.weight(1f))
            Text("＋ new", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable { onNew() })
        }
        Spacer(Modifier.height(12.dp))

        // search box
        Row(
            Modifier.fillMaxWidth()
                .border(1.dp, GhostBorder, RoundedCornerShape(20.dp))
                .background(VoidLighter, RoundedCornerShape(20.dp))
                .padding(horizontal = 14.dp, vertical = 10.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text("⌕", color = GhostTextDim, style = MaterialTheme.typography.bodyMedium,
                modifier = Modifier.padding(end = 8.dp))
            BasicTextField(
                value = query, onValueChange = { query = it },
                modifier = Modifier.weight(1f),
                textStyle = MaterialTheme.typography.bodyMedium.copy(color = GhostText),
                cursorBrush = SolidColor(TerminalGreen),
                singleLine = true,
                decorationBox = { inner ->
                    if (query.isEmpty())
                        Text("search chats…", color = GhostTextDim,
                            style = MaterialTheme.typography.bodyMedium)
                    inner()
                },
            )
            if (query.isNotEmpty())
                Text("✕", color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { query = "" }.padding(start = 8.dp))
        }

        Spacer(Modifier.height(12.dp))

        when {
            conversations.isEmpty() && boxChats.isEmpty() && !boxLoading && !boxFailed ->
                EmptyLine("no chats yet. start one with ＋ new.")
            else -> LazyColumn(Modifier.fillMaxSize()) {
                if (filtered.isNotEmpty()) {
                    items(filtered, key = { "local-" + it.id }) { c ->
                        ChatRow(c, c.id == activeConvId, onSelect, onDelete)
                    }
                }
                item(key = "box-header") {
                    Spacer(Modifier.height(12.dp))
                    SectionLabel("ON THE BOX")
                    Spacer(Modifier.height(8.dp))
                }
                when {
                    boxFailed -> item(key = "box-failed") {
                        EmptyLine("box unreachable , the archive list needs a connection.")
                    }
                    boxLoading && boxChats.isEmpty() -> item(key = "box-loading") {
                        EmptyLine("reading from the box…")
                    }
                    boxChats.isEmpty() -> item(key = "box-empty") {
                        EmptyLine(if (query.isBlank()) "nothing persisted yet , non-incognito chats land here."
                                  else "nothing on the box matches \"${query.trim()}\".")
                    }
                    else -> {
                        items(boxChats, key = { "box-" + it.id }) { c ->
                            BoxChatRow(c) { onOpenBoxChat(c.id) }
                        }
                        if (!boxExhausted) item(key = "box-more") {
                            Text(if (boxLoading) "loading…" else "LOAD MORE ▾",
                                color = TerminalDim, style = MaterialTheme.typography.labelMedium,
                                modifier = Modifier.fillMaxWidth()
                                    .clickable(enabled = !boxLoading) {
                                        boxLoading = true
                                        scope.launch {
                                            val page = BoxClient.boxChats(ctx, q = query.trim(),
                                                beforeUpdated = boxChats.last().updatedAt)
                                            boxLoading = false
                                            if (page == null) { boxFailed = true; return@launch }
                                            boxChats = boxChats + page
                                            if (page.size < 20) boxExhausted = true
                                        }
                                    }
                                    .padding(vertical = 10.dp))
                        }
                    }
                }
            }
        }
    }
}

@Composable
private fun BoxChatRow(c: BoxClient.BoxChat, onOpen: () -> Unit) {
    Row(
        Modifier.fillMaxWidth()
            .border(1.dp, GhostBorder, RectangleShape)
            .background(Void)
            .clickable { onOpen() }
            .padding(14.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f)) {
            Text(c.title.ifBlank { "(untitled)" }, color = GhostText,
                style = MaterialTheme.typography.bodyMedium,
                maxLines = 1, overflow = TextOverflow.Ellipsis)
            Spacer(Modifier.height(2.dp))
            Text(java.text.SimpleDateFormat("MMM d, HH:mm", java.util.Locale.US)
                    .format(java.util.Date(c.updatedAt)) + " · ${c.messages} msgs",
                color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        }
        Text("→", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
    }
    Spacer(Modifier.height(8.dp))
}

@Composable
private fun ChatRow(c: Conversation, active: Boolean, onSelect: (String) -> Unit, onDelete: (String) -> Unit) {
    Row(
        Modifier.fillMaxWidth()
            .border(1.dp, if (active) TerminalDim else GhostBorder, RectangleShape)
            .background(if (active) VoidLighter else Void)
            .clickable { onSelect(c.id) }
            .padding(14.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f)) {
            Text(c.title, color = if (active) TerminalGreen else GhostText,
                style = MaterialTheme.typography.bodyMedium,
                maxLines = 1, overflow = TextOverflow.Ellipsis)
            Spacer(Modifier.height(2.dp))
            Text("${c.updatedLabel} · ${c.messageCount} msgs", color = GhostTextDim,
                style = MaterialTheme.typography.labelMedium)
        }
        Text("✕", color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.clickable { onDelete(c.id) }.padding(start = 10.dp))
    }
    Spacer(Modifier.height(8.dp))
}
