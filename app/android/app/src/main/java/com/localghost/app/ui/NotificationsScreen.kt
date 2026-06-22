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
import com.localghost.app.net.PendingNotification
import com.localghost.app.ui.theme.*

@Composable
fun NotificationsScreen(items: Loadable<List<PendingNotification>>) {
    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp)) {
        item {
            Spacer(Modifier.height(12.dp))
            SectionLabel("QUEUED BY DAEMONS")
            Spacer(Modifier.height(8.dp))
        }
        when (items) {
            is Loadable.Loading -> item { LoadingRow() }
            is Loadable.Failed -> item { ErrorLine(items.reason) }
            is Loadable.Loaded -> if (items.value.isEmpty()) item {
                EmptyLine("queue empty. Daemons on the box add items here; the phone polls " +
                    "every 15 minutes. Nothing is pushed through a third party.")
            } else items(items.value) { n ->
                Column(Modifier.fillMaxWidth().border(1.dp, TerminalDim, RectangleShape)
                    .background(VoidLighter).padding(14.dp)) {
                    Text(n.daemonId, color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
                    Spacer(Modifier.height(4.dp))
                    Text(n.title, color = GhostText, style = MaterialTheme.typography.titleMedium)
                    Spacer(Modifier.height(2.dp))
                    Text(n.body, color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
                }
            }
        }
        item { Spacer(Modifier.height(24.dp)) }
    }
}
