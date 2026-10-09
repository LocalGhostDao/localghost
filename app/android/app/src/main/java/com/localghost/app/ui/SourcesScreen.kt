package com.localghost.app.ui

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
import kotlinx.coroutines.launch

/**
 * SOURCES: everything the box draws on beyond your own archive, one row each with its state
 * (Wikipedia, the news feeds, the market numbers, the weather, the maps and the heights, speech),
 * a page to open where there is one, and a fetch from the mirror where the box lacks the set. A
 * fetch runs on the box in the background; this page follows it while it runs (every five
 * seconds) and says how it ended. Each row's ⓘ says what the source is made of.
 */
@Composable
fun SourcesScreen(onOpen: (String) -> Unit, onFeeds: () -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var data by remember { mutableStateOf<BoxClient.Sources?>(null) }
    var failed by remember { mutableStateOf(false) }
    var confirm by remember { mutableStateOf<BoxClient.Source?>(null) }
    var note by remember { mutableStateOf("") }
    var tick by remember { mutableIntStateOf(0) }
    LaunchedEffect(tick) {
        while (true) {
            val d = BoxClient.sources(ctx)
            if (d != null) { data = d; failed = false } else failed = data == null
            kotlinx.coroutines.delay(if (d?.job?.running == true) 5_000 else 60_000)
        }
    }
    fun fetch(src: BoxClient.Source) {
        confirm = null
        note = "asking the box to fetch…"
        scope.launch {
            // the maps' heights for your part of the world: a box around the phone's last fix (the
            // position goes to your box and nowhere else); the world without one
            val region = if (src.action == "maps") com.localghost.app.sync.LocationLog.last(ctx)?.let { SourcesText.region(it.lat, it.lon) } ?: "" else ""
            val (ok, why) = BoxClient.sourcesFetch(ctx, src.action, region)
            note = if (ok) "" else "! $why"
            tick++
        }
    }
    confirm?.let { src ->
        AskDialog(
            title = src.label.uppercase(),
            body = SourcesText.confirm(src.id),
            confirmLabel = "FETCH",
            onConfirm = { fetch(src) },
            onDismiss = { confirm = null },
        )
    }
    Column(Modifier.fillMaxSize().padding(horizontal = 20.dp).verticalScroll(rememberScrollState())) {
        Spacer(Modifier.height(12.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            SectionLabel("SOURCES")
            InfoButton("sources")
        }
        Text("what your box draws on beyond your own archive, and its state", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        Spacer(Modifier.height(12.dp))
        val d = data
        d?.job?.let { j ->
            Column(Modifier.fillMaxWidth().border(1.dp, if (j.running) TerminalGreen else GhostBorder, RectangleShape).background(VoidLighter).padding(10.dp)) {
                Text(SourcesText.job(j.step, j.startedAt, j.endedAt, j.running, j.exit, System.currentTimeMillis() / 1000), color = if (j.running) TerminalGreen else if (j.exit == 0) GhostText else Warning,
                    style = MaterialTheme.typography.labelMedium)
                if (j.last.isNotEmpty()) Text("› " + j.last, color = GhostTextDim, style = MaterialTheme.typography.labelSmall)
            }
            Spacer(Modifier.height(12.dp))
        }
        if (note.isNotEmpty()) {
            Text(note, color = if (note.startsWith("!")) Warning else TerminalDim, style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(8.dp))
        }
        when {
            d == null && failed -> ErrorLine("the box did not answer , is it unlocked?")
            d == null -> LoadingRow()
            else -> d.sources.forEach { src ->
                Column(Modifier.fillMaxWidth().padding(vertical = 6.dp).border(1.dp, GhostBorder, RectangleShape)
                    .then(if (src.open.isNotEmpty()) Modifier.clickable { onOpen(src.open) } else Modifier)
                    .padding(12.dp)) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text(SourcesText.mark(src.state), color = stateColour(src.state), style = MaterialTheme.typography.titleMedium,
                            modifier = Modifier.padding(end = 10.dp))
                        Text(src.name, color = GhostText, style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f))
                        InfoButton(src.id)
                        if (src.open.isNotEmpty()) Text("›", color = TerminalGreen, style = MaterialTheme.typography.titleSmall)
                    }
                    Spacer(Modifier.height(4.dp))
                    Text(src.line, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                    if (src.detail.isNotEmpty()) Text(src.detail, color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                    val running = d.job?.running == true
                    Row(Modifier.padding(top = 6.dp)) {
                        if (src.action.isNotEmpty() && src.label.isNotEmpty()) {
                            Text("[ ${src.label} ]", color = if (running) GhostBorder else TerminalGreen, style = MaterialTheme.typography.labelMedium,
                                modifier = Modifier.clickable(enabled = !running) { confirm = src }.padding(end = 12.dp))
                        }
                        if (src.id == "news") {
                            Text("[ feeds ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                                modifier = Modifier.clickable { onFeeds() })
                        }
                    }
                }
            }
        }
        Spacer(Modifier.height(24.dp))
    }
}

@Composable
private fun stateColour(state: String) = when (state) {
    "ready" -> TerminalGreen; "partial", "importing" -> Warning; "missing", "off" -> GhostTextDim; else -> TerminalDim
}
