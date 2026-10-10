package com.localghost.app.ui

import androidx.compose.animation.animateContentSize
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
    onRenameBoxChat: (Long, String) -> Unit = { _, _ -> },
    onDeleteBoxChat: (Long) -> Unit = {},
) {
    var query by remember { mutableStateOf("") }
    // THE BOX'S conversations , everything synthd persisted, searched server-side (titles AND
    // message bodies), keyset-paged. There is one list: the drawer's `conversations` is the same
    // box list (MainActivity.refreshChats maps /v1/chats), so it is not drawn again above this one;
    // it is only a change signal, a rename or delete refreshes the drawer, and that reloads this
    // list so the box's truth comes back. Search debounces 350ms so typing does not strafe the API.
    val ctx = androidx.compose.ui.platform.LocalContext.current
    val scope = rememberCoroutineScope()
    var boxChats by remember { mutableStateOf<List<BoxClient.BoxChat>>(emptyList()) }
    var boxLoading by remember { mutableStateOf(false) }
    var boxExhausted by remember { mutableStateOf(false) }
    var boxFailed by remember { mutableStateOf(false) }
    LaunchedEffect(query, conversations) {
        kotlinx.coroutines.delay(350)
        boxLoading = true; boxFailed = false
        val page = BoxClient.boxChats(ctx, q = query.trim())
        boxLoading = false
        if (page == null) { boxFailed = true; return@LaunchedEffect }
        boxChats = page
        boxExhausted = page.size < 20
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
            query.isBlank() && boxChats.isEmpty() && !boxLoading && !boxFailed ->
                EmptyLine("no chats yet. start one with ＋ new , non-incognito chats land here.")
            else -> LazyColumn(Modifier.fillMaxSize()) {
                when {
                    boxFailed -> item(key = "box-failed") {
                        ErrorLine("the box did not answer , is it unlocked? the chat list lives there.")
                    }
                    boxLoading && boxChats.isEmpty() -> item(key = "box-loading") {
                        LoadingRow()
                    }
                    boxChats.isEmpty() -> item(key = "box-empty") {
                        EmptyLine("nothing on the box matches \"${query.trim()}\".")
                    }
                    else -> {
                        items(boxChats, key = { "box-" + it.id }) { c ->
                            BoxChatRow(c, active = c.id.toString() == activeConvId,
                                onOpen = { onOpenBoxChat(c.id) },
                                onRename = { t -> onRenameBoxChat(c.id, t) },
                                onDelete = { onDeleteBoxChat(c.id) })
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
private fun BoxChatRow(c: BoxClient.BoxChat, active: Boolean = false, onOpen: () -> Unit, onRename: (String) -> Unit = {}, onDelete: () -> Unit = {}) {
    // Inline rename: the pencil turns the title into an edit field in place , no dialog, terminal
    // style. Save sends the new title; the list refresh brings the box's truth back. The chat
    // open in CHAT right now is drawn in green so the list says where you are.
    var editing by remember { mutableStateOf(false) }
    var confirmDel by remember { mutableStateOf(false) }
    var draft by remember { mutableStateOf(c.title) }
    Row(
        Modifier.fillMaxWidth()
            .border(1.dp, if (active) TerminalDim else GhostBorder, RectangleShape)
            .background(if (active) VoidLighter else Void)
            .clickable(enabled = !editing) { onOpen() }
            .padding(14.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Column(Modifier.weight(1f)) {
            if (editing) {
                BasicTextField(draft, { draft = it }, singleLine = true,
                    textStyle = MaterialTheme.typography.bodyMedium.copy(color = TerminalGreen),
                    cursorBrush = SolidColor(TerminalGreen),
                    modifier = Modifier.fillMaxWidth())
            } else {
                Text(c.title.ifBlank { "(untitled)" }, color = if (active) TerminalGreen else GhostText,
                    style = MaterialTheme.typography.bodyMedium,
                    maxLines = 1, overflow = TextOverflow.Ellipsis)
            }
            Spacer(Modifier.height(2.dp))
            Text(java.text.SimpleDateFormat("d MMM, HH:mm", java.util.Locale.UK)
                    .format(java.util.Date(c.updatedAt)) + " · ${c.messages} msgs",
                color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        }
        if (editing) {
            Text("[ save ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable {
                    val t = draft.trim()
                    if (t.isNotEmpty() && t != c.title) onRename(t)
                    editing = false
                }.padding(start = 8.dp))
            Text("[ x ]", color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable { draft = c.title; editing = false }.padding(start = 8.dp))
        } else if (confirmDel) {
            // Two-tap delete , the confirm IS the guard, no dialog. Auto-reverts if ignored.
            LaunchedEffect(confirmDel) { kotlinx.coroutines.delay(3000); confirmDel = false }
            Text("[ delete? ]", color = Warning, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable { onDelete() }.padding(start = 8.dp))
            Text("[ no ]", color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable { confirmDel = false }.padding(start = 8.dp))
        } else {
            Text("✎", color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable { draft = c.title; editing = true }.padding(start = 8.dp, end = 6.dp))
            Text("✕", color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable { confirmDel = true }.padding(end = 6.dp))
            Text("›", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        }
    }
    Spacer(Modifier.height(8.dp))
}
