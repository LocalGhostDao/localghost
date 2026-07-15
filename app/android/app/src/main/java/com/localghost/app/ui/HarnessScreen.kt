package com.localghost.app.ui

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.clickable
import androidx.compose.ui.platform.LocalContext
import com.localghost.app.net.BoxClient
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
import androidx.compose.runtime.*
import com.localghost.app.net.DaemonStatus
import com.localghost.app.ui.theme.*

@Composable
fun HarnessScreen(daemons: Loadable<List<DaemonStatus>>) {
    // Tap a row , see its history. The stats names are the sampler's names; UI rows that present a
    // host vital under a friendlier id map here.
    var statsFor by remember { mutableStateOf<String?>(null) }
    statsFor?.let { name -> ServiceStatsDialog(name = name, onDismiss = { statsFor = null }) }

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
            is Loadable.Loaded -> items(daemons.value) { d ->
                DaemonRow(d, onClick = {
                    statsFor = when (d.id) {
                        "cpu" -> "host.cpu"; "memory" -> "host.mem"; "gpu" -> "host.gpu"
                        "system disk" -> "host.disk"
                        else -> d.id // daemons, postgres, redis, volume: sampler names match
                    }
                })
            }
        }
        item { Spacer(Modifier.height(24.dp)) }
    }
}

@Composable
private fun DaemonRow(d: DaemonStatus, onClick: () -> Unit = {}) {
    Column(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape)
        .background(VoidLighter).clickable { onClick() }.padding(14.dp)) {
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


/** One target's history: the last ~100 minutes at 10-second grain, the last 24 hours at minute
 *  grain, and the computed day line. Sparklines plot the numeric value where the target has one
 *  (load, GB, %, latency) and health-code otherwise , red segments are non-zero codes either way. */
@Composable
private fun ServiceStatsDialog(name: String, onDismiss: () -> Unit) {
    val ctx = LocalContext.current
    var stats by remember { mutableStateOf<BoxClient.ServiceStats?>(null) }
    var failed by remember { mutableStateOf(false) }
    LaunchedEffect(name) {
        stats = BoxClient.serviceStats(ctx, name)
        if (stats == null) failed = true
    }
    androidx.compose.ui.window.Dialog(onDismissRequest = onDismiss) {
        Column(Modifier.background(Void).border(1.dp, TerminalDim).padding(16.dp)) {
            Text(name, color = TerminalGreen, style = MaterialTheme.typography.titleMedium)
            Spacer(Modifier.height(8.dp))
            val st = stats
            when {
                failed -> Text("stats unavailable , the sampler needs a deploy + a few minutes of uptime",
                    color = Warning, style = MaterialTheme.typography.bodySmall)
                st == null -> Text("reading from the box…", color = GhostTextDim,
                    style = MaterialTheme.typography.bodySmall)
                else -> {
                    Text(st.day, color = GhostText, style = MaterialTheme.typography.bodySmall)
                    Spacer(Modifier.height(12.dp))
                    Text("LAST ~100 MIN · 10s", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                    Sparkline(st.s10)
                    Spacer(Modifier.height(10.dp))
                    Text("LAST 24H · 1m", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                    Sparkline(st.s1m)
                }
            }
            Spacer(Modifier.height(12.dp))
            GhostButton("CLOSE", onClick = onDismiss, modifier = Modifier.fillMaxWidth())
        }
    }
}

@Composable
private fun Sparkline(points: List<BoxClient.StatPoint>) {
    if (points.isEmpty()) {
        Text("no samples yet", color = GhostTextDim, style = MaterialTheme.typography.labelSmall)
        return
    }
    val series = points.asReversed() // stored newest-first; draw oldest -> newest
    val hasV = series.any { it.v != 0.0 }
    Canvas(Modifier.fillMaxWidth().height(56.dp).background(VoidLighter).padding(4.dp)) {
        val n = series.size
        if (n < 2) return@Canvas
        val vals = if (hasV) series.map { it.v.toFloat() } else series.map { it.c.toFloat() }
        val vMin = vals.min()
        val vMax = maxOf(vals.max(), vMin + 0.001f)
        val stepX = size.width / (n - 1)
        var prev: androidx.compose.ui.geometry.Offset? = null
        series.forEachIndexed { i, p ->
            val x = i * stepX
            val y = size.height - (vals[i] - vMin) / (vMax - vMin) * size.height
            val here = androidx.compose.ui.geometry.Offset(x, y)
            prev?.let { pr ->
                drawLine(
                    color = if (p.c > 0) Warning else TerminalGreen,
                    start = pr, end = here, strokeWidth = 2f,
                )
            }
            prev = here
        }
    }
}
