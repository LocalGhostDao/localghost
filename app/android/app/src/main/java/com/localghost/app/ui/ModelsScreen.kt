package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.LinearProgressIndicator
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.unit.dp
import com.localghost.app.net.PhoneModel
import com.localghost.app.ui.theme.*

/** State the host passes down per model id. */
data class ModelRowState(
    val installed: Boolean,
    val active: Boolean,
    val downloading: Boolean,
    val downloadedBytes: Long,
    val totalBytes: Long,
)

@Composable
fun ModelsScreen(
    models: List<PhoneModel>,
    stateOf: (String) -> ModelRowState,
    onDownload: (String) -> Unit,
    onCancel: (String) -> Unit,
    onActivate: (String) -> Unit,
    onDelete: (String) -> Unit,
) {
    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp)) {
        item {
            Spacer(Modifier.height(12.dp))
            SectionLabel("ON-PHONE MODELS")
            Spacer(Modifier.height(8.dp))
            Text("Models your box offers for this phone to run, for when the box is " +
                 "unreachable. You download them from the box into this app's storage; delete " +
                 "removes the local copy only. The box keeps it, so you can pull it again. " +
                 "They see none of your life-index; generic answers only.",
                 color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
            Spacer(Modifier.height(6.dp))
        }
        if (models.isEmpty()) {
            item { EmptyLine("the box is offering no phone-runnable models yet.") }
        } else {
            items(models) { m -> ModelRow(m, stateOf(m.id), onDownload, onCancel, onActivate, onDelete) }
        }
        item { Spacer(Modifier.height(24.dp)) }
    }
}

@Composable
private fun ModelRow(
    m: PhoneModel, st: ModelRowState,
    onDownload: (String) -> Unit, onCancel: (String) -> Unit,
    onActivate: (String) -> Unit, onDelete: (String) -> Unit,
) {
    Column(Modifier.fillMaxWidth().border(1.dp, if (st.active) TerminalGreen else GhostBorder, RectangleShape)
        .background(VoidLighter).padding(14.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Column(Modifier.weight(1f)) {
                Text(m.name, color = TerminalGreen, style = MaterialTheme.typography.titleMedium)
                Text("${gb(m.sizeBytes)} · ${m.detail}", color = GhostTextDim,
                    style = MaterialTheme.typography.labelMedium)
            }
            if (st.active) Text("● ACTIVE", color = TerminalGreen,
                style = MaterialTheme.typography.labelMedium)
        }

        Spacer(Modifier.height(10.dp))

        when {
            st.downloading -> {
                val frac = if (st.totalBytes > 0)
                    (st.downloadedBytes.toFloat() / st.totalBytes).coerceIn(0f, 1f) else 0f
                Text("▼ ${gb(st.downloadedBytes)} / ${gb(st.totalBytes)}",
                    color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                Spacer(Modifier.height(4.dp))
                LinearProgressIndicator(progress = { frac }, color = TerminalGreen,
                    trackColor = Void, strokeCap = StrokeCap.Butt,
                    modifier = Modifier.fillMaxWidth().height(2.dp))
                Spacer(Modifier.height(8.dp))
                Text("[ CANCEL ]", color = Warning, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { onCancel(m.id) })
            }
            st.installed -> Row {
                if (!st.active) Text("[ USE THIS ]", color = TerminalGreen,
                    style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { onActivate(m.id) }.padding(end = 16.dp))
                Text("[ DELETE ]", color = Warning, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { onDelete(m.id) })
            }
            else -> Text("[ DOWNLOAD ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable { onDownload(m.id) })
        }
    }
}

private fun gb(bytes: Long): String = "%.1f GB".format(bytes / 1_000_000_000.0)
