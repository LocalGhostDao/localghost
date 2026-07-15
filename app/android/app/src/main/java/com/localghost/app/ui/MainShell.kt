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
    CHAT("CHAT", "›_"),
    CHATS("CHATS", "≡_"),
    MEMORIES("MEMORIES", "◇"),
    NOTIFICATIONS("NOTIFICATIONS", "△"),
    HARNESS("BOX STATUS", "◉"),
    SYNC("SYNC", "⇅"),
    GALLERY("GALLERY", "▦"),
    CODES("CODES", "⚿"),
    SETTINGS("SETTINGS", "⚙"),
    GLOSSARY("GLOSSARY", "≣"),
    CONNECTORS("CONNECTORS", "⊹"),
    MODELS("MODELS", "▢"),
    ABOUT("ABOUT", "?"),
    VERIFY("VERIFY BUILD", "✓"),
}

@Composable
fun MainShell(
    messages: List<Message>,
    streaming: Boolean,
    onSend: (String) -> Unit,
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
    pending: Loadable<List<PendingNotification>>,
    lifeContext: LifeContext?,
    memories: Loadable<List<MemoryEntry>>,
    daemons: Loadable<List<DaemonStatus>>,
    sync: SyncUiState,
    onSync: () -> Unit,
    onTogglePause: () -> Unit = {},
    onRequestFullAccess: () -> Unit,
    onTestNotification: () -> Unit,
    allowMobileSync: Boolean,
    onToggleMobileSync: (Boolean) -> Unit,
    thinkLevel: String = "",
    onCycleThink: () -> Unit = {},
    onOpenBoxChat: (Long) -> Unit = {},
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
    var dest by rememberSaveable { mutableStateOf(Dest.CHAT) }
    var showWipe by remember { mutableStateOf(false) }
    var showAddSheet by remember { mutableStateOf(false) }
    val drawerState = rememberDrawerState(DrawerValue.Closed)
    val scope = rememberCoroutineScope()
    fun close() = scope.launch { drawerState.close() }
    fun open() = scope.launch { drawerState.open() }

    // On rotation / size change, the drawer can re-settle open, force it closed.
    val orientation = LocalConfiguration.current.orientation
    LaunchedEffect(orientation) { drawerState.close() }

    BackHandler(enabled = drawerState.isOpen || dest != Dest.CHAT) {
        if (drawerState.isOpen) close() else dest = Dest.CHAT
    }

    ModalNavigationDrawer(
        drawerState = drawerState,
        drawerContent = {
            DrawerPanel(
                current = dest,
                boxConnected = boxConnected,
                conversations = conversations,
                activeConvId = activeConvId,
                onSelect = { dest = it; close() },
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
                TopBar(title = dest.label, onMenu = { open() },
                    onNewChat = if (dest == Dest.CHAT) onNewConversation else null)

                PermissionBanner(permState, onPermAction)

                Box(Modifier.weight(1f).fillMaxWidth()
                    .padding(bottom = pad.calculateBottomPadding())) {
                    when (dest) {
                        Dest.CHAT -> ChatScreen(messages, streaming, localModeActive, pendingAttachments,
                            onSend, onStopChat, { showAddSheet = true }, onClearAttachment,
                            brainLabel, brainIsBox, phoneModels, onPickBox, onPickPhoneModel,
                            { dest = Dest.MODELS },
                            incognito = incognito, onToggleIncognito = onToggleIncognito)
                        Dest.CHATS -> ChatsScreen(conversations, activeConvId,
                            onSelect = { onSelectConversation(it); dest = Dest.CHAT },
                            onNew = { onNewConversation(); dest = Dest.CHAT },
                            onDelete = onDeleteConversation,
                            onOpenBoxChat = { id -> onOpenBoxChat(id); dest = Dest.CHAT })
                        Dest.MEMORIES -> MemoriesScreen(lifeContext, memories)
                        Dest.NOTIFICATIONS -> {
                            val nctx = androidx.compose.ui.platform.LocalContext.current
                            val nowSec = System.currentTimeMillis() / 1000
                            // Warn within 6 hours of the 2-day token expiring, or once it is dead.
                            val hint = when {
                                com.localghost.app.security.SessionStore.isExpired(nctx, nowSec) -> SessionHint.EXPIRED
                                com.localghost.app.security.SessionStore.isExpiringSoon(nctx, nowSec, 6 * 3600) -> SessionHint.EXPIRING_SOON
                                else -> SessionHint.NONE
                            }
                            NotificationsScreen(pending, hint)
                        }
                        Dest.HARNESS -> HarnessScreen(daemons)
                        Dest.SYNC -> SyncScreen(sync, onSync, onRequestFullAccess, onTestNotification, onTogglePause = onTogglePause)
                        Dest.GALLERY -> GalleryScreen()
                        Dest.CODES -> PinManagementScreen(devices)
                        Dest.SETTINGS -> SettingsScreen(
                            onOpenVerify = { dest = Dest.VERIFY },
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
                        Dest.GLOSSARY -> GlossaryScreen()
                        Dest.CONNECTORS -> ConnectorsScreen(connectors, onConnect, onDisconnect)
                        Dest.MODELS -> ModelsScreen(catalogModels, modelRowState,
                            onDownloadModel, onCancelModel, onActivateModel, onDeleteModel)
                        Dest.ABOUT -> AboutScreen()
                        Dest.VERIFY -> VerifyScreen()
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

                if (showWipe) {
                    ConfirmDialog(
                        title = "WIPE EVERYTHING",
                        body = "Global crypto-erase on the box. The master key is destroyed " +
                            "and every persona becomes noise at once. Nobody reverses this.",
                        requireWord = "WIPE",
                        confirmLabel = "WIPE EVERYTHING",
                        onConfirm = { showWipe = false; onWipe() },
                        onDismiss = { showWipe = false },
                    )
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
private fun TopBar(title: String, onMenu: () -> Unit, onNewChat: (() -> Unit)? = null) {
    Row(
        Modifier.fillMaxWidth().padding(horizontal = 20.dp, vertical = 18.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text("≡", color = TerminalGreen, style = MaterialTheme.typography.titleLarge,
            modifier = Modifier.clickable { onMenu() }.padding(end = 16.dp))
        Image(
            painter = painterResource(R.drawable.ic_ghost),
            contentDescription = null,
            contentScale = ContentScale.Fit,
            modifier = Modifier.size(22.dp).padding(end = 8.dp),
        )
        Text(title, color = GhostText, style = MaterialTheme.typography.titleMedium)
        if (onNewChat != null) {
            Spacer(Modifier.weight(1f))
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
                        Text("✕", color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
                            modifier = Modifier.clickable { onDeleteConversation(c.id) }.padding(start = 8.dp))
                    }
                }
                Spacer(Modifier.height(4.dp))
                if (!showMoreChats && conversations.size > 5) {
                    Text("view more ▾", color = TerminalDim,
                        style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.clickable { showMoreChats = true }.padding(vertical = 4.dp))
                } else if (showMoreChats || conversations.size > 10) {
                    Text("see all chats →", color = TerminalDim,
                        style = MaterialTheme.typography.labelMedium,
                        modifier = Modifier.clickable { onSelect(Dest.CHATS) }.padding(vertical = 4.dp))
                }
            }

            Spacer(Modifier.height(20.dp))
            SectionLabel("YOUR ARCHIVE")
            listOf(Dest.GALLERY, Dest.MEMORIES, Dest.SYNC).forEach {
                DrawerRow(it, it == current) { onSelect(it) }
            }

            Spacer(Modifier.height(20.dp))
            SectionLabel("THE BOX")
            listOf(Dest.HARNESS, Dest.MODELS, Dest.NOTIFICATIONS, Dest.CONNECTORS, Dest.CODES).forEach {
                DrawerRow(it, it == current) { onSelect(it) }
            }

            Spacer(Modifier.height(16.dp))
            HorizontalDivider(color = GhostBorder)
            Spacer(Modifier.height(16.dp))

            listOf(Dest.SETTINGS, Dest.GLOSSARY, Dest.ABOUT).forEach {
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
