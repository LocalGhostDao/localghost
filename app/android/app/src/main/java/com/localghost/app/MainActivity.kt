package com.localghost.app

import android.Manifest
import android.os.Build
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
import androidx.compose.runtime.saveable.rememberSaveable
import androidx.compose.runtime.mutableStateListOf
import androidx.compose.runtime.mutableIntStateOf
import androidx.compose.runtime.mutableLongStateOf
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
import com.localghost.app.ui.Grant
import com.localghost.app.ui.WelcomeScreen
import com.localghost.app.ui.SetupScreen
import com.localghost.app.ui.QrScanScreen
import com.localghost.app.ui.Loadable
import com.localghost.app.ui.PermState
import com.localghost.app.net.ChatCapabilities
import com.localghost.app.net.Connector
import com.localghost.app.net.Conversation
import com.localghost.app.net.DeviceCert
import androidx.work.WorkInfo
import androidx.work.WorkManager
import com.localghost.app.local.LocalModel
import com.localghost.app.local.ModelStore
import com.localghost.app.local.PhoneBenchWords
import com.localghost.app.local.ModelDownloadWorker
import com.localghost.app.net.PhoneModel
import com.localghost.app.ui.ModelRowState
import com.localghost.app.ui.DownloadTick
import com.localghost.app.ui.LockScreen
import com.localghost.app.ui.MainShell
import com.localghost.app.ui.PinScreen
import com.localghost.app.ui.SyncUiState
import com.localghost.app.ui.theme.LocalGhostTheme
import kotlinx.coroutines.Job
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.async
import kotlinx.coroutines.launch

private sealed interface Screen {
    data class Crash(val report: String) : Screen
    data object Welcome : Screen
    data object Setup : Screen
    data object Scan : Screen
    data object Gate : Screen
    data object Pin : Screen
    data object Shell : Screen
}

// Do not auto-kick a full sync more than once every 5 minutes, no matter how often the app is
// foregrounded or unlocked. Manual SYNC NOW bypasses this; the 15-min periodic worker is unaffected.
private const val AUTO_SYNC_COOLDOWN_MS = 5 * 60 * 1000L
// the screen stays on this long after the last touch while the app is open
private const val SCREEN_ON_MS = 60_000L

class MainActivity : ComponentActivity() {

    private var screen by mutableStateOf<Screen>(Screen.Gate)
    private var busy by mutableStateOf(false)
    private var unlockProgress by mutableStateOf<UnlockSnapshot?>(null)
    // Teardown progress shown at the gate while the box spins down after a LOCK.
    private var lockProgress by mutableStateOf<UnlockSnapshot?>(null)
    // the vault rings' last move: the iris opening at READY, the monitor switching off at LOCKED
    private var vaultOpening by mutableStateOf(false)
    private var vaultClosing by mutableStateOf(false)
    private var error by mutableStateOf<String?>(null)
    // After a QR scan we keep the decoded link here so the Setup screen can prefill its fields (and keep
    // them if the box rejects us), and track the background enrol's outcome: null = still in flight, true
    // = enrolled, false = failed. These drive the post-animation routing without cutting the success short.
    private var scannedLink by mutableStateOf<EnrollLink?>(null)
    private var scanEnrolOk by mutableStateOf<Boolean?>(null)
    private var sync by mutableStateOf(SyncUiState())

    private val messages = mutableStateListOf<Message>()
    private var streaming by mutableStateOf(false)
    // when the chat was last written to (a question sent, an answer finished): a question from HOME
    // continues the chat while it is recent, else starts a new one
    private var chatTouchedMs by mutableLongStateOf(0L)
    private var pendingNav by mutableStateOf("")   // set by notification tap, consumed post-unlock
    private var genStartMs = 0L
    private var genChars = 0
    var lastGenStats by mutableStateOf("")
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
    private var trailJob: kotlinx.coroutines.Job? = null
    // the phone model's load state, mirrored for the chat's model pill (loading… / ready)
    private var phoneModelState by mutableStateOf(LocalModel.State.ABSENT)
    private var boxReachable by mutableStateOf(true)
    private var conversations by mutableStateOf<List<Conversation>>(emptyList())
    private var activeConvId by mutableStateOf<String?>(null)
    private var allowMobileSyncState by mutableStateOf(false)
    var thinkLevelState by mutableStateOf("")
    var incognitoState by mutableStateOf(false)
    private var currentChatId = 0L
    private val downloadProgress = mutableStateMapOf<String, DownloadTick>()
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

    // The welcome chain: every permission the app wants, asked one group after another from one
    // launcher. Android will not show two dialogs at once, and background location may only be
    // asked once foreground location is held (11+ sends the person to a settings page for it), so
    // the groups run in sequence and each result launches the next. Done fires when the queue is
    // empty, whatever was granted.
    private val permChain = ArrayDeque<Array<String>>()
    private var permChainDone: (() -> Unit)? = null
    private var permAsking by mutableStateOf(false)
    private val chainLauncher = registerForActivityResult(
        ActivityResultContracts.RequestMultiplePermissions()
    ) { permTick++; nextInChain() }

    private fun nextInChain() {
        while (permChain.isNotEmpty()) {
            val group = permChain.removeFirst().filter { !granted(it) }.toTypedArray()
            if (group.isEmpty()) continue
            if (group.contains(Manifest.permission.ACCESS_BACKGROUND_LOCATION) &&
                !com.localghost.app.sync.LocationLog.hasPermission(this)) continue
            if (group.contains(Manifest.permission.ACCESS_MEDIA_LOCATION) && !hasImages()) continue
            chainLauncher.launch(group)
            return
        }
        permAsking = false
        permChainDone?.invoke()
        permChainDone = null
    }

    private fun startWelcomeGrants() {
        if (permAsking) return
        permChain.clear()
        if (Build.VERSION.SDK_INT >= 33) permChain.add(arrayOf(Manifest.permission.POST_NOTIFICATIONS))
        permChain.add(imagePerms)
        permChain.add(arrayOf(Manifest.permission.ACCESS_MEDIA_LOCATION))
        permChain.add(arrayOf(Manifest.permission.ACCESS_FINE_LOCATION, Manifest.permission.ACCESS_COARSE_LOCATION))
        if (Build.VERSION.SDK_INT >= 29) permChain.add(arrayOf(Manifest.permission.ACCESS_BACKGROUND_LOCATION))
        permChain.add(arrayOf(Manifest.permission.CAMERA))
        permChain.add(arrayOf(Manifest.permission.RECORD_AUDIO))
        AppSettings.setEverAskedMedia(this, true)
        AppSettings.setWelcomeAsked(this, true)
        permAsking = true
        permChainDone = { refreshGrants() }
        nextInChain()
    }

    /** The welcome rows, recomputed whenever a grant changes (permTick). */
    private fun welcomeGrants(): List<Grant> {
        // Any of the group counts as on: coarse-only location, or partial photo access, is a
        // choice the person made, not a failure. BLOCKED = asked before and the OS will not show
        // the dialog again; the row says so and GRANT ACCESS cannot help, only settings can.
        fun state(vararg perms: String): PermState = when {
            perms.any { granted(it) } -> PermState.GRANTED
            AppSettings.welcomeAsked(this) && perms.none { shouldShowRequestPermissionRationale(it) } -> PermState.BLOCKED
            else -> PermState.DENIED
        }
        val rows = mutableListOf<Grant>()
        if (Build.VERSION.SDK_INT >= 33) rows += Grant("◈", "notifications",
            "the phrase on your lock screen, and the box when it has something to say",
            state(Manifest.permission.POST_NOTIFICATIONS))
        rows += Grant("◈", "location",
            "the trail: where you were, a point every quarter hour, and the language around you",
            state(Manifest.permission.ACCESS_FINE_LOCATION, Manifest.permission.ACCESS_COARSE_LOCATION))
        if (Build.VERSION.SDK_INT >= 29) rows += Grant("◈", "location, always",
            "the trail keeps going with the app closed; 'while using' stops it the moment you leave",
            state(Manifest.permission.ACCESS_BACKGROUND_LOCATION))
        rows += Grant("◈", "photos & videos",
            "synced to your box and indexed there, nowhere else",
            state(Manifest.permission.READ_MEDIA_IMAGES, Manifest.permission.READ_MEDIA_VISUAL_USER_SELECTED))
        rows += Grant("◈", "camera", "to scan the codes on your box", state(Manifest.permission.CAMERA))
        rows += Grant("◈", "microphone",
            "voice notes to your check-in, and questions asked aloud; heard on your box, nowhere else",
            state(Manifest.permission.RECORD_AUDIO))
        return rows
    }

    /** CONTINUE on the welcome screen: apply the trail switch, settle home (the SIM's country, for
     *  the phrases to know when you are away), start what needs no box, move on. */
    private fun finishWelcome(trail: Boolean, noBox: Boolean) {
        com.localghost.app.phrases.PhraseOffer.settleHome(this)
        AppSettings.setLocationTrail(this, trail)
        AppSettings.setOnboarded(this, true)
        Thread { com.localghost.app.phrases.PhraseSurface.refresh(applicationContext); com.localghost.app.phrases.PhraseOffer.check(applicationContext) }.start()
        if (com.localghost.app.sync.LocationLog.active(this)) {
            com.localghost.app.sync.LocationLog.schedule(this)
            com.localghost.app.sync.LocationLog.sampleNow(this)
        }
        refreshGrants()
        if (noBox) { enterLocalOnly(); return }
        screen = if (BoxConfig.isConfigured(this)) Screen.Gate else Screen.Setup
    }

    override fun onCreate(savedInstanceState: Bundle?) {
        super.onCreate(savedInstanceState)
        // No picture of the app in RECENTS, and none shown while it comes back: Android keeps a
        // screenshot of the last frame and draws it until the app's first new frame, which flashed
        // the unlocked screen (chat, memories) before the gate. The app locks itself on stop; with
        // this the phone shows a plain card instead of what was on screen.
        setRecentsScreenshotEnabled(false)
        intent?.getStringExtra("nav")?.let { pendingNav = it }
        com.localghost.app.net.BoxClient.appCtx = applicationContext
        sync = sync.copy(paused = AppSettings.syncPaused(this))
        thinkLevelState = AppSettings.thinkLevel(this)
        AppLock.ensureKey(this)
        Notifications.ensureChannel(this)
        // ghost.phrased: redraw the lock-screen card and widget for this hour (no-op when both are
        // off) and arm the next refresh. Runs outside the security gate on purpose , a phrase on the
        // lock screen is the point, and none of it touches the box.
        com.localghost.app.phrases.PhraseSurface.ensureChannel(this)
        Thread {
            com.localghost.app.phrases.PhraseSurface.refresh(applicationContext) // parses the packs; not on the UI thread
            // Somewhere new since last time? The phrases offer themselves, once per country.
            if (AppSettings.onboarded(applicationContext)) com.localghost.app.phrases.PhraseOffer.check(applicationContext)
        }.start()
        PollWorker.schedule(this)
        SyncWorker.schedule(this)          // 15-min background sync, Wi-Fi only
        com.localghost.app.sync.HealthSync.schedule(this) // the last week of Health Connect, every six hours
        com.localghost.app.sync.BoxFetch.schedule(this)   // the news feeds and the tickers, hourly, for the box
        CrashHandler.pending(this)?.let { screen = Screen.Crash(it) }
        // what a killed app left in its cache (a capture in flight is minutes old at most)
        Thread { com.localghost.app.security.CacheSweep.sweep(cacheDir, minAgeMs = 10 * 60_000L) }.start()

        // Welcome first, once: every permission asked before any code is scanned, and the two
        // things that need no box (the lock-screen phrase, the trail) switched on. Then setup vs
        // use: no enrolled box means the setup screen. (A pending crash still takes precedence.)
        if (screen !is Screen.Crash && !AppSettings.onboarded(this)) {
            screen = Screen.Welcome
        } else if (screen !is Screen.Crash && !BoxConfig.isConfigured(this)) {
            screen = Screen.Setup
            // A tap on the lock-screen card with no box enrolled lands on the phrases, not on a
            // form asking for a box that does not exist.
            if (pendingNav == "phrases") enterLocalOnly()
        }
        if (com.localghost.app.sync.LocationLog.active(this)) {
            com.localghost.app.sync.LocationLog.schedule(this)
        }
        com.localghost.app.local.MapPrefetch.schedule(this) // only when "download maps" is ticked
        com.localghost.app.update.ServerUpdates.schedule(this) // the mirror once a day on Wi-Fi: a newer server release?
        lifecycleScope.launch { LocalModel.stateFlow.collect { phoneModelState = it } }

        lifecycleScope.launch {
            repeatOnLifecycle(Lifecycle.State.STARTED) { ForegroundPoller.run(this@MainActivity) { screen == Screen.Shell } }
        }

        captureShare(intent)
        enableEdgeToEdge(
            statusBarStyle = SystemBarStyle.dark(AndroidColor.TRANSPARENT),
            navigationBarStyle = SystemBarStyle.dark(AndroidColor.TRANSPARENT),
        )
        setContent {
            com.localghost.app.ui.PrivateTheme { // the theme, and no keyboard learning in any field
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
                    Screen.Welcome -> {
                        var trail by rememberSaveable { mutableStateOf(true) }
                        WelcomeScreen(
                            grants = run { permTick; welcomeGrants() },
                            asking = permAsking,
                            trailOn = trail, onTrail = { trail = it },
                            onGrant = ::startWelcomeGrants,
                            onSettings = ::openAppSettings,
                            onContinue = { finishWelcome(trail, noBox = false) },
                            onNoBox = { finishWelcome(trail, noBox = true) },
                        )
                    }
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
                        enrolOutcome = scanEnrolOk,
                    )
                    Screen.Gate -> LockScreen(error, unlocking = lockProgress != null, progress = lockProgress, onLocalOnly = ::enterLocalOnly, onReenroll = { scannedLink = null; error = null; scanEnrolOk = null; screen = Screen.Scan }, closing = vaultClosing) { passBiometric() }
                    Screen.Pin -> PinScreen(busy, error, unlockProgress, opening = vaultOpening) { submit(it) }
                    Screen.Shell -> MainShell(
                genStats = lastGenStats,
                navRequest = pendingNav, onNavConsumed = { pendingNav = "" },
                        messages = messages, streaming = streaming, onSend = ::sendChat, onStopChat = ::stopChat,
                        chatTouchedMs = chatTouchedMs,
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
                        onPickBox = { forceLocalMode = false; LocalModel.pin(false) },
                        // picked in the chat: loaded now and kept loaded while it is the one talked to
                        onPickPhoneModel = { id -> activateModel(id); forceLocalMode = true; LocalModel.pin(true); LocalModel.preload(this) },
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
                        grants = run { permTick; welcomeGrants() },
                        permAsking = permAsking,
                        onGrantAll = ::startWelcomeGrants,
                        onAppSettings = ::openAppSettings,
                        pending = pending,
                        lifeContext = lifeContext, memories = memories, daemons = daemons,
                        onRefreshDaemons = ::refreshDaemons,
                        sync = sync, onSync = ::startSync, onTogglePause = ::toggleSyncPause,
                        onRequestFullAccess = { AppSettings.setEverAskedMedia(this, true); launchForResult(mediaLauncher, imagePerms) },
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
        // Welcome too: its permission dialogs background the activity one after another.
        val preEnrolment = screen is Screen.Setup || screen is Screen.Scan || screen is Screen.Welcome
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
        chatTouchedMs = System.currentTimeMillis()
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
        // THE PHONE SEARCHES, THE BOX NEVER DOES. When the web mode says so, look the question up
        // here first and hand the findings to the box as labelled context; the answer then draws
        // on the archive and the outside world with the box still never opening a socket to it.
        var web: org.json.JSONArray? = null
        var webHits: List<com.localghost.app.net.WebSearch.Hit> = emptyList()
        val mode = AppSettings.webMode(this)
        // The last fix, when recent, goes to the box and nowhere else: "what's the weather like?"
        // is answered from the box's daily pull of the world's larger places, "anywhere good near
        // here?" against its own map data. The web gets the search words and never a position.
        val fix = com.localghost.app.sync.LocationLog.last(this)?.takeIf { System.currentTimeMillis() / 1000 - it.ts < 6 * 3600 }
        // A status line in the answer's place until the first word: what is happening right now.
        // Every line also joins the TRAIL (steps), which stays under the answer behind a toggle.
        val steps = ArrayList<String>()
        fun step(s: String) {
            val line = com.localghost.app.chat.Trail.line(s)
            if (line.isNotEmpty() && (steps.isEmpty() || steps.last() != line)) steps.add(line)
        }
        fun status(s: String) {
            step(s)
            if (messages.lastOrNull()?.role == Message.Role.GHOST && messages.last().text.isEmpty())
                messages[messages.size - 1] = Message(Message.Role.GHOST, "", status = s, steps = steps.toList())
            else messages.add(Message(Message.Role.GHOST, "", status = s, steps = steps.toList()))
        }
        // THE SMARTER SEARCH: the box's model says first what the question needs and which
        // searches would find it (a few seconds; the phone plans by itself when the box cannot
        // say), the phone runs them and sends each read page's paragraphs, the box keeps the
        // passages that answer. When what was read comes nowhere near the need and the plan had
        // a search left, the box asks for it (ChatChunk.More) and the question is asked again
        // with both rounds' findings , once, never a loop.
        val engine = com.localghost.app.net.WebSearch.Engine(AppSettings.searchEngine(this), AppSettings.braveKey(this))
        // THE MODEL LOADS AFTER THE UNLOCK: a question asked in the first seconds after a cold one
        // waits here, with the load shown in the answer's place, and goes once the model answers
        waitForBoxModel(::status)
        var wantWeb = com.localghost.app.net.WebSearch.shouldSearch(mode, text)
        val planAnswer: BoxClient.PlanAnswer? = if (wantWeb) {
            status("asking your box what to look for…")
            BoxClient.chatPlan(this, text, messages.toList())
        } else null
        val plan = planAnswer?.plan
        // in auto mode the model's "this needs nothing from outside" is final; "on" searches anyway
        if (plan != null && !plan.search && mode != "on") wantWeb = false
        // WHAT THE BOX KEEPS is never searched for, in any mode: a coin's price, the top coins,
        // crypto as a whole, a rate between currencies, the day's headlines. The box puts its own
        // numbers in the answer. Without the box's word (no plan in time) the phone judges alone.
        val boxHas = plan?.boxHas?.takeIf { it.isNotBlank() }
            ?: if (plan == null && com.localghost.app.net.BoxKnows.covers(text)) "the box's own numbers" else null
        if (boxHas != null && wantWeb) {
            wantWeb = false
            status("no web search , the box has this ($boxHas)")
        }
        // Without the box's plan, a follow-up ("how about now?") borrows the question before it,
        // so the phone does not search the three words as they stand.
        val searchText = if (plan == null) com.localghost.app.net.FollowUp.standalone(text,
            messages.filter { it.role == Message.Role.USER }.map { it.text }.dropLast(1)) else text
        val ownNeed = if (plan == null && searchText != text) searchText else ""
        // without the box's plan, auto does not send a question about the person to the web
        if (plan == null && mode == "auto" && com.localghost.app.net.FollowUp.looksPersonal(searchText)) wantWeb = false
        // a follow-up does not carry a private question to the web (auto only)
        if (mode == "auto" && searchText != text && !com.localghost.app.net.FollowUp.mayBorrow(
                messages.filter { it.role == Message.Role.USER }.map { it.text }.dropLast(1),
                com.localghost.app.net.WebSearch::looksFresh)) wantWeb = false
        if (wantWeb) {
            status("searching the web on this phone" + (if (engine.brave) " (Brave)" else "") +
                ((plan?.need?.takeIf { it.isNotBlank() } ?: ownNeed.takeIf { it.isNotBlank() })?.let { " for: $it" } ?: "") + "…")
            webHits = com.localghost.app.net.WebSearch.search(searchText, engine, plan?.first, plan?.need ?: ownNeed)
            // WHO READS THE PAGES: the box on its GPU reads them in seconds and gets the
            // paragraphs; a box on its CPU (or one that did not answer the plan in time) gets the
            // phone's model's notes instead, checked against the pages, with a verbatim quote each.
            val pages = webHits.count { it.kind == "page" }
            val pageTokens = webHits.filter { it.kind == "page" }.sumOf { h -> h.paragraphs.sumOf { it.length } / 4 }
            val route = com.localghost.app.local.PhoneReader.Plan.route(true, planAnswer?.box, plan != null,
                com.localghost.app.local.LocalModel.usable(this), com.localghost.app.local.LocalModel.Speed.promptTps(this),
                com.localghost.app.local.LocalModel.Speed.genTps(this), pages, pageTokens)
            if (route == com.localghost.app.local.PhoneReader.Route.PHONE_READS) {
                val need = plan?.need?.takeIf { it.isNotBlank() } ?: com.localghost.app.net.WebSearch.cleanQuery(searchText)
                val read = com.localghost.app.local.PhoneReader.digest(this, need, webHits) { status(it) }
                webHits = read.hits
                status("${read.digested} of $pages pages read into notes on this phone in ${read.seconds.toInt()} s " +
                    "(the box is " + (if (planAnswer?.box?.known == true) "on its CPU" else "slow to answer") + ") , asking your box…")
            }
            if (webHits.isNotEmpty()) web = com.localghost.app.net.WebSearch.toJson(webHits)
            if (route != com.localghost.app.local.PhoneReader.Route.PHONE_READS) {
                val read = webHits.count { it.paragraphs.isNotEmpty() || it.excerpt.isNotBlank() }
                status(if (webHits.isEmpty()) "nothing found on the web , asking your box…"
                    else "${webHits.size} found, $read read on this phone , asking your box…")
            }
        } else {
            status("asking your box…")
        }
        var round = if (wantWeb) 1 else 0
        var more: List<String>? = null
        while (true) {
            more = null
            BoxClient.chat(incognito = incognitoState, chatId = if (incognitoState) 0L else currentChatId, messages.toList(), text, activeConvId, atts, chatCaps, imageB64 = imageB64, web = web,
                here = fix?.let { it.lat to it.lon }, need = plan?.need ?: ownNeed, round = round,
                spare = if (round == 1) (plan?.spare ?: emptyList()) else emptyList()).collect { chunk ->
                when (chunk) {
                    is BoxClient.ChatChunk.Memories -> mems = chunk.ids
                    is BoxClient.ChatChunk.More -> { more = chunk.queries; status("the box read the findings and wants more: ${chunk.queries.joinToString(" · ")} , searching again…") }
                    is BoxClient.ChatChunk.Status -> if (reply.isEmpty() && reasoning.isEmpty()) status(chunk.text) else step(chunk.text)
                    is BoxClient.ChatChunk.Steps -> chunk.lines.forEach { step(it) }
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
                            messages[messages.size - 1] = Message(Message.Role.GHOST, body, mems, reasoning = reasoning, web = webHits, steps = steps.toList())
                        else messages.add(Message(Message.Role.GHOST, body, mems, reasoning = reasoning, web = webHits, steps = steps.toList()))
                    }
                    is BoxClient.ChatChunk.Token -> {
                        if (genStartMs == 0L) genStartMs = System.currentTimeMillis()
                        genChars += chunk.text.length
                        reply += chunk.text
                        if (messages.lastOrNull()?.role == Message.Role.GHOST)
                            messages[messages.size - 1] = Message(Message.Role.GHOST, reply, mems, reasoning = reasoning, web = webHits, steps = steps.toList())
                        else messages.add(Message(Message.Role.GHOST, reply, mems, reasoning = reasoning, web = webHits, steps = steps.toList()))
                    }
                    BoxClient.ChatChunk.Done -> {
                        if (more != null) return@collect // the second round follows; the bubble stays a status line
                        streaming = false
                        // A stream that ended without a word must not leave "asking your box…" standing forever.
                        if (reply.isEmpty() && reasoning.isEmpty() && messages.lastOrNull()?.role == Message.Role.GHOST && messages.last().text.isEmpty())
                            messages[messages.size - 1] = Message(Message.Role.GHOST, "The box sent no answer , Box Status says whether the model is up.")
                        // DEBUG tok/s , chars/4 approximates tokens well enough for a health readout;
                        // timed from FIRST answer token so model thinking does not dilute the rate.
                        if (genStartMs > 0 && genChars > 0) {
                            val secs = (System.currentTimeMillis() - genStartMs).coerceAtLeast(1) / 1000.0
                            lastGenStats = "≈ %.1f tok/s · %d chars · %.1fs".format(
                                (genChars / 4.0) / secs, genChars, secs)
                        }
                        genStartMs = 0L; genChars = 0
                    }
                }
            }
            val again = more
            if (again == null || round != 1 || !streaming) break
            // round two: the searches the box asked for, all of them, merged with the first round
            val second = com.localghost.app.net.WebSearch.search(searchText, engine, again, plan?.need ?: ownNeed, runAll = true)
            webHits = com.localghost.app.net.WebSearch.merge(webHits, second)
            web = if (webHits.isNotEmpty()) com.localghost.app.net.WebSearch.toJson(webHits) else null
            status("${second.size} more found , asking your box again…")
        round = 2
        }
    }

    /** Waits (up to [ModelWait.BOX_WAIT_MS]) while the box's model loads, showing how far it is.
     *  Returns at once when it is ready or the box cannot say (an older box, a dropped call). */
    private suspend fun waitForBoxModel(status: (String) -> Unit) {
        var m = BoxClient.modelStatus(this) ?: return
        if (m.ready) return
        val t0 = System.currentTimeMillis()
        while (!m.ready) {
            status(com.localghost.app.net.ModelWait.boxLine(m))
            if (System.currentTimeMillis() - t0 > com.localghost.app.net.ModelWait.BOX_WAIT_MS) {
                status(com.localghost.app.net.ModelWait.boxGaveUp(m))
                return
            }
            kotlinx.coroutines.delay(com.localghost.app.net.ModelWait.POLL_MS)
            m = BoxClient.modelStatus(this) ?: return
        }
        status("your box's model is ready , asking your box…")
    }

    private suspend fun generateLocal(text: String) {
        // THE LIFEBOAT: no box (or local forced). The phone's own model answers; when the web mode
        // says so, the phone searches, its model reads the pages into notes, and answers from them.
        fun say(body: String, status: String = "", web: List<com.localghost.app.net.WebSearch.Hit> = emptyList()) {
            val m = Message(Message.Role.GHOST, body, status = status, web = web)
            if (messages.lastOrNull()?.role == Message.Role.GHOST) messages[messages.size - 1] = m else messages.add(m)
        }
        if (!com.localghost.app.local.LocalModel.usable(this)) {
            say(if (!com.localghost.app.local.NativeLlama.ensureLibrary())
                "No box, and this build of the app carries no on-phone model runtime (its llama.cpp pin is not set). I can't answer right now."
            else "No box, and no on-phone model installed. I can't answer right now. " +
                "Reconnect to your box, or download the phone model from it (MODELS in the menu) for replies without it.")
            streaming = false
            return
        }
        if (forceLocalMode) LocalModel.pin(true) // being talked to: keep it
        // loading: the seconds so far against how long this phone's last load took, every second
        val ticker = if (LocalModel.state != LocalModel.State.READY) lifecycleScope.launch {
            val t0 = System.currentTimeMillis()
            val last = LocalModel.Speed.loadMs(this@MainActivity)
            while (true) {
                say("", com.localghost.app.net.ModelWait.phoneLine(System.currentTimeMillis() - t0, last))
                kotlinx.coroutines.delay(1_000)
            }
        } else null
        val loaded = try { com.localghost.app.local.LocalModel.ensureLoaded(this) } finally { ticker?.cancel() }
        if (!loaded) {
            say("The phone's model would not load (${com.localghost.app.local.LocalModel.state.name.lowercase()}). MODELS in the menu says more.")
            streaming = false
            return
        }
        val mode = AppSettings.webMode(this)
        var hits: List<com.localghost.app.net.WebSearch.Hit> = emptyList()
        if (com.localghost.app.net.WebSearch.shouldSearch(mode, text)) {
            say("", "no box , searching the web on this phone…")
            val engine = com.localghost.app.net.WebSearch.Engine(AppSettings.searchEngine(this), AppSettings.braveKey(this))
            // a follow-up ("how about now?") searched with the question before it
            val earlier = messages.filter { it.role == Message.Role.USER }.map { it.text }.dropLast(1)
            val q = com.localghost.app.net.FollowUp.standalone(text, earlier)
            // a follow-up does not carry a private question to the web (auto only)
            val found = if (mode == "auto" && (com.localghost.app.net.FollowUp.looksPersonal(q) ||
                    (q != text && !com.localghost.app.net.FollowUp.mayBorrow(earlier, com.localghost.app.net.WebSearch::looksFresh)))) emptyList()
                else com.localghost.app.net.WebSearch.search(q, engine)
            if (found.isNotEmpty()) {
                val read = com.localghost.app.local.PhoneReader.digest(this, com.localghost.app.net.WebSearch.cleanQuery(text), found) { say("", it) }
                hits = read.hits
                say("", "${read.digested} pages read on this phone , answering…", hits)
            }
        }
        var reply = ""
        // the conversation so far (the question itself and this answer's placeholder left out)
        val history = messages.dropLast(1).filter { it.text.isNotBlank() }
            .let { if (it.lastOrNull()?.role == Message.Role.USER && it.last().text == text) it.dropLast(1) else it }
            .map { (it.role == Message.Role.USER) to it.text }
        val answer = com.localghost.app.local.PhoneReader.answerAlone(this, text, hits, history) { whole ->
            // called on the model's thread: the transcript is changed on the main one
            reply = whole
            runOnUiThread { say(whole, "", hits) }
            streaming
        }
        // the numbers under the answer: what this phone just did
        val st = LocalModel.last
        val speed = if (answer != null && st != null && st.genMs > 0)
            "on this phone · wrote ${st.genTokens} tokens at ${PhoneBenchWords.tps(st.genTokens, st.genMs)} tok/s · read ${st.promptTokens} at ${PhoneBenchWords.tps(st.promptTokens, st.promptMs)} tok/s"
            else ""
        answer?.let { say(it, speed, hits) }
        if (answer == null && reply.isEmpty()) say("The phone's model gave no answer.")
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

    /** Deep-link to this app's system settings page: the only way past a permission the OS will
     *  no longer ask about. */
    private fun openAppSettings() {
        startActivity(Intent(Settings.ACTION_APPLICATION_DETAILS_SETTINGS,
            Uri.fromParts("package", packageName, null)))
    }

    private fun onPermAction() {
        when (capturePermState()) {
            PermState.BLOCKED -> openAppSettings() // prompt is dead
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
                    downloadProgress[id] = DownloadTick(0L, 1L, at = 0L)   // placeholder until first progress tick
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
            "phone · $name" + when (phoneModelState) {
                LocalModel.State.LOADING -> " · loading…"
                LocalModel.State.READY -> " · ready"
                LocalModel.State.FAILED -> " · did not load"
                else -> ""
            }
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
            downloadedBytes = dl?.done ?: 0L,
            totalBytes = dl?.total ?: 0L,
            bytesPerSecond = dl?.bytesPerSecond ?: -1L,
            secondsLeft = dl?.secondsLeft ?: -1L,
            lastProgressAt = dl?.at ?: 0L,
        )
    }

    private fun downloadModel(id: String) {
        val model = offeredModels.firstOrNull { it.id == id } ?: return
        downloadProgress[id] = DownloadTick(0L, model.sizeBytes, at = 0L)
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
                        val bps = info.progress.getLong(ModelDownloadWorker.P_RATE, -1L)
                        val left = info.progress.getLong(ModelDownloadWorker.P_LEFT, -1L)
                        val prev = downloadProgress[id]
                        if (total > 0) downloadProgress[id] = DownloadTick(done, total, bps, left,
                            if (prev != null && prev.done == done) prev.at else System.currentTimeMillis())
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
        // only a DIFFERENT model drops the loaded one; picking the same one again keeps it
        if (ModelStore.activeId(this) != id) {
            ModelStore.setActive(this, id)
            LocalModel.unload()
        }
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
    /** Box Status's rows, again: called by the screen while it is open. A failed poll keeps the
     *  last good rows (the screen says STALE from the sampler's side) rather than blanking them. */
    private fun refreshDaemons() {
        if (localOnly) return
        lifecycleScope.launch {
            val fresh = loadOr("daemons") { Loadable.Loaded(BoxClient.daemonStatuses(this@MainActivity)) }
            if (fresh is Loadable.Loaded || daemons !is Loadable.Loaded) daemons = fresh
        }
    }

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
        openTrailOnPhone()       // a phone with no box: its own trail, behind its own unlock
        localModeActive = true
        localModelPresent = LocalModel.isModelPresent(this)
        screen = Screen.Shell
    }

    private fun lockBox() {
        lifecycleScope.launch {
            // THE VAULT RINGS GO OUT: the gate at once, every ring lit and the services stopping
            // while the box does the real teardown; then the box's own steps, each putting out a
            // ring from the inside; then the monitor switches off and the gate is back.
            screen = Screen.Gate
            lockProgress = UnlockSnapshot.teardown(emptyMap()) // every ring lit: the iris closes in
            val call = async { try { BoxClient.lock(this@MainActivity) } catch (_: Exception) { emptyList() } }
            kotlinx.coroutines.delay(com.localghost.app.ui.VAULT_ARRIVE_MS.toLong())
            if (!call.isCompleted) lockProgress = UnlockSnapshot.teardown(mapOf(UnlockStage.STOP_SERVICES to StageState.RUNNING))
            val steps = call.await()
            if (steps.isNotEmpty()) {
                val acc = mutableMapOf<UnlockStage, StageState>()
                for (s in steps) {
                    acc[s.stage] = s.state
                    lockProgress = UnlockSnapshot.teardown(acc)
                    kotlinx.coroutines.delay(240)
                }
                kotlinx.coroutines.delay(200)
            }
            // no answer from the box: the phone locks anyway, the rings just switch off
            vaultClosing = true
            kotlinx.coroutines.delay(com.localghost.app.ui.VAULT_CLOSE_MS.toLong() + 50)
            lockProgress = null
            vaultClosing = false
            tearDownCache()
        }
    }

    // tearDownCache drops every piece of unlocked-session state held in app memory , chat, caches,
    // loadables, jobs. Called on lock, on local-only entry, and after re-pair, so nothing from an
    // unlocked session lingers on the phone once the box goes dark.
    private fun tearDownCache() {
        com.localghost.app.sync.TrailKeys.forget() // the trail's key leaves memory with the session
        LocalModel.pin(false) // locked: the weights go after the idle minutes
        com.localghost.app.security.CacheSweep.sweep(cacheDir) // captures, video and voice fetched to play
        com.localghost.app.ui.clearMapMemory() // the map's last view of the box's data
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
        // STOP ends the answer on the box too: closing the app no longer does (the box writes it
        // to the end and saves it), so this is the one way to cut it short
        val cid = currentChatId
        if (streaming && cid > 0) lifecycleScope.launch { BoxClient.chatStop(this@MainActivity, cid) }
        writingJob?.cancel()
        chatJob?.cancel()
        streaming = false
    }

    // --- auto-sync on open (silent, Wi-Fi only) ---
    private fun maybeAutoSync() {
        if (autoSyncTried) return
        autoSyncTried = true
        // The trail's backlog goes first: a few KB of points that may have waited since before
        // this box existed. Its own worker also flushes every quarter hour; this is the moment a
        // session appears, so the map is current when the person opens it.
        lifecycleScope.launch(Dispatchers.IO) {
            trailJob?.join() // sealed points need the box to hold this phone's key first
            com.localghost.app.sync.LocationLog.flush(this@MainActivity)
        }
        // and any voice note still on the phone (made away from home, or a take the app died holding)
        lifecycleScope.launch(Dispatchers.IO) { com.localghost.app.voice.VoiceNotes.uploadPending(this@MainActivity) }
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
    private fun setMute(muted: Boolean) {
        NotifyState.setMuted(this, muted)             // local cache
        if (!muted) NotifyState.setLastPostedAt(this, 0L) else Notifications.cancelAll(this)
        sync = sync.copy(notificationsMuted = muted)
    }

    private fun exportJson() {
        // not built on the box yet; the button is gone from SETTINGS and this says so if anything
        // still calls it (it used to share a made-up file)
        exportState = "export is not built yet"
    }

    private fun wipeEverything() {
        lifecycleScope.launch {
            // Nothing is asked of the box: a crypto-erase of the box is done at the box (secd's
            // resetup), never from a phone, and a stub here used to pretend otherwise.

            // The REAL local clear. This is the revocability the security model depends on: before a
            // risky crossing the phone must stop being a working credential, not merely forget its UI
            // state. So destroy what persists, not just what is in RAM:
            //   - BoxConfig.clear: box URL, device token, name, fingerprint, AND the device cert + key
            //     (DeviceCert stores them as BoxConfig secrets), all in the encrypted prefs.
            //   - the crash log, in case it captured anything.
            // After this the phone cannot reach or authenticate to the box; re-pairing needs a fresh
            // enrolment QR from the box at home, which is the intended cost.
            BoxConfig.clear(this@MainActivity)
            DeviceCert.forget(this@MainActivity) // and the device keys in the Keystore
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
        intent.getStringExtra("nav")?.let { pendingNav = it }
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
            msgs.asReversed().forEach { m -> messages.add(boxMessage(m)) }
            currentChatId = id
            AppSettings.setLastChatId(this@MainActivity, id)
            incognitoState = false
            // the app was closed while the box answered: it kept writing; follow it to the end
            msgs.firstOrNull()?.takeIf { it.role != "user" && it.state == "writing" }?.let { watchWriting(id, it.id) }
        }
    }

    /** A saved message as the chat shows it: the answer with its thinking, its sources, and a
     *  line when it is still being written or was stopped. */
    private fun boxMessage(m: BoxClient.BoxChatMsg): Message = if (m.role == "user") Message(Message.Role.USER, m.content)
        else Message(Message.Role.GHOST, m.content, reasoning = m.reasoning, web = m.sources,
            status = when {
                m.state == "writing" -> "the box is still writing this answer…"
                m.state == "stopped" && m.content.isBlank() -> "stopped before the first word"
                else -> ""
            }) .let { if (m.state == "stopped" && m.content.isNotBlank()) it.copy(text = m.content + "\n\n*(stopped)*") else it }

    private var writingJob: kotlinx.coroutines.Job? = null

    /** Polls the answer the box is still writing (every 1.5 s) and shows it growing, until it is
     *  done, the chat changes, or a new question starts streaming. */
    private fun watchWriting(chatId: Long, msgId: Long) {
        writingJob?.cancel()
        writingJob = lifecycleScope.launch {
            while (true) {
                kotlinx.coroutines.delay(1_500)
                if (currentChatId != chatId || streaming) return@launch
                val newest = BoxClient.boxChatMessages(this@MainActivity, chatId, limit = 1)?.firstOrNull() ?: continue
                if (newest.id != msgId) return@launch
                if (messages.lastOrNull()?.role == Message.Role.GHOST) messages[messages.size - 1] = boxMessage(newest)
                if (newest.state != "writing") return@launch
            }
        }
    }

    private fun passBiometric() {
        error = null
        // a certificate two weeks past its last unlock: the box's door refuses it, so say it here
        // rather than let the PIN fail against a closed door; the re-enrol row below is the way in
        if (DeviceCert.expired(this)) {
            error = "this phone's key ran out: it is renewed once a day while the phone talks to the box, and two weeks went by without that. Scan the box's QR to enrol it again"
            return
        }
        if (!AppLock.deviceAuthAvailable(this)) { screen = Screen.Pin; return }
        // RECENT DEVICE UNLOCK SKIPS THE PROMPT. The gate key carries a 10s auth window, and the
        // phone's own lockscreen unlock opens it , so "unlocked my phone onto the app" goes straight
        // to the box PIN with zero extra taps and zero extra fingerprints. The OS vouches for the
        // recency (the cipher only inits inside the window); nothing here trusts a timestamp we
        // recorded ourselves.
        if (AppLock.tryGateCipher() != null) { openTrailAfterGate(); screen = Screen.Pin; return }
        // Outside the window: the windowed-key prompt pattern , authenticate WITHOUT a CryptoObject
        // (duration-bound keys do not do per-use crypto binding), then retry the cipher, which the
        // just-completed authentication now allows.
        BiometricPrompt.Builder(this)
            .setTitle("Unlock LocalGhost").setSubtitle("Authenticate to enter your code")
            .setAllowedAuthenticators(BIOMETRIC_STRONG or DEVICE_CREDENTIAL).build()
            .authenticate(CancellationSignal(), mainExecutor,
                object : BiometricPrompt.AuthenticationCallback() {
                    override fun onAuthenticationSucceeded(r: BiometricPrompt.AuthenticationResult) {
                        if (AppLock.tryGateCipher() != null) { openTrailAfterGate(); screen = Screen.Pin }
                        else error = "authentication did not open the gate , try again"
                    }
                    override fun onAuthenticationError(code: Int, msg: CharSequence) { error = msg.toString() }
                })
    }

    /** The gate just saw the phone's owner: a trail key still kept on this phone (one made before
     *  the box had it) opens into memory now, inside the Keystore's window, so the PIN unlock that
     *  follows can hand it to the box ([com.localghost.app.sync.TrailKeys]). Off the UI thread. */
    private fun openTrailAfterGate() {
        if (com.localghost.app.sync.TrailKeys.where(this) != "phone") return
        val app = applicationContext
        Thread { com.localghost.app.sync.TrailKeys.onDeviceAuth(app) }.start()
    }

    /**
     * LOCAL-ONLY on a phone whose trail key is kept here (no box has it): the trail opens with the
     * phone's own unlock. Silent when the phone was unlocked in the last 30 s, a prompt otherwise.
     * A phone whose key is in a box's vault never opens its trail here: that takes the box PIN.
     */
    private fun openTrailOnPhone() {
        if (com.localghost.app.sync.TrailKeys.where(this) != "phone" || com.localghost.app.sync.TrailKeys.isOpen()) return
        val app = applicationContext
        Thread {
            if (com.localghost.app.sync.TrailKeys.onDeviceAuth(app)) return@Thread
            runOnUiThread {
                BiometricPrompt.Builder(this)
                    .setTitle("Open your trail").setSubtitle("Where this phone has been is sealed to your phone's lock")
                    .setAllowedAuthenticators(BIOMETRIC_STRONG or DEVICE_CREDENTIAL).build()
                    .authenticate(CancellationSignal(), mainExecutor,
                        object : BiometricPrompt.AuthenticationCallback() {
                            override fun onAuthenticationSucceeded(r: BiometricPrompt.AuthenticationResult) {
                                Thread { com.localghost.app.sync.TrailKeys.onDeviceAuth(app) }.start()
                            }
                        })
            }
        }.start()
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
        // The box sets the pace: a warm box replays one of its own last cold unlocks, step by step
        // (secd, replay.go), so the phone shows what the box streams and adds no floor of its own.
        busy = true; error = null; unlockProgress = UnlockSnapshot.initial()
        holdScreenOn()
        val unlockStart = android.os.SystemClock.elapsedRealtime()
        lifecycleScope.launch {
            // Stream unlock progress: a cold account ticks through its stages once a second, a warm
            // one replays a cold one. The view is identical for any account.
            var ok = false
            BoxClient.submitPinStreaming(this@MainActivity, pin).collect { snap ->
                unlockProgress = snap
                holdScreenOn() // a long unlock must not let the screen go dark
                if (snap.done) ok = true
                if (snap.failed != null) error = snap.failed
            }
            // The screen paces the steps so every ring is readable (UnlockPacer); wait for that floor
            // before the iris, or a warm box that finished in a blink would open before the rings did.
            if (ok) {
                val floorLeft = com.localghost.app.net.UnlockPacer.FLOOR_MS -
                    (android.os.SystemClock.elapsedRealtime() - unlockStart)
                if (floorLeft > 0) kotlinx.coroutines.delay(floorLeft)
            }
            // READY: the vault rings open like an iris before the app appears behind them
            if (ok && screen is Screen.Pin) {
                vaultOpening = true
                kotlinx.coroutines.delay(com.localghost.app.ui.VAULT_OPEN_MS.toLong())
                vaultOpening = false
            }
            busy = false; unlockProgress = null
            // the app went to the background meanwhile and locked itself (onStop): the gate stays
            if (screen !is Screen.Pin) return@launch
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
                // the device key: the phone's own replaces the QR's, once (DeviceCert, secd rekey.go);
                // then the trail's key, fetched from the vault (or handed to it) and held until the
                // app locks. In this order: the trail key is filed on the box under the certificate.
                trailJob = lifecycleScope.launch(Dispatchers.IO) {
                    runCatching { DeviceCert.rotateIfNeeded(this@MainActivity) }
                    com.localghost.app.sync.TrailKeys.onBoxUnlocked(this@MainActivity)
                    // which build the box runs, for the daily check's "a newer release is out"
                    BoxClient.updateStatus(this@MainActivity)?.let {
                        com.localghost.app.update.ServerUpdates.noteBoxVersion(this@MainActivity, it.version)
                        com.localghost.app.update.ServerUpdates.notifyOnce(this@MainActivity)
                    }
                }
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
                    // the two switches are this phone's own (mute, mobile data): nothing on the
                    // box holds them. A stub "box settings" read here used to put both back to
                    // off at every unlock, so mute undid itself and mobile sync went off while
                    // the switch still showed on.
                    sync = sync.copy(notificationsMuted = NotifyState.isMuted(this@MainActivity))
                    // the lock-screen card's home brief, fresh as the app opens
                    com.localghost.app.phrases.HomeBrief.fetch(this@MainActivity)
                }
            } else if (error == null) error = "Could not reach your box"
        }
    }

    // --- grants ---
    private fun granted(p: String) =
        ContextCompat.checkSelfPermission(this, p) == PackageManager.PERMISSION_GRANTED
    override fun onResume() { super.onResume(); permTick++; authGate.onResume(); holdScreenOn() }

    // --- the screen stays on for a minute after the last touch while the app is open ---
    // The phone's own timeout (often 15 or 30 s) is too short to read an answer or watch an unlock.
    // FLAG_KEEP_SCREEN_ON is set on every touch and cleared a minute later; the phone's timeout
    // then applies as usual. Off at once when the app leaves the screen.
    private val screenOnHandler = android.os.Handler(android.os.Looper.getMainLooper())
    private val screenOnEnd = Runnable { window.clearFlags(android.view.WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON) }

    private fun holdScreenOn() {
        window.addFlags(android.view.WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
        screenOnHandler.removeCallbacks(screenOnEnd)
        screenOnHandler.postDelayed(screenOnEnd, SCREEN_ON_MS)
    }

    override fun onUserInteraction() { super.onUserInteraction(); holdScreenOn() }

    override fun onPause() {
        super.onPause()
        screenOnHandler.removeCallbacks(screenOnEnd)
        window.clearFlags(android.view.WindowManager.LayoutParams.FLAG_KEEP_SCREEN_ON)
    }

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
