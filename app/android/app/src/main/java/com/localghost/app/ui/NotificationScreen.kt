package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import com.localghost.app.net.BoxClient
import com.localghost.app.net.HomeData
import com.localghost.app.net.PendingNotification
import com.localghost.app.ui.theme.*
import kotlinx.coroutines.launch

/**
 * ONE NOTIFICATION'S PAGE, where a tap on it lands: who said it and when, the whole of what it
 * said, its question with the choices when it is one, and under it the thing it is about, shown
 * here rather than only linked: the day (its story and photos), the memory, the day's top stories,
 * the places near you. A button goes on to the full screen. Marked seen on opening; ✕ deletes it
 * on the box.
 */
@Composable
fun NotificationScreen(id: Long, onOpenTarget: (NotifLink.Target) -> Unit, onDay: (String) -> Unit, onBack: () -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var n by remember(id) { mutableStateOf<PendingNotification?>(null) }
    var missing by remember(id) { mutableStateOf(false) }
    var answering by remember(id) { mutableStateOf("") }
    LaunchedEffect(id) {
        val h = BoxClient.notificationHistory(ctx)
        val found = h?.firstOrNull { it.id == id }
        if (found == null) { missing = h != null; return@LaunchedEffect }
        n = found
        if (!found.seen && BoxClient.notificationSeen(ctx, id)) n = found.copy(seen = true)
    }
    Column(Modifier.fillMaxSize().padding(horizontal = 20.dp).verticalScroll(rememberScrollState())) {
        Spacer(Modifier.height(12.dp))
        val nn = n
        when {
            missing -> {
                ErrorLine("this notification is no longer on the box (a week is kept)")
                Nav("‹ all notifications") { onBack() }
            }
            nn == null -> LoadingRow()
            else -> {
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text(NotifPage.who(nn.daemonId, nn.kind), color = TerminalGreen, style = MaterialTheme.typography.labelMedium, modifier = Modifier.weight(1f))
                    if (nn.created > 0) Text(NotificationTime.ago(System.currentTimeMillis() / 1000 - nn.created), color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                    Text("  ✕", color = TerminalDim, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.clickable { scope.launch { if (BoxClient.notificationDelete(ctx, id)) onBack() } }.padding(start = 8.dp))
                }
                Spacer(Modifier.height(8.dp))
                Text(nn.title, color = GhostText, style = MaterialTheme.typography.titleLarge)
                Spacer(Modifier.height(6.dp))
                Text(nn.body, color = GhostText, style = MaterialTheme.typography.bodyMedium)
                if (nn.created > 0) {
                    Text(java.text.SimpleDateFormat("EEEE d MMMM · HH:mm", java.util.Locale.UK).format(java.util.Date(nn.created * 1000)),
                        color = TerminalDim, style = MaterialTheme.typography.labelSmall, modifier = Modifier.padding(top = 4.dp))
                }
                // an ask: the choices, one tap each; the answer once given
                if (nn.options.isNotEmpty()) {
                    Spacer(Modifier.height(12.dp))
                    Text(NotifPage.askLine(nn.options, nn.answer), color = if (nn.answer.isEmpty()) TerminalGreen else GhostTextDim, style = MaterialTheme.typography.labelMedium)
                    if (nn.answer.isEmpty()) {
                        Spacer(Modifier.height(6.dp))
                        Row {
                            nn.options.forEach { opt ->
                                Text("[ $opt ]", color = if (answering == opt) GhostTextDim else TerminalGreen, style = MaterialTheme.typography.labelMedium,
                                    modifier = Modifier.clickable(enabled = answering.isEmpty()) {
                                        answering = opt
                                        scope.launch {
                                            if (BoxClient.notificationAnswer(ctx, id, opt)) n = nn.copy(answer = opt)
                                            answering = ""
                                        }
                                    }.padding(end = 14.dp, top = 4.dp, bottom = 4.dp))
                            }
                        }
                    }
                }
                // what it is about, here
                val target = NotifLink.resolve(nn.link, nn.daemonId, nn.kind)
                Spacer(Modifier.height(18.dp))
                when (NotifPage.shows(target)) {
                    "day" -> AboutDay(NotifPage.dayOf(target)) { onDay(NotifPage.dayOf(target)) }
                    "memory" -> AboutMemory(NotifPage.memoryOf(target)) { onOpenTarget(target) }
                    "near" -> AboutNear { onOpenTarget(target) }
                    "news" -> AboutNews { onOpenTarget(target) }
                    "memories", "checkin", "status" -> Nav(NotifPage.goes(target)) { onOpenTarget(target) }
                }
                Spacer(Modifier.height(20.dp))
                Nav("‹ all notifications") { onBack() }
            }
        }
        Spacer(Modifier.height(24.dp))
    }
}

@Composable
private fun Nav(text: String, onTap: () -> Unit) {
    Text(text, color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
        modifier = Modifier.clickable { onTap() }.padding(vertical = 6.dp))
}

/** The day a notification is about: its story and its photos, then the whole day. */
@Composable
private fun AboutDay(day: String, onOpen: () -> Unit) {
    val ctx = LocalContext.current
    var story by remember(day) { mutableStateOf<BoxClient.DayStory?>(null) }
    var frames by remember(day) { mutableStateOf<List<BoxClient.GalleryFrame>?>(null) }
    LaunchedEffect(day) { story = BoxClient.dayStory(ctx, day) }
    LaunchedEffect(day) {
        val (a, b) = DayText.bounds(day)
        frames = BoxClient.dayFrames(ctx, a, b) ?: emptyList()
    }
    SectionLabel(DayText.heading(day).uppercase())
    Spacer(Modifier.height(6.dp))
    story?.takeIf { it.summary.isNotBlank() }?.let { st ->
        if (st.title.isNotBlank()) Text(st.title, color = TerminalGreen, style = MaterialTheme.typography.titleSmall)
        Text(st.summary, color = GhostText, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.padding(top = 4.dp))
    }
    frames?.takeIf { it.isNotEmpty() }?.let { fs ->
        Spacer(Modifier.height(8.dp))
        Text(DayText.media(fs.count { it.kind != "video" }, fs.count { it.kind == "video" }), color = TerminalDim, style = MaterialTheme.typography.labelSmall)
        Spacer(Modifier.height(4.dp))
        ThumbStrip(fs.map { it.hash }, title = DayText.heading(day))
    }
    Spacer(Modifier.height(8.dp))
    Nav("the whole day ›") { onOpen() }
}

/** The memory a notification brought back: its title, its covers and the first of its body; a tap
 *  anywhere on it, or the line under, opens the memory's own page. */
@Composable
private fun AboutMemory(id: Long, onOpen: () -> Unit) {
    val ctx = LocalContext.current
    var m by remember(id) { mutableStateOf<BoxClient.MemRow?>(null) }
    LaunchedEffect(id) { m = BoxClient.memoriesList(ctx)?.firstOrNull { it.id == id } }
    m?.let { row ->
        SectionLabel("THE MEMORY")
        Spacer(Modifier.height(6.dp))
        Column(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).background(VoidLighter)
            .clickable { onOpen() }.padding(12.dp)) {
            Text(row.title, color = TerminalGreen, style = MaterialTheme.typography.titleSmall)
            Text(MemoryText.origin(row.kind, row.summaryLine, row.meta?.optString("line") ?: ""), color = TerminalDim, style = MaterialTheme.typography.labelSmall)
            if (row.covers.isNotEmpty()) {
                Spacer(Modifier.height(8.dp))
                ThumbStrip(row.covers.take(8), title = row.title, size = 64.dp)
            }
            Spacer(Modifier.height(6.dp))
            Text(row.body, color = GhostText, style = MaterialTheme.typography.bodySmall, maxLines = 6)
        }
        Spacer(Modifier.height(8.dp))
    }
    Nav("the memory ›") { onOpen() }
}

/** The places near the trail's newest point, as MEMORIES › near you ranks them. */
@Composable
private fun AboutNear(onOpen: () -> Unit) {
    val ctx = LocalContext.current
    var near by remember { mutableStateOf<BoxClient.Nearby?>(null) }
    var noFix by remember { mutableStateOf(false) }
    LaunchedEffect(Unit) {
        val p = com.localghost.app.sync.LocationLog.newest(ctx)
        if (p == null) { noFix = true; return@LaunchedEffect }
        near = BoxClient.nearby(ctx, p.lat, p.lon, 15)
    }
    SectionLabel("NEAR YOU")
    Spacer(Modifier.height(6.dp))
    when {
        noFix -> Text("no position on this phone yet", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        near == null -> LoadingRow()
        else -> near!!.suggestions.take(5).forEach { s ->
            Column(Modifier.fillMaxWidth().padding(vertical = 4.dp)) {
                Text(s.name + (if (s.kind.isNotEmpty()) " · ${s.kind}" else ""), color = GhostText, style = MaterialTheme.typography.bodyMedium)
                Text(HomeData.distance(s.distanceKm) + " " + s.bearing + " · " + s.why, color = GhostTextDim, style = MaterialTheme.typography.labelSmall)
            }
        }
    }
    Spacer(Modifier.height(8.dp))
    Nav("near you ›") { onOpen() }
}

/** The day's news: the brief's points. */
@Composable
private fun AboutNews(onOpen: () -> Unit) {
    val ctx = LocalContext.current
    var news by remember { mutableStateOf<BoxClient.News?>(com.localghost.app.net.HomeCache.news(ctx)) }
    LaunchedEffect(Unit) { BoxClient.news(ctx, since = System.currentTimeMillis() / 1000 - 86_400, keep = true)?.let { news = it } }
    SectionLabel("THE DAY'S NEWS")
    Spacer(Modifier.height(6.dp))
    news?.let { n ->
        val points = HomeText.points(n.brief, n.briefStories)
        if (points.isEmpty()) Text(HomeText.noBrief(n.stories.size), color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        points.take(8).forEach { p ->
            Row(Modifier.fillMaxWidth().padding(vertical = 4.dp)) {
                Text("•", color = TerminalGreen, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.width(16.dp))
                Text(p.text, color = GhostText, style = MaterialTheme.typography.bodyMedium)
            }
        }
    } ?: LoadingRow()
    Spacer(Modifier.height(8.dp))
    Nav("NEWS ›") { onOpen() }
}
