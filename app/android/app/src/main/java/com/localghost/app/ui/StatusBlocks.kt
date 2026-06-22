package com.localghost.app.ui

import androidx.compose.foundation.layout.*
import androidx.compose.material3.CircularProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import com.localghost.app.ui.theme.GhostTextDim
import com.localghost.app.ui.theme.TerminalDim
import com.localghost.app.ui.theme.TerminalGreen
import com.localghost.app.ui.theme.Warning

@Composable
fun LoadingRow(text: String = "reading from the box…") {
    Row(verticalAlignment = Alignment.CenterVertically,
        modifier = Modifier.padding(vertical = 8.dp)) {
        CircularProgressIndicator(color = TerminalGreen, strokeWidth = 2.dp,
            modifier = Modifier.size(16.dp))
        Spacer(Modifier.width(12.dp))
        Text(text, color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
    }
}

@Composable
fun EmptyLine(text: String) {
    Text(text, color = TerminalDim, style = MaterialTheme.typography.bodyMedium,
        modifier = Modifier.padding(vertical = 4.dp))
}

@Composable
fun ErrorLine(text: String) {
    Text("! $text", color = Warning, style = MaterialTheme.typography.bodyMedium,
        modifier = Modifier.padding(vertical = 4.dp))
}
