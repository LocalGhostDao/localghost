package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.unit.dp
import com.localghost.app.chat.Attachment
import com.localghost.app.chat.Message
import com.localghost.app.ui.theme.*

@Composable
fun ChatScreen(
    messages: List<Message>,
    streaming: Boolean,
    localModeActive: Boolean,
    pendingAttachments: List<Attachment>,
    onSend: (String) -> Unit,
    onStop: () -> Unit,
    onAddToChat: () -> Unit,
    onClearAttachment: (Attachment) -> Unit,
) {
    var input by remember { mutableStateOf("") }
    val listState = rememberLazyListState()
    LaunchedEffect(messages.size, streaming) {
        if (messages.isNotEmpty()) listState.animateScrollToItem(messages.size - 1)
    }
    Column(Modifier.fillMaxSize().imePadding()) {
        if (localModeActive) {
            Text("◇ no box — on-phone model, limited context",
                color = Warning, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.fillMaxWidth().padding(horizontal = 16.dp, vertical = 6.dp))
        }
        if (messages.isEmpty()) {
            EmptyState(Modifier.weight(1f))
        } else {
            LazyColumn(
                state = listState,
                modifier = Modifier.weight(1f).fillMaxWidth().padding(horizontal = 16.dp),
                verticalArrangement = Arrangement.spacedBy(12.dp),
            ) {
                item { Spacer(Modifier.height(8.dp)) }
                items(messages) { MessageBubble(it) }
                if (streaming) item {
                    Text("◇ retrieving from index…", color = TerminalDim,
                        style = MaterialTheme.typography.labelMedium)
                }
            }
        }

        // pending attachments (to be sent + ingested with the next message)
        if (pendingAttachments.isNotEmpty()) {
            Column(Modifier.fillMaxWidth().padding(horizontal = 12.dp)) {
                pendingAttachments.forEach { a ->
                    Row(Modifier.fillMaxWidth().padding(vertical = 2.dp),
                        verticalAlignment = Alignment.CenterVertically) {
                        Text(if (a.kind == Attachment.Kind.IMAGE) "▣" else "◍",
                            color = TerminalGreen, style = MaterialTheme.typography.bodyMedium)
                        Spacer(Modifier.width(8.dp))
                        Text(a.name, color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
                            modifier = Modifier.weight(1f))
                        Text("[ x ]", color = Warning, style = MaterialTheme.typography.labelMedium,
                            modifier = Modifier.clickable { onClearAttachment(a) }.padding(start = 8.dp))
                    }
                }
                Text("attached items are added to your index (deduped against camera sync)",
                    color = TerminalDim, style = MaterialTheme.typography.labelMedium)
            }
        }

        Row(Modifier.fillMaxWidth().padding(12.dp), verticalAlignment = Alignment.CenterVertically) {
            // add-to-chat
            Text("+", color = TerminalGreen, style = MaterialTheme.typography.titleLarge,
                modifier = Modifier.clickable { onAddToChat() }
                    .border(1.dp, GhostBorder, RectangleShape)
                    .padding(horizontal = 12.dp, vertical = 6.dp))
            Spacer(Modifier.width(8.dp))
            OutlinedTextField(
                value = input, onValueChange = { input = it },
                modifier = Modifier.weight(1f),
                placeholder = { Text("query your index…", color = GhostTextDim) },
                maxLines = 4,
                keyboardOptions = KeyboardOptions(imeAction = ImeAction.Send),
                keyboardActions = KeyboardActions(onSend = {
                    if (input.isNotBlank() || pendingAttachments.isNotEmpty()) { onSend(input.trim()); input = "" } }),
                colors = OutlinedTextFieldDefaults.colors(
                    focusedTextColor = GhostText, unfocusedTextColor = GhostText,
                    cursorColor = TerminalGreen,
                    focusedBorderColor = TerminalGreen, unfocusedBorderColor = GhostBorder,
                    focusedContainerColor = VoidLighter, unfocusedContainerColor = VoidLighter),
            )
            Spacer(Modifier.width(8.dp))
            if (streaming) GhostButton("STOP", onStop)
            else GhostButton("SEND", {
                if (input.isNotBlank() || pendingAttachments.isNotEmpty()) { onSend(input.trim()); input = "" }
            }, enabled = input.isNotBlank() || pendingAttachments.isNotEmpty())
        }
    }
}

@Composable
private fun EmptyState(modifier: Modifier) {
    Column(modifier.fillMaxWidth().padding(32.dp),
        verticalArrangement = Arrangement.Center) {
        SectionLabel("GHOST.SYNTHD")
        Spacer(Modifier.height(12.dp))
        Text("Local model, on the box.", color = TerminalGreen,
            style = glow(MaterialTheme.typography.titleMedium))
        Spacer(Modifier.height(10.dp))
        Text("Runs on your box. Retrieves from the index built out of your synced photos, " +
             "videos and voice notes, then answers from that. The prompt and the index never " +
             "leave the box.",
             color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
        Spacer(Modifier.height(20.dp))
        listOf("what did I do in Rome?", "summarise last week", "when was I last diving?").forEach {
            Text("> $it", color = TerminalDim, style = MaterialTheme.typography.bodyMedium,
                modifier = Modifier.padding(vertical = 4.dp))
        }
    }
}

@Composable
private fun MessageBubble(msg: Message) {
    val isUser = msg.role == Message.Role.USER
    Column(Modifier.fillMaxWidth(), horizontalAlignment = if (isUser) Alignment.End else Alignment.Start) {
        if (msg.memoriesUsed.isNotEmpty()) {
            Text("◇ retrieved: ${msg.memoriesUsed.joinToString(" · ")}", color = TerminalDim,
                style = MaterialTheme.typography.labelMedium, modifier = Modifier.padding(bottom = 4.dp))
        }
        if (msg.attachments.isNotEmpty()) {
            Text("⊹ attached: ${msg.attachments.joinToString(" · ") { it.name }}", color = TerminalDim,
                style = MaterialTheme.typography.labelMedium, modifier = Modifier.padding(bottom = 4.dp))
        }
        Box(Modifier
            .background(if (isUser) VoidLighter else Void)
            .border(1.dp, if (isUser) GhostBorder else TerminalDim, RectangleShape)
            .padding(12.dp)) {
            Text(msg.text, color = if (isUser) GhostText else TerminalGreen,
                style = MaterialTheme.typography.bodyMedium)
        }
    }
}
