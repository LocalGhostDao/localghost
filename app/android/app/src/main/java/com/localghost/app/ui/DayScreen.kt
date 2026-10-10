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
import com.localghost.app.ui.theme.*
import kotlinx.coroutines.launch

/**
 * ONE DAY, everything the box has of it: the day's story (written by the box from the photos, the
 * trail, the health sync, the notes and the check-in; a past day without one can be written now),
 * its photos and videos, the outing it was part of, where it went, the steps and the sleep, the
 * notes. The day before and after, the same date a year either side. HOME's "this day" rows and
 * the weekly highlight open here; the trail is one tap on, on the MAP.
 */
@Composable
fun DayScreen(day: String, onDay: (String) -> Unit, onOpenMap: (String) -> Unit, onOpenTarget: (String) -> Unit, onAsk: (String) -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    val today: String = remember { DayText.of(System.currentTimeMillis() / 1000) }
    val bounds: Pair<Long, Long> = remember(day) { DayText.bounds(day) }
    var story by remember(day) { mutableStateOf<BoxClient.DayStory?>(null) }
    var storyRead by remember(day) { mutableStateOf(false) }
    var writing by remember(day) { mutableStateOf(false) }
    var frames by remember(day) { mutableStateOf<List<BoxClient.GalleryFrame>?>(null) }
    var summary by remember(day) { mutableStateOf<BoxClient.DaySummary?>(null) }
    var outings by remember(day) { mutableStateOf<List<BoxClient.MemRow>>(emptyList()) }
    LaunchedEffect(day) {
        story = BoxClient.dayStory(ctx, day)
        storyRead = true
    }
    var framesFailed by remember(day) { mutableStateOf(false) } // the box did not list them: said, not shown as none
    LaunchedEffect(day) { val f = BoxClient.dayFrames(ctx, bounds.first, bounds.second); framesFailed = f == null; frames = f ?: emptyList() }
    LaunchedEffect(day) { summary = BoxClient.daySummary(ctx, bounds) }
    LaunchedEffect(day) {
        outings = BoxClient.memoriesList(ctx)?.filter { m ->
            m.kind == "outing" && m.meta != null && DayText.touches(m.meta.optLong("start"), m.meta.optLong("end"), day)
        } ?: emptyList()
    }
    Column(Modifier.fillMaxSize().padding(horizontal = 20.dp).verticalScroll(rememberScrollState())) {
        Spacer(Modifier.height(12.dp))
        // the days either side, and the same date a year either side
        Row(verticalAlignment = Alignment.CenterVertically) {
            Nav("‹ the day before") { onDay(DayText.shift(day, -1)) }
            Spacer(Modifier.weight(1f))
            if (day < today) Nav("the day after ›") { onDay(DayText.shift(day, 1)) }
        }
        Spacer(Modifier.height(10.dp))
        Text(DayText.heading(day), color = GhostText, style = MaterialTheme.typography.titleLarge)
        Text(DayText.ago(day, today), color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
        Row(Modifier.padding(top = 4.dp)) {
            Nav("‹ a year before") { onDay(DayText.shiftYears(day, -1)) }
            Spacer(Modifier.width(16.dp))
            if (DayText.shiftYears(day, 1) <= today) Nav("a year after ›") { onDay(DayText.shiftYears(day, 1)) }
        }
        Spacer(Modifier.height(16.dp))

        // the story
        val st = story
        SectionLabel("THE DAY")
        Spacer(Modifier.height(6.dp))
        when {
            !storyRead -> LoadingRow()
            st == null && !writing -> ErrorLine("the box did not answer , is it unlocked?")
            st != null && st.summary.isNotBlank() -> {
                if (st.title.isNotBlank()) Text(st.title, color = TerminalGreen, style = MaterialTheme.typography.titleSmall)
                Spacer(Modifier.height(4.dp))
                Text(st.summary, color = GhostText, style = MaterialTheme.typography.bodyMedium)
                if (st.writtenBy.isNotBlank()) Text("written by " + st.writtenBy, color = TerminalDim, style = MaterialTheme.typography.labelSmall)
            }
            writing -> Text("the box is writing this day… (a minute or two)", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            day <= today -> {
                Text("no story of this day yet", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                Text("[ write this day ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable {
                        writing = true
                        scope.launch {
                            BoxClient.dayStory(ctx, day, build = true)?.let { story = it }
                            writing = false
                        }
                    }.padding(vertical = 6.dp))
            }
        }

        // the photos and videos
        val fs = frames
        Spacer(Modifier.height(18.dp))
        SectionLabel("PHOTOS")
        Spacer(Modifier.height(6.dp))
        when {
            fs == null -> LoadingRow()
            framesFailed -> ErrorLine("the box did not answer , is it unlocked?")
            fs.isEmpty() -> Text("none from this day", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            else -> {
                Text(DayText.media(fs.count { it.kind != "video" }, fs.count { it.kind == "video" }) +
                    (fs.mapNotNull { f -> f.place.split(" / ").lastOrNull()?.takeIf { it.isNotBlank() } }.distinct().take(3)
                        .takeIf { it.isNotEmpty() }?.let { " · " + it.joinToString(", ") } ?: ""),
                    color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                Spacer(Modifier.height(6.dp))
                ThumbStrip(fs.map { it.hash }, title = DayText.heading(day), size = 96.dp)
            }
        }

        // the outing it was part of
        outings.forEach { m ->
            Spacer(Modifier.height(18.dp))
            SectionLabel("PART OF AN OUTING")
            Spacer(Modifier.height(6.dp))
            Column(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).background(VoidLighter)
                .clickable { onOpenTarget("memories:${m.id}") }.padding(12.dp)) {
                Text(m.title, color = TerminalGreen, style = MaterialTheme.typography.titleSmall)
                m.outingLine?.let { Text(it, color = TerminalDim, style = MaterialTheme.typography.labelSmall) }
                Spacer(Modifier.height(4.dp))
                Text(m.body, color = GhostText, style = MaterialTheme.typography.bodySmall, maxLines = 4)
            }
        }

        // where it went, the body, the notes
        val sm = summary
        if (sm != null) {
            val body = DayText.body(sm.steps, sm.sleepMinutes, sm.exerciseMinutes)
            if (sm.places.isNotEmpty() || body.isNotEmpty()) {
                Spacer(Modifier.height(18.dp))
                SectionLabel("WHERE AND HOW")
                Spacer(Modifier.height(6.dp))
                if (sm.places.isNotEmpty()) Text("⌖ " + sm.places.take(8).joinToString(" · "), color = GhostText, style = MaterialTheme.typography.bodySmall)
                if (body.isNotEmpty()) Text(body, color = GhostTextDim, style = MaterialTheme.typography.labelMedium, modifier = Modifier.padding(top = 4.dp))
            }
            if (sm.notes.isNotEmpty()) {
                Spacer(Modifier.height(18.dp))
                SectionLabel("NOTES")
                Spacer(Modifier.height(6.dp))
                sm.notes.take(8).forEach { n -> Text("· $n", color = GhostText, style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(vertical = 2.dp)) }
            }
        }
        if (storyRead && fs != null && fs.isEmpty() && (st == null || st.summary.isBlank()) && outings.isEmpty() &&
            (sm == null || (sm.places.isEmpty() && sm.notes.isEmpty() && sm.steps == 0))) {
            Spacer(Modifier.height(12.dp))
            Text(DayText.nothing(day, today), color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        }

        Spacer(Modifier.height(20.dp))
        Row {
            Nav("[ the trail on the MAP ]") { onOpenMap(day) }
            Spacer(Modifier.width(16.dp))
            Nav("[ ask about this day ]") { onAsk("What did I do on " + DayText.heading(day) + "?") }
        }
        Spacer(Modifier.height(24.dp))
    }
}

@Composable
private fun Nav(text: String, onTap: () -> Unit) {
    Text(text, color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
        modifier = Modifier.clickable { onTap() }.padding(vertical = 4.dp))
}
