package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import com.localghost.app.net.LifeContext
import com.localghost.app.net.MemoryEntry
import com.localghost.app.ui.theme.*

@Composable
fun MemoriesScreen(context: LifeContext?, entries: Loadable<List<MemoryEntry>>) {
    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp),
        verticalArrangement = Arrangement.spacedBy(14.dp)) {

        item {
            Spacer(Modifier.height(12.dp))
            SectionLabel("EXTRACTED ON THE BOX")
            Spacer(Modifier.height(10.dp))
            if (context != null) {
                Text("${fmt(context.memories)} memories · ${fmt(context.photos)} photos · " +
                     "${fmt(context.videos)} videos · ${fmt(context.voiceNotes)} voice notes",
                    color = TerminalGreen, style = MaterialTheme.typography.bodyMedium)
                Spacer(Modifier.height(2.dp))
                Text("indexed on the box · updated ${context.lastUpdated} · never leaves it",
                    color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            } else {
                Text("reading index from the box…", color = GhostTextDim,
                    style = MaterialTheme.typography.bodyMedium)
            }
            Spacer(Modifier.height(8.dp))
        }

        when (entries) {
            is Loadable.Loading -> item { LoadingRow() }
            is Loadable.Failed -> item { ErrorLine(entries.reason) }
            is Loadable.Loaded -> {
                if (entries.value.isEmpty()) item {
                    EmptyLine("empty. After sync, ghost.framed extracts entries here. " +
                        "Stored on your box, not in this app.")
                } else items(entries.value) { e -> MemoryCard(e) }
            }
        }
        item { Spacer(Modifier.height(24.dp)) }
    }
}

@Composable
private fun MemoryCard(e: MemoryEntry) {
    Column(Modifier.fillMaxWidth()
        .border(1.dp, GhostBorder, RectangleShape)
        .background(VoidLighter)
        .padding(14.dp)) {
        Row {
            Text(e.whenLabel.uppercase(), color = TerminalDim,
                style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.weight(1f))
            Text(e.daemonId, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        }
        Spacer(Modifier.height(6.dp))
        Text(e.title, color = GhostText, style = MaterialTheme.typography.titleMedium)
        Spacer(Modifier.height(4.dp))
        Text(e.summary, color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
    }
}

private fun fmt(n: Int): String =
    n.toString().reversed().chunked(3).joinToString(",").reversed()
