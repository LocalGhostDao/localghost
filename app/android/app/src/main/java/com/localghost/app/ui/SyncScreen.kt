package com.localghost.app.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.unit.dp
import com.localghost.app.ui.theme.*
import kotlinx.coroutines.launch

@Composable
fun SyncScreen(
    sync: SyncUiState,
    onSync: () -> Unit,
    onRequestFullAccess: () -> Unit,
    onTestNotification: () -> Unit,
    onTogglePause: () -> Unit = {},
) {
    Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(20.dp)) {
        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.SpaceBetween,
            verticalAlignment = Alignment.CenterVertically) {
            SectionLabel("SYNC")
            // Top-right pause: honored by BOTH the 15-min periodic worker and the auto/manual
            // one-shots (the worker checks the flag before doing anything). Resume does not itself
            // start a sync , the next trigger (button, app open, timer) does.
            Text(if (sync.paused) "[ RESUME ]" else "[ PAUSE ]",
                color = if (sync.paused) Warning else TerminalGreen,
                style = MaterialTheme.typography.bodyMedium,
                modifier = Modifier.clickable { onTogglePause() })
        }
        Spacer(Modifier.height(8.dp))
        if (sync.paused) {
            Text("! sync is paused , nothing uploads until you resume", color = Warning,
                style = MaterialTheme.typography.bodySmall)
            Spacer(Modifier.height(8.dp))
        }
        Text("New photos and videos are copied to the box every 15 minutes over Wi-Fi, and " +
             "when you open the app. Originals stay on your phone; copies live on your box. " +
             "Nothing is uploaded anywhere else.",
             color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
        Spacer(Modifier.height(16.dp))
        SectionLabel("ACCESS")
        Spacer(Modifier.height(8.dp))
        val pctx = androidx.compose.ui.platform.LocalContext.current
        val openSettings = {
            pctx.startActivity(android.content.Intent(
                android.provider.Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
                android.net.Uri.parse("package:" + pctx.packageName)))
        }
        grantLine("camera photos",
            when { sync.hasImages -> "FULL"; sync.partial -> "PARTIAL"; else -> "none" },
            ok = sync.hasImages, warn = sync.partial,
            rationale = "without this nothing syncs , PARTIAL means Android is hiding most of your photos from the ghost",
            onFix = onRequestFullAccess)
        grantLine("camera videos", if (sync.hasVideo) "FULL" else "none", ok = sync.hasVideo, warn = false,
            rationale = "videos stay on the phone until granted", onFix = onRequestFullAccess)
        grantLine("location (unredacted)", if (sync.hasLocation) "GRANTED" else "none",
            ok = sync.hasLocation, warn = false,
            rationale = "without this Android strips GPS from synced photos , no places, no map dots",
            onFix = onRequestFullAccess)
        Spacer(Modifier.height(4.dp))
        Text("> every grant feeds ONLY your box. denying one disables its feature, nothing else.",
            color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        Text("> permanently denied? [ open system settings ]", color = GhostTextDim,
            style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.clickable { openSettings() })
        run {
            // HEALTH , Samsung Health writes into the phone's Health Connect store; this reads it
            // there and ships steps/sleep/exercise to the box (tallyd). The data's only network
            // hop is phone -> box, same channel as photos. Grant once in the system sheet; sync
            // ships the last 7 days, upserted, so tapping again only refines.
            val hctx = androidx.compose.ui.platform.LocalContext.current
            val scope = androidx.compose.runtime.rememberCoroutineScope()
            var healthMsg by remember { mutableStateOf("") }
            var granted by remember { mutableStateOf(false) }
            var grantedCount by remember { mutableStateOf(0) }
            LaunchedEffect(Unit) {
                if (com.localghost.app.sync.HealthSync.available(hctx)) {
                    grantedCount = com.localghost.app.sync.HealthSync.grantedCount(hctx)
                    granted = grantedCount == com.localghost.app.sync.HealthSync.PERMISSIONS.size
                }
            }
            val permLauncher = androidx.activity.compose.rememberLauncherForActivityResult(
                androidx.health.connect.client.PermissionController.createRequestPermissionResultContract()) { g ->
                granted = g.containsAll(com.localghost.app.sync.HealthSync.PERMISSIONS)
            }
            val total = com.localghost.app.sync.HealthSync.PERMISSIONS.size
            grantLine("health (steps · sleep · heart · more)",
                when { granted -> "FULL"; grantedCount > 0 -> "$grantedCount/$total"; else -> "none" },
                ok = granted, warn = grantedCount in 1 until total,
                rationale = "partial is fine , sync ships what is granted and names what it skipped",
                onFix = { permLauncher.launch(com.localghost.app.sync.HealthSync.PERMISSIONS) })
            Spacer(Modifier.height(8.dp))
            if (com.localghost.app.sync.HealthSync.available(hctx)) {
                GhostButton(if (granted) "SYNC HEALTH (7 DAYS)" else "CONNECT HEALTH",
                    onClick = {
                        if (!granted) permLauncher.launch(com.localghost.app.sync.HealthSync.PERMISSIONS)
                        else scope.launch {
                            healthMsg = "reading health connect…"
                            val res = com.localghost.app.sync.HealthSync.sync(hctx)
                            val skip = if (res.skipped.isEmpty()) "" else
                                " (skipped: ${res.skipped.joinToString(", ")})"
                            healthMsg = when {
                                res.error != null -> "! ${res.error}$skip"
                                res.days > 0 -> "shipped ${res.days} day(s) to your box$skip"
                                else -> "no health data found for the last 7 days$skip"
                            }
                        }
                    }, modifier = Modifier.fillMaxWidth())
                if (healthMsg.isNotEmpty()) {
                    Spacer(Modifier.height(6.dp))
                    Text(healthMsg, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                }
            } else {
                Text("> Health Connect not available on this device", color = TerminalDim,
                    style = MaterialTheme.typography.labelMedium)
            }
        }
        Spacer(Modifier.height(20.dp))
        GhostButton("SYNC NOW", onSync, enabled = !sync.busy, modifier = Modifier.fillMaxWidth())
        if (sync.partial) {
            Spacer(Modifier.height(12.dp))
            GhostButton("GRANT FULL ACCESS", onRequestFullAccess, modifier = Modifier.fillMaxWidth())
        }
        if (sync.busy) {
            if (sync.photoTotal > 0) { Spacer(Modifier.height(20.dp)); bar("PHOTOS", sync.photoDone, sync.photoTotal) }
            if (sync.videoTotal > 0) {
                Spacer(Modifier.height(16.dp)); bar("VIDEOS", sync.videoDone, sync.videoTotal)
                if (sync.curVideoSize > 0) {
                    Spacer(Modifier.height(10.dp))
                    val frac = (sync.curVideoRead.toFloat() / sync.curVideoSize).coerceIn(0f, 1f)
                    Text("  ${sync.curVideoName}  ${mb(sync.curVideoRead)}/${mb(sync.curVideoSize)}",
                        color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                    Spacer(Modifier.height(4.dp))
                    LinearProgressIndicator(progress = { frac }, color = TerminalDim, trackColor = VoidLighter,
                        strokeCap = StrokeCap.Butt, modifier = Modifier.fillMaxWidth().height(4.dp))
                }
            }
            // Throughput + ETA + total-bytes progress , the "how long is this going to take" line.
            if (sync.bytesTotal > 0) {
                Spacer(Modifier.height(16.dp))
                val overall = (sync.bytesSent.toFloat() / sync.bytesTotal).coerceIn(0f, 1f)
                LinearProgressIndicator(progress = { overall }, color = TerminalGreen, trackColor = VoidLighter,
                    strokeCap = StrokeCap.Butt, modifier = Modifier.fillMaxWidth().height(6.dp))
                Spacer(Modifier.height(6.dp))
                val speed = if (sync.speedBps > 0) speedStr(sync.speedBps) else "measuring…"
                val eta = if (sync.etaSeconds > 0) etaStr(sync.etaSeconds) else "estimating…"
                Text("${mb(sync.bytesSent)} / ${mb(sync.bytesTotal)}   ·   $speed   ·   $eta left",
                    color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            }
        }
        sync.status?.let {
            Spacer(Modifier.height(16.dp))
            Text((if (sync.isError) "! " else "> ") + it,
                color = if (sync.isError) Warning else GhostText, style = MaterialTheme.typography.bodyMedium)
        }
        Spacer(Modifier.height(28.dp))
        Text("> notifications and mobile-data sync live in Settings.",
            color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
    }
}

@Composable
private fun bar(label: String, done: Int, total: Int) {
    val frac = if (total > 0) (done.toFloat() / total).coerceIn(0f, 1f) else 0f
    Text("> $label  $done / $total  ·  ${(frac * 100).toInt()}%",
        color = TerminalGreen, style = MaterialTheme.typography.bodyMedium)
    Spacer(Modifier.height(6.dp))
    LinearProgressIndicator(progress = { frac }, color = TerminalGreen, trackColor = VoidLighter,
        strokeCap = StrokeCap.Butt, modifier = Modifier.fillMaxWidth().height(6.dp))
}

private fun mb(bytes: Long): String =
    if (bytes < 1024 * 1024) "${bytes / 1024}KB" else "%.1fMB".format(bytes / 1024.0 / 1024.0)

private fun speedStr(bps: Double): String = when {
    bps >= 1_000_000 -> "%.1f MB/s".format(bps / 1_000_000)
    bps >= 1_000 -> "%.0f KB/s".format(bps / 1_000)
    else -> "%.0f B/s".format(bps)
}

private fun etaStr(seconds: Long): String = when {
    seconds >= 3600 -> "%dh %dm".format(seconds / 3600, (seconds % 3600) / 60)
    seconds >= 60 -> "%dm %ds".format(seconds / 60, seconds % 60)
    else -> "${seconds}s"
}

@Composable
private fun grantLine(label: String, value: String, ok: Boolean, warn: Boolean,
                      rationale: String = "", onFix: (() -> Unit)? = null) {
    val fixable = !ok && onFix != null
    Column(Modifier.fillMaxWidth().padding(vertical = 4.dp)
        .let { if (fixable) it.clickable { onFix!!() } else it }) {
        Row {
            Text(if (ok) "[+] " else if (warn) "[~] " else "[ ] ",
                color = if (ok) TerminalGreen else if (warn) Warning else GhostTextDim,
                style = MaterialTheme.typography.bodyMedium)
            Text("$label  ", color = GhostText, style = MaterialTheme.typography.bodyMedium)
            Text(value, color = if (ok) TerminalGreen else if (warn) Warning else GhostTextDim,
                style = MaterialTheme.typography.bodyMedium)
            if (fixable) Text("  [ fix ]", color = TerminalGreen,
                style = MaterialTheme.typography.labelMedium)
        }
        if (rationale.isNotEmpty() && !ok) {
            Text("    $rationale", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        }
    }
}
