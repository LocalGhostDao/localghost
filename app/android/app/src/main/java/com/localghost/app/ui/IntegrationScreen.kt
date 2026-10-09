package com.localghost.app.ui

import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import com.localghost.app.net.BoxClient
import com.localghost.app.ui.theme.*
import kotlinx.coroutines.launch

/**
 * ONE INTEGRATION'S PAGE: the pull drawn at the top (the places it draws from on the left, the
 * box on the right, the packets running while the page asks the box and breathing once it has
 * answered), then what the box has, the places one row each with its mark and what it gives,
 * the actions (open the page, the feeds, a fetch from the mirror behind a question), and how
 * it is made, from the same words as the ⓘ, folded under the rest.
 */
@Composable
fun IntegrationScreen(id: String, onOpen: (String) -> Unit, onFeeds: () -> Unit, onBack: () -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var data by remember { mutableStateOf<BoxClient.Sources?>(null) }
    var failed by remember { mutableStateOf(false) }
    var confirm by remember { mutableStateOf(false) }
    var note by remember { mutableStateOf("") }
    var howOpen by remember { mutableStateOf(false) }
    var tick by remember { mutableIntStateOf(0) }
    LaunchedEffect(tick) {
        while (true) {
            val d = BoxClient.sources(ctx)
            if (d != null) { data = d; failed = false } else failed = data == null
            kotlinx.coroutines.delay(if (d?.job?.running == true) 5_000 else 60_000)
        }
    }
    val d = data
    val src = d?.sources?.firstOrNull { it.id == id }
    fun fetch(s: BoxClient.Source) {
        confirm = false
        note = "asking the box to fetch…"
        scope.launch {
            val region = if (s.action == "maps") com.localghost.app.sync.LocationLog.last(ctx)?.let { SourcesText.region(it.lat, it.lon) } ?: "" else ""
            val (ok, why) = BoxClient.sourcesFetch(ctx, s.action, region)
            note = if (ok) "" else "! $why"
            tick++
        }
    }
    if (confirm && src != null) AskDialog(
        title = src.label.uppercase(),
        body = SourcesText.confirm(src.id),
        confirmLabel = "FETCH",
        onConfirm = { fetch(src) },
        onDismiss = { confirm = false },
    )
    Column(Modifier.fillMaxSize().padding(horizontal = 20.dp).verticalScroll(rememberScrollState())) {
        Spacer(Modifier.height(12.dp))
        Text("‹ integrations", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.clickable { onBack() }.padding(vertical = 4.dp))
        Spacer(Modifier.height(8.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(SourcesText.glyph(id), color = TerminalGreen, style = glow(MaterialTheme.typography.titleLarge), modifier = Modifier.padding(end = 10.dp))
            SectionLabel((src?.name ?: id).uppercase())
            InfoButton(id)
        }
        // THE PULL: streaming until the box has answered, breathing after
        val states = src?.from?.map { it.state } ?: listOf("", "")
        PullCanvas(fromStates = states, steady = src != null)
        when {
            src == null && failed -> ErrorLine("the box did not answer , is it unlocked?")
            src == null && d != null -> ErrorLine("the box has no integration called $id")
            src == null -> LoadingRow("asking the box…")
            else -> {
                // WHAT THE BOX HAS
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(SourcesText.mark(src.state), color = stateColour(src.state), style = MaterialTheme.typography.titleMedium, modifier = Modifier.padding(end = 10.dp))
                    Text(src.line, color = GhostText, style = MaterialTheme.typography.bodyMedium)
                }
                if (src.detail.isNotEmpty()) Text(src.detail, color = GhostTextDim, style = MaterialTheme.typography.labelMedium, modifier = Modifier.padding(top = 4.dp))
                // THE JOB, if one runs or just ran
                d?.job?.let { j ->
                    if (j.step == src.action || j.running) {
                        Spacer(Modifier.height(8.dp))
                        Column(Modifier.fillMaxWidth().border(1.dp, if (j.running) TerminalGreen else GhostBorder, RectangleShape).background(VoidLighter).padding(10.dp)) {
                            Text(SourcesText.job(j.step, j.startedAt, j.endedAt, j.running, j.exit, System.currentTimeMillis() / 1000),
                                color = if (j.running) TerminalGreen else if (j.exit == 0) GhostText else Warning, style = MaterialTheme.typography.labelMedium)
                            if (j.last.isNotEmpty()) Text("› " + j.last, color = GhostTextDim, style = MaterialTheme.typography.labelSmall)
                        }
                    }
                }
                if (note.isNotEmpty()) {
                    Spacer(Modifier.height(6.dp))
                    Text(note, color = if (note.startsWith("!")) Warning else TerminalDim, style = MaterialTheme.typography.labelMedium)
                }
                // THE ACTIONS
                val running = d?.job?.running == true
                Row(Modifier.padding(top = 10.dp)) {
                    if (src.open.isNotEmpty()) {
                        Text("[ open ${src.open.uppercase()} ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                            modifier = Modifier.clickable { onOpen(src.open) }.padding(end = 12.dp, top = 4.dp, bottom = 4.dp))
                    }
                    if (src.id == "news") {
                        Text("[ the feeds ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                            modifier = Modifier.clickable { onFeeds() }.padding(end = 12.dp, top = 4.dp, bottom = 4.dp))
                    }
                    if (src.action.isNotEmpty() && src.label.isNotEmpty()) {
                        Text("[ ${src.label} ]", color = if (running) GhostBorder else TerminalGreen, style = MaterialTheme.typography.labelMedium,
                            modifier = Modifier.clickable(enabled = !running) { confirm = true }.padding(top = 4.dp, bottom = 4.dp))
                    }
                }
                // WHERE IT DRAWS FROM
                if (src.from.isNotEmpty()) {
                    Spacer(Modifier.height(14.dp))
                    Text("DRAWS FROM", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                    Spacer(Modifier.height(4.dp))
                    src.from.forEach { f ->
                        Row(Modifier.fillMaxWidth().padding(vertical = 3.dp), verticalAlignment = Alignment.Top) {
                            Text(SourcesText.fromMark(f.state), color = when (f.state) { "ok" -> TerminalGreen; "off" -> GhostBorder; "" -> TerminalDim; else -> Warning },
                                style = MaterialTheme.typography.labelMedium, modifier = Modifier.width(18.dp))
                            Column {
                                Text(f.name, color = if (f.state == "off") GhostTextDim else GhostText, style = MaterialTheme.typography.labelMedium)
                                if (f.role.isNotEmpty()) Text(f.role, color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                            }
                        }
                    }
                }
                // HOW IT IS MADE, folded
                Explain.of(id)?.let { t ->
                    Spacer(Modifier.height(14.dp))
                    Text(if (howOpen) "[ − how it is made ]" else "[ + how it is made ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.clickable { howOpen = !howOpen }.padding(vertical = 4.dp))
                    Column(Modifier.animateContentSize()) {
                        if (howOpen) t.paragraphs.forEach { p ->
                            Text(p, color = GhostTextDim, style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(top = 6.dp))
                        }
                    }
                }
            }
        }
        Spacer(Modifier.height(24.dp))
    }
}
