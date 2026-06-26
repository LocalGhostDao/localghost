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
import com.localghost.app.net.DaemonStatus
import com.localghost.app.net.LifeContext
import com.localghost.app.net.MemoryEntry
import com.localghost.app.net.DeviceInfo
import com.localghost.app.net.DeviceCert
import com.localghost.app.net.PendingNotification
import com.localghost.app.net.UnlockSnapshot
import com.localghost.app.notify.ForegroundPoller
import com.localghost.app.notify.NotifyState
import com.localghost.app.notify.Notifications
import com.localghost.app.notify.PollWorker
import com.localghost.app.security.AppLock
import com.localghost.app.security.AuthGate
import com.localghost.app.security.BoxConfig
import com.localghost.app.security.DeviceIdentity
import com.localghost.app.settings.AppSettings
import com.localghost.app.sync.CommandResult
import com.localghost.app.sync.MediaKind
import com.localghost.app.sync.SyncEngine
import com.localghost.app.sync.SyncWorker
import com.localghost.app.ui.CrashScreen
import com.localghost.app.ui.QrScanScreen
import com.localghost.app.ui.SetupScreen
import com.localghost.app.ui.Loadable
import com.localghost.app.ui.PermState
import com.localghost.app.net.ChatCapabilities
import com.localghost.app.net.Connector
import com.localghost.app.net.BoxSettings
import com.localghost.app.net.Conversation
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

class MainActivity : ComponentActivity() {

    private var screen by mutableStateOf<Screen>(Screen.Gate)
    private var busy by mutableStateOf(false)
    private var unlockProgress by mutableStateOf<UnlockSnapshot?>(null)
    private var error by mutableStateOf<String?>(null)
    private var sync by mutableStateOf(SyncUiState())

    private val messages = mutableStateListOf<Message>()
    private var streaming by mutableStateOf(false)
    private var chatJob: Job? = null
    private var pendingAttachments by mutableStateOf<List<Attachment>>(emptyList())
    private var permTick by mutableIntStateOf(0)   // bump to recompute PermState on resume
    private val authGate = AuthGate()     // testable lock-decision logic (see AuthGateTest)
    private var chatCaps by mutableStateOf(ChatCapabilities())
    private var forceLocalMode by mutableStateOf(false)   // manual override
    private var localModeActive by mutableStateOf(false)  // box-down or forced, shown in chat
    private var localModelPresent by mutableStateOf(false)
    private var boxReachable by mutableStateOf(true)
    private var conversations by mutableStateOf<List<Conversation>>(emptyList())
    private var activeConvId by mutableStateOf<String?>(null)
    private var allowMobileSyncState by mutableStateOf(false)
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
        DeviceIdentity.ensureKey()
        AppLock.ensureKey()
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

        enableEdgeToEdge(
            statusBarStyle = SystemBarStyle.dark(AndroidColor.TRANSPARENT),
            navigationBarStyle = SystemBarStyle.dark(AndroidColor.TRANSPARENT),
        )
        setContent {
            LocalGhostTheme {
                when (val s = screen) {
                    is Screen.Crash -> CrashScreen(s.report) { CrashHandler.clear(this); screen = Screen.Gate }
                    Screen.Setup -> SetupScreen(
                        busy = busy,
                        error = error,
                        onScanQr = { screen = Screen.Scan },
                        onEnroll = { url, code, name, fp -> enroll(url, code, name, fp) },
                    )
                    Screen.Scan -> QrScanScreen(
                        onLink = { link ->
                            // A scanned enrol link gives us the box address, one-time code and the
                            // pinned fingerprint. Enrol straight away, exactly like the typed path.
                            screen = Screen.Setup
                            enroll(link.baseUrl(), link.code, link.boxName.ifBlank { "phone" }, link.certFingerprint)
                        },
                        onCancel = { screen = Screen.Setup },
                    )
                    Screen.Gate -> LockScreen(error, unlocking = false, progress = null) { passBiometric() }
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
                        sync = sync, onSync = ::startSync,
                        onRequestFullAccess = { AppSettings.setEverAskedMedia(this, true); launchForResult(mediaLauncher, imagePerms) },
                        onTestNotification = ::testNotification,
                        allowMobileSync = allowMobileSyncState,
                        onToggleMobileSync = ::setMobileSync,
                        onToggleMute = ::setMute,
                        boxConnected = true,
                        onLock = { tearDownCache(); screen = Screen.Gate },
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
    }

    override fun onStop() {
        super.onStop()
        // authGate decides: lock + tear down unless we launched a picker, or a crash is showing.
        // Setup also survives backgrounding (the user isn't enrolled yet, so the gate is wrong).
        val mustTearDown = authGate.onStop(keepCurrentScreen = screen is Screen.Crash || screen is Screen.Setup)
        if (mustTearDown) {
            screen = Screen.Gate; busy = false; error = null; autoSyncTried = false
            tearDownCache()
        }
    }

    // --- chat ---
    private fun sendChat(text: String) {
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
        var mems: List<String> = emptyList()
        BoxClient.chat(messages.toList(), text, activeConvId, atts, chatCaps).collect { chunk ->
            when (chunk) {
                is BoxClient.ChatChunk.Memories -> mems = chunk.ids
                is BoxClient.ChatChunk.Token -> {
                    reply += chunk.text
                    if (messages.lastOrNull()?.role == Message.Role.GHOST)
                        messages[messages.size - 1] = Message(Message.Role.GHOST, reply, mems)
                    else messages.add(Message(Message.Role.GHOST, reply, mems))
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
                contentResolver.openInputStream(uri)?.use { BoxClient.ingestAttachment(a, it) }
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
        activeConvId = id
        streaming = false; chatJob?.cancel()
        lifecycleScope.launch {
            val msgs = BoxClient.loadConversation(id)
            messages.clear(); messages.addAll(msgs)
        }
    }

    private fun newConversation() {
        streaming = false; chatJob?.cancel()
        messages.clear()
        lifecycleScope.launch {
            activeConvId = BoxClient.createConversation(this@MainActivity)
            conversations = BoxClient.conversations(this@MainActivity)
        }
    }

    private fun deleteConversation(id: String) {
        lifecycleScope.launch {
            BoxClient.deleteConversation(id)
            conversations = BoxClient.conversations(this@MainActivity)
            if (activeConvId == id) { activeConvId = null; messages.clear() }
        }
    }

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
        if (hasImages() && hasLocation() && isUnmetered() && !sync.busy) runSync()
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
            BoxClient.wipeEverything(this@MainActivity)
            // local teardown: drop session, return to lock
            tearDownCache()
            screen = Screen.Gate
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

    // --- auth ---
    private fun passBiometric() {
        error = null
        BiometricPrompt.Builder(this)
            .setTitle("Unlock LocalGhost").setSubtitle("Authenticate to enter your code")
            .setAllowedAuthenticators(BIOMETRIC_STRONG or DEVICE_CREDENTIAL).build()
            .authenticate(BiometricPrompt.CryptoObject(AppLock.gateCipher()),
                CancellationSignal(), mainExecutor,
                object : BiometricPrompt.AuthenticationCallback() {
                    override fun onAuthenticationSucceeded(r: BiometricPrompt.AuthenticationResult) { screen = Screen.Pin }
                    override fun onAuthenticationError(code: Int, msg: CharSequence) { error = msg.toString() }
                })
    }

    private fun enroll(url: String, code: String, name: String, fingerprint: String) {
        busy = true; error = null
        lifecycleScope.launch {
            val result = BoxClient.enroll(url, code, name, fingerprint)
            busy = false
            if (result.ok) {
                // The box issued this device its client cert + key during enrolment. Store them so
                // every later call presents the device cert for mTLS (nginx checks it at the
                // handshake). Without this the phone could reach the box but be rejected at the TLS
                // layer on every authenticated route.
                val certPem = result.deviceCertPem
                val keyPem = result.deviceKeyPem
                if (certPem.isNullOrBlank() || keyPem.isNullOrBlank()) {
                    error = "the box did not return a device certificate; enrolment incomplete"
                    return@launch
                }
                DeviceCert.store(this@MainActivity, certPem, keyPem)
                BoxConfig.write(this@MainActivity, BoxConfig.Config(
                    baseUrl = url, deviceToken = result.deviceToken,
                    deviceName = name, certFingerprint = fingerprint))
                error = null
                screen = Screen.Gate          // enrolled; now authenticate to enter
            } else {
                error = result.error ?: "enrolment failed"
            }
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
                maybeAutoSync()
                pending = Loadable.Loaded(BoxClient.pollPending(this@MainActivity))
                lifeContext = BoxClient.lifeContext(this@MainActivity)
                memories = Loadable.Loaded(BoxClient.memories(this@MainActivity))
                daemons = Loadable.Loaded(BoxClient.daemonStatuses(this@MainActivity))
                devices = Loadable.Loaded(BoxClient.devices(this@MainActivity))
                connectors = Loadable.Loaded(BoxClient.connectors(this@MainActivity))
                availableDaemons = BoxClient.availableChatDaemons(this@MainActivity)
                localModelPresent = LocalModel.isModelPresent(this@MainActivity)
                offeredModels = BoxClient.availableModels(this@MainActivity)
                boxReachable = BoxClient.reachable(this@MainActivity)
                conversations = BoxClient.conversations(this@MainActivity)
                allowMobileSyncState = AppSettings.allowMobileSync(this@MainActivity)
                refreshModels()
                offeredModels.forEach { m -> reattachIfDownloading(m.id) }
                val bs = BoxClient.settings(this@MainActivity)
                AppSettings.setAllowMobileSync(this@MainActivity, bs.allowMobileSync)
                NotifyState.setMuted(this@MainActivity, bs.notificationsMuted)
                sync = sync.copy(notificationsMuted = bs.notificationsMuted)
            } else error = "Could not reach your box"
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

    // --- sync (manual; allowed on any network) ---
    private fun startSync() {
        sync = sync.copy(status = null, isError = false)
        if (!hasImages() || !hasVideo()) { AppSettings.setEverAskedMedia(this, true); mediaLauncher.launch(imagePerms); return }
        if (!hasLocation()) { locationLauncher.launch(Manifest.permission.ACCESS_MEDIA_LOCATION); return }
        runSync()
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
                override fun onStart(kind: MediaKind, total: Int) {
                    sync = if (kind == MediaKind.PHOTO) sync.copy(photoTotal = total) else sync.copy(videoTotal = total)
                }
                override fun onItemStart(kind: MediaKind, name: String, index: Int, total: Int, size: Long) {
                    sync = sync.copy(curName = name)
                    if (kind == MediaKind.VIDEO) sync = sync.copy(curVideoName = name, curVideoRead = 0, curVideoSize = size)
                }
                override fun onItemBytes(kind: MediaKind, read: Long, size: Long) {
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
            sync = sync.copy(busy = false, isError = false, curVideoSize = 0,
                status = "Done — $photos photos, $videos videos (stubbed — no box yet)")
        }
    }
}
