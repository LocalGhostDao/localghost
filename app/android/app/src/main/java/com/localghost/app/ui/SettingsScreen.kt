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
import kotlinx.coroutines.launch

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
    // ONE SCREEN, IN THE ORDER THINGS MATTER: the box first (what it runs, updates, lock), then
    // what the phone sends it (photos, the trail, health), then what the phone keeps for itself
    // (maps), then how the box answers (chat), then the rest. Each part folds; the closed line
    // says its state, so the screen reads at a glance and opens only where you are going.
    val ctx = androidx.compose.ui.platform.LocalContext.current
    val scope = rememberCoroutineScope()
    var askLock by remember { mutableStateOf(false) } // LOCK BOX NOW asks once: it is one tap from dark
    if (askLock) AskDialog(
        title = "LOCK THE BOX",
        body = "The box stops its databases, unmounts the drive and drops the key from memory. It goes dark until you enter your PIN again; your data is untouched.",
        confirmLabel = "LOCK",
        onConfirm = { askLock = false; onLock() },
        onDismiss = { askLock = false },
    )
    Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState())
        .padding(20.dp).padding(bottom = 24.dp)) {
        SectionLabel("SETTINGS")
        Spacer(Modifier.height(12.dp))

        Fold("YOUR BOX", "the build it runs, updates, lock, verify", openAtFirst = true) {
            ServerUpdateSection(onLock)
            Spacer(Modifier.height(16.dp))
            Spacer(Modifier.height(8.dp))
            GhostButton("LOCK BOX NOW", { askLock = true }, modifier = Modifier.fillMaxWidth())
            Spacer(Modifier.height(4.dp))
            Text("Spins the box down: stops the databases, unmounts the drive, and drops the key from " +
                 "memory. The box goes dark until you enter your PIN again. Your data is untouched.",
                 color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(16.dp))
            Spacer(Modifier.height(8.dp))
            // THIS PHONE'S KEY: made when, good until when, renewed at each unlock once a day old
            com.localghost.app.net.DeviceCert.dates(ctx)?.let { d ->
                Spacer(Modifier.height(8.dp))
                Text(CertText.line(d.notBefore, d.notAfter, System.currentTimeMillis()), color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                Spacer(Modifier.height(16.dp))
            }
            GhostButton("VERIFY THIS APP ✓", onOpenVerify, modifier = Modifier.fillMaxWidth())
            Spacer(Modifier.height(4.dp))
            Text("Shows this app's commit, its signing certificate and the source manifest, to check " +
                 "against the public repository. The box's own build is shown above, under the release " +
                 "it runs. An audit action, not a daily one , which is why it lives here.",
                 color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        }

        Fold("PHOTOS AND FILES", if (allowMobileSync) "sync on Wi-Fi and mobile data" else "sync on Wi-Fi only", openAtFirst = false) {
            toggleRow(
                label = "sync over mobile data",
                sub = if (allowMobileSync) "on, uses Wi-Fi and mobile (4G/5G)"
                      else "off, Wi-Fi only (recommended)",
                checked = allowMobileSync, onChange = onToggleMobileSync,
            )
        }

        Fold("LOCATION TRAIL", "where you have been, a fix every quarter hour", openAtFirst = true) {
            Spacer(Modifier.height(8.dp))
            // Read and written right here, like the phrase switches: this is per-phone state, and the
            // shell has no reason to carry it.
            var trailTick by remember { mutableIntStateOf(0) }
            val trailOn = remember(trailTick) { com.localghost.app.settings.AppSettings.locationTrail(ctx) }
            val trailAllowed = remember(trailTick) { com.localghost.app.sync.LocationLog.hasPermission(ctx) }
            val trailBackground = remember(trailTick) { com.localghost.app.sync.LocationLog.hasBackground(ctx) }
            val waiting = remember(trailTick) { com.localghost.app.sync.LocationLog.pendingCount(ctx) }
            val today = remember(trailTick) { com.localghost.app.sync.LocationLog.countToday(ctx) }
            val passive = remember(trailTick) { com.localghost.app.sync.LocationLog.passiveToday(ctx) }
            val sealedTo = remember(trailTick) { com.localghost.app.sync.TrailKeys.where(ctx) }
            // the counts are the lines under the switch (TrailStatus); the switch says how it is kept
            @Suppress("UNUSED_VARIABLE") val passiveSeen = passive
            val sealedLine = when (sealedTo) {
                "box" -> " · sealed on this phone, opened only by your box PIN"
                "phone" -> " · sealed on this phone, opened by the phone's own unlock"
                else -> ""
            }
            toggleRow(
                label = "keep the trail",
                sub = when {
                    !trailOn -> "off, the phone takes no fixes"
                    !trailAllowed -> "on, but location is not allowed for LocalGhost , nothing is recorded"
                    !trailBackground -> "on while the app is open only ('always' not allowed)"
                    waiting > 0 -> "on, a point every quarter hour plus other apps' fixes$sealedLine · $waiting waiting for the box"
                    else -> "on, a point every quarter hour plus other apps' fixes$sealedLine · all on the box"
                },
                checked = trailOn,
                onChange = { on ->
                    com.localghost.app.settings.AppSettings.setLocationTrail(ctx, on)
                    if (on) com.localghost.app.sync.LocationLog.schedule(ctx) else com.localghost.app.sync.LocationLog.stop(ctx)
                    trailTick++
                },
            )
            // WHERE THE TRAIL IS: the phone's newest fix it can read, and how the last hand-over to
            // the box went. "It says no position" and "is it reaching the box" are both answered here.
            if (trailOn) {
                val newest = remember(trailTick) { com.localghost.app.sync.LocationLog.newest(ctx) }
                val send = remember(trailTick) { com.localghost.app.sync.LocationLog.lastSend(ctx) }
                val now = System.currentTimeMillis() / 1000
                val lastState = remember(trailTick) { com.localghost.app.sync.LocationLog.lastState(ctx) }
                Text(TrailStatus.fixLine(newest?.ts, now, lastState), color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                val byVia = remember(trailTick) { com.localghost.app.sync.LocationLog.todayByVia(ctx) }
                val sentToday = remember(trailTick) { com.localghost.app.sync.LocationLog.sentToday(ctx) }
                val sentTotal = remember(trailTick) { com.localghost.app.sync.LocationLog.sentTotal(ctx) }
                val ring = remember(trailTick) {
                    if (com.localghost.app.sync.TrailKeys.opener() != null) com.localghost.app.sync.LocationLog.recent(ctx).size else null
                }
                Text(TrailStatus.todayLine(today, byVia, sentToday, waiting), color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                Text(TrailStatus.holdsLine(ring, sentTotal), color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                Text(TrailStatus.sendLine(send?.at, send?.what, send?.ok, send?.lastOkAt, waiting, now),
                    color = if (send != null && !send.ok && waiting > 0) Warning else GhostTextDim, style = MaterialTheme.typography.labelMedium)
                Text("[ send the trail to the box now ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable {
                        scope.launch {
                            kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) { com.localghost.app.sync.LocationLog.flush(ctx) }
                            trailTick++
                        }
                    }.padding(vertical = 6.dp))
            }
            // The trail is drawn on the map , by day, with a clock along the line , and the switch
            // that records it lives here; one tap joins the two.
            Text("[ see the trail on the map ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable { onOpenMap() }.padding(vertical = 6.dp))
        }

        Fold("HEALTH", "steps, sleep, heart rate from Health Connect, to the box", openAtFirst = true) {
            HealthSection()
        }

        // The map fetches tiles from the box as you look; ticked, the phone keeps them ahead of
        // time (Wi-Fi only, once a day), streets around where you have been first, and whole
        // countries when picked.
        var mapTick by remember { mutableIntStateOf(0) }
        val mapsOn = remember(mapTick) { com.localghost.app.settings.AppSettings.mapDownload(ctx) }
        val mapCountries = remember(mapTick) { com.localghost.app.local.MapPrefetch.countryProgress(ctx).map { it.name } }
        val mapPickedN = remember(mapTick) { com.localghost.app.settings.AppSettings.mapCountries(ctx).size }
        Fold("MAPS ON THIS PHONE",
            (if (mapsOn) "downloading ahead of time" else "off") + " · " +
                (if (mapPickedN > 0 && mapCountries.isEmpty()) "$mapPickedN picked" else MapCountryText.picked(mapCountries)),
            openAtFirst = false) {
            Spacer(Modifier.height(8.dp))
            val mapBudget = remember(mapTick) { com.localghost.app.settings.AppSettings.mapBudgetMB(ctx) }
            // the status line follows a run as it goes (the worker notes its progress every ten tiles)
            var mapStatus by remember { mutableStateOf("") }
            LaunchedEffect(mapTick) {
                while (true) {
                    mapStatus = kotlinx.coroutines.withContext(kotlinx.coroutines.Dispatchers.IO) {
                        com.localghost.app.local.MapPrefetch.statusLine(ctx)
                    }
                    kotlinx.coroutines.delay(3_000)
                }
            }
            toggleRow(
                label = "download maps",
                sub = if (mapsOn) "on Wi-Fi, once a day: streets around where you have been, then the coast and main roads outwards · $mapStatus"
                    else "off , tiles come from the box as you look (slow the first time anywhere) · $mapStatus",
                checked = mapsOn,
                onChange = { on ->
                    com.localghost.app.settings.AppSettings.setMapDownload(ctx, on)
                    if (on) { com.localghost.app.local.MapPrefetch.schedule(ctx); com.localghost.app.local.MapPrefetch.runNow(ctx) }
                    else com.localghost.app.local.MapPrefetch.cancel(ctx)
                    mapTick++
                },
            )
            if (mapsOn) {
                Row(verticalAlignment = Alignment.CenterVertically, modifier = Modifier.padding(vertical = 4.dp)) {
                    Text("keep up to", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                    listOf(250, 500, 1000, 2000).forEach { mb ->
                        Text(if (mb >= 1000) "${mb / 1000} GB" else "$mb MB",
                            color = if (mb == mapBudget) TerminalGreen else GhostTextDim,
                            style = MaterialTheme.typography.labelMedium,
                            modifier = Modifier.clickable {
                                com.localghost.app.settings.AppSettings.setMapBudgetMB(ctx, mb); mapTick++
                            }.padding(horizontal = 8.dp, vertical = 6.dp))
                    }
                }
                MapCountriesSection(mapTick, onChanged = { mapTick++ })
                Text("[ download now ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { com.localghost.app.local.MapPrefetch.runNow(ctx); mapTick++ }.padding(vertical = 6.dp))
            }
        }

        // THE NEWS AND THE RATES the phone fetches for the box: the box opens no connection, so the
        // feeds and the tickers are fetched here and handed over; the box keeps, groups and
        // summarises them. The honest cost is stated: publishers and exchanges see this phone's
        // address, and the box gets nothing while the phone is off.
        var fetchTick by remember { mutableIntStateOf(0) }
        val fetchOn = remember(fetchTick) { com.localghost.app.settings.AppSettings.boxFetch(ctx) }
        val fetchLast = remember(fetchTick) { com.localghost.app.sync.BoxFetch.last(ctx) }
        Fold("NEWS AND RATES", if (fetchOn) "fetched by this phone for the box · " + (fetchLast?.let { NewsText.ago(it.at, System.currentTimeMillis() / 1000) } ?: "not yet") else "off", openAtFirst = false) {
            Spacer(Modifier.height(8.dp))
            toggleRow(
                label = "fetch the news and the rates",
                sub = if (fetchOn) "hourly on Wi-Fi this phone fetches the box's list of feeds and the exchange tickers for it; on mobile data, or with the phone away, the box fetches for itself"
                      else "off , the box fetches for itself whatever network this phone is on",
                checked = fetchOn,
                onChange = { on ->
                    com.localghost.app.settings.AppSettings.setBoxFetch(ctx, on)
                    com.localghost.app.sync.BoxFetch.schedule(ctx)
                    if (on) com.localghost.app.sync.BoxFetch.runNow(ctx)
                    fetchTick++
                },
            )
            if (fetchOn) {
                val now = System.currentTimeMillis() / 1000
                Text(when {
                    fetchLast == null -> "no run yet"
                    fetchLast.note.isNotEmpty() -> "last run ${NewsText.ago(fetchLast.at, now)}: ${fetchLast.note}"
                    else -> "last run ${NewsText.ago(fetchLast.at, now)}: ${fetchLast.feedsOK} of ${fetchLast.feeds} feeds, ${fetchLast.ratesOK} of ${fetchLast.rates} tickers answered"
                }, color = if (fetchLast?.note?.isNotEmpty() == true) Warning else GhostTextDim, style = MaterialTheme.typography.labelMedium)
                Text("[ fetch now ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable { com.localghost.app.sync.BoxFetch.runNow(ctx); fetchTick++ }.padding(vertical = 6.dp))
                Text("> the box pulls general information in to use in context, never anything of yours out: the publishers and the exchanges see this phone's address when it fetches and the box's when the box does, and that is all they get. Digests at 07:00 and 19:00 in your zone. The list of feeds is kept on the box and edited under INTEGRATIONS › News › feeds.",
                    color = TerminalDim, style = MaterialTheme.typography.labelMedium)
            }
        }

        Fold("CHAT", "thinking " + thinkLevel.ifEmpty { "off" } + " · web search", openAtFirst = false) {
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
        }

        Fold("NOTIFICATIONS", if (notificationsMuted) "muted on this phone" else "on", openAtFirst = false) {
            Spacer(Modifier.height(8.dp))
            toggleRow(
                label = "daemon notifications",
                sub = if (notificationsMuted) "muted, daemons stay silent"
                      else "active, daemons can notify you",
                checked = !notificationsMuted, onChange = { on -> onToggleMute(!on) },
            )
        }

        Fold("PHRASES", "the language around you, on the lock screen", openAtFirst = false) {
            Spacer(Modifier.height(8.dp))
            // On from the start: at home the news and the prices, away the phrases. This is the way
            // out (and back in), and where home is set , the SIM's country by default.
            var phraseTick by remember { mutableIntStateOf(0) }
            val phrasesOn = remember(phraseTick) { com.localghost.app.phrases.PhraseState.enabled(ctx) }
            val home = remember(phraseTick) { com.localghost.app.phrases.PhraseOffer.homeCountry(ctx) }
            val here = remember(phraseTick) { com.localghost.app.phrases.CountryDetect.detect(ctx) }
            toggleRow(
                label = "the lock-screen card",
                sub = if (phrasesOn) "on · at home, the news your box picked and BTC and ETH; away, the phrase of the hour in the language around you"
                    else "off · no card on the lock screen",
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
        }

        Fold("YOUR DATA", "where it lives, backups, codes", openAtFirst = false) {
            Spacer(Modifier.height(8.dp))
            Text("The box holds the index. The phone holds nothing.",
                color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(12.dp))
            // honest: the export is not built on the box yet (it used to hand over a made-up
            // file); the button says so instead of pretending
            @Suppress("UNUSED_EXPRESSION") onExport
            @Suppress("UNUSED_EXPRESSION") exportState
            Text("There is no export from the app. Your originals sit on the box's encrypted volume as ordinary files, readable with your PIN; the box makes nightly backups sealed to a key you place on it (Sunday full, the other nights what changed), and ghost.restore reads them back. Both are the operator's, at the box.",
                color = TerminalDim, style = MaterialTheme.typography.labelMedium)

            Spacer(Modifier.height(12.dp))
            Text("PIN changes happen at the box, not in the app. Run `ghost.secd changepin-<slot>` " +
                 "(keeps your data) or `ghost.secd resetup-<slot>` (wipes and starts fresh) over a " +
                 "local-network SSH session. A coerced phone cannot change or reset a PIN.",
                 color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        }

        var crt by remember { mutableStateOf(com.localghost.app.settings.AppSettings.crt(ctx)) }
        Fold("SCREEN", if (crt) "the odd flicker" else "no flicker", openAtFirst = false) {
            toggleRow(
                label = "the odd flicker",
                sub = if (crt) "on: now and then, on a page change, the screen shows its glass for a moment (a wash of scanlines, a sweep, a heading typing itself in), then is plain again"
                      else "off: never",
                checked = crt, onChange = { on -> crt = on; com.localghost.app.settings.AppSettings.setCrt(ctx, on) },
            )
            Text("> drawing only, nothing of yours; the unlock's rings and the scanner's aperture stay either way",
                color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        }

        Fold("DEVELOPER", "debug mode", openAtFirst = false) {
            var dbg by remember { mutableStateOf(com.localghost.app.settings.AppSettings.debugMode(ctx)) }
            Text(if (dbg) "[x] app is in DEBUG MODE , tap to disable" else "[ ] set app in debug mode",
                color = if (dbg) TerminalGreen else GhostTextDim,
                style = MaterialTheme.typography.bodyMedium,
                modifier = Modifier.clickable {
                    dbg = !dbg
                    com.localghost.app.settings.AppSettings.setDebugMode(ctx, dbg)
                }.padding(vertical = 6.dp))
            Text("> shows the tok/s meter under chat replies, and the map's graticule",
                color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        }

        Fold("DANGER", "wipe this phone", openAtFirst = false) {
            Spacer(Modifier.height(8.dp))
            WipeButton(onWipe)
            Spacer(Modifier.height(4.dp))
            Text("Forgets the box on THIS PHONE: the enrolment, the certificate, the session. The box " +
                 "and everything on it are untouched; a crypto-erase of the box is done at the box " +
                 "(ghost.secd resetup), never from a phone.",
                 color = Warning, style = MaterialTheme.typography.labelMedium)
        }

        Spacer(Modifier.height(28.dp))
        Text("> the only cloud is you", color = GhostTextDim,
            style = MaterialTheme.typography.labelMedium)
    }
}

/** A part of the screen that folds: the label, a one-line state when closed, the body when open. */
@Composable
private fun Fold(label: String, closedLine: String, openAtFirst: Boolean, body: @Composable () -> Unit) {
    var open by remember { mutableStateOf(openAtFirst) }
    Spacer(Modifier.height(6.dp))
    Row(Modifier.fillMaxWidth().clickable { open = !open }.padding(vertical = 8.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(if (open) "▾" else "▸", color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
        Spacer(Modifier.width(8.dp))
        Column(Modifier.weight(1f)) {
            Text(label, color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
            if (!open) Text(closedLine, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        }
    }
    if (open) {
        Column(Modifier.fillMaxWidth().padding(start = 4.dp)) { body() }
    }
    Spacer(Modifier.height(6.dp))
}

/**
 * HEALTH: Health Connect (where Samsung Health and the watch write) to the box, every six hours
 * by itself and here on request. The permission state, how the last hand-over went, and the probe
 * that says what Health Connect holds and which app put it there.
 */
@Composable
private fun HealthSection() {
    val hctx = androidx.compose.ui.platform.LocalContext.current
    val scope = androidx.compose.runtime.rememberCoroutineScope()
    var tick by remember { mutableIntStateOf(0) }
    var healthMsg by remember { mutableStateOf("") }
    var grantedCount by remember { mutableStateOf(0) }
    val total = com.localghost.app.sync.HealthSync.PERMISSIONS.size
    val available = remember { com.localghost.app.sync.HealthSync.available(hctx) }
    LaunchedEffect(tick) {
        if (available) grantedCount = com.localghost.app.sync.HealthSync.grantedCount(hctx)
    }
    val granted = grantedCount == total
    val permLauncher = androidx.activity.compose.rememberLauncherForActivityResult(
        androidx.health.connect.client.PermissionController.createRequestPermissionResultContract()) { g ->
        grantedCount = com.localghost.app.sync.HealthSync.PERMISSIONS.count { it in g }
        healthMsg = when {
            grantedCount == total -> "all health permissions granted , the last week ships now"
            grantedCount > 0 -> "$grantedCount of $total granted , what is granted ships, the rest is named as skipped"
            else -> "no permissions granted , health stays off (your call)"
        }
        if (grantedCount > 0) scope.launch { com.localghost.app.sync.HealthSync.sync(hctx); tick++ }
    }
    if (!available) {
        Text("Health Connect is not on this phone , nothing to read", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        return
    }
    val run = remember(tick) { com.localghost.app.sync.HealthSync.lastRun(hctx) }
    Text(when { granted -> "allowed: all $total kinds"; grantedCount > 0 -> "allowed: $grantedCount of $total kinds"; else -> "not allowed yet" },
        color = if (granted) GhostText else Warning, style = MaterialTheme.typography.bodyMedium)
    Text(HealthStatus.line(null, run?.at ?: 0L, run?.days ?: 0, run?.newestDay ?: "", run?.error ?: "", run?.skipped ?: "", System.currentTimeMillis() / 1000)
        .removePrefix("the box holds no day yet · "),
        color = if (run != null && run.error.isNotEmpty()) Warning else GhostTextDim, style = MaterialTheme.typography.labelMedium)
    Spacer(Modifier.height(8.dp))
    if (!granted) {
        GhostButton(if (grantedCount > 0) "ALLOW THE REST" else "ALLOW HEALTH CONNECT", onClick = {
            healthMsg = "opening the Health Connect permission sheet…"
            try { permLauncher.launch(com.localghost.app.sync.HealthSync.PERMISSIONS) }
            catch (e: Exception) { healthMsg = "! permission sheet refused to open: ${e.message ?: "no reason given"}" }
        }, modifier = Modifier.fillMaxWidth())
        Spacer(Modifier.height(8.dp))
    }
    if (grantedCount > 0) {
        Text("[ send the last week now ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.clickable {
                scope.launch {
                    healthMsg = "reading Health Connect…"
                    val res = com.localghost.app.sync.HealthSync.sync(hctx)
                    val skip = if (res.skipped.isEmpty()) "" else " (skipped: ${res.skipped.joinToString(", ")})"
                    healthMsg = when {
                        res.error != null -> "! ${res.error}$skip"
                        res.days > 0 -> "shipped ${res.days} day(s) to your box$skip"
                        else -> "no health data found for the last 7 days$skip , tap [ what is in Health Connect? ]"
                    }
                    tick++
                }
            }.padding(vertical = 6.dp))
        Text("[ send the whole history ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.clickable {
                scope.launch {
                    healthMsg = "walking your history month by month…"
                    val res = com.localghost.app.sync.HealthSync.syncAll(hctx) { p -> healthMsg = p }
                    val skip = if (res.skipped.isEmpty()) "" else " (skipped: ${res.skipped.joinToString(", ")})"
                    healthMsg = when {
                        res.error != null -> "! ${res.error}$skip"
                        res.days > 0 -> "done , ${res.days} day(s) of history on your box$skip"
                        else -> "no health history found$skip"
                    }
                    tick++
                }
            }.padding(vertical = 6.dp))
    }
    var probeLines by remember { mutableStateOf<List<String>>(emptyList()) }
    var probing by remember { mutableStateOf(false) }
    Text(if (probing) "[ reading Health Connect… ]" else "[ what is in Health Connect? ]",
        color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
        modifier = Modifier.clickable {
            if (!probing) {
                probing = true
                scope.launch { probeLines = com.localghost.app.sync.HealthSync.probe(hctx); probing = false }
            }
        }.padding(vertical = 6.dp))
    probeLines.forEach { l -> Text("  $l", color = TerminalDim, style = MaterialTheme.typography.labelMedium) }
    if (probeLines.isNotEmpty()) Text("  a type with nothing in it is not shared with Health Connect: Samsung Health › Settings › Health Connect › allow it",
        color = TerminalDim, style = MaterialTheme.typography.labelMedium)
    if (healthMsg.isNotEmpty()) {
        Spacer(Modifier.height(6.dp))
        Text(healthMsg, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
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
    ) { Text("[ FORGET THE BOX ON THIS PHONE ]", style = MaterialTheme.typography.labelLarge) }

    if (confirming) {
        ConfirmDialog(
            title = "WIPE THIS PHONE",
            body = "This destroys the box connection, the device certificate, and the identity key on " +
                "this phone. The box keeps your data. To use this phone again you re-pair it, which " +
                "needs to be done at home with your security key. There is no undo from here.",
            requireWord = "WIPE",
            confirmLabel = "FORGET THE BOX",
            onConfirm = { confirming = false; onWipe() },
            onDismiss = { confirming = false },
        )
    }
}

/**
 * SERVER: the build the box runs (name, version, commit, when it was built, which Go), a newer
 * release from the mirror (the phone checks once a day, update/ServerUpdates.kt) and DEPLOY, the
 * shelf (the releases the box keeps, any of which can go back on), and ROLL BACK. The box checks a
 * release's signature with the key it already holds, puts it on, locks and restarts onto it, so the
 * app locks too; unlock again when it is back. On trial until its first unlock has run ten minutes
 * with the daemons up; back by itself if it fails, or with ROLL BACK.
 */
@Composable
private fun ServerUpdateSection(onLock: () -> Unit) {
    val ctx = androidx.compose.ui.platform.LocalContext.current
    val scope = androidx.compose.runtime.rememberCoroutineScope()
    var status by remember { mutableStateOf<com.localghost.app.net.BoxClient.UpdateStatus?>(null) }
    var offer by remember { mutableStateOf(com.localghost.app.update.ServerUpdates.lastOffer(ctx)) }
    var busy by remember { mutableStateOf("") }
    var result by remember { mutableStateOf("") }
    var armed by remember { mutableStateOf("") } // a shelf release tapped once: the next tap puts it on
    var ask by remember { mutableStateOf("") } // "deploy" or "rollback": the question open before the box restarts
    LaunchedEffect(Unit) {
        status = com.localghost.app.net.BoxClient.updateStatus(ctx)
        status?.let { com.localghost.app.update.ServerUpdates.noteBoxVersion(ctx, it.version) }
        if (offer == null) offer = com.localghost.app.update.ServerUpdates.check(ctx)
    }
    val lock: suspend (String) -> Unit = { what -> result = what; kotlinx.coroutines.delay(2500); onLock() }
    SectionLabel("SERVER")
    Spacer(Modifier.height(8.dp))
    val st = status
    Text(when {
        st == null -> "the box has not said which build it runs (an older build, or it is out of reach)"
        else -> "your box runs " + com.localghost.app.update.ReleaseInfo.describe(st.label, st.commit, st.builtAt, st.go)
    }, color = GhostText, style = MaterialTheme.typography.bodyMedium)
    if (st != null && st.trialState == "trial") Text("on trial: back to ${st.trialPrev} by itself if its first unlock fails; confirmed after ten minutes up",
        color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
    if (st != null && st.trialState == "rolled_back") Text("${st.trialVersion} was rolled back: ${st.trialReason}",
        color = Warning, style = MaterialTheme.typography.labelMedium)
    val o = offer
    val newer = o != null && st != null && com.localghost.app.update.ReleaseInfo.newer(o.release.version, st.version)
    Spacer(Modifier.height(6.dp))
    if (o != null && newer) {
        Text("${o.release.label} is out" + (if (o.release.date.isNotEmpty()) " (${com.localghost.app.update.ReleaseInfo.at(o.release.date)})" else "") +
            ", ${o.release.changes.size} change${if (o.release.changes.size == 1) "" else "s"}" +
            (if (o.release.since.isNotEmpty()) " since ${o.release.since}" else "") + ":",
            color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
        o.release.changes.take(12).forEach {
            Text("  $it", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        }
        if (o.release.changes.size > 12) Text("  … ${o.release.changes.size - 12} more", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
    } else if (o != null) {
        Text("the mirror's newest is " + com.localghost.app.update.ReleaseInfo.describe(o.release.label, o.release.commit, o.release.date, o.release.go) +
            (if (st != null && o.release.version == st.version.removePrefix("v")) ": what your box runs" else ""),
            color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
    } else {
        val miss = com.localghost.app.update.ServerUpdates.lastMiss(ctx)
        Text(if (miss.isEmpty()) "the mirror has not been read yet" else miss, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
    }
    if (busy.isNotEmpty()) {
        Spacer(Modifier.height(6.dp))
        Text("> $busy", color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
    }
    if (result.isNotEmpty()) {
        Spacer(Modifier.height(6.dp))
        Text(result, color = GhostText, style = MaterialTheme.typography.labelMedium)
    }
    Spacer(Modifier.height(8.dp))
    // DEPLOY and ROLL BACK restart the box, so each asks once before it does
    if (ask == "deploy" && o != null) AskDialog(
        title = "DEPLOY ${o.release.label}",
        body = "Your box fetches ${o.release.label} from the mirror, checks every file against the signed list, and restarts onto it on trial: back to what it runs now by itself if the first unlock fails. The box locks as it restarts; unlock it again in a minute.",
        confirmLabel = "DEPLOY",
        onConfirm = {
            ask = ""
            busy = "starting…"; result = ""
            scope.launch {
                val (ok, what) = com.localghost.app.update.ServerUpdates.deploy(ctx, o) { busy = it }
                busy = ""
                if (ok) lock("your box is restarting onto $what. It locks as it does: unlock it again in a minute.")
                else result = "not deployed: $what"
            }
        },
        onDismiss = { ask = "" },
    )
    if (ask == "rollback" && st != null) AskDialog(
        title = "ROLL BACK",
        body = "Your box puts ${st.trialPrev.ifEmpty { "the earlier build" }} back on and restarts onto it. It locks as it restarts; unlock it again in a minute. Your data is untouched.",
        confirmLabel = "ROLL BACK",
        onConfirm = {
            ask = ""
            busy = "putting the earlier build back…"; result = ""
            scope.launch {
                val (ok, why) = com.localghost.app.net.BoxClient.updateRollback(ctx)
                busy = ""
                if (ok) lock("your box is restarting onto the earlier build. Unlock it again in a minute.")
                else result = "not rolled back: $why"
            }
        },
        onDismiss = { ask = "" },
    )
    if (o != null && newer && busy.isEmpty()) {
        GhostButton("DEPLOY ${o.release.label}", { ask = "deploy" }, modifier = Modifier.fillMaxWidth())
    }
    // THE SHELF: the releases the box keeps, the running one left out; a tap, then a second to
    // confirm, puts one back on (verified again, on trial again)
    val shelf = st?.shelf?.filter { it.set && it.version != st.version } ?: emptyList()
    if (shelf.isNotEmpty() && busy.isEmpty()) {
        Spacer(Modifier.height(4.dp))
        Text("ON THE SHELF, to put back on", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
        shelf.forEach { k ->
            Row(Modifier.fillMaxWidth().padding(vertical = 4.dp), verticalAlignment = Alignment.CenterVertically) {
                Text(com.localghost.app.update.ReleaseInfo.describe(k.label, k.commit, k.date, k.go),
                    color = GhostTextDim, style = MaterialTheme.typography.labelMedium, modifier = Modifier.weight(1f))
                Text(if (armed == k.version) "[ sure? ]" else "[ put on ]", color = if (armed == k.version) Warning else TerminalGreen,
                    style = MaterialTheme.typography.labelMedium,
                    modifier = Modifier.clickable {
                        if (armed != k.version) { armed = k.version; return@clickable }
                        armed = ""
                        busy = "your box is checking ${k.label} again and putting it on…"; result = ""
                        scope.launch {
                            val (ok, why) = com.localghost.app.net.BoxClient.updateSwitch(ctx, k.version)
                            busy = ""
                            if (ok) lock("your box is restarting onto ${k.label}. It locks as it does: unlock it again in a minute.")
                            else result = "not put on: $why"
                        }
                    }.padding(start = 10.dp, top = 4.dp, bottom = 4.dp))
            }
        }
    }
    if (st != null && (st.trialState == "trial" || st.trialState == "confirmed") && busy.isEmpty()) {
        Spacer(Modifier.height(8.dp))
        GhostButton("ROLL BACK TO ${st.trialPrev.ifEmpty { "THE EARLIER BUILD" }}", { ask = "rollback" }, modifier = Modifier.fillMaxWidth())
    }
    Text("[ check the mirror now ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
        modifier = Modifier.clickable {
            scope.launch {
                busy = "reading the mirror…"; result = ""
                val fresh = com.localghost.app.update.ServerUpdates.check(ctx)
                offer = fresh ?: offer
                status = com.localghost.app.net.BoxClient.updateStatus(ctx) ?: status
                busy = ""
                if (fresh == null) result = com.localghost.app.update.ServerUpdates.lastMiss(ctx).ifEmpty { "the mirror did not answer" }
            }
        }.padding(vertical = 6.dp))
}
