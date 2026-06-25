package com.localghost.app.ui

import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Switch
import androidx.compose.material3.SwitchDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import com.localghost.app.ui.theme.*

@Composable
fun SettingsScreen(
    allowMobileSync: Boolean,
    onToggleMobileSync: (Boolean) -> Unit,
    notificationsMuted: Boolean,
    onToggleMute: (Boolean) -> Unit,
    onExport: () -> Unit,
    exportState: String?,
    onChangeCode: () -> Unit,
    onWipe: () -> Unit,
) {
    Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState())
        .padding(20.dp).padding(bottom = 24.dp)) {
        SectionLabel("SETTINGS")
        Spacer(Modifier.height(20.dp))

        SectionLabel("SYNC")
        Spacer(Modifier.height(8.dp))
        toggleRow(
            label = "sync over mobile data",
            sub = if (allowMobileSync) "on, uses Wi-Fi and mobile (4G/5G)"
                  else "off, Wi-Fi only (recommended)",
            checked = allowMobileSync, onChange = onToggleMobileSync,
        )

        Spacer(Modifier.height(24.dp))
        SectionLabel("NOTIFICATIONS")
        Spacer(Modifier.height(8.dp))
        toggleRow(
            label = "daemon notifications",
            sub = if (notificationsMuted) "muted, daemons stay silent"
                  else "active, daemons can notify you",
            checked = !notificationsMuted, onChange = { on -> onToggleMute(!on) },
        )

        Spacer(Modifier.height(24.dp))
        SectionLabel("YOUR DATA")
        Spacer(Modifier.height(8.dp))
        Text("The box holds the index. The phone holds nothing. These act on the box.",
            color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        Spacer(Modifier.height(12.dp))

        GhostButton("EXPORT TO JSON", onExport, modifier = Modifier.fillMaxWidth())
        exportState?.let {
            Spacer(Modifier.height(8.dp))
            Text("> $it", color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
        }

        Spacer(Modifier.height(12.dp))
        GhostButton("CHANGE CODE", onChangeCode, modifier = Modifier.fillMaxWidth())
        Spacer(Modifier.height(4.dp))
        Text("Changing the code re-keys the box. The old key is destroyed. The data goes " +
             "with it. There is no recovery. That is the design.",
             color = Warning, style = MaterialTheme.typography.labelMedium)

        Spacer(Modifier.height(20.dp))
        SectionLabel("DESTRUCTIVE")
        Spacer(Modifier.height(8.dp))
        WipeButton(onWipe)
        Spacer(Modifier.height(4.dp))
        Text("Crypto-erase, global. The master key is destroyed on the box and every " +
             "persona's data becomes noise at once. Nobody reverses this, including you.",
             color = Warning, style = MaterialTheme.typography.labelMedium)

        Spacer(Modifier.height(28.dp))
        Text("> the only cloud is you", color = GhostTextDim,
            style = MaterialTheme.typography.labelMedium)
    }
}

@Composable
private fun toggleRow(label: String, sub: String, checked: Boolean, onChange: (Boolean) -> Unit) {
    Row(Modifier.fillMaxWidth().padding(vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(label, color = GhostText, style = MaterialTheme.typography.bodyMedium)
            Text(sub, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        }
        Switch(
            checked = checked, onCheckedChange = onChange,
            colors = SwitchDefaults.colors(
                checkedThumbColor = Void, checkedTrackColor = TerminalGreen,
                uncheckedThumbColor = GhostTextDim, uncheckedTrackColor = VoidLighter,
                uncheckedBorderColor = GhostBorder,
            ),
        )
    }
}

@Composable
private fun WipeButton(onWipe: () -> Unit) {
    androidx.compose.material3.OutlinedButton(
        onClick = onWipe,
        shape = androidx.compose.ui.graphics.RectangleShape,
        border = androidx.compose.foundation.BorderStroke(1.dp, Warning),
        colors = androidx.compose.material3.ButtonDefaults.outlinedButtonColors(contentColor = Warning),
        modifier = Modifier.fillMaxWidth(),
    ) { Text("[ WIPE EVERYTHING ]", style = MaterialTheme.typography.labelLarge) }
}
