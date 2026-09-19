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
        item {
            // NOBODY-WATCHES-WATCHD, surfaced: the sampler writes every 10s; stale rings mean the
            // watcher itself (or Redis) is down, and everything below is the PAST wearing green.
            val hctx = androidx.compose.ui.platform.LocalContext.current
            var fresh by remember { mutableStateOf<BoxClient.StatsFreshness?>(null) }
            LaunchedEffect(Unit) { fresh = BoxClient.statsFreshness(hctx) }
            fresh?.let { f ->
                if (f.stale) {
                    Text("⚠ STALE , last sample " +
                        (if (f.ageSeconds >= 0) "${f.ageSeconds}s ago" else "never") +
                        ". The watcher itself may be down; rows below show the last known state, not the present.",
                        color = Warning, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.padding(bottom = 6.dp))
                }
            }
        }
        item { PipelinePanel() }
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

/**
 * THE ARCHIVE'S PROGRESS , the stock-take as a panel: every stage as a bar with done/total and
 * what is left, the description rate and the time the rest will take at that pace, searchd's
 * queue, and framed's own check while it runs. Polled every 5 s while the screen is open; the
 * numbers are the box's (/v1/pipeline), nothing is estimated on the phone.
 */
@Composable
private fun PipelinePanel() {
    val ctx = LocalContext.current
    var p by remember { mutableStateOf<BoxClient.Pipeline?>(null) }
    var unsupported by remember { mutableStateOf(false) }
    LaunchedEffect(Unit) {
        while (true) {
            val got = BoxClient.pipeline(ctx)
            if (got == null && p == null) unsupported = true else if (got != null) { p = got; unsupported = false }
            kotlinx.coroutines.delay(5_000)
        }
    }
    Column(Modifier.fillMaxWidth().border(1.dp, TerminalDim, RectangleShape).background(VoidLighter).padding(14.dp)) {
        Row {
            Text("◉", color = TerminalGreen, style = MaterialTheme.typography.bodyMedium)
            Spacer(Modifier.width(8.dp))
            Text("archive pipeline", color = TerminalGreen, style = MaterialTheme.typography.titleMedium)
            Spacer(Modifier.weight(1f))
            p?.let { Text("v${it.version}", color = GhostTextDim, style = MaterialTheme.typography.labelMedium) }
        }
        Spacer(Modifier.height(6.dp))
        val pl = p
        when {
            pl == null && unsupported -> Text("the box does not report pipeline progress yet , deploy the current build",
                color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            pl == null -> Text("reading from the box…", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            pl.total == 0 -> Text("no photos or videos archived yet", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            else -> {
                Text("${pl.photos} photos · ${pl.videos} videos", color = GhostText, style = MaterialTheme.typography.bodyMedium)
                Spacer(Modifier.height(8.dp))
                StageBar("at the latest stage", pl.atLatest, strong = true)
                StageBar("derived (v${pl.version})", pl.derived)
                StageBar("previewed", pl.previewed)
                StageBar("described", pl.described)
                StageBar("titled", pl.titled)
                StageBar("tagged", pl.tagged)
                Spacer(Modifier.height(8.dp))
                // Pace and ETA: the box counts descriptions landed in the last hour; the ETA is
                // the rest at that pace. No pace = nothing described in the last hour, which is
                // either done or a model that is not running; both are said plainly.
                val left = pl.described.left
                val pace = when {
                    left == 0 -> "every photo and video is described"
                    pl.describedLastHour > 0 -> "${pl.describedLastHour}/h · ${pl.describedLastDay} today · " +
                        "$left left, about ${eta(pl.etaSeconds)}"
                    pl.caption.pending > 0 -> "$left left, none described in the last hour , the vision model is idle or warming"
                    pl.caption.parked > 0 -> "$left left, ${pl.caption.parked} caption jobs parked (five failures each) , unpark or check oracled"
                    else -> "$left left, nothing queued , the next stock-take will queue them"
                }
                Text(pace, color = if (left == 0) TerminalGreen else GhostText, style = MaterialTheme.typography.labelMedium)
                Spacer(Modifier.height(4.dp))
                Text("queue · captions ${pl.caption.pending}${parked(pl.caption)} · tags ${pl.tag.pending}${parked(pl.tag)} · embeds ${pl.embed.pending}",
                    color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                pl.converge?.let { c ->
                    Spacer(Modifier.height(4.dp))
                    val line = when {
                        c.running -> "checking now · ${c.done} of ${c.toDo} rows handled · re-read ${c.rederived}, asked searchd for ${c.notified}" +
                            (if (c.unrenderable > 0) " · ${c.unrenderable} videos without a frame grab" else "")
                        c.finishedAt > 0 -> "last check ${ago(pl.now - c.finishedAt)}: ${c.summary}"
                        else -> "check pending"
                    }
                    Text(line, color = if (c.running) TerminalGreen else GhostTextDim, style = MaterialTheme.typography.labelMedium)
                }
            }
        }
    }
}

private fun parked(q: BoxClient.Queue) = if (q.parked > 0) " (+${q.parked} parked)" else ""

private fun eta(seconds: Long): String = when {
    seconds < 0 -> "unknown"
    seconds < 90 -> "a minute"
    seconds < 3600 -> "${(seconds + 30) / 60} min"
    seconds < 86400 -> "${(seconds + 1800) / 3600} h"
    else -> "${(seconds + 43200) / 86400} days"
}

private fun ago(seconds: Long): String = when {
    seconds < 60 -> "just now"
    seconds < 3600 -> "${seconds / 60} min ago"
    seconds < 86400 -> "${seconds / 3600} h ago"
    else -> "${seconds / 86400} days ago"
}

@Composable
private fun StageBar(label: String, s: BoxClient.Stage, strong: Boolean = false) {
    val colour = if (s.left == 0) TerminalGreen else if (strong) GhostText else GhostTextDim
    Column(Modifier.fillMaxWidth().padding(vertical = 3.dp)) {
        Row {
            Text(label, color = colour, style = MaterialTheme.typography.labelMedium, modifier = Modifier.weight(1f))
            Text(if (s.left == 0) "${s.done} · 100%" else "${s.done} of ${s.total} · ${s.pct}% · ${s.left} left",
                color = colour, style = MaterialTheme.typography.labelMedium)
        }
        Spacer(Modifier.height(2.dp))
        Canvas(Modifier.fillMaxWidth().height(if (strong) 8.dp else 4.dp)) {
            drawRect(color = Void)
            val w = if (s.total <= 0) size.width else size.width * s.done / s.total
            drawRect(color = if (s.left == 0) TerminalGreen else TerminalDim,
                size = androidx.compose.ui.geometry.Size(w.coerceIn(0f, size.width), size.height))
        }
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
    // The DRILL-IN , each daemon's domain from its own tables (/v1/daemon/summary), stacked above
    // the sparklines. Box Status is the menu; this dialog is the per-daemon screen.
    var detail by remember(name) { mutableStateOf<List<Pair<String, String>>?>(null) }
    LaunchedEffect(name) { detail = com.localghost.app.net.BoxClient.daemonSummary(ctx, name) }
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
            detail?.let { rows ->
                rows.forEach { (k, v) ->
                    if (v.length > 24) {
                        // Long values get the full width as a paragraph , squeezed into the row's
                        // leftover space they wrapped one letter per line, which read like the
                        // dialog was having a stroke.
                        Column(Modifier.fillMaxWidth().padding(vertical = 2.dp)) {
                            Text(k, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                            Text(v, color = GhostText, style = MaterialTheme.typography.labelMedium)
                        }
                    } else {
                        Row(Modifier.fillMaxWidth().padding(vertical = 2.dp)) {
                            Text("$k  ", color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
                                modifier = Modifier.weight(1f))
                            Text(v, color = GhostText, style = MaterialTheme.typography.labelMedium)
                        }
                    }
                }
                if (rows.isNotEmpty()) Spacer(Modifier.height(10.dp))
            }
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
