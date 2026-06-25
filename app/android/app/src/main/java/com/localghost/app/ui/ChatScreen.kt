package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
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
    brainLabel: String,                    // what's answering now, e.g. "the box" / "phone · Gemma 4"
    brainIsBox: Boolean,                   // true = box engine, false = on-phone
    phoneModels: List<Triple<String,String,Boolean>>, // (id, name, installed) box-served models
    onPickBox: () -> Unit,                 // use the box (clear force-local)
    onPickPhoneModel: (String) -> Unit,    // force-local with this model id
    onGetModel: () -> Unit,                // no models installed -> go to MODELS
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

        // composer: one rounded container — pill on top, then + / field / send
        val canSend = input.isNotBlank() || pendingAttachments.isNotEmpty()
        Column(
            Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 10.dp)
                .border(1.dp, GhostBorder, RoundedCornerShape(24.dp))
                .background(VoidLighter, RoundedCornerShape(24.dp))
                .padding(horizontal = 6.dp, vertical = 6.dp),
        ) {
            // model pill, inside the composer
            ModelPill(brainLabel, brainIsBox, phoneModels, onPickBox, onPickPhoneModel, onGetModel)

            Row(verticalAlignment = Alignment.Bottom) {
                // add-to-chat (circular)
                Box(
                    Modifier.size(40.dp).clip(CircleShape).clickable { onAddToChat() },
                    contentAlignment = Alignment.Center,
                ) { Text("+", color = TerminalGreen, fontSize = 22.sp) }

                BasicTextField(
                    value = input, onValueChange = { input = it },
                    modifier = Modifier.weight(1f).padding(horizontal = 6.dp, vertical = 10.dp),
                    textStyle = MaterialTheme.typography.bodyMedium.copy(color = GhostText),
                    cursorBrush = SolidColor(TerminalGreen),
                    maxLines = 5,
                    keyboardOptions = KeyboardOptions(imeAction = ImeAction.Send),
                    keyboardActions = KeyboardActions(onSend = {
                        if (canSend) { onSend(input.trim()); input = "" } }),
                    decorationBox = { inner ->
                        if (input.isEmpty())
                            Text("query your index…", color = GhostTextDim,
                                style = MaterialTheme.typography.bodyMedium)
                        inner()
                    },
                )

                // send / stop (circular, filled when actionable)
                val active = streaming || canSend
                Box(
                    Modifier.size(40.dp).clip(CircleShape)
                        .background(if (active) TerminalGreen else GhostBorder)
                        .clickable(enabled = active) {
                            if (streaming) onStop()
                            else if (canSend) { onSend(input.trim()); input = "" }
                        },
                    contentAlignment = Alignment.Center,
                ) {
                    Text(if (streaming) "■" else "↑",
                        color = Void, fontSize = if (streaming) 14.sp else 20.sp)
                }
            }
        }
    }
}

@Composable
private fun EmptyState(modifier: Modifier) {
    Column(modifier.fillMaxWidth().padding(horizontal = 24.dp).padding(top = 40.dp),
        verticalArrangement = Arrangement.Top) {
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
@Composable
private fun MessageBubble(msg: Message) {
    val isUser = msg.role == Message.Role.USER
    var memOpen by remember { mutableStateOf(false) }
    Column(Modifier.fillMaxWidth(), horizontalAlignment = if (isUser) Alignment.End else Alignment.Start) {
        // injected memories — collapsed behind a green + toggle; white when expanded
        if (msg.memoriesUsed.isNotEmpty()) {
            Row(verticalAlignment = Alignment.CenterVertically,
                modifier = Modifier.clickable { memOpen = !memOpen }.padding(bottom = 4.dp)) {
                Text(if (memOpen) "−" else "+", color = TerminalGreen, fontSize = 13.sp,
                    modifier = Modifier.padding(end = 6.dp))
                Text("${msg.memoriesUsed.size} memories", color = TerminalGreen,
                    style = MaterialTheme.typography.labelMedium)
            }
            if (memOpen) {
                Column(Modifier.padding(start = 18.dp, bottom = 6.dp)) {
                    msg.memoriesUsed.forEach {
                        Text("◇ $it", color = GhostText,
                            style = MaterialTheme.typography.labelMedium,
                            modifier = Modifier.padding(vertical = 1.dp))
                    }
                }
            }
        }
        if (msg.attachments.isNotEmpty()) {
            Text("⊹ attached: ${msg.attachments.joinToString(" · ") { it.name }}", color = TerminalDim,
                style = MaterialTheme.typography.labelMedium, modifier = Modifier.padding(bottom = 4.dp))
        }
        Box(Modifier
            .background(if (isUser) VoidLighter else Void)
            .border(1.dp, if (isUser) GhostBorder else GhostBorder, RectangleShape)
            .padding(12.dp)) {
            // response body in soft grey (easy to read); user echo also grey
            Text(msg.text, color = GhostText,
                style = MaterialTheme.typography.bodyMedium)
        }
    }
}


@Composable
private fun ModelPill(
    label: String,
    isBox: Boolean,
    phoneModels: List<Triple<String, String, Boolean>>,
    onPickBox: () -> Unit,
    onPickPhoneModel: (String) -> Unit,
    onGetModel: () -> Unit,
) {
    var open by remember { mutableStateOf(false) }
    Box(Modifier.padding(start = 6.dp, bottom = 2.dp)) {
        Row(
            Modifier.clip(RoundedCornerShape(14.dp))
                .border(1.dp, if (isBox) TerminalDim else GhostBorder, RoundedCornerShape(14.dp))
                .background(Void, RoundedCornerShape(14.dp))
                .clickable { open = true }
                .padding(horizontal = 12.dp, vertical = 6.dp)
                .widthIn(max = 240.dp),
            verticalAlignment = Alignment.CenterVertically,
        ) {
            Text(if (isBox) "▪" else "◈", color = if (isBox) TerminalGreen else Warning,
                fontSize = 11.sp)
            Spacer(Modifier.width(6.dp))
            Text(label, color = GhostText, style = MaterialTheme.typography.labelMedium,
                maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f, fill = false))
            Spacer(Modifier.width(6.dp))
            Text(if (open) "▴" else "▾", color = GhostTextDim, fontSize = 11.sp)
        }
        DropdownMenu(expanded = open, onDismissRequest = { open = false },
            modifier = Modifier.background(Void).widthIn(min = 240.dp)) {
            // box engine
            DropdownLabel("ON THE BOX")
            DropdownMenuItem(
                text = {
                    Column {
                        Text("the box", color = if (isBox) TerminalGreen else GhostText,
                            style = MaterialTheme.typography.bodyMedium)
                        Text("synthd · full life-index", color = GhostTextDim,
                            style = MaterialTheme.typography.labelMedium)
                    }
                },
                leadingIcon = { Text("▪", color = TerminalGreen, fontSize = 12.sp) },
                onClick = { open = false; onPickBox() })

            // on-phone models
            DropdownLabel("ON THIS PHONE")
            if (phoneModels.isEmpty()) {
                DropdownMenuItem(
                    text = { Text("get an on-phone model", color = GhostTextDim,
                        style = MaterialTheme.typography.bodyMedium) },
                    leadingIcon = { Text("↓", color = GhostTextDim, fontSize = 12.sp) },
                    onClick = { open = false; onGetModel() })
            } else {
                phoneModels.forEach { (id, name, installed) ->
                    DropdownMenuItem(
                        text = {
                            Column {
                                Text(name, color = GhostText, style = MaterialTheme.typography.bodyMedium,
                                    maxLines = 1, overflow = TextOverflow.Ellipsis)
                                Text(if (installed) "downloaded" else "tap to download",
                                    color = if (installed) TerminalDim else GhostTextDim,
                                    style = MaterialTheme.typography.labelMedium)
                            }
                        },
                        leadingIcon = {
                            Text(if (installed) "◈" else "↓",
                                color = if (installed) Warning else GhostTextDim, fontSize = 12.sp)
                        },
                        onClick = {
                            open = false
                            if (installed) onPickPhoneModel(id) else onGetModel()
                        })
                }
            }
        }
    }
}

@Composable
private fun DropdownLabel(text: String) {
    Text(text, color = TerminalDim, style = MaterialTheme.typography.labelMedium,
        modifier = Modifier.padding(start = 12.dp, top = 8.dp, bottom = 2.dp))
}
