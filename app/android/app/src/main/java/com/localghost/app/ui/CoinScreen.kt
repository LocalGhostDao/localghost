package com.localghost.app.ui

import androidx.compose.foundation.Canvas
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.gestures.detectDragGestures
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.Path
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.graphics.drawscope.Stroke
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.localghost.app.net.BoxClient
import com.localghost.app.ui.theme.*

/**
 * A COIN'S OWN PAGE: the box's price (rolling as it moves; BTC, ETH and SOL every five seconds),
 * the day's change and when it was made; the chart over a day, a week, a month, a year or all the
 * box holds, a finger on it reading any point; the figures (market cap at the box's price, supply,
 * the day's volume, the range's high and low); what the coin is, from Coinbase's list, with its
 * site and white paper; and where the price comes from, market by market, each with its share.
 */
@Composable
fun CoinScreen(symbol: String) {
    val ctx = androidx.compose.ui.platform.LocalContext.current
    var page by remember(symbol) { mutableStateOf<BoxClient.CoinPage?>(null) }
    var failed by remember(symbol) { mutableStateOf(false) }
    var fast by remember { mutableStateOf<BoxClient.Fast?>(null) }
    var range by remember { mutableStateOf(CoinText.Range.DAY) }
    var points by remember(symbol, range) { mutableStateOf<List<BoxClient.PricePoint>?>(null) }
    var refreshing by remember { mutableStateOf(false) }
    var tick by remember { mutableIntStateOf(0) }
    var nowS by remember { mutableStateOf(System.currentTimeMillis() / 1000) }
    LaunchedEffect(symbol, tick) {
        while (true) {
            val p = BoxClient.coinPage(ctx, symbol)
            if (p != null) page = p
            failed = p == null && page == null
            refreshing = false
            kotlinx.coroutines.delay(60_000)
        }
    }
    LaunchedEffect(symbol, range, tick) {
        points = null
        points = if (range.days > 0) BoxClient.priceDays(ctx, symbol, range.days)
                 else BoxClient.priceSeries(ctx, symbol, range.res, range.hours)
    }
    LaunchedEffect(symbol) {
        while (true) {
            if (symbol in listOf("BTC", "ETH", "SOL")) BoxClient.fast(ctx)?.let { fast = it }
            nowS = System.currentTimeMillis() / 1000
            kotlinx.coroutines.delay(if (symbol in listOf("BTC", "ETH", "SOL")) 5_000 else 15_000)
        }
    }
    val p = page
    val accent: Color = p?.color?.let { CoinText.color(it) }?.let { Color(it) } ?: TerminalGreen
    Refreshable(refreshing, { refreshing = true; tick++ }, Modifier.fillMaxSize()) {
        Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(horizontal = 20.dp)) {
            Spacer(Modifier.height(12.dp))
            if (p == null) {
                if (failed) ErrorLine("the box did not answer , pull down to try again") else LoadingRow()
                return@Column
            }
            // the name, the rank
            Row(verticalAlignment = Alignment.CenterVertically) {
                Box(Modifier.size(10.dp).background(accent, CircleShape))
                Spacer(Modifier.width(8.dp))
                Text(p.name, color = GhostText, style = MaterialTheme.typography.titleMedium, modifier = Modifier.weight(1f))
                Text(CoinText.rank(p.rank), color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            }
            Spacer(Modifier.height(8.dp))
            // the price: the fast lane's when it has the coin, else the minute's, else the list's
            val fastPrice = fast?.prices?.entries?.firstOrNull { it.key == symbol }?.value
            val price = fastPrice?.first ?: p.index?.price?.takeIf { it > 0 } ?: p.listPrice
            val change: Double? = fastPrice?.second ?: p.index?.change24 ?: p.change24
            Row(verticalAlignment = Alignment.Bottom) {
                TickingPrice(HomeText.money(price), price, GhostText, MaterialTheme.typography.headlineMedium, fontSize = 32.sp, arrow = true)
                Spacer(Modifier.width(10.dp))
                val ch = HomeText.change(change)
                Text(ch, color = if (ch.startsWith("-")) Warning else TerminalGreen, style = MaterialTheme.typography.titleMedium,
                    modifier = Modifier.padding(bottom = 4.dp))
            }
            val fastAt: Long = fast?.at ?: 0L
            val at: Long = if (fastPrice != null && fastAt > 0) fastAt / 1000 else (p.index?.at ?: 0L)
            val made = p.index?.let { CoinText.madeFrom(it.markets, it.n, it.paths) } ?: ""
            Text(listOf(HomeText.updated(at, nowS), made).filter { it.isNotEmpty() }.joinToString(" · ").ifEmpty { "the list's price: the box does not price this coin yet" },
                color = TerminalDim, style = MaterialTheme.typography.labelSmall)
            Spacer(Modifier.height(14.dp))
            // the ranges
            Row {
                CoinText.Range.values().forEach { r ->
                    val on = r == range
                    Text(r.label, color = if (on) Void else TerminalGreen, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.padding(end = 6.dp).border(1.dp, if (on) TerminalGreen else TerminalDim, RectangleShape)
                            .background(if (on) TerminalGreen else Void).clickable { range = r }
                            .padding(horizontal = 10.dp, vertical = 4.dp))
                }
            }
            Spacer(Modifier.height(8.dp))
            val pts = points
            when {
                pts == null -> Box(Modifier.fillMaxWidth().height(180.dp), contentAlignment = Alignment.Center) { LoadingRow() }
                pts.size < 2 -> Box(Modifier.fillMaxWidth().height(180.dp), contentAlignment = Alignment.Center) {
                    Text("not enough history on the box yet for this range", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                }
                else -> PriceChart(CoinText.thin(pts), range.days > 0, accent)
            }
            // the figures
            Spacer(Modifier.height(16.dp))
            val closes = pts?.map { it.close } ?: emptyList()
            val capNow = if (p.supply > 0 && price > 0) price * p.supply else p.marketCap
            Figures(listOf(
                "market cap" to CoinText.dollars(capNow),
                "24 h volume" to CoinText.dollars(p.volume24),
                "supply" to CoinText.supply(p.supply, symbol),
                "${range.label} change" to (CoinText.changeOver(closes)?.let { HomeText.change(it) } ?: "—"),
                "${range.label} high" to (closes.maxOrNull()?.let { HomeText.money(it) } ?: "—"),
                "${range.label} low" to (closes.minOrNull()?.let { HomeText.money(it) } ?: "—"),
            ))
            // what it is
            // what it is: the box's own text, read up from Wikipedia, the coin's site and Coinbase;
            // Coinbase's line until the box has written it
            val about: String = p.written.ifBlank { p.description }
            if (about.isNotBlank() || p.website.isNotBlank() || p.whitepaper.isNotBlank()) {
                Spacer(Modifier.height(18.dp))
                SectionLabel("ABOUT ${symbol}")
                Spacer(Modifier.height(6.dp))
                if (about.isNotBlank()) Text(about, color = GhostText, style = MaterialTheme.typography.bodyMedium)
                val by = CoinText.writtenBy(p.written, p.writtenFrom, p.description)
                if (by.isNotEmpty()) Text(by, color = TerminalDim, style = MaterialTheme.typography.labelSmall, modifier = Modifier.padding(top = 4.dp))
                Row(Modifier.padding(top = 6.dp)) {
                    if (p.website.isNotBlank()) LinkText("[ website ]", p.website)
                    if (p.whitepaper.isNotBlank()) LinkText("[ white paper ]", p.whitepaper)
                }
            }
            // where the price comes from
            if (p.markets.isNotEmpty()) {
                Spacer(Modifier.height(18.dp))
                SectionLabel("WHERE THE PRICE COMES FROM")
                Spacer(Modifier.height(4.dp))
                Text("each market's last price in dollars, weighed by its day's volume and how fresh it is; one far off the last price is left out",
                    color = GhostTextDim, style = MaterialTheme.typography.labelSmall)
                Spacer(Modifier.height(6.dp))
                p.markets.forEach { m -> MarketRow(m, symbol, accent) }
            }
            Spacer(Modifier.height(24.dp))
        }
    }
}

/** The figures under the chart, two to a row. */
@Composable
private fun Figures(items: List<Pair<String, String>>) {
    items.chunked(2).forEach { row ->
        Row(Modifier.fillMaxWidth().padding(vertical = 4.dp)) {
            row.forEach { (k, v) ->
                Column(Modifier.weight(1f)) {
                    Text(k, color = GhostTextDim, style = MaterialTheme.typography.labelSmall)
                    Text(v, color = GhostText, style = MaterialTheme.typography.bodyMedium)
                }
            }
            if (row.size == 1) Spacer(Modifier.weight(1f))
        }
    }
}

@Composable
private fun LinkText(label: String, url: String) {
    val ctx = androidx.compose.ui.platform.LocalContext.current
    Text(label, color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
        modifier = Modifier.padding(end = 12.dp).clickable {
            SafeLink.open(ctx, url) // the listing's address is the listing's to write: web addresses only
        })
}

/** One market: where, the price in dollars, its share as a bar; a market left out says why. */
@Composable
private fun MarketRow(m: BoxClient.CoinMarket, symbol: String, accent: Color) {
    Column(Modifier.fillMaxWidth().padding(vertical = 5.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(CoinText.market(m.exchange, symbol, m.quote), color = if (m.out.isEmpty()) GhostText else GhostTextDim,
                style = MaterialTheme.typography.labelMedium, modifier = Modifier.weight(1f))
            Text(HomeText.money(m.usd), color = GhostText, style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.width(8.dp))
            Text(if (m.out.isEmpty()) CoinText.share(m.weight) else "out", color = if (m.out.isEmpty()) TerminalGreen else Warning,
                style = MaterialTheme.typography.labelMedium, modifier = Modifier.width(40.dp))
        }
        if (m.out.isEmpty()) {
            Box(Modifier.padding(top = 3.dp).fillMaxWidth().height(3.dp).background(VoidLighter)) {
                Box(Modifier.fillMaxWidth(m.weight.toFloat().coerceIn(0.01f, 1f)).height(3.dp).background(accent.copy(alpha = 0.8f)))
            }
        } else {
            Text(m.out + " · " + CoinText.age(m.ageS) + " old", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
        }
    }
}

/**
 * The chart: the closes as a line with a fading fill under it, the range's high and low marked; a
 * finger on it (a tap or a drag) reads the point under it, until it lifts.
 */
@Composable
private fun PriceChart(points: List<BoxClient.PricePoint>, daily: Boolean, accent: Color) {
    var picked by remember(points) { mutableStateOf<Int?>(null) }
    val lo = points.minOf { it.close }
    val hi = points.maxOf { it.close }
    val up = points.last().close >= points.first().close
    val line = if (up) accent else Warning
    Column(Modifier.fillMaxWidth()) {
        val sel = picked?.let { points.getOrNull(it) }
        Text(if (sel != null) CoinText.at(sel.close, sel.t, daily) else "high " + HomeText.money(hi) + " · low " + HomeText.money(lo),
            color = if (sel != null) GhostText else TerminalDim, style = MaterialTheme.typography.labelSmall)
        Spacer(Modifier.height(4.dp))
        Canvas(Modifier.fillMaxWidth().height(180.dp)
            .pointerInput(points) {
                detectTapGestures(onPress = { o ->
                    picked = ((o.x / size.width) * (points.size - 1)).toInt().coerceIn(0, points.size - 1)
                    tryAwaitRelease()
                    picked = null
                })
            }
            .pointerInput(points) {
                detectDragGestures(
                    onDragEnd = { picked = null }, onDragCancel = { picked = null },
                ) { change, _ ->
                    picked = ((change.position.x / size.width) * (points.size - 1)).toInt().coerceIn(0, points.size - 1)
                }
            }) {
            val span = (hi - lo).takeIf { it > 0 } ?: 1.0
            val w = size.width
            val h = size.height
            fun x(i: Int) = w * i / (points.size - 1).toFloat()
            fun y(v: Double) = (h * 0.92f - ((v - lo) / span).toFloat() * h * 0.84f)
            val path = Path()
            points.forEachIndexed { i, pt -> if (i == 0) path.moveTo(x(i), y(pt.close)) else path.lineTo(x(i), y(pt.close)) }
            val fill = Path().apply {
                addPath(path)
                lineTo(w, h)
                lineTo(0f, h)
                close()
            }
            drawPath(fill, Brush.verticalGradient(listOf(line.copy(alpha = 0.28f), line.copy(alpha = 0f))))
            drawPath(path, line, style = Stroke(width = 2.dp.toPx(), cap = StrokeCap.Round))
            picked?.let { i ->
                val px = x(i)
                val py = y(points[i].close)
                drawLine(GhostTextDim, Offset(px, 0f), Offset(px, h), strokeWidth = 1.dp.toPx())
                drawCircle(line, radius = 4.dp.toPx(), center = Offset(px, py))
            }
        }
    }
}

/** A week of hourly closes as a small line, green when it ended higher, amber when lower. */
@Composable
fun Sparkline(closes: List<Double>, modifier: Modifier = Modifier) {
    if (closes.size < 2) {
        Spacer(modifier)
        return
    }
    val lo = closes.min()
    val hi = closes.max()
    val c = if (closes.last() >= closes.first()) TerminalGreen else Warning
    Canvas(modifier) {
        val span = (hi - lo).takeIf { it > 0 } ?: 1.0
        val path = Path()
        closes.forEachIndexed { i, v ->
            val px = size.width * i / (closes.size - 1).toFloat()
            val py = size.height * (1f - ((v - lo) / span).toFloat())
            if (i == 0) path.moveTo(px, py) else path.lineTo(px, py)
        }
        drawPath(path, c, style = Stroke(width = 1.5.dp.toPx(), cap = StrokeCap.Round))
    }
}
