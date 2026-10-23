package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.text.style.TextDecoration
import androidx.compose.ui.unit.dp
import com.localghost.app.net.BoxClient
import com.localghost.app.sync.CalendarSync
import com.localghost.app.ui.theme.*
import kotlinx.coroutines.launch
import java.time.LocalDate
import java.time.ZoneId

/**
 * THE CALENDAR , what the phone's calendar holds, as the box keeps it: the agenda from today
 * (earlier and later a month at a time), a search across two years, each event with its place,
 * its guests and their answers, and its notes. Read-only: the phone's own calendar app is where
 * an event is made or changed, and the box has it within a minute or so of the change.
 */
@Composable
fun CalendarScreen(onOpenDay: (String) -> Unit, backLabel: String = "", onBack: () -> Unit = {}) {
    val ctx = LocalContext.current
    val zone = remember { ZoneId.systemDefault() }
    val today = remember { LocalDate.now(zone) }
    var from by rememberSaveable { mutableStateOf(today.toEpochDay()) }
    var days by rememberSaveable { mutableIntStateOf(30) }
    var q by rememberSaveable { mutableStateOf("") }
    var asked by rememberSaveable { mutableStateOf("") }
    var page by remember { mutableStateOf<CalendarText.Page?>(null) }
    var failed by remember { mutableStateOf(false) }
    var open by remember { mutableStateOf("") }
    LaunchedEffect(from, days, asked) {
        page = null; failed = false
        val start = LocalDate.ofEpochDay(from)
        val (a, _) = CalendarText.dayBounds(start, zone)
        val (_, b) = CalendarText.dayBounds(start.plusDays(days.toLong() - 1), zone)
        val p = if (asked.isNotBlank()) BoxClient.calendar(ctx, q = asked) else BoxClient.calendar(ctx, a, b)
        failed = p == null
        page = p
    }
    val start = LocalDate.ofEpochDay(from)
    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp)) {
        item {
            Spacer(Modifier.height(12.dp))
            if (backLabel.isNotEmpty()) {
                Link(backLabel) { onBack() }
                Spacer(Modifier.height(8.dp))
            }
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("CALENDAR", color = GhostText, style = MaterialTheme.typography.titleLarge)
                InfoButton("calendar")
            }
            Text(CalendarText.synced(page?.meta, System.currentTimeMillis() / 1000), color = TerminalDim, style = MaterialTheme.typography.labelSmall)
            if (!CalendarSync.granted(ctx)) Text("the phone has not allowed reading the calendar , SETTINGS › CALENDAR", color = Warning,
                style = MaterialTheme.typography.labelMedium, modifier = Modifier.padding(top = 4.dp))
            Spacer(Modifier.height(10.dp))
            BasicTextField(q, { q = it }, singleLine = true,
                textStyle = MaterialTheme.typography.bodySmall.copy(color = GhostText), cursorBrush = SolidColor(TerminalGreen),
                keyboardOptions = KeyboardOptions(imeAction = ImeAction.Search),
                keyboardActions = KeyboardActions(onSearch = { asked = q.trim() }),
                decorationBox = { inner -> Box(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).padding(8.dp)) {
                    if (q.isEmpty()) Text("search: a name, a place, a word", color = TerminalDim, style = MaterialTheme.typography.bodySmall); inner() } },
                modifier = Modifier.fillMaxWidth())
            Row(Modifier.padding(top = 6.dp), verticalAlignment = Alignment.CenterVertically) {
                if (asked.isNotBlank()) {
                    Link("‹ back to the agenda") { q = ""; asked = "" }
                } else {
                    Link("‹ earlier") { from -= 30; days = 30 }
                    Spacer(Modifier.width(16.dp))
                    if (start != today) Link("today") { from = today.toEpochDay(); days = 30 }
                    Spacer(Modifier.weight(1f))
                    Link("later ›") { from += 30; days = 30 }
                }
            }
            Spacer(Modifier.height(8.dp))
        }
        val p = page
        when {
            p == null && !failed -> item { LoadingRow() }
            failed -> item { ErrorLine("the box did not answer , is it unlocked?") }
            p != null && p.events.isEmpty() -> item {
                Text(if (asked.isNotBlank()) "nothing matches “$asked” a year either side" else "nothing on the calendar from " +
                    CalendarText.header(start, today).lowercase() + " for $days days",
                    color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            }
            p != null -> {
                val groups = if (asked.isNotBlank()) CalendarText.agenda(p.events, zone)
                    else CalendarText.agenda(p.events, zone, start, start.plusDays(days.toLong()))
                groups.forEach { (d, evs) ->
                    item(key = "h" + d) {
                        Spacer(Modifier.height(14.dp))
                        Text(CalendarText.header(d, today), color = if (d == today) TerminalGreen else TerminalDim,
                            style = MaterialTheme.typography.labelMedium,
                            modifier = if (!d.isAfter(today)) Modifier.clickable { onOpenDay(d.toString()) }.padding(vertical = 2.dp) else Modifier.padding(vertical = 2.dp))
                        Spacer(Modifier.height(4.dp))
                    }
                    items(evs, key = { e -> d.toString() + "/" + e.id + "/" + e.begin }) { e ->
                        val k = d.toString() + "/" + e.id + "/" + e.begin
                        EventRow(e, zone, d, open == k) { open = if (open == k) "" else k }
                    }
                }
                item { Spacer(Modifier.height(24.dp)) }
            }
        }
    }
}

@Composable
private fun Link(text: String, onTap: () -> Unit) {
    Text(text, color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
        modifier = Modifier.clickable { onTap() }.padding(vertical = 4.dp))
}

/** One event: the time, the title and its mark, the place and guests; tapped, the guests'
 *  answers and the notes. */
@Composable
internal fun EventRow(e: CalendarText.Event, zone: ZoneId, on: LocalDate?, expanded: Boolean, onTap: () -> Unit) {
    val dim = !e.live
    Column(Modifier.fillMaxWidth().padding(vertical = 3.dp).border(1.dp, GhostBorder, RectangleShape).background(VoidLighter)
        .clickable { onTap() }.padding(horizontal = 10.dp, vertical = 8.dp)) {
        Row(verticalAlignment = Alignment.Top) {
            Text(CalendarText.time(e, zone, on), color = if (dim) TerminalDim else TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.width(96.dp))
            Column(Modifier.weight(1f)) {
                Text(CalendarText.title(e), color = if (dim) GhostTextDim else GhostText, style = MaterialTheme.typography.bodyMedium,
                    textDecoration = if (e.status == "cancelled") TextDecoration.LineThrough else null)
                val det = CalendarText.detail(e)
                if (det.isNotEmpty()) Text(det, color = GhostTextDim, style = MaterialTheme.typography.labelSmall, maxLines = if (expanded) 6 else 2)
                val m = CalendarText.mark(e)
                if (m.isNotEmpty()) Text(m, color = if (m == "cancelled" || m == "declined") Warning else TerminalDim, style = MaterialTheme.typography.labelSmall)
            }
        }
        if (expanded) {
            if (e.attendees.isNotEmpty()) {
                Spacer(Modifier.height(6.dp))
                e.attendees.take(30).forEach { a ->
                    Text("  " + a.name.ifBlank { a.email } + (if (a.status.isNotEmpty() && a.status != "none") " · " + a.status else ""),
                        color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                }
                if (e.attendees.size > 30) Text("  and ${e.attendees.size - 30} more", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
            }
            if (e.organizer.isNotBlank() && !e.organizer.equals(e.account, ignoreCase = true))
                Text("organised by " + e.organizer, color = TerminalDim, style = MaterialTheme.typography.labelSmall, modifier = Modifier.padding(top = 4.dp))
            if (e.notes.isNotBlank()) Text(e.notes.trim(), color = GhostText, style = MaterialTheme.typography.bodySmall, modifier = Modifier.padding(top = 6.dp))
        }
    }
}

/** THE DAY page's calendar: what the phone's calendar had that day. Nothing shown when it had
 *  nothing (or the box has no calendar); the day is not about the calendar. */
@Composable
fun DayCalendar(day: String) {
    val ctx = LocalContext.current
    val zone = remember { ZoneId.systemDefault() }
    val d = remember(day) { runCatching { LocalDate.parse(day) }.getOrNull() } ?: return
    var evs by remember(day) { mutableStateOf<List<CalendarText.Event>>(emptyList()) }
    var open by remember(day) { mutableStateOf("") }
    LaunchedEffect(day) {
        val (a, b) = CalendarText.dayBounds(d, zone)
        evs = BoxClient.calendar(ctx, a, b)?.events?.let { CalendarText.onDay(it, d, zone) } ?: emptyList()
    }
    if (evs.isEmpty()) return
    Spacer(Modifier.height(18.dp))
    SectionLabel("ON THE CALENDAR")
    Spacer(Modifier.height(6.dp))
    evs.forEach { e ->
        val k = "" + e.id + "/" + e.begin
        EventRow(e, zone, d, open == k) { open = if (open == k) "" else k }
    }
}

/** HOME's NEXT: what the calendar says comes next, a line an event; a tap opens the agenda. */
@Composable
fun HomeNext(events: List<CalendarText.Event>, onOpen: () -> Unit) {
    if (events.isEmpty()) return
    val zone = remember { ZoneId.systemDefault() }
    val now = System.currentTimeMillis() / 1000
    Column(Modifier.fillMaxWidth().clickable { onOpen() }.padding(vertical = 4.dp)) {
        SectionLabel("NEXT")
        Spacer(Modifier.height(4.dp))
        events.take(3).forEach { e ->
            Row(verticalAlignment = Alignment.Top, modifier = Modifier.padding(vertical = 2.dp)) {
                Text(CalendarText.whenNext(e, now, zone), color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.width(112.dp))
                Column(Modifier.weight(1f)) {
                    Text(CalendarText.title(e), color = GhostText, style = MaterialTheme.typography.bodySmall, maxLines = 1)
                    if (e.location.isNotBlank()) Text(e.location.trim(), color = GhostTextDim, style = MaterialTheme.typography.labelSmall, maxLines = 1)
                }
            }
        }
    }
}

/**
 * SETTINGS › CALENDAR: the permission, the switch, the last sync, a sync now, and forgetting what
 * the box holds.
 */
@Composable
internal fun CalendarSection(onLine: (String) -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var tick by remember { mutableIntStateOf(0) }
    var granted by remember { mutableStateOf(CalendarSync.granted(ctx)) }
    var on by remember { mutableStateOf(CalendarSync.enabled(ctx)) }
    var meta by remember { mutableStateOf<CalendarText.Meta?>(null) }
    var msg by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var armForget by remember { mutableStateOf(false) }
    val ask = androidx.activity.compose.rememberLauncherForActivityResult(
        androidx.activity.result.contract.ActivityResultContracts.RequestPermission()) { ok ->
        granted = ok
        if (ok) {
            CalendarSync.schedule(ctx)
            busy = true; msg = "reading the calendar…"
            scope.launch {
                val r = CalendarSync.sync(ctx, force = true)
                msg = if (r.error.isNotEmpty()) "! " + r.error else "${r.events} events on your box"
                busy = false; tick++
            }
        } else msg = "not allowed , the calendar stays on the phone (your call); the app's settings can allow it later"
    }
    LaunchedEffect(tick) { meta = BoxClient.calendar(ctx, 1, 2)?.meta }
    LaunchedEffect(granted, on, meta) {
        val m = meta
        onLine(when {
            !granted -> "not allowed yet"
            !on -> "off"
            m != null -> "${m.events} events, a year either side"
            else -> "your plans, read here, kept on the box"
        })
    }
    Text("Every calendar this phone syncs, a year back and a year ahead, with the guests and the notes, sent to your box when it changes. " +
        "Nothing is written to any calendar, and no calendar service is asked anything.", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
    Spacer(Modifier.height(8.dp))
    if (!granted) {
        GhostButton("ALLOW THE CALENDAR", onClick = { ask.launch(CalendarSync.PERMISSION) }, modifier = Modifier.fillMaxWidth())
    } else {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(if (on) "sending to the box" else "off , nothing is sent", color = if (on) GhostText else Warning,
                style = MaterialTheme.typography.bodyMedium, modifier = Modifier.weight(1f))
            Text(if (on) "[ turn off ]" else "[ turn on ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable { on = !on; CalendarSync.setEnabled(ctx, on) }.padding(vertical = 6.dp))
        }
        Text(CalendarText.synced(meta, System.currentTimeMillis() / 1000), color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        CalendarSync.lastRun(ctx)?.takeIf { it.error.isNotEmpty() }?.let {
            Text("last try: " + it.error, color = Warning, style = MaterialTheme.typography.labelMedium)
        }
        if (on) Text(if (busy) "[ reading the calendar… ]" else "[ sync now ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.clickable {
                if (!busy) {
                    busy = true; msg = "reading the calendar…"
                    scope.launch {
                        val r = CalendarSync.sync(ctx, force = true)
                        msg = when {
                            r.error.isNotEmpty() -> "! " + r.error
                            r.removed > 0 -> "${r.events} events on your box, ${r.removed} gone from the phone taken off"
                            else -> "${r.events} events on your box"
                        }
                        busy = false; tick++
                    }
                }
            }.padding(vertical = 6.dp))
    }
    Text(if (armForget) "[ tap again: forget the whole calendar on the box ]" else "[ forget the calendar on the box ]",
        color = if (armForget) Warning else TerminalDim, style = MaterialTheme.typography.labelMedium,
        modifier = Modifier.clickable {
            if (!armForget) { armForget = true; return@clickable }
            armForget = false
            scope.launch {
                msg = if (BoxClient.calendarForget(ctx)) "the box holds no calendar now" + (if (on && granted) " , turn sending off too, or it comes back at the next change" else "")
                    else "! the box did not answer"
                tick++
            }
        }.padding(vertical = 6.dp))
    if (msg.isNotEmpty()) Text(msg, color = if (msg.startsWith("!")) Warning else GhostTextDim, style = MaterialTheme.typography.labelMedium)
}
