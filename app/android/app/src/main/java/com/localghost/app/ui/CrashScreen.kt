package com.localghost.app.ui

import android.content.ClipData
import android.content.ClipboardManager
import android.content.Context
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import com.localghost.app.ui.theme.GhostText
import com.localghost.app.ui.theme.Warning

@Composable
fun CrashScreen(report: String, onDismiss: () -> Unit) {
    val ctx = LocalContext.current
    GhostScaffold { pad ->
        Column(Modifier.fillMaxSize().padding(pad).padding(24.dp)) {
            SectionLabel("FATAL — LAST RUN")
            Spacer(Modifier.height(12.dp))
            Text("The previous session crashed. Trace below.", color = Warning,
                style = MaterialTheme.typography.bodyMedium)
            Spacer(Modifier.height(16.dp))
            Row(Modifier.fillMaxWidth()) {
                GhostButton("COPY", { copy(ctx, report) }, modifier = Modifier.weight(1f))
                Spacer(Modifier.width(12.dp))
                GhostButton("DISMISS", onDismiss, modifier = Modifier.weight(1f))
            }
            Spacer(Modifier.height(16.dp))
            Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState())) {
                Text(report, color = GhostText, style = MaterialTheme.typography.labelMedium)
                Spacer(Modifier.height(24.dp))
            }
        }
    }
}

private fun copy(ctx: Context, text: String) {
    (ctx.getSystemService(Context.CLIPBOARD_SERVICE) as ClipboardManager)
        .setPrimaryClip(ClipData.newPlainText("LocalGhost crash", text))
}
