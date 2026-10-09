package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import com.localghost.app.ui.theme.*

/** The ⓘ beside a section's label: opens what the section is made of (Explain). */
@Composable
fun InfoButton(topic: String, modifier: Modifier = Modifier) {
    var open by remember { mutableStateOf(false) }
    Text("ⓘ", color = TerminalDim, style = MaterialTheme.typography.labelMedium,
        modifier = modifier.clickable { open = true }.padding(horizontal = 6.dp, vertical = 2.dp))
    if (open) InfoSheet(topic) { open = false }
}

/** The explanation itself: a dialog in the app's frame, the paragraphs, CLOSE. */
@Composable
fun InfoSheet(topic: String, onClose: () -> Unit) {
    val t = Explain.of(topic) ?: return
    Dialog(onDismissRequest = onClose) {
        Column(Modifier.fillMaxWidth().border(1.dp, TerminalGreen, RectangleShape).background(Void).padding(18.dp)) {
            Text(t.title.uppercase(), color = TerminalGreen, style = MaterialTheme.typography.titleMedium)
            Spacer(Modifier.height(10.dp))
            Column(Modifier.weight(1f, fill = false).verticalScroll(rememberScrollState())) {
                t.paragraphs.forEach { p ->
                    Text(p, color = GhostText, style = MaterialTheme.typography.bodySmall)
                    Spacer(Modifier.height(10.dp))
                }
            }
            GhostButton("CLOSE", onClose, modifier = Modifier.fillMaxWidth())
        }
    }
}

/** A plain question with two answers, in the app's frame: for an act worth a second tap that is
 *  not destructive (a fetch of tens of gigabytes). ConfirmDialog is the gate for the destructive. */
@Composable
fun AskDialog(title: String, body: String, confirmLabel: String, onConfirm: () -> Unit, onDismiss: () -> Unit) {
    Dialog(onDismissRequest = onDismiss) {
        Column(Modifier.fillMaxWidth().border(1.dp, TerminalGreen, RectangleShape).background(Void).padding(18.dp)) {
            Text(title, color = TerminalGreen, style = MaterialTheme.typography.titleMedium)
            Spacer(Modifier.height(10.dp))
            Text(body, color = GhostText, style = MaterialTheme.typography.bodySmall)
            Spacer(Modifier.height(14.dp))
            GhostButton(confirmLabel, onConfirm, modifier = Modifier.fillMaxWidth())
            Spacer(Modifier.height(6.dp))
            Text("[ not now ]", color = TerminalDim, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable { onDismiss() }.padding(vertical = 4.dp))
        }
    }
}
