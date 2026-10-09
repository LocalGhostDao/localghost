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
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import kotlinx.coroutines.launch
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.unit.dp
import com.localghost.app.local.TransferRate
import com.localghost.app.net.PhoneModel
import com.localghost.app.ui.theme.*

/** State the host passes down per model id. */
data class ModelRowState(
    val installed: Boolean,
    val active: Boolean,
    val downloading: Boolean,
    val downloadedBytes: Long,
    val totalBytes: Long,
    val bytesPerSecond: Long = -1,  // -1 while the first second is measured
    val secondsLeft: Long = -1,
    val lastProgressAt: Long = 0,   // when the byte count last moved (wall clock), 0 before the first tick
)

/** One model download's latest progress, as the worker reported it. */
data class DownloadTick(
    val done: Long,
    val total: Long,
    val bytesPerSecond: Long = -1,
    val secondsLeft: Long = -1,
    val at: Long = System.currentTimeMillis(),
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
            Text("The phone's own model, from your box (never from the internet). It reads web pages into " +
                 "notes when the box is slow (its model on the CPU) so the box only has to read the notes, and " +
                 "it answers by itself, web search included, when the box cannot be reached. It sees none of " +
                 "your life-index. Delete removes the phone's copy only; the box keeps it.",
                 color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
            Spacer(Modifier.height(6.dp))
            // what this phone can actually do: the runtime in this build, the model's state, its speed
            val ctx = androidx.compose.ui.platform.LocalContext.current
            val built = remember { com.localghost.app.local.NativeLlama.ensureLibrary() }
            val speed = com.localghost.app.local.LocalModel.Speed
            val line = when {
                !built -> "this build carries no model runtime (its llama.cpp pin is not set) , the phone cannot run one"
                !com.localghost.app.local.LocalModel.isModelPresent(ctx) -> "runtime in this build (llama.cpp ${com.localghost.app.BuildConfig.LLAMA_CPP_COMMIT.take(8)}), no model downloaded yet"
                speed.measured(ctx) -> "reads %.0f tokens/s, writes %.1f tokens/s on this phone".format(java.util.Locale.US, speed.promptTps(ctx), speed.genTps(ctx)) +
                    (if (speed.loadMs(ctx) > 0) " · loads in %.1f s".format(java.util.Locale.US, speed.loadMs(ctx) / 1000.0) else "") +
                    " · " + com.localghost.app.local.LocalModel.state.name.lowercase() +
                    (com.localghost.app.local.LocalModel.lastFormat.takeIf { it.isNotEmpty() }?.let { " · prompt format $it" } ?: "")
                else -> "installed, not run yet , its speed shows here after the first answer"
            }
            Text(line, color = if (built) TerminalDim else Warning, style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(6.dp))
        }
        item { BenchmarkBlock() }
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
    var armed by remember(m.id) { mutableStateOf(false) } // DELETE tapped once: the next tap does it
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
                // a clock for "stalled": the worker only reports when bytes arrive
                val now: Long by produceState(System.currentTimeMillis()) {
                    while (true) { kotlinx.coroutines.delay(1_000); value = System.currentTimeMillis() }
                }
                val stalled = st.lastProgressAt > 0 && now - st.lastProgressAt > 10_000
                Row(verticalAlignment = Alignment.Bottom) {
                    Text("${(frac * 100).toInt()}%", color = TerminalGreen,
                        style = MaterialTheme.typography.titleMedium)
                    Spacer(Modifier.width(10.dp))
                    Text("${TransferRate.size(st.downloadedBytes)} of ${TransferRate.size(st.totalBytes)}",
                        color = TerminalDim, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.padding(bottom = 2.dp))
                }
                Spacer(Modifier.height(6.dp))
                LinearProgressIndicator(progress = { frac }, color = TerminalGreen,
                    trackColor = Void, strokeCap = StrokeCap.Butt,
                    modifier = Modifier.fillMaxWidth().height(6.dp))
                Spacer(Modifier.height(6.dp))
                val line = when {
                    st.lastProgressAt == 0L -> "waiting for the box…"
                    stalled -> "no data for ${(now - st.lastProgressAt) / 1000} s , waiting for the box (it picks up where it stopped)"
                    else -> "▼ " + TransferRate.rate(st.bytesPerSecond) +
                        TransferRate.left(st.secondsLeft).takeIf { it.isNotEmpty() }?.let { "  ·  $it" }.orEmpty()
                }
                Text(line, color = if (stalled) Warning else GhostTextDim,
                    style = MaterialTheme.typography.labelMedium)
                Spacer(Modifier.height(8.dp))
                Text("[ CANCEL ]", color = Warning, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { onCancel(m.id) })
            }
            st.installed -> Row {
                if (!st.active) Text("[ USE THIS ]", color = TerminalGreen,
                    style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { onActivate(m.id) }.padding(end = 16.dp))
                Text(if (armed) "[ SURE? ${gb(m.sizeBytes)} GOES ]" else "[ DELETE ]", color = Warning, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { if (armed) { armed = false; onDelete(m.id) } else armed = true })
            }
            else -> Text("[ DOWNLOAD ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable { onDownload(m.id) })
        }
    }
}

private fun gb(bytes: Long): String = "%.1f GB".format(bytes / 1_000_000_000.0)


/**
 * BENCHMARK: how this phone runs its model, measured by the runtime (PhoneBench). The latest run in
 * full (load, read and write speeds, what a typical answer feels like, the phone and threads), the
 * earlier ones a line each, so a new model or a new build can be compared with the last.
 */
@Composable
private fun BenchmarkBlock() {
    val ctx = androidx.compose.ui.platform.LocalContext.current
    val usable = remember { com.localghost.app.local.LocalModel.usable(ctx) }
    if (!usable) return
    val scope = androidx.compose.runtime.rememberCoroutineScope()
    var runs by androidx.compose.runtime.remember { androidx.compose.runtime.mutableStateOf(com.localghost.app.local.LocalModel.benchRuns(ctx)) }
    var step by androidx.compose.runtime.remember { androidx.compose.runtime.mutableStateOf<String?>(null) }
    var failed by androidx.compose.runtime.remember { androidx.compose.runtime.mutableStateOf(false) }
    Column(Modifier.fillMaxWidth()) {
        SectionLabel("BENCHMARK")
        Spacer(Modifier.height(6.dp))
        val s = step
        if (s != null) {
            Text("› $s (keep this screen open)", color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
        } else {
            Text(if (runs.isEmpty()) "[ run the benchmark ]" else "[ run it again ]", color = TerminalGreen,
                style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable {
                    failed = false
                    step = "starting…"
                    scope.launch {
                        val r = com.localghost.app.local.LocalModel.benchmark(ctx) { now -> step = now }
                        step = null
                        if (r != null) runs = com.localghost.app.local.LocalModel.benchRuns(ctx) else failed = true
                    }
                }.padding(vertical = 4.dp))
            if (runs.isEmpty() && !failed) Text("about half a minute: the model is loaded if it is not, reads a long passage, " +
                "then writes a paragraph. The numbers are llama.cpp's own.", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            if (failed) Text("the model did not load or answer , MODELS above says why", color = Warning,
                style = MaterialTheme.typography.labelMedium)
        }
        runs.firstOrNull()?.let { r ->
            Spacer(Modifier.height(6.dp))
            com.localghost.app.local.PhoneBench.lines(r).forEach { (k, v) ->
                Row(Modifier.padding(vertical = 1.dp)) {
                    Text(k, color = GhostTextDim, style = MaterialTheme.typography.labelMedium, modifier = Modifier.width(104.dp))
                    Text(v, color = GhostText, style = MaterialTheme.typography.labelMedium)
                }
            }
        }
        if (runs.size > 1) {
            Spacer(Modifier.height(6.dp))
            Text("earlier runs", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            runs.drop(1).forEach { r ->
                Text(com.localghost.app.local.PhoneBench.short(r), color = TerminalDim, style = MaterialTheme.typography.labelSmall)
            }
        }
    }
}
