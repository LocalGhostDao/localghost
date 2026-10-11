package com.localghost.app.ui

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.geometry.Size
import androidx.compose.ui.graphics.PathEffect
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import com.localghost.app.net.BoxClient
import com.localghost.app.ui.theme.*
import java.time.LocalDate
import java.time.ZoneId

/**
 * HEALTH , tallyd's screen, three pages deep. The dashboard: every kind the box holds, in four
 * groups (movement, sleep, heart, body), each a tile with the latest value, how it stands to the
 * window's mean and the window drawn small; the window (a week to everything) is chosen once at
 * the top. A tile opens the kind: the window drawn large with its mean, the stats (latest, mean,
 * median, max and min with their days, the total where it sums), the last days against the days
 * before, the weekday pattern, and every day listed. A day opens the day: all its numbers against
 * the 30-day mean, the heart rate's curve through the day, and the way to the day's own page.
 * Everything here is the box's copy of the phone's Health Connect data; stats, not judgements.
 */
@Composable
fun HealthScreen(onOpenDay: (String) -> Unit = {}) {
    val ctx = LocalContext.current
    var series by remember { mutableStateOf<List<BoxClient.HealthSeries>?>(null) }
    var failed by remember { mutableStateOf(false) } // the box did not answer: said, not spun on
    var win by rememberSaveable { mutableIntStateOf(1) } // 30D
    var openMetric by rememberSaveable { mutableStateOf("") }
    var openDay by rememberSaveable { mutableStateOf("") }
    // ten years once; the pages cut their own windows from it
    LaunchedEffect(Unit) { val s = BoxClient.healthStats(ctx, 3660); if (s != null) series = s else failed = true }
    androidx.activity.compose.BackHandler(enabled = openDay.isNotEmpty() || openMetric.isNotEmpty()) {
        if (openDay.isNotEmpty()) openDay = "" else openMetric = ""
    }
    val all = series?.filter { it.values.isNotEmpty() && it.metric != "calories" }
        ?.sortedBy { HealthText.order(it.metric) } ?: emptyList()
    when {
        openDay.isNotEmpty() -> HealthDayPage(openDay, all, onBack = { openDay = "" }, onDay = { openDay = it }, onOpenDay = onOpenDay)
        openMetric.isNotEmpty() -> HealthMetricPage(openMetric, all, win, onWin = { win = it }, onBack = { openMetric = "" }, onDay = { openDay = it })
        else -> HealthDashboard(series, failed, all, win, onWin = { win = it }, onMetric = { openMetric = it }, onDay = { openDay = it })
    }
}

// --- the window ---

private fun today(): LocalDate = LocalDate.now(ZoneId.systemDefault())

/** A metric's days and values inside the chosen window. */
private fun cut(s: BoxClient.HealthSeries, win: Int): Pair<List<String>, List<Double>> =
    HealthText.window(s.days, s.values, HealthText.since(today(), HealthText.windows[win].days))

private fun windowLabel(win: Int): String = when (val d = HealthText.windows[win].days) { 0 -> "all-time"; 365 -> "1-year"; else -> "$d-day" }

@Composable
private fun WindowChips(win: Int, onWin: (Int) -> Unit) {
    Row(Modifier.horizontalScroll(rememberScrollState())) {
        HealthText.windows.forEachIndexed { i, w ->
            val on = i == win
            Text(w.label, color = if (on) Void else TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.padding(end = 8.dp)
                    .border(1.dp, TerminalGreen, RectangleShape)
                    .background(if (on) TerminalGreen else Void)
                    .clickable { onWin(i) }
                    .padding(horizontal = 10.dp, vertical = 6.dp))
        }
    }
}

@Composable
private fun BackLine(label: String, onBack: () -> Unit) {
    Text("‹ $label", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
        modifier = Modifier.clickable { onBack() }.padding(vertical = 6.dp))
}

// --- the dashboard ---

@Composable
private fun HealthDashboard(series: List<BoxClient.HealthSeries>?, failed: Boolean, all: List<BoxClient.HealthSeries>,
                            win: Int, onWin: (Int) -> Unit, onMetric: (String) -> Unit, onDay: (String) -> Unit) {
    val ctx = LocalContext.current
    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp), verticalArrangement = Arrangement.spacedBy(12.dp)) {
        item {
            Spacer(Modifier.height(12.dp))
            Row(verticalAlignment = Alignment.CenterVertically) { SectionLabel("HEALTH"); InfoButton("health") }
            Spacer(Modifier.height(4.dp))
            Text("your box's copy · from Health Connect, every six hours in the background and on request in SETTINGS › HEALTH · a tile opens the kind, a day opens the day",
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
            all.isEmpty() -> item {
                Text("! no health data on the box yet , allow Health Connect in SETTINGS › HEALTH and it ships the last week",
                    color = TerminalDim, style = MaterialTheme.typography.bodyMedium)
            }
            else -> {
                item {
                    WindowChips(win, onWin)
                    Spacer(Modifier.height(6.dp))
                    val inWin = all.map { cut(it, win) }.filter { it.second.isNotEmpty() }
                    val days = inWin.flatMap { it.first }.distinct().sorted()
                    Text(HealthText.windowLine(days, inWin.size, HealthText.windows[win]), color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                    // the newest day, a tap away: the day page is where every number of one day sits
                    val newest = days.lastOrNull()
                    if (newest != null) {
                        Spacer(Modifier.height(4.dp))
                        Text("> newest day $newest ›", color = TerminalGreen, style = MaterialTheme.typography.bodyMedium,
                            modifier = Modifier.clickable { onDay(newest) }.padding(vertical = 4.dp))
                    }
                }
                HealthText.groups.forEach { g ->
                    val inGroup = all.filter { HealthText.metric(it.metric).group == g }.map { it to cut(it, win) }.filter { it.second.second.isNotEmpty() }
                    if (inGroup.isEmpty()) return@forEach
                    item(key = "g-$g") {
                        Spacer(Modifier.height(4.dp))
                        SectionLabel(g)
                    }
                    // two tiles a row
                    items(inGroup.chunked(2), key = { row -> "t-" + row.joinToString("+") { it.first.metric } }) { row ->
                        Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.spacedBy(12.dp)) {
                            row.forEach { (s, dv) -> Box(Modifier.weight(1f)) { MetricTile(s.metric, dv.first, dv.second, win) { onMetric(s.metric) } } }
                            if (row.size == 1) Spacer(Modifier.weight(1f))
                        }
                    }
                }
            }
        }
        item { Spacer(Modifier.height(20.dp)) }
    }
}

/** One kind on the dashboard: the latest, against the window's mean, the window drawn small. */
@Composable
private fun MetricTile(metric: String, days: List<String>, values: List<Double>, win: Int, onClick: () -> Unit) {
    val s = HealthText.summary(days, values) ?: return
    Column(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).background(Void).clickable { onClick() }.padding(10.dp)) {
        Text(HealthText.label(metric), color = TerminalDim, style = MaterialTheme.typography.labelMedium, maxLines = 1)
        Spacer(Modifier.height(2.dp))
        Text(HealthText.fmt(metric, s.latest), color = GhostText, style = MaterialTheme.typography.titleMedium, maxLines = 1)
        Text(HealthText.fmtDelta(metric, s.latest - s.mean) + " on mean", color = GhostTextDim, style = MaterialTheme.typography.labelSmall, maxLines = 1)
        Spacer(Modifier.height(6.dp))
        Bars(values, s.mean, Modifier.fillMaxWidth().height(28.dp), meanLine = false)
    }
}

/** Bars from a zero baseline, scaled to the window's own max (shape over scale), the latest bar
 *  lit; the mean as a dashed line when asked. */
@Composable
private fun Bars(values: List<Double>, mean: Double, modifier: Modifier, meanLine: Boolean) {
    Canvas(modifier) {
        val n = values.size
        if (n == 0) return@Canvas
        val mx = values.max()
        val top = if (mx > 0) mx else 1.0
        val bw = size.width / n
        values.forEachIndexed { i, v ->
            val h = (v / top).toFloat() * size.height
            drawRect(if (i == n - 1) TerminalGreen else TerminalDim, topLeft = Offset(i * bw + bw * 0.15f, size.height - h),
                size = Size(maxOf(bw * 0.7f, 1f), h))
        }
        if (meanLine && mean > 0) {
            val y = size.height - (mean / top).toFloat() * size.height
            drawLine(GhostTextDim, Offset(0f, y), Offset(size.width, y), strokeWidth = 1f,
                pathEffect = PathEffect.dashPathEffect(floatArrayOf(6f, 6f)))
        }
    }
}

// --- one kind ---

@Composable
private fun HealthMetricPage(metric: String, all: List<BoxClient.HealthSeries>, win: Int, onWin: (Int) -> Unit, onBack: () -> Unit, onDay: (String) -> Unit) {
    val s = all.firstOrNull { it.metric == metric }
    val (days, values) = if (s == null) emptyList<String>() to emptyList() else cut(s, win)
    val sum = HealthText.summary(days, values)
    val wl = windowLabel(win)
    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
        item {
            Spacer(Modifier.height(12.dp))
            BackLine("health", onBack)
            Row(verticalAlignment = Alignment.CenterVertically) { SectionLabel(HealthText.label(metric).uppercase()); InfoButton("health") }
            Spacer(Modifier.height(8.dp))
            WindowChips(win, onWin)
            Spacer(Modifier.height(6.dp))
            Text(HealthText.windowLine(days, 1, HealthText.windows[win]), color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        }
        if (sum == null) {
            item { Text("no days of ${HealthText.label(metric)} in this window", color = TerminalDim, style = MaterialTheme.typography.bodyMedium) }
        } else {
            item {
                Column(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).background(Void).padding(12.dp)) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text("> ${HealthText.label(metric)}", color = TerminalGreen, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.weight(1f))
                        Text(HealthText.fmt(metric, sum.latest), color = GhostText, style = MaterialTheme.typography.titleMedium)
                    }
                    Spacer(Modifier.height(8.dp))
                    Bars(values, sum.mean, Modifier.fillMaxWidth().height(120.dp), meanLine = true)
                    Spacer(Modifier.height(4.dp))
                    Row {
                        Text(days.first(), color = TerminalDim, style = MaterialTheme.typography.labelSmall, modifier = Modifier.weight(1f))
                        Text("- - mean ${HealthText.fmt(metric, sum.mean)}", color = GhostTextDim, style = MaterialTheme.typography.labelSmall)
                        Spacer(Modifier.weight(1f))
                        Text(days.last(), color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                    }
                }
            }
            item {
                SectionLabel("STATS")
                Spacer(Modifier.height(4.dp))
                HealthText.statLines(metric, sum, wl).forEach { Text(it, color = GhostText, style = MaterialTheme.typography.bodyMedium) }
                Text(HealthText.againstMean(metric, sum.latest, sum.mean, wl), color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                // the last days against the days before, by the calendar; a week when the window
                // holds two, a month when it holds two
                val ser = s!!
                val t7 = if (HealthText.windows[win].days == 0 || HealthText.windows[win].days >= 14) HealthText.trendLine(ser.days, ser.values, today(), 7) else ""
                val t30 = if (HealthText.windows[win].days == 0 || HealthText.windows[win].days >= 60) HealthText.trendLine(ser.days, ser.values, today(), 30) else ""
                if (t7.isNotEmpty()) Text(t7, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                if (t30.isNotEmpty()) Text(t30, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                if (metric == "sleep_minutes") {
                    // efficiency where the stages came with the night: asleep over time in bed
                    val awake = all.firstOrNull { it.metric == "sleep_awake_minutes" }
                    val aw = awake?.let { a -> a.days.indexOf(sum.latestDay).let { i -> if (i < 0) null else a.values[i] } }
                    val eff = if (aw != null) HealthText.sleepEfficiency(sum.latest, aw) else null
                    if (eff != null) Text("efficiency ${"%.0f".format(eff)}% on ${sum.latestDay} (asleep over time in bed)", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                }
            }
            item {
                SectionLabel("BY WEEKDAY")
                Spacer(Modifier.height(6.dp))
                val means = HealthText.weekdayMeans(days, values)
                val top = means.filterNotNull().maxOrNull() ?: 0.0
                Row(Modifier.fillMaxWidth().height(70.dp), horizontalArrangement = Arrangement.spacedBy(6.dp), verticalAlignment = Alignment.Bottom) {
                    means.forEachIndexed { i, m ->
                        Column(Modifier.weight(1f).fillMaxHeight(), verticalArrangement = Arrangement.Bottom, horizontalAlignment = Alignment.CenterHorizontally) {
                            if (m != null) {
                                Text(HealthText.fmt(metric, m).substringBefore(' '), color = GhostTextDim, style = MaterialTheme.typography.labelSmall, maxLines = 1)
                                val frac = if (top > 0) (m / top).toFloat() else 0f
                                Box(Modifier.fillMaxWidth(0.7f).height((36 * frac).dp.coerceAtLeast(1.dp)).background(TerminalDim))
                            } else Box(Modifier.fillMaxWidth(0.7f).height(1.dp).background(GhostBorder))
                            Text(HealthText.weekdayLetters[i], color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                        }
                    }
                }
                val wline = HealthText.weekdayLine(metric, means)
                if (wline.isNotEmpty()) { Spacer(Modifier.height(4.dp)); Text(wline, color = GhostTextDim, style = MaterialTheme.typography.labelMedium) }
            }
            item { SectionLabel("DAYS") }
            val rows = days.indices.reversed().toList()
            items(rows, key = { "d-" + days[it] }) { i ->
                val v = values[i]
                Row(Modifier.fillMaxWidth().clickable { onDay(days[i]) }.padding(vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                    Text(days[i], color = TerminalDim, style = MaterialTheme.typography.labelMedium, modifier = Modifier.width(96.dp))
                    Text(HealthText.fmt(metric, v), color = GhostText, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.width(88.dp))
                    Canvas(Modifier.weight(1f).height(8.dp)) {
                        val frac = if (sum.max > 0) (v / sum.max).toFloat() else 0f
                        drawRect(if (i == days.size - 1) TerminalGreen else TerminalDim, size = Size(size.width * frac, size.height))
                    }
                    Text(" ›", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                }
            }
        }
        item { Spacer(Modifier.height(20.dp)) }
    }
}

// --- one day ---

@Composable
private fun HealthDayPage(day: String, all: List<BoxClient.HealthSeries>, onBack: () -> Unit, onDay: (String) -> Unit, onOpenDay: (String) -> Unit) {
    val ctx = LocalContext.current
    var got by remember(day) { mutableStateOf<BoxClient.HealthDay?>(null) }
    var failed by remember(day) { mutableStateOf(false) }
    val zone = ZoneId.systemDefault()
    LaunchedEffect(day) {
        val d = try { LocalDate.parse(day) } catch (_: Exception) { null }
        if (d == null) { failed = true; return@LaunchedEffect }
        val from = d.atStartOfDay(zone).toEpochSecond()
        val to = d.plusDays(1).atStartOfDay(zone).toEpochSecond()
        val r = BoxClient.healthDay(ctx, day, from, to)
        if (r != null) got = r else failed = true
    }
    // the 30-day mean of each kind, the day's numbers' yardstick
    val since30 = HealthText.since(today(), 30)
    fun mean30(metric: String): Double? = all.firstOrNull { it.metric == metric }?.let { s ->
        val (_, v) = HealthText.window(s.days, s.values, since30); if (v.isEmpty()) null else v.average()
    }
    // the days the box holds, for prev and next
    val allDays = all.flatMap { it.days }.distinct().sorted()
    val idx = allDays.indexOf(day)
    val prev = if (idx > 0) allDays[idx - 1] else null
    val next = if (idx >= 0 && idx < allDays.size - 1) allDays[idx + 1] else null
    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
        item {
            Spacer(Modifier.height(12.dp))
            BackLine("back", onBack)
            Row(verticalAlignment = Alignment.CenterVertically) { SectionLabel(day); InfoButton("health") }
            Spacer(Modifier.height(4.dp))
            Row(Modifier.fillMaxWidth()) {
                if (prev != null) Text("‹ $prev", color = TerminalGreen, style = MaterialTheme.typography.labelMedium, modifier = Modifier.clickable { onDay(prev) }.padding(vertical = 4.dp))
                Spacer(Modifier.weight(1f))
                if (next != null) Text("$next ›", color = TerminalGreen, style = MaterialTheme.typography.labelMedium, modifier = Modifier.clickable { onDay(next) }.padding(vertical = 4.dp))
            }
        }
        when {
            got == null && failed -> item { ErrorLine("the box did not answer , is it unlocked?") }
            got == null -> item { LoadingRow() }
            else -> {
                val d = got!!
                if (d.metrics.isEmpty()) item { Text("nothing recorded for the day", color = TerminalDim, style = MaterialTheme.typography.bodyMedium) }
                HealthText.groups.forEach { g ->
                    val keys = d.metrics.keys.filter { it != "calories" && HealthText.metric(it).group == g }.sortedBy { HealthText.order(it) }
                    if (keys.isEmpty()) return@forEach
                    item(key = "g-$g") {
                        Spacer(Modifier.height(2.dp))
                        SectionLabel(g)
                        Spacer(Modifier.height(4.dp))
                        keys.forEach { k -> Text(HealthText.dayLine(k, d.metrics[k]!!, mean30(k)), color = GhostText, style = MaterialTheme.typography.bodyMedium) }
                        if (g == HealthText.SLEEP) {
                            val eff = HealthText.sleepEfficiency(d.metrics["sleep_minutes"] ?: 0.0, d.metrics["sleep_awake_minutes"] ?: -1.0)
                            if (eff != null) Text("efficiency ${"%.0f".format(eff)}% (asleep over time in bed)", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                        }
                    }
                }
                item(key = "hr") {
                    Spacer(Modifier.height(2.dp))
                    SectionLabel("HEART RATE THROUGH THE DAY")
                    Spacer(Modifier.height(6.dp))
                    val hhmm = { ts: Long -> java.time.Instant.ofEpochSecond(ts).atZone(zone).toLocalTime().toString().take(5) }
                    Text(HealthText.samplesLine(d.samples, hhmm), color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                    if (d.samples.isNotEmpty()) {
                        Spacer(Modifier.height(6.dp))
                        val dayStart = LocalDate.parse(day).atStartOfDay(zone).toEpochSecond()
                        HeartCurve(d.samples, dayStart, Modifier.fillMaxWidth().height(110.dp).border(1.dp, GhostBorder, RectangleShape).padding(6.dp))
                        Row(Modifier.fillMaxWidth()) {
                            listOf("00", "06", "12", "18", "24").forEachIndexed { i, h ->
                                Text(h, color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                                if (i < 4) Spacer(Modifier.weight(1f))
                            }
                        }
                    }
                }
                item {
                    Spacer(Modifier.height(6.dp))
                    GhostButton("open the day", onClick = { onOpenDay(day) })
                    Text("the day's own page: its photos, places, notes and check-in", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                }
            }
        }
        item { Spacer(Modifier.height(20.dp)) }
    }
}

/** The heart rate's five-minute readings as a line over the day's 24 hours, the range's own
 *  scale with the low and high marked. */
@Composable
private fun HeartCurve(samples: List<Pair<Long, Double>>, dayStart: Long, modifier: Modifier) {
    Canvas(modifier) {
        val lo = samples.minOf { it.second }
        val hi = samples.maxOf { it.second }
        val span = if (hi > lo) hi - lo else 1.0
        fun x(ts: Long) = ((ts - dayStart).toFloat() / 86400f).coerceIn(0f, 1f) * size.width
        fun y(v: Double) = size.height - ((v - lo) / span).toFloat() * size.height
        var last: Offset? = null
        samples.forEach { (ts, v) ->
            val p = Offset(x(ts), y(v))
            val l = last
            // a gap of more than half an hour is drawn as a gap, not a line through the unknown
            if (l != null && x(ts) - l.x < size.width / 48f) drawLine(TerminalGreen, l, p, strokeWidth = 2f)
            else drawRect(TerminalGreen, topLeft = Offset(p.x - 1f, p.y - 1f), size = Size(2f, 2f))
            last = p
        }
        // the low and the high as dashed guides
        drawLine(GhostBorder, Offset(0f, y(lo)), Offset(size.width, y(lo)), strokeWidth = 1f, pathEffect = PathEffect.dashPathEffect(floatArrayOf(4f, 6f)))
        drawLine(GhostBorder, Offset(0f, y(hi)), Offset(size.width, y(hi)), strokeWidth = 1f, pathEffect = PathEffect.dashPathEffect(floatArrayOf(4f, 6f)))
    }
}
