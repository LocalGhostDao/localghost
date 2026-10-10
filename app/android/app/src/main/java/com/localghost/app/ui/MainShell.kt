package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.activity.compose.BackHandler
import androidx.compose.ui.platform.LocalConfiguration
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.StrokeCap
import androidx.compose.ui.text.style.TextOverflow
import androidx.compose.ui.unit.dp
import androidx.compose.foundation.Image
import androidx.compose.ui.res.painterResource
import androidx.compose.ui.layout.ContentScale
import com.localghost.app.R
import com.localghost.app.chat.Attachment
import com.localghost.app.chat.Message
import com.localghost.app.net.DaemonStatus
import com.localghost.app.net.LifeContext
import com.localghost.app.net.MemoryEntry
import com.localghost.app.net.DeviceInfo
import com.localghost.app.net.ChatCapabilities
import com.localghost.app.net.PhoneModel
import com.localghost.app.net.Connector
import com.localghost.app.net.Conversation
import com.localghost.app.net.PendingNotification
import com.localghost.app.ui.theme.*
import kotlinx.coroutines.launch

enum class Dest(val label: String, val glyph: String) {
    HOME("HOME", "⌂"),
    CHAT("CHAT", "›_"),
    CHATS("CHATS", "≡_"),
    MEMORIES("MEMORIES", "◇"),
    MEMORY("MEMORY", "◇"),
    CHECKIN("CHECK-IN", "◐"),
    SOURCES("INTEGRATIONS", "⊛"),
    INTEGRATION("INTEGRATION", "⊛"),
    FEEDS("NEWS FEEDS", "¶"),
    WIKIPEDIA("WIKIPEDIA", "W"),
    NEWS("NEWS", "¶"),
    CRYPTO("CRYPTO", "₿"),
    COIN("COIN", "◈"),
    DAY("DAY", "◷"),
    NOTIFICATION("NOTIFICATION", "△"),
    NOTIFICATIONS("NOTIFICATIONS", "△"),
    HARNESS("BOX STATUS", "◉"),
    SYNC("SYNC", "⇅"),
    GALLERY("GALLERY", "▦"),
    MAP("MAP", "◎"),
    PHRASES("PHRASES", "»"),
    HEALTH("HEALTH", "♥"),
    CODES("CODES", "⚿"),
    SETTINGS("SETTINGS", "⚙"),
    PERMISSIONS("PERMISSIONS", "⊡"),
    GLOSSARY("GLOSSARY", "≣"),
    CONNECTORS("CONNECTORS", "⊹"),
    MODELS("MODELS", "▢"),
    ABOUT("ABOUT", "?"),
    VERIFY("VERIFY BUILD", "✓"),
}

@Composable
fun MainShell(
    genStats: String = "",
    navRequest: String = "",
    onNavConsumed: () -> Unit = {},
    messages: List<Message>,
    streaming: Boolean,
    onSend: (String) -> Unit,
    chatTouchedMs: Long = 0L,
    onStopChat: () -> Unit,
    pendingAttachments: List<Attachment>,
    onClearAttachment: (Attachment) -> Unit,
    chatCaps: ChatCapabilities,
    onChatCaps: (ChatCapabilities) -> Unit,
    localModeActive: Boolean,
    forceLocal: Boolean,
    onForceLocal: (Boolean) -> Unit,
    localModelPresent: Boolean,
    brainLabel: String,
    brainIsBox: Boolean,
    phoneModels: List<Triple<String,String,Boolean>>,
    onPickBox: () -> Unit,
    onPickPhoneModel: (String) -> Unit,
    catalogModels: List<PhoneModel>,
    modelRowState: (String) -> ModelRowState,
    onDownloadModel: (String) -> Unit,
    onCancelModel: (String) -> Unit,
    onActivateModel: (String) -> Unit,
    onDeleteModel: (String) -> Unit,
    availableDaemons: List<String>,
    onCamera: () -> Unit,
    onPhotos: () -> Unit,
    onFiles: () -> Unit,
    onVoice: () -> Unit,
    connectors: Loadable<List<Connector>>,
    onConnect: (String) -> Unit,
    onDisconnect: (String) -> Unit,
    permState: PermState,
    onPermAction: () -> Unit,
    grants: List<Grant> = emptyList(),
    permAsking: Boolean = false,
    onGrantAll: () -> Unit = {},
    onAppSettings: () -> Unit = {},
    pending: Loadable<List<PendingNotification>>,
    lifeContext: LifeContext?,
    memories: Loadable<List<MemoryEntry>>,
    daemons: Loadable<List<DaemonStatus>>,
    onRefreshDaemons: () -> Unit = {},
    sync: SyncUiState,
    onSync: () -> Unit,
    onTogglePause: () -> Unit = {},
    onRequestFullAccess: () -> Unit,
    allowMobileSync: Boolean,
    onToggleMobileSync: (Boolean) -> Unit,
    thinkLevel: String = "",
    onCycleThink: () -> Unit = {},
    onOpenBoxChat: (Long) -> Unit = {},
    onRenameBoxChat: (Long, String) -> Unit = { _, _ -> },
    onDeleteBoxChat: (Long) -> Unit = {},
    incognito: Boolean = false,
    onToggleIncognito: () -> Unit = {},
    onToggleMute: (Boolean) -> Unit,
    boxConnected: Boolean,
    onLock: () -> Unit,
    onExport: () -> Unit,
    exportState: String?,
    onWipe: () -> Unit,
    devices: Loadable<List<DeviceInfo>>,
    conversations: List<Conversation>,
    activeConvId: String?,
    onSelectConversation: (String) -> Unit,
    onNewConversation: () -> Unit,
    onDeleteConversation: (String) -> Unit,
) {
    // HOME first: the prices, the day's news and a box to ask from; a question asked there opens CHAT
    var dest by rememberSaveable { mutableStateOf(Dest.HOME) }
    // the story a point of home's brief opens in NEWS (0: none)
    var newsFocus by rememberSaveable { mutableLongStateOf(0L) }
    // the coin whose page is open, and where it was opened from (‹ and back go there)
    var coinSym by rememberSaveable { mutableStateOf("BTC") }
    var coinFrom by rememberSaveable { mutableStateOf(Dest.CRYPTO) }
    // the pages INTEGRATIONS opens (WIKIPEDIA, NEWS, CRYPTO, MAP, the feeds) go back to it when
    // opened from there: to the integration's page when one is open, else to the cards
    var fromSources by rememberSaveable { mutableStateOf(false) }
    var integOpen by rememberSaveable { mutableStateOf("") } // the integration whose page is open
    fun openCoin(sym: String, from: Dest) { coinSym = sym; coinFrom = from; dest = Dest.COIN }
    // what a notification opens: a day on MAP, "near" in MEMORIES ("" none)
    var mapDay by rememberSaveable { mutableStateOf("") }
    var memFocus by rememberSaveable { mutableStateOf("") }
    // one memory's page: its id, and where it came from (‹ and back go there)
    var memOpen by rememberSaveable { mutableLongStateOf(0L) }
    var memFrom by rememberSaveable { mutableStateOf(Dest.MEMORIES) }
    fun openMemory(id: Long) {
        if (dest != Dest.MEMORY) memFrom = dest
        memOpen = id
        dest = Dest.MEMORY
    }
    // one day's page (HOME's "this day", the weekly highlight): where it came from, for back
    var dayOpen by rememberSaveable { mutableStateOf("") }
    var dayFrom by rememberSaveable { mutableStateOf(Dest.HOME) }
    fun openDay(day: String) {
        if (dest != Dest.DAY) dayFrom = dest
        dayOpen = day
        dest = Dest.DAY
    }
    // one notification's page (a tap in the shade or on the list): the id, where it came from
    var notifOpen by rememberSaveable { mutableStateOf(0L) }
    var notifFrom by rememberSaveable { mutableStateOf(Dest.NOTIFICATIONS) }
    fun openNotification(id: Long) {
        if (dest != Dest.NOTIFICATION) notifFrom = dest
        notifOpen = id
        dest = Dest.NOTIFICATION
    }
    fun openTarget(t: NotifLink.Target) {
        when (t.dest) {
            "notification" -> t.arg.toLongOrNull()?.let { openNotification(it) }
            "day" -> if (t.arg.isNotEmpty()) openDay(t.arg) else dest = Dest.MEMORIES
            "map" -> { mapDay = t.arg; dest = Dest.MAP }
            // a memory's id: its own page; "near" (or nothing): the list, NEAR YOU open
            "memories" -> t.arg.toLongOrNull()?.let { openMemory(it) } ?: run { memFocus = t.arg; dest = Dest.MEMORIES }
            "checkin" -> dest = Dest.CHECKIN
            "news" -> { newsFocus = 0L; dest = Dest.NEWS }
            "status" -> dest = Dest.HARNESS
            "notifications" -> dest = Dest.NOTIFICATIONS
        }
    }
    // A notification tap lands here AFTER the security gate (MainShell only exists unlocked):
    // navigate to the thing the notification was about, once.
    LaunchedEffect(navRequest) {
        when {
            // a notification's own place (the shade's tap): "map:<day>", "memories:<id>", "status"
            navRequest.startsWith("map") || navRequest.startsWith("memories") || navRequest.startsWith("day:") ||
                navRequest.startsWith("notification:") || navRequest == "status" || navRequest == "checkin" ->
                openTarget(NotifLink.resolve(navRequest, "", ""))
        }
        when (navRequest) {
            "notifications" -> dest = Dest.NOTIFICATIONS
            "news" -> dest = Dest.NEWS
            "home" -> dest = Dest.HOME
            "chat" -> dest = Dest.CHAT
            "phrases" -> dest = Dest.PHRASES
            "settings" -> dest = Dest.SETTINGS
        }
        if (navRequest.isNotEmpty()) onNavConsumed()
    }
    var showWipe by remember { mutableStateOf(false) }
    var showAddSheet by remember { mutableStateOf(false) }
    val drawerState = rememberDrawerState(DrawerValue.Closed)
    val scope = rememberCoroutineScope()
    fun close() = scope.launch { drawerState.close() }
    fun open() = scope.launch { drawerState.open() }

    // On rotation / size change, the drawer can re-settle open, force it closed.
    val orientation = LocalConfiguration.current.orientation
    LaunchedEffect(orientation) { drawerState.close() }

    // WHERE BACK GOES, one answer for the system key and the TopBar's ‹: a page opened from another
    // returns there (a coin to its list, a day or a memory or a notification to what opened it, a
    // page SOURCES opened to SOURCES), the rest to HOME
    fun goBack() {
        when (dest) {
            Dest.COIN -> dest = coinFrom
            Dest.DAY -> dest = if (dayFrom == Dest.DAY) Dest.HOME else dayFrom
            Dest.NOTIFICATION -> dest = if (notifFrom == Dest.NOTIFICATION) Dest.NOTIFICATIONS else notifFrom
            Dest.MEMORY -> dest = if (memFrom == Dest.MEMORY) Dest.MEMORIES else memFrom
            Dest.INTEGRATION -> { dest = Dest.SOURCES; integOpen = "" }
            Dest.FEEDS -> dest = if (integOpen.isNotEmpty()) Dest.INTEGRATION else Dest.SOURCES
            Dest.WIKIPEDIA, Dest.NEWS, Dest.CRYPTO, Dest.MAP -> { dest = if (!fromSources) Dest.HOME else if (integOpen.isNotEmpty()) Dest.INTEGRATION else Dest.SOURCES; fromSources = false }
            else -> dest = Dest.HOME
        }
    }

    BackHandler(enabled = drawerState.isOpen || dest != Dest.HOME) {
        if (drawerState.isOpen) close() else goBack()
    }

    ModalNavigationDrawer(
        drawerState = drawerState,
        drawerContent = {
            DrawerPanel(
                current = dest,
                boxConnected = boxConnected,
                conversations = conversations,
                activeConvId = activeConvId,
                onSelect = { dest = it; fromSources = false; close() },
                onSelectConversation = { onSelectConversation(it); close() },
                onNewConversation = { onNewConversation(); close() },
                onDeleteConversation = onDeleteConversation,
                onLock = { close(); onLock() },
            )
        },
    ) {
        GhostScaffold { pad ->
            Column(Modifier.fillMaxSize()
                .padding(top = pad.calculateTopPadding())
                .padding(top = 4.dp)) {
                TopBar(title = if (dest == Dest.COIN) coinSym else dest.label, onMenu = { open() },
                    onHome = if (dest == Dest.HOME) null else ({ goBack() }),
                    onNewChat = if (dest == Dest.CHAT) onNewConversation else null,
                    chatToggles = dest == Dest.CHAT, incognito = incognito, onToggleIncognito = onToggleIncognito)

                PermissionBanner(permState, onPermAction)
                PhraseOfferBanner(onOpen = { dest = Dest.PHRASES })

                Box(Modifier.weight(1f).fillMaxWidth()
                    .padding(bottom = pad.calculateBottomPadding())) {
                    when (dest) {
                        Dest.HOME -> HomeScreen(
                            // a question from HOME goes on with the chat while it is recent (twenty
                            // minutes), else starts a new one; HOME says which under its box
                            onAsk = { q ->
                                if (messages.isEmpty() || System.currentTimeMillis() - chatTouchedMs > 20 * 60_000L) onNewConversation()
                                onSend(q); dest = Dest.CHAT
                            },
                            continuing = if (messages.isNotEmpty() && System.currentTimeMillis() - chatTouchedMs <= 20 * 60_000L)
                                messages.lastOrNull { it.role == Message.Role.USER }?.text ?: "" else "",
                            onNewChat = { onNewConversation() },
                            onOpenChat = { dest = Dest.CHAT },
                            onOpenNews = { newsFocus = 0L; dest = Dest.NEWS },
                            onOpenStory = { id -> newsFocus = id; dest = Dest.NEWS },
                            onOpenCrypto = { dest = Dest.CRYPTO },
                            onOpenCoin = { sym -> openCoin(sym, Dest.HOME) },
                            onOpenTarget = { link -> openTarget(NotifLink.resolve(link, "", "")) })
                        Dest.CRYPTO -> CryptoScreen(onOpenCoin = { sym -> openCoin(sym, Dest.CRYPTO) })
                        Dest.COIN -> CoinScreen(coinSym)
                        Dest.DAY -> DayScreen(if (dayOpen.isEmpty()) DayText.of(System.currentTimeMillis() / 1000) else dayOpen,
                            onDay = { d -> dayOpen = d },
                            onOpenMap = { d -> mapDay = d; dest = Dest.MAP },
                            onOpenTarget = { link -> openTarget(NotifLink.resolve(link, "", "")) },
                            onAsk = { q -> onNewConversation(); onSend(q); dest = Dest.CHAT })
                        Dest.CHAT -> ChatScreen(messages, streaming, localModeActive, pendingAttachments,
                            onSend, onStopChat, { showAddSheet = true }, onClearAttachment,
                            brainLabel, brainIsBox, phoneModels, onPickBox, onPickPhoneModel,
                            { dest = Dest.MODELS },
                            incognito = incognito, onToggleIncognito = onToggleIncognito,
                            genStats = if (com.localghost.app.settings.AppSettings.debugMode(
                                    androidx.compose.ui.platform.LocalContext.current)) genStats else "")
                        Dest.CHATS -> ChatsScreen(conversations, activeConvId,
                            onSelect = { onSelectConversation(it); dest = Dest.CHAT },
                            onNew = { onNewConversation(); dest = Dest.CHAT },
                            onDelete = onDeleteConversation,
                            onOpenBoxChat = { id -> onOpenBoxChat(id); dest = Dest.CHAT },
                            onRenameBoxChat = onRenameBoxChat,
                            onDeleteBoxChat = onDeleteBoxChat)
                        Dest.MEMORIES -> MemoriesScreen(lifeContext, open = memFocus, onOpened = { memFocus = "" },
                            onOpenDay = { d -> openDay(d) },
                            onOpenMemory = { id -> openMemory(id) },
                            onOpenCheckin = { dest = Dest.CHECKIN })
                        Dest.MEMORY -> MemoryScreen(memOpen,
                            onOpenDay = { d -> openDay(d) },
                            onOpenMemory = { id -> openMemory(id) },
                            backLabel = (if (memFrom == Dest.MEMORY) Dest.MEMORIES else memFrom).label.lowercase(),
                            onBack = { goBack() })
                        Dest.CHECKIN -> CheckinScreen(onOpenDay = { d -> openDay(d) })
                        Dest.SOURCES -> SourcesScreen(onOpenIntegration = { id -> integOpen = id; dest = Dest.INTEGRATION })
                        Dest.INTEGRATION -> IntegrationScreen(integOpen,
                            onOpen = { page ->
                                fromSources = true
                                dest = when (page) { "wikipedia" -> Dest.WIKIPEDIA; "news" -> { newsFocus = 0L; Dest.NEWS }; "crypto" -> Dest.CRYPTO; "map" -> Dest.MAP; else -> Dest.INTEGRATION }
                            },
                            onFeeds = { dest = Dest.FEEDS },
                            onBack = { goBack() })
                        Dest.FEEDS -> NewsFeedsScreen(onBack = { goBack() })
                        Dest.WIKIPEDIA -> WikipediaScreen()
                        Dest.NEWS -> NewsScreen(openStory = newsFocus, onStoryShown = { newsFocus = 0L })
                        Dest.NOTIFICATIONS -> {
                            val nctx = androidx.compose.ui.platform.LocalContext.current
                            val nowSec = System.currentTimeMillis() / 1000
                            // Warn within 6 hours of the 2-day token expiring, or once it is dead.
                            val hint = when {
                                com.localghost.app.security.SessionStore.isExpired(nctx, nowSec) -> SessionHint.EXPIRED
                                com.localghost.app.security.SessionStore.isExpiringSoon(nctx, nowSec, 6 * 3600) -> SessionHint.EXPIRING_SOON
                                else -> SessionHint.NONE
                            }
                            NotificationsScreen(pending, hint, onOpen = { t -> openTarget(t) }, onOpenOne = { id -> openNotification(id) })
                        }
                        Dest.NOTIFICATION -> NotificationScreen(notifOpen,
                            onOpenTarget = { t -> openTarget(t) },
                            onDay = { d -> openDay(d) },
                            onBack = { dest = Dest.NOTIFICATIONS })
                        Dest.HARNESS -> HarnessScreen(daemons, onRefresh = onRefreshDaemons)
                        Dest.SYNC -> SyncScreen(sync, onSync, onRequestFullAccess, onTogglePause = onTogglePause)
                        Dest.GALLERY -> GalleryScreen()
                        Dest.MAP -> MapScreen(openDay = mapDay, onDayShown = { mapDay = "" })
                        Dest.PHRASES -> PhrasesScreen()
                        Dest.HEALTH -> HealthScreen()
                        Dest.CODES -> PinManagementScreen(devices)
                        Dest.SETTINGS -> SettingsScreen(
                            onOpenVerify = { dest = Dest.VERIFY },
                            onOpenMap = { dest = Dest.MAP },
                            allowMobileSync = allowMobileSync,
                            onToggleMobileSync = onToggleMobileSync,
                            thinkLevel = thinkLevel,
                            onCycleThink = onCycleThink,
                            notificationsMuted = sync.notificationsMuted,
                            onToggleMute = onToggleMute,
                            onExport = onExport,
                            exportState = exportState,
                            onLock = onLock,
                            onWipe = { showWipe = true },
                        )
                        Dest.PERMISSIONS -> PermissionsScreen(grants, permAsking, onGrantAll, onAppSettings)
                        Dest.GLOSSARY -> GlossaryScreen()
                        Dest.CONNECTORS -> ConnectorsScreen(connectors, onConnect, onDisconnect)
                        Dest.MODELS -> ModelsScreen(catalogModels, modelRowState,
                            onDownloadModel, onCancelModel, onActivateModel, onDeleteModel)
                        Dest.ABOUT -> AboutScreen()
                        Dest.VERIFY -> VerifyScreen()
                    }
                    // THE GLASS, now and then (Crt.kt, CrtMood): on a page change the shell rolls
                    // for one small effect, a wash, a stray line or the heading typing in, never two
                    // inside a minute and a half; drawing only, no touch taken; off in SETTINGS › SCREEN
                    val crtOn = com.localghost.app.settings.AppSettings.crt(androidx.compose.ui.platform.LocalContext.current)
                    var crtLast by rememberSaveable { mutableLongStateOf(0L) }
                    LaunchedEffect(dest) {
                        if (!crtOn) { CrtState.effect = CrtMood.Effect.NONE; return@LaunchedEffect }
                        val now = System.currentTimeMillis()
                        val e = CrtMood.pick(now, crtLast, kotlin.random.Random.nextInt(0, 1 shl 20))
                        if (e == CrtMood.Effect.NONE) { CrtState.effect = CrtMood.Effect.NONE; return@LaunchedEffect }
                        crtLast = now
                        CrtState.effect = e
                        kotlinx.coroutines.delay(CrtMood.holdMs(e))
                        CrtState.effect = CrtMood.Effect.NONE
                    }
                    if (crtOn) {
                        CrtWash(playing = CrtState.effect == CrtMood.Effect.WASH, modifier = Modifier.matchParentSize())
                        CrtLine(playing = CrtState.effect == CrtMood.Effect.LINE, modifier = Modifier.matchParentSize())
                    }
                }

                if (sync.busy) {
                    val total = sync.photoTotal + sync.videoTotal
                    val done = sync.photoDone + sync.videoDone
                    val frac = if (total > 0) (done.toFloat() / total).coerceIn(0f, 1f) else 0f
                    Column(Modifier.fillMaxWidth().background(Void)
                        .padding(horizontal = 14.dp).padding(bottom = pad.calculateBottomPadding(), top = 4.dp)) {
                        Text("▶ syncing $done/$total · ${sync.curName.ifBlank { "…" }}",
                            color = TerminalDim, style = MaterialTheme.typography.labelMedium,
                            maxLines = 1, overflow = TextOverflow.Ellipsis)
                        Spacer(Modifier.height(3.dp))
                        LinearProgressIndicator(progress = { frac }, color = TerminalGreen,
                            trackColor = VoidLighter, strokeCap = StrokeCap.Butt,
                            modifier = Modifier.fillMaxWidth().height(2.dp))
                    }
                }

                // one confirmation, in SETTINGS (WipeButton); the second here said "global
                // crypto-erase on the box", which nothing does: the phone forgets the box, the box
                // keeps everything
                if (showWipe) {
                    showWipe = false
                    onWipe()
                }
                if (showAddSheet) {
                    AddToChatSheet(
                        caps = chatCaps,
                        availableDaemons = availableDaemons,
                        onCaps = onChatCaps,
                        forceLocal = forceLocal,
                        onForceLocal = onForceLocal,
                        localModelPresent = localModelPresent,
                        onManageModels = { showAddSheet = false; dest = Dest.MODELS },
                        onCamera = { showAddSheet = false; onCamera() },
                        onPhotos = { showAddSheet = false; onPhotos() },
                        onFiles = { showAddSheet = false; onFiles() },
                        onVoice = { showAddSheet = false; onVoice() },
                        onOpenConnectors = { showAddSheet = false; dest = Dest.CONNECTORS },
                        onDismiss = { showAddSheet = false },
                    )
                }
            }
        }
    }
}

@Composable
private fun TopBar(
    title: String, onMenu: () -> Unit, onHome: (() -> Unit)? = null, onNewChat: (() -> Unit)? = null,
    chatToggles: Boolean = false, incognito: Boolean = false, onToggleIncognito: () -> Unit = {},
) {
    Row(
        Modifier.fillMaxWidth().padding(horizontal = 20.dp, vertical = 18.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text("≡", color = TerminalGreen, style = MaterialTheme.typography.titleLarge,
            modifier = Modifier.clickable { onMenu() }.padding(end = 16.dp))
        // BACK HOME from anywhere but home: the arrow, the ghost and the title all go there
        val home = Modifier.then(if (onHome != null) Modifier.clickable { onHome() } else Modifier)
        Row(home, verticalAlignment = Alignment.CenterVertically) {
            if (onHome != null) {
                Text("‹", color = TerminalGreen, style = MaterialTheme.typography.titleLarge, modifier = Modifier.padding(end = 10.dp))
            }
            Image(
                painter = painterResource(R.drawable.ic_ghost),
                contentDescription = if (onHome != null) "Home" else null,
                contentScale = ContentScale.Fit,
                modifier = Modifier.size(22.dp).padding(end = 8.dp),
            )
            Text(title, color = GhostText, style = MaterialTheme.typography.titleMedium)
        }
        Spacer(Modifier.weight(1f))
        if (chatToggles) {
            // INCOGNITO and WEB, one icon each, up here where they are seen before typing. Incognito
            // starts off (the chat is saved on the box) and turns amber when on; web starts on
            // "auto" (searched on this phone when a question needs the outside world) and cycles
            // auto → on (every question) → off. The word under the globe says which.
            val wctx = androidx.compose.ui.platform.LocalContext.current
            var web by remember { mutableStateOf(com.localghost.app.settings.AppSettings.webMode(wctx)) }
            Column(horizontalAlignment = Alignment.CenterHorizontally,
                modifier = Modifier.clickable { onToggleIncognito() }.padding(horizontal = 8.dp)) {
                androidx.compose.material3.Icon(painterResource(R.drawable.ic_incognito),
                    contentDescription = if (incognito) "Incognito on: not saved" else "Incognito off: saved on the box",
                    tint = if (incognito) Warning else GhostTextDim, modifier = Modifier.size(22.dp))
                Text(if (incognito) "incognito" else "saved", color = if (incognito) Warning else TerminalDim,
                    style = MaterialTheme.typography.labelSmall)
            }
            Column(horizontalAlignment = Alignment.CenterHorizontally,
                modifier = Modifier.clickable {
                    web = when (web) { "auto" -> "on"; "on" -> "off"; else -> "auto" }
                    com.localghost.app.settings.AppSettings.setWebMode(wctx, web)
                }.padding(horizontal = 8.dp)) {
                androidx.compose.material3.Icon(painterResource(R.drawable.ic_web),
                    contentDescription = "Web search: $web",
                    tint = when (web) { "on" -> TerminalGreen; "auto" -> TerminalDim; else -> GhostBorder },
                    modifier = Modifier.size(22.dp))
                Text("web $web", color = if (web == "on") TerminalGreen else TerminalDim,
                    style = MaterialTheme.typography.labelSmall)
            }
            Spacer(Modifier.width(8.dp))
        }
        if (onNewChat != null) {
            Text("＋", color = TerminalGreen, style = MaterialTheme.typography.titleLarge,
                modifier = Modifier.clickable { onNewChat() })
        }
    }
}

@Composable
private fun DrawerPanel(
    current: Dest,
    boxConnected: Boolean,
    conversations: List<Conversation>,
    activeConvId: String?,
    onSelect: (Dest) -> Unit,
    onSelectConversation: (String) -> Unit,
    onNewConversation: () -> Unit,
    onDeleteConversation: (String) -> Unit,
    onLock: () -> Unit,
) {
    ModalDrawerSheet(
        drawerContainerColor = Void,
        drawerContentColor = GhostText,
        modifier = Modifier.fillMaxWidth(0.82f).widthIn(max = 360.dp),
    ) {
        Column(Modifier.fillMaxSize().verticalScroll(rememberScrollState()).padding(20.dp)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Image(
                    painter = painterResource(R.drawable.ic_ghost),
                    contentDescription = null,
                    contentScale = ContentScale.Fit,
                    modifier = Modifier.size(32.dp).padding(end = 12.dp),
                )
                Text("LOCALGHOST", color = TerminalGreen, style = MaterialTheme.typography.titleLarge)
            }
            Spacer(Modifier.height(8.dp))
            // connection status, at the TOP, under the wordmark
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(if (boxConnected) "●" else "○",
                    color = if (boxConnected) TerminalGreen else GhostTextDim,
                    style = MaterialTheme.typography.bodyMedium)
                Spacer(Modifier.width(8.dp))
                Text(if (boxConnected) "connected to the box" else "not connected",
                    color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            }

            Spacer(Modifier.height(28.dp))
            // The menu grew a destination at a time until fourteen flat rows made nothing findable.
            // Organised by what the person is THINKING about, not by which daemon serves it:
            //   CHAT , the product, first and alone (recent conversations live right under it)
            //   YOUR ARCHIVE , the life being kept: pictures, memories, and the pipe feeding them
            //   THE BOX , the machine: status, models, notifications, pairing and verification
            //   the app-level tail (settings, glossary, about) stays below the divider as before
            DrawerRow(Dest.HOME, Dest.HOME == current) { onSelect(Dest.HOME) }
            DrawerRow(Dest.CHAT, Dest.CHAT == current) { onSelect(Dest.CHAT) }

            if (conversations.isNotEmpty()) {
                Spacer(Modifier.height(20.dp))
                Row(verticalAlignment = Alignment.CenterVertically) {
                    Text("RECENT CHATS", color = TerminalDim,
                        style = MaterialTheme.typography.labelMedium)
                    Spacer(Modifier.weight(1f))
                    Text("＋ new", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.clickable { onNewConversation() })
                }
                Spacer(Modifier.height(8.dp))
                // Quick-switching lives HERE, not on the chats screen: 5 recents keep the drawer
                // tight, "view more" doubles it in place for the deep-switch days, and only past
                // ten do you leave the drawer at all.
                var showMoreChats by remember { mutableStateOf(false) }
                var armedDelete by remember { mutableStateOf("") } // a chat's ✕ tapped once: the second tap deletes
                conversations.take(if (showMoreChats) 10 else 5).forEach { c ->
                    Row(Modifier.fillMaxWidth()
                        .clickable { onSelectConversation(c.id); onSelect(Dest.CHAT) }
                        .padding(vertical = 8.dp),
                        verticalAlignment = Alignment.CenterVertically) {
                        Column(Modifier.weight(1f)) {
                            Text(c.title,
                                color = if (c.id == activeConvId) TerminalGreen else GhostText,
                                style = MaterialTheme.typography.bodyMedium, maxLines = 1)
                            Text("${c.updatedLabel} · ${c.messageCount} msgs", color = GhostTextDim,
                                style = MaterialTheme.typography.labelMedium)
                        }
                        Text(if (armedDelete == c.id) "[ delete? ]" else "✕", color = if (armedDelete == c.id) Warning else GhostTextDim, style = MaterialTheme.typography.labelMedium,
                            modifier = Modifier.clickable { if (armedDelete == c.id) { armedDelete = ""; onDeleteConversation(c.id) } else armedDelete = c.id }.padding(start = 8.dp))
                    }
                }
                Spacer(Modifier.height(4.dp))
                if (!showMoreChats && conversations.size > 5) {
                    Text("view more ▾", color = TerminalDim,
                        style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.clickable { showMoreChats = true }.padding(vertical = 4.dp))
                } else if (showMoreChats || conversations.size > 10) {
                    Text("see all chats ›", color = TerminalDim,
                        style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.clickable { onSelect(Dest.CHATS) }.padding(vertical = 4.dp))
                }
            }

            Spacer(Modifier.height(20.dp))
            SectionLabel("YOUR ARCHIVE")
            // PHRASES appears while the lock-screen card is on (from the start; off by hand in settings).
            val phrasesOn = com.localghost.app.phrases.PhraseState.enabled(androidx.compose.ui.platform.LocalContext.current)
            listOf(Dest.GALLERY, Dest.MAP, Dest.PHRASES, Dest.HEALTH, Dest.CHECKIN, Dest.MEMORIES, Dest.SYNC)
                .filter { it != Dest.PHRASES || phrasesOn || current == Dest.PHRASES }
                .forEach { DrawerRow(it, it == current) { onSelect(it) } }

            Spacer(Modifier.height(20.dp))
            // THE BOX: INTEGRATIONS first (what the box draws on beyond the archive: Wikipedia,
            // the news, the market numbers, the weather, the maps, speech; one row, the pages
            // open from its cards and from HOME), then the machine itself
            SectionLabel("THE BOX")
            DrawerRow(Dest.SOURCES, current in setOf(Dest.SOURCES, Dest.FEEDS, Dest.INTEGRATION, Dest.NEWS, Dest.CRYPTO, Dest.WIKIPEDIA)) { onSelect(Dest.SOURCES) }
            listOf(Dest.HARNESS, Dest.MODELS, Dest.NOTIFICATIONS, Dest.CONNECTORS, Dest.CODES).forEach {
                DrawerRow(it, it == current) { onSelect(it) }
            }

            Spacer(Modifier.height(16.dp))
            HorizontalDivider(color = GhostBorder)
            Spacer(Modifier.height(16.dp))

            // the app itself: its settings, the phone's permissions in one place, the words, about
            listOf(Dest.SETTINGS, Dest.PERMISSIONS, Dest.GLOSSARY, Dest.ABOUT).forEach {
                DrawerRow(it, it == current) { onSelect(it) }
            }
            DrawerRowRaw(glyph = "⏻", label = "LOCK", selected = false, onClick = onLock)
        }
    }
}

@Composable
private fun SectionLabel(text: String) {
    Text(text, color = TerminalDim, style = MaterialTheme.typography.labelMedium,
        modifier = Modifier.padding(bottom = 6.dp))
}

@Composable
private fun DrawerRow(dest: Dest, selected: Boolean, onClick: () -> Unit) =
    DrawerRowRaw(dest.glyph, dest.label, selected, onClick)

@Composable
private fun DrawerRowRaw(glyph: String, label: String, selected: Boolean, onClick: () -> Unit) {
    Row(
        Modifier.fillMaxWidth().clickable { onClick() }.padding(vertical = 14.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        val tint = if (selected) TerminalGreen else GhostText
        Text(glyph, color = tint, style = MaterialTheme.typography.titleMedium,
            modifier = Modifier.width(36.dp))
        Text(label, color = tint, style = MaterialTheme.typography.bodyLarge)
    }
}
