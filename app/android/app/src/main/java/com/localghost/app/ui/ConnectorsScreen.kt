package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.unit.dp
import com.localghost.app.net.Connector
import com.localghost.app.ui.theme.*

@Composable
fun ConnectorsScreen(
    connectors: Loadable<List<Connector>>,
    onConnect: (String) -> Unit,
    onDisconnect: (String) -> Unit,
) {
    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp)) {
        item {
            Spacer(Modifier.height(12.dp))
            SectionLabel("CONNECTORS")
            Spacer(Modifier.height(8.dp))
            Text("External sources the box pulls into your index. The box holds the " +
                 "credentials and does the syncing — this phone only starts the connection " +
                 "and never sees the tokens.",
                 color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
            Spacer(Modifier.height(6.dp))
        }
        when (connectors) {
            is Loadable.Loading -> item { LoadingRow("reading connectors…") }
            is Loadable.Failed -> item { ErrorLine(connectors.reason) }
            is Loadable.Loaded -> items(connectors.value) { c ->
                ConnectorRow(c, onConnect, onDisconnect)
            }
        }
        item { Spacer(Modifier.height(24.dp)) }
    }
}

@Composable
private fun ConnectorRow(c: Connector, onConnect: (String) -> Unit, onDisconnect: (String) -> Unit) {
    Column(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape)
        .background(VoidLighter).padding(14.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Column(Modifier.weight(1f)) {
                Text(c.name, color = TerminalGreen, style = MaterialTheme.typography.titleMedium)
                Text(c.detail, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            }
            if (c.connected) {
                Text("[ DISCONNECT ]", color = Warning, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { onDisconnect(c.id) })
            } else {
                Text("[ CONNECT ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { onConnect(c.id) })
            }
        }
        if (c.connected) {
            Spacer(Modifier.height(4.dp))
            Text("● connected — syncing on the box", color = TerminalDim,
                style = MaterialTheme.typography.labelMedium)
        }
    }
}
