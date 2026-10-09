package com.localghost.app.ui

import android.Manifest
import android.content.pm.PackageManager
import androidx.activity.compose.rememberLauncherForActivityResult
import androidx.activity.result.contract.ActivityResultContracts
import androidx.camera.core.CameraSelector
import androidx.camera.core.FocusMeteringAction
import androidx.camera.core.ImageAnalysis
import androidx.camera.core.ImageProxy
import androidx.camera.lifecycle.ProcessCameraProvider
import androidx.camera.view.PreviewView
import androidx.compose.foundation.Canvas
import androidx.compose.foundation.layout.Arrangement
import androidx.compose.foundation.layout.Row
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.graphics.graphicsLayer
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.*
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Slider
import androidx.compose.material3.SliderDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.input.pointer.pointerInput
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import androidx.compose.ui.viewinterop.AndroidView
import kotlinx.coroutines.Dispatchers
import kotlinx.coroutines.withContext
import androidx.core.content.ContextCompat
import androidx.lifecycle.compose.LocalLifecycleOwner
import com.localghost.app.net.EnrollLink
import com.localghost.app.qr.QrMatrixDecode
import com.localghost.app.qr.QrSampler
import com.localghost.app.ui.theme.*
import androidx.compose.ui.graphics.nativeCanvas
import java.util.concurrent.Executors

/**
 * Camera QR scanner for enrolment. It previews the camera, runs each frame through the sampler +
 * matrix decoder, and on a successful decode of a localghost:// enrol link calls onScanned. If the
 * camera permission is denied or scanning fails, the user falls back to the typed path , a bad scan
 * can never produce a wrong enrolment because the box fingerprint in the link must still match at
 * the TLS pin.
 */
@Composable
fun QrScanScreen(
    onScanned: (EnrollLink) -> Unit,
    onProceed: () -> Unit,
    onCancel: () -> Unit,
    enrolOutcome: Boolean? = null, // the box's answer to the enrol started by onScanned: null while it is still out
) {
    val context = LocalContext.current
    val lifecycleOwner = LocalLifecycleOwner.current
    var granted by remember {
        mutableStateOf(
            ContextCompat.checkSelfPermission(context, Manifest.permission.CAMERA) ==
                PackageManager.PERMISSION_GRANTED
        )
    }
    var status by remember { mutableStateOf("point at the QR on the box") }
    // Detection tick , flashes on EVERY successful decode, repeats included. "I saw it" and
    // "I did something about it" are different facts; the scanner now reports both.
    var tickAt by remember { mutableStateOf(0L) }
    var tickShow by remember { mutableStateOf(false) }
    LaunchedEffect(tickAt) {
        if (tickAt > 0L) {
            tickShow = true
            kotlinx.coroutines.delay(650)
            tickShow = false
        }
    }
    // Live decode diagnostic, surfaced on screen. ScanDiag.last is written by the analyser thread each
    // frame with the exact stage reached (finder count, grid size, "no candidate decoded", "decoded N
    // chars"), so polling it here shows WHERE a code is getting stuck instead of failing silently.
    var diag by remember { mutableStateOf("") }
    var coach by remember { mutableStateOf<String?>(null) } // "move closer" / "hold steady" hint, or null
    LaunchedEffect(Unit) {
        while (true) {
            diag = ScanDiag.last
            // Coaching from the shared streak counter: only after a sustained run of finders-but-no-decode
            // (~1.5s), turn module pixel size into a "move closer / back / focus" hint. Cleared as soon as
            // anything decodes (tryDecode zeroes the streak).
            val streak = com.localghost.app.qr.QrSampler.ScanGeom.noDecodeStreak
            coach = if (streak >= 6) {
                val mod = com.localghost.app.qr.QrSampler.ScanGeom.moduleLenPx
                when {
                    mod in 0.1..3.0 -> "move closer , the code is too small to read"
                    mod > 9.0 -> "move back a little , the code is too big for the frame"
                    else -> "hold steady and tap the code to focus"
                }
            } else null
            kotlinx.coroutines.delay(180)
        }
    }
    // When a readable QR turns out not to be an enrol link, quip holds what it was + a dry line.
    // lastQuipFor debounces it so the same code does not re-fire every frame while it sits in view.
    var quip by remember { mutableStateOf<com.localghost.app.qr.QrGuess?>(null) }
    var lastQuipFor by remember { mutableStateOf<String?>(null) }
    // Geometry of the QR currently in view, for the AR overlay. Null when nothing is detected.
    var overlay by remember { mutableStateOf<Overlay?>(null) }
    // On a valid enrol scan we latch the link here and play the happy-ghost animation. onScanned fires
    // at once to begin enrolling; onProceed fires when the animation ends. Latching also stops the
    // scanner re-triggering on later frames of the same code.
    var foundLink by remember { mutableStateOf<EnrollLink?>(null) }
    val haptic = androidx.compose.ui.platform.LocalHapticFeedback.current
    // Accumulates multi-frame enrolment QRs across camera frames. Remembered so it survives recompositions.
    val frames = remember { com.localghost.app.qr.FrameAssembler() }
    // The erasure-coded set (LGQR2) a current box rotates: any K of its K+M frames complete it.
    val stream = remember { com.localghost.app.qr.StreamAssembler() }
    var frameProgress by remember { mutableStateOf<Pair<Int, Int>?>(null) }
    var capturedFrames by remember { mutableStateOf<Set<Int>>(emptySet()) }
    var frameFlashAt by remember { mutableStateOf(0L) } // timestamp of the last new-frame pulse
    var enrolAnim by remember { mutableStateOf(0f) } // 0..1 over the animation
    // Two-frame confirmation gate. A live scanner runs many decode attempts per frame (8 orientations,
    // several versions and biases); very occasionally one lands a Reed-Solomon miscorrection that is
    // internally consistent but wrong. A wrong decode is worse than no decode here, so we never act on a
    // payload the first time we see it , we require the SAME payload on two decodes before promoting it.
    // A real code repeats frame to frame; a random miscorrection does not. pendingPayload holds the
    // last frame's payload awaiting a match; it is cleared whenever the chain breaks.
    var pendingPayload by remember { mutableStateOf<String?>(null) }
    // Two-rate sampling. HUNTING (no code in view) decodes at most every 500ms , cheap, the common
    // case is pointing at nothing. FOUND (a not-for-us code held in view) decodes every 100ms so the
    // overlay tracks the code and the sad ghost animates smoothly. lastDecodeAt gates the rate;
    // lastSeenAt lets found-mode time out when the code leaves the frame. Plain Longs in remember,
    // read/written only on the analysis thread, so no atomics needed.
    val timing = remember { ScanTiming() }

    val permLauncher = rememberLauncherForActivityResult(
        ActivityResultContracts.RequestPermission()
    ) { ok -> granted = ok }

    LaunchedEffect(Unit) {
        if (!granted) permLauncher.launch(Manifest.permission.CAMERA)
    }

    // On a valid enrol scan: kick the enrol off in the BACKGROUND immediately (onScanned starts the
    // network request in the host), then play a fixed ~2.6s AR success animation over the live camera ,
    // the aperture blows open like the unlock's iris, BOX FOUND snaps in and the address and fingerprint
    // type themselves out. The enrol and the animation run at once, so the time is not dead waiting even
    // though the box can take a moment to answer. Only once it has fully played do we call onProceed to
    // leave the scanner, so the success is always seen for its full length; the host routes on the outcome.
    LaunchedEffect(foundLink) {
        val link = foundLink ?: return@LaunchedEffect
        onScanned(link) // start enrolling now, in the background
        val steps = 52
        for (i in 1..steps) {
            enrolAnim = i.toFloat() / steps
            kotlinx.coroutines.delay(50) // 52 * 50ms = 2600ms
        }
        onProceed() // the open has played , hand off
    }

    // A free clock while a real box is found, for the held ring's faint breathe.
    var celebrate by remember { mutableStateOf(0f) }
    LaunchedEffect(foundLink) {
        while (foundLink != null) {
            celebrate += 0.06f
            kotlinx.coroutines.delay(40)
        }
        celebrate = 0f
    }

    Box(Modifier.fillMaxSize().background(Void)) {
        if (!granted) {
            Column(
                Modifier.fillMaxSize().systemBarsPadding().padding(20.dp),
                horizontalAlignment = Alignment.CenterHorizontally,
            ) {
                SectionLabel("SCAN THE BOX QR")
                Spacer(Modifier.height(12.dp))
                Text("Camera permission is needed to scan. You can also go back and type the box " +
                     "address, code and fingerprint by hand.",
                     color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
                Spacer(Modifier.height(16.dp))
                GhostButton("BACK TO TYPED ENTRY", onCancel, modifier = Modifier.fillMaxWidth())
            }
            return@Box
        }

        val analysisExecutor = remember { Executors.newSingleThreadExecutor() }

        // The PreviewView is created once and kept; the camera is bound to the lifecycle in a
        // LaunchedEffect below, NOT in the view factory. Binding in the factory ran once and never
        // rebound, so after the activity backgrounded (e.g. the camera permission dialog, which stops
        // the activity and tears the camera down) the camera never came back and the screen sat dead.
        // bindToLifecycle with the real lifecycleOwner handles stop/resume itself, so the camera
        // returns when the app does.
        val previewView = remember { PreviewView(context) }
        // The bound Camera, kept so the tap-to-focus gesture and the manual zoom slider drive its
        // CameraControl.
        var camera by remember { mutableStateOf<androidx.camera.core.Camera?>(null) }
        // MANUAL ZOOM. The phone's own zoom used to be automatic , a no-decode streak on a small code
        // zoomed 2x , but it fired when it was not wanted, fought the person, and made a code it had
        // zoomed into look worse. Gone. The person sets the zoom with the slider below; it starts at
        // 1x every time the scanner opens. There is no torch either: no automatic flash, so the light
        // never turns itself on. A dark code is lit by moving to better light or the phone's own
        // system flashlight, not by the scanner deciding for you.
        var zoom by remember { mutableStateOf(1f) }
        var maxZoom by remember { mutableStateOf(1f) }
        // Leaving the screen (a decode, back, the app going away): the camera unbound and the zoom
        // reset, explicitly. bindToLifecycle follows the ACTIVITY's lifecycle, which stays alive when
        // this composable leaves, so without this the camera stayed open until the app backgrounded.
        DisposableEffect(Unit) {
            onDispose {
                runCatching {
                    camera?.cameraControl?.setZoomRatio(1f)
                    ProcessCameraProvider.getInstance(context).get().unbindAll()
                }
                analysisExecutor.shutdown()
            }
        }
        // Tap-to-focus feedback ring: where the last tap landed and its fade clock. tapTick (not the
        // offset) keys the animation so tapping the same spot twice still replays the ring.
        var focusRingAt by remember { mutableStateOf<androidx.compose.ui.geometry.Offset?>(null) }
        var focusRingTick by remember { mutableIntStateOf(0) }
        var focusRingT by remember { mutableStateOf(1f) }
        LaunchedEffect(focusRingTick) {
            if (focusRingAt == null) return@LaunchedEffect
            focusRingT = 0f
            while (focusRingT < 1f) {
                kotlinx.coroutines.delay(30)
                focusRingT += 0.07f
            }
            focusRingAt = null
        }

        LaunchedEffect(granted) {
            if (!granted) return@LaunchedEffect
            val provider = withContext(Dispatchers.IO) {
                ProcessCameraProvider.getInstance(context).get()
            }
            val preview = androidx.camera.core.Preview.Builder().build().also {
                it.surfaceProvider = previewView.surfaceProvider
            }
            // Analysis resolution. A dense code (v9 enrol code is 57 modules, a v11 is 61) needs enough
            // pixels per module for the binariser and finder detection to resolve small modules. 720p gave
            // roughly 7px per module on a code filling the frame, which is why the big ones only decoded in
            // a narrow zoom band. 1080p gives ~50% more linear resolution, widening the workable distance.
            // Each frame is ~2x heavier to binarise and scan, but the frame throttle keeps the rate low, so
            // the extra cost is paid a few times a second, not thirty. If it runs warm, this is the dial.
            // ResolutionStrategy picks the closest the device actually supports.
            val resolutionSelector = androidx.camera.core.resolutionselector.ResolutionSelector.Builder()
                .setResolutionStrategy(
                    androidx.camera.core.resolutionselector.ResolutionStrategy(
                        // 720p, not 1080p , detection cost scales with pixels and the sampler
                        // binarises up to twice a frame (sticky + probe); 2.25x less work per
                        // pass means 2.25x more attempts per second, and the small-module
                        // leniency already covers what the resolution gives up at range.
                        android.util.Size(1280, 720),
                        androidx.camera.core.resolutionselector.ResolutionStrategy.FALLBACK_RULE_CLOSEST_HIGHER_THEN_LOWER,
                    )
                )
                .build()
            val analysis = ImageAnalysis.Builder()
                .setBackpressureStrategy(ImageAnalysis.STRATEGY_KEEP_ONLY_LATEST)
                .setResolutionSelector(resolutionSelector)
                .build()
            analysis.setAnalyzer(analysisExecutor) { proxy ->
                // Two-rate gate. The full pipeline (binarise, finder scan, multi-triple sample, decode) is
                // heavy, and running it flat out heats the phone and then thermally throttles, which is what
                // makes it feel slower over time. So we push HARD only when it matters: once a code has been
                // DETECTED in the recent past (finders found this frame, whether or not it decoded), sample
                // every ~180ms so a stubborn dense code gets many attempts a second and locks fast; when
                // nothing is in view, fall back to ~400ms hunting. The ghost and reticle animate on their own
                // clocks between decodes, so the rate itself is not felt.
                val now = System.currentTimeMillis()
                val codeInView = now - timing.lastDetectAt < DETECT_WINDOW_MS
                // Field-tuned down from 180/400: flat-out sampling heats the phone into thermal
                // throttle, which FEELS like the scanner getting worse the longer you try. 600ms
                // hunting is plenty to notice a code entering view within a blink.
                //
                // A code IN VIEW gets 150ms: the box now holds each rotating frame for ONE second
                // (twelve frames, any eight enough), so the first frame , the one that starts the
                // assembly burst , has about six attempts inside its window instead of the four
                // that 250ms left it. A code in view is the enrolment scan or a stray code held up
                // on purpose, a bounded moment either way, not the hunt the thermal tuning is for.
                //
                // During multi-frame ASSEMBLY, 100ms: capturing the rotating sequence is a burst
                // measured in seconds, ~10 attempts per one-second frame, and with erasure coding a
                // frame that still fails costs one more frame, not a lap. The burst ends when
                // assembly does, so nothing here can cook the phone.
                val assembling = capturedFrames.isNotEmpty() &&
                    frameProgress?.let { it.first < it.second } == true
                val interval = when {
                    assembling -> 100L
                    codeInView -> 150L
                    else -> 600L
                }
                if (now - timing.lastDecodeAt < interval) {
                    proxy.close()
                    return@setAnalyzer
                }
                timing.lastDecodeAt = now
                if (foundLink != null) {
                    // Enrolment already latched. The QR keeps rotating on the box, but there is nothing
                    // left to read , stop decoding entirely so duplicate frames cannot re-enter the parse
                    // path (which was causing the post-completion errors). The found overlay stays up.
                    proxy.close()
                    return@setAnalyzer
                }
                val result = tryDecode(proxy, frames, stream)
                proxy.close()
                // A code is "in view" when this frame either sampled a grid (corners set) or saw at least
                // two finder patterns , the marginal codes that fail to sample are exactly the ones that
                // need more attempts per second, and previously they never opened the fast window at all.
                // Two finders, not one: a single 1:1:3:1:1 coincidence in texture is common, two together
                // almost always means a real code, so the fast rate doesn't burn battery on wallpaper.
                if (com.localghost.app.qr.QrSampler.ScanGeom.corners != null ||
                    com.localghost.app.qr.QrSampler.ScanGeom.findersSeen >= 2) timing.lastDetectAt = now
                when (result) {
                    is ScanResult.Enrol -> {
                        tickAt = System.currentTimeMillis()
                        // Found the box. A CLEAN decode (plain RS, no erasures) that parsed as a valid enrol
                        // link is trustworthy on the first frame: the strict localghost:// pattern plus a
                        // well-formed 64-hex pinned fingerprint make a random miscorrection into a valid link
                        // effectively impossible, and a wrong fingerprint would fail the TLS pin anyway (fails
                        // safe , connection refused, re-scan). So we latch it at once. An erasure-path decode
                        // ("conf"/"logo", e.g. a logo or blurred code) is more willing to manufacture a
                        // consistent-but-wrong payload, so those still require the same payload on two frames.
                        val payload = "${result.link.host}:${result.link.port}:${result.link.code}:${result.link.certFingerprint}"
                        overlay = result.overlay
                        timing.lastSeenAt = now
                        if (foundLink == null) {
                            if (result.clean || pendingPayload == payload) {
                                status = "found ${result.link.host}"
                                quip = null
                                coach = null
                                foundLink = result.link
                            } else {
                                status = "reading…"
                                pendingPayload = payload
                            }
                        }
                    }
                    is ScanResult.NotForUs -> {
                        tickAt = System.currentTimeMillis()
                        // A readable code that is not the way in. Anchor the overlay and let the ghost
                        // orbit it. Only name it once the same payload has been seen twice, so a transient
                        // wrong decode never flashes the wrong opinion. Mark lastSeenAt for the timeout.
                        overlay = result.overlay
                        timing.lastSeenAt = now
                        val payload = result.guess.preview
                        if (pendingPayload == payload) {
                            if (result.guess.preview != lastQuipFor) {
                                lastQuipFor = result.guess.preview
                                quip = result.guess
                            }
                        } else {
                            pendingPayload = payload
                        }
                    }
                    is ScanResult.Frames -> {
                        tickAt = System.currentTimeMillis()
                        coach = null
                        // Mid-capture of a multi-frame identity. Anchor the overlay, show progress, and on
                        // a NEWLY captured frame fire a brief success pulse + haptic so each scan feels
                        // acknowledged. Already-scanned frames just keep their checkmark, no re-pulse.
                        overlay = result.overlay
                        timing.lastSeenAt = now
                        frameProgress = result.have to result.want
                        capturedFrames = result.captured
                        if (result.justCaptured) {
                            frameFlashAt = now
                            haptic.performHapticFeedback(HapticFeedbackType.LongPress)
                            status = "captured ${result.have} of ${result.want}"
                        } else {
                            status = "hold steady , ${result.have} of ${result.want}"
                        }
                    }
                    ScanResult.Nothing -> {
                        // No readable QR this frame. A gap breaks the confirmation chain. In found mode,
                        // keep the ghost for a short grace period (the code may just have blurred for a
                        // frame); once gone past the timeout, drop back to hunting and clear the ghost.
                        pendingPayload = null
                        if (quip != null && now - timing.lastSeenAt > FOUND_TIMEOUT_MS) {
                            quip = null
                            lastQuipFor = null
                            overlay = null
                        } else if (quip == null) {
                            overlay = null
                        }
                    }
                }
            }
            // Bind with a short retry. On a COLD first launch the camera device can still be held by
            // the OS (or the just-granted permission has not fully propagated), and bindToLifecycle
            // throws , which, uncaught, left a dead preview that only worked when you reopened the
            // scanner (second time the camera is free and permission is already settled). This is that
            // "open it twice" bug. A few spaced retries make the first open succeed.
            var bound = false
            var lastErr: Exception? = null
            repeat(5) { attempt ->
                if (bound) return@repeat
                try {
                    provider.unbindAll()
                    camera = provider.bindToLifecycle(
                        lifecycleOwner, CameraSelector.DEFAULT_BACK_CAMERA, preview, analysis
                    )
                    bound = true
                } catch (e: Exception) {
                    lastErr = e
                    kotlinx.coroutines.delay(250L * (attempt + 1)) // 250, 500, 750, 1000ms backoff
                }
            }
            if (!bound) {
                status = "camera busy , tap to retry"
                ScanDiag.last = "camera bind failed: ${lastErr?.javaClass?.simpleName}"
            }
            // the slider's range; start every scan at 1x, no zoom carried over
            maxZoom = camera?.cameraInfo?.zoomState?.value?.maxZoomRatio ?: 1f
            zoom = 1f
            runCatching { camera?.cameraControl?.setZoomRatio(1f) }
        }

        // Full-bleed camera preview. The AR overlays and the text panels float over it. Tapping focuses
        // AND meters at that point: focus fixes close-range blur (a phone screen at 12cm sits at the edge
        // of the lens's comfort zone and hunts), and exposure metering on the tapped spot is the real win
        // for scanning a SCREEN , auto-exposure averages the dark room and blows the bright screen out,
        // crushing exactly the low-contrast grey marks (Samsung's ring finders) that detection needs.
        // previewView.meteringPointFactory maps view coordinates through the preview's own transform, so
        // the tap lands on the right sensor region regardless of crop or rotation. The action auto-cancels
        // back to continuous auto after a few seconds, so a stray tap can never leave the camera stuck.
        AndroidView(
            factory = { previewView },
            modifier = Modifier.fillMaxSize().pointerInput(Unit) {
                detectTapGestures { tap ->
                    val cam = camera ?: return@detectTapGestures
                    val point = previewView.meteringPointFactory.createPoint(tap.x, tap.y)
                    val action = FocusMeteringAction
                        .Builder(point, FocusMeteringAction.FLAG_AF or FocusMeteringAction.FLAG_AE)
                        .setAutoCancelDuration(6, java.util.concurrent.TimeUnit.SECONDS)
                        .build()
                    cam.cameraControl.startFocusAndMetering(action)
                    focusRingAt = tap
                    focusRingTick++
                }
            },
        )

        // Brief expanding ring where the tap landed, so focusing visibly registered.
        run {
            val at = focusRingAt
            if (at != null && focusRingT < 1f) {
                Canvas(Modifier.fillMaxSize()) {
                    drawCircle(
                        color = TerminalGreen.copy(alpha = (1f - focusRingT) * 0.85f),
                        radius = 40f * (1f + 0.5f * focusRingT),
                        center = at,
                        style = androidx.compose.ui.graphics.drawscope.Stroke(width = 3f),
                    )
                }
            }
        }

            // THE AIMING RETICLE, fixed in the centre of the screen: eight segments round a crosshair
            // you point at the QR. It does not chase the code across the frame (that mapping is the
            // fragile, device-specific part), it sits still and you aim , which is what a scanner
            // reticle is for. As the box's rotating frames land the segments fill and the crosshair
            // becomes a padlock, the code shown as a lock as it is read, shut at eight; a code that
            // reads but is not a box turns it red. Spins on its own ~60ms clock.
            var spin by remember { mutableStateOf(0f) }
            LaunchedEffect(granted) {
                while (granted) { spin += 3.2f; kotlinx.coroutines.delay(60) }
            }
            if (foundLink == null) Canvas(Modifier.fillMaxSize()) {
                val cx = size.width / 2f
                val cy = size.height * 0.46f
                val radius = size.minDimension * 0.30f
                val wrong = quip != null
                val have = frameProgress?.first ?: 0
                val want = frameProgress?.second ?: 1
                val lit = if (wrong) 0 else QrApertureModel.litSegments(have, want)
                val tint = if (wrong) AngryRed else TerminalGreen
                // a check flashes on the code each time a frame is read (fades over ~450ms)
                val tick = (1f - (System.currentTimeMillis() - frameFlashAt) / 450f).coerceIn(0f, 1f)
                drawAperture(cx, cy, radius, tint, spin, lit, wrong, tick)
            }


        // Floating title at the top, clear of the status bar and the camera cutout. Sits on a dark
        // rounded pill so the green text stays legible even over a bright or greenish camera image.
        if (foundLink == null) Column(
            Modifier.align(Alignment.TopCenter).fillMaxWidth().statusBarsPadding().padding(20.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            Box(
                Modifier
                    .background(Void.copy(alpha = 0.72f), MaterialTheme.shapes.small)
                    .padding(horizontal = 14.dp, vertical = 7.dp)
            ) {
                SectionLabel("SCAN THE BOX QR")
            }
        }

        // Floating panel at the bottom, over the camera, clear of the nav buttons. A dark gradient
        // scrim sits under the text so it stays legible over a bright camera image.
        if (foundLink == null) Column(
            Modifier.align(Alignment.BottomCenter).fillMaxWidth()
                .background(
                    androidx.compose.ui.graphics.Brush.verticalGradient(
                        listOf(androidx.compose.ui.graphics.Color.Transparent, Void.copy(alpha = 0.82f), Void)
                    )
                )
                .navigationBarsPadding()
                .padding(horizontal = 20.dp, vertical = 16.dp),
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            // Status and live decode diagnostic, on their own dark pill so they read over any camera
            // content. The diagnostic line is the honest feedback: it names the stage the current frame
            // reached, so a code that detects but will not decode says so ("grid found, no candidate
            // decoded") instead of just sitting there silently.
            Column(
                Modifier
                    .fillMaxWidth()
                    .background(Void.copy(alpha = 0.72f), MaterialTheme.shapes.small)
                    .padding(horizontal = 12.dp, vertical = 8.dp)
            ) {
                Text("> $status", color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
                if (tickShow) {
                    Text("  ✓ code read", color = TerminalGreen,
                        style = MaterialTheme.typography.labelMedium)
                }
                // Coaching hint: shown only during a sustained no-decode streak (see the analyser). It is
                // the actionable version of the silent technical diag , tells the person what to DO.
                coach?.let { c ->
                    Spacer(Modifier.height(4.dp))
                    Text("! $c", color = Warning, textAlign = TextAlign.Center,
                        style = MaterialTheme.typography.bodyMedium,
                        modifier = Modifier.fillMaxWidth())
                }
                // The multi-frame progress is shown by the aperture's own segments over the code now,
                // not by a second row of pips down here , one place, less to read.
                // The decoder's own commentary is for debugging (settings › debug mode); a person
                // scanning sees the coaching line above and the frame pips, nothing else.
                if (diag.isNotEmpty() && com.localghost.app.settings.AppSettings.debugMode(context)) {
                    Spacer(Modifier.height(3.dp))
                    Text("· $diag", color = GhostTextDim, style = MaterialTheme.typography.labelSmall)
                }
            }

            // A readable-but-wrong QR: the aperture over it is already red; here, one terse line
            // naming what it was, no more.
            quip?.let { g ->
                Spacer(Modifier.height(10.dp))
                Text("that is ${g.label}, not a box , point at the QR on the box",
                    color = Warning, style = MaterialTheme.typography.bodyMedium,
                    modifier = Modifier.fillMaxWidth().background(VoidLighter, MaterialTheme.shapes.small).padding(12.dp))
            }

            // MANUAL ZOOM. Shown only when the camera can zoom. The person sets it; nothing zooms on
            // its own. Dragging the slider drives the camera at once, and the label reads the ratio.
            if (maxZoom > 1.05f) {
                Spacer(Modifier.height(10.dp))
                Row(verticalAlignment = Alignment.CenterVertically,
                    modifier = Modifier.fillMaxWidth()
                        .background(Void.copy(alpha = 0.72f), MaterialTheme.shapes.small)
                        .padding(horizontal = 12.dp, vertical = 6.dp)) {
                    Text("ZOOM", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
                    Slider(
                        value = zoom, valueRange = 1f..maxZoom,
                        onValueChange = { zoom = it; runCatching { camera?.cameraControl?.setZoomRatio(it) } },
                        colors = SliderDefaults.colors(thumbColor = TerminalGreen, activeTrackColor = TerminalGreen,
                            inactiveTrackColor = GhostBorder),
                        modifier = Modifier.weight(1f).padding(horizontal = 10.dp),
                    )
                    Text("${"%.1f".format(zoom)}x", color = TerminalGreen,
                        fontFamily = androidx.compose.ui.text.font.FontFamily.Monospace,
                        style = MaterialTheme.typography.labelMedium)
                }
            }

            Spacer(Modifier.height(8.dp))
            GhostButton("CANCEL / TYPE INSTEAD", onCancel, modifier = Modifier.fillMaxWidth())
        }

        // FOUND: establishing identity. Not an "opening" , the phone is gaining a signed identity with
        // the box, so the sequence builds that out and hands over to the PIN. One ring, four beads that
        // fill as each step lands, and a small glyph in the middle that changes per step: the box's
        // identity read from the code, a secure channel to it, the device certificate signed, the
        // identity pinned , then a steady "ready , unlock with your PIN". Minimal: the animation carries
        // it, with one quiet line of words. enrolAnim (0..1 over ~2.6s) drives it; celebrate breathes.
        if (foundLink != null) {
            // the machine names the CONNECTION it will reach (the port only when it is not the usual 443)
            val addr = foundLink?.let { l -> if (l.port == 443 || l.port == 0) l.host else "${l.host}:${l.port}" } ?: "the box"
            val a = enrolAnim.coerceIn(0f, 1f)
            val step = QrApertureModel.stepAt(a)
            val ready = QrApertureModel.ready(a)
            // the camera is not needed once the whole code is found: the screen darkens (fast, over
            // the first ~250ms) so the establishing sequence and its words read clearly.
            val scrim = (a / 0.10f).coerceIn(0f, 1f) * 0.94f
            Box(Modifier.fillMaxSize().background(Void.copy(alpha = scrim))) {
                Canvas(Modifier.fillMaxSize()) {
                    val cx = size.width / 2f
                    val cy = size.height * 0.40f
                    val radius = size.minDimension * 0.26f
                    drawEstablish(cx, cy, radius, celebrate, a)
                }
                Column(
                    Modifier.fillMaxSize().systemBarsPadding().padding(24.dp),
                    horizontalAlignment = Alignment.CenterHorizontally,
                ) {
                    Spacer(Modifier.weight(0.60f))
                    val line = when (step) {
                        QrApertureModel.Step.IDENTITY -> "reading the box's identity"
                        QrApertureModel.Step.CHANNEL -> "secure channel · $addr"
                        QrApertureModel.Step.CERTIFICATE -> "signing this phone's certificate"
                        QrApertureModel.Step.PINNED -> "pinning the box's identity"
                        null -> "identity established"
                    }
                    androidx.compose.animation.Crossfade(targetState = line,
                        animationSpec = androidx.compose.animation.core.tween(300), label = "step") { l ->
                        Text(l, color = if (ready) TerminalGreen else GhostTextDim, textAlign = TextAlign.Center,
                            fontFamily = androidx.compose.ui.text.font.FontFamily.Monospace,
                            style = MaterialTheme.typography.bodyMedium)
                    }
                    // the arrival line, fading in only at READY
                    val readyIn = ((a - QrApertureModel.STEPS_FRAC) / (1f - QrApertureModel.STEPS_FRAC)).coerceIn(0f, 1f)
                    Spacer(Modifier.height(10.dp))
                    // the arrival says what the box said, never READY on the code alone: the enrol
                    // started with the scan and may still be out, or have been refused
                    Text(when (enrolOutcome) {
                        true -> "READY , unlock with your PIN"
                        false -> "the box did not take it , what it said is next"
                        null -> "code read , waiting for the box to answer…"
                    }, color = if (enrolOutcome == false) Warning else TerminalGreen,
                        style = MaterialTheme.typography.titleMedium,
                        modifier = Modifier.graphicsLayer { alpha = readyIn })
                    Spacer(Modifier.weight(0.40f))
                }
            }
        }
    }
}

/**
 * The three outcomes of looking at a frame. Nothing (no readable QR, stay quiet, the person is still
 * lining up the shot), Enrol (a real localghost enrol link, proceed), or NotForUs (a QR that decoded
 * fine but is not an enrol link, where we say what it is and have an opinion). The found cases carry
 * the QR's finder points and the frame geometry so the overlay can be anchored to the actual code.
 */
private sealed interface ScanResult {
    object Nothing : ScanResult
    data class Enrol(val link: EnrollLink, val overlay: Overlay, val clean: Boolean) : ScanResult
    data class NotForUs(val guess: com.localghost.app.qr.QrGuess, val overlay: Overlay) : ScanResult
    data class Frames(val have: Int, val want: Int, val captured: Set<Int>, val justCaptured: Boolean, val overlay: Overlay) : ScanResult
}

/**
 * Where the QR is, so the UI can draw on it. finders are the three finder-pattern centres in image
 * pixel space; frameW/frameH are the analysis frame size; rotation is the degrees the frame must be
 * rotated to be upright (from the ImageProxy). The Canvas maps these to view space.
 */
private data class Overlay(
    val finders: List<com.localghost.app.qr.QrSampler.FinderPoint>,
    val frameW: Int,
    val frameH: Int,
    val rotation: Int,
    val corners: List<com.localghost.app.qr.QrSampler.FinderPoint>? = null,
)

/**
 * Map the QR finder points from analysis-frame image space to Canvas view space. Two steps: rotate the
 * image-space point so it is upright (the back camera usually delivers frames rotated 90 degrees from
 * the portrait preview), then scale and centre for PreviewView's default FILL_CENTER crop (scale by the
 * larger ratio so the image fills the view, and offset by the cropped overflow).
 *
 * HONEST NOTE: this is the fiddly part and only a real device confirms it. The rotation cases other
 * than 90 are handled but untested, and front-camera mirroring is not (the scanner uses the back
 * camera). If the box lands offset or mirrored on a device, this function is where the fix goes.
 */
private fun mapFindersToView(
    ov: Overlay,
    viewW: Float,
    viewH: Float,
): List<androidx.compose.ui.geometry.Offset> =
    mapPointsToView(ov.finders, ov.frameW, ov.frameH, ov.rotation, viewW, viewH)

/**
 * Map a list of image-space points (analysis frame) to Canvas view space, applying the frame rotation
 * then PreviewView's FILL_CENTER scale/crop. Shared by the finder overlay and the diagnostic QR outline.
 */
private fun mapPointsToView(
    points: List<com.localghost.app.qr.QrSampler.FinderPoint>,
    frameW: Int,
    frameH: Int,
    rotation: Int,
    viewW: Float,
    viewH: Float,
): List<androidx.compose.ui.geometry.Offset> {
    val (upW, upH) = when (rotation) {
        90, 270 -> frameH.toFloat() to frameW.toFloat()
        else -> frameW.toFloat() to frameH.toFloat()
    }
    val scale = maxOf(viewW / upW, viewH / upH)
    val dx = (viewW - upW * scale) / 2f
    val dy = (viewH - upH * scale) / 2f
    return points.map { p ->
        val (rx, ry) = when (rotation) {
            90 -> (frameH - p.y).toFloat() to p.x.toFloat()
            180 -> (frameW - p.x).toFloat() to (frameH - p.y).toFloat()
            270 -> p.y.toFloat() to (frameW - p.x).toFloat()
            else -> p.x.toFloat() to p.y.toFloat()
        }
        androidx.compose.ui.geometry.Offset(rx * scale + dx, ry * scale + dy)
    }
}

/** Mutable sampling timestamps, held in remember and touched only on the analysis thread. */
private class ScanTiming {
    var lastDecodeAt = 0L
    var lastSeenAt = 0L
    var lastDetectAt = 0L
}

/** How long a found code can be missing before found-mode drops back to hunting. */
private const val FOUND_TIMEOUT_MS = 1500L

/** How recently finders must have been detected to keep sampling at the fast (in-view) rate. Long
 *  enough to bridge a frame or two of blur while holding a code steady, short enough to drop back to
 *  the hunting rate once the code has genuinely left the frame. */
private const val DETECT_WINDOW_MS = 700L


/** Pull luminance from the frame, sample candidate grids, and let our decoder pick the real one. */
// Reusable per-frame buffers. The analyser runs on a single thread, so one set of buffers can be
// reused across frames instead of allocating a ~3.7MB luminance array (and a byte array) every decode.
// Those repeated large allocations were churning the garbage collector, which both heats the phone and
// makes it stutter more the longer it runs. Buffers grow only if the frame size changes.
private object ScanBuffers {
    var lum: IntArray = IntArray(0)
    var bytes: ByteArray = ByteArray(0)
    fun lumFor(size: Int): IntArray {
        if (lum.size != size) lum = IntArray(size)
        return lum
    }
    fun bytesFor(size: Int): ByteArray {
        if (bytes.size != size) bytes = ByteArray(size)
        return bytes
    }
}

private fun tryDecode(proxy: ImageProxy, frames: com.localghost.app.qr.FrameAssembler,
                      stream: com.localghost.app.qr.StreamAssembler): ScanResult {
    return try {
        val plane = proxy.planes[0]
        val buffer = plane.buffer
        val rowStride = plane.rowStride
        val w = proxy.width
        val h = proxy.height
        val lum = ScanBuffers.lumFor(w * h)
        val data = ScanBuffers.bytesFor(buffer.remaining())
        buffer.get(data)
        for (y in 0 until h) {
            val base = y * rowStride
            val rowOut = y * w
            for (x in 0 until w) lum[rowOut + x] = data[base + x].toInt() and 0xFF
        }
        com.localghost.app.qr.QrSampler.ScanGeom.corners = null
        com.localghost.app.qr.QrSampler.ScanGeom.findersSeen = 0
        com.localghost.app.qr.QrSampler.ScanGeom.frameW = w
        com.localghost.app.qr.QrSampler.ScanGeom.frameH = h
        com.localghost.app.qr.QrSampler.ScanGeom.rotation = proxy.imageInfo.rotationDegrees

        // Sample several candidate grids (versions, alignment on/off) and let the decoder be the judge.
        // The image stage cannot tell a subtly-wrong sampling from a right one; only format BCH + Reed
        // Solomon can. We try each candidate (and the decoder itself tries all 4 rotations) and keep the
        // first that actually decodes. This is what makes tilted real frames work: the best-looking grid
        // and the decodable grid are not always the same, and only the decode settles it.
        val (candidates, diag) = QrSampler.sampleCandidates(lum, w, h)
        // f=N is the finder-cluster count this frame , the detection-vs-decode discriminator that the
        // frame dump used to answer: f=0 means the finders were never seen (detection problem), f>=3
        // with no decode means the maths downstream is what is failing.
        ScanDiag.last = "${diag.note} f=${QrSampler.ScanGeom.findersSeen}"
        if (candidates.isEmpty()) return ScanResult.Nothing

        var text: String? = null
        var overlay: Overlay? = null
        for (cand in candidates) {
            val t = try {
                QrMatrixDecode.decode(cand.grid, cand.conf)
            } catch (e: Exception) {
                null
            }
            if (t != null) {
                text = t
                overlay = Overlay(cand.finders, w, h, proxy.imageInfo.rotationDegrees,
                    com.localghost.app.qr.QrSampler.ScanGeom.corners)
                break
            }
        }
        val gN = com.localghost.app.qr.QrSampler.ScanGeom.gridN
        val ver = if (gN >= 21) (gN - 17) / 4 else 0
        val align = if (com.localghost.app.qr.QrSampler.ScanGeom.alignFound) "align+" else "align-"
        if (text == null || overlay == null) {
            ScanDiag.last = "v$ver $align f=${com.localghost.app.qr.QrSampler.ScanGeom.findersSeen}: sampled, none decoded"
            // Track a finders-but-no-decode streak on the shared ScanGeom so the composable can turn it
            // into on-screen coaching (this function is top-level, with no access to composable state).
            if (com.localghost.app.qr.QrSampler.ScanGeom.findersSeen >= 1) {
                com.localghost.app.qr.QrSampler.ScanGeom.noDecodeStreak += 1
            }
            return ScanResult.Nothing
        }
        // Whether the decode was CLEAN (plain Reed-Solomon, no erasures). A clean decode of a code that
        // parses as a valid enrol link is trustworthy on the first frame , the strict URL pattern plus a
        // well-formed pinned fingerprint make a random miscorrection into a valid enrol link effectively
        // impossible. Erasure-path decodes ("conf"/"logo") are more willing to manufacture a consistent-
        // but-wrong payload, so those still require the two-frame confirmation downstream.
        val cleanDecode = QrMatrixDecode.lastPath == "clean"
        com.localghost.app.qr.QrSampler.ScanGeom.noDecodeStreak = 0 // decoding works; clear any coaching
        ScanDiag.last = "v$ver $align ${QrMatrixDecode.lastPath} ${text.length}ch"

        // Multi-frame enrolment: a real device identity spans several QRs. If this decode is a frame,
        // feed it to the assembler and only parse once every frame is captured and the checksum verifies.
        // A single-QR (small) enrol link never matches the frame magic and falls straight through.
        val toParse: String
        if (stream.isFrame(text)) {
            // Erasure-coded set: every distinct frame counts, whichever it is. The pips show a COUNT
            // (the first `have` of K), not identities, because with parity any K of K+M do.
            val payload = stream.offer(text)
            val (have, want) = stream.progress()
            if (payload == null) {
                ScanDiag.last = "enrol frame ${have} of ${want} (any of ${stream.totalFrames()})"
                return ScanResult.Frames(have, want, (1..have).toSet(), stream.lastOfferWasNew, overlay)
            }
            ScanDiag.last = "enrol complete ${have} of ${want}"
            toParse = String(payload, Charsets.ISO_8859_1)
        } else if (frames.isFrame(text)) {
            val joined = frames.offer(text)
            val (have, want) = frames.progress()
            if (joined == null) {
                ScanDiag.last = "enrol frame ${have} of ${want} captured"
                return ScanResult.Frames(have, want, frames.capturedSeqs(), frames.lastOfferWasNew, overlay)
            }
            if (frames.lastOfferWasNew) {
                ScanDiag.last = "enrol complete ${have} of ${want}"
            }
            toParse = joined
        } else {
            // Not a well-formed frame. But if we are MID-COLLECTION (some frames captured, not all), a
            // decode that is not a clean frame is almost always a garbled candidate of one , the decoder
            // tries several samplings per physical QR and a bad one can lose the "LGQR1" prefix. Showing
            // "not an enrol link" for it is wrong and alarming. Stay in frame mode and keep the progress
            // UI up rather than routing a near-miss to the wrong-QR classifier.
            val (have, want) = frames.progress()
            if (want > 0 && have < want) {
                return ScanResult.Frames(have, want, frames.capturedSeqs(), false, overlay)
            }
            val (shave, swant) = stream.progress()
            if (swant > 0 && shave < swant) {
                return ScanResult.Frames(shave, swant, (1..shave).toSet(), false, overlay)
            }
            toParse = text
        }

        when (val r = EnrollLink.parseResult(toParse)) {
            is EnrollLink.Result.Ok -> ScanResult.Enrol(r.link, overlay, cleanDecode)
            is EnrollLink.Result.Outdated -> ScanResult.NotForUs(
                com.localghost.app.qr.QrGuess(
                    "a newer box",
                    "That code is from a newer LocalGhost than this app. Update the app and try again.",
                    "enrol v${r.sawVersion}",
                ),
                overlay,
            )
            EnrollLink.Result.Malformed -> ScanResult.NotForUs(
                com.localghost.app.qr.QrContent.classify(text), overlay,
            )
            EnrollLink.Result.NotEnroll -> ScanResult.NotForUs(
                com.localghost.app.qr.QrContent.classify(text), overlay,
            )
        }
    } catch (t: Throwable) {
        // A scanned code must never crash the app. Any throwable becomes "no readable QR" and the
        // person keeps scanning or types the values. A bad scan can never enrol anyway, because the
        // fingerprint pin must still match.
        ScanDiag.last = "frame error: ${t.message ?: t.javaClass.simpleName}"
        ScanResult.Nothing
    }
}

/** Thread-safe holder for the latest scan-pipeline diagnostic, read by the status line. */
private object ScanDiag {
    @Volatile var last: String = "starting"
}

// --- the vault aperture drawn over the code, in the unlock's language (QrApertureModel) ---

/**
 * The aiming reticle, fixed in the centre of the screen. A ring of eight segments (one per frame):
 * the lit ones bright, the rest a faint outline, so a multi-frame enrolment fills the ring as its
 * frames land. A scan tick sweeps the ring as it turns. In the middle, a crosshair to aim; a green
 * check flashes there on each frame read ([tick] 1..0 over its fade). Red (wrong == true) when a
 * code read but is not the way in.
 */
private fun androidx.compose.ui.graphics.drawscope.DrawScope.drawAperture(
    cx: Float, cy: Float, radius: Float,
    tint: androidx.compose.ui.graphics.Color, spin: Float, lit: Int, wrong: Boolean, tick: Float,
) {
    val segs = QrApertureModel.SEGMENTS   // eight, one per frame
    val gap = 10f                          // degrees of gap between segments
    val sweep = 360f / segs - gap
    val stroke = radius * 0.11f
    val topLeft = androidx.compose.ui.geometry.Offset(cx - radius, cy - radius)
    val arcSize = androidx.compose.ui.geometry.Size(radius * 2, radius * 2)
    val flare = 0.35f * tick
    for (i in 0 until segs) {
        val start = QrApertureModel.segmentAngle(i, segs) - sweep / 2f + spin * 0.12f
        val on = i < lit
        val alpha = if (on) (0.85f + flare).coerceAtMost(1f) else 0.16f
        drawArc(
            color = tint.copy(alpha = alpha),
            startAngle = start, sweepAngle = sweep, useCenter = false,
            topLeft = topLeft, size = arcSize,
            style = androidx.compose.ui.graphics.drawscope.Stroke(width = if (on) stroke else stroke * 0.5f,
                cap = androidx.compose.ui.graphics.StrokeCap.Round),
        )
    }
    // a scan tick sweeping the ring while it reads (not when wrong)
    if (!wrong) {
        val a = Math.toRadians((spin % 360f - 90f).toDouble())
        drawCircle(tint, radius * 0.05f,
            androidx.compose.ui.geometry.Offset(cx + (radius * kotlin.math.cos(a)).toFloat(),
                cy + (radius * kotlin.math.sin(a)).toFloat()))
    }
    // THE MIDDLE. Just a crosshair to aim at the QR , no lock over it. When a frame is read a green
    // check flashes on the code (tick 1..0 over its fade), the only thing that appears in the centre.
    val g = radius * 0.30f
    if (tick > 0.02f && !wrong) {
        val sw = g * 0.22f
        drawLine(tint.copy(alpha = tick), androidx.compose.ui.geometry.Offset(cx - g * 0.5f, cy),
            androidx.compose.ui.geometry.Offset(cx - g * 0.1f, cy + g * 0.45f), sw, cap = androidx.compose.ui.graphics.StrokeCap.Round)
        drawLine(tint.copy(alpha = tick), androidx.compose.ui.geometry.Offset(cx - g * 0.1f, cy + g * 0.45f),
            androidx.compose.ui.geometry.Offset(cx + g * 0.55f, cy - g * 0.45f), sw, cap = androidx.compose.ui.graphics.StrokeCap.Round)
    } else {
        val c = g * 0.7f
        val cw = radius * 0.03f
        val al = if (wrong) 0.6f else 0.45f
        for (s in listOf(-1f, 1f)) {
            drawLine(tint.copy(alpha = al), androidx.compose.ui.geometry.Offset(cx + s * c, cy),
                androidx.compose.ui.geometry.Offset(cx + s * c * 0.4f, cy), cw)
            drawLine(tint.copy(alpha = al), androidx.compose.ui.geometry.Offset(cx, cy + s * c),
                androidx.compose.ui.geometry.Offset(cx, cy + s * c * 0.4f), cw)
        }
    }
}


// --- establishing identity, once a box is found (QrApertureModel.Step) ---

/**
 * The found sequence, told in animation. One ring builds out clockwise as the identity is established;
 * four beads on it (top, right, bottom, left) fill as each step lands, the current one pulsing; and a
 * small glyph in the middle changes per step , a code grid read, a channel opening, a certificate
 * signed, a lock closing , then a steady tick once READY. [t] is 0..1 over the whole sequence;
 * [shimmer] is a free clock for a faint breathe. Drawn over the darkened screen (the camera is no
 * longer needed once the whole code is found), so the words beside it read clearly.
 */
private fun androidx.compose.ui.graphics.drawscope.DrawScope.drawEstablish(
    cx: Float, cy: Float, radius: Float, shimmer: Float, t: Float,
) {
    val centre = androidx.compose.ui.geometry.Offset(cx, cy)
    val tint = TerminalGreen
    val breathe = 0.9f + 0.1f * kotlin.math.sin(shimmer * 3f)
    val steps = QrApertureModel.Step.entries
    val done = QrApertureModel.stepsDone(t)
    val step = QrApertureModel.stepAt(t)
    val sp = QrApertureModel.stepProgress(t)
    val ready = QrApertureModel.ready(t)

    // base ring, faint
    drawCircle(tint.copy(alpha = 0.14f), radius, centre,
        style = androidx.compose.ui.graphics.drawscope.Stroke(width = radius * 0.03f))
    // the ring builds out clockwise from the top as the identity is established
    val buildF = if (ready) 1f else (t / QrApertureModel.STEPS_FRAC).coerceIn(0f, 1f)
    val topLeft = androidx.compose.ui.geometry.Offset(cx - radius, cy - radius)
    val arcSize = androidx.compose.ui.geometry.Size(radius * 2, radius * 2)
    drawArc(color = tint.copy(alpha = 0.9f * breathe), startAngle = -90f, sweepAngle = 360f * buildF,
        useCenter = false, topLeft = topLeft, size = arcSize,
        style = androidx.compose.ui.graphics.drawscope.Stroke(width = radius * 0.06f, cap = androidx.compose.ui.graphics.StrokeCap.Round))
    // the four beads
    for (i in steps.indices) {
        val ang = Math.toRadians((-90f + i * 90f).toDouble())
        val bx = cx + (radius * kotlin.math.cos(ang)).toFloat()
        val by = cy + (radius * kotlin.math.sin(ang)).toFloat()
        val at = androidx.compose.ui.geometry.Offset(bx, by)
        val filled = i < done
        val current = step != null && i == done
        val bead = radius * (if (current) 0.09f + 0.02f * breathe else 0.07f)
        drawCircle(Void, bead * 1.6f, at)
        drawCircle(tint.copy(alpha = if (filled) 1f else if (current) 0.6f else 0.2f), bead, at)
        if (filled) drawCircle(Void, bead * 0.4f, at) // a made bead is a ring, an unmade one a dot
    }

    // the centre glyph
    val g = radius * 0.5f
    when {
        ready -> {
            // a steady tick, gently pulsing
            val p = 0.9f + 0.1f * kotlin.math.sin(shimmer * 4f)
            val sw = g * 0.16f
            drawLine(tint, androidx.compose.ui.geometry.Offset(cx - g * 0.45f, cy + g * 0.02f),
                androidx.compose.ui.geometry.Offset(cx - g * 0.1f, cy + g * 0.4f), sw * p, cap = androidx.compose.ui.graphics.StrokeCap.Round)
            drawLine(tint, androidx.compose.ui.geometry.Offset(cx - g * 0.1f, cy + g * 0.4f),
                androidx.compose.ui.geometry.Offset(cx + g * 0.5f, cy - g * 0.4f), sw * p, cap = androidx.compose.ui.graphics.StrokeCap.Round)
        }
        step == QrApertureModel.Step.IDENTITY -> {
            // a 3x3 code grid, cells appearing with progress
            val cell = g * 0.5f
            val pattern = intArrayOf(1,0,1, 0,1,0, 1,1,0)
            var shown = 0
            val total = pattern.count { it == 1 }
            for (r in 0..2) for (c in 0..2) {
                if (pattern[r * 3 + c] == 0) continue
                shown++
                if (shown.toFloat() / total > sp + 0.001f) continue
                drawRect(tint, androidx.compose.ui.geometry.Offset(cx - g * 0.75f + c * cell, cy - g * 0.75f + r * cell),
                    androidx.compose.ui.geometry.Size(cell * 0.8f, cell * 0.8f))
            }
        }
        step == QrApertureModel.Step.CHANNEL -> {
            // two nodes with a pulse travelling between , the channel opening
            val top = androidx.compose.ui.geometry.Offset(cx, cy - g * 0.7f)
            val bot = androidx.compose.ui.geometry.Offset(cx, cy + g * 0.7f)
            drawLine(tint.copy(alpha = 0.35f), top, bot, g * 0.06f)
            drawCircle(tint, g * 0.16f, top); drawCircle(tint, g * 0.16f, bot)
            val py = cy - g * 0.7f + g * 1.4f * sp
            drawCircle(tint, g * 0.12f, androidx.compose.ui.geometry.Offset(cx, py))
        }
        step == QrApertureModel.Step.CERTIFICATE -> {
            // a document with a signature stroke drawing across it, and a seal
            drawRect(tint.copy(alpha = 0.5f), androidx.compose.ui.geometry.Offset(cx - g * 0.6f, cy - g * 0.7f),
                androidx.compose.ui.geometry.Size(g * 1.2f, g * 1.4f),
                style = androidx.compose.ui.graphics.drawscope.Stroke(width = g * 0.06f))
            // the signature , a squiggle whose length follows progress
            val path = androidx.compose.ui.graphics.Path()
            val n = 24
            val upto = (n * sp).toInt().coerceIn(1, n)
            for (k in 0..upto) {
                val x = cx - g * 0.4f + (g * 0.8f) * (k.toFloat() / n)
                val y = cy + g * 0.2f + kotlin.math.sin(k * 0.9f) * g * 0.18f
                if (k == 0) path.moveTo(x, y) else path.lineTo(x, y)
            }
            drawPath(path, tint, style = androidx.compose.ui.graphics.drawscope.Stroke(width = g * 0.07f, cap = androidx.compose.ui.graphics.StrokeCap.Round))
            if (sp > 0.8f) drawCircle(tint, g * 0.13f, androidx.compose.ui.geometry.Offset(cx + g * 0.4f, cy + g * 0.5f))
        }
        step == QrApertureModel.Step.PINNED -> {
            // a padlock whose shackle closes as progress rises
            val bodyTop = cy - g * 0.1f
            drawRect(tint, androidx.compose.ui.geometry.Offset(cx - g * 0.5f, bodyTop),
                androidx.compose.ui.geometry.Size(g * 1.0f, g * 0.8f))
            drawRect(Void, androidx.compose.ui.geometry.Offset(cx - g * 0.08f, bodyTop + g * 0.28f),
                androidx.compose.ui.geometry.Size(g * 0.16f, g * 0.3f))
            // the shackle: an arc that swings down and closes
            val lift = (1f - sp) * g * 0.5f
            val sTop = androidx.compose.ui.geometry.Offset(cx - g * 0.35f, bodyTop - lift)
            drawArc(color = tint, startAngle = 180f, sweepAngle = 180f, useCenter = false,
                topLeft = androidx.compose.ui.geometry.Offset(sTop.x, bodyTop - g * 0.55f - lift),
                size = androidx.compose.ui.geometry.Size(g * 0.7f, g * 0.7f),
                style = androidx.compose.ui.graphics.drawscope.Stroke(width = g * 0.12f, cap = androidx.compose.ui.graphics.StrokeCap.Round))
        }
        else -> {}
    }
}
