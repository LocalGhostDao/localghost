package com.localghost.app

import android.Manifest
import android.content.Context
import android.content.pm.PackageManager
import android.hardware.biometrics.BiometricManager.Authenticators.BIOMETRIC_STRONG
import android.hardware.biometrics.BiometricManager.Authenticators.DEVICE_CREDENTIAL
import android.hardware.biometrics.BiometricPrompt
import android.net.ConnectivityManager
import android.net.NetworkCapabilities
import android.net.Uri
import android.provider.Settings
import android.content.Intent
import android.graphics.Color as AndroidColor
import android.os.Bundle
import android.os.VibrationEffect
import android.os.VibratorManager
import android.os.CancellationSignal
import androidx.activity.ComponentActivity
import androidx.activity.compose.setContent
import androidx.activity.SystemBarStyle
import androidx.activity.enableEdgeToEdge
import androidx.activity.result.contract.ActivityResultContracts
import androidx.compose.runtime.getValue
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.remember
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.mutableStateMapOf
import androidx.compose.runtime.setValue
import androidx.core.content.ContextCompat
import androidx.lifecycle.Lifecycle
import androidx.lifecycle.lifecycleScope
import androidx.lifecycle.repeatOnLifecycle
import com.localghost.app.chat.Attachment
import com.localghost.app.chat.Message
import com.localghost.app.debug.CrashHandler
import com.localghost.app.net.BoxClient
import com.localghost.app.net.EnrollLink
import com.localghost.app.net.DaemonStatus
import com.localghost.app.net.LifeContext
import com.localghost.app.net.MemoryEntry
import com.localghost.app.net.DeviceInfo
import com.localghost.app.net.PendingNotification
import com.localghost.app.net.UnlockSnapshot
import com.localghost.app.net.UnlockStage
import com.localghost.app.net.StageState
import com.localghost.app.notify.ForegroundPoller
import com.localghost.app.notify.NotifyState
import com.localghost.app.notify.Notifications
import com.localghost.app.notify.PollWorker
import com.localghost.app.security.AppLock
import com.localghost.app.security.AuthGate
import com.localghost.app.security.BoxConfig
import com.localghost.app.settings.AppSettings
import com.localghost.app.sync.CommandResult
import com.localghost.app.sync.MediaKind
import com.localghost.app.sync.SyncEngine
import com.localghost.app.sync.SyncWorker
import com.localghost.app.ui.CrashScreen
import com.localghost.app.ui.SetupScreen
import com.localghost.app.ui.QrScanScreen
import com.localghost.app.ui.Loadable
import com.localghost.app.ui.PermState
import com.localghost.app.net.ChatCapabilities
import com.localghost.app.net.Connector
import com.localghost.app.net.BoxSettings
import com.localghost.app.net.Conversation
import com.localghost.app.net.DeviceCert
import androidx.work.WorkInfo
import androidx.work.WorkManager
import com.localghost.app.local.LocalModel
import com.localghost.app.local.ModelStore
import com.localghost.app.local.ModelDownloadWorker
import com.localghost.app.net.PhoneModel
import com.localghost.app.ui.ModelRowState
import com.localghost.app.ui.LockScreen
import com.localghost.app.ui.MainShell
import com.localghost.app.ui.PinScreen
import com.localghost.app.ui.SyncUiState
import com.localghost.app.ui.theme.LocalGhostTheme
import kotlinx.coroutines.Job
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.launch

private sealed interface Screen {
    data class Crash(val report: String) : Screen
    data object Setup : Screen
    data object Scan : Screen
    data object Gate : Screen
    data object Pin : Screen
    data object Shell : Screen
}

// Do not auto-kick a full sync more than once every 5 minutes, no matter how often the app is
// foregrounded or unlocked. Manual SYNC NOW bypasses this; the 15-min periodic worker is unaffected.
private const val AUTO_SYNC_COOLDOWN_MS = 5 * 60 * 1000L

class MainActivity : ComponentActivity() {

    private var screen by mutableStateOf<Screen>(Screen.Gate)
    private var busy by mutableStateOf(false)
    private var unlockProgress by mutableStateOf<UnlockSnapshot?>(null)
    // Teardown progress shown at the gate while the box spins down after a LOCK.
    private var lockProgress by mutableStateOf<UnlockSnapshot?>(null)
    private var error by mutableStateOf<String?>(null)
    // After a QR scan we keep the decoded link here so the Setup screen can prefill its fields (and keep
    // them if the box rejects us), and track the background enrol's outcome: null = still in flight, true
    // = enrolled, false = failed. These drive the post-animation routing without cutting the success short.
    private var scannedLink by mutableStateOf<EnrollLink?>(null)
    private var scanEnrolOk by mutableStateOf<Boolean?>(null)
    private var sync by mutableStateOf(SyncUiState())

    private val messages = mutableStateListOf<Message>()
    private var streaming by mutableStateOf(false)
    private var chatJob: Job? = null
    private var pendingAttachments by mutableStateOf<List<Attachment>>(emptyList())
    private var permTick by mutableIntStateOf(0)   // bump to recompute PermState on resume
    private val authGate = AuthGate()     // testable lock-decision logic (see AuthGateTest)
    private var chatCaps by mutableStateOf(ChatCapabilities())
    private var forceLocalMode by mutableStateOf(false)   // manual override
    // Local-only mode: the escape hatch when there is no box, or setup/enrol/PIN failed. A limited
    // interface , on-phone models only, no box, no history , so the app is usable regardless.
    private var localOnly by mutableStateOf(false)
    private var localModeActive by mutableStateOf(false)  // box-down or forced, shown in chat
    private var localModelPresent by mutableStateOf(false)
    private var boxReachable by mutableStateOf(true)
    private var conversations by mutableStateOf<List<Conversation>>(emptyList())
    private var activeConvId by mutableStateOf<String?>(null)
    private var allowMobileSyncState by mutableStateOf(false)
    var thinkLevelState by mutableStateOf("")
    var incognitoState by mutableStateOf(false)
    private var currentChatId = 0L
    private val downloadProgress = mutableStateMapOf<String, Pair<Long, Long>>()
    private var installedModels by mutableStateOf<List<String>>(emptyList())
    private var activeModel by mutableStateOf<String?>(null)
    private var offeredModels by mutableStateOf<List<PhoneModel>>(emptyList())
    private var connectors by mutableStateOf<Loadable<List<Connector>>>(Loadable.Loading)
    private var availableDaemons by mutableStateOf<List<String>>(emptyList())
    private var cameraUri: android.net.Uri? = null
    private var pending by mutableStateOf<Loadable<List<PendingNotification>>>(Loadable.Loading)
    private var lifeContext by mutableStateOf<LifeContext?>(null)
    private var memories by mutableStateOf<Loadable<List<MemoryEntry>>>(Loadable.Loading)
    private var daemons by mutableStateOf<Loadable<List<DaemonStatus>>>(Loadable.Loading)
    private var exportState by mutableStateOf<String?>(null)
    private var devices by mutableStateOf<Loadable<List<DeviceInfo>>>(Loadable.Loading)

    private val engine by lazy { SyncEngine(this) }
    private var autoSyncTried = false

    private val imagePerms = arrayOf(
        Manifest.permission.READ_MEDIA_IMAGES,
        Manifest.permission.READ_MEDIA_VIDEO,
        Manifest.permission.READ_MEDIA_VISUAL_USER_SELECTED,
    )

    private val mediaLauncher = registerForActivityResult(
        ActivityResultContracts.RequestMultiplePermissions()
    ) {
        refreshGrants()
        if (hasImages() && !hasLocation()) locationLauncher.launch(Manifest.permission.ACCESS_MEDIA_LOCATION)
        else afterGrants()
    }
    private val locationLauncher = registerForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { refreshGrants(); afterGrants() }
    private val cameraLauncher = registerForActivityResult(
        ActivityResultContracts.TakePicture()
    ) { ok -> if (ok) cameraUri?.let { attach(it, Attachment.Kind.IMAGE) } }

    private val filePicker = registerForActivityResult(
        ActivityResultContracts.GetMultipleContents()
    ) { uris -> uris.forEach { attach(it, Attachment.Kind.IMAGE) } }   // files ingest same path

    private val imagePicker = registerForActivityResult(
        ActivityResultContracts.GetMultipleContents()
    ) { uris -> uris.forEach { attach(it, Attachment.Kind.IMAGE) } }

    private val voicePicker = registerForActivityResult(
        ActivityResultContracts.GetMultipleContents()
    ) { uris -> uris.forEach { attach(it, Attachment.Kind.VOICE) } }

    private val notifLauncher = registerForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        com.localghost.app.net.BoxClient.appCtx = applicationContext
        sync = sync.copy(paused = AppSettings.syncPaused(this))
        thinkLevelState = AppSettings.thinkLevel(this)
        AppLock.ensureKey(this)
        Notifications.ensureChannel(this)
        PollWorker.schedule(this)
        SyncWorker.schedule(this)          // 15-min background sync, Wi-Fi only
        CrashHandler.pending(this)?.let { screen = Screen.Crash(it) }

        // Setup vs use: if the box connection hasn't been enrolled, start at the setup screen.
        // (A pending crash still takes precedence.)
        if (screen !is Screen.Crash && !BoxConfig.isConfigured(this)) {
            screen = Screen.Setup
        }

        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) { ForegroundPoller.run(this@MainActivity) }
        }

        captureShare(intent)
        enableEdgeToEdge(
            statusBarStyle = SystemBarStyle.dark(AndroidColor.TRANSPARENT),
            navigationBarStyle = SystemBarStyle.dark(AndroidColor.TRANSPARENT),
        )
        setContent {
            LocalGhostTheme {
                // Computed once: the phone's own name, used to prefill the device-name field at enrolment.
                val phoneDefault = remember { phoneName() }
                // If the box is slow, the background enrol from a scan can still be running when the 2s
                // success animation ends and we land on Setup. Promote to the gate the moment it succeeds;
                // a failure just leaves the person on Setup with the fields still filled and the error shown.
                LaunchedEffect(scanEnrolOk, screen) {
                    if (screen is Screen.Setup && scanEnrolOk == true) {
                        scannedLink = null; scanEnrolOk = null; screen = Screen.Gate
                    }
                }
                when (val s = screen) {
                    is Screen.Crash -> CrashScreen(s.report) { CrashHandler.clear(this); screen = Screen.Gate }
                    Screen.Setup -> SetupScreen(
                        busy = busy,
                        error = error,
                        prefilledUrl = scannedLink?.baseUrl() ?: "",
                        prefilledCode = scannedLink?.code ?: "",
                        prefilledName = phoneDefault,
                        prefilledFingerprint = scannedLink?.certFingerprint ?: "",
                        onScanQr = { scannedLink = null; error = null; scanEnrolOk = null; screen = Screen.Scan },
                        onEnroll = { url, code, name, fp -> enroll(url, code, name, fp) },
                        onLocalOnly = ::enterLocalOnly,
                    )
                    Screen.Scan -> QrScanScreen(
                        // Fires the instant a valid enrol code is confirmed: start the network enrol NOW so it
                        // overlaps the success animation, and stash the link so Setup can prefill from it.
                        onScanned = { link -> scannedLink = link; enrollFromScan(link) },
                        // Fires only after the full 2s success animation. If the box already answered OK we
                        // skip straight to the gate; otherwise we show Setup (prefilled), where the outcome
                        // watcher above promotes on success or the error surfaces on failure.
                        onProceed = {
                            if (scanEnrolOk == true) { scannedLink = null; scanEnrolOk = null; screen = Screen.Gate }
                            else screen = Screen.Setup
                        },
                        onCancel = { scannedLink = null; error = null; scanEnrolOk = null; screen = Screen.Setup },
                    )
                    Screen.Gate -> LockScreen(error, unlocking = lockProgress != null, progress = lockProgress, onLocalOnly = ::enterLocalOnly, onReenroll = { scannedLink = null; error = null; scanEnrolOk = null; screen = Screen.Scan }) { passBiometric() }
                    Screen.Pin -> PinScreen(busy, error, unlockProgress) { submit(it) }
                    Screen.Shell -> MainShell(
                        messages = messages, streaming = streaming, onSend = ::sendChat, onStopChat = ::stopChat,
                        pendingAttachments = pendingAttachments,
                        onClearAttachment = ::clearAttachment,
                        chatCaps = chatCaps,
                        onChatCaps = { chatCaps = it },
                        localModeActive = localModeActive,
                        forceLocal = forceLocalMode,
                        onForceLocal = { forceLocalMode = it },
                        localModelPresent = localModelPresent,
                        brainLabel = brainLabel(),
                        brainIsBox = brainIsBox(),
                        phoneModels = phoneModelChoices(),
                        onPickBox = { forceLocalMode = false },
                        onPickPhoneModel = { id -> activateModel(id); forceLocalMode = true },
                        catalogModels = offeredModels,
                        modelRowState = ::modelRowState,
                        onDownloadModel = ::downloadModel,
                        onCancelModel = ::cancelDownload,
                        onActivateModel = ::activateModel,
                        onDeleteModel = ::deleteModel,
                        availableDaemons = availableDaemons,
                        onCamera = ::startCamera,
                        onPhotos = { launchForResult(imagePicker, "image/*") },
                        onFiles = { launchForResult(filePicker, "*/*") },
                        onVoice = { launchForResult(voicePicker, "audio/*") },
                        connectors = connectors,
                        onConnect = ::connectConnector,
                        onDisconnect = ::disconnectConnector,
                        permState = run { permTick; capturePermState() },
                        onPermAction = ::onPermAction,
                        pending = pending,
                        lifeContext = lifeContext, memories = memories, daemons = daemons,
                        sync = sync, onSync = ::startSync, onTogglePause = ::toggleSyncPause,
                        onRequestFullAccess = { AppSettings.setEverAskedMedia(this, true); launchForResult(mediaLauncher, imagePerms) },
                        onTestNotification = ::testNotification,
                        allowMobileSync = allowMobileSyncState,
                        thinkLevel = thinkLevelState,
                        onOpenBoxChat = { id -> openBoxChat(id) },
                        onRenameBoxChat = { id, title ->
                            lifecycleScope.launch {
                                BoxClient.renameChat(this@MainActivity, id, title)
                                refreshChats()
                            }
                        },
                        onDeleteBoxChat = { id -> deleteConversation(id.toString()) },
                        incognito = incognitoState,
                        onToggleIncognito = {
                            incognitoState = !incognitoState
                            if (incognitoState) currentChatId = 0L // a fresh incognito thread, nothing to append to
                        },
                        onCycleThink = {
                            val next = when (AppSettings.thinkLevel(this)) {
                                "" -> "brief"; "brief" -> "deep"; else -> ""
                            }
                            AppSettings.setThinkLevel(this, next)
                            thinkLevelState = next
                        },
                        onToggleMobileSync = ::setMobileSync,
                        onToggleMute = ::setMute,
                        boxConnected = !localOnly,
                        onLock = ::lockBox,
                        onExport = ::exportJson,
                        exportState = exportState,
                        onWipe = ::wipeEverything,
                        devices = devices,
                        conversations = conversations,
                        activeConvId = activeConvId,
                        onSelectConversation = ::selectConversation,
                        onNewConversation = ::newConversation,
                        onDeleteConversation = ::deleteConversation,
                    )
                }
            }
        }
    }

    override fun onStart() {
        super.onStart()
        sync = sync.copy(notificationsMuted = NotifyState.isMuted(this))
        maybeAutoPrompt() // back from background on the gate: fire the fingerprint, no tap needed
    }

    override fun onStop() {
        super.onStop()
        autoPrompted = false
        // authGate decides: lock + tear down unless we launched a picker, or a crash is showing.
        // Setup AND Scan survive backgrounding: both are pre-enrolment (the user isn't enrolled yet, so
        // the gate is wrong), and Scan in particular backgrounds the activity itself when the camera
        // permission dialog appears , without this it would lock the user out mid-setup and re-prompt
        // for a fingerprint they have not even set up against a box yet. The rule lives in AuthGate so
        // it is unit-tested (see AuthGateTest.keepForScreen_*).
        val preEnrolment = screen is Screen.Setup || screen is Screen.Scan
        val mustTearDown = authGate.onStop(
            keepCurrentScreen = AuthGate.keepForScreen(preEnrolment, crashShowing = screen is Screen.Crash)
        )
        if (mustTearDown) {
            screen = Screen.Gate; busy = false; error = null; autoSyncTried = false
            tearDownCache()
        }
    }

    // --- chat ---
    private fun sendChat(text: String) {
        // Invariant: one generator at a time. The UI gates sending while streaming, but this is the
        // last line of defence for any path that reaches here anyway , overwriting chatJob without
        // cancelling would leave the old generator appending to the transcript alongside the new one.
        chatJob?.cancel()
        val atts = pendingAttachments
        messages.add(Message(Message.Role.USER, text, attachments = atts))
        pendingAttachments = emptyList()
        streaming = true
        chatJob = lifecycleScope.launch {
            // Route: forced local, or box unreachable -> on-phone model. Else the box.
            val useLocal = forceLocalMode || !BoxClient.reachable(this@MainActivity)
            localModeActive = useLocal
            if (useLocal) generateLocal(text) else generateFromBox(text, atts)
        }
    }

    private suspend fun generateFromBox(text: String, atts: List<Attachment>) {
        var reply = ""
        var reasoning = ""
        var mems: List<String> = emptyList()
        // First image attachment rides the stream as base64 , the box's projector (the same one
        // captioning the archive) answers questions about what it sees. One image for now; the
        // multimodal template takes one cleanly, a gallery takes protocol work.
        val imageB64 = atts.firstOrNull { it.kind == com.localghost.app.chat.Attachment.Kind.IMAGE }?.let { att ->
            try {
                contentResolver.openInputStream(att.uri)?.use { input ->
                    android.util.Base64.encodeToString(input.readBytes(), android.util.Base64.NO_WRAP)
                }
            } catch (e: Exception) {
                android.util.Log.w("LocalGhost", "attachment read failed: ${e.message}"); null
            }
        } ?: ""
        BoxClient.chat(incognito = incognitoState, chatId = if (incognitoState) 0L else currentChatId, messages.toList(), text, activeConvId, atts, chatCaps, imageB64 = imageB64).collect { chunk ->
            when (chunk) {
                is BoxClient.ChatChunk.Memories -> mems = chunk.ids
                is BoxClient.ChatChunk.ChatId -> {
                    currentChatId = chunk.id
                    // Persisted so the conversation survives the PROCESS, not just the box , the box
                    // always kept it; the screen forgot it on every re-unlock.
                    AppSettings.setLastChatId(this@MainActivity, chunk.id)
                    refreshChats() // the adopted chat just moved to the top of the recents
                }
                is BoxClient.ChatChunk.Reasoning -> {
                    // The model thinking, LIVE , the TEXT, not just a count. The bubble renders it
                    // collapsed behind a "thinking… (n)" toggle that streams while expanded; before
                    // the first answer token it doubles as the progress indicator (no more dead
                    // air), and it stays expandable after the answer lands. The indicator string no
                    // longer pollutes msg.text , the markdown renderer only ever sees the answer.
                    reasoning += chunk.text
                    val body = reply // "" until the first real token
                    if (messages.lastOrNull()?.role == Message.Role.GHOST)
                        messages[messages.size - 1] = Message(Message.Role.GHOST, body, mems, reasoning = reasoning)
                    else messages.add(Message(Message.Role.GHOST, body, mems, reasoning = reasoning))
                }
                is BoxClient.ChatChunk.Token -> {
                    reply += chunk.text
                    if (messages.lastOrNull()?.role == Message.Role.GHOST)
                        messages[messages.size - 1] = Message(Message.Role.GHOST, reply, mems, reasoning = reasoning)
                    else messages.add(Message(Message.Role.GHOST, reply, mems, reasoning = reasoning))
                }
                BoxClient.ChatChunk.Done -> streaming = false
            }
        }
    }

    private suspend fun generateLocal(text: String) {
        if (!LocalModel.ensureLoaded(this@MainActivity)) {
            messages.add(Message(Message.Role.GHOST,
                "No box, and no on-phone model installed. I can't answer right now. " +
                "Reconnect to your box, or install a local model for offline replies."))
            streaming = false
            return
        }
        var reply = ""
        LocalModel.generate(text, shouldContinue = { streaming }).collect { piece ->
            reply += piece
            if (messages.lastOrNull()?.role == Message.Role.GHOST)
                messages[messages.size - 1] = Message(Message.Role.GHOST, reply)
            else messages.add(Message(Message.Role.GHOST, reply))
        }
        streaming = false
    }

    private fun buzz(ms: Long = 25) {
        val vib = (getSystemService(Context.VIBRATOR_MANAGER_SERVICE) as? VibratorManager)?.defaultVibrator
        vib?.vibrate(VibrationEffect.createOneShot(ms, VibrationEffect.DEFAULT_AMPLITUDE))
    }

    private fun attach(uri: Uri, kind: Attachment.Kind) {
        val name = queryName(uri) ?: when (kind) {
            Attachment.Kind.IMAGE -> "image"
            Attachment.Kind.VOICE -> "voice-note"
        }
        val a = Attachment(uri, name, kind)
        pendingAttachments = pendingAttachments + a
        // Ingest to the box index now, raw bytes, same path as camera sync so hashes match
        // and the box dedups if camera sync later sweeps the same file.
        lifecycleScope.launch {
            runCatching {
                contentResolver.openInputStream(uri)?.use { BoxClient.ingestAttachment(this@MainActivity, a, it) }
            }
        }
    }

    private fun clearAttachment(a: Attachment) {
        pendingAttachments = pendingAttachments.filterNot { it === a }
    }

    private fun queryName(uri: Uri): String? = runCatching {
        contentResolver.query(uri, null, null, null, null)?.use { c ->
            val i = c.getColumnIndex(android.provider.OpenableColumns.DISPLAY_NAME)
            if (i >= 0 && c.moveToFirst()) c.getString(i) else null
        }
    }.getOrNull()

    /** Capture-permission state for the standing banner. BLOCKED = prompt is dead, settings only. */
    private fun capturePermState(): PermState {
        if (hasImages() && hasVideo()) return PermState.GRANTED
        if (isPartial()) return PermState.GRANTED   // user picked specific items; sync works on those
        val canPrompt = shouldShowRequestPermissionRationale(Manifest.permission.READ_MEDIA_IMAGES)
        // After a denial, rationale=true means we can still prompt; false + not-granted = blocked,
        // UNLESS we've never asked. We treat never-asked as DENIED (prompt works the first time).
        val everAsked = AppSettings.everAskedMedia(this)
        return if (!canPrompt && everAsked) PermState.BLOCKED else PermState.DENIED
    }

    private fun onPermAction() {
        when (capturePermState()) {
            PermState.BLOCKED -> {
                // prompt is dead — deep-link to this app's system settings page
                startActivity(Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
                    Uri.fromParts("package", packageName, null)))
            }
            else -> {
                AppSettings.setEverAskedMedia(this, true)
                launchForResult(mediaLauncher, imagePerms)
            }
        }
    }

    /** Launch something that backgrounds us briefly without triggering the lock. */
    private fun <I> launchForResult(launcher: androidx.activity.result.ActivityResultLauncher<I>, input: I) {
        authGate.expectResult()
        launcher.launch(input)
    }

    private fun startCamera() {
        val file = java.io.File(cacheDir, "capture_${System.currentTimeMillis()}.jpg")
        val uri = androidx.core.content.FileProvider.getUriForFile(
            this, "$packageName.fileprovider", file)
        cameraUri = uri
        launchForResult(cameraLauncher, uri)
    }

    private fun connectConnector(id: String) {
        lifecycleScope.launch {
            BoxClient.connect(id)
            connectors = Loadable.Loaded(BoxClient.connectors(this@MainActivity))
        }
    }

    private fun disconnectConnector(id: String) {
        lifecycleScope.launch {
            BoxClient.disconnect(id)
            connectors = Loadable.Loaded(BoxClient.connectors(this@MainActivity))
        }
    }

    /** Drop all box-fed cache back to Loading and clear chat. The phone owns nothing; on the
     *  next unlock the daemons push a full sync. Called on lock and after wipe/re-key. */
    private fun reattachIfDownloading(id: String) {
        WorkManager.getInstance(this)
            .getWorkInfosForUniqueWork(ModelDownloadWorker.workName(id)).get()
            ?.firstOrNull()?.let { wi ->
                if (wi.state == WorkInfo.State.RUNNING || wi.state == WorkInfo.State.ENQUEUED) {
                    downloadProgress[id] = 0L to 1L   // placeholder until first progress tick
                    observeDownload(id)
                }
            }
    }

    // Full list the box offers, each tagged with whether it's already downloaded here.
    private fun phoneModelChoices(): List<Triple<String, String, Boolean>> =
        offeredModels.map { m -> Triple(m.id, m.name, installedModels.contains(m.id)) }

    private fun brainIsBox(): Boolean = !forceLocalMode && boxReachable

    private fun brainLabel(): String = when {
        brainIsBox() -> "the box"
        else -> {
            val id = activeModel
            val name = offeredModels.firstOrNull { it.id == id }?.name ?: "on-phone"
            "phone · $name"
        }
    }

    private fun refreshModels() {
        installedModels = ModelStore.installed(this)
        activeModel = ModelStore.activeId(this) ?: installedModels.firstOrNull()
        localModelPresent = LocalModel.isModelPresent(this)
    }

    private fun modelRowState(id: String): ModelRowState {
        val dl = downloadProgress[id]
        return ModelRowState(
            installed = installedModels.contains(id),
            active = activeModel == id,
            downloading = dl != null,
            downloadedBytes = dl?.first ?: 0L,
            totalBytes = dl?.second ?: 0L,
        )
    }

    private fun downloadModel(id: String) {
        val model = offeredModels.firstOrNull { it.id == id } ?: return
        downloadProgress[id] = 0L to model.sizeBytes
        ModelDownloadWorker.enqueue(this, model.id, model.name, model.sizeBytes, model.sha256)
        observeDownload(id)
    }

    private fun observeDownload(id: String) {
        val wm = WorkManager.getInstance(this)
        wm.getWorkInfosForUniqueWorkLiveData(ModelDownloadWorker.workName(id))
            .observe(this) { infos ->
                val info = infos.firstOrNull() ?: return@observe
                when (info.state) {
                    WorkInfo.State.RUNNING -> {
                        val done = info.progress.getLong(ModelDownloadWorker.P_DONE, 0L)
                        val total = info.progress.getLong(ModelDownloadWorker.P_TOTAL, 0L)
                        if (total > 0) downloadProgress[id] = done to total
                    }
                    WorkInfo.State.SUCCEEDED -> { downloadProgress.remove(id); refreshModels() }
                    WorkInfo.State.FAILED -> { downloadProgress.remove(id); error = "download failed" }
                    WorkInfo.State.CANCELLED -> downloadProgress.remove(id)
                    else -> { /* ENQUEUED / BLOCKED: keep showing queued */ }
                }
            }
    }

    private fun cancelDownload(id: String) {
        ModelDownloadWorker.cancel(this, id)
        downloadProgress.remove(id)
    }

    private fun activateModel(id: String) {
        ModelStore.setActive(this, id)
        LocalModel.unload()   // drop the old handle; next generate loads the new one
        refreshModels()
    }

    private fun deleteModel(id: String) {
        if (activeModel == id) { LocalModel.unload(); ModelStore.setActive(this, null) }
        ModelStore.delete(this, id)
        refreshModels()
    }

    private fun selectConversation(id: String) {
        // Drawer rows are box chats now (/v1/chats) , selection is chatId adoption, same as CHATS.
        streaming = false; chatJob?.cancel()
        id.toLongOrNull()?.let { openBoxChat(it) }
    }

    /** The drawer's recents, from the box's persisted chats. The old source was a STUB (delay(80),
     *  unused params) , the list was backed by nothing. Maps /v1/chats rows into the drawer's row
     *  type; called after unlock, after a stream adopts/updates a chat, and after rename/delete. */
    private fun refreshChats() {
        lifecycleScope.launch {
            val chats = BoxClient.boxChats(this@MainActivity) ?: return@launch
            conversations = chats.map {
                Conversation(it.id.toString(), it.title.ifBlank { "(untitled)" },
                    relativeLabel(it.updatedAt), it.messages.toInt())
            }
        }
    }

    private fun relativeLabel(epochMs: Long): String {
        val d = (System.currentTimeMillis() - epochMs) / 1000
        return when {
            d < 90 -> "just now"
            d < 3600 -> "${d / 60}m ago"
            d < 86_400 -> "${d / 3600}h ago"
            else -> "${d / 86_400}d ago"
        }
    }

    private fun newConversation() {
        streaming = false; chatJob?.cancel()
        messages.clear()
        currentChatId = 0L // latent bug: without this, the "new" chat APPENDED to the old one on the box
        AppSettings.setLastChatId(this, 0L)
        activeConvId = null
        // No create call: a chat comes into existence on the box when the first message streams
        // (chatId adoption). The old create was a stub anyway.
    }

    private fun deleteConversation(id: String) {
        lifecycleScope.launch {
            val cid = id.toLongOrNull() ?: return@launch
            BoxClient.deleteChat(this@MainActivity, cid)
            if (currentChatId == cid) {
                currentChatId = 0L
                AppSettings.setLastChatId(this@MainActivity, 0L)
                messages.clear()
            }
            refreshChats()
        }
    }

    /**
     * Run a single Loadable load, turning any failure into Loadable.Failed instead of letting it
     * throw. Used for the post-unlock loads so one failing endpoint shows its own error line rather
     * than aborting the rest of the screen. The label names the section in the error.
     */
    private suspend fun <T> loadOr(label: String, block: suspend () -> Loadable<T>): Loadable<T> =
        try {
            block()
        } catch (e: Exception) {
            Loadable.Failed("could not load $label")
        }

    // Lock: tell the box to spin the drive down, show the teardown steps ticking through (mirroring
    // the mount so it's visibly a fresh cold start next time), then lock the app UI at the gate. The
    // box call is best-effort , on failure we still lock the app and go to the gate.
    // Enter the on-phone-only interface: no box, no history, chat served by a local model. The escape
    // hatch reachable from Setup (skip enrolment) and from the lock gate (skip the PIN), so a broken or
    // unreachable box, a failed enrol, or a forgotten PIN never leaves the app unusable.
    private fun enterLocalOnly() {
        forceLocalMode = true
        localOnly = true
        error = null
        tearDownCache()          // no history , clean local slate
        localModeActive = true
        localModelPresent = LocalModel.isModelPresent(this)
        screen = Screen.Shell
    }

    private fun lockBox() {
        lifecycleScope.launch {
            val steps = try { BoxClient.lock(this@MainActivity) } catch (_: Exception) { emptyList() }
            screen = Screen.Gate
            if (steps.isNotEmpty()) {
                val acc = mutableMapOf<UnlockStage, StageState>()
                for (s in steps) {
                    acc[s.stage] = s.state
                    lockProgress = UnlockSnapshot.teardown(acc)
                    kotlinx.coroutines.delay(160)
                }
                kotlinx.coroutines.delay(300)
                lockProgress = null
            }
            tearDownCache()
        }
    }

    // tearDownCache drops every piece of unlocked-session state held in app memory , chat, caches,
    // loadables, jobs. Called on lock, on local-only entry, and after re-pair, so nothing from an
    // unlocked session lingers on the phone once the box goes dark.
    private fun tearDownCache() {
        messages.clear()
        pendingAttachments = emptyList()
        lifeContext = null
        memories = Loadable.Loading
        daemons = Loadable.Loading
        pending = Loadable.Loading
        devices = Loadable.Loading
        connectors = Loadable.Loading
        availableDaemons = emptyList()
        conversations = emptyList()
        activeConvId = null
        allowMobileSyncState = false
        localModeActive = false
        streaming = false
        chatJob?.cancel()
    }

    private fun stopChat() {
        chatJob?.cancel()
        streaming = false
    }

    // --- auto-sync on open (silent, Wi-Fi only) ---
    private fun maybeAutoSync() {
        if (autoSyncTried) return
        autoSyncTried = true
        // Cooldown: even across lock/unlock cycles (which reset autoSyncTried), do not kick a fresh
        // full sync more than once every few minutes. Returning to the app should not restart sync ,
        // the periodic 15-min worker and the cursor already keep the box current. A manual SYNC NOW
        // still bypasses this (it calls syncNow directly).
        val now = System.currentTimeMillis()
        val last = AppSettings.lastAutoSyncAt(this)
        if (now - last < AUTO_SYNC_COOLDOWN_MS) return
        val battery = getSystemService(android.os.BatteryManager::class.java)
        val batteryLow = battery?.getIntProperty(android.os.BatteryManager.BATTERY_PROPERTY_CAPACITY)?.let { it in 1..19 } == true
        if (batteryLow) {
            android.util.Log.i("LocalGhost", "auto sync skipped: battery low")
            return
        }
        if (hasImages() && hasLocation() && isUnmetered() && !sync.busy) {
            AppSettings.setLastAutoSyncAt(this, now)
            sync = sync.copy(busy = true, status = "Syncing in the background , you can lock the phone")
            // Auto path: SILENT notification channel , the loud "syncing now" one is for the button.
            com.localghost.app.sync.SyncWorker.syncNow(this, manual = false)
            observeSyncWork()
        }
    }

    private fun isMeteredNow(): Boolean {
        val cm = getSystemService(android.net.ConnectivityManager::class.java) ?: return false
        return cm.isActiveNetworkMetered
    }

    private fun isUnmetered(): Boolean {
        val cm = getSystemService(Context.CONNECTIVITY_SERVICE) as ConnectivityManager
        val caps = cm.getNetworkCapabilities(cm.activeNetwork) ?: return false
        return caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_NOT_METERED)
    }

    // --- notifications ---
    private fun testNotification() {
        if (!Notifications.hasPermission(this)) { notifLauncher.launch(Manifest.permission.POST_NOTIFICATIONS); return }
        Notifications.postBatch(this, listOf(
            PendingNotification("ghost.watchd", "Dog check", "Paul please don't get another dog, 10 is enough."),
            PendingNotification("ghost.cued", "Reflection waiting", "A question is ready when you have a moment."),
            PendingNotification("ghost.shadowd", "Pattern flagged", "Reviewed a message and noticed something."),
        ))
    }

    private fun setMute(muted: Boolean) {
        NotifyState.setMuted(this, muted)             // local cache
        if (!muted) NotifyState.setLastPostedAt(this, 0L) else Notifications.cancelAll(this)
        sync = sync.copy(notificationsMuted = muted)
        lifecycleScope.launch {
            BoxClient.setSettings(this@MainActivity,
                BoxSettings(AppSettings.allowMobileSync(this@MainActivity), muted))
        }
    }

    private fun exportJson() {
        exportState = "exporting from the box…"
        lifecycleScope.launch {
            val json = BoxClient.exportJson(this@MainActivity)
            val file = java.io.File(cacheDir, "localghost-export.json").apply { writeText(json) }
            val uri = androidx.core.content.FileProvider.getUriForFile(
                this@MainActivity, "$packageName.fileprovider", file)
            exportState = "exported · ${json.length} bytes"
            val share = Intent(Intent.ACTION_SEND).apply {
                type = "application/json"
                putExtra(Intent.EXTRA_STREAM, uri)
                addFlags(Intent.FLAG_GRANT_READ_URI_PERMISSION)
            }
            startActivity(Intent.createChooser(share, "Export LocalGhost data"))
        }
    }

    private fun wipeEverything() {
        lifecycleScope.launch {
            // Tell the box to crypto-erase its side (best effort; may be unreachable, which is fine ,
            // the point of clearing the phone is that it stops being a usable key regardless).
            BoxClient.wipeEverything(this@MainActivity)

            // The REAL local clear. This is the revocability the security model depends on: before a
            // risky crossing the phone must stop being a working credential, not merely forget its UI
            // state. So destroy what persists, not just what is in RAM:
            //   - BoxConfig.clear: box URL, device token, name, fingerprint, AND the device cert + key
            //     (DeviceCert stores them as BoxConfig secrets), all in the encrypted prefs.
            //   - the crash log, in case it captured anything.
            // After this the phone cannot reach or authenticate to the box; re-pairing needs a fresh
            // enrolment QR from the box at home, which is the intended cost.
            BoxConfig.clear(this@MainActivity)
            CrashHandler.clear(this@MainActivity)

            tearDownCache()        // in-memory UI state
            screen = Screen.Setup  // unenrolled now, so setup is the correct destination, not the gate
        }
    }

    private fun setMobileSync(allow: Boolean) {
        allowMobileSyncState = allow                  // reactive UI update, instant
        AppSettings.setAllowMobileSync(this, allow)   // local cache
        lifecycleScope.launch(Dispatchers.IO) {       // off the main thread — no UI hang
            SyncWorker.schedule(this@MainActivity)    // reschedule with new constraint
            BoxClient.setSettings(this@MainActivity,
                BoxSettings(allow, NotifyState.isMuted(this@MainActivity)))
        }
    }

    /** Coming back from the background lands on the gate , prompt the fingerprint IMMEDIATELY
     *  instead of making the person tap UNLOCK first. Guarded so the prompt's own lifecycle (it
     *  briefly backgrounds the activity) cannot re-fire it in a loop; a dismissed prompt leaves
     *  the UNLOCK button as the manual fallback. */
    private var autoPrompted = false
    // Text shared into the app (ACTION_SEND) waits here until the gate passes , the share lands
    // before authentication, and posting to the box needs the session regardless.
    private var pendingShare: String? = null

    private fun captureShare(intent: android.content.Intent?) {
        if (intent?.action == android.content.Intent.ACTION_SEND && intent.type == "text/plain") {
            intent.getStringExtra(android.content.Intent.EXTRA_TEXT)?.takeIf { it.isNotBlank() }?.let {
                pendingShare = it
            }
        }
    }

    override fun onNewIntent(intent: android.content.Intent) {
        super.onNewIntent(intent)
        captureShare(intent)
        flushShare()
    }

    private fun flushShare() {
        val text = pendingShare ?: return
        if (screen !is Screen.Shell) return // gate first; retried after unlock
        pendingShare = null
        lifecycleScope.launch {
            val ok = BoxClient.noteAdd(this@MainActivity, text)
            android.widget.Toast.makeText(this@MainActivity,
                if (ok) "sent to your journal" else "box unreachable , note not sent",
                android.widget.Toast.LENGTH_SHORT).show()
        }
    }
    private fun maybeAutoPrompt() {
        if (screen is Screen.Gate && !autoPrompted) {
            // Prompt-first: coming to the foreground on the gate goes STRAIGHT to authentication ,
            // fingerprint sheet, or the device PIN/pattern sheet for people without biometrics
            // enrolled (DEVICE_CREDENTIAL is already in the allowed set), or silently through to
            // the box PIN inside the 10s device-unlock window. The gate screen is only ever SEEN
            // after a cancel, where its UNLOCK button is the retry.
            autoPrompted = true
            passBiometric()
        }
        if (screen !is Screen.Gate) autoPrompted = false
        // Re-arm when the app leaves the foreground, so the NEXT foregrounding prompts again ,
        // the old once-per-process flag meant backgrounding and returning showed a dead gate.
    }

    // --- auth ---
    /** Load a persisted conversation from the box into the chat view. Messages arrive newest-first;
     *  reversed for display. Adopting the chatId means the next question APPENDS to this chat on
     *  the box , continuation, not a fork. Incognito switches off: you are inside a saved chat. */
    private fun openBoxChat(id: Long) {
        lifecycleScope.launch {
            val msgs = BoxClient.boxChatMessages(this@MainActivity, id) ?: run {
                android.util.Log.w("LocalGhost", "box chat $id failed to load"); return@launch
            }
            messages.clear()
            msgs.asReversed().forEach { m ->
                messages.add(Message(
                    if (m.role == "user") Message.Role.USER else Message.Role.GHOST, m.content))
            }
            currentChatId = id
            AppSettings.setLastChatId(this@MainActivity, id)
            incognitoState = false
        }
    }

    private fun passBiometric() {
        error = null
        if (!AppLock.deviceAuthAvailable(this)) { screen = Screen.Pin; return }
        // RECENT DEVICE UNLOCK SKIPS THE PROMPT. The gate key carries a 10s auth window, and the
        // phone's own lockscreen unlock opens it , so "unlocked my phone onto the app" goes straight
        // to the box PIN with zero extra taps and zero extra fingerprints. The OS vouches for the
        // recency (the cipher only inits inside the window); nothing here trusts a timestamp we
        // recorded ourselves.
        if (AppLock.tryGateCipher() != null) { screen = Screen.Pin; return }
        // Outside the window: the windowed-key prompt pattern , authenticate WITHOUT a CryptoObject
        // (duration-bound keys do not do per-use crypto binding), then retry the cipher, which the
        // just-completed authentication now allows.
        BiometricPrompt.Builder(this)
            .setTitle("Unlock LocalGhost").setSubtitle("Authenticate to enter your code")
            .setAllowedAuthenticators(BIOMETRIC_STRONG or DEVICE_CREDENTIAL).build()
            .authenticate(CancellationSignal(), mainExecutor,
                object : BiometricPrompt.AuthenticationCallback() {
                    override fun onAuthenticationSucceeded(r: BiometricPrompt.AuthenticationResult) {
                        if (AppLock.tryGateCipher() != null) screen = Screen.Pin
                        else error = "authentication did not open the gate , try again"
                    }
                    override fun onAuthenticationError(code: Int, msg: CharSequence) { error = msg.toString() }
                })
    }

    // A friendly default for THIS phone's device name at enrolment: the name the user gave the phone
    // (Settings > About > Device name , the same one Bluetooth and the hotspot use) if it is set, else
    // the hardware make/model. This is a read-only global setting: no permission, and nothing account-
    // or identity-related is touched, so it stays consistent with the local-first, no-surveillance premise.
    private fun phoneName(): String {
        val set = android.provider.Settings.Global.getString(contentResolver, android.provider.Settings.Global.DEVICE_NAME)
        if (!set.isNullOrBlank()) return set.trim()
        val make = android.os.Build.MANUFACTURER?.trim().orEmpty().replaceFirstChar { it.uppercase() }
        val model = android.os.Build.MODEL?.trim().orEmpty()
        val combo = if (make.isBlank() || model.startsWith(make, ignoreCase = true)) model else "$make $model"
        return combo.ifBlank { "phone" }
    }

    // Shared enrol core. Enrolment is one SCAN, no network call: the box generated the device keypair
    // and delivered the cert + key inside the QR (EnrollLink), so we import them into the encrypted
    // store (DeviceCert) , used for mTLS on every later call , and write the box config. url/name may
    // have been edited on the setup screen (e.g. a DDNS host); the cert, key, and fingerprint come from
    // the scanned link. Returns null on success, or a human error string. Navigation is the caller's.
    private suspend fun doEnrollFromLink(link: EnrollLink, url: String, name: String): String? {
        val certPem = link.deviceCertPem
        val keyPem = link.deviceKeyPem
        if (certPem.isNullOrBlank() || keyPem.isNullOrBlank())
            return "this QR carries no device certificate , regenerate the enrolment QR on the box"
        DeviceCert.store(this, certPem, keyPem)
        BoxConfig.write(this, BoxConfig.Config(
            baseUrl = url, deviceToken = "", // no session yet; a token is issued on first PIN unlock
            deviceName = name, certFingerprint = link.certFingerprint))
        return null
    }

    // Typed/confirm path: the cert/key are only ever in the scanned QR, so this enrols from the last
    // scanned link (with any edited url/name), or asks the user to scan if there is none.
    private fun enroll(url: String, code: String, name: String, fingerprint: String) {
        val link = scannedLink
        if (link == null) {
            error = "scan the box QR to enrol , the certificate is delivered in the QR, not entered"
            return
        }
        busy = true; error = null
        lifecycleScope.launch {
            val err = doEnrollFromLink(link, url, name)
            busy = false
            error = err
            if (err == null) { scannedLink = null; scanEnrolOk = null; screen = Screen.Gate }
        }
    }

    // Scan path: enrol in the background while the 2s success animation plays. Deliberately does NOT
    // navigate , it records the outcome in scanEnrolOk so the celebration is never cut short. onProceed
    // (after the animation) and the Setup watcher route on it: success -> gate, failure -> stay on Setup
    // with the fields prefilled and the error shown.
    private fun enrollFromScan(link: EnrollLink) {
        busy = true; error = null; scanEnrolOk = null
        lifecycleScope.launch {
            val err = doEnrollFromLink(link, link.baseUrl(), phoneName())
            busy = false
            error = err
            scanEnrolOk = (err == null)
        }
    }

    private fun submit(pin: String) {
        busy = true; error = null; unlockProgress = UnlockSnapshot.initial()
        lifecycleScope.launch {
            // Stream unlock progress: a hot account fills the stages in instantly, a cold one ticks
            // through them once a second. The view is identical for any account.
            var ok = false
            BoxClient.submitPinStreaming(this@MainActivity, pin).collect { snap ->
                unlockProgress = snap
                if (snap.done) ok = true
                if (snap.failed != null) error = snap.failed
            }
            busy = false; unlockProgress = null
            if (ok) {
                refreshGrants()
                sync = sync.copy(notificationsMuted = NotifyState.isMuted(this@MainActivity))
                if (!Notifications.hasPermission(this@MainActivity))
                    notifLauncher.launch(Manifest.permission.POST_NOTIFICATIONS)
                screen = Screen.Shell
                buzz()
                // Chat continuity: the box kept the conversation; put it back on the screen. Only
                // when the screen is actually empty (a live in-memory chat wins) and not incognito
                // (incognito threads are deliberately not persisted anywhere, including here).
                if (messages.isEmpty() && !incognitoState) {
                    val last = AppSettings.lastChatId(this@MainActivity)
                    if (last > 0) openBoxChat(last)
                }
                refreshChats()
                flushShare()
                maybeAutoSync()
                // Each load is independent. Against the real box one endpoint can fail (a daemon down,
                // a network blip) without the others, so wrap each Loadable load so a failure lands as
                // Loadable.Failed (which every screen renders as an ErrorLine) instead of throwing and
                // aborting the rest , which would leave later sections stuck on Loading forever. The
                // non-Loadable bits below are best-effort and guarded the same way.
                pending = loadOr("pending") { Loadable.Loaded(BoxClient.pollPending(this@MainActivity)) }
                lifeContext = runCatching { BoxClient.lifeContext(this@MainActivity) }.getOrNull()
                memories = loadOr("memories") { Loadable.Loaded(BoxClient.memories(this@MainActivity)) }
                daemons = loadOr("daemons") { Loadable.Loaded(BoxClient.daemonStatuses(this@MainActivity)) }
                devices = loadOr("devices") { Loadable.Loaded(BoxClient.devices(this@MainActivity)) }
                connectors = loadOr("connectors") { Loadable.Loaded(BoxClient.connectors(this@MainActivity)) }
                runCatching {
                    availableDaemons = BoxClient.availableChatDaemons(this@MainActivity)
                    localModelPresent = LocalModel.isModelPresent(this@MainActivity)
                    offeredModels = BoxClient.availableModels(this@MainActivity)
                    boxReachable = BoxClient.reachable(this@MainActivity)
                    refreshChats()
                    allowMobileSyncState = AppSettings.allowMobileSync(this@MainActivity)
                    refreshModels()
                    offeredModels.forEach { m -> reattachIfDownloading(m.id) }
                    val bs = BoxClient.settings(this@MainActivity)
                    AppSettings.setAllowMobileSync(this@MainActivity, bs.allowMobileSync)
                    NotifyState.setMuted(this@MainActivity, bs.notificationsMuted)
                    sync = sync.copy(notificationsMuted = bs.notificationsMuted)
                }
            } else if (error == null) error = "Could not reach your box"
        }
    }

    // --- grants ---
    private fun granted(p: String) =
        ContextCompat.checkSelfPermission(this, p) == PackageManager.PERMISSION_GRANTED
    override fun onResume() { super.onResume(); permTick++; authGate.onResume() }

    private fun hasImages() = granted(Manifest.permission.READ_MEDIA_IMAGES)
    private fun hasVideo() = granted(Manifest.permission.READ_MEDIA_VIDEO)
    private fun hasLocation() = granted(Manifest.permission.ACCESS_MEDIA_LOCATION)
    private fun isPartial() = !hasImages() && granted(Manifest.permission.READ_MEDIA_VISUAL_USER_SELECTED)

    private fun refreshGrants() {
        sync = sync.copy(hasImages = hasImages(), hasVideo = hasVideo(),
            hasLocation = hasLocation(), partial = isPartial())
    }

    // Observe the one-shot background sync so the UI reflects its finish even though the upload runs in
    // the worker, not here. WorkManager's LiveData survives config changes; when the work leaves RUNNING
    // we clear busy and show a done/failed line. Progress detail lives in the foreground notification.
    private var syncObserved = false
    private var runningSyncWork: String? = null
    private fun observeSyncWork() {
        // Observe ONCE. This is called from every manual and auto sync kick; each call previously
        // stacked another LiveData observer on the same unique work, so state writes multiplied with
        // every sync of the session.
        if (syncObserved) return
        syncObserved = true
        val wm = androidx.work.WorkManager.getInstance(this)
        // Observe BOTH sync work names , the button's one-shot AND the 15-minute periodic. Before
        // this, a background periodic run painted NOTHING on the sync screen: the UI only watched
        // the one-shot name, so the screen sat empty while uploads visibly happened in the shade.
        for (workName in listOf("localghost.sync.now", "localghost.sync")) {
        wm.getWorkInfosForUniqueWorkLiveData(workName).observe(this) { infos ->
            val info = infos?.firstOrNull() ?: return@observe
            // MERGE, don't fight: two observers feed one screen, and LiveData emits for the IDLE
            // name too (ENQUEUED periodic, last week's SUCCEEDED one-shot). Without this gate every
            // idle emission wiped the running one's progress , the bar flickered in and out on each
            // item. Rule: while ANY name is RUNNING, only the RUNNING name may paint.
            val running = info.state == androidx.work.WorkInfo.State.RUNNING
            if (running) runningSyncWork = workName
            if (!running && runningSyncWork != null && runningSyncWork != workName) return@observe
            if (!running && runningSyncWork == workName) runningSyncWork = null
            // Live counts published by the worker (setProgressAsync). The worker now publishes the
            // COMPLETE set , both kinds plus the byte meter , on every update, so this observer just
            // paints; no merging of partial updates into stale state, which was how last run's photo
            // numbers stayed frozen mid-bar while this run moved the video ones (three counters
            // apparently racing) and the byte line sat dead at "0KB / measuring…" forever.
            val pTotal = info.progress.getInt("ptotal", -1)
            if (pTotal >= 0) {
                sync = sync.copy(
                    photoDone = info.progress.getInt("pdone", 0), photoTotal = pTotal,
                    videoDone = info.progress.getInt("vdone", 0), videoTotal = info.progress.getInt("vtotal", 0),
                    bytesSent = info.progress.getLong("bytes", 0), bytesTotal = info.progress.getLong("bytestotal", 0),
                    speedBps = info.progress.getDouble("speed", 0.0), etaSeconds = info.progress.getLong("eta", 0),
                )
            } else {
                // Legacy shape (kind + done/total) from an old worker mid-flight across an app update.
                val done = info.progress.getInt("done", -1)
                val total = info.progress.getInt("total", -1)
                val kind = info.progress.getString("kind") ?: "PHOTO"
                if (total > 0) sync = if (kind == "VIDEO")
                    sync.copy(videoDone = done, videoTotal = total)
                else
                    sync.copy(photoDone = done, photoTotal = total)
            }
            when (info.state) {
                androidx.work.WorkInfo.State.SUCCEEDED ->
                    sync = sync.copy(busy = false, isError = false, status = "Sync complete , copies are on your box",
                        bytesSent = 0, bytesTotal = 0, speedBps = 0.0, etaSeconds = 0)
                androidx.work.WorkInfo.State.FAILED ->
                    sync = sync.copy(busy = false, isError = true, status = "Sync failed , it will retry automatically",
                        bytesSent = 0, bytesTotal = 0, speedBps = 0.0, etaSeconds = 0)
                androidx.work.WorkInfo.State.CANCELLED ->
                    sync = sync.copy(busy = false, status = "Sync cancelled",
                        bytesSent = 0, bytesTotal = 0, speedBps = 0.0, etaSeconds = 0)
                else -> {} // ENQUEUED / RUNNING / BLOCKED: keep the "syncing in background" line
            }
        }
        }
    }

    private fun toggleSyncPause() {
        val now = !AppSettings.syncPaused(this)
        AppSettings.setSyncPaused(this, now)
        sync = sync.copy(paused = now)
        android.util.Log.i("LocalGhost", if (now) "sync paused" else "sync resumed")
    }

    // --- sync (manual; allowed on any network) ---
    private fun startSync() {
        // Wi-Fi only BY DEFAULT , the manual button included. Streaming a camera roll over 4G is a
        // bill nobody meant to run up; the "sync over mobile data" toggle in settings is the single
        // explicit opt-in, and it governs every path (periodic and auto via worker constraints,
        // manual here).
        if (!AppSettings.allowMobileSync(this) && isMeteredNow()) {
            sync = sync.copy(status = "on mobile data , enable 'sync over mobile data' in settings, or join Wi-Fi")
            return
        }
        sync = sync.copy(status = null, isError = false)
        if (!hasImages() || !hasVideo()) { AppSettings.setEverAskedMedia(this, true); mediaLauncher.launch(imagePerms); return }
        if (!hasLocation()) { locationLauncher.launch(Manifest.permission.ACCESS_MEDIA_LOCATION); return }
        // Do NOT reset the cursor here. It advances per CONFIRMED (202) upload, so a manual sync
        // RESUMES from the last confirmed item , if 54 of 2932 are already on the box, this continues at
        // 55 instead of re-streaming all 2932 (the box would dedup them by content hash, but re-sending
        // hundreds of MB of already-stored video is pure waste). A genuinely stale cursor is handled by
        // the box's hash dedup as a backstop, not by blowing away progress on every tap.
        android.util.Log.i("LocalGhost", "manual sync: resuming from saved cursor")
        // Run the actual upload in a FOREGROUND WORKER, not the Activity's lifecycleScope. The old path
        // died the instant the screen locked (Activity coroutines are cancelled on background). The
        // worker keeps going, promotes itself to a dataSync foreground service, and shows progress in
        // the notification shade , so a 400MB video finishes whether or not the screen is on.
        sync = sync.copy(busy = true, status = "Syncing in the background , you can lock the phone", isError = false)
        com.localghost.app.sync.SyncWorker.syncNow(this)
        observeSyncWork()
    }

    private fun afterGrants() {
        if (hasImages() && hasLocation()) runSync()
        else sync = sync.copy(status = "Grants incomplete — need camera photos + location.", isError = true)
    }

    private fun runSync() {
        sync = sync.copy(busy = true, status = "Reading camera roll…", isError = false,
            photoTotal = 0, photoDone = 0, videoTotal = 0, videoDone = 0,
            curName = "", curVideoName = "", curVideoRead = 0, curVideoSize = 0)
        lifecycleScope.launch {
            var photos = 0; var videos = 0
            val progress = object : SyncEngine.Progress {
                override fun onStart(kind: MediaKind, total: Int, totalBytes: Long) {
                    sync = if (kind == MediaKind.PHOTO)
                        sync.copy(photoTotal = total, bytesTotal = totalBytes, bytesSent = 0, speedBps = 0.0, etaSeconds = 0)
                    else sync.copy(videoTotal = total, bytesTotal = totalBytes, bytesSent = 0, speedBps = 0.0, etaSeconds = 0)
                }
                override fun onItemStart(kind: MediaKind, name: String, index: Int, total: Int, size: Long) {
                    sync = sync.copy(curName = name)
                    if (kind == MediaKind.VIDEO) sync = sync.copy(curVideoName = name, curVideoRead = 0, curVideoSize = size)
                }
                override fun onItemBytes(kind: MediaKind, read: Long, size: Long, runBytesSent: Long, speedBps: Double, etaSeconds: Long) {
                    sync = sync.copy(bytesSent = runBytesSent, speedBps = speedBps, etaSeconds = etaSeconds)
                    if (kind == MediaKind.VIDEO) sync = sync.copy(curVideoRead = read, curVideoSize = size)
                }
                override fun onItemDone(kind: MediaKind, sent: Int, total: Int) {
                    sync = if (kind == MediaKind.PHOTO) sync.copy(photoDone = sent) else sync.copy(videoDone = sent)
                }
                override fun onDone(result: CommandResult) {
                    if (result.kind == MediaKind.PHOTO) photos = result.itemsSent
                    if (result.kind == MediaKind.VIDEO) videos = result.itemsSent
                }
            }
            engine.runCamera(MediaKind.PHOTO, progress)
            engine.runCamera(MediaKind.VIDEO, progress)
            refreshGrants()
            // itemsSent counts CONFIRMED (202) uploads. "0 confirmed" with items present means the box
            // refused or was unreachable , the per-item reason is in logcat under the LocalGhost tag.
            sync = sync.copy(busy = false, isError = false, curVideoSize = 0,
                status = "Done — $photos photos, $videos videos confirmed on the box")
        }
    }
}
