package com.localghost.app.ui

import androidx.compose.animation.animateContentSize
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.compose.ui.Alignment
import com.localghost.app.net.BoxClient
import com.localghost.app.net.LifeContext
import com.localghost.app.ui.theme.*
import kotlinx.coroutines.launch

/**
 * MEMORIES , the real corpus off /v1/memories, SOVEREIGN in both directions: everything here is
 * viewable, editable, addable, and deletable by the person, and the model may never overwrite an
 * edit or resurrect a deletion. ghost.synthd writes the distilled rows (from chats now; from the
 * journal entries framed/voiced/noted/tallyd write, as those land); rows the person authors are
 * kind=user and untouchable from birth. The daily check-in and the voice notes have a page of
 * their own (CHECK-IN); a memory's title opens its own page (MemoryScreen).
 */
@Composable
fun MemoriesScreen(context: LifeContext?, open: String = "", onOpened: () -> Unit = {}, onOpenDay: (String) -> Unit = {},
                   onOpenMemory: (Long) -> Unit = {}, onOpenCheckin: () -> Unit = {}) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var rows by remember { mutableStateOf<List<BoxClient.MemRow>?>(null) }
    var adding by remember { mutableStateOf(false) }
    var memQuery by remember { mutableStateOf("") }
    var memKind by remember { mutableStateOf("all") }
    var otd by remember { mutableStateOf<List<BoxClient.OtdYear>?>(null) }
    var otdOpen by remember { mutableStateOf(false) }
    var otdLoading by remember { mutableStateOf(false) }
    var jotting by remember { mutableStateOf(false) }
    var jotSent by remember { mutableStateOf(false) }
    // WHAT YOU PHOTOGRAPH and NEAR YOU , the taste synthd distils from the photos' tags, and the
    // places around the phone's last fix that fit it. Both load on tap, from the box, never the net.
    var tasteOpen by remember { mutableStateOf(false) }
    var taste by remember { mutableStateOf<BoxClient.Taste?>(null) }
    var tasteLoading by remember { mutableStateOf(false) }
    var nearOpen by remember { mutableStateOf(false) }
    var near by remember { mutableStateOf<BoxClient.Nearby?>(null) }
    var nearLoading by remember { mutableStateOf(false) }
    var nearKm by remember { mutableStateOf(15) }
    // WHERE "NEAR" IS: this phone's newest fix it can read (the sealed last point, or the recent
    // ring while unlocked), else the box's newest trail point. It used to be the phone's last
    // point alone, and when that could not be read it said "no position yet" to a person whose
    // trail was on and on the box.
    var nearFix by remember { mutableStateOf(com.localghost.app.sync.LocationLog.newest(ctx)) }
    var nearFromBox by remember { mutableStateOf(false) }
    var nearLooked by remember { mutableStateOf(false) }
    val trailActive = remember { com.localghost.app.sync.LocationLog.active(ctx) }
    fun loadNear() {
        nearLoading = true
        scope.launch {
            if (nearFix == null) {
                // the box's newest point: the last vertex of the newest day it has a track for
                val t = BoxClient.geoDayTracks(ctx, 2)?.filter { it.n >= 1 && it.times.size == it.n }?.maxByOrNull { it.times.last() }
                if (t != null) {
                    nearFix = com.localghost.app.sync.LocationLog.Point(t.times.last(), t.lat.last(), t.lon.last())
                    nearFromBox = true
                }
                nearLooked = true
            }
            val fix = nearFix
            near = if (fix != null) BoxClient.nearby(ctx, fix.lat, fix.lon, nearKm) else null
            nearLoading = false
        }
    }
    fun reload() { scope.launch { rows = BoxClient.memoriesList(ctx) } }
    LaunchedEffect(Unit) { reload() }
    // what a notification opened: "near" opens NEAR YOU (a memory's id opens its own page, in the
    // shell, and never lands here)
    LaunchedEffect(open) {
        if (open.isEmpty()) return@LaunchedEffect
        if (open == "near") {
            nearOpen = true
            if (near == null && !nearLoading) loadNear()
        }
        onOpened()
    }

    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp)) {
        item {
            Spacer(Modifier.height(12.dp))
            SectionLabel("MEMORIES")
            Spacer(Modifier.height(6.dp))
            if (context != null) {
                Text("indexed on the box · never leaves it", color = GhostTextDim,
                    style = MaterialTheme.typography.labelMedium)
            }
            Spacer(Modifier.height(4.dp))
            Row {
                Text(if (adding) "[ − cancel ]" else "[ + add a memory ]", color = TerminalGreen,
                    style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { adding = !adding; jotting = false })
                Spacer(Modifier.width(14.dp))
                // A JOT goes to the JOURNAL, not straight to memories: noted ingests it, synthd
                // decides at distillation whether it is durable , same path as a shared email.
                Text(if (jotting) "[ − cancel ]" else "[ + jot a note ]", color = TerminalGreen,
                    style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { jotting = !jotting; adding = false })
            }
            if (jotSent) Text("sent to the journal , distilled within minutes", color = TerminalDim,
                style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(4.dp))
            // the check-in and the voice notes moved to their own page
            Text("the daily check-in and your voice notes: CHECK-IN ›", color = TerminalDim,
                style = MaterialTheme.typography.labelMedium, modifier = Modifier.clickable { onOpenCheckin() })
        }
        item {
            // ABOUT ME AND MY PEOPLE: a note the box makes memories from (one per person, and facts
            // about me), and the chat starts every question from
            AboutCard(onSaved = { reload() })
        }
        item {
            // WHAT YOU PHOTOGRAPH , the taste. Tags ranked by the share of photo days they appear on,
            // so a burst of five hundred beach photos on one day counts as one day; then the interests
            // (beaches, harbours, peaks ...) those tags add up to. Assembled on the box from the tags,
            // no model call, refreshed as the pipeline tags more.
            Text(if (tasteOpen) "[ − what you photograph ]" else "[ + what you photograph , what the archive says you like ]",
                color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable {
                    tasteOpen = !tasteOpen
                    if (tasteOpen && taste == null && !tasteLoading) {
                        tasteLoading = true
                        scope.launch { taste = BoxClient.taste(ctx); tasteLoading = false }
                    }
                })
            if (tasteOpen) {
                val t = taste
                when {
                    tasteLoading -> Text("reading the tags…", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                    t == null -> Text("! the box did not answer", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                    t.likes.isEmpty() -> {
                        val msg = if (t.note.isNotBlank()) t.note else if (t.summary.isNotBlank()) t.summary else "nothing tagged yet"
                        Text("! $msg", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                    }
                    else -> TasteCard(t)
                }
            }
        }
        item {
            // NEAR YOU , the taste laid over the box's own map data around the phone's last fix:
            // "a beach 2 km north-east you have never photographed". The position goes to the box,
            // which answers from its own tables; nothing leaves it.
            Text(if (nearOpen) "[ − near you ]" else "[ + near you , places that fit, from your own map data ]",
                color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable {
                    nearOpen = !nearOpen
                    if (nearOpen && near == null && !nearLoading) loadNear()
                })
            if (nearOpen) {
                val n = near
                val fix = nearFix
                when {
                    nearLoading -> Text("asking the box what is within $nearKm km…", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                    fix == null && !trailActive -> Text("! no position , turn on the location trail (settings) and the box can say what is around you",
                        color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                    fix == null && nearLooked -> Text("! the trail is on, but neither this phone nor the box has a point to measure from yet , SETTINGS › LOCATION TRAIL says where it is",
                        color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                    fix == null -> Text("! no position yet", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                    n == null -> Text("! the box did not answer", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                    else -> {
                        Text(TrailStatus.nearFrom(nearFromBox, fix.ts, System.currentTimeMillis() / 1000),
                            color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                        NearbyCard(n, nearKm, onKm = { k -> nearKm = k; near = null; loadNear() })
                    }
                }
            }
        }
        item {
            // ON THIS DAY , synthd's retrospective. Loaded on TAP, not on entry: the first build
            // of a day narrates through the model and can take a minute; cached days are instant.
            Text(if (otdOpen) "[ − on this day ]" else "[ + on this day , what were you doing? ]",
                color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable {
                    otdOpen = !otdOpen
                    if (otdOpen && otd == null && !otdLoading) {
                        otdLoading = true
                        scope.launch { otd = BoxClient.onThisDay(ctx); otdLoading = false }
                    }
                })
            if (otdOpen) {
                when {
                    otdLoading -> Text("composing from your history… (first time today takes a minute)",
                        color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                    otd?.isEmpty() != false -> if (!otdLoading && otd != null)
                        Text("! no history for this date yet , it grows as years of photos and notes accumulate",
                            color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                    else -> {}
                }
            }
        }
        if (otdOpen && otd?.isNotEmpty() == true) items(otd!!, key = { "otd-${it.year}" }) { y ->
            OtdYearCard(y) { onOpenDay(DayText.shiftYears(DayText.of(System.currentTimeMillis() / 1000), -y.yearsAgo)) }
        }
        if (jotting) item {
            MemoryEditor(initTitle = "", initBody = "", onSave = { t, b ->
                scope.launch {
                    jotSent = BoxClient.noteAdd(ctx, (t + "\n\n" + b).trim())
                    jotting = false
                }
            }, onCancel = { jotting = false })
        }
        if (adding) item {
            MemoryEditor(initTitle = "", initBody = "", onSave = { t, b ->
                scope.launch { BoxClient.memoryAdd(ctx, t, b); adding = false; reload() }
            }, onCancel = { adding = false })
        }
        when {
            rows == null -> item {
                Text("reading memories from the box…", color = GhostTextDim,
                    style = MaterialTheme.typography.bodyMedium)
            }
            rows!!.isEmpty() -> item {
                Text("! nothing distilled yet , finished conversations become memories within " +
                     "minutes; photos join as the journal fills", color = TerminalDim,
                    style = MaterialTheme.typography.bodyMedium)
            }
            else -> {
                item {
                    BasicTextField(memQuery, { memQuery = it }, singleLine = true,
                        textStyle = MaterialTheme.typography.bodySmall.copy(color = GhostText),
                        cursorBrush = SolidColor(TerminalGreen),
                        decorationBox = { inner -> Box(Modifier.fillMaxWidth()
                            .border(1.dp, GhostBorder, RectangleShape).padding(8.dp)) {
                            if (memQuery.isEmpty()) Text("filter memories…", color = TerminalDim,
                                style = MaterialTheme.typography.bodySmall); inner() } },
                        modifier = Modifier.fillMaxWidth())
                    Spacer(Modifier.height(6.dp))
                    val yours = rows!!.count { it.kind == "user" }
                    Text("${rows!!.size} memories" + (if (yours > 0) " · $yours yours" else ""),
                        color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                    Spacer(Modifier.height(6.dp))
                    // the kinds, as chips: one picked shows only its memories
                    val counts = MemoryKinds.counts(rows!!.map { it.kind })
                    Row(Modifier.horizontalScroll(rememberScrollState())) {
                        MemoryKinds.all.forEach { k ->
                            val n = counts[k.id] ?: 0
                            if (k.id != "all" && n == 0) return@forEach
                            val on = memKind == k.id
                            Text(k.label + (if (k.id != "all") " $n" else ""), color = if (on) Void else TerminalGreen,
                                style = MaterialTheme.typography.labelMedium,
                                modifier = Modifier.padding(end = 6.dp).border(1.dp, if (on) TerminalGreen else TerminalDim, RectangleShape)
                                    .background(if (on) TerminalGreen else Void).clickable { memKind = k.id }
                                    .padding(horizontal = 10.dp, vertical = 4.dp))
                        }
                    }
                }
                items(rows!!.filter { MemoryKinds.shown(memKind, it.kind, it.partOf) && (memQuery.isBlank() ||
                        it.title.contains(memQuery, true) || it.body.contains(memQuery, true)) },
                    key = { "mem-${it.id}" }) { m ->
                MemoryRowCard(m,
                    onOpen = { onOpenMemory(m.id) },
                    onEdit = { t, b -> scope.launch { BoxClient.memoryEdit(ctx, m.id, t, b); reload() } },
                    onDelete = { scope.launch { BoxClient.memoryDelete(ctx, m.id); reload() } })
                }
            }
        }
        item { Spacer(Modifier.height(20.dp)) }
    }
}

@Composable
private fun OtdYearCard(y: BoxClient.OtdYear, onOpenDay: () -> Unit = {}) {
    val ctx = LocalContext.current
    Column(Modifier.fillMaxWidth().animateContentSize().border(1.dp, GhostBorder, RectangleShape).background(Void).padding(12.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text("${y.year} , ${if (y.yearsAgo == 1) "1 year" else "${y.yearsAgo} years"} ago",
                color = TerminalGreen, style = MaterialTheme.typography.bodyMedium, modifier = Modifier.weight(1f))
            // the whole day: its story, photos, outing, notes
            Text("the day ›", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable { onOpenDay() }.padding(4.dp))
        }
        // the day's title as the box built it (weekday, date, the places), then its route in a line
        if (y.title.isNotBlank()) {
            Text(y.title, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        }
        if (y.line.isNotBlank()) {
            Text(y.line, color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        }
        if (y.narrative.isNotBlank()) {
            Spacer(Modifier.height(4.dp))
            Text(y.narrative, color = GhostText, style = MaterialTheme.typography.bodySmall)
        }
        if (y.places.isNotEmpty()) {
            Spacer(Modifier.height(4.dp))
            Text("⌖ " + y.places.joinToString(" · "), color = TerminalDim,
                style = MaterialTheme.typography.labelMedium)
        }
        if (y.photos.isNotEmpty()) {
            Spacer(Modifier.height(8.dp))
            // a tap opens the day's photos as a slideshow, from the one tapped
            ThumbStrip(y.photos, title = "${y.year}")
        }
        if (y.notes.isNotEmpty()) {
            Spacer(Modifier.height(6.dp))
            y.notes.take(3).forEach { n ->
                Text("· $n", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            }
        }
    }
}

/** One memory in the list: its title (a tap opens the memory's own page), its covers, its body, where
 *  it came from; ✎ edits in place, 🗑 deletes after a second tap. */
@Composable
private fun MemoryRowCard(m: BoxClient.MemRow, onOpen: () -> Unit, onEdit: (String, String) -> Unit, onDelete: () -> Unit) {
    var editing by remember { mutableStateOf(false) }
    var confirmDel by remember { mutableStateOf(false) }
    Column(Modifier.fillMaxWidth().animateContentSize().border(1.dp, GhostBorder, RectangleShape).background(Void).padding(12.dp)) {
        if (editing) {
            MemoryEditor(initTitle = m.title, initBody = m.body,
                onSave = { t, b -> editing = false; onEdit(t, b) },
                onCancel = { editing = false })
        } else {
            Row(verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) {
                Text(m.title + " ›", color = GhostText, style = MaterialTheme.typography.bodyMedium,
                    modifier = Modifier.weight(1f).clickable { onOpen() })
                Text("✎", color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { editing = true }.padding(start = 6.dp))
                if (confirmDel) {
                    LaunchedEffect(confirmDel) { kotlinx.coroutines.delay(3000); confirmDel = false }
                    Text(" [ delete? ]", color = Warning, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.clickable { onDelete() })
                } else {
                    Text(" 🗑", color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.clickable { confirmDel = true })
                }
            }
            // AN OUTING, a TRIP or a DAY carries its photos: the cover frames synthd picked, spread across it.
            if ((m.kind == "outing" || m.kind == "trip" || m.kind == "day") && m.covers.isNotEmpty()) {
                Spacer(Modifier.height(8.dp))
                CoverStrip(m.covers)
            }
            if (m.body.isNotBlank()) {
                Spacer(Modifier.height(4.dp))
                Text(m.body, color = GhostTextDim, style = MaterialTheme.typography.bodySmall)
            }
            Spacer(Modifier.height(4.dp))
            Text(MemoryText.origin(m.kind, m.summaryLine, m.meta?.optString("line") ?: "") + " · " +
                java.text.SimpleDateFormat("MMM d, yyyy", java.util.Locale.US)
                    .format(java.util.Date(m.createdAt)),
                color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        }
    }
}

@Composable
internal fun MemoryEditor(initTitle: String, initBody: String, onSave: (String, String) -> Unit, onCancel: () -> Unit) {
    var title by remember { mutableStateOf(initTitle) }
    var body by remember { mutableStateOf(initBody) }
    Column(Modifier.fillMaxWidth().border(1.dp, TerminalGreen, RectangleShape).padding(10.dp)) {
        BasicTextField(title, { title = it }, singleLine = true,
            textStyle = MaterialTheme.typography.bodyMedium.copy(color = TerminalGreen),
            cursorBrush = SolidColor(TerminalGreen),
            decorationBox = { inner -> Box { if (title.isEmpty()) Text("title", color = TerminalDim); inner() } },
            modifier = Modifier.fillMaxWidth())
        Spacer(Modifier.height(6.dp))
        BasicTextField(body, { body = it },
            textStyle = MaterialTheme.typography.bodySmall.copy(color = GhostText),
            cursorBrush = SolidColor(TerminalGreen),
            decorationBox = { inner -> Box { if (body.isEmpty()) Text("what to remember", color = TerminalDim); inner() } },
            modifier = Modifier.fillMaxWidth().heightIn(min = 40.dp))
        Spacer(Modifier.height(6.dp))
        Row {
            Text("[ save ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable { if (title.isNotBlank()) onSave(title.trim(), body.trim()) })
            Spacer(Modifier.width(12.dp))
            Text("[ cancel ]", color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable { onCancel() })
        }
    }
}

/** The cover frames of an outing, thumbnails off /v1/frames/thumb, loaded as they scroll in; a
 *  tap opens them as a slideshow (a video plays). */
@Composable
private fun CoverStrip(hashes: List<String>) {
    ThumbStrip(hashes)
}

/** The taste: one sentence, then the likes by category with their share of photo days, then the
 *  interests the box will match places against. */
@Composable
private fun TasteCard(t: BoxClient.Taste) {
    Column(Modifier.fillMaxWidth().animateContentSize().border(1.dp, GhostBorder, RectangleShape).background(Void).padding(12.dp)) {
        Text(t.summary, color = GhostText, style = MaterialTheme.typography.bodySmall)
        Spacer(Modifier.height(6.dp))
        Text("over ${t.days} days with a camera out · ${t.photos} photos", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        Spacer(Modifier.height(6.dp))
        val order = listOf("place", "nature", "activity", "food", "animal", "vehicle", "object", "people", "event", "")
        val byCat: Map<String, List<BoxClient.Like>> = t.likes.groupBy { it.category }
        val cats = ArrayList<String>()
        for (c in order) if (byCat.containsKey(c)) cats.add(c)
        for (c in byCat.keys) if (c !in cats) cats.add(c)
        for (cat in cats) {
            val ls = byCat[cat] ?: continue
            Text((cat.ifBlank { "other" }) + ": " + ls.joinToString(" · ") { "${it.tag} ${(it.share * 100).toInt()}%" },
                color = GhostTextDim, style = MaterialTheme.typography.labelMedium, modifier = Modifier.padding(vertical = 1.dp))
        }
        if (t.interests.isNotEmpty()) {
            Spacer(Modifier.height(6.dp))
            Text("places to your taste: " + t.interests.take(6).joinToString(" · ") { it.name },
                color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
        }
    }
}

/** What is around the last fix and fits: name, kind, distance and bearing, then why the box
 *  thinks so and whether you have photographed there before. */
@Composable
private fun NearbyCard(n: BoxClient.Nearby, km: Int, onKm: (Int) -> Unit) {
    Column(Modifier.fillMaxWidth().animateContentSize().border(1.dp, GhostBorder, RectangleShape).background(Void).padding(12.dp)) {
        Row {
            for (k in listOf(5, 15, 40)) {
                val on = k == km
                Text("$k km", color = if (on) Void else TerminalGreen, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.padding(end = 8.dp).border(1.dp, TerminalGreen, RectangleShape)
                        .background(if (on) TerminalGreen else Void).clickable { if (!on) onKm(k) }
                        .padding(horizontal = 10.dp, vertical = 4.dp))
            }
        }
        Spacer(Modifier.height(8.dp))
        if (n.suggestions.isEmpty()) {
            val msg = if (n.note.isNotBlank()) n.note else "nothing within $km km that fits what you photograph"
            Text("! $msg", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        }
        n.suggestions.forEach { s ->
            Column(Modifier.padding(vertical = 4.dp)) {
                Text("${s.name} · ${s.kind} · ${if (s.distanceKm < 1) "${(s.distanceKm * 1000).toInt()} m" else "%.1f km".format(java.util.Locale.US, s.distanceKm)} ${s.bearing}" +
                    (if (s.beenThere == 0) "  · new" else ""),
                    color = if (s.beenThere == 0) GhostText else GhostTextDim, style = MaterialTheme.typography.bodySmall)
                Text(s.why, color = TerminalDim, style = MaterialTheme.typography.labelMedium)
            }
        }
    }
}


/** The note about me and my people: written here, kept on the box, made into memories there. */
@Composable
private fun AboutCard(onSaved: () -> Unit) {
    val ctx = androidx.compose.ui.platform.LocalContext.current
    val scope = rememberCoroutineScope()
    var open by remember { mutableStateOf(false) }
    var about by remember { mutableStateOf<BoxClient.About?>(null) }
    var text by remember { mutableStateOf("") }
    var saving by remember { mutableStateOf(false) }
    LaunchedEffect(open) {
        if (open) BoxClient.about(ctx)?.let { about = it; text = it.text }
    }
    Text(if (open) "[ − about me and my people ]" else "[ + about me and my people ]", color = TerminalGreen,
        style = MaterialTheme.typography.labelMedium, modifier = Modifier.clickable { open = !open })
    if (!open) return
    Column(Modifier.fillMaxWidth().padding(top = 6.dp).border(1.dp, GhostBorder, RectangleShape).background(Void).padding(12.dp)) {
        Text("who I am, and the people in my life: names, who they are to me, what matters. The box makes memories from it, one per person, and every chat starts from it.",
            color = GhostTextDim, style = MaterialTheme.typography.labelSmall)
        Spacer(Modifier.height(8.dp))
        BasicTextField(text, { if (it.length <= 8000) text = it },
            textStyle = MaterialTheme.typography.bodySmall.copy(color = GhostText),
            cursorBrush = SolidColor(TerminalGreen),
            decorationBox = { inner -> Box(Modifier.fillMaxWidth().heightIn(min = 120.dp)
                .border(1.dp, GhostBorder, RectangleShape).padding(8.dp)) {
                if (text.isEmpty()) Text("I'm … I live in … My partner … My friends …", color = TerminalDim,
                    style = MaterialTheme.typography.bodySmall); inner() } },
            modifier = Modifier.fillMaxWidth())
        Spacer(Modifier.height(6.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(if (saving) "saving…" else "[ save ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable(enabled = !saving && text != about?.text) {
                    saving = true
                    scope.launch {
                        BoxClient.saveAbout(ctx, text.trim())?.let { about = it }
                        saving = false
                        onSaved()
                    }
                })
            Spacer(Modifier.width(12.dp))
            val a = about
            if (a != null) Text(AboutText.status(a.name, a.me, a.people, a.pending, a.text.isNotBlank()),
                color = TerminalDim, style = MaterialTheme.typography.labelSmall)
        }
    }
}
