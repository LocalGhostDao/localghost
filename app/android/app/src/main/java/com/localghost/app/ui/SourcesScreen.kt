package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.grid.GridCells
import androidx.compose.foundation.lazy.grid.LazyVerticalGrid
import androidx.compose.foundation.lazy.grid.items
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import com.localghost.app.net.BoxClient
import com.localghost.app.ui.theme.*

/**
 * INTEGRATIONS: everything the box draws on beyond your own archive, a card each (Wikipedia,
 * the news, the market numbers, the weather, the maps and the heights, speech) with its state,
 * one line of what the box has, and how many places it draws from. A card opens the
 * integration's page (IntegrationScreen): the pull drawn, the places listed, the actions. The
 * fetch running, if any, sits above the cards and is followed every five seconds.
 */
/** What INTEGRATIONS last showed, for the next open to draw at once; goes with the session's
 *  memory on lock (clearMapMemory's companions). */
object SourcesMemory {
    @Volatile var last: BoxClient.Sources? = null
}

@Composable
fun SourcesScreen(onOpenIntegration: (String) -> Unit) {
    val ctx = LocalContext.current
    // the last answer shown at once (the page opened a moment ago; the box is asked again behind it)
    var data by remember { mutableStateOf(SourcesMemory.last) }
    var failed by remember { mutableStateOf(false) }
    LaunchedEffect(Unit) {
        while (true) {
            val d = BoxClient.sources(ctx)
            if (d != null) { data = d; SourcesMemory.last = d; failed = false } else failed = data == null
            kotlinx.coroutines.delay(if (d?.job?.running == true) 5_000 else 60_000)
        }
    }
    val d = data
    LazyVerticalGrid(columns = GridCells.Fixed(2), modifier = Modifier.fillMaxSize().padding(horizontal = 20.dp),
        horizontalArrangement = Arrangement.spacedBy(10.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
        item(span = { androidx.compose.foundation.lazy.grid.GridItemSpan(2) }) {
            Column {
                Spacer(Modifier.height(12.dp))
                Row(verticalAlignment = Alignment.CenterVertically) {
                    SectionLabel("INTEGRATIONS")
                    InfoButton("sources")
                }
                Text("what your box draws on beyond your own archive · a card opens the pull", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                d?.job?.let { j ->
                    Spacer(Modifier.height(10.dp))
                    Column(Modifier.fillMaxWidth().border(1.dp, if (j.running) TerminalGreen else GhostBorder, RectangleShape).background(VoidLighter).padding(10.dp)) {
                        Text(SourcesText.job(j.step, j.startedAt, j.endedAt, j.running, j.exit, System.currentTimeMillis() / 1000), color = if (j.running) TerminalGreen else if (j.exit == 0) GhostText else Warning,
                            style = MaterialTheme.typography.labelMedium)
                        if (j.last.isNotEmpty()) Text("› " + j.last, color = GhostTextDim, style = MaterialTheme.typography.labelSmall)
                    }
                }
                Spacer(Modifier.height(4.dp))
                when {
                    d == null && failed -> ErrorLine("the box did not answer , is it unlocked?")
                    d == null -> LoadingRow()
                }
            }
        }
        if (d != null) items(d.sources, key = { it.id }) { src -> IntegrationCard(src) { onOpenIntegration(src.id) } }
        item(span = { androidx.compose.foundation.lazy.grid.GridItemSpan(2) }) { Spacer(Modifier.height(24.dp)) }
    }
}

/** One integration as a card: the glyph, the name, the state, one line, the count of places. */
@Composable
private fun IntegrationCard(src: BoxClient.Source, onClick: () -> Unit) {
    val colour = stateColour(src.state)
    Column(Modifier.fillMaxWidth().heightIn(min = 150.dp).border(1.dp, if (src.state == "ready") TerminalDim else GhostBorder, RectangleShape)
        .background(VoidLighter).clickable { onClick() }.padding(12.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(SourcesText.glyph(src.id), color = TerminalGreen, style = glow(MaterialTheme.typography.titleLarge))
            Spacer(Modifier.weight(1f))
            Text(SourcesText.mark(src.state) + " " + SourcesText.word(src.state), color = colour, style = MaterialTheme.typography.labelSmall)
        }
        Spacer(Modifier.height(8.dp))
        Text(src.name, color = GhostText, style = MaterialTheme.typography.titleSmall)
        Spacer(Modifier.height(4.dp))
        Text(src.line, color = GhostTextDim, style = MaterialTheme.typography.labelSmall, maxLines = 3, overflow = TextOverflow.Ellipsis)
        Spacer(Modifier.weight(1f))
        Spacer(Modifier.height(6.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(SourcesText.fromCount(src.from.size), color = TerminalDim, style = MaterialTheme.typography.labelSmall, modifier = Modifier.weight(1f))
            Text("›", color = TerminalGreen, style = MaterialTheme.typography.titleSmall)
        }
    }
}

@Composable
internal fun stateColour(state: String) = when (state) {
    "ready" -> TerminalGreen; "partial", "importing" -> Warning; "missing", "off" -> GhostTextDim; else -> TerminalDim
}
