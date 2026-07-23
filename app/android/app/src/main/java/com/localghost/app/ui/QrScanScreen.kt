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
import androidx.compose.foundation.background
import androidx.compose.foundation.gestures.detectTapGestures
import androidx.compose.foundation.layout.*
import androidx.compose.material3.MaterialTheme
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
    // network request in the host), then play a fixed ~2.6s AR success animation over the live camera , a
    // big happy ghost pops in and bobs while fireworks burst around it. The enrol and the celebration run
    // at once, so the time is not dead waiting even though the box can take a moment to answer. Only once
    // the animation has fully played do we call onProceed to leave the scanner, so the success is always
    // seen for its full length no matter how fast or slow the box responds; the host routes on the outcome.
    LaunchedEffect(foundLink) {
        val link = foundLink ?: return@LaunchedEffect
        onScanned(link) // start enrolling now, in the background
        val steps = 52
        for (i in 1..steps) {
            enrolAnim = i.toFloat() / steps
            kotlinx.coroutines.delay(50) // 52 * 50ms = 2600ms
        }
        onProceed() // celebration done , hand off
    }

    // Celebration clock: runs while a real box is found, drives the fireworks burst.
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
        DisposableEffect(Unit) { onDispose { analysisExecutor.shutdown() } }

        // The PreviewView is created once and kept; the camera is bound to the lifecycle in a
        // LaunchedEffect below, NOT in the view factory. Binding in the factory ran once and never
        // rebound, so after the activity backgrounded (e.g. the camera permission dialog, which stops
        // the activity and tears the camera down) the camera never came back and the screen sat dead.
        // bindToLifecycle with the real lifecycleOwner handles stop/resume itself, so the camera
        // returns when the app does.
        val previewView = remember { PreviewView(context) }
        // The bound Camera, kept so the tap-to-focus gesture below can drive its CameraControl.
        var camera by remember { mutableStateOf<androidx.camera.core.Camera?>(null) }
        // AUTO-TORCH , after five rounds the detector is algorithm-complete; what defeats it now
        // is a dim hallway, same as every scanner. Mean luma below the floor for ~15 frames
        // lights the torch; comfortably bright for as long puts it out. Hysteresis, not a
        // flicker; the person can always cover the lens if they disagree.
        var darkFrames by remember { mutableStateOf(0) }
        var brightFrames by remember { mutableStateOf(0) }
        var torchOn by remember { mutableStateOf(false) }
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
                run {
                    // Subsampled mean luma straight off the Y plane , 1 pixel in 64, pennies.
                    val plane = proxy.planes[0]
                    val buf = plane.buffer.duplicate()
                    var sum = 0L; var n = 0
                    var i = 0
                    val lim = buf.limit()
                    while (i < lim) { sum += buf.get(i).toInt() and 0xFF; n++; i += 64 }
                    val mean = if (n > 0) (sum / n).toInt() else 128
                    if (mean < 55) { darkFrames++; brightFrames = 0 } else if (mean > 80) { brightFrames++; darkFrames = 0 }
                    if (!torchOn && darkFrames > 15) {
                        torchOn = true
                        camera?.cameraControl?.enableTorch(true)
                    } else if (torchOn && brightFrames > 15) {
                        torchOn = false
                        camera?.cameraControl?.enableTorch(false)
                    }
                }
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
                // throttle, which FEELS like the scanner getting worse the longer you try. 250ms hot
                // still lands ~12 attempts per rotating frame (3.2s hold); 600ms is plenty to notice
                // a code entering view within a blink.
                //
                // EXCEPT during multi-frame ASSEMBLY: capturing the rotating enrol sequence is a
                // bounded burst measured in seconds, not the indefinite hunt the thermal tuning
                // protects against , and every rotating frame that fails to decode inside its own
                // display window costs a FULL cycle of the rotation (the observed "go through the
                // codes once or twice"). 100ms during assembly is ~32 attempts per displayed frame,
                // so a marginal decode gets nearly three times the chances before the display moves
                // on. The burst ends when assembly does, so nothing here can cook the phone.
                val assembling = capturedFrames.isNotEmpty() &&
                    frameProgress?.let { it.first < it.second } == true
                val interval = when {
                    assembling -> 100L
                    codeInView -> 250L
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
                val result = tryDecode(proxy, frames)
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

            // FEEDBACK RETICLE: whenever the detector has locked onto a code's position this frame
            // (ScanGeom.corners is set), draw a clean pulsing corner-bracket square around it, so the
            // person can see "yes, I'm seeing a code, hold steady" even while the decode is still being
            // worked out. It tracks the detected quad; it is feedback, not the decode verdict (that is
            // the ghost). Refreshed on its own ~80ms tick so it animates between decode passes.
            var geomTick by remember { mutableStateOf(0) }
            LaunchedEffect(granted) {
                while (granted) { geomTick++; kotlinx.coroutines.delay(80) }
            }
            // Reticle stabiliser. Raw per-frame corners are honest but twitchy: a one-frame finder
            // coincidence teleports the bracket across the screen, and even a solid lock breathes a
            // few pixels frame to frame. Three rules make it feel locked-on instead:
            //   REJECT , a quad whose corners jump more than a third of the frame within 400ms is a
            //            misdetection; keep showing the last good one.
            //   SMOOTH , accepted quads blend 40% toward the new position (EMA), absorbing breath.
            //   HOLD   , when detection drops, the last quad lingers 350ms so a missed frame or two
            //            does not blink the bracket while the person is holding perfectly still.
            val smooth = remember { object {
                var quad: List<com.localghost.app.qr.QrSampler.FinderPoint>? = null
                var at = 0L
            } }
            run {
                geomTick // read so this recomposes on the tick
                val corners = com.localghost.app.qr.QrSampler.ScanGeom.corners
                val fw = com.localghost.app.qr.QrSampler.ScanGeom.frameW
                val fh = com.localghost.app.qr.QrSampler.ScanGeom.frameH
                val rot = com.localghost.app.qr.QrSampler.ScanGeom.rotation
                val nowMs = System.currentTimeMillis()
                if (foundLink == null && corners != null && corners.size == 4 && quadLooksSquare(corners)) {
                    val prev = smooth.quad
                    val limit = (minOf(fw, fh) / 3f)
                    val jumped = prev != null && nowMs - smooth.at < 400 && prev.zip(corners).any { (a, b) ->
                        val dx = (a.x - b.x).toFloat(); val dy = (a.y - b.y).toFloat()
                        dx * dx + dy * dy > limit * limit
                    }
                    if (!jumped) {
                        smooth.quad = if (prev == null || prev.size != 4) corners
                        else prev.zip(corners).map { (a, b) ->
                            com.localghost.app.qr.QrSampler.FinderPoint(
                                a.x + ((b.x - a.x) * 0.4f).toInt(),
                                a.y + ((b.y - a.y) * 0.4f).toInt(),
                            )
                        }
                        smooth.at = nowMs
                    }
                } else if (nowMs - smooth.at > 350) {
                    smooth.quad = null
                }
                val q4 = smooth.quad
                if (foundLink == null && q4 != null) {
                    Canvas(Modifier.fillMaxSize()) {
                        val q = mapPointsToView(q4, fw, fh, rot, size.width, size.height)
                        val pulse = 0.5f + 0.5f * kotlin.math.sin(geomTick * 0.25f)
                        drawReticle(q, TerminalGreen, pulse)
                    }
                }
            }

            // AR overlay: the ghost drawn over the code, because the code decoded
            // but it is not the way in. The orbit phase advances on its own clock (~100ms) so the ghost
            // flies even between decode passes. The finder points come from the analysis frame (image
            // space); we map them to this Canvas's view space. HONEST NOTE: the mapping is the part to
            // verify on a real device , camera resolution vs preview size vs rotation is the classic
            // source of an offset or mirrored overlay and cannot be confirmed by reasoning alone.
            var orbit by remember { mutableStateOf(0f) }
            LaunchedEffect(quip != null) {
                while (quip != null) {
                    orbit += 0.18f
                    kotlinx.coroutines.delay(100)
                }
            }
            // Rage ramps toward 1 while a wrong code is in view and decays when it leaves, so the ghost
            // visibly gets angrier the longer you keep showing it the wrong code, then calms down.
            var rage by remember { mutableStateOf(0f) }
            LaunchedEffect(granted) {
                while (granted) {
                    rage = if (quip != null) (rage + 0.022f).coerceAtMost(1f) else (rage - 0.04f).coerceAtLeast(0f)
                    kotlinx.coroutines.delay(50)
                }
            }
            overlay?.let { ov ->
                Canvas(Modifier.fillMaxSize()) {
                    val pts = mapFindersToView(ov, size.width, size.height)
                    // On a fresh frame capture, flash a green tick right on the code , the "we saw THIS
                    // one" confirmation Vlad asked for, drawn where the phone is actually pointed. Brief
                    // (matches frameFlashAt's ~450ms window) so it acknowledges without obscuring the next.
                    if (pts.size == 3 && System.currentTimeMillis() - frameFlashAt < 450L) {
                        val minX = pts.minOf { it.x }; val maxX = pts.maxOf { it.x }
                        val minY = pts.minOf { it.y }; val maxY = pts.maxOf { it.y }
                        val cx = (minX + maxX) / 2f; val cy = (minY + maxY) / 2f
                        val r = (maxX - minX).coerceAtLeast(60f) * 0.28f
                        drawCircle(TerminalGreen.copy(alpha = 0.22f), r * 1.7f, androidx.compose.ui.geometry.Offset(cx, cy))
                        val sw = r * 0.28f
                        drawLine(TerminalGreen, androidx.compose.ui.geometry.Offset(cx - r * 0.55f, cy),
                            androidx.compose.ui.geometry.Offset(cx - r * 0.1f, cy + r * 0.5f), sw, cap = androidx.compose.ui.graphics.StrokeCap.Round)
                        drawLine(TerminalGreen, androidx.compose.ui.geometry.Offset(cx - r * 0.1f, cy + r * 0.5f),
                            androidx.compose.ui.geometry.Offset(cx + r * 0.6f, cy - r * 0.5f), sw, cap = androidx.compose.ui.graphics.StrokeCap.Round)
                    }
                    // Only the WRONG-code ghost is drawn over the code. A successful scan is handled by the
                    // full-screen celebration layer at the end, so nothing is drawn on the code on success.
                    if (pts.size == 3 && quip != null) {
                        // No bracket , just the ghost, getting ANGRIER the longer you keep showing it
                        // rubbish. `rage` ramps toward 1 while a wrong code is in view (and decays
                        // otherwise); it reddens the ghost, grows it, shakes it harder, and pushes a red
                        // glow out behind it. The thing we scanned pops up above and mouths off.
                        val minX = pts.minOf { it.x }; val maxX = pts.maxOf { it.x }
                        val minY = pts.minOf { it.y }
                        val cx = (minX + maxX) / 2f
                        val pad = 24f
                        val angry = androidx.compose.ui.graphics.lerp(Warning, AngryRed, rage)
                        val ghostS = 54f + rage * 34f
                        val jx = kotlin.math.sin(orbit * (3.1f + rage * 3f)) * (6f + rage * 14f)
                        val jy = kotlin.math.cos(orbit * (2.7f + rage * 3f)) * (4f + rage * 10f)
                        val gx = cx + jx
                        val gy = minY - pad - ghostS * 1.3f + jy
                        // Spooky backdrop: a deep void heart with a faint breathing mist ring, so the ghost
                        // reads over a bright or busy camera frame AND looks like it brought its own gloom.
                        // Drawn always, even before rage builds, so the ghost is visible the instant it appears.
                        drawSpookyHalo(gx, gy, ghostS, angry, orbit)
                        if (rage > 0.02f) {
                            drawCircle(
                                brush = androidx.compose.ui.graphics.Brush.radialGradient(
                                    colors = listOf(AngryRed.copy(alpha = 0.45f * rage), androidx.compose.ui.graphics.Color.Transparent),
                                    center = androidx.compose.ui.geometry.Offset(gx, gy),
                                    radius = ghostS * 2.6f,
                                ),
                                radius = ghostS * 2.6f,
                                center = androidx.compose.ui.geometry.Offset(gx, gy),
                            )
                        }
                        drawAngryGhost(gx, gy, ghostS, angry, orbit, rage)
                        val said = quip?.let { shoutFor(it) } ?: ""
                        if (said.isNotEmpty()) {
                            drawSpeechBubble(gx, gy - ghostS * 1.5f, said, angry, orbit)
                        }
                    }
                }
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
                // Multi-frame enrolment progress: one pip per frame, filled + checked once captured. A
                // just-captured frame briefly brightens (frameFlashAt), so each scan visibly lands.
                frameProgress?.let { (_, want) ->
                    if (want > 1) {
                        Spacer(Modifier.height(6.dp))
                        val flashing = System.currentTimeMillis() - frameFlashAt < 450L
                        Row(horizontalArrangement = Arrangement.spacedBy(6.dp)) {
                            for (seq in 1..want) {
                                val got = seq in capturedFrames
                                Text(
                                    if (got) "☑" else "☐",
                                    color = when {
                                        got && flashing -> TerminalGreen
                                        got -> TerminalGreen.copy(alpha = 0.85f)
                                        else -> GhostTextDim
                                    },
                                    style = MaterialTheme.typography.labelMedium,
                                )
                            }
                        }
                        Spacer(Modifier.height(2.dp))
                        Text(
                            "${capturedFrames.size} of $want frames , keep the phone pointed at the box",
                            color = GhostTextDim,
                            style = MaterialTheme.typography.labelSmall,
                        )
                    }
                }
                if (diag.isNotEmpty()) {
                    Spacer(Modifier.height(3.dp))
                    Text("· $diag", color = GhostTextDim, style = MaterialTheme.typography.labelSmall)
                }
            }

            // When a readable-but-wrong QR is in view, say what it was and have an opinion.
            quip?.let { g ->
                Spacer(Modifier.height(10.dp))
                Column(
                    modifier = Modifier
                        .fillMaxWidth()
                        .background(VoidLighter, MaterialTheme.shapes.small)
                        .padding(12.dp)
                ) {
                    Text("Looks like ${g.label}.", color = Warning, style = MaterialTheme.typography.bodyMedium)
                    Spacer(Modifier.height(4.dp))
                    Text(g.quip, color = TerminalGreen, style = MaterialTheme.typography.bodySmall)
                    Spacer(Modifier.height(6.dp))
                    Text(g.preview, color = GhostTextDim, style = MaterialTheme.typography.labelSmall)
                }
            }

            Spacer(Modifier.height(8.dp))
            GhostButton("CANCEL / TYPE INSTEAD", onCancel, modifier = Modifier.fillMaxWidth())
        }

        // SUCCESS. A real box is scanned. This is AR: the live camera stays behind the celebration, and a
        // big happy ghost pops in and bobs over a soft glow while colourful fireworks burst around it. The
        // status text sits on a dark pill so it stays legible over the camera. enrolAnim (0..1 over the
        // animation) pops the ghost in with a little overshoot; celebrate is the free clock driving the
        // fireworks and the bob. Nothing is drawn opaque , the whole point is to keep the AR camera visible.
        if (foundLink != null) {
            val boxLabel = foundLink?.boxName?.trim()?.takeIf { it.isNotEmpty() } ?: "the box"
            Box(Modifier.fillMaxSize()) {
                Canvas(Modifier.fillMaxSize()) {
                    drawFireworks(size.width, size.height, celebrate)
                    val cx = size.width / 2f
                    val cy = size.height * 0.42f + kotlin.math.sin(celebrate * 2.2f) * (size.height * 0.012f)
                    val a = enrolAnim.coerceIn(0f, 1f)
                    val pop = 1f - (1f - a) * (1f - a)                                   // ease-out
                    val overshoot = 1f + 0.10f * kotlin.math.sin(a * Math.PI.toFloat())  // gentle pop
                    val s = size.minDimension * 0.12f * pop * overshoot
                    if (s > 1f) {
                        // Spooky backdrop: same treatment as the angry ghost , deep void heart with a faint
                        // breathing spectral-green mist , then the celebration's own green glow on top.
                        drawSpookyHalo(cx, cy, s, TerminalGreen, celebrate)
                        drawCircle(
                            brush = androidx.compose.ui.graphics.Brush.radialGradient(
                                colors = listOf(TerminalGreen.copy(alpha = 0.30f), androidx.compose.ui.graphics.Color.Transparent),
                                center = androidx.compose.ui.geometry.Offset(cx, cy),
                                radius = s * 2.6f,
                            ),
                            radius = s * 2.6f,
                            center = androidx.compose.ui.geometry.Offset(cx, cy),
                        )
                        drawHappyGhost(cx, cy, s, TerminalGreen)
                    }
                }
                Column(
                    Modifier.fillMaxSize().systemBarsPadding().padding(24.dp),
                    horizontalAlignment = Alignment.CenterHorizontally,
                ) {
                    Spacer(Modifier.weight(0.62f))
                    Column(
                        Modifier
                            .background(Void.copy(alpha = 0.72f), MaterialTheme.shapes.small)
                            .padding(horizontal = 16.dp, vertical = 10.dp),
                        horizontalAlignment = Alignment.CenterHorizontally,
                    ) {
                        Text("BOX FOUND", color = TerminalGreen, style = MaterialTheme.typography.headlineSmall)
                        Spacer(Modifier.height(4.dp))
                        Text("connecting to $boxLabel…", color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
                    }
                    Spacer(Modifier.weight(0.38f))
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
/**
 * Whether a sampled quad is plausibly a QR code seen in perspective. The sampler sets ScanGeom.corners
 * for ANY grid it managed to sample, including grids built from finder-shaped coincidences in ordinary
 * texture; drawing the reticle on those covers the screen in shapes where there is no code at all. A
 * real code, even tilted hard, keeps its four sides within a modest band of each other and its two
 * diagonals close; junk quads assembled from unrelated points are wildly skewed and fail one of the
 * two ratio checks. Gates only the DRAWING , detection, sampling and the fast-rate signal are untouched.
 */
private fun quadLooksSquare(q: List<com.localghost.app.qr.QrSampler.FinderPoint>): Boolean {
    if (q.size != 4) return false
    fun d(a: com.localghost.app.qr.QrSampler.FinderPoint, b: com.localghost.app.qr.QrSampler.FinderPoint): Float =
        kotlin.math.hypot((a.x - b.x).toFloat(), (a.y - b.y).toFloat())
    val sides = listOf(d(q[0], q[1]), d(q[1], q[2]), d(q[2], q[3]), d(q[3], q[0]))
    val shortest = sides.min()
    if (shortest < 1f) return false                    // degenerate
    if (sides.max() / shortest > 1.8f) return false    // ~45 degrees of tilt still passes; junk doesn't
    val d1 = d(q[0], q[2]); val d2 = d(q[1], q[3])
    return maxOf(d1, d2) / minOf(d1, d2).coerceAtLeast(1f) <= 1.45f
}

/**
 * The ghosts' backdrop: a deep void heart with a faint, slowly breathing mist ring in the ghost's own
 * colour, so it reads over a bright or busy camera frame and looks properly haunted rather than like a
 * drop shadow. Everything is radial and fades to transparent, keeping the AR feel. `t` is any advancing
 * animation clock (orbit for the angry ghost, the celebration clock for the happy one).
 */
private fun androidx.compose.ui.graphics.drawscope.DrawScope.drawSpookyHalo(
    cx: Float, cy: Float, s: Float,
    tint: androidx.compose.ui.graphics.Color, t: Float,
) {
    val centre = androidx.compose.ui.geometry.Offset(cx, cy)
    val breathe = 0.9f + 0.1f * kotlin.math.sin(t * 5.3f)   // slow candle-flicker of the mist
    // Outer spectral mist: transparent at the heart, faint tint mid-ring, gone at the edge.
    drawCircle(
        brush = androidx.compose.ui.graphics.Brush.radialGradient(
            0.0f to androidx.compose.ui.graphics.Color.Transparent,
            0.55f to tint.copy(alpha = 0.13f * breathe),
            1.0f to androidx.compose.ui.graphics.Color.Transparent,
            center = centre, radius = s * 3.1f,
        ),
        radius = s * 3.1f, center = centre,
    )
    // Deep void heart, darker than a shadow , the gloom the ghost brought with it.
    drawCircle(
        brush = androidx.compose.ui.graphics.Brush.radialGradient(
            colors = listOf(Void.copy(alpha = 0.80f), Void.copy(alpha = 0.42f), androidx.compose.ui.graphics.Color.Transparent),
            center = centre, radius = s * 2.2f,
        ),
        radius = s * 2.2f, center = centre,
    )
}

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

/** The ghost silhouette body (dome top, scalloped bottom), centred at (cx, cy), size s. */
private fun ghostBody(cx: Float, cy: Float, s: Float): androidx.compose.ui.graphics.Path =
    androidx.compose.ui.graphics.Path().apply {
        moveTo(cx - s, cy + s)
        cubicTo(cx - s, cy - s * 1.3f, cx + s, cy - s * 1.3f, cx + s, cy + s)
        val n = 3
        val step = (2 * s) / n
        var x = cx + s
        for (i in 0 until n) {
            val nx = x - step
            val midY = if (i % 2 == 0) cy + s * 1.35f else cy + s * 0.75f
            quadraticTo((x + nx) / 2f, midY, nx, cy + s)
            x = nx
        }
        close()
    }

/**
 * The brand ghost, ANGRY variant: bigger, filled, vibrating with rage. Centred at (cx, cy). The body
 * is filled (so it reads as solid and full, not a wisp), with a darker fill under a bright outline.
 * Angry V-brows, hard eyes, a jagged grimace, and anger marks puffing off the head that pulse with the
 * phase. Pure Canvas so it animates wherever the hover/jitter puts it.
 */
private fun androidx.compose.ui.graphics.drawscope.DrawScope.drawAngryGhost(
    cx: Float, cy: Float, s: Float, color: androidx.compose.ui.graphics.Color, phase: Float, rage: Float,
) {
    val body = ghostBody(cx, cy, s)
    // fill first (alpha grows with rage so it reads more solid and hot), then a bright outline
    drawPath(body, color.copy(alpha = 0.22f + 0.3f * rage), style = androidx.compose.ui.graphics.drawscope.Fill)
    drawPath(body, color, style = androidx.compose.ui.graphics.drawscope.Stroke(width = 4f))

    // angry eyebrows: two strokes angled down toward the centre (a scowl), steeper as rage climbs
    val browY = cy - s * 0.32f
    val eo = s * 0.40f
    val bl = s * 0.34f
    val tilt = bl * (0.35f + 0.4f * rage)
    drawLine(color,
        androidx.compose.ui.geometry.Offset(cx - eo - bl / 2, browY - tilt),
        androidx.compose.ui.geometry.Offset(cx - eo + bl / 2, browY + tilt), strokeWidth = 5f)
    drawLine(color,
        androidx.compose.ui.geometry.Offset(cx + eo - bl / 2, browY + tilt),
        androidx.compose.ui.geometry.Offset(cx + eo + bl / 2, browY - tilt), strokeWidth = 5f)

    // hard round eyes under the brows
    val eyeY = cy - s * 0.05f
    drawCircle(color, radius = s * 0.16f, center = androidx.compose.ui.geometry.Offset(cx - eo, eyeY))
    drawCircle(color, radius = s * 0.16f, center = androidx.compose.ui.geometry.Offset(cx + eo, eyeY))

    // a jagged, gritted grimace (zig-zag) across the lower face
    val mouth = androidx.compose.ui.graphics.Path().apply {
        val mw = s * 0.7f
        val my = cy + s * 0.42f
        val left = cx - mw / 2f
        moveTo(left, my)
        val teeth = 4
        val tw = mw / teeth
        for (i in 0 until teeth) {
            val x1 = left + tw * (i + 0.5f)
            val x2 = left + tw * (i + 1f)
            lineTo(x1, my - s * 0.14f)
            lineTo(x2, my)
        }
    }
    drawPath(mouth, color, style = androidx.compose.ui.graphics.drawscope.Stroke(width = 3f))

    // anger marks: jagged sparks puffing off the head, MORE of them and longer as rage climbs
    val pulse = 0.6f + 0.4f * kotlin.math.sin(phase * 4f)
    val markR = s * (1.15f + 0.12f * pulse)
    val markLen = s * (0.22f + 0.4f * rage) * pulse
    val count = 4 + (rage * 6f).toInt()
    for (i in 0 until count) {
        val a = (i.toFloat() / count) * (2f * Math.PI.toFloat()) + phase * 0.5f
        val bx = cx + markR * kotlin.math.cos(a)
        val by = cy + markR * kotlin.math.sin(a)
        drawLine(color,
            androidx.compose.ui.geometry.Offset(bx, by),
            androidx.compose.ui.geometry.Offset(bx + markLen * kotlin.math.cos(a), by + markLen * kotlin.math.sin(a)),
            strokeWidth = 3f)
    }
}

/** Short angry thing the scanned code "shouts" from the bubble, by kind. Keep it punchy. */
private fun shoutFor(g: com.localghost.app.qr.QrGuess): String = when {
    g.label.contains("link", true) || g.label.contains("web", true) -> "I'm just a website!"
    g.label.contains("wifi", true) -> "I'm someone's WiFi!"
    g.label.contains("contact", true) || g.label.contains("card", true) -> "I'm a business card!"
    g.label.contains("text", true) -> "I'm just text!"
    else -> "Wrong code!"
}

/**
 * A small speech bubble centred above (cx, cy) holding the shout. Rounded rect with a downward tail,
 * text drawn via the native canvas. Bobs gently on the phase so it feels alive.
 */
private fun androidx.compose.ui.graphics.drawscope.DrawScope.drawSpeechBubble(
    cx: Float, cy: Float, text: String, color: androidx.compose.ui.graphics.Color, phase: Float,
) {
    val paint = android.graphics.Paint().apply {
        isAntiAlias = true
        textSize = 30f
        typeface = android.graphics.Typeface.MONOSPACE
        this.color = android.graphics.Color.argb(255, (color.red * 255).toInt(), (color.green * 255).toInt(), (color.blue * 255).toInt())
    }
    val bob = kotlin.math.sin(phase * 2f) * 3f
    val tw = paint.measureText(text)
    val padX = 18f; val padY = 12f
    val bw = tw + padX * 2
    val bh = 30f + padY * 2
    val left = cx - bw / 2f
    val top = cy - bh / 2f + bob
    // bubble background (dark) and outline
    val rect = androidx.compose.ui.geometry.Rect(left, top, left + bw, top + bh)
    val corner = androidx.compose.ui.geometry.CornerRadius(12f, 12f)
    drawRoundRect(
        color = androidx.compose.ui.graphics.Color(0xFF101418),
        topLeft = androidx.compose.ui.geometry.Offset(rect.left, rect.top),
        size = androidx.compose.ui.geometry.Size(bw, bh), cornerRadius = corner,
    )
    drawRoundRect(
        color = color,
        topLeft = androidx.compose.ui.geometry.Offset(rect.left, rect.top),
        size = androidx.compose.ui.geometry.Size(bw, bh), cornerRadius = corner,
        style = androidx.compose.ui.graphics.drawscope.Stroke(width = 3f),
    )
    // downward tail toward the ghost
    val tail = androidx.compose.ui.graphics.Path().apply {
        moveTo(cx - 10f, top + bh)
        lineTo(cx + 10f, top + bh)
        lineTo(cx, top + bh + 16f)
        close()
    }
    drawPath(tail, androidx.compose.ui.graphics.Color(0xFF101418))
    drawPath(tail, color, style = androidx.compose.ui.graphics.drawscope.Stroke(width = 3f))
    // text
    drawContext.canvas.nativeCanvas.drawText(text, left + padX, top + padY + 24f, paint)
}

/**
 * A clean AR "locked on" reticle: four L-shaped corner brackets at the detected quad's corners, with
 * a faint connecting outline. pulse (0..1) gently breathes the bracket length and alpha so it reads as
 * a live lock, not a static box. q is the four corners in view space (TL, TR, BR, BL order).
 */
private fun androidx.compose.ui.graphics.drawscope.DrawScope.drawReticle(
    q: List<androidx.compose.ui.geometry.Offset>, color: androidx.compose.ui.graphics.Color, pulse: Float,
) {
    if (q.size != 4) return
    // faint full outline so the whole code is gently framed
    for (i in 0 until 4) {
        drawLine(color.copy(alpha = 0.25f), q[i], q[(i + 1) % 4], strokeWidth = 2f)
    }
    // bracket length is a fraction of the shorter side, breathing with the pulse
    val side = minOf(
        (q[0] - q[1]).getDistance(), (q[1] - q[2]).getDistance(),
        (q[2] - q[3]).getDistance(), (q[3] - q[0]).getDistance(),
    )
    val len = side * (0.18f + 0.05f * pulse)
    val a = 0.7f + 0.3f * pulse
    for (i in 0 until 4) {
        val p = q[i]
        val nLeft = q[(i + 3) % 4]   // previous corner
        val nRight = q[(i + 1) % 4]  // next corner
        val toL = (nLeft - p).let { it / it.getDistance() }
        val toR = (nRight - p).let { it / it.getDistance() }
        drawLine(color.copy(alpha = a), p, p + toL * len, strokeWidth = 5f)
        drawLine(color.copy(alpha = a), p, p + toR * len, strokeWidth = 5f)
    }
}


/**
 * Draws celebratory fireworks across the frame when an enrol scan succeeds: several bursts at
 * staggered times and fixed pseudo-random positions, each throwing a ring of fading sparks outward.
 * Driven by a single rising clock t, so it keeps going for the ~2.6s the success animation runs.
 */
private fun androidx.compose.ui.graphics.drawscope.DrawScope.drawFireworks(w: Float, h: Float, t: Float) {
    fun frac(x: Float) = x - kotlin.math.floor(x)
    val colors = listOf(
        TerminalGreen,
        androidx.compose.ui.graphics.Color(0xFFFFE066), // gold
        androidx.compose.ui.graphics.Color(0xFF66FFCC), // mint
        androidx.compose.ui.graphics.Color(0xFF66D9FF), // sky
        androidx.compose.ui.graphics.Color(0xFFFF6FB5), // pink
        androidx.compose.ui.graphics.Color(0xFFB388FF), // violet
        androidx.compose.ui.graphics.Color(0xFFFF9E4D), // orange
        androidx.compose.ui.graphics.Color(0xFF4DFFA6), // spring
        Warning,                                        // amber
    )
    val bursts = 12
    for (b in 0 until bursts) {
        val seed = b * 97.13f
        val bx = (0.08f + 0.84f * frac(kotlin.math.sin(seed) * 4391.7f)) * w
        val by = (0.08f + 0.62f * frac(kotlin.math.cos(seed * 1.7f) * 2917.3f)) * h
        val cycle = 1.4f
        val local = (t - b * 0.13f) % cycle   // tighter stagger => more bursts alive at once
        if (local < 0f || local > 1f) continue
        val p = local                          // 0..1 burst progress
        val col = colors[b % colors.size]
        val rays = 12 + (b % 4) * 3            // 12..21 rays, varied per burst
        val radius = p * (80f + (b % 4) * 28f)
        val alpha = (1f - p).coerceIn(0f, 1f)
        val spark = 2.5f + 2f * (1f - p)
        for (i in 0 until rays) {
            val a = (i.toFloat() / rays) * (2f * Math.PI.toFloat()) + b * 0.3f
            val ca = kotlin.math.cos(a); val sa = kotlin.math.sin(a)
            drawLine(
                col.copy(alpha = alpha * 0.55f),
                androidx.compose.ui.geometry.Offset(bx + radius * 0.5f * ca, by + radius * 0.5f * sa),
                androidx.compose.ui.geometry.Offset(bx + radius * ca, by + radius * sa),
                strokeWidth = 2f,
            )
            drawCircle(col.copy(alpha = alpha), radius = spark, center = androidx.compose.ui.geometry.Offset(bx + radius * ca, by + radius * sa))
        }
        // bright flash at the burst centre in its first moments
        if (p < 0.25f) {
            drawCircle(col.copy(alpha = (1f - p / 0.25f) * 0.8f), radius = 5f, center = androidx.compose.ui.geometry.Offset(bx, by))
        }
    }
}
private fun androidx.compose.ui.graphics.drawscope.DrawScope.drawHappyGhost(
    cx: Float, cy: Float, s: Float, color: androidx.compose.ui.graphics.Color,
) {
    val body = ghostBody(cx, cy, s)
    val outline = (s * 0.04f).coerceAtLeast(3f)
    // faint fill so a big ghost reads as solid rather than a wisp, then a crisp outline
    drawPath(body, color.copy(alpha = 0.16f), style = androidx.compose.ui.graphics.drawscope.Fill)
    drawPath(body, color, style = androidx.compose.ui.graphics.drawscope.Stroke(width = outline))
    val eyeY = cy - s * 0.10f
    val eo = s * 0.40f
    // big round happy eyes
    drawCircle(color, radius = s * 0.14f, center = androidx.compose.ui.geometry.Offset(cx - eo, eyeY))
    drawCircle(color, radius = s * 0.14f, center = androidx.compose.ui.geometry.Offset(cx + eo, eyeY))
    // a big upturned smile
    val smile = androidx.compose.ui.graphics.Path().apply {
        moveTo(cx - s * 0.42f, cy + s * 0.28f)
        quadraticTo(cx, cy + s * 0.86f, cx + s * 0.42f, cy + s * 0.28f)
    }
    drawPath(smile, color, style = androidx.compose.ui.graphics.drawscope.Stroke(width = (s * 0.05f).coerceAtLeast(3f)))
}

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

private fun tryDecode(proxy: ImageProxy, frames: com.localghost.app.qr.FrameAssembler): ScanResult {
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
        if (frames.isFrame(text)) {
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
