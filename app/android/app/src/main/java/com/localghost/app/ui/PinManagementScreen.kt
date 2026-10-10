package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.*
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.unit.dp
import com.localghost.app.net.DeviceInfo
import com.localghost.app.ui.theme.*
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.runtime.rememberCoroutineScope
import kotlinx.coroutines.launch
import androidx.compose.foundation.clickable
import androidx.compose.runtime.remember
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.getValue
import androidx.compose.runtime.setValue

/**
 * The CODES screen. PIN management was deliberately removed from the app: a PIN can only be changed
 * or reset at the box, over a local-network SSH session, never from a phone. This is so a coerced
 * phone cannot change or reset a PIN and lock you out or take over the account. The screen now shows
 * the enrolled devices and points to the box commands for PIN changes.
 */
@Composable
fun PinManagementScreen(
    devices: Loadable<List<DeviceInfo>>,
) {
    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp)) {
        item {
            Spacer(Modifier.height(12.dp))
            SectionLabel("CODES")
            Spacer(Modifier.height(8.dp))
            Text("Codes are managed at the box, not in the app. To change a code (keeps your data) " +
                 "run `ghost.secd changepin-<slot>`; to reset one (wipes that slot and starts " +
                 "fresh) run `ghost.secd resetup-<slot>`. Both work only over a local-network SSH " +
                 "session, so a phone, even unlocked under coercion, cannot change or reset a code.",
                 color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
            Spacer(Modifier.height(20.dp))
            SectionLabel("DEVICES")
            Spacer(Modifier.height(8.dp))
            Text("Each device enrols separately and syncs on its own cursor. The box dedups " +
                 "by content, so the same photo from two devices is one memory.",
                 color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
            Spacer(Modifier.height(6.dp))
        }
        when (devices) {
            is Loadable.Loading -> item { LoadingRow("reading devices…") }
            is Loadable.Failed -> item { ErrorLine(devices.reason) }
            is Loadable.Loaded -> items(devices.value) { d -> DeviceRow(d) }
        }
        item { Spacer(Modifier.height(24.dp)) }
    }
}

@Composable
private fun DeviceRow(d: DeviceInfo) {
    // RENAME , every enrolled phone shares this archive, so labelling one from another is
    // bookkeeping. The box keeps the name against the phone's stable id, so it survives the
    // next reinstall along with its sync position.
    val ctx = androidx.compose.ui.platform.LocalContext.current
    val scope = rememberCoroutineScope()
    var draft by remember(d.id) { mutableStateOf(d.name) }
    var saved by remember(d.id) { mutableStateOf(false) }
    Column(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape)
        .background(VoidLighter).padding(14.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(d.name, color = TerminalGreen, style = MaterialTheme.typography.titleMedium)
            if (d.thisDevice) {
                Spacer(Modifier.width(8.dp))
                Text("[ this device ]", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            }
        }
        if (d.model.isNotEmpty() && d.model != d.name) {
            Text(d.model, color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        }
        Text("id ${d.id.take(8)} , from its certificate, not a serial number",
            color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        Spacer(Modifier.height(4.dp))
        Text("${d.frames} ${if (d.frames == 1L) "frame" else "frames"} archived from this phone · last sync ${ago(d.lastSyncTs)}",
            color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        Spacer(Modifier.height(6.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            BasicTextField(value = draft, onValueChange = { draft = it.take(40); saved = false },
                textStyle = MaterialTheme.typography.labelMedium.copy(color = TerminalGreen),
                cursorBrush = SolidColor(TerminalGreen),
                modifier = Modifier.weight(1f).border(1.dp, GhostBorder, RectangleShape).padding(8.dp))
            Spacer(Modifier.width(8.dp))
            Text(if (saved) "[ saved ]" else "[ name it ]", color = TerminalGreen,
                style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable {
                    scope.launch {
                        saved = com.localghost.app.net.BoxClient.setDeviceName(
                            ctx, draft.trim(), "", device = d.id)
                    }
                })
        }
        Text("newest photo offered ${stamp(d.lastPhotoTs)}", color = GhostTextDim,
            style = MaterialTheme.typography.labelMedium)
        Text("newest video offered ${stamp(d.lastVideoTs)}", color = GhostTextDim,
            style = MaterialTheme.typography.labelMedium)
    }
}

/** "3h ago" for a wall-clock epoch, "never" for zero , the box reports seconds. */
private fun ago(ts: Long): String {
    if (ts <= 0) return "never"
    val d = (System.currentTimeMillis() / 1000) - ts
    return when {
        d < 90 -> "just now"
        d < 3600 -> "${d / 60}m ago"
        d < 86400 -> "${d / 3600}h ago"
        else -> "${d / 86400}d ago"
    }
}

/** A cursor position is the taken_at of the newest item that device has offered. */
private fun stamp(ts: Long): String {
    if (ts <= 0) return "none yet"
    val f = java.text.SimpleDateFormat("d MMM yyyy, HH:mm", java.util.Locale.UK)
    return f.format(java.util.Date(ts * 1000))
}
