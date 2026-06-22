package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import com.localghost.app.ui.theme.*

@Composable
fun ChangeCodeDialog(
    onConfirm: (old: String, new: String) -> Unit,
    onDismiss: () -> Unit,
) {
    var old by remember { mutableStateOf("") }
    var new by remember { mutableStateOf("") }
    var confirm by remember { mutableStateOf("") }
    val valid = old.isNotEmpty() && new.length >= 4 && new == confirm

    Dialog(onDismissRequest = onDismiss) {
        Column(Modifier.fillMaxWidth().border(1.dp, Warning, RectangleShape).background(Void).padding(20.dp)) {
            Text("> CHANGE CODE", color = Warning, style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(12.dp))
            Text("This re-keys the box. The old key is destroyed and the data with it. " +
                 "There is no recovery.", color = Warning, style = MaterialTheme.typography.bodyMedium)
            Spacer(Modifier.height(16.dp))
            field("current code", old) { old = it }
            Spacer(Modifier.height(8.dp))
            field("new code", new) { new = it }
            Spacer(Modifier.height(8.dp))
            field("confirm new code", confirm) { confirm = it }
            Spacer(Modifier.height(16.dp))
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.End,
                verticalAlignment = Alignment.CenterVertically) {
                GhostButton("CANCEL", onDismiss)
                Spacer(Modifier.width(12.dp))
                OutlinedButton(
                    onClick = { if (valid) onConfirm(old, new) }, enabled = valid,
                    shape = RectangleShape,
                    border = androidx.compose.foundation.BorderStroke(1.dp, if (valid) Warning else GhostBorder),
                    colors = ButtonDefaults.outlinedButtonColors(
                        contentColor = Warning, disabledContentColor = GhostBorder),
                ) { Text("[ RE-KEY & WIPE ]", style = MaterialTheme.typography.labelLarge) }
            }
        }
    }
}

@Composable
private fun field(label: String, value: String, onChange: (String) -> Unit) {
    OutlinedTextField(
        value = value, onValueChange = onChange, singleLine = true,
        label = { Text(label, color = GhostTextDim) },
        visualTransformation = PasswordVisualTransformation(),
        modifier = Modifier.fillMaxWidth(),
        colors = OutlinedTextFieldDefaults.colors(
            focusedTextColor = GhostText, unfocusedTextColor = GhostText, cursorColor = Warning,
            focusedBorderColor = Warning, unfocusedBorderColor = GhostBorder,
            focusedContainerColor = VoidLighter, unfocusedContainerColor = VoidLighter),
    )
}
