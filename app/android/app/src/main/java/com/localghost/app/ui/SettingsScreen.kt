package com.localghost.app.ui

import androidx.compose.foundation.border
import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Switch
import androidx.compose.material3.SwitchDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import com.localghost.app.ui.theme.*

@Composable
fun SettingsScreen(
    onOpenVerify: () -> Unit = {},
    onOpenMap: () -> Unit = {},
    allowMobileSync: Boolean,
    onToggleMobileSync: (Boolean) -> Unit,
    thinkLevel: String = "",
    onCycleThink: () -> Unit = {},
    notificationsMuted: Boolean,
    onToggleMute: (Boolean) -> Unit,
    onExport: () -> Unit,
    exportState: String?,
    onLock: () -> Unit,
    onWipe: () -> Unit,
) {
    Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState())
        .padding(20.dp).padding(bottom = 24.dp)) {
        SectionLabel("SETTINGS")
        Spacer(Modifier.height(20.dp))

        SectionLabel("SYNC")
        Spacer(Modifier.height(8.dp))
        toggleRow(
            label = "sync over mobile data",
            sub = if (allowMobileSync) "on, uses Wi-Fi and mobile (4G/5G)"
                  else "off, Wi-Fi only (recommended)",
            checked = allowMobileSync, onChange = onToggleMobileSync,
        )

        Spacer(Modifier.height(24.dp))
        SectionLabel("LOCATION TRAIL")
        Spacer(Modifier.height(8.dp))
        // Read and written right here, like the phrase switches: this is per-phone state, and the
        // shell has no reason to carry it.
        val ctx = androidx.compose.ui.platform.LocalContext.current
        var trailTick by remember { mutableIntStateOf(0) }
        val trailOn = remember(trailTick) { com.localghost.app.settings.AppSettings.locationTrail(ctx) }
        val trailAllowed = remember(trailTick) { com.localghost.app.sync.LocationLog.hasPermission(ctx) }
        val trailBackground = remember(trailTick) { com.localghost.app.sync.LocationLog.hasBackground(ctx) }
        val waiting = remember(trailTick) { com.localghost.app.sync.LocationLog.pendingCount(ctx) }
        val today = remember(trailTick) { com.localghost.app.sync.LocationLog.countToday(ctx) }
        toggleRow(
            label = "keep the trail",
            sub = when {
                !trailOn -> "off, the phone takes no fixes"
                !trailAllowed -> "on, but location is not allowed for LocalGhost , nothing is recorded"
                !trailBackground -> "on while the app is open only ('always' not allowed)"
                waiting > 0 -> "on, a point every quarter hour · $today today · $waiting waiting for the box"
                else -> "on, a point every quarter hour · $today today · all on the box"
            },
            checked = trailOn,
            onChange = { on ->
                com.localghost.app.settings.AppSettings.setLocationTrail(ctx, on)
                if (on) com.localghost.app.sync.LocationLog.schedule(ctx) else com.localghost.app.sync.LocationLog.stop(ctx)
                trailTick++
            },
        )
        // The trail is drawn on the map , by day, with a clock along the line , and the switch
        // that records it lives here; one tap joins the two.
        Text("[ see the trail on the map ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.clickable { onOpenMap() }.padding(vertical = 6.dp))

        Spacer(Modifier.height(24.dp))
        SectionLabel("PHRASES")
        Spacer(Modifier.height(8.dp))
        // Off until the phone lands somewhere that is not home and the person says yes; this is the
        // manual way in (and out), and where home is set , the SIM's country by default.
        var phraseTick by remember { mutableIntStateOf(0) }
        val phrasesOn = remember(phraseTick) { com.localghost.app.phrases.PhraseState.enabled(ctx) }
        val home = remember(phraseTick) { com.localghost.app.phrases.PhraseOffer.homeCountry(ctx) }
        val here = remember(phraseTick) { com.localghost.app.phrases.CountryDetect.detect(ctx) }
        toggleRow(
            label = "phrases on the lock screen",
            sub = if (phrasesOn) "on · the phrase of the hour in the language around you · PHRASES is in the drawer"
                else "off · offered once when you land somewhere that is not home; this switch is the other way in",
            checked = phrasesOn,
            onChange = { on ->
                if (on) com.localghost.app.phrases.PhraseOffer.accept(ctx)
                else {
                    com.localghost.app.phrases.PhraseState.setEnabled(ctx, false)
                    com.localghost.app.phrases.PhraseState.setLockScreenOn(ctx, false)
                    Thread { com.localghost.app.phrases.PhraseSurface.refresh(ctx) }.start()
                }
                phraseTick++
            },
        )
        Spacer(Modifier.height(8.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            Column(Modifier.weight(1f)) {
                Text("home", color = GhostText, style = MaterialTheme.typography.bodyMedium)
                Text((if (home.isEmpty()) "not set , the SIM has no country" else com.localghost.app.phrases.CountryNames.of(home)) +
                    " · the phrases never offer themselves here" +
                    (if (here.country.isNotEmpty() && here.country != home) " · you seem to be in ${com.localghost.app.phrases.CountryNames.of(here.country)} (${here.source})" else ""),
                    color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            }
            if (here.country.isNotEmpty() && here.country != home) {
                Text("[ home is here ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { com.localghost.app.settings.AppSettings.setHomeCountry(ctx, here.country); phraseTick++ }.padding(6.dp))
            }
        }

        Spacer(Modifier.height(24.dp))
        SectionLabel("CHAT")
        Spacer(Modifier.height(8.dp))
        // Deliberation depth for every chat answer. Tapping cycles off -> brief -> deep. Honest
        // mechanics: this asks the model to show its working (and gives it a bigger token budget) ,
        // deeper means slower, especially on CPU.
        Row(Modifier.fillMaxWidth().clickable { onCycleThink() }.padding(vertical = 8.dp)) {
            Column(Modifier.weight(1f)) {
                Text("thinking", color = GhostText, style = MaterialTheme.typography.bodyLarge)
                Text(when (thinkLevel) {
                    "brief" -> "brief , a few lines of reasoning first (slower)"
                    "deep" -> "deep , thorough reasoning first (much slower)"
                    else -> "off , answers directly (fastest)"
                }, color = GhostTextDim, style = MaterialTheme.typography.bodySmall)
            }
            Text(when (thinkLevel) { "brief" -> "[ BRIEF ]"; "deep" -> "[ DEEP ]"; else -> "[ OFF ]" },
                color = TerminalGreen, style = MaterialTheme.typography.bodyMedium)
        }

        Spacer(Modifier.height(16.dp))
        // WEB SEARCH , the engine the PHONE uses when a question goes to the web (the box never
        // does). DuckDuckGo needs nothing and is scraped HTML, which it sometimes answers with a
        // bot check; Brave is a real API with the person's own key and DuckDuckGo behind it.
        // Google offers neither: its results page forbids scripts, and its search API is closed
        // to new customers and ends on 1 January 2027.
        var engine by remember { mutableStateOf(com.localghost.app.settings.AppSettings.searchEngine(ctx)) }
        var braveKey by remember { mutableStateOf(com.localghost.app.settings.AppSettings.braveKey(ctx)) }
        Text("web search engine", color = GhostText, style = MaterialTheme.typography.bodyLarge)
        Text(if (engine == "brave") (if (braveKey.isBlank()) "Brave , paste your API key below; until then DuckDuckGo answers" else "Brave with your key , DuckDuckGo if it fails")
            else "DuckDuckGo , no key, nothing to set up",
            color = GhostTextDim, style = MaterialTheme.typography.bodySmall)
        Spacer(Modifier.height(6.dp))
        Row {
            for ((id, label) in listOf("duckduckgo" to "DuckDuckGo", "brave" to "Brave (your key)")) {
                val on = engine == id
                Text(label, color = if (on) Void else TerminalGreen, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.padding(end = 8.dp)
                        .border(1.dp, TerminalGreen, androidx.compose.ui.graphics.RectangleShape)
                        .background(if (on) TerminalGreen else Void)
                        .clickable { engine = id; com.localghost.app.settings.AppSettings.setSearchEngine(ctx, id) }
                        .padding(horizontal = 10.dp, vertical = 6.dp))
            }
        }
        if (engine == "brave") {
            Spacer(Modifier.height(8.dp))
            androidx.compose.foundation.text.BasicTextField(braveKey, {
                braveKey = it.trim(); com.localghost.app.settings.AppSettings.setBraveKey(ctx, braveKey)
            }, singleLine = true,
                textStyle = MaterialTheme.typography.bodySmall.copy(color = GhostText),
                cursorBrush = androidx.compose.ui.graphics.SolidColor(TerminalGreen),
                visualTransformation = if (braveKey.isEmpty()) androidx.compose.ui.text.input.VisualTransformation.None
                    else androidx.compose.ui.text.input.PasswordVisualTransformation(),
                decorationBox = { inner -> Box(Modifier.fillMaxWidth()
                    .border(1.dp, GhostBorder, androidx.compose.ui.graphics.RectangleShape).padding(8.dp)) {
                    if (braveKey.isEmpty()) Text("Brave Search API key (api-dashboard.search.brave.com)", color = TerminalDim,
                        style = MaterialTheme.typography.bodySmall); inner() } },
                modifier = Modifier.fillMaxWidth())
            Text("kept on this phone only · the box never sees it · Brave charges per 1,000 searches after a monthly free credit",
                color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        }

        Spacer(Modifier.height(24.dp))
        SectionLabel("NOTIFICATIONS")
        Spacer(Modifier.height(8.dp))
        toggleRow(
            label = "daemon notifications",
            sub = if (notificationsMuted) "muted, daemons stay silent"
                  else "active, daemons can notify you",
            checked = !notificationsMuted, onChange = { on -> onToggleMute(!on) },
        )

        Spacer(Modifier.height(24.dp))
        SectionLabel("YOUR DATA")
        Spacer(Modifier.height(8.dp))
        Text("The box holds the index. The phone holds nothing. These act on the box.",
            color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        Spacer(Modifier.height(12.dp))

        GhostButton("EXPORT TO JSON", onExport, modifier = Modifier.fillMaxWidth())
        exportState?.let {
            Spacer(Modifier.height(8.dp))
            Text("> $it", color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
        }

        Spacer(Modifier.height(12.dp))
        Text("PIN changes happen at the box, not in the app. Run `ghost.secd changepin-<slot>` " +
             "(keeps your data) or `ghost.secd resetup-<slot>` (wipes and starts fresh) over a " +
             "local-network SSH session. A coerced phone cannot change or reset a PIN.",
             color = GhostTextDim, style = MaterialTheme.typography.labelMedium)

        Spacer(Modifier.height(24.dp))
        SectionLabel("DEVELOPER")
        run {
            val dctx = androidx.compose.ui.platform.LocalContext.current
            var dbg by remember { mutableStateOf(com.localghost.app.settings.AppSettings.debugMode(dctx)) }
            Text(if (dbg) "[x] app is in DEBUG MODE , tap to disable" else "[ ] set app in debug mode",
                color = if (dbg) TerminalGreen else GhostTextDim,
                style = MaterialTheme.typography.bodyMedium,
                modifier = Modifier.clickable {
                    dbg = !dbg
                    com.localghost.app.settings.AppSettings.setDebugMode(dctx, dbg)
                }.padding(vertical = 6.dp))
            Text("> shows the tok/s meter under chat replies; more diagnostics will attach here",
                color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        }
        Spacer(Modifier.height(20.dp))
        SectionLabel("SESSION")
        Spacer(Modifier.height(8.dp))
        GhostButton("LOCK BOX NOW", onLock, modifier = Modifier.fillMaxWidth())
        Spacer(Modifier.height(4.dp))
        Text("Spins the box down: stops the databases, unmounts the drive, and drops the key from " +
             "memory. The box goes dark until you enter your PIN again. Your data is untouched.",
             color = GhostTextDim, style = MaterialTheme.typography.labelMedium)

        Spacer(Modifier.height(20.dp))
        SectionLabel("TRUST")
        Spacer(Modifier.height(8.dp))
        GhostButton("VERIFY BUILD ✓", onOpenVerify, modifier = Modifier.fillMaxWidth())
        Spacer(Modifier.height(4.dp))
        Text("Checks that what the box is running matches the public source. An audit action, not " +
             "a daily one , which is why it lives here instead of taking a menu slot.",
             color = GhostTextDim, style = MaterialTheme.typography.labelMedium)

        Spacer(Modifier.height(20.dp))
        SectionLabel("DESTRUCTIVE")
        Spacer(Modifier.height(8.dp))
        WipeButton(onWipe)
        Spacer(Modifier.height(4.dp))
        Text("Crypto-erase, global. The master key is destroyed on the box and every " +
             "persona's data becomes noise at once. Nobody reverses this, including you.",
             color = Warning, style = MaterialTheme.typography.labelMedium)

        Spacer(Modifier.height(28.dp))
        Text("> the only cloud is you", color = GhostTextDim,
            style = MaterialTheme.typography.labelMedium)
    }
}

@Composable
private fun toggleRow(label: String, sub: String, checked: Boolean, onChange: (Boolean) -> Unit) {
    Row(Modifier.fillMaxWidth().padding(vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(label, color = GhostText, style = MaterialTheme.typography.bodyMedium)
            Text(sub, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        }
        Switch(
            checked = checked, onCheckedChange = onChange,
            colors = SwitchDefaults.colors(
                checkedThumbColor = Void, checkedTrackColor = TerminalGreen,
                uncheckedThumbColor = GhostTextDim, uncheckedTrackColor = VoidLighter,
                uncheckedBorderColor = GhostBorder,
            ),
        )
    }
}

@Composable
private fun WipeButton(onWipe: () -> Unit) {
    var confirming by remember { mutableStateOf(false) }
    androidx.compose.material3.OutlinedButton(
        onClick = { confirming = true },
        shape = androidx.compose.ui.graphics.RectangleShape,
        border = androidx.compose.foundation.BorderStroke(1.dp, Warning),
        colors = androidx.compose.material3.ButtonDefaults.outlinedButtonColors(contentColor = Warning),
        modifier = Modifier.fillMaxWidth(),
    ) { Text("[ WIPE EVERYTHING ]", style = MaterialTheme.typography.labelLarge) }

    if (confirming) {
        ConfirmDialog(
            title = "WIPE THIS PHONE",
            body = "This destroys the box connection, the device certificate, and the identity key on " +
                "this phone. The box keeps your data. To use this phone again you re-pair it, which " +
                "needs to be done at home with your security key. There is no undo from here.",
            requireWord = "WIPE",
            confirmLabel = "WIPE EVERYTHING",
            onConfirm = { confirming = false; onWipe() },
            onDismiss = { confirming = false },
        )
    }
}
