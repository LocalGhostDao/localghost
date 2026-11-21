package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.text.selection.SelectionContainer
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import com.localghost.app.net.BoxClient
import com.localghost.app.ui.theme.*
import kotlinx.coroutines.delay
import kotlinx.coroutines.launch

/**
 * SHADOW , the box checking itself, and what it has named. Four pages in one screen: the fleet
 * (every detector, which model reads cold, what was named lately, what was changed), one
 * detector (what it reads, its threshold inside its bounds, a mute of three days at most, its
 * history), the catalogue (thirty patterns, built or not, each with why), and one chat's audit
 * trail (what the box asked, what it had been given, the arbiter's record, and "read it cold
 * now").
 *
 * Nothing here turns a detector off: there is no such thing on the box. [open] is where a
 * notification's tap lands: "chat:<id>", a detector's id, or "" for the fleet.
 */
@Composable
fun ShadowScreen(open: String = "", onOpened: () -> Unit = {}, onOpenChat: (Long) -> Unit = {}) {
    val ctx = LocalContext.current
    // "" the fleet; "d:<id>" a detector; "c:<chat>" a chat's trail; "catalogue"
    var page by rememberSaveable { mutableStateOf("") }
    var fleet by remember { mutableStateOf<ShadowText.Fleet?>(null) }
    var failed by remember { mutableStateOf(false) }
    var tick by remember { mutableIntStateOf(0) }
    LaunchedEffect(open) {
        if (open.isNotEmpty()) {
            page = if (ShadowText.chatOf(open) > 0) "c:" + ShadowText.chatOf(open) else "d:$open"
            onOpened()
        }
    }
    LaunchedEffect(tick) {
        val f = BoxClient.shadowFleet(ctx)
        failed = f == null
        if (f != null) fleet = f
    }
    androidx.activity.compose.BackHandler(enabled = page.isNotEmpty()) { page = "" }
    val f = fleet
    when {
        page.startsWith("c:") -> ShadowTrailPage(page.removePrefix("c:").toLongOrNull() ?: 0L, onBack = { page = "" }, onOpenChat = onOpenChat)
        f == null -> Column(Modifier.fillMaxSize().padding(horizontal = 20.dp)) {
            Spacer(Modifier.height(12.dp))
            ShadowTitle()
            if (failed) ErrorLine("the box did not answer , is it unlocked?") else LoadingRow()
        }
        page == "catalogue" -> ShadowCatalogue(f, onBack = { page = "" }, onDetector = { page = "d:$it" })
        page.startsWith("d:") -> {
            val d = f.detectors.firstOrNull { it.id == page.removePrefix("d:") }
            // a detector this box does not have (a link from a newer one): the fleet
            if (d == null) LaunchedEffect(page) { page = "" }
            else ShadowDetectorPage(f, d, onBack = { page = "" }, onChanged = { tick++ }, onChat = { page = "c:$it" })
        }
        else -> ShadowFleet(f, onDetector = { page = "d:$it" }, onCatalogue = { page = "catalogue" }, onChat = { page = "c:$it" })
    }
}

@Composable
private fun ShadowTitle() {
    Row(verticalAlignment = Alignment.CenterVertically) {
        Text("SHADOW", color = GhostText, style = MaterialTheme.typography.titleLarge)
        InfoButton("shadow")
    }
}

@Composable
private fun ShadowBack(onBack: () -> Unit) {
    Spacer(Modifier.height(12.dp))
    Text("‹ shadow", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
        modifier = Modifier.clickable { onBack() }.padding(vertical = 4.dp))
    Spacer(Modifier.height(8.dp))
}

@Composable
private fun ShadowHead(text: String) {
    Text(text, color = TerminalDim, style = MaterialTheme.typography.labelMedium, modifier = Modifier.padding(top = 16.dp, bottom = 6.dp))
}

/** A bordered block; [edge] is its border (a muted green for the ordinary, the warning colour for what was named). */
@Composable
private fun ShadowBlock(edge: Color = GhostBorder, onTap: (() -> Unit)? = null, content: @Composable ColumnScope.() -> Unit) {
    Column(Modifier.fillMaxWidth().padding(vertical = 3.dp).border(1.dp, edge, RectangleShape).background(VoidLighter)
        .let { if (onTap != null) it.clickable { onTap() } else it }.padding(horizontal = 10.dp, vertical = 8.dp), content = content)
}

@Composable
private fun ShadowAction(label: String, enabled: Boolean = true, onTap: () -> Unit) {
    Text("[ $label ]", color = if (enabled) TerminalGreen else GhostBorder, style = MaterialTheme.typography.labelMedium,
        modifier = Modifier.clickable(enabled = enabled) { onTap() }.padding(end = 14.dp, top = 6.dp, bottom = 6.dp))
}

/** One thing that was named: its title and when, and on a tap what it said. */
@Composable
private fun ShadowObservation(o: ShadowText.Observation, detectorName: String, onChat: (Long) -> Unit) {
    var openBody by rememberSaveable(o.id) { mutableStateOf(false) }
    ShadowBlock(edge = if (o.muted) GhostBorder else Warning, onTap = { openBody = !openBody }) {
        Text(o.title, color = GhostText, style = MaterialTheme.typography.bodyMedium)
        Text(ShadowText.dayTime(o.at) + (if (detectorName.isNotBlank()) " · $detectorName" else "") + if (o.muted) " · while muted" else "",
            color = TerminalDim, style = MaterialTheme.typography.labelSmall)
        if (openBody) {
            Spacer(Modifier.height(6.dp))
            SelectionContainer { Text(o.body, color = GhostTextDim, style = MaterialTheme.typography.bodySmall) }
            val chat = if (o.subject.startsWith("chat:")) o.subject.removePrefix("chat:").substringBefore('/').toLongOrNull() ?: 0L else 0L
            if (chat > 0) ShadowAction("this chat's audit trail") { onChat(chat) }
        }
    }
}

@Composable
private fun ShadowFleet(f: ShadowText.Fleet, onDetector: (String) -> Unit, onCatalogue: () -> Unit, onChat: (Long) -> Unit) {
    val ctx = LocalContext.current
    var recent by remember { mutableStateOf<List<ShadowText.Observation>?>(null) }
    LaunchedEffect(Unit) { recent = BoxClient.shadowObservations(ctx, limit = 6) }
    val now = System.currentTimeMillis() / 1000
    val names = remember(f) { f.detectors.associate { it.id to it.name } }
    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp)) {
        item {
            Spacer(Modifier.height(12.dp))
            ShadowTitle()
            Text("The box checking itself. It names what it sees and stops: nothing is blocked, and nothing here turns it off.",
                color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(10.dp))
            ShadowBlock {
                Text(ShadowText.readBy(f.arbiter), color = GhostText, style = MaterialTheme.typography.bodySmall)
                val note = ShadowText.readByNote(f.arbiter)
                if (note.isNotBlank()) Text(note, color = GhostTextDim, style = MaterialTheme.typography.labelSmall, modifier = Modifier.padding(top = 4.dp))
            }
        }
        val r = recent
        if (!r.isNullOrEmpty()) {
            item { ShadowHead("NAMED LATELY") }
            items(r, key = { "o:" + it.id }) { o -> ShadowObservation(o, names[o.detector] ?: "", onChat) }
        } else if (r != null) {
            item { ShadowHead("NAMED LATELY"); Text("nothing yet", color = GhostTextDim, style = MaterialTheme.typography.labelMedium) }
        }
        ShadowText.sections(f).forEach { (title, dets) ->
            item(key = "h:$title") { ShadowHead(title) }
            items(dets, key = { "d:" + it.id }) { d ->
                ShadowBlock(onTap = { onDetector(d.id) }) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text(d.name, color = GhostText, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.weight(1f))
                        Text("›", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                    }
                    Text(ShadowText.spokenLine(d) + if (d.mutedUntil > now) " · muted" else "", color = if (d.mutedUntil > now) Warning else TerminalDim,
                        style = MaterialTheme.typography.labelSmall)
                }
            }
        }
        item {
            ShadowHead("THE CATALOGUE")
            ShadowBlock(onTap = onCatalogue) {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(ShadowText.builtCount(f), color = GhostTextDim, style = MaterialTheme.typography.bodySmall, modifier = Modifier.weight(1f))
                    Text("›", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                }
            }
            ShadowHead("THE SETTINGS")
            Text(ShadowText.resetLine(f), color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            if (f.changes.isEmpty()) Text("no threshold or mute has been changed", color = TerminalDim, style = MaterialTheme.typography.labelSmall, modifier = Modifier.padding(top = 6.dp))
        }
        items(f.changes.take(12)) { c ->
            Text(ShadowText.changeLine(c, names[c.detector] ?: ""), color = if (c.quieter) Warning else TerminalDim, style = MaterialTheme.typography.labelSmall,
                modifier = Modifier.padding(top = 3.dp))
        }
        item { Spacer(Modifier.height(24.dp)) }
    }
}

@Composable
private fun ShadowDetectorPage(f: ShadowText.Fleet, d: ShadowText.Detector, onBack: () -> Unit, onChanged: () -> Unit, onChat: (Long) -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var history by remember(d.id) { mutableStateOf<List<ShadowText.Observation>?>(null) }
    var busy by remember { mutableStateOf(false) }
    var note by remember(d.id) { mutableStateOf("") }
    LaunchedEffect(d.id) { history = BoxClient.shadowObservations(ctx, detector = d.id, limit = 40) }
    val entry = f.catalogue.firstOrNull { it.n == d.entry }
    val now = System.currentTimeMillis() / 1000
    fun act(what: suspend () -> Boolean) {
        if (busy) return
        busy = true; note = ""
        scope.launch {
            if (!what()) note = "the box did not take that"
            busy = false
            onChanged()
        }
    }
    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp)) {
        item {
            ShadowBack(onBack)
            Text(d.name.uppercase(), color = GhostText, style = MaterialTheme.typography.titleLarge)
            if (entry != null) Text("pattern ${entry.n} · ${entry.name}", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(10.dp))
            ShadowBlock {
                Text("It reads " + d.reads + ".", color = GhostText, style = MaterialTheme.typography.bodySmall)
                Text(ShadowText.sentLine(d) + if (d.model) " · asks the model, so it waits its turn" else "", color = TerminalDim,
                    style = MaterialTheme.typography.labelSmall, modifier = Modifier.padding(top = 4.dp))
            }
            ShadowHead("THRESHOLD")
            Text(ShadowText.thresholdLine(d), color = GhostText, style = MaterialTheme.typography.bodySmall)
            if (d.tunable) {
                Text("It counts " + d.unit + ". Higher is quieter.", color = GhostTextDim, style = MaterialTheme.typography.labelSmall)
                Row {
                    val down = ShadowText.stepped(d, up = false)
                    val up = ShadowText.stepped(d, up = true)
                    ShadowAction(if (down != null) "louder: " + ShadowText.num(down) else "louder", enabled = down != null && !busy) {
                        act { BoxClient.shadowThreshold(ctx, d.id, down!!) != null }
                    }
                    ShadowAction(if (up != null) "quieter: " + ShadowText.num(up) else "quieter", enabled = up != null && !busy) {
                        act { BoxClient.shadowThreshold(ctx, d.id, up!!) != null }
                    }
                }
            }
            ShadowHead("MUTE")
            val muted = ShadowText.mutedLine(d, now)
            if (muted.isNotBlank()) {
                Text(muted, color = Warning, style = MaterialTheme.typography.labelMedium)
                ShadowAction("lift the mute", enabled = !busy) { act { BoxClient.shadowMute(ctx, d.id, 0) != null } }
            } else {
                Text("A mute is ${f.maxMuteHours / 24} days at most. Muted, it goes on recording and nothing is sent.", color = GhostTextDim,
                    style = MaterialTheme.typography.labelSmall)
                Row {
                    ShadowAction("a day", enabled = !busy) { act { BoxClient.shadowMute(ctx, d.id, 24) != null } }
                    ShadowAction("${f.maxMuteHours / 24} days", enabled = !busy) { act { BoxClient.shadowMute(ctx, d.id, f.maxMuteHours) != null } }
                }
            }
            if (note.isNotBlank()) ErrorLine(note)
            ShadowHead("WHAT IT HAS NAMED")
        }
        val h = history
        when {
            h == null -> item { LoadingRow() }
            h.isEmpty() -> item { Text("nothing yet", color = GhostTextDim, style = MaterialTheme.typography.labelMedium) }
            else -> items(h, key = { "o:" + it.id }) { o -> ShadowObservation(o, "", onChat) }
        }
        if (entry != null) item {
            ShadowHead("THE PATTERN")
            Text(ShadowText.tags(entry), color = TerminalDim, style = MaterialTheme.typography.labelSmall)
            Text(entry.tells, color = GhostTextDim, style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(top = 4.dp))
            Text(entry.does, color = GhostTextDim, style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(top = 6.dp))
        }
        item { Spacer(Modifier.height(24.dp)) }
    }
}

@Composable
private fun ShadowCatalogue(f: ShadowText.Fleet, onBack: () -> Unit, onDetector: (String) -> Unit) {
    val names = remember(f) { f.detectors.associate { it.id to it.name } }
    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp)) {
        item {
            ShadowBack(onBack)
            Text("THE CATALOGUE", color = GhostText, style = MaterialTheme.typography.titleLarge)
            Text(ShadowText.builtCount(f), color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(8.dp))
        }
        items(f.catalogue, key = { "e:" + it.n }) { e ->
            ShadowBlock(edge = if (e.built) TerminalDim else GhostBorder) {
                Text("${e.n}  ${e.name}", color = if (e.built) GhostText else GhostTextDim, style = MaterialTheme.typography.bodyMedium)
                Text(ShadowText.tags(e), color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                Text(e.tells, color = GhostTextDim, style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(top = 4.dp))
                Text(e.does, color = if (e.built) GhostText else GhostTextDim, style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(top = 6.dp))
                if (e.built) Row { e.detectors.forEach { id -> ShadowAction(names[id] ?: id) { onDetector(id) } } }
            }
        }
        item { Spacer(Modifier.height(24.dp)) }
    }
}

/** One chat's audit trail, and the button that asks for it to be read cold now. */
@Composable
private fun ShadowTrailPage(chat: Long, onBack: () -> Unit, onOpenChat: (Long) -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var trail by remember(chat) { mutableStateOf<ShadowText.Trail?>(null) }
    var named by remember(chat) { mutableStateOf<List<ShadowText.Observation>>(emptyList()) }
    var failed by remember(chat) { mutableStateOf(false) }
    var note by remember(chat) { mutableStateOf("") }
    var tick by remember(chat) { mutableIntStateOf(0) }
    LaunchedEffect(chat, tick) {
        val t = BoxClient.shadowTrail(ctx, chat)
        failed = t == null
        if (t != null) trail = t
        named = BoxClient.shadowObservations(ctx, about = "chat:$chat") ?: named
        // while the arbiter reads, look again every few seconds; it says when it is done
        if (t != null && t.reading) { delay(4000); tick++ }
    }
    val t = trail
    val now = System.currentTimeMillis() / 1000
    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp)) {
        item {
            ShadowBack(onBack)
            Text("AUDIT TRAIL", color = GhostText, style = MaterialTheme.typography.titleLarge)
            Text("What the box asked you in this chat, what it had been given when it answered, and what the arbiter made of each answer.",
                color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            Row {
                ShadowAction("read it cold now", enabled = t != null && !t.reading) {
                    scope.launch {
                        val (going, why) = BoxClient.shadowRead(ctx, chat)
                        note = if (going) "" else why.ifBlank { "the box did not start it" }
                        tick++
                    }
                }
                ShadowAction("open the chat") { onOpenChat(chat) }
            }
            if (note.isNotBlank()) ErrorLine(note)
            if (t != null) {
                val line = ShadowText.askedLine(t, now)
                if (line.isNotBlank()) Text(line, color = if (t.reading) TerminalGreen else TerminalDim, style = MaterialTheme.typography.labelMedium)
            }
        }
        if (named.isNotEmpty()) {
            item { ShadowHead("NAMED ABOUT THIS CHAT") }
            items(named, key = { "o:" + it.id }) { o -> ShadowObservation(o, "", onChat = {}) }
        }
        when {
            t == null && failed -> item { ErrorLine("the box did not answer , is it unlocked?") }
            t == null -> item { LoadingRow() }
            t.entries.isEmpty() -> item { Text("the box has not answered in this chat", color = GhostTextDim, style = MaterialTheme.typography.labelMedium) }
            else -> {
                item { ShadowHead("EACH ANSWER, OLDEST FIRST") }
                items(t.entries, key = { "a:" + it.msgId }) { e -> ShadowTrailEntry(e) }
            }
        }
        item { Spacer(Modifier.height(24.dp)) }
    }
}

@Composable
private fun ShadowTrailEntry(e: ShadowText.TrailEntry) {
    var more by rememberSaveable(e.msgId) { mutableStateOf(false) }
    val r = e.read
    val flagged = r != null && (r.conduct.isNotEmpty() || r.verdict == "no")
    ShadowBlock(edge = if (flagged) Warning else GhostBorder, onTap = { more = !more }) {
        Text("answered " + ShadowText.dayTime(e.atMs / 1000) + if (e.praise) " · opens with praise" else "", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
        Text(ShadowText.readLine(r), color = if (flagged) Warning else GhostText, style = MaterialTheme.typography.bodySmall)
        r?.conduct?.forEach { c ->
            Text(ShadowText.findingTitle(c.kind) + ": “" + c.quote + "”", color = Warning, style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(top = 4.dp))
        }
        if (e.asked.isNotEmpty()) {
            Text("it asked:", color = TerminalDim, style = MaterialTheme.typography.labelSmall, modifier = Modifier.padding(top = 6.dp))
            e.asked.forEach { q -> Text("· $q", color = GhostTextDim, style = MaterialTheme.typography.bodySmall) }
        }
        if (more) {
            if (r != null && r.cold.isNotBlank()) {
                Text("read cold, it said:", color = TerminalDim, style = MaterialTheme.typography.labelSmall, modifier = Modifier.padding(top = 6.dp))
                SelectionContainer { Text(r.cold, color = GhostTextDim, style = MaterialTheme.typography.bodySmall) }
            }
            Text(if (e.given.isEmpty()) "what it had been given was not kept for this answer" else "it had been given:", color = TerminalDim,
                style = MaterialTheme.typography.labelSmall, modifier = Modifier.padding(top = 6.dp))
            e.given.forEach { g -> Text("· $g", color = GhostTextDim, style = MaterialTheme.typography.bodySmall) }
            if (e.reasoning.isNotBlank()) {
                Text("its reasoning, as saved:", color = TerminalDim, style = MaterialTheme.typography.labelSmall, modifier = Modifier.padding(top = 6.dp))
                SelectionContainer { Text(e.reasoning, color = GhostTextDim, style = MaterialTheme.typography.bodySmall) }
            }
        } else {
            Text("tap for what it had been given" + if (r != null && r.cold.isNotBlank()) " and the cold read" else "", color = TerminalDim,
                style = MaterialTheme.typography.labelSmall, modifier = Modifier.padding(top = 4.dp))
        }
    }
}

/**
 * What ghost.shadowd named about one thing, shown where that thing is: under a mail that leans
 * on pressure, say. Nothing when nothing was named, or when the box does not answer.
 */
@Composable
fun ShadowAbout(about: String, modifier: Modifier = Modifier) {
    val ctx = LocalContext.current
    var named by remember(about) { mutableStateOf<List<ShadowText.Observation>>(emptyList()) }
    LaunchedEffect(about) { named = BoxClient.shadowObservations(ctx, about = about, limit = 3) ?: emptyList() }
    if (named.isEmpty()) return
    Column(modifier) {
        named.forEach { o ->
            Column(Modifier.fillMaxWidth().border(1.dp, Warning, RectangleShape).padding(12.dp)) {
                Text("ghost.shadowd: " + o.title, color = Warning, style = MaterialTheme.typography.labelMedium)
                Text(o.body, color = GhostTextDim, style = MaterialTheme.typography.labelSmall, modifier = Modifier.padding(top = 4.dp))
            }
        }
    }
}
