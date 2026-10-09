package com.localghost.app.ui

import androidx.activity.compose.BackHandler
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.text.BasicTextField
import androidx.compose.foundation.text.KeyboardActions
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
import androidx.compose.ui.text.input.ImeAction
import androidx.compose.ui.unit.dp
import com.localghost.app.net.BoxClient
import com.localghost.app.ui.theme.*
import kotlinx.coroutines.launch

/**
 * WIKIPEDIA, the box's own copy, read from its database: a search box, the articles a phrase
 * names (surest first, each saying how it was found), and an article whole with its sections. The
 * state line says what the box has (the edition and the counts, or how far the import is, or that
 * there is none). Nothing here leaves the box: the chat answers "what is X" from the same tables,
 * and this page is where to look when the chat's answer wants checking, or when the question is
 * only a word.
 */
@Composable
fun WikipediaScreen() {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var query by rememberSaveable { mutableStateOf("") }
    var state by remember { mutableStateOf<BoxClient.Wiki?>(null) }
    var hits by remember { mutableStateOf<List<BoxClient.WikiHit>?>(null) }
    var searching by remember { mutableStateOf(false) }
    var article by remember { mutableStateOf<BoxClient.WikiArticle?>(null) }
    var opening by remember { mutableStateOf(0L) }
    var stateFailed by remember { mutableStateOf(false) } // the box did not answer the first ask
    var note by remember { mutableStateOf("") } // why a search or an open came back with nothing
    LaunchedEffect(Unit) { val s = BoxClient.wiki(ctx); if (s != null) state = s else stateFailed = true }
    fun search() {
        val q = query.trim()
        if (q.isEmpty()) return
        searching = true
        article = null
        note = ""
        scope.launch {
            val r = BoxClient.wiki(ctx, q = q)
            // no answer is not "nothing found": the list stays as it was and the line says so
            if (r != null) { state = r; hits = r.hits; stateFailed = false } else note = "! the box did not answer , is it unlocked?"
            searching = false
        }
    }
    fun open(idx: Long) {
        opening = idx
        note = ""
        scope.launch {
            val a = BoxClient.wiki(ctx, idx = idx)?.article
            if (a != null) article = a else note = "! the box did not give the article , try again"
            opening = 0L
        }
    }
    // the system back key closes an open article before it leaves the page
    BackHandler(enabled = article != null) { article = null }

    // ONE ARTICLE
    article?.let { a ->
        Column(Modifier.fillMaxSize().padding(horizontal = 20.dp).verticalScroll(rememberScrollState())) {
            Spacer(Modifier.height(12.dp))
            Text("‹ results", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable { article = null }.padding(vertical = 4.dp))
            Spacer(Modifier.height(8.dp))
            Text(a.title, color = GhostText, style = MaterialTheme.typography.titleLarge)
            Text(state?.edition ?: "Wikipedia", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
            if (a.disamb) Text("a page of meanings: the ones below are different things", color = Warning, style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(12.dp))
            Text(a.lead, color = GhostText, style = MaterialTheme.typography.bodyMedium)
            WikiText.parts(a.body).forEach { p ->
                Spacer(Modifier.height(16.dp))
                if (p.heading.isNotBlank()) {
                    Text(p.heading, color = TerminalGreen, style = MaterialTheme.typography.titleSmall)
                    Spacer(Modifier.height(4.dp))
                }
                Text(p.text, color = GhostTextDim, style = MaterialTheme.typography.bodySmall)
            }
            Spacer(Modifier.height(24.dp))
        }
        return
    }

    Column(Modifier.fillMaxSize().padding(horizontal = 20.dp).verticalScroll(rememberScrollState())) {
        Spacer(Modifier.height(12.dp))
        Row(verticalAlignment = Alignment.CenterVertically) { SectionLabel("WIKIPEDIA"); InfoButton("wikipedia") }
        Spacer(Modifier.height(4.dp))
        val st = state
        Text(if (st == null && stateFailed) "! the box did not answer , is it unlocked?" else if (st == null) "asking the box…" else WikiText.state(st.state, st.edition, st.articles, st.redirects, st.imported, st.entries, st.error, st.leftMinutes, st.readers),
            color = if (st?.state == "failed" || (st == null && stateFailed)) Warning else GhostTextDim, style = MaterialTheme.typography.labelMedium)
        if (note.isNotEmpty()) Text(note, color = Warning, style = MaterialTheme.typography.labelMedium)
        // ready, but something the box keeps trying (the lookup indexes, say): said under the line
        if (st?.state == "ready" && st.error.isNotEmpty()) Text("! " + st.error, color = Warning, style = MaterialTheme.typography.labelSmall)
        if (st != null) WikiText.stats(st.articles, st.redirects, st.bytes, st.startedAt, st.doneAt, st.skipped, st.indexed, st.likeness, st.answers, st.state).forEach {
            Text(it, color = TerminalDim, style = MaterialTheme.typography.labelSmall)
        }
        Spacer(Modifier.height(12.dp))
        BasicTextField(query, { query = it }, singleLine = true,
            textStyle = MaterialTheme.typography.bodyMedium.copy(color = GhostText),
            cursorBrush = SolidColor(TerminalGreen),
            keyboardOptions = KeyboardOptions(imeAction = ImeAction.Search),
            keyboardActions = KeyboardActions(onSearch = { search() }),
            decorationBox = { inner -> Row(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).padding(10.dp),
                verticalAlignment = Alignment.CenterVertically) {
                Box(Modifier.weight(1f)) { if (query.isEmpty()) Text("a title, a place, a thing…", color = TerminalDim, style = MaterialTheme.typography.bodyMedium); inner() }
                Text(if (searching) "…" else "[ find ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { search() }.padding(start = 10.dp))
            } },
            modifier = Modifier.fillMaxWidth())
        Spacer(Modifier.height(12.dp))
        val list = hits
        when {
            searching -> LoadingRow("reading the box's Wikipedia…")
            list == null -> if (st?.state == "ready") Text("the whole English Wikipedia, on your box; type a word", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                else if (st?.state == "importing") Text("type a title; what is in already is found", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
            list.isEmpty() -> Text(if (st?.state == "importing") "nothing by that title among what is in so far" else "nothing by that name, nor close to it, nor in a lead", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
            else -> list.forEach { h ->
                Column(Modifier.fillMaxWidth().padding(vertical = 6.dp).border(1.dp, GhostBorder, RectangleShape).background(VoidLighter)
                    .clickable { open(h.idx) }.padding(12.dp)) {
                    Row(verticalAlignment = Alignment.CenterVertically) {
                        Text(h.title, color = TerminalGreen, style = MaterialTheme.typography.titleSmall, modifier = Modifier.weight(1f))
                        Text(if (opening == h.idx) "…" else WikiText.how(h.how), color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                    }
                    if (h.disamb) Text("a page of meanings", color = Warning, style = MaterialTheme.typography.labelSmall)
                    Spacer(Modifier.height(4.dp))
                    Text(h.lead, color = GhostTextDim, style = MaterialTheme.typography.bodySmall, maxLines = 4)
                }
            }
        }
        Spacer(Modifier.height(24.dp))
    }
}
