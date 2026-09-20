package com.localghost.app.ui

import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.itemsIndexed
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.ui.platform.LocalClipboardManager
import androidx.compose.ui.text.AnnotatedString
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
    incognito: Boolean = false,
    onToggleIncognito: () -> Unit = {},
    genStats: String = "",                 // debug-mode tok/s line ("" = hidden)
) {
    var input by remember { mutableStateOf("") }
    val listState = rememberLazyListState()
    // FOLLOW THE TAIL, unless the person takes the wheel. followTail flips false the moment a user
    // drag leaves the bottom, and back true when they return there , so reading something upstream
    // mid-generation stays exactly where they put it, and letting go at the bottom resumes the ride.
    val atBottom by remember {
        derivedStateOf {
            val info = listState.layoutInfo
            val last = info.visibleItemsInfo.lastOrNull()
            last == null || last.index >= info.totalItemsCount - 1
        }
    }
    var followTail by remember { mutableStateOf(true) }
    LaunchedEffect(listState.isScrollInProgress, atBottom) {
        if (listState.isScrollInProgress) followTail = atBottom // user is driving: follow only if they drove TO the bottom
        else if (atBottom) followTail = true                    // settled at the bottom: resume following
    }
    // Keyed on CONTENT growth, not just message count , the old size-only key meant the view sat
    // still while a long reply streamed past the fold. Tail text + reasoning lengths change per
    // chunk, so this re-fires exactly when there is new tail to show.
    val tailLen = messages.lastOrNull()?.text?.length ?: 0
    val tailThink = messages.lastOrNull()?.reasoning?.length ?: 0
    LaunchedEffect(messages.size, tailLen, tailThink, streaming) {
        if (followTail && messages.isNotEmpty()) {
            listState.scrollToItem(listState.layoutInfo.totalItemsCount.coerceAtLeast(1) - 1)
        }
    }
    // ime MINUS navigationBars, not plain imePadding: the shell already pads the nav bar for every
    // screen, and the IME inset spans the nav bar's space , plain imePadding stacked the two, which
    // is why the input box floated a nav-bar-and-change above the keyboard while typing. The
    // exclusion makes the two layers sum to exactly keyboard height; keyboard closed, it is zero.
    Column(Modifier.fillMaxSize().windowInsetsPadding(WindowInsets.ime.exclude(WindowInsets.navigationBars))) {
        if (localModeActive) {
            Text("◇ no box, on-phone model, limited context",
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
                itemsIndexed(messages) { i, m ->
                    MessageBubble(m, selectable = !(streaming && i == messages.lastIndex))
                }
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
                    if (genStats.isNotEmpty()) {
            Text(genStats, color = TerminalDim, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.padding(horizontal = 16.dp))
        }
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

        // composer: one rounded container, pill on top, then + / field / send
        // Not sendable while a reply is streaming: the round button already morphs into STOP, but the
        // keyboard's IME send action goes through canSend too , without this it could fire a second
        // generation mid-stream, interleaving two replies into the transcript.
        // Incognito: this conversation never touches the box's chat tables. The state is visible ,
        // an invisible privacy mode you cannot verify is worse than none.
        Row(Modifier.fillMaxWidth().padding(bottom = 6.dp)) {
            Text(if (incognito) "◉ INCOGNITO , not saved" else "○ incognito off , conversation saved on the box",
                color = if (incognito) Warning else GhostTextDim,
                style = MaterialTheme.typography.bodySmall,
                modifier = Modifier.clickable { onToggleIncognito() })
        }
        // Web: the PHONE looks the question up and hands the findings to the box, which itself
        // never reaches the internet. Off by default; auto only for questions that look like they
        // need the outside world; on for every question. Tap to cycle. Visible, like incognito ,
        // a search that leaves the phone for a third party must never be a surprise.
        run {
            val wctx = androidx.compose.ui.platform.LocalContext.current
            var web by remember { mutableStateOf(com.localghost.app.settings.AppSettings.webMode(wctx)) }
            Row(Modifier.fillMaxWidth().padding(bottom = 6.dp)) {
                Text(when (web) {
                    "on" -> "◉ WEB on , this phone searches for every question (DuckDuckGo, and weather, rates and Wikipedia when asked) and hands the findings to the box"
                    "auto" -> "◐ web auto , searched on this phone when a question needs the outside world: news, prices, weather, who is, how much"
                    else -> "○ web off , the box answers from your archive alone"
                }, color = if (web == "on") TerminalGreen else GhostTextDim,
                    style = MaterialTheme.typography.bodySmall,
                    modifier = Modifier.clickable {
                        web = when (web) { "off" -> "auto"; "auto" -> "on"; else -> "off" }
                        com.localghost.app.settings.AppSettings.setWebMode(wctx, web)
                    })
            }
        }
        val canSend = !streaming && (input.isNotBlank() || pendingAttachments.isNotEmpty())
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
private fun MessageBubble(msg: Message, selectable: Boolean = true) {
    val isUser = msg.role == Message.Role.USER
    var memOpen by remember { mutableStateOf(false) }
    var thinkOpen by remember { mutableStateOf(false) }
    Column(Modifier.fillMaxWidth(), horizontalAlignment = if (isUser) Alignment.End else Alignment.Start) {
        // injected memories, collapsed behind a green + toggle; white when expanded
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
        // What the phone found on the web for this turn, numbered as the box saw them: a "[2]" in
        // the answer is row 2 here, and a tap opens it. Weather, rates and summaries carry a glyph
        // so a figure reads as a figure, not as a page somebody wrote.
        if (msg.web.isNotEmpty()) {
            var webOpen by remember { mutableStateOf(false) }
            val wctx = androidx.compose.ui.platform.LocalContext.current
            Row(verticalAlignment = Alignment.CenterVertically,
                modifier = Modifier.clickable { webOpen = !webOpen }.padding(bottom = 4.dp)) {
                Text(if (webOpen) "−" else "+", color = TerminalGreen, fontSize = 13.sp,
                    modifier = Modifier.padding(end = 6.dp))
                Text("${msg.web.size} from the web · searched on this phone", color = TerminalGreen,
                    style = MaterialTheme.typography.labelMedium)
            }
            if (webOpen) {
                Column(Modifier.padding(start = 18.dp, bottom = 6.dp)) {
                    msg.web.forEachIndexed { i, h ->
                        val glyph = when (h.kind) { "weather" -> "☂"; "rate" -> "€"; "summary" -> "W"; else -> "◇" }
                        val meta = listOfNotNull(h.site.ifEmpty { null }, h.published.take(10).ifEmpty { null }).joinToString(" · ")
                        Column(Modifier.fillMaxWidth().clickable {
                            runCatching { wctx.startActivity(android.content.Intent(android.content.Intent.ACTION_VIEW, android.net.Uri.parse(h.url))) }
                        }.padding(vertical = 3.dp)) {
                            Text("[${i + 1}] $glyph ${h.title}", color = GhostText, style = MaterialTheme.typography.labelMedium)
                            if (meta.isNotEmpty()) Text(meta, color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                            val body = h.excerpt.ifEmpty { h.snippet }
                            if (body.isNotEmpty()) Text(body.take(220) + (if (body.length > 220) "…" else ""), color = GhostTextDim, style = MaterialTheme.typography.labelSmall)
                        }
                    }
                }
            }
        }
        if (msg.attachments.isNotEmpty()) {
            Text("⊹ attached  ${msg.attachments.joinToString(" · ") { it.name }}", color = TerminalDim,
                style = MaterialTheme.typography.labelMedium, modifier = Modifier.padding(bottom = 4.dp))
        }
        // The model's reasoning, collapsed behind a toggle. It STREAMS while expanded (the message
        // object is replaced per chunk, so this just recomposes), doubles as the progress indicator
        // before the first answer token, and stays readable after the answer lands.
        if (msg.reasoning.isNotEmpty()) {
            Row(verticalAlignment = Alignment.CenterVertically,
                modifier = Modifier.clickable { thinkOpen = !thinkOpen }
                    .padding(top = 2.dp, bottom = 6.dp, end = 12.dp)) {
                Text(if (thinkOpen) "−" else "+", color = TerminalDim, fontSize = 13.sp,
                    modifier = Modifier.padding(end = 6.dp))
                Text("thinking… (${msg.reasoning.length})", color = TerminalDim,
                    style = MaterialTheme.typography.labelMedium)
            }
            if (thinkOpen) {
                Text(msg.reasoning, color = GhostTextDim,
                    style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier
                        .padding(start = 18.dp, bottom = 6.dp)
                        .animateContentSize()
                        .background(VoidLighter)
                        .border(1.dp, GhostBorder, RectangleShape)
                        .padding(8.dp))
            }
        }
        // No bubble around nothing: while the model is still reasoning the answer text is empty,
        // and an empty bordered box under the thinking row read as a rendering bug.
        if (msg.text.isNotEmpty()) {
            Box(Modifier
                .background(if (isUser) VoidLighter else Void)
                .border(1.dp, if (isUser) GhostBorder else GhostBorder, RectangleShape)
                .padding(12.dp)) {
                // GHOST replies render as markdown; the USER echo stays LITERAL. Selection wraps
                // COMPLETED messages only: a SelectionContainer over a multi-Text tree recomposing
                // per token inside a LazyColumn is exactly the fragile construct that blanked the
                // stream , and selecting mid-stream is pointless anyway. The message becomes
                // selectable the moment streaming ends.
                val body: @Composable () -> Unit = {
                    if (isUser) {
                        Text(msg.text, color = GhostText,
                            style = MaterialTheme.typography.bodyMedium)
                    } else {
                        MarkdownText(msg.text)
                    }
                }
                if (selectable) SelectionContainer { body() } else body()
            }
            // Whole-message copy , selection handles fragments, this grabs the FULL raw text
            // (markdown markers included, so a copied reply pastes as valid markdown elsewhere).
            // LocalClipboardManager is deprecated in favour of the suspend-based LocalClipboard;
            // the replacement needs a coroutine per copy for no behavioural gain here, so the old
            // one stays, deliberately and visibly, until the copy path is reworked.
            @Suppress("DEPRECATION")
            val clipboard = LocalClipboardManager.current
            Text("[ copy ]", color = TerminalDim, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier
                    .padding(top = 2.dp)
                    .clickable { clipboard.setText(AnnotatedString(msg.text)) })
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
