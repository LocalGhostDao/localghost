package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.CircleShape
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.clip
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.localghost.app.net.BoxClient
import com.localghost.app.net.HomeCache
import com.localghost.app.net.HomeData
import com.localghost.app.phrases.HomeBriefText
import com.localghost.app.sync.BoxFetch
import com.localghost.app.ui.theme.*
import kotlinx.coroutines.launch

/**
 * HOME , where the app opens. BTC and ETH, always, at the box's own price (Coinbase's last trade,
 * asked every five seconds while home is open) with the day's change and CRYPTO50 under them (the
 * other coins one tap away, on CRYPTO); the day's news as one point per story, the box's brief,
 * each point opening its story in NEWS; and a box to ask from, which starts a new chat with the
 * question. Pull down to refresh: the box's copy at once, and the feeds fetched when they are half
 * an hour old. Read again every minute while it is open.
 */
@Composable
fun HomeScreen(onAsk: (String) -> Unit, onOpenNews: () -> Unit, onOpenStory: (Long) -> Unit, onOpenCrypto: () -> Unit,
               onOpenCoin: (String) -> Unit = {}, onOpenTarget: (String) -> Unit = {},
               continuing: String = "", onNewChat: () -> Unit = {}, onOpenChat: () -> Unit = {}) {
    val ctx = androidx.compose.ui.platform.LocalContext.current
    val scope = rememberCoroutineScope()
    // the latest the phone kept (HomeCache): home opens on it, then reads the box
    val kept: HomeData.Snap? = remember { HomeCache.snap(ctx) }
    var news by remember { mutableStateOf<BoxClient.News?>(HomeCache.newsWith(HomeCache.news(ctx), kept)) }
    var rates by remember { mutableStateOf<BoxClient.Rates?>(HomeCache.rates(ctx)) }
    var fast by remember { mutableStateOf<BoxClient.Fast?>(HomeCache.fastOf(kept)) }
    var snap by remember { mutableStateOf<HomeData.Snap?>(kept) }
    var forYou by remember { mutableStateOf<HomeData.ForYou?>(HomeCache.forYou) }
    // the weather where the phone is, from the box's daily pull (its position goes to the box and
    // nowhere else); read with the rest, every minute
    var weather by remember { mutableStateOf<BoxClient.Weather?>(null) }
    var failed by remember { mutableStateOf(false) }
    var refreshing by remember { mutableStateOf(false) }
    var fetching by remember { mutableStateOf(false) }
    var writing by remember { mutableStateOf(false) }
    var briefNote by remember { mutableStateOf("") }
    var tick by remember { mutableIntStateOf(0) }
    var nowS by remember { mutableStateOf(System.currentTimeMillis() / 1000) }
    LaunchedEffect(tick) {
        while (true) {
            // home's snapshot first (a few kilobytes: the prices, the brief, FOR YOU), then the rest
            val h = BoxClient.home(ctx)
            if (h != null) {
                snap = h
                HomeCache.fastOf(h)?.let { f -> if (f.at > (fast?.at ?: 0L)) fast = f }
                news = HomeCache.newsWith(news, h)
                h.forYou?.let { forYou = it }
            }
            val r = BoxClient.rates(ctx, keep = true)
            val n = BoxClient.news(ctx, since = System.currentTimeMillis() / 1000 - 86_400, keep = true)
            com.localghost.app.sync.LocationLog.last(ctx)?.let { fix -> BoxClient.weather(ctx, fix.lat, fix.lon)?.let { weather = it } }
            if (r != null) rates = r
            if (n != null) news = HomeCache.newsWith(n, snap)
            failed = r == null && n == null && h == null
            nowS = System.currentTimeMillis() / 1000
            refreshing = false
            kotlinx.coroutines.delay(60_000)
        }
    }
    // BTC and ETH every five seconds, from the box's Redis (the box asks the exchanges, not this phone)
    LaunchedEffect(Unit) {
        while (true) {
            BoxClient.fast(ctx)?.let { fast = it }
            kotlinx.coroutines.delay(5_000)
        }
    }
    // "write now": the box writes the day's brief from the summaries it has, up to two minutes
    val writeBrief: () -> Unit = {
        if (!writing) {
            writing = true
            briefNote = ""
            scope.launch {
                val r = BoxClient.writeBrief(ctx)
                writing = false
                when {
                    r == null -> briefNote = HomeText.briefNot("")
                    r.written -> news = news?.copy(brief = r.brief, briefAt = r.briefAt, briefStories = r.briefStories)
                    else -> briefNote = HomeText.briefNot(r.why)
                }
            }
        }
    }
    val refresh: () -> Unit = {
        refreshing = true
        tick++
        if (!fetching && NewsText.wantsFetch(news?.lastFetch ?: 0, System.currentTimeMillis() / 1000)) {
            fetching = true
            scope.launch {
                kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) { BoxFetch.run(ctx, force = true) }
                kotlinx.coroutines.delay(35_000) // the box takes the batch within half a minute
                fetching = false
                tick++
            }
        }
    }
    val stamp: Long = nowS
    Column(Modifier.fillMaxSize().padding(horizontal = 20.dp)) {
        Refreshable(refreshing, refresh, Modifier.weight(1f).fillMaxWidth()) {
            Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState())) {
                Spacer(Modifier.height(16.dp))
                Text(java.text.SimpleDateFormat("EEEE d MMMM · HH:mm", java.util.Locale.UK).format(java.util.Date(stamp * 1000L)) +
                    (if (fetching) " · fetching the feeds…" else ""),
                    color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                Spacer(Modifier.height(12.dp))
                PricesCard(rates, fast, HomeCache.marketOf(rates, snap), failed, onOpenCrypto, onOpenCoin)
                Spacer(Modifier.height(12.dp))
                WeatherCard(weather, stamp)
                Spacer(Modifier.height(20.dp))
                Row(verticalAlignment = Alignment.CenterVertically) {
                    SectionLabel("THE DAY'S NEWS")
                    InfoButton("news")
                    Spacer(Modifier.weight(1f))
                    Text(if (writing) "writing…" else "[ write now ]", color = if (writing) GhostTextDim else TerminalGreen,
                        style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.clickable(enabled = !writing) { writeBrief() }.padding(4.dp))
                    Spacer(Modifier.width(8.dp))
                    Text("all ›", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.clickable { onOpenNews() }.padding(4.dp))
                }
                if (briefNote.isNotEmpty()) {
                    Text(briefNote, color = Warning, style = MaterialTheme.typography.labelSmall)
                }
                Spacer(Modifier.height(6.dp))
                val n = news
                when {
                    n == null && failed -> ErrorLine("the box did not answer , is it unlocked?")
                    n == null -> LoadingRow()
                    else -> {
                        val points: List<HomeText.Point> = HomeText.points(n.brief, n.briefStories)
                        if (points.isNotEmpty()) {
                            points.forEach { p ->
                                BriefPoint(p) {
                                    val id = p.story
                                    if (id != null) onOpenStory(id) else onOpenNews()
                                }
                            }
                            Spacer(Modifier.height(4.dp))
                            Text(HomeText.written(n.briefAt, stamp), color = GhostTextDim, style = MaterialTheme.typography.labelSmall)
                        } else {
                            Text(HomeText.noBrief(n.stories.size), color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                            Spacer(Modifier.height(8.dp))
                            // no brief yet: the most-told stories, each opening in NEWS
                            val top = HomeBriefText.pick(n.stories.map { s ->
                                HomeBriefText.Story(s.id, s.title, s.summary, s.items.map { it.outlet }, s.lastSeen, s.sources)
                            }, stamp, n = 5)
                            top.forEach { s ->
                                Column(Modifier.fillMaxWidth().clickable { onOpenStory(s.id) }.padding(vertical = 6.dp)) {
                                    Text(s.title, color = GhostText, style = MaterialTheme.typography.titleSmall, maxLines = 2, overflow = TextOverflow.Ellipsis)
                                    val lead = NewsText.lead(s.summary)
                                    if (lead.isNotBlank() && lead != s.title.trim()) {
                                        Text(lead, color = GhostTextDim, style = MaterialTheme.typography.labelMedium, maxLines = 2, overflow = TextOverflow.Ellipsis)
                                    }
                                    Text(HomeBriefText.outlets(s.outlets, s.lastSeen, stamp), color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                                }
                            }
                        }
                    }
                }
                forYou?.let { f ->
                    if (!f.empty) {
                        Spacer(Modifier.height(20.dp))
                        ForYouCard(f, stamp, onOpenStory, onOpenTarget)
                    }
                }
                Spacer(Modifier.height(16.dp))
            }
        }
        // which chat a question goes to: the recent one (named, a tap opens it, [ new ] starts
        // over), else a new one
        if (continuing.isNotEmpty()) {
            Row(Modifier.fillMaxWidth().padding(horizontal = 6.dp, vertical = 2.dp), verticalAlignment = Alignment.CenterVertically) {
                Text("continues “${continuing.take(40)}${if (continuing.length > 40) "…" else ""}” ›", color = TerminalDim, style = MaterialTheme.typography.labelSmall,
                    maxLines = 1, overflow = TextOverflow.Ellipsis, modifier = Modifier.weight(1f).clickable { onOpenChat() })
                Text("[ new chat ]", color = TerminalGreen, style = MaterialTheme.typography.labelSmall,
                    modifier = Modifier.clickable { onNewChat() }.padding(start = 8.dp))
            }
        }
        AskBox(onAsk)
        Spacer(Modifier.height(8.dp))
    }
}

/**
 * THE WEATHER where the phone is: the conditions now, three days, and under them where it came
 * from (the nearest of the places the box pulls daily, and how fresh). Nothing when the phone has
 * no position yet or the box has no pull; a line says which.
 */
@Composable
private fun WeatherCard(w: BoxClient.Weather?, nowS: Long) {
    Column(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).background(VoidLighter).padding(14.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("WEATHER", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
            InfoButton("weather")
        }
        when {
            w == null -> Text("no position yet, or the box has not answered", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            w.noGeo -> Text("nothing pulled: the box's place list is missing · INTEGRATIONS › Weather", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            w.place.isEmpty() -> Text(w.text.ifBlank { "no forecast on the box yet (pulled once a day)" }, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            else -> {
                Spacer(Modifier.height(4.dp))
                Text(HomeText.weatherNow(w.tempC, w.feelsC, w.code, w.windKmh), color = GhostText, style = MaterialTheme.typography.titleMedium)
                if (w.days.isNotEmpty()) {
                    Spacer(Modifier.height(4.dp))
                    Text(w.days.take(3).joinToString("   ") { HomeText.weatherDay(it.date, it.maxC, it.minC, it.code, it.rainPct) },
                        color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                }
                Spacer(Modifier.height(4.dp))
                Text(HomeText.weatherSource(w.place, w.country, w.fetchedAt, w.places, nowS), color = TerminalDim, style = MaterialTheme.typography.labelSmall)
            }
        }
    }
}

/** One point of the brief: a green dot, the sentence, and a chevron when it opens its story. */
@Composable
private fun BriefPoint(p: HomeText.Point, onTap: () -> Unit) {
    Row(Modifier.fillMaxWidth().clickable { onTap() }.padding(vertical = 7.dp), verticalAlignment = Alignment.Top) {
        Text("•", color = TerminalGreen, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.width(16.dp))
        Text(p.text, color = GhostText, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.weight(1f))
        if (p.story != null) Text(" ›", color = TerminalDim, style = MaterialTheme.typography.bodyMedium)
    }
}

/**
 * BTC and ETH, always, each price rolling when it moves; when they were updated and from how many
 * exchanges under them, with CRYPTO50; a tap opens CRYPTO with the rest.
 */
@Composable
private fun PricesCard(rates: BoxClient.Rates?, fast: BoxClient.Fast?, market: BoxClient.Market?, failed: Boolean,
                       onOpenCrypto: () -> Unit, onOpenCoin: (String) -> Unit) {
    // the age line counts on its own, every second
    var nowS by remember { mutableStateOf(System.currentTimeMillis() / 1000) }
    LaunchedEffect(Unit) {
        while (true) {
            kotlinx.coroutines.delay(1_000)
            nowS = System.currentTimeMillis() / 1000
        }
    }
    Column(Modifier.fillMaxWidth().border(1.dp, TerminalDim, RectangleShape).background(VoidLighter)
        .clickable { onOpenCrypto() }.padding(14.dp)) {
        val r = rates
        val f = fast
        if (r == null && (f == null || f.prices.isEmpty())) {
            Text(if (failed) "no prices: the box did not answer" else "reading the box's prices…", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            return@Column
        }
        val live: Map<String, Pair<Double, Double?>> = HomeText.withFast(r?.index?.associate { ix -> ix.symbol to (ix.price to ix.change24) } ?: emptyMap(), f?.prices ?: emptyMap())
        val coins: List<HomeText.Coin> = live.map { (sym, v) -> HomeText.liveOnly(sym, v.first, v.second) }
        val pinned: List<HomeText.Coin> = HomeText.pinned(coins)
        if (pinned.isEmpty()) Text("no prices on the box yet", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        pinned.forEach { c ->
            // a coin's row opens its page; the rest of the card opens CRYPTO
            Row(verticalAlignment = Alignment.Bottom, modifier = Modifier.clickable { onOpenCoin(c.symbol) }.padding(vertical = 2.dp)) {
                Text(c.symbol, color = TerminalGreen, style = MaterialTheme.typography.titleMedium, modifier = Modifier.width(56.dp))
                Box(Modifier.weight(1f)) {
                    TickingPrice(HomeText.money(c.usd), c.usd, GhostText, MaterialTheme.typography.titleLarge, fontSize = 22.sp, arrow = true)
                }
                val ch = HomeText.change(c.change24)
                Text(ch, color = if (ch.startsWith("-")) Warning else TerminalGreen, style = MaterialTheme.typography.titleSmall)
            }
        }
        // when: the fast lane's pass, else the minute's BTC
        val fastAt: Long = (f?.at ?: 0L) / 1000
        val at: Long = if (fastAt > 0 && f?.prices?.containsKey("BTC") == true) fastAt else (r?.btcAt ?: 0L)
        val fastN: Int = f?.venues?.entries?.firstOrNull { it.key == "BTC" }?.value ?: 0
        val exchanges: Int = if (fastAt > 0 && fastN > 0) fastN else (r?.btcN ?: 0)
        val line = HomeText.pricesLine(at, nowS, exchanges)
        if (line.isNotEmpty()) Text(line, color = TerminalDim, style = MaterialTheme.typography.labelSmall)
        Spacer(Modifier.height(6.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            val m = market
            Text(if (m != null && m.value > 0) "${m.code} " + "%.1f".format(java.util.Locale.US, m.value) + "  " + HomeText.change(m.dayChange) + " today" else "",
                color = GhostTextDim, style = MaterialTheme.typography.labelMedium, modifier = Modifier.weight(1f))
            Text("prices ›", color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
        }
    }
}

/**
 * FOR YOU: places near where my trail last was, to what I photograph; this day in earlier years
 * (its story, or the map on that day); the day's news that touches what I said about myself.
 * Each row opens what it is about.
 */
@Composable
private fun ForYouCard(f: HomeData.ForYou, nowS: Long, onOpenStory: (Long) -> Unit, onOpenTarget: (String) -> Unit) {
    Row(verticalAlignment = Alignment.CenterVertically) { SectionLabel("FOR YOU"); InfoButton("memories") }
    if (f.places.isNotEmpty()) {
        Spacer(Modifier.height(6.dp))
        Text(HomeData.nearFrom(f.from, nowS), color = TerminalDim, style = MaterialTheme.typography.labelSmall)
        f.places.forEach { p ->
            ForYouRow(p.name + (if (p.new) "  · new" else ""), HomeData.placeLine(p), p.why) { onOpenTarget("memories:near") }
        }
    }
    if (f.days.isNotEmpty()) {
        Spacer(Modifier.height(6.dp))
        Text("this day", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
        f.days.forEach { d ->
            ForYouRow(HomeData.yearsAgo(d.yearsAgo) + (if (d.title.isNotBlank()) " · " + d.title else ""), HomeData.dayLine(d), d.lead) {
                onOpenTarget(HomeData.dayTarget(d))
            }
        }
    }
    if (f.stories.isNotEmpty()) {
        Spacer(Modifier.height(6.dp))
        Text("from the news", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
        f.stories.forEach { s -> ForYouRow(s.title, s.why, s.lead) { onOpenStory(s.id) } }
    }
    f.remember?.let { m ->
        Spacer(Modifier.height(6.dp))
        Text(HomeData.rememberHeading(m.kind), color = TerminalDim, style = MaterialTheme.typography.labelSmall)
        ForYouRow(m.title, "", m.body) { onOpenTarget("memories:${m.id}") }
    }
}

@Composable
private fun ForYouRow(title: String, line: String, more: String, onTap: () -> Unit) {
    Row(Modifier.fillMaxWidth().clickable { onTap() }.padding(vertical = 6.dp), verticalAlignment = Alignment.Top) {
        Text("•", color = TerminalGreen, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.width(16.dp))
        Column(Modifier.weight(1f)) {
            Text(title, color = GhostText, style = MaterialTheme.typography.bodyMedium, maxLines = 2, overflow = TextOverflow.Ellipsis)
            if (line.isNotBlank()) Text(line, color = TerminalDim, style = MaterialTheme.typography.labelSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
            if (more.isNotBlank()) Text(more, color = GhostTextDim, style = MaterialTheme.typography.labelMedium, maxLines = 2, overflow = TextOverflow.Ellipsis)
        }
        Text(" ›", color = TerminalDim, style = MaterialTheme.typography.bodyMedium)
    }
}

/** The question box: what is typed here starts a new chat. */
@Composable
private fun AskBox(onAsk: (String) -> Unit) {
    var input by remember { mutableStateOf("") }
    var voiceNote by remember { mutableStateOf("") }
    val canSend = input.isNotBlank()
    Row(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RoundedCornerShape(22.dp))
        .background(VoidLighter, RoundedCornerShape(22.dp)).padding(horizontal = 10.dp, vertical = 4.dp),
        verticalAlignment = Alignment.CenterVertically) {
        BasicTextField(
            value = input, onValueChange = { input = it },
            modifier = Modifier.weight(1f).padding(horizontal = 6.dp, vertical = 10.dp),
            textStyle = MaterialTheme.typography.bodyMedium.copy(color = GhostText),
            cursorBrush = SolidColor(TerminalGreen),
            maxLines = 4,
            keyboardOptions = KeyboardOptions(imeAction = ImeAction.Send),
            keyboardActions = KeyboardActions(onSend = { if (canSend) { onAsk(input.trim()); input = "" } }),
            decorationBox = { inner ->
                if (input.isEmpty()) Text("ask your box…", color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
                inner()
            },
        )
        VoiceAskButton(onWords = { w -> input = if (input.isBlank()) w else input.trimEnd() + " " + w }, onNote = { voiceNote = it })
        Box(Modifier.size(36.dp).clip(CircleShape).background(if (canSend) TerminalGreen else VoidLighter)
            .clickable(enabled = canSend) { onAsk(input.trim()); input = "" },
            contentAlignment = Alignment.Center) {
            Text("›", color = if (canSend) Void else GhostTextDim, fontSize = 20.sp)
        }
    }
    if (voiceNote.isNotEmpty()) Text(voiceNote, color = if (voiceNote.startsWith("!")) Warning else TerminalDim,
        style = MaterialTheme.typography.labelSmall, modifier = Modifier.padding(start = 12.dp, top = 2.dp))
}

/**
 * CRYPTO , the coins behind home's two: CRYPTO50, then the hundred largest Coinbase lists, each at
 * the box's own price (BTC, ETH and SOL seconds old, the rest a minute old), blended from every
 * market it trades in, with its week as a small line and the day's change; a tap opens the coin's
 * page. Pull down to read the box again.
 */
@Composable
fun CryptoScreen(onOpenCoin: (String) -> Unit = {}) {
    val ctx = androidx.compose.ui.platform.LocalContext.current
    // the last list the phone kept (HomeCache), at once; the box's a moment later
    var rates by remember { mutableStateOf<BoxClient.Rates?>(HomeCache.rates(ctx)) }
    var fast by remember { mutableStateOf<BoxClient.Fast?>(HomeCache.fastOf(HomeCache.snap(ctx))) }
    var failed by remember { mutableStateOf(false) }
    var refreshing by remember { mutableStateOf(false) }
    var tick by remember { mutableIntStateOf(0) }
    var nowS by remember { mutableStateOf(System.currentTimeMillis() / 1000) }
    LaunchedEffect(tick) {
        while (true) {
            val r = BoxClient.rates(ctx, keep = true)
            if (r != null) rates = r
            failed = r == null && rates == null
            refreshing = false
            kotlinx.coroutines.delay(60_000)
        }
    }
    LaunchedEffect(Unit) {
        while (true) {
            BoxClient.fast(ctx)?.let { fast = it }
            nowS = System.currentTimeMillis() / 1000
            kotlinx.coroutines.delay(5_000)
        }
    }
    LaunchedEffect(Unit) {
        while (true) {
            kotlinx.coroutines.delay(1_000)
            nowS = System.currentTimeMillis() / 1000
        }
    }
    // each coin's week, for its row's line (ten minutes old at most on the box)
    var sparks by remember { mutableStateOf<Map<String, List<Double>>>(emptyMap()) }
    LaunchedEffect(tick) { BoxClient.sparks(ctx)?.let { sparks = it } }
    Column(Modifier.fillMaxSize().padding(horizontal = 20.dp).padding(top = 16.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) { SectionLabel("CRYPTO PRICES"); InfoButton("prices") }
        Spacer(Modifier.height(6.dp))
        Refreshable(refreshing, { refreshing = true; tick++ }, Modifier.weight(1f).fillMaxWidth()) {
            val r = rates
            val f = fast
            val live: Map<String, Pair<Double, Double?>> = HomeText.withFast(r?.index?.associate { ix -> ix.symbol to (ix.price to ix.change24) } ?: emptyMap(), f?.prices ?: emptyMap())
            val ranks: List<HomeText.Coin> = r?.ranks?.map { c -> HomeText.Coin(c.rank, c.symbol.uppercase(), c.name, c.priceUsd, c.change24, c.marketCap, c.supply) } ?: emptyList()
            val rows: List<HomeText.Coin> = HomeText.merge(ranks, live, n = 100)
            LazyColumn(Modifier.fillMaxSize()) {
                when {
                    r == null && failed -> item { ErrorLine("the box did not answer , is it unlocked? pull down to try again") }
                    r == null -> item { LoadingRow() }
                    else -> {
                        // one line on top: the market as a whole, and when the prices were made
                        val fastAt: Long = (f?.at ?: 0L) / 1000
                        val minuteAt: Long = r.index.maxOfOrNull { it.at } ?: 0L
                        item {
                            val m = r.market
                            Text(listOf(
                                if (m != null && m.value > 0) "${m.code} " + "%.1f".format(java.util.Locale.US, m.value) + " " + HomeText.change(m.dayChange) else "",
                                HomeText.updated(maxOf(fastAt, minuteAt), nowS),
                            ).filter { it.isNotBlank() }.joinToString(" · "), color = TerminalDim, style = MaterialTheme.typography.labelSmall,
                                modifier = Modifier.padding(bottom = 8.dp))
                        }
                        if (rows.isEmpty()) item { EmptyLine("no rank list on the box yet: Coinbase's is fetched hourly") }
                        items(rows, key = { it.symbol + it.rank }) { c -> CoinLine(c, sparks[c.symbol] ?: emptyList()) { onOpenCoin(c.symbol) } }
                    }
                }
            }
        }
    }
}

/** A coin's row: rank, symbol and name (with its cap), its week as a line, the price and the day's change. */
@Composable
private fun CoinLine(c: HomeText.Coin, week: List<Double>, onOpen: () -> Unit) {
    Row(Modifier.fillMaxWidth().clickable { onOpen() }.padding(vertical = 7.dp), verticalAlignment = Alignment.CenterVertically) {
        Text("${c.rank}", color = GhostTextDim, style = MaterialTheme.typography.labelSmall, modifier = Modifier.width(28.dp))
        Column(Modifier.weight(1f)) {
            Text(c.symbol, color = TerminalGreen, style = MaterialTheme.typography.titleSmall)
            Text(c.name + (if (c.cap > 0) " · " + HomeText.cap(c.cap) else ""), color = GhostTextDim,
                style = MaterialTheme.typography.labelSmall, maxLines = 1, overflow = TextOverflow.Ellipsis)
        }
        Sparkline(CoinText.thin(week, 48), Modifier.width(56.dp).height(22.dp))
        Spacer(Modifier.width(10.dp))
        Column(horizontalAlignment = Alignment.End, modifier = Modifier.widthIn(min = 76.dp)) {
            TickingPrice(HomeText.money(c.usd), c.usd, GhostText, MaterialTheme.typography.bodyMedium)
            val ch = HomeText.change(c.change24)
            Text(ch, color = if (ch.startsWith("-")) Warning else TerminalGreen, style = MaterialTheme.typography.labelSmall)
        }
    }
}
