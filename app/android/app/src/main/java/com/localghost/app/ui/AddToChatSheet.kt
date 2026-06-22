package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.material3.*
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import com.localghost.app.net.ChatCapabilities
import com.localghost.app.ui.theme.*

/**
 * The "add to chat" sheet — sources to ingest, capabilities for the turn, and a route to
 * box-side connectors. Everything here either feeds the box index or scopes what the chat
 * may do. The one capability that leaves the box (reach) is off by default and flagged.
 */
@OptIn(ExperimentalMaterial3Api::class)
@Composable
fun AddToChatSheet(
    caps: ChatCapabilities,
    availableDaemons: List<String>,
    onCaps: (ChatCapabilities) -> Unit,
    forceLocal: Boolean,
    onForceLocal: (Boolean) -> Unit,
    localModelPresent: Boolean,
    onManageModels: () -> Unit,
    onCamera: () -> Unit,
    onPhotos: () -> Unit,
    onFiles: () -> Unit,
    onVoice: () -> Unit,
    onOpenConnectors: () -> Unit,
    onDismiss: () -> Unit,
) {
    ModalBottomSheet(onDismissRequest = onDismiss, containerColor = Void,
        contentColor = GhostText) {
        Column(Modifier.fillMaxWidth().padding(horizontal = 20.dp).padding(bottom = 24.dp)) {
            SectionLabel("ADD TO CHAT")
            Spacer(Modifier.height(14.dp))

            // source grid
            Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                SourceTile("◉", "CAMERA", Modifier.weight(1f), onCamera)
                SourceTile("▣", "PHOTOS", Modifier.weight(1f), onPhotos)
                SourceTile("⊟", "FILES", Modifier.weight(1f), onFiles)
                SourceTile("◍", "VOICE", Modifier.weight(1f), onVoice)
            }

            Spacer(Modifier.height(20.dp))
            SectionLabel("FOR THIS TURN")
            Spacer(Modifier.height(8.dp))

            // reach beyond the box — the only boundary-crossing capability, off by default
            ToggleRow(
                label = "reach beyond the box",
                sub = if (caps.reachBeyondBox) "this turn may fetch from the open web"
                      else "off — answers stay on the box",
                checked = caps.reachBeyondBox,
                emphasis = caps.reachBeyondBox,
                onChange = { onCaps(caps.copy(reachBeyondBox = it)) },
            )

            Spacer(Modifier.height(4.dp))
            ToggleRow(
                label = "use on-phone model",
                sub = when {
                    !localModelPresent -> "no local model installed"
                    forceLocal -> "forced — answers run on this phone, no life-index"
                    else -> "off — uses the box (falls back automatically if unreachable)"
                },
                checked = forceLocal,
                emphasis = forceLocal,
                onChange = { if (localModelPresent) onForceLocal(it) },
            )
            if (!localModelPresent) {
                Text("> get an on-phone model", color = TerminalGreen,
                    style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.padding(top = 2.dp, start = 2.dp)
                        .clickable { onManageModels() })
            }

            Spacer(Modifier.height(8.dp))
            Text("DAEMONS", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(4.dp))
            Text("ghost.synthd always answers. Add others as tools for this turn.",
                color = TerminalDim, style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(6.dp))
            availableDaemons.forEach { d ->
                val on = caps.daemons.contains(d)
                ToggleRow(
                    label = d, sub = "", checked = on, emphasis = false,
                    onChange = { add ->
                        val next = if (add) caps.daemons + d else caps.daemons - d
                        onCaps(caps.copy(daemons = next))
                    },
                )
            }

            Spacer(Modifier.height(20.dp))
            Row(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape)
                .clickable { onOpenConnectors() }.padding(14.dp),
                verticalAlignment = Alignment.CenterVertically) {
                Text("⊹", color = TerminalGreen, style = MaterialTheme.typography.titleMedium)
                Spacer(Modifier.width(12.dp))
                Column(Modifier.weight(1f)) {
                    Text("CONNECTORS", color = GhostText, style = MaterialTheme.typography.titleMedium)
                    Text("external sources the box pulls from", color = GhostTextDim,
                        style = MaterialTheme.typography.labelMedium)
                }
                Text(">", color = GhostTextDim, style = MaterialTheme.typography.titleMedium)
            }
        }
    }
}

@Composable
private fun SourceTile(glyph: String, label: String, modifier: Modifier, onClick: () -> Unit) {
    Column(modifier
        .border(1.dp, GhostBorder, RectangleShape)
        .clickable { onClick() }
        .padding(vertical = 16.dp),
        horizontalAlignment = Alignment.CenterHorizontally) {
        Text(glyph, color = TerminalGreen, style = MaterialTheme.typography.titleLarge)
        Spacer(Modifier.height(6.dp))
        Text(label, color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
            textAlign = TextAlign.Center)
    }
}

@Composable
private fun ToggleRow(label: String, sub: String, checked: Boolean, emphasis: Boolean, onChange: (Boolean) -> Unit) {
    Row(Modifier.fillMaxWidth().padding(vertical = 6.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(label, color = if (emphasis) Warning else GhostText,
                style = MaterialTheme.typography.bodyMedium)
            if (sub.isNotEmpty()) Text(sub,
                color = if (emphasis) Warning else GhostTextDim,
                style = MaterialTheme.typography.labelMedium)
        }
        Switch(checked = checked, onCheckedChange = onChange,
            colors = SwitchDefaults.colors(
                checkedThumbColor = Void,
                checkedTrackColor = if (emphasis) Warning else TerminalGreen,
                uncheckedThumbColor = GhostTextDim, uncheckedTrackColor = VoidLighter,
                uncheckedBorderColor = GhostBorder))
    }
}
