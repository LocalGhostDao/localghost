package com.localghost.app.ui

import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import com.localghost.app.net.BoxClient
import com.localghost.app.ui.theme.*

/**
 * HEALTH , tallyd's drill-in screen, the first of the per-daemon screens. Everything here is the
 * box's own copy of the phone's Health Connect data: 30-day daily series per metric, drawn as bar
 * strips with min/avg/max , stats, not judgements. The pattern for the rest of the fleet: one
 * screen per daemon, the daemon's domain drawn from its own tables.
 */

private val metricLabels = mapOf(
    "steps" to "steps", "sleep_minutes" to "sleep", "exercise_minutes" to "exercise",
    "distance_km" to "distance", "active_calories" to "active kcal", "floors" to "floors",
    "hr_avg" to "heart rate (avg)", "hr_min" to "heart rate (min)", "hr_max" to "heart rate (max)",
    "weight_kg" to "weight",
    "sleep_deep_minutes" to "deep sleep", "sleep_light_minutes" to "light sleep", "sleep_rem_minutes" to "REM sleep",
    "sleep_awake_minutes" to "awake in bed", "resting_hr" to "resting heart rate", "hrv_ms" to "heart rate variability",
    "spo2_pct" to "blood oxygen", "resp_rate" to "breathing rate", "vo2max" to "VO2 max", "body_fat_pct" to "body fat",
)

private fun fmtVal(metric: String, v: Double): String = when (metric) {
    "sleep_minutes", "sleep_deep_minutes", "sleep_light_minutes", "sleep_rem_minutes", "sleep_awake_minutes" -> "${(v / 60).toInt()}h${"%02d".format((v % 60).toInt())}m"
    "spo2_pct", "body_fat_pct" -> "%.0f%%".format(v)
    "hrv_ms" -> "%.0f ms".format(v)
    "resp_rate" -> "%.1f/min".format(v)
    "vo2max" -> "%.1f".format(v)
    "distance_km" -> "%.1f km".format(v)
    "weight_kg" -> "%.1f kg".format(v)
    "exercise_minutes" -> "${v.toInt()}m"
    else -> "%,d".format(v.toInt())
}

@Composable
fun HealthScreen() {
    val ctx = LocalContext.current
    var series by remember { mutableStateOf<List<BoxClient.HealthSeries>?>(null) }
    var failed by remember { mutableStateOf(false) } // the box did not answer: said, not spun on
    LaunchedEffect(Unit) { val s = BoxClient.healthStats(ctx, 30); if (s != null) series = s else failed = true }
    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp)) {
        item {
            Spacer(Modifier.height(12.dp))
            Row(verticalAlignment = Alignment.CenterVertically) { SectionLabel("HEALTH"); InfoButton("health") }
            Spacer(Modifier.height(4.dp))
            Text("30 days · your box's copy · from Health Connect, every six hours in the background and on request in SETTINGS › HEALTH",
                color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            // WHERE THE DATA STANDS: the newest day the box holds, and how the phone's last
            // hand-over went. "I can't get my health data from my watch" is answered here, not
            // guessed at: the box's newest day says whether anything arrives, the phone's last
            // run says whether anything was found to send.
            val newest = series?.mapNotNull { it.days.lastOrNull() }?.maxOrNull()
            val run = remember { com.localghost.app.sync.HealthSync.lastRun(ctx) }
            Spacer(Modifier.height(6.dp))
            Text(HealthStatus.line(newest, run?.at ?: 0L, run?.days ?: 0, run?.newestDay ?: "", run?.error ?: "", run?.skipped ?: "", System.currentTimeMillis() / 1000),
                color = if (run != null && run.error.isNotEmpty()) Warning else GhostTextDim, style = MaterialTheme.typography.labelMedium)
        }
        when {
            series == null && failed -> item { ErrorLine("the box did not answer , is it unlocked?") }
            series == null -> item { LoadingRow() }
            series!!.isEmpty() -> item {
                Text("! no health data on the box yet , allow Health Connect in SETTINGS › HEALTH and it ships the last week",
                    color = TerminalDim, style = MaterialTheme.typography.bodyMedium)
            }
            else -> {
                val order = listOf("steps", "sleep_minutes", "sleep_deep_minutes", "sleep_light_minutes", "sleep_rem_minutes", "sleep_awake_minutes",
                    "exercise_minutes", "distance_km", "active_calories", "floors", "resting_hr", "hr_avg", "hr_max", "hr_min", "hrv_ms",
                    "spo2_pct", "resp_rate", "vo2max", "weight_kg", "body_fat_pct")
                // "calories" was Health Connect's resting estimate (a constant), not a measurement
                val sorted = series!!.filter { it.values.isNotEmpty() && it.metric != "calories" }
                    .sortedBy { order.indexOf(it.metric).let { i -> if (i < 0) 99 else i } }
                items(sorted, key = { it.metric }) { s ->
                MetricCard(s)
                }
            }
        }
        item { Spacer(Modifier.height(20.dp)) }
    }
}

@Composable
private fun MetricCard(s: BoxClient.HealthSeries) {
    val label = metricLabels[s.metric] ?: s.metric
    val latest = s.values.last()
    val mn = s.values.min(); val mx = s.values.max(); val avg = s.values.average()
    Column(Modifier.fillMaxWidth().animateContentSize().border(1.dp, GhostBorder, RectangleShape).background(Void).padding(12.dp)) {
        Row(verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) {
            Text("> $label", color = TerminalGreen, style = MaterialTheme.typography.bodyMedium,
                modifier = Modifier.weight(1f))
            Text(fmtVal(s.metric, latest), color = GhostText, style = MaterialTheme.typography.bodyMedium)
        }
        Spacer(Modifier.height(6.dp))
        // 30 bars, scaled to the metric's own range , shape over scale, honest zero baseline.
        Canvas(Modifier.fillMaxWidth().height(44.dp)) {
            val n = s.values.size
            if (n == 0) return@Canvas
            val bw = size.width / n
            val top = if (mx > 0) mx else 1.0
            s.values.forEachIndexed { i, v ->
                val h = (v / top).toFloat() * size.height
                drawRect(TerminalGreen, topLeft = Offset(i * bw + bw * 0.15f, size.height - h),
                    size = Size(bw * 0.7f, h))
            }
        }
        Spacer(Modifier.height(4.dp))
        Text("min ${fmtVal(s.metric, mn)} · avg ${fmtVal(s.metric, avg)} · max ${fmtVal(s.metric, mx)}",
            color = TerminalDim, style = MaterialTheme.typography.labelMedium)
    }
}
