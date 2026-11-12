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
 * ONE MEMORY'S PAGE. A notification that brings a memory back ("a year ago today you ...") used to
 * land on MEMORIES with the list filtered to that one title, the page's chrome and chips and all;
 * it lands here now, on the memory alone: what kind of thing it is, the title, its photos (an
 * outing's or a day's covers, a tap opens them as a slideshow), the body in full, where it came
 * from and when, the day it was made from when there is one, and the person's two powers over it,
 * edit and delete (the model never overwrites an edit or resurrects a deletion). A memory's title
 * in the list opens here too.
 *
 * Two things more, from how the box keeps memories: the ANCHOR, the person's mark on one of the
 * few memories their life hangs on (set and lifted here and nowhere else; the box never guesses
 * one), and the COMPANY the memory keeps, the others the box holds with it and why (who it
 * names, where it was, the day it was written, what it is part of), each a tap away. Opening
 * the page tells the box the memory was looked at, which is one of the things it weighs.
 */
@Composable
fun MemoryScreen(id: Long, onOpenDay: (String) -> Unit, onOpenMemory: (Long) -> Unit = {}, backLabel: String = "memories", onBack: () -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var m by remember(id) { mutableStateOf<BoxClient.MemRow?>(null) }
    var missing by remember(id) { mutableStateOf(false) }
    var editing by remember(id) { mutableStateOf(false) }
    var confirmDel by remember(id) { mutableStateOf(false) }
    // THE PARTS of a trip (its outings and days) or of an outing away (its days): the rows the box
    // folded under this one, oldest first
    var parts by remember(id) { mutableStateOf<List<BoxClient.MemRow>>(emptyList()) }
    var whole by remember(id) { mutableStateOf<BoxClient.MemRow?>(null) }
    // THE COMPANY IT KEEPS: the memories linked to this one, and why (null until the box answers)
    var links by remember(id) { mutableStateOf<List<BoxClient.MemLink>?>(null) }
    var allLinks by remember(id) { mutableStateOf(false) }
    var failed by remember(id) { mutableStateOf(false) } // the box did not answer the load
    var note by remember(id) { mutableStateOf("") } // an edit or delete that did not land
    fun load() {
        scope.launch {
            // the row, its parts and its whole each come from the box by name, never from the
            // feed: the feed is cut at a limit, and a day under an old trip or month is among
            // the first rows past it (it opened as "not on the box any more")
            val one = BoxClient.memoryById(ctx, id)
            val found = one?.firstOrNull()
            if (found != null) {
                m = found
                parts = if (found.ref.isEmpty()) emptyList() else (BoxClient.memoriesParts(ctx, found.ref) ?: emptyList())
                whole = found.partOf.takeIf { it.isNotEmpty() }?.let { ref -> BoxClient.memoryByRef(ctx, ref)?.firstOrNull() }
                failed = false
            } else { missing = one != null; failed = one == null && m == null }
        }
    }
    LaunchedEffect(id) { load() }
    // once per opening, not per reload: an edit that reloads the page is not a second look
    LaunchedEffect(id) { links = BoxClient.memoryOpen(ctx, id) }
    Column(Modifier.fillMaxSize().padding(horizontal = 20.dp).verticalScroll(rememberScrollState())) {
        Spacer(Modifier.height(12.dp))
        Text("‹ $backLabel", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.clickable { onBack() }.padding(vertical = 4.dp))
        Spacer(Modifier.height(8.dp))
        val row = m
        if (note.isNotEmpty()) Text(note, color = Warning, style = MaterialTheme.typography.labelMedium)
        when {
            missing -> ErrorLine("this memory is not on the box any more (deleted, or the box was rebuilt)")
            row == null && failed -> ErrorLine("the box did not answer , is it unlocked?")
            row == null -> LoadingRow()
            editing -> MemoryEditor(initTitle = row.title, initBody = row.body,
                // an edit the box did not take keeps the editor open with the words in it
                onSave = { t, b -> scope.launch {
                    if (BoxClient.memoryEdit(ctx, id, t, b)) { editing = false; note = ""; load() }
                    else note = "! the box did not keep the edit , is it unlocked? your words are still below"
                } },
                onCancel = { editing = false })
            else -> {
                SectionLabel(MemoryText.kindLabel(row.kind))
                Spacer(Modifier.height(6.dp))
                Text(row.title, color = GhostText, style = MaterialTheme.typography.titleLarge)
                Spacer(Modifier.height(4.dp))
                Text(MemoryText.origin(row.kind, row.summaryLine, row.meta?.optString("line") ?: "") + " · " +
                    java.text.SimpleDateFormat("d MMMM yyyy", java.util.Locale.UK).format(java.util.Date(row.createdAt)),
                    color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                if (row.anchor) {
                    Spacer(Modifier.height(4.dp))
                    Text(MemoryText.ANCHOR_LINE, color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
                }
                if (row.front) {
                    Spacer(Modifier.height(2.dp))
                    Text(MemoryText.FRONT_LINE, color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                }
                if (row.covers.isNotEmpty()) {
                    Spacer(Modifier.height(12.dp))
                    ThumbStrip(row.covers, title = row.title, size = 112.dp)
                }
                if (row.body.isNotBlank()) {
                    Spacer(Modifier.height(14.dp))
                    Text(row.body, color = GhostText, style = MaterialTheme.typography.bodyMedium)
                }
                // READ AGAIN: what the box found when it checked this against the record it was
                // made from. A doubt is the person's to settle: edit, delete, or leave it.
                val formatDay = { ms: Long -> java.text.SimpleDateFormat("d MMMM", java.util.Locale.UK).format(java.util.Date(ms)) }
                row.doubt?.let { d ->
                    Spacer(Modifier.height(12.dp))
                    Text(MemoryText.doubtLine(d.optString("ref", ""), d.optString("found", ""), d.optLong("at", 0L), formatDay),
                        color = Warning, style = MaterialTheme.typography.labelMedium)
                }
                if (row.doubtedFacts.isNotEmpty()) {
                    Spacer(Modifier.height(12.dp))
                    SectionLabel("NOT SURE OF THESE ANY MORE")
                    Spacer(Modifier.height(4.dp))
                    row.doubtedFacts.forEach { (fact, found) ->
                        Text("? " + MemoryText.factDoubt(fact, found), color = Warning, style = MaterialTheme.typography.labelMedium,
                            modifier = Modifier.padding(vertical = 2.dp))
                    }
                    Text("left out of what the box tells of them until you decide; ✎ to rewrite, or delete the memory",
                        color = TerminalDim, style = MaterialTheme.typography.labelSmall, modifier = Modifier.padding(top = 4.dp))
                }
                if (row.doubt == null && row.doubtedFacts.isEmpty()) MemoryText.confirmedLine(row.verifiedAt, formatDay)?.let { line ->
                    Spacer(Modifier.height(8.dp))
                    Text(line, color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                }
                // what it was made from: an outing's or a day's facts, as synthd kept them
                row.meta?.let { meta ->
                    val facts = ArrayList<String>()
                    // an outing: its places (most photographed first) and its country; its tags
                    val places = meta.optJSONArray("places")?.let { a -> (0 until a.length()).map { a.optString(it) }.filter { it.isNotBlank() } } ?: emptyList()
                    val where = (places.ifEmpty { listOf(meta.optString("place")) }.filter { it.isNotBlank() } +
                        listOfNotNull(meta.optString("country").takeIf { it.isNotBlank() })).distinct()
                    if (where.isNotEmpty()) facts.add("⌖ " + where.take(5).joinToString(" · "))
                    val tags = meta.optJSONArray("tags")?.let { a -> (0 until a.length()).mapNotNull { i ->
                        a.optJSONObject(i)?.optString("tag")?.takeIf { it.isNotBlank() } ?: a.optString(i).takeIf { it.isNotBlank() && a.optJSONObject(i) == null } } } ?: emptyList()
                    if (tags.isNotEmpty()) facts.add(tags.take(8).joinToString(" · "))
                    val stays = meta.optInt("stays"); val moves = meta.optInt("moves")
                    if (stays > 0 || moves > 0) facts.add(listOfNotNull(
                        stays.takeIf { it > 0 }?.let { "$it stop${if (it == 1) "" else "s"}" },
                        moves.takeIf { it > 0 }?.let { "$it move${if (it == 1) "" else "s"}" }).joinToString(" · "))
                    val walk = meta.optDouble("walkM", 0.0); val ride = meta.optDouble("rideM", 0.0)
                    if (walk >= 500 || ride >= 500) facts.add(listOfNotNull(
                        walk.takeIf { it >= 500 }?.let { "walked %.1f km".format(java.util.Locale.US, it / 1000) },
                        ride.takeIf { it >= 500 }?.let { "rode %.0f km".format(java.util.Locale.US, it / 1000) }).joinToString(" · "))
                    if (facts.isNotEmpty()) {
                        Spacer(Modifier.height(14.dp))
                        SectionLabel("FROM")
                        Spacer(Modifier.height(4.dp))
                        facts.forEach { Text(it, color = GhostTextDim, style = MaterialTheme.typography.labelMedium, modifier = Modifier.padding(vertical = 1.dp)) }
                    }
                }
                // what this is a part of, and what is part of this
                whole?.let { w ->
                    Spacer(Modifier.height(14.dp))
                    Text("part of ${w.title} ›", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.clickable { onOpenMemory(w.id) }.padding(vertical = 4.dp))
                }
                if (parts.isNotEmpty()) {
                    Spacer(Modifier.height(14.dp))
                    SectionLabel(MemoryText.partsLabel(row.kind))
                    Spacer(Modifier.height(4.dp))
                    parts.forEach { p ->
                        val pd = MemoryText.dayOf(p.ref)
                        Row(Modifier.fillMaxWidth().clickable {
                            if (p.kind == "day" && pd.isNotEmpty()) onOpenDay(pd) else onOpenMemory(p.id)
                        }.padding(vertical = 5.dp), verticalAlignment = Alignment.CenterVertically) {
                            Text(if (p.kind == "day") "◷" else "◇", color = TerminalDim, style = MaterialTheme.typography.labelMedium, modifier = Modifier.width(22.dp))
                            Text(p.title, color = GhostText, style = MaterialTheme.typography.bodySmall, modifier = Modifier.weight(1f), maxLines = 1)
                            Text(MemoryText.partLabel(p.kind) + " ›", color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
                        }
                    }
                }
                // THE COMPANY IT KEEPS: what the box holds with this memory, and why. The whole and
                // the parts are above already, so a link that only says "part" is not said twice.
                val company = (links ?: emptyList()).filter { it.why != "part" }
                if (company.isNotEmpty()) {
                    Spacer(Modifier.height(14.dp))
                    SectionLabel("KEPT WITH")
                    Spacer(Modifier.height(4.dp))
                    (if (allLinks) company else company.take(COMPANY_SHOWN)).forEach { l ->
                        Row(Modifier.fillMaxWidth().clickable { onOpenMemory(l.id) }.padding(vertical = 5.dp), verticalAlignment = Alignment.CenterVertically) {
                            Text(if (l.anchor) "◆" else if (l.kind == "day") "◷" else "◇", color = if (l.anchor) TerminalGreen else TerminalDim,
                                style = MaterialTheme.typography.labelMedium, modifier = Modifier.width(22.dp))
                            Column(Modifier.weight(1f)) {
                                Text(l.title, color = GhostText, style = MaterialTheme.typography.bodySmall, maxLines = 1)
                                Text(MemoryText.linkWhy(l.why, row.kind, l.kind), color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                            }
                            Text("›", color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
                        }
                    }
                    if (!allLinks && company.size > COMPANY_SHOWN) {
                        Text("[ ${company.size - COMPANY_SHOWN} more ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                            modifier = Modifier.clickable { allLinks = true }.padding(vertical = 4.dp))
                    }
                }
                val day = MemoryText.dayOf(row.ref)
                Spacer(Modifier.height(20.dp))
                // THE ANCHOR: the person's mark, set and lifted here. The box never sets one.
                Text(if (row.anchor) "[ ◆ lift the anchor ]" else "[ ◆ mark as an anchor ]",
                    color = if (row.anchor) GhostTextDim else TerminalGreen, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { scope.launch {
                        if (BoxClient.memoryAnchor(ctx, id, !row.anchor)) { note = ""; load() }
                        else note = "! the box did not take the mark , is it unlocked?"
                    } }.padding(vertical = 4.dp))
                if (!row.anchor) Text("for the few your life hangs on; only you can mark one", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                Spacer(Modifier.height(10.dp))
                Row(verticalAlignment = Alignment.CenterVertically) {
                    if (day.isNotEmpty()) {
                        Text(if (row.kind == "trip") "[ the first day › ]" else "[ the day › ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                            modifier = Modifier.clickable { onOpenDay(day) }.padding(vertical = 4.dp))
                        Spacer(Modifier.width(16.dp))
                    }
                    Text("[ ✎ edit ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.clickable { editing = true }.padding(vertical = 4.dp))
                    Spacer(Modifier.width(16.dp))
                    if (confirmDel) {
                        LaunchedEffect(confirmDel) { kotlinx.coroutines.delay(3000); confirmDel = false }
                        Text("[ delete for good? ]", color = Warning, style = MaterialTheme.typography.labelMedium,
                            modifier = Modifier.clickable { scope.launch {
                                if (BoxClient.memoryDelete(ctx, id)) onBack() else { confirmDel = false; note = "! the box did not delete it , is it unlocked?" }
                            } }.padding(vertical = 4.dp))
                    } else {
                        Text("[ ✕ delete ]", color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
                            modifier = Modifier.clickable { confirmDel = true }.padding(vertical = 4.dp))
                    }
                }
                Spacer(Modifier.height(6.dp))
                Text(when (row.kind) {
                    "user" -> "written by you; the box never changes it"
                    else -> "an edit is yours for good: the box never writes over it, and a deletion is never undone by the model"
                }, color = TerminalDim, style = MaterialTheme.typography.labelSmall)
            }
        }
        Spacer(Modifier.height(24.dp))
    }
}

/** How many of a memory's neighbours its page lists before "more". */
private const val COMPANY_SHOWN = 8
