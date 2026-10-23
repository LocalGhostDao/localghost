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
import androidx.compose.foundation.text.selection.SelectionContainer
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
import com.localghost.app.sync.ContactsSync
import com.localghost.app.ui.theme.*
import kotlinx.coroutines.launch
import java.time.LocalDate

/**
 * THE CONTACTS , what the phone's address book holds, as the box keeps it: everyone by name
 * (starred first), a search over names, organisations, addresses and numbers, and each person
 * opened to their numbers, addresses and birthday. Read-only: the phone's own contacts app is
 * where someone is added or changed, and the box has it within a few minutes.
 */
@Composable
fun ContactsScreen(backLabel: String = "", onBack: () -> Unit = {}) {
    val ctx = LocalContext.current
    var q by rememberSaveable { mutableStateOf("") }
    var asked by rememberSaveable { mutableStateOf("") }
    var page by remember { mutableStateOf<ContactsText.Page?>(null) }
    var failed by remember { mutableStateOf(false) }
    var open by remember { mutableStateOf("") }
    val today = remember { LocalDate.now() }
    LaunchedEffect(asked) {
        page = null; failed = false
        val p = BoxClient.contacts(ctx, asked)
        failed = p == null
        page = p
    }
    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp)) {
        item {
            Spacer(Modifier.height(12.dp))
            if (backLabel.isNotEmpty()) {
                Text(backLabel, color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { onBack() }.padding(vertical = 4.dp))
                Spacer(Modifier.height(8.dp))
            }
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text("CONTACTS", color = GhostText, style = MaterialTheme.typography.titleLarge)
                InfoButton("contacts")
            }
            Text(ContactsText.synced(page?.meta, System.currentTimeMillis() / 1000), color = TerminalDim, style = MaterialTheme.typography.labelSmall)
            if (!ContactsSync.granted(ctx)) Text("the phone has not allowed reading the contacts , SETTINGS › CONTACTS", color = Warning,
                style = MaterialTheme.typography.labelMedium, modifier = Modifier.padding(top = 4.dp))
            Spacer(Modifier.height(10.dp))
            BasicTextField(q, { q = it; if (it.isBlank() && asked.isNotEmpty()) asked = "" }, singleLine = true,
                textStyle = MaterialTheme.typography.bodySmall.copy(color = GhostText), cursorBrush = SolidColor(TerminalGreen),
                keyboardOptions = KeyboardOptions(imeAction = ImeAction.Search),
                keyboardActions = KeyboardActions(onSearch = { asked = q.trim() }),
                decorationBox = { inner -> Box(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).padding(8.dp)) {
                    if (q.isEmpty()) Text("search: a name, a company, a number, an address", color = TerminalDim, style = MaterialTheme.typography.bodySmall); inner() } },
                modifier = Modifier.fillMaxWidth())
            Spacer(Modifier.height(8.dp))
        }
        val p = page
        when {
            p == null && !failed -> item { LoadingRow() }
            failed -> item { ErrorLine("the box did not answer , is it unlocked?") }
            p != null && p.contacts.isEmpty() -> item {
                Text(if (asked.isNotBlank()) "nobody matches “$asked”" else "no contacts on the box yet",
                    color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            }
            p != null -> {
                var lastLetter = ""
                val rows = ArrayList<ContactRowItem>()
                val starred = p.contacts.filter { it.starred }
                if (starred.isNotEmpty() && asked.isBlank()) {
                    rows.add(ContactRowItem("★ STARRED", null))
                    starred.forEach { rows.add(ContactRowItem("", it)) }
                }
                (if (asked.isBlank()) p.contacts.filter { !it.starred } else p.contacts).forEach { c ->
                    val l = ContactsText.letter(c)
                    if (asked.isBlank() && l != lastLetter) { rows.add(ContactRowItem(l, null)); lastLetter = l }
                    rows.add(ContactRowItem("", c))
                }
                items(rows, key = { r -> r.contact?.let { "c:" + it.lookup + "/" + it.id } ?: ("h:" + r.header) }) { r ->
                    val c = r.contact
                    if (c == null) {
                        Text(r.header, color = TerminalDim, style = MaterialTheme.typography.labelMedium, modifier = Modifier.padding(top = 12.dp, bottom = 4.dp))
                    } else {
                        val k = c.lookup + "/" + c.id
                        ContactRow(c, open == k, today) { open = if (open == k) "" else k }
                    }
                }
                item { Spacer(Modifier.height(24.dp)) }
            }
        }
    }
}

/** One row of the list: a letter (or the starred heading) with no contact, or a contact. */
private class ContactRowItem(val header: String, val contact: ContactsText.Contact?)

@Composable
private fun ContactRow(c: ContactsText.Contact, expanded: Boolean, today: LocalDate, onTap: () -> Unit) {
    Column(Modifier.fillMaxWidth().padding(vertical = 3.dp).border(1.dp, GhostBorder, RectangleShape).background(VoidLighter)
        .clickable { onTap() }.padding(horizontal = 10.dp, vertical = 8.dp)) {
        Text(ContactsText.name(c) + if (c.nickname.isNotBlank() && !c.nickname.equals(c.name, true)) "  (" + c.nickname + ")" else "",
            color = GhostText, style = MaterialTheme.typography.bodyMedium)
        val second = ContactsText.second(c)
        if (second.isNotEmpty() && !expanded) Text(second, color = GhostTextDim, style = MaterialTheme.typography.labelSmall, maxLines = 1)
        if (expanded) SelectionContainer {
            Column(Modifier.padding(top = 6.dp)) {
                listOf(c.title, c.org).filter { it.isNotBlank() }.joinToString(", ").takeIf { it.isNotEmpty() }?.let {
                    Text(it, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                }
                c.numbers.forEach { n ->
                    Row(Modifier.padding(top = 2.dp)) {
                        Text(n.label.ifEmpty { "phone" }, color = TerminalDim, style = MaterialTheme.typography.labelSmall, modifier = Modifier.width(80.dp))
                        Text(n.number, color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
                    }
                }
                c.emails.forEach { e ->
                    Row(Modifier.padding(top = 2.dp)) {
                        Text(e.label.ifEmpty { "email" }, color = TerminalDim, style = MaterialTheme.typography.labelSmall, modifier = Modifier.width(80.dp))
                        Text(e.address, color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
                    }
                }
                ContactsText.birthdayLine(c.birthday, today).takeIf { it.isNotEmpty() }?.let { b ->
                    Row(Modifier.padding(top = 2.dp)) {
                        Text("birthday", color = TerminalDim, style = MaterialTheme.typography.labelSmall, modifier = Modifier.width(80.dp))
                        Text(b, color = GhostText, style = MaterialTheme.typography.labelMedium)
                    }
                }
            }
        }
    }
}

/** SETTINGS › CONTACTS: the permission, the switch, the last sync, a sync now, and forgetting
 *  what the box holds. */
@Composable
internal fun ContactsSection(onLine: (String) -> Unit) {
    val ctx = LocalContext.current
    val scope = rememberCoroutineScope()
    var tick by remember { mutableIntStateOf(0) }
    var granted by remember { mutableStateOf(ContactsSync.granted(ctx)) }
    var on by remember { mutableStateOf(ContactsSync.enabled(ctx)) }
    var meta by remember { mutableStateOf<ContactsText.Meta?>(null) }
    var msg by remember { mutableStateOf("") }
    var busy by remember { mutableStateOf(false) }
    var armForget by remember { mutableStateOf(false) }
    val ask = androidx.activity.compose.rememberLauncherForActivityResult(
        androidx.activity.result.contract.ActivityResultContracts.RequestPermission()) { ok ->
        granted = ok
        if (ok) {
            ContactsSync.schedule(ctx)
            busy = true; msg = "reading the contacts…"
            scope.launch {
                val r = ContactsSync.sync(ctx, force = true)
                msg = if (r.error.isNotEmpty()) "! " + r.error else "${r.contacts} contacts on your box"
                busy = false; tick++
            }
        } else msg = "not allowed , the contacts stay on the phone (your call); the app's settings can allow it later"
    }
    LaunchedEffect(tick) { meta = BoxClient.contacts(ctx, limit = 1)?.meta }
    LaunchedEffect(granted, on, meta) {
        val m = meta
        onLine(when {
            !granted -> "not allowed yet"
            !on -> "off"
            m != null -> "${m.contacts} contacts on the box"
            else -> "names for numbers, read here, kept on the box"
        })
    }
    Text("Everyone in this phone's contacts, with their numbers, addresses, organisation and birthday (not the photos, not the notes), " +
        "sent to your box when they change. The chat uses them for who someone is and how to reach them, the day's story for birthdays. " +
        "Nothing is written to the address book, and no contacts service is asked anything.", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
    Spacer(Modifier.height(8.dp))
    if (!granted) {
        GhostButton("ALLOW THE CONTACTS", onClick = { ask.launch(ContactsSync.PERMISSION) }, modifier = Modifier.fillMaxWidth())
    } else {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(if (on) "sending to the box" else "off , nothing is sent", color = if (on) GhostText else Warning,
                style = MaterialTheme.typography.bodyMedium, modifier = Modifier.weight(1f))
            Text(if (on) "[ turn off ]" else "[ turn on ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable { on = !on; ContactsSync.setEnabled(ctx, on) }.padding(vertical = 6.dp))
        }
        Text(ContactsText.synced(meta, System.currentTimeMillis() / 1000), color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        ContactsSync.lastRun(ctx)?.takeIf { it.error.isNotEmpty() }?.let {
            Text("last try: " + it.error, color = Warning, style = MaterialTheme.typography.labelMedium)
        }
        if (on) Text(if (busy) "[ reading the contacts… ]" else "[ sync now ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.clickable {
                if (!busy) {
                    busy = true; msg = "reading the contacts…"
                    scope.launch {
                        val r = ContactsSync.sync(ctx, force = true)
                        msg = when {
                            r.error.isNotEmpty() -> "! " + r.error
                            r.removed > 0 -> "${r.contacts} contacts on your box, ${r.removed} gone from the phone taken off"
                            else -> "${r.contacts} contacts on your box"
                        }
                        busy = false; tick++
                    }
                }
            }.padding(vertical = 6.dp))
    }
    Text(if (armForget) "[ tap again: forget every contact on the box ]" else "[ forget the contacts on the box ]",
        color = if (armForget) Warning else TerminalDim, style = MaterialTheme.typography.labelMedium,
        modifier = Modifier.clickable {
            if (!armForget) { armForget = true; return@clickable }
            armForget = false
            scope.launch {
                msg = if (BoxClient.contactsForget(ctx)) "the box holds no contacts now" + (if (on && granted) " , turn sending off too, or they come back at the next change" else "")
                    else "! the box did not answer"
                tick++
            }
        }.padding(vertical = 6.dp))
    if (msg.isNotEmpty()) Text(msg, color = if (msg.startsWith("!")) Warning else GhostTextDim, style = MaterialTheme.typography.labelMedium)
}
