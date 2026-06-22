package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import com.localghost.app.ui.theme.*

/**
 * A destructive-action gate. The user must type [requireWord] exactly to enable proceed.
 * No "are you sure?" hand-holding — a deliberate act, in the brand's no-excuses register.
 */
@Composable
fun ConfirmDialog(
    title: String,
    body: String,
    requireWord: String,
    confirmLabel: String,
    onConfirm: () -> Unit,
    onDismiss: () -> Unit,
) {
    var typed by remember { mutableStateOf("") }
    val armed = typed.trim() == requireWord

    Dialog(onDismissRequest = onDismiss) {
        Column(
            Modifier.fillMaxWidth().border(1.dp, Warning, RectangleShape).background(Void).padding(20.dp)
        ) {
            Text("> $title", color = Warning, style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(12.dp))
            Text(body, color = GhostText, style = MaterialTheme.typography.bodyMedium)
            Spacer(Modifier.height(16.dp))
            Text("Type $requireWord to proceed.", color = GhostTextDim,
                style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(8.dp))
            OutlinedTextField(
                value = typed, onValueChange = { typed = it }, singleLine = true,
                modifier = Modifier.fillMaxWidth(),
                colors = OutlinedTextFieldDefaults.colors(
                    focusedTextColor = GhostText, unfocusedTextColor = GhostText,
                    cursorColor = Warning,
                    focusedBorderColor = Warning, unfocusedBorderColor = GhostBorder,
                    focusedContainerColor = VoidLighter, unfocusedContainerColor = VoidLighter),
            )
            Spacer(Modifier.height(16.dp))
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.End,
                verticalAlignment = Alignment.CenterVertically) {
                GhostButton("CANCEL", onDismiss)
                Spacer(Modifier.width(12.dp))
                OutlinedButton(
                    onClick = { if (armed) onConfirm() }, enabled = armed,
                    shape = RectangleShape,
                    border = androidx.compose.foundation.BorderStroke(1.dp, if (armed) Warning else GhostBorder),
                    colors = ButtonDefaults.outlinedButtonColors(
                        contentColor = Warning, disabledContentColor = GhostBorder),
                ) { Text("[ $confirmLabel ]", style = MaterialTheme.typography.labelLarge) }
            }
        }
    }
}
