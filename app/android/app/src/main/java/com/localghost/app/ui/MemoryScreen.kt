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
    var failed by remember(id) { mutableStateOf(false) } // the box did not answer the load
    var note by remember(id) { mutableStateOf("") } // an edit or delete that did not land
    fun load() {
        scope.launch {
            val list = BoxClient.memoriesList(ctx)
            val found = list?.firstOrNull { it.id == id }
            if (found != null) {
                m = found
                parts = if (found.ref.isEmpty()) emptyList() else list.filter { it.partOf == found.ref }.sortedBy { it.createdAt }
                whole = found.partOf.takeIf { it.isNotEmpty() }?.let { ref -> list.firstOrNull { it.ref == ref } }
                failed = false
            } else { missing = list != null; failed = list == null && m == null }
        }
    }
    LaunchedEffect(id) { load() }
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
                if (row.covers.isNotEmpty()) {
                    Spacer(Modifier.height(12.dp))
                    ThumbStrip(row.covers, title = row.title, size = 112.dp)
                }
                if (row.body.isNotBlank()) {
                    Spacer(Modifier.height(14.dp))
                    Text(row.body, color = GhostText, style = MaterialTheme.typography.bodyMedium)
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
                    SectionLabel(if (row.kind == "trip") "THE OUTINGS AND THE DAYS OF IT" else "THE DAYS OF IT")
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
                val day = MemoryText.dayOf(row.ref)
                Spacer(Modifier.height(20.dp))
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
