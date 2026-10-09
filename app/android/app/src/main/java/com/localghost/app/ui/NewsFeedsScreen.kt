package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.graphics.SolidColor
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.unit.dp
import com.localghost.app.net.BoxClient
import com.localghost.app.ui.theme.*
import kotlinx.coroutines.launch

/**
 * FEEDS: the publications the box reads, each with how it is doing (when it last answered, how
 * many entries, failures), on or off with a tap, taken off the list with a second tap, and a new
 * one added by its address (an RSS or Atom feed, https). The box fetches a new feed within two
 * hours, sooner on HOME's pull-to-refresh.
 */
@Composable
fun NewsFeedsScreen(onBack: () -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var feeds by remember { mutableStateOf<List<BoxClient.Feed>?>(null) }
    var failed by remember { mutableStateOf(false) }
    var note by remember { mutableStateOf("") }
    var name by rememberSaveable { mutableStateOf("") }
    var url by rememberSaveable { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var removing by remember { mutableStateOf("") } // a feed tapped once to remove: the second tap does it
    LaunchedEffect(Unit) {
        val f = BoxClient.newsFeeds(ctx)
        if (f != null) feeds = f else failed = true
    }
    fun change(body: org.json.JSONObject, what: String) {
        busy = true; note = ""
        scope.launch {
            val (list, why) = BoxClient.newsFeedsChange(ctx, body)
            busy = false
            if (list != null) { feeds = list; note = what } else note = "! $why"
        }
    }
    Column(Modifier.fillMaxSize().padding(horizontal = 20.dp).verticalScroll(rememberScrollState())) {
        Spacer(Modifier.height(12.dp))
        Text("‹ news", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.clickable { onBack() }.padding(vertical = 4.dp))
        Spacer(Modifier.height(8.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            SectionLabel("NEWS FEEDS")
            InfoButton("news")
        }
        Text("the publications your box reads · a tap switches one off, [ remove ] takes it off the list", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        Spacer(Modifier.height(12.dp))
        // ADD ONE
        Column(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).background(VoidLighter).padding(10.dp)) {
            Text("add a feed", color = GhostText, style = MaterialTheme.typography.titleSmall)
            Spacer(Modifier.height(6.dp))
            BasicTextField(url, { url = it.trim() }, singleLine = true,
                textStyle = MaterialTheme.typography.bodySmall.copy(color = GhostText), cursorBrush = SolidColor(TerminalGreen),
                keyboardOptions = KeyboardOptions(keyboardType = KeyboardType.Uri),
                decorationBox = { inner -> Box(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).background(Void).padding(8.dp)) {
                    if (url.isEmpty()) Text("https://… the feed's address (RSS or Atom)", color = TerminalDim, style = MaterialTheme.typography.bodySmall); inner() } },
                modifier = Modifier.fillMaxWidth())
            Spacer(Modifier.height(6.dp))
            BasicTextField(name, { name = it }, singleLine = true,
                textStyle = MaterialTheme.typography.bodySmall.copy(color = GhostText), cursorBrush = SolidColor(TerminalGreen),
                decorationBox = { inner -> Box(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).background(Void).padding(8.dp)) {
                    if (name.isEmpty()) Text("its name, as you want it shown (optional)", color = TerminalDim, style = MaterialTheme.typography.bodySmall); inner() } },
                modifier = Modifier.fillMaxWidth())
            Spacer(Modifier.height(8.dp))
            val can = !busy && url.startsWith("https://") && url.length > 12
            GhostButton(if (busy) "…" else "ADD", {
                if (!can) return@GhostButton
                change(org.json.JSONObject().put("add", org.json.JSONObject().put("name", name).put("url", url)), "added · fetched within two hours")
                name = ""; url = ""
            }, modifier = Modifier.fillMaxWidth(), enabled = can)
        }
        if (note.isNotEmpty()) {
            Spacer(Modifier.height(6.dp))
            Text(note, color = if (note.startsWith("!")) Warning else TerminalDim, style = MaterialTheme.typography.labelMedium)
        }
        Spacer(Modifier.height(12.dp))
        val list = feeds
        when {
            list == null && failed -> ErrorLine("the box did not answer , is it unlocked?")
            list == null -> LoadingRow()
            list.isEmpty() -> Text("no feeds · add one above", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
            else -> list.forEach { f ->
                val now = System.currentTimeMillis() / 1000
                Column(Modifier.fillMaxWidth().padding(vertical = 4.dp).border(1.dp, if (f.enabled) GhostBorder else VoidLighter, RectangleShape)
                    .padding(10.dp)) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text(if (f.enabled) "●" else "○", color = if (f.enabled) TerminalGreen else GhostTextDim, style = MaterialTheme.typography.titleSmall,
                            modifier = Modifier.clickable(enabled = !busy) {
                                change(org.json.JSONObject().put("enable", f.id).put("on", !f.enabled), if (f.enabled) "${f.name} off" else "${f.name} on")
                            }.padding(end = 10.dp))
                        Text(f.name, color = if (f.enabled) GhostText else GhostTextDim, style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f))
                        Text(if (removing == f.id) "[ sure? ]" else "[ remove ]", color = if (removing == f.id) Warning else TerminalDim, style = MaterialTheme.typography.labelSmall,
                            modifier = Modifier.clickable(enabled = !busy) {
                                if (removing != f.id) removing = f.id
                                else { removing = ""; change(org.json.JSONObject().put("remove", f.id), "${f.name} removed") }
                            }.padding(start = 8.dp))
                    }
                    Text(f.url, color = TerminalDim, style = MaterialTheme.typography.labelSmall, maxLines = 1)
                    Text(FeedsText.feedLine(f.enabled, f.lastOk, f.lastFetch, f.lastStatus, f.lastItems, f.failures, now), color = GhostTextDim, style = MaterialTheme.typography.labelSmall)
                }
            }
        }
        Spacer(Modifier.height(24.dp))
    }
}
