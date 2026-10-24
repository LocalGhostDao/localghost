package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.input.VisualTransformation
import androidx.compose.ui.unit.dp
import com.localghost.app.net.BoxClient
import com.localghost.app.ui.theme.*
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/**
 * MAIL , the box's own address (docs/MAIL.md). Three pages in one: the list (INBOX, SENT,
 * ARCHIVE, a search), one message (who, when, the verdicts the box reached about where it came
 * from, the text, the attachments kept and not kept, the thread, and the actions), and the
 * composer (a reply, a reply to all, a forward, or a new message). Nothing is sent until SEND is
 * tapped; the box signs it and the relay delivers it. The text of a message is shown as text,
 * never rendered: no image loads, no link opens on its own.
 */
@Composable
fun MailScreen(openId: Long = 0L, onOpened: () -> Unit = {}) {
    var folder by rememberSaveable { mutableStateOf("inbox") }
    var q by rememberSaveable { mutableStateOf("") }
    var open by rememberSaveable { mutableLongStateOf(0L) }
    // the composer: "" closed, else new / reply / replyAll / forward
    var compose by rememberSaveable { mutableStateOf("") }
    var cTo by rememberSaveable { mutableStateOf("") }
    var cCc by rememberSaveable { mutableStateOf("") }
    var cSubject by rememberSaveable { mutableStateOf("") }
    var cBody by rememberSaveable { mutableStateOf("") }
    var cReplyTo by rememberSaveable { mutableLongStateOf(0L) }
    var cForwardOf by rememberSaveable { mutableLongStateOf(0L) }
    var cQuote by rememberSaveable { mutableStateOf(true) }
    var tick by remember { mutableIntStateOf(0) }
    LaunchedEffect(openId) { if (openId > 0) { open = openId; compose = ""; onOpened() } }
    androidx.activity.compose.BackHandler(enabled = compose.isNotEmpty() || open > 0) {
        if (compose.isNotEmpty()) compose = "" else open = 0
    }
    fun startCompose(kind: String, to: List<String>, cc: List<String>, subject: String, replyTo: Long, forwardOf: Long) {
        cTo = to.joinToString(", "); cCc = cc.joinToString(", "); cSubject = subject; cBody = ""
        cReplyTo = replyTo; cForwardOf = forwardOf; cQuote = true; compose = kind
    }
    when {
        compose.isNotEmpty() -> MailCompose(kind = compose, to = cTo, cc = cCc, subject = cSubject, body = cBody, quote = cQuote,
            onTo = { cTo = it }, onCc = { cCc = it }, onSubject = { cSubject = it }, onBody = { cBody = it }, onQuote = { cQuote = it },
            replyTo = cReplyTo, forwardOf = cForwardOf,
            onDone = { sent -> compose = ""; if (sent) { folder = "sent"; open = 0; tick++ } },
            onCancel = { compose = "" })
        open > 0 -> MailMessagePage(id = open, onBack = { open = 0; tick++ },
            onReply = { r, all, own -> val (to, cc) = MailText.replyTo(r, own, all); startCompose(if (all) "replyAll" else "reply", to, cc, MailText.replySubject(r.subject), r.id, 0) },
            onForward = { r -> startCompose("forward", emptyList(), emptyList(), MailText.forwardSubject(r.subject), 0, r.id) },
            onOpenOther = { id -> open = id },
            onGone = { open = 0; tick++ })
        else -> MailList(folder = folder, q = q, tick = tick, onFolder = { folder = it }, onQ = { q = it },
            onOpen = { open = it }, onNew = { startCompose("new", emptyList(), emptyList(), "", 0, 0) }, onRefresh = { tick++ })
    }
}

@Composable
private fun MailField(value: String, hint: String, onChange: (String) -> Unit, single: Boolean = true, minLines: Int = 1) {
    BasicTextField(value, onChange, singleLine = single, minLines = minLines,
        textStyle = MaterialTheme.typography.bodySmall.copy(color = GhostText),
        cursorBrush = SolidColor(TerminalGreen),
        visualTransformation = VisualTransformation.None,
        decorationBox = { inner -> Box(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).padding(8.dp)) {
            if (value.isEmpty()) Text(hint, color = TerminalDim, style = MaterialTheme.typography.bodySmall); inner() } },
        modifier = Modifier.fillMaxWidth())
}

@Composable
private fun MailList(folder: String, q: String, tick: Int, onFolder: (String) -> Unit, onQ: (String) -> Unit,
                     onOpen: (Long) -> Unit, onNew: () -> Unit, onRefresh: () -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var page by remember { mutableStateOf<BoxClient.MailPage?>(null) }
    var asked by remember { mutableStateOf(false) }
    var more by remember { mutableStateOf(true) }
    var loading by remember { mutableStateOf(false) }
    var search by rememberSaveable { mutableStateOf(q) }
    LaunchedEffect(folder, q, tick) {
        loading = true
        page = BoxClient.mailList(ctx, folder, q)
        more = (page?.rows?.size ?: 0) >= 50
        asked = true
        loading = false
    }
    // a message still on its way: ask again every few seconds until it is delivered or not, so
    // "waiting at the relay" turns into "delivered" while the person is looking
    val onItsWay = page?.rows?.any { it.direction == "out" && it.state == "queued" } == true
    LaunchedEffect(page, onItsWay) {
        if (!onItsWay) return@LaunchedEffect
        delay(4_000)
        page = BoxClient.mailList(ctx, folder, q)
    }
    val nowS = System.currentTimeMillis() / 1000
    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp), verticalArrangement = Arrangement.spacedBy(2.dp)) {
        item {
            Spacer(Modifier.height(12.dp))
            Row(verticalAlignment = Alignment.CenterVertically) {
                SectionLabel("MAIL"); InfoButton("mail")
                Spacer(Modifier.weight(1f))
                Text("NEW ›", color = TerminalGreen, style = MaterialTheme.typography.labelMedium, modifier = Modifier.clickable { onNew() }.padding(4.dp))
            }
            Spacer(Modifier.height(6.dp))
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                MailText.folders.forEach { (key, label) ->
                    val on = key == folder
                    Text(label, color = if (on) TerminalGreen else GhostTextDim, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.border(1.dp, if (on) TerminalGreen else GhostBorder, RectangleShape).clickable { onFolder(key) }.padding(horizontal = 10.dp, vertical = 6.dp))
                }
            }
            Spacer(Modifier.height(8.dp))
            Row(verticalAlignment = Alignment.CenterVertically) {
                Box(Modifier.weight(1f)) { MailField(search, "search sender, subject, text", { search = it }) }
                Spacer(Modifier.width(8.dp))
                Text(if (q.isNotEmpty() && search == q) "CLEAR" else "FIND", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { if (q.isNotEmpty() && search == q) { search = ""; onQ("") } else onQ(search.trim()) }.padding(6.dp))
            }
            page?.counts?.let { c ->
                Spacer(Modifier.height(4.dp))
                Text(MailText.counts(c.inbox, c.unseen, c.sent, c.waiting), color = TerminalDim, style = MaterialTheme.typography.labelSmall)
            }
            Spacer(Modifier.height(6.dp))
        }
        val rows = page?.rows ?: emptyList()
        if (!asked || (loading && rows.isEmpty())) item { LoadingRow() }
        else if (page == null) item { Text("the box did not answer; pull again in a moment", color = GhostTextDim, style = MaterialTheme.typography.labelMedium, modifier = Modifier.clickable { onRefresh() }) }
        else if (rows.isEmpty()) item { Text(MailText.empty(folder, q), color = GhostTextDim, style = MaterialTheme.typography.labelMedium) }
        items(rows, key = { it.id }) { r ->
            val unread = r.direction == "in" && r.state == "new"
            Column(Modifier.fillMaxWidth().border(1.dp, if (unread) TerminalDim else GhostBorder, RectangleShape).background(VoidLighter)
                .clickable { onOpen(r.id) }.padding(12.dp)) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(MailText.who(r), color = if (unread) TerminalGreen else GhostText, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.weight(1f), maxLines = 1)
                    Text(MailText.whenShort(if (r.direction == "out") r.sentAt else r.receivedAt, nowS), color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                }
                Text(MailText.subject(r.subject), color = GhostText, style = MaterialTheme.typography.bodySmall, maxLines = 1)
                Text(MailText.second(r), color = GhostTextDim, style = MaterialTheme.typography.labelSmall, maxLines = 2)
                val marks = MailText.marks(r)
                if (marks.isNotEmpty()) Text(marks, color = if (r.spam == "high") Warning else TerminalDim, style = MaterialTheme.typography.labelSmall)
            }
        }
        if (rows.isNotEmpty() && more) item {
            Text(if (loading) "reading…" else "older ›", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable {
                    if (!loading) scope.launch {
                        loading = true
                        val next = BoxClient.mailList(ctx, folder, q, before = rows.last().id)
                        if (next != null) { page = BoxClient.MailPage(rows + next.rows, next.counts); more = next.rows.size >= 50 }
                        loading = false
                    }
                }.padding(vertical = 10.dp))
        }
        item { Spacer(Modifier.height(24.dp)) }
    }
}

@Composable
private fun MailMessagePage(id: Long, onBack: () -> Unit, onReply: (MailText.Row, Boolean, String) -> Unit, onForward: (MailText.Row) -> Unit,
                            onOpenOther: (Long) -> Unit, onGone: () -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var m by remember(id) { mutableStateOf<BoxClient.MailMessage?>(null) }
    var asked by remember(id) { mutableStateOf(false) }
    var own by remember { mutableStateOf("") }
    var showVerdicts by rememberSaveable(id) { mutableStateOf(false) }
    var armed by remember(id) { mutableStateOf(false) }
    var note by remember(id) { mutableStateOf("") }
    LaunchedEffect(id) {
        m = BoxClient.mailMessage(ctx, id)
        asked = true
        if (own.isEmpty()) own = BoxClient.mailStatus(ctx)?.settings?.address ?: ""
    }
    val msg = m
    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
        item {
            Spacer(Modifier.height(12.dp))
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("‹ MAIL", color = TerminalGreen, style = MaterialTheme.typography.labelMedium, modifier = Modifier.clickable { onBack() }.padding(end = 8.dp))
                Spacer(Modifier.weight(1f))
                InfoButton("mail")
            }
        }
        if (msg == null) {
            item { if (!asked) LoadingRow() else Text("this message is gone, or the box did not answer", color = GhostTextDim, style = MaterialTheme.typography.labelMedium, modifier = Modifier.clickable { onGone() }) }
            return@LazyColumn
        }
        val r = msg.row
        item {
            Column(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).background(VoidLighter).padding(14.dp)) {
                Text(MailText.subject(r.subject), color = GhostText, style = MaterialTheme.typography.titleMedium)
                Spacer(Modifier.height(6.dp))
                if (r.direction == "in") {
                    Text("from " + (if (r.fromName.isNotBlank()) r.fromName + " <" + r.from + ">" else r.from.ifBlank { "an unreadable sender" }), color = GhostText, style = MaterialTheme.typography.bodySmall)
                } else {
                    Text("from " + r.from, color = GhostText, style = MaterialTheme.typography.bodySmall)
                }
                if (r.to.isNotEmpty()) Text("to " + r.to.joinToString(", "), color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                if (r.cc.isNotEmpty()) Text("cc " + r.cc.joinToString(", "), color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                if (r.replyTo.isNotBlank() && r.replyTo != r.from) Text("replies go to " + r.replyTo, color = Warning, style = MaterialTheme.typography.labelMedium)
                Text(MailText.whenLong(if (r.sentAt > 0) r.sentAt else r.receivedAt), color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                if (r.direction == "out") Text(MailText.second(r), color = if (r.state == "bounced" || r.state == "failed") Warning else GhostTextDim, style = MaterialTheme.typography.labelMedium)
            }
        }
        // the verdicts: one line, the rest on a tap
        if (r.direction == "in") item {
            val v = msg.verdicts
            Column(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).clickable { showVerdicts = !showVerdicts }.padding(12.dp)) {
                if (v == null) Text("not judged (an older box)", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                else {
                    Text(MailText.spamLine(v), color = when (v.spam) { "high" -> Warning; "medium" -> GhostText; else -> TerminalGreen }, style = MaterialTheme.typography.labelMedium)
                    if (showVerdicts) {
                        Spacer(Modifier.height(6.dp))
                        MailText.verdictLines(v).forEach { Text("· " + it, color = GhostTextDim, style = MaterialTheme.typography.labelSmall) }
                    } else Text("tap for what the box checked", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                }
            }
        }
        // the text
        item {
            Column(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).padding(14.dp)) {
                if (msg.body.isBlank()) Text("(no text)", color = TerminalDim, style = MaterialTheme.typography.bodySmall)
                else SelectionContainer { Text(msg.body, color = GhostText, style = MaterialTheme.typography.bodySmall) }
                if (msg.bodyKind == "html-reduced") { Spacer(Modifier.height(6.dp)); Text("the message was HTML; this is its text, nothing loaded", color = TerminalDim, style = MaterialTheme.typography.labelSmall) }
            }
        }
        // the attachments
        if (msg.attachments.isNotEmpty()) item {
            Column(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).padding(12.dp)) {
                Text("ATTACHMENTS", color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
                msg.attachments.forEach { a ->
                    Text(MailText.attachmentLine(a), color = if (a.kept) GhostText else TerminalDim, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.fillMaxWidth().clickable(enabled = a.kept) {
                            scope.launch {
                                note = "saving " + a.name + "…"
                                val b = BoxClient.mailAttachment(ctx, a.id)
                                note = if (b == null) "the box did not hand it over" else MailSave.toDownloads(ctx, a.name, a.mime, b)
                            }
                        }.padding(vertical = 6.dp))
                }
                Text("a tap saves a file to Downloads; nothing is opened by the app", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                if (note.isNotEmpty()) Text(note, color = GhostTextDim, style = MaterialTheme.typography.labelSmall)
            }
        }
        // the actions
        item {
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                GhostButton("REPLY", { onReply(r, false, own) }, modifier = Modifier.weight(1f))
                if (r.to.size + r.cc.size > 1) GhostButton("ALL", { onReply(r, true, own) }, modifier = Modifier.weight(0.7f))
                GhostButton("FORWARD", { onForward(r) }, modifier = Modifier.weight(1f))
            }
            Spacer(Modifier.height(8.dp))
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                if (r.direction == "in") {
                    val archived = r.state == "archived"
                    GhostButton(if (archived) "UNARCHIVE" else "ARCHIVE", {
                        scope.launch { if (BoxClient.mailState(ctx, r.id, if (archived) "seen" else "archived")) { m = msg.copy(row = r.copy(state = if (archived) "seen" else "archived")) } }
                    }, modifier = Modifier.weight(1f))
                    GhostButton("UNREAD", { scope.launch { if (BoxClient.mailState(ctx, r.id, "new")) onBack() } }, modifier = Modifier.weight(1f))
                }
                GhostButton(if (armed) "DELETE, SURE" else "DELETE", {
                    if (!armed) armed = true else scope.launch { if (BoxClient.mailDelete(ctx, r.id)) onGone() else note = "the box did not delete it" }
                }, modifier = Modifier.weight(1f))
            }
            if (armed) Text("a second tap deletes the message, its attachments and the sealed original", color = Warning, style = MaterialTheme.typography.labelSmall)
        }
        // the thread
        val others = msg.thread.filter { it.id != r.id }
        if (others.isNotEmpty()) {
            item { Text("THE THREAD", color = TerminalGreen, style = MaterialTheme.typography.labelMedium) }
            items(others, key = { "t" + it.id }) { t ->
                Column(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).clickable { onOpenOther(t.id) }.padding(10.dp)) {
                    Row { Text(MailText.who(t), color = GhostText, style = MaterialTheme.typography.labelMedium, modifier = Modifier.weight(1f)); Text(MailText.whenShort(if (t.direction == "out") t.sentAt else t.receivedAt, System.currentTimeMillis() / 1000), color = TerminalDim, style = MaterialTheme.typography.labelSmall) }
                    Text(MailText.second(t), color = GhostTextDim, style = MaterialTheme.typography.labelSmall, maxLines = 2)
                }
            }
        }
        item {
            Text("size " + MailText.bytes(msg.rawSize) + " · the sealed original stays on the box", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
            Spacer(Modifier.height(24.dp))
        }
    }
}

@Composable
private fun MailCompose(kind: String, to: String, cc: String, subject: String, body: String, quote: Boolean,
                        onTo: (String) -> Unit, onCc: (String) -> Unit, onSubject: (String) -> Unit, onBody: (String) -> Unit, onQuote: (Boolean) -> Unit,
                        replyTo: Long, forwardOf: Long, onDone: (Boolean) -> Unit, onCancel: () -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var busy by remember { mutableStateOf(false) }
    var result by remember { mutableStateOf("") }
    val title = when (kind) { "reply" -> "REPLY"; "replyAll" -> "REPLY TO ALL"; "forward" -> "FORWARD"; else -> "NEW MESSAGE" }
    val split = { s: String -> s.split(',', ';', ' ').map { it.trim() }.filter { it.isNotEmpty() } }
    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp), verticalArrangement = Arrangement.spacedBy(8.dp)) {
        item {
            Spacer(Modifier.height(12.dp))
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("‹ BACK", color = TerminalGreen, style = MaterialTheme.typography.labelMedium, modifier = Modifier.clickable { onCancel() }.padding(end = 8.dp))
                Spacer(Modifier.weight(1f))
                SectionLabel(title)
            }
            Spacer(Modifier.height(6.dp))
        }
        item { MailField(to, "to (addresses, comma between)", onTo) }
        item { MailField(cc, "cc", onCc) }
        item { MailField(subject, "subject", onSubject) }
        item { MailField(body, "your text", onBody, single = false, minLines = 8) }
        if (kind == "reply" || kind == "replyAll") item {
            Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.clickable { onQuote(!quote) }) {
                Text(if (quote) "☑" else "☐", color = TerminalGreen, style = MaterialTheme.typography.bodyMedium)
                Spacer(Modifier.width(8.dp))
                Text("quote the message under your text", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            }
        }
        if (kind == "forward") item { Text("the forwarded message and its kept attachments go under your text", color = TerminalDim, style = MaterialTheme.typography.labelSmall) }
        item {
            val can = split(to).isNotEmpty() && !busy
            Row(horizontalArrangement = Arrangement.spacedBy(8.dp)) {
                GhostButton(if (busy) "SENDING…" else "SEND", {
                    if (can) scope.launch {
                        busy = true
                        val why = BoxClient.mailSend(ctx, split(to), split(cc), subject, body, replyTo, forwardOf, quote)
                        busy = false
                        if (why.isEmpty()) { result = ""; onDone(true) } else result = why
                    }
                }, modifier = Modifier.weight(1f), enabled = can || busy)
                GhostButton("DISCARD", onCancel, modifier = Modifier.weight(1f))
            }
            if (result.isNotEmpty()) Text(result, color = Warning, style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(4.dp))
            Text("the box signs it with its own key and hands it to the relay, which delivers it; SENT shows when it landed or bounced", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
            Spacer(Modifier.height(24.dp))
        }
    }
}
