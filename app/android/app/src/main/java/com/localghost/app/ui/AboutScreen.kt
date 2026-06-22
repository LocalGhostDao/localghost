package com.localghost.app.ui

import androidx.compose.foundation.layout.*
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import com.localghost.app.ui.theme.GhostText
import com.localghost.app.ui.theme.GhostTextDim
import com.localghost.app.ui.theme.TerminalGreen

@Composable
fun AboutScreen() {
    Column(Modifier.fillMaxSize().padding(20.dp)) {
        SectionLabel("ABOUT")
        Spacer(Modifier.height(16.dp))
        Text("LOCALGHOST", color = TerminalGreen, style = MaterialTheme.typography.titleLarge)
        Spacer(Modifier.height(8.dp))
        Text("> the only cloud is you", color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
        Spacer(Modifier.height(24.dp))
        Text("All processing and storage runs on hardware you own. This app is a " +
             "thin client: it authenticates over mTLS, syncs media, and reads results. It " +
             "holds no index and no model. If you lose the phone, the data is on the box, not " +
             "in your pocket.",
             color = GhostText, style = MaterialTheme.typography.bodyMedium)
    }
}
