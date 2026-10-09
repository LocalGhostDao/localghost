package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.lazy.rememberLazyListState
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.unit.dp
import com.localghost.app.net.BoxClient
import com.localghost.app.sync.BoxFetch
import com.localghost.app.ui.theme.*
import kotlinx.coroutines.launch

/**
 * THE NEWS, as the box tells it: the stories of the last two days, the most-told first, each with
 * the model's grounded summary when it has one (a lead, then the key facts as points) and the
 * outlets telling it. The feeds come to the box (the phone fetches them on Wi-Fi, the box on its
 * own otherwise); the box groups and summarises them, reading a story's article for its summary
 * and letting it go. Opening a story's article is this phone's browser. A line of the box's market
 * numbers sits at the top. Pull down to refresh: the box's copy at once, and the feeds fetched
 * when they are half an hour old. [openStory] (from a point of home's brief) opens and scrolls to
 * that story, once.
 */
@Composable
fun NewsScreen(openStory: Long = 0L, onStoryShown: () -> Unit = {}) {
    val ctx = androidx.compose.ui.platform.LocalContext.current
    val scope = rememberCoroutineScope()
    var news by remember { mutableStateOf<BoxClient.News?>(null) }
    var rates by remember { mutableStateOf<BoxClient.Rates?>(null) }
    var failed by remember { mutableStateOf(false) }
    var fetching by remember { mutableStateOf(false) }
    var refreshing by remember { mutableStateOf(false) }
    var tick by remember { mutableIntStateOf(0) }
    var open by remember { mutableStateOf<Long?>(if (openStory > 0) openStory else null) }
    val list = rememberLazyListState()
    LaunchedEffect(tick) {
        failed = false
        val n = BoxClient.news(ctx)
        if (n == null) failed = true else news = n
        rates = BoxClient.rates(ctx) ?: rates
        refreshing = false
    }
    // the story a point of home's brief asked for: open it and bring it to the top, once
    LaunchedEffect(news, openStory) {
        val n = news ?: return@LaunchedEffect
        if (openStory <= 0) return@LaunchedEffect
        val i = n.stories.indexOfFirst { it.id == openStory }
        if (i >= 0) {
            open = openStory
            list.scrollToItem(i)
        }
        onStoryShown()
    }
    fun fetchFeeds() {
        if (fetching) return
        fetching = true
        scope.launch {
            kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) { BoxFetch.run(ctx, force = true) }
            // the box drains its inbox within half a minute; look again then
            kotlinx.coroutines.delay(35_000)
            fetching = false
            tick++
        }
    }
    val now = System.currentTimeMillis() / 1000
    Column(Modifier.fillMaxSize().padding(horizontal = 20.dp).padding(top = 20.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            SectionLabel("NEWS")
            InfoButton("news")
            Spacer(Modifier.weight(1f))
            Text(if (fetching) "fetching…" else "[ fetch now ]", color = if (fetching) GhostTextDim else TerminalGreen,
                style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable(enabled = !fetching) { fetchFeeds() })
        }
        Spacer(Modifier.height(6.dp))
        rates?.let { r ->
            val marketLine: String = r.market?.let { m -> NewsText.market(m.value, m.dayChange, m.constituents) } ?: ""
            val prices: List<NewsText.Price> = r.index.map { ix -> NewsText.Price(ix.symbol, ix.price, ix.n, ix.at) }
            Text(NewsText.markets(prices, r.fx, r.fxDay, now, marketLine), color = GhostText, style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(4.dp))
        }
        Text(NewsText.status(news?.lastFetch ?: 0, news?.lastDigest ?: 0, now, fetching), color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        Spacer(Modifier.height(12.dp))
        Refreshable(refreshing, {
            refreshing = true
            tick++
            if (NewsText.wantsFetch(news?.lastFetch ?: 0, System.currentTimeMillis() / 1000)) fetchFeeds()
        }, Modifier.weight(1f).fillMaxWidth()) {
            val n = news
            LazyColumn(Modifier.fillMaxSize(), state = list) {
                when {
                    failed && n == null -> item { ErrorLine("the box did not answer , is it unlocked? pull down to try again") }
                    n == null -> item { LoadingRow() }
                    n.stories.isEmpty() -> item { EmptyLine("no stories yet: pull down, or [ fetch now ], to fetch the feeds") }
                    else -> items(n.stories, key = { it.id }) { s ->
                        StoryRow(s, open == s.id, now, onToggle = { open = if (open == s.id) null else s.id },
                            onOpen = { link ->
                                runCatching { ctx.startActivity(android.content.Intent(android.content.Intent.ACTION_VIEW, android.net.Uri.parse(link))) }
                            })
                    }
                }
                item { Spacer(Modifier.height(24.dp)) }
            }
        }
    }
}

@Composable
private fun StoryRow(s: BoxClient.NewsStory, open: Boolean, now: Long, onToggle: () -> Unit, onOpen: (String) -> Unit) {
    Column(Modifier.fillMaxWidth().padding(bottom = 8.dp)) {
        Column(
            Modifier.fillMaxWidth()
                .border(1.dp, if (open) TerminalDim else GhostBorder, RectangleShape)
                .background(if (open) VoidLighter else Void)
                .clickable { onToggle() }
                .padding(14.dp),
        ) {
            Text(s.title, color = if (s.sources > 1) TerminalGreen else GhostText, style = MaterialTheme.typography.titleSmall)
            val told: NewsText.Told = NewsText.told(s.summary)
            if (told.lead.isNotEmpty()) {
                Spacer(Modifier.height(6.dp))
                Text(told.lead, color = GhostText, style = MaterialTheme.typography.bodyMedium)
            }
            // the points: all of them when open, the first two when closed
            val shown: List<String> = if (open) told.points else told.points.take(2)
            if (shown.isNotEmpty()) Spacer(Modifier.height(4.dp))
            shown.forEach { p ->
                Row(Modifier.fillMaxWidth().padding(vertical = 2.dp), verticalAlignment = Alignment.Top) {
                    Text("•", color = TerminalGreen, style = MaterialTheme.typography.bodySmall, modifier = Modifier.width(14.dp))
                    Text(p, color = GhostTextDim, style = MaterialTheme.typography.bodySmall, modifier = Modifier.weight(1f))
                }
            }
            if (!open && told.points.size > shown.size) {
                Text("+ ${told.points.size - shown.size} more", color = TerminalDim, style = MaterialTheme.typography.labelSmall,
                    modifier = Modifier.padding(start = 14.dp, top = 2.dp))
            }
            Spacer(Modifier.height(6.dp))
            Text(NewsText.outlets(s.items.map { it.outlet }, s.lastSeen, now), color = TerminalDim, style = MaterialTheme.typography.labelMedium)
            if (open) {
                // each outlet's own headline, and the article , opened in this phone's browser
                Spacer(Modifier.height(8.dp))
                s.items.forEach { it ->
                    Column(Modifier.fillMaxWidth().padding(vertical = 3.dp)) {
                        Text(it.outlet + ": " + it.title, color = GhostText, style = MaterialTheme.typography.labelMedium)
                        if (it.summary.isNotEmpty() && it.summary != s.summary) {
                            Text(it.summary, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                        }
                        if (it.link.isNotEmpty()) {
                            Text("[ open at ${it.outlet} ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                                modifier = Modifier.clickable { onOpen(it.link) }.padding(vertical = 2.dp))
                        }
                    }
                }
            }
        }
    }
}
