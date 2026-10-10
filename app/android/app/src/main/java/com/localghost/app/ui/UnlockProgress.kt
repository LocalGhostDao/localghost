package com.localghost.app.ui

import androidx.compose.animation.Crossfade
import androidx.compose.animation.core.RepeatMode
import androidx.compose.animation.core.animateFloat
import androidx.compose.animation.core.animateFloatAsState
import androidx.compose.animation.core.infiniteRepeatable
import androidx.compose.animation.core.rememberInfiniteTransition
import androidx.compose.animation.core.tween
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.LaunchedEffect
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.produceState
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.draw.alpha
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import com.localghost.app.local.TransferRate
import com.localghost.app.net.StageState
import com.localghost.app.net.UnlockClock
import com.localghost.app.net.UnlockClockStore
import com.localghost.app.net.UnlockEstimate
import com.localghost.app.net.UnlockPacer
import com.localghost.app.net.UnlockSnapshot
import com.localghost.app.net.UnlockStage
import com.localghost.app.ui.theme.GhostBorder
import com.localghost.app.ui.theme.GhostTextDim
import com.localghost.app.ui.theme.TerminalDim
import com.localghost.app.ui.theme.TerminalGreen
import com.localghost.app.ui.theme.Warning

/**
 * The unlock (and lock) screen's progress: a percentage and the time left, a bar that moves with
 * time, one line with the step, and under it what the box is doing now and, turning every few
 * seconds, something useful the app can do.
 *
 * The time comes from [UnlockClock]: this phone times every cold unlock and learns what each step
 * takes on this box; a box that still loads its model during unlock sends its own estimate. A warm
 * box replays one of its own cold unlocks step by step, so a quick unlock looks like any other
 * without the phone adding anything. Everything shown is
 * built from the stage stream alone, which the box sends identically for every account, so a real
 * and a duress unlock look the same.
 */
/**
 * Paces a real unlock snapshot so each step is readable: holds every step on screen for at least a
 * moment and walks them in order, even when the box finishes them all in one poll (a warm box
 * replays a cold unlock, secd replay.go). Both the vault rings and [UnlockProgress] are fed this, so
 * the picture and the words move together. A fresh pacer per run (keyed on whether this is a lock).
 */
@Composable
fun rememberPaced(real: UnlockSnapshot): UnlockSnapshot {
    val locking = real.stages.firstOrNull()?.stage == UnlockStage.STOP_SERVICES
    val pacer = remember(locking) { UnlockPacer() }
    // a fast tick so the held steps advance between the box's once-a-second polls
    var frame by remember(locking) { mutableStateOf(0) }
    // and it stops once the unlock is over (done or failed): nothing is held after that
    LaunchedEffect(locking, real.done, real.failed) {
        if (real.done || real.failed != null) return@LaunchedEffect
        while (true) { kotlinx.coroutines.delay(90); frame++ }
    }
    pacer.observe(real)
    return remember(real, frame) { pacer.display() } ?: real
}

@Composable
fun UnlockProgress(snapshot: UnlockSnapshot, modifier: Modifier = Modifier) {
    val ctx = LocalContext.current
    val kind = snapshot.stages.firstOrNull()?.stage // RESOLVE: an unlock; STOP_SERVICES: a lock
    val locking = kind == UnlockStage.STOP_SERVICES
    val clock = remember(kind) { UnlockClock(UnlockClockStore.load(ctx)) }
    val seed = remember(kind) { (System.nanoTime() % 1000).toInt() }
    // a quarter-second clock for the live times; the tip turns every 6 s
    val over = snapshot.done || snapshot.failed != null
    val tick: Long by produceState(0L, over) {
        while (!over) { kotlinx.coroutines.delay(250); value += 1 }
    }
    val est: UnlockEstimate = remember(snapshot, tick) { clock.observe(snapshot); clock.estimate() }
    LaunchedEffect(snapshot.done) {
        if (snapshot.done) clock.learn()?.let { UnlockClockStore.save(ctx, it) }
    }
    // finished as far as the screen goes: the box said ready (and any floor has passed)
    val finished = snapshot.done && est.current == null

    Column(modifier.fillMaxWidth()) {
        // 42%   about 20 s left
        Row(verticalAlignment = Alignment.Bottom) {
            Text("${(est.fraction * 100).toInt()}%", color = TerminalGreen,
                fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.headlineMedium)
            Spacer(Modifier.width(12.dp))
            Text(leftText(snapshot, est, locking, finished), color = if (est.overdue) Warning else GhostTextDim,
                style = MaterialTheme.typography.labelMedium, modifier = Modifier.padding(bottom = 6.dp))
        }
        Spacer(Modifier.height(8.dp))
        ProgressBar(est.fraction, failed = snapshot.failed != null)
        Spacer(Modifier.height(16.dp))

        StepLine(snapshot, est, finished)

        snapshot.failed?.let {
            Spacer(Modifier.height(10.dp))
            Text("! $it", color = Warning, style = MaterialTheme.typography.bodyMedium)
        }
        if (!finished && snapshot.failed == null) {
            Spacer(Modifier.height(14.dp))
            // what the box is doing now: always there, it changes with the step
            // the model's own phase only while the box is really loading it (not while padding)
            Crossfade(targetState = "> " + UnlockTidbits.doing(est.current, snapshot.model.takeIf { !snapshot.done }),
                animationSpec = tween(350), label = "doing") { text ->
                Text(text, color = TerminalDim, style = MaterialTheme.typography.bodySmall, minLines = 2)
            }
            // why this step is not instant, so a step that holds for a second reads as work, not a stall
            Crossfade(targetState = UnlockTidbits.why(est.current), animationSpec = tween(350), label = "why") { why ->
                Text(if (why.isEmpty()) " " else "  $why", color = GhostBorder,
                    style = MaterialTheme.typography.labelSmall, minLines = 2)
            }
            if (!locking) {
                Spacer(Modifier.height(18.dp))
                val turn = (tick / 24).toInt()
                Text("did you know?", color = TerminalGreen, fontFamily = FontFamily.Monospace,
                    style = MaterialTheme.typography.labelSmall)
                Spacer(Modifier.height(4.dp))
                Crossfade(targetState = UnlockTidbits.tip(turn, seed), animationSpec = tween(450), label = "tip") { text ->
                    Text(text, color = GhostTextDim, style = MaterialTheme.typography.bodyMedium, minLines = 3)
                }
            }
        }
    }
}

@Composable
private fun ProgressBar(fraction: Float, failed: Boolean) {
    val shown by animateFloatAsState(fraction.coerceIn(0f, 1f), tween(600), label = "bar")
    Box(Modifier.fillMaxWidth().height(6.dp).background(GhostBorder)) {
        Box(Modifier.fillMaxWidth(shown).fillMaxHeight().background(if (failed) Warning else TerminalGreen))
    }
}

/** One line: "step 4 of 7 · starting database" and that step's time so far (the model's own
 *  percent while it loads). The list of every step was one too many things to read. */
@Composable
private fun StepLine(snap: UnlockSnapshot, est: UnlockEstimate, finished: Boolean) {
    // the last stage (ready / locked) is the arrival, not a step to wait through
    val steps = snap.stages.dropLast(1)
    val cur = est.current
    val idx = steps.indexOfFirst { it.stage == cur }
    val failedAt = snap.stages.firstOrNull { it.state == StageState.ERRORED }?.stage
    val text = when {
        failedAt != null -> "stopped at step ${steps.indexOfFirst { it.stage == failedAt } + 1} of ${steps.size} · ${failedAt.label}"
        finished || cur == null || idx < 0 -> "all ${steps.size} steps done"
        else -> "step ${idx + 1} of ${steps.size} · ${cur.label}"
    }
    // the running step breathes, so a long one still looks alive
    val t = rememberInfiniteTransition(label = "step")
    val pulse = if (!finished && failedAt == null) t.animateFloat(0.55f, 1f, infiniteRepeatable(tween(700), RepeatMode.Reverse), label = "pulse").value else 1f
    val took = cur?.let { est.took[it] }
    val pct = snap.model?.pct?.takeIf { cur == UnlockStage.MODEL && it in 1..99 && !snap.done }
    val right = if (finished || failedAt != null) "" else listOfNotNull(pct?.let { "$it%" }, took?.let { dur(it) }).joinToString("  ")
    Row(Modifier.fillMaxWidth().padding(vertical = 2.dp), verticalAlignment = Alignment.CenterVertically) {
        Text(text, color = if (failedAt != null) Warning else TerminalGreen, fontFamily = FontFamily.Monospace,
            style = MaterialTheme.typography.bodyMedium, modifier = Modifier.weight(1f).alpha(pulse))
        Text(right, color = TerminalDim, fontFamily = FontFamily.Monospace, style = MaterialTheme.typography.labelMedium)
    }
}

// never "it was already open": that is what the box's replay of a cold unlock exists to hide
private fun leftText(s: UnlockSnapshot, e: UnlockEstimate, locking: Boolean, finished: Boolean): String = when {
    s.failed != null -> "stopped"
    finished -> if (locking) "locked" else "ready"
    e.overdue -> "taking longer than usual (${dur(e.took[e.current] ?: 0)} on this step)"
    e.secondsLeft < 0 -> "measuring…"
    !e.confident -> TransferRate.left(e.secondsLeft).replace("about", "roughly") + " (first time on this phone)"
    else -> TransferRate.left(e.secondsLeft)
}

/** 0.4 s, 12 s, 1:05 */
internal fun dur(ms: Long): String = when {
    ms < 10_000 -> String.format(java.util.Locale.US, "%.1f s", ms / 1000.0)
    ms < 60_000 -> "${ms / 1000} s"
    else -> String.format(java.util.Locale.US, "%d:%02d", ms / 60_000, (ms / 1000) % 60)
}
