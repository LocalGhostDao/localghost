package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.setValue
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import com.localghost.app.net.BoxClient
import com.localghost.app.net.PendingNotification
import kotlinx.coroutines.launch
import com.localghost.app.ui.theme.*

/** SessionHint tells the notifications screen whether to warn the user that the box can no longer be
 *  polled. EXPIRED: the session token is dead, the app must be re-unlocked to fetch anything.
 *  EXPIRING_SOON: still valid but close to the 2-day limit, a gentle heads-up. NONE: all good. */
enum class SessionHint { NONE, EXPIRING_SOON, EXPIRED }

@Composable
fun NotificationsScreen(
    @Suppress("UNUSED_PARAMETER") items: Loadable<List<PendingNotification>>,
    sessionHint: SessionHint = SessionHint.NONE,
    onOpen: (NotifLink.Target) -> Unit = {},
    onOpenOne: (Long) -> Unit = {},
) {
    // THE HISTORY, read when the screen opens: what every daemon on the box has said, newest
    // first. Reading it consumes nothing (the push the phone's pollers take is a separate cursor),
    // so what a notification said is here after it was shown, and here when it was never shown
    // (muted, or the phone was off). Tapping one opens its page (NotificationScreen: the whole of
    // it, and the thing it is about shown under it); the green line under it goes straight to the
    // thing (the day, the memory, NEWS, Box Status: NotifLink); ✕ deletes it on the box. A week is kept.
    val ctx = androidx.compose.ui.platform.LocalContext.current
    val scope = androidx.compose.runtime.rememberCoroutineScope()
    var history by androidx.compose.runtime.remember { androidx.compose.runtime.mutableStateOf<Loadable<List<PendingNotification>>>(Loadable.Loading) }
    var armed by androidx.compose.runtime.remember { androidx.compose.runtime.mutableLongStateOf(0L) } // the notification whose ✕ was tapped once
    androidx.compose.runtime.LaunchedEffect(Unit) {
        val h = BoxClient.notificationHistory(ctx)
        history = if (h == null) Loadable.Failed("the box did not answer (locked, or the session expired)") else Loadable.Loaded(h)
    }
    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp)) {
        if (sessionHint != SessionHint.NONE) item {
            Spacer(Modifier.height(12.dp))
            val msg = if (sessionHint == SessionHint.EXPIRED)
                "This session has expired, so the box can no longer check for notifications. " +
                    "Open the app and unlock to refresh."
            else
                "This session will expire soon. Once it does, you will have to open the app and " +
                    "unlock to check if you have notifications , the box cannot notify an expired session."
            Column(Modifier.fillMaxWidth().border(1.dp, TerminalGreen, RectangleShape)
                .background(VoidLighter).padding(14.dp)) {
                Text("SESSION", color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
                Spacer(Modifier.height(4.dp))
                Text(msg, color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
            }
        }
        item {
            Spacer(Modifier.height(12.dp))
            SectionLabel("FROM THE BOX")
            Spacer(Modifier.height(4.dp))
            Text("the last week, newest first · a tap opens it · nothing is pushed through a third party",
                color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(8.dp))
        }
        when (val h = history) {
            is Loadable.Loading -> item { LoadingRow() }
            is Loadable.Failed -> item { ErrorLine(h.reason) }
            is Loadable.Loaded -> if (h.value.isEmpty()) item {
                EmptyLine("nothing yet. The daemons add items here: the evening check-in, a day a year ago, " +
                    "somewhere new near you that fits what you photograph, a service that went down.")
            } else items(h.value, key = { it.id }) { n ->
                Column(Modifier.fillMaxWidth().border(1.dp, if (n.seen) TerminalDim else TerminalGreen, RectangleShape)
                    .background(VoidLighter)
                    .clickable {
                        // its page: the whole of it, and what it is about, shown there
                        if (!n.seen) history = Loadable.Loaded(h.value.map { if (it.id == n.id) it.copy(seen = true) else it })
                        onOpenOne(n.id)
                    }
                    .padding(14.dp)) {
                    Row(verticalAlignment = androidx.compose.ui.Alignment.CenterVertically) {
                        Text(n.daemonId.removePrefix("ghost.") + (if (n.kind.isNotEmpty() && n.kind != "message") " · " + n.kind else ""),
                            color = TerminalGreen, style = MaterialTheme.typography.labelMedium, modifier = Modifier.weight(1f))
                        if (n.created > 0) Text(NotificationTime.ago(System.currentTimeMillis() / 1000 - n.created),
                            color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                        // ✕ arms, a second tap deletes; anything else tapped disarms it
                        val arm = armed == n.id
                        Text(if (arm) "  [ delete? ]" else "  ✕", color = if (arm) Warning else TerminalDim, style = MaterialTheme.typography.labelMedium,
                            modifier = Modifier.clickable {
                                if (!arm) { armed = n.id; return@clickable }
                                armed = 0L
                                scope.launch {
                                    if (BoxClient.notificationDelete(ctx, n.id)) history = Loadable.Loaded(h.value.filter { it.id != n.id })
                                }
                            }.padding(start = 8.dp))
                    }
                    Spacer(Modifier.height(4.dp))
                    Text(n.title, color = if (n.seen) GhostTextDim else GhostText, style = MaterialTheme.typography.titleMedium)
                    Spacer(Modifier.height(2.dp))
                    // a digest in the list: its first story and how many more, the whole on its page
                    Text(NotifPage.preview(n.kind, n.body), color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
                    val target = NotifLink.resolve(n.link, n.daemonId, n.kind)
                    if (target.dest.isNotEmpty() && target.dest != "notifications") {
                        Spacer(Modifier.height(6.dp))
                        // straight to the thing, past its page
                        Text(NotifText.opens(target), color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                            modifier = Modifier.clickable { onOpen(target) })
                    }
                }
            }
        }
        item { Spacer(Modifier.height(24.dp)) }
    }
}

/** "3 min ago", "2 h ago", "4 days ago": pure, for the tests. */
object NotificationTime {
    fun ago(sec: Long): String = when {
        sec < 90 -> "just now"
        sec < 3600 -> "${sec / 60} min ago"
        sec < 2 * 86400 -> "${sec / 3600} h ago"
        else -> "${sec / 86400} days ago"
    }
}
