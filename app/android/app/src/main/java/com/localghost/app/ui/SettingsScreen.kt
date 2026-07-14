package com.localghost.app.ui

import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Switch
import androidx.compose.material3.SwitchDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import com.localghost.app.ui.theme.*

@Composable
fun SettingsScreen(
    onOpenVerify: () -> Unit = {},
    allowMobileSync: Boolean,
    onToggleMobileSync: (Boolean) -> Unit,
    thinkLevel: String = "",
    onCycleThink: () -> Unit = {},
    notificationsMuted: Boolean,
    onToggleMute: (Boolean) -> Unit,
    onExport: () -> Unit,
    exportState: String?,
    onLock: () -> Unit,
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
        SectionLabel("CHAT")
        Spacer(Modifier.height(8.dp))
        // Deliberation depth for every chat answer. Tapping cycles off -> brief -> deep. Honest
        // mechanics: this asks the model to show its working (and gives it a bigger token budget) ,
        // deeper means slower, especially on CPU.
        Row(Modifier.fillMaxWidth().clickable { onCycleThink() }.padding(vertical = 8.dp)) {
            Column(Modifier.weight(1f)) {
                Text("thinking", color = GhostText, style = MaterialTheme.typography.bodyLarge)
                Text(when (thinkLevel) {
                    "brief" -> "brief , a few lines of reasoning first (slower)"
                    "deep" -> "deep , thorough reasoning first (much slower)"
                    else -> "off , answers directly (fastest)"
                }, color = GhostTextDim, style = MaterialTheme.typography.bodySmall)
            }
            Text(when (thinkLevel) { "brief" -> "[ BRIEF ]"; "deep" -> "[ DEEP ]"; else -> "[ OFF ]" },
                color = TerminalGreen, style = MaterialTheme.typography.bodyMedium)
        }

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
        Text("PIN changes happen at the box, not in the app. Run `ghost.secd changepin-<slot>` " +
             "(keeps your data) or `ghost.secd resetup-<slot>` (wipes and starts fresh) over a " +
             "local-network SSH session. A coerced phone cannot change or reset a PIN.",
             color = GhostTextDim, style = MaterialTheme.typography.labelMedium)

        Spacer(Modifier.height(24.dp))
        SectionLabel("SESSION")
        Spacer(Modifier.height(8.dp))
        GhostButton("LOCK BOX NOW", onLock, modifier = Modifier.fillMaxWidth())
        Spacer(Modifier.height(4.dp))
        Text("Spins the box down: stops the databases, unmounts the drive, and drops the key from " +
             "memory. The box goes dark until you enter your PIN again. Your data is untouched.",
             color = GhostTextDim, style = MaterialTheme.typography.labelMedium)

        Spacer(Modifier.height(20.dp))
        SectionLabel("TRUST")
        Spacer(Modifier.height(8.dp))
        GhostButton("VERIFY BUILD ✓", onOpenVerify, modifier = Modifier.fillMaxWidth())
        Spacer(Modifier.height(4.dp))
        Text("Checks that what the box is running matches the public source. An audit action, not " +
             "a daily one , which is why it lives here instead of taking a menu slot.",
             color = GhostTextDim, style = MaterialTheme.typography.labelMedium)

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
    var confirming by remember { mutableStateOf(false) }
    androidx.compose.material3.OutlinedButton(
        onClick = { confirming = true },
        shape = androidx.compose.ui.graphics.RectangleShape,
        border = androidx.compose.foundation.BorderStroke(1.dp, Warning),
        colors = androidx.compose.material3.ButtonDefaults.outlinedButtonColors(contentColor = Warning),
        modifier = Modifier.fillMaxWidth(),
    ) { Text("[ WIPE EVERYTHING ]", style = MaterialTheme.typography.labelLarge) }

    if (confirming) {
        ConfirmDialog(
            title = "WIPE THIS PHONE",
            body = "This destroys the box connection, the device certificate, and the identity key on " +
                "this phone. The box keeps your data. To use this phone again you re-pair it, which " +
                "needs to be done at home with your security key. There is no undo from here.",
            requireWord = "WIPE",
            confirmLabel = "WIPE EVERYTHING",
            onConfirm = { confirming = false; onWipe() },
            onDismiss = { confirming = false },
        )
    }
}
