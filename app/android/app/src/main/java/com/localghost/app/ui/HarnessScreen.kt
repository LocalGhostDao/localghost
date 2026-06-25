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
import com.localghost.app.net.DaemonStatus
import com.localghost.app.ui.theme.*

@Composable
fun HarnessScreen(daemons: Loadable<List<DaemonStatus>>) {
    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp)) {
        item {
            Spacer(Modifier.height(12.dp))
            SectionLabel("BOX STATUS")
            Spacer(Modifier.height(8.dp))
            Text("Processes running on your box. They read what you sync, build the local " +
                 "index, and queue notifications. They run on the box only. This app polls " +
                 "them, it does not run them.",
                 color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
            Spacer(Modifier.height(6.dp))
        }
        when (daemons) {
            is Loadable.Loading -> item { LoadingRow("polling daemons…") }
            is Loadable.Failed -> item { ErrorLine(daemons.reason) }
            is Loadable.Loaded -> items(daemons.value) { d -> DaemonRow(d) }
        }
        item { Spacer(Modifier.height(24.dp)) }
    }
}

@Composable
private fun DaemonRow(d: DaemonStatus) {
    Column(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape)
        .background(VoidLighter).padding(14.dp)) {
        Row {
            Text(dot(d.state), color = stateColor(d.state), style = MaterialTheme.typography.bodyMedium)
            Spacer(Modifier.width(8.dp))
            Text(d.id, color = TerminalGreen, style = MaterialTheme.typography.titleMedium)
            Spacer(Modifier.weight(1f))
            Text(label(d.state), color = stateColor(d.state), style = MaterialTheme.typography.labelMedium)
        }
        Spacer(Modifier.height(6.dp))
        Text(d.role, color = GhostText, style = MaterialTheme.typography.bodyMedium)
        Spacer(Modifier.height(2.dp))
        Text("${d.detail}  ·  ${d.lastRun}", color = GhostTextDim,
            style = MaterialTheme.typography.labelMedium)
    }
}

private fun dot(s: DaemonStatus.State) = when (s) {
    DaemonStatus.State.WORKING -> "◉"
    DaemonStatus.State.LISTENING -> "◎"
    DaemonStatus.State.IDLE -> "○"
    DaemonStatus.State.ERROR -> "✕"
}
private fun label(s: DaemonStatus.State) = when (s) {
    DaemonStatus.State.WORKING -> "WORKING"
    DaemonStatus.State.LISTENING -> "LISTENING"
    DaemonStatus.State.IDLE -> "IDLE"
    DaemonStatus.State.ERROR -> "ERROR"
}
private fun stateColor(s: DaemonStatus.State) = when (s) {
    DaemonStatus.State.WORKING -> TerminalGreen
    DaemonStatus.State.LISTENING -> TerminalGreen
    DaemonStatus.State.IDLE -> GhostTextDim
    DaemonStatus.State.ERROR -> Warning
}
