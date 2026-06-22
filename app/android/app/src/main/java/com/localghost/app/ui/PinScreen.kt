package com.localghost.app.ui

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.layout.*
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.res.painterResource
import com.localghost.app.R
import androidx.compose.ui.hapticfeedback.HapticFeedbackType
import androidx.compose.ui.platform.LocalHapticFeedback
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.Dp
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.localghost.app.ui.theme.*

@Composable
fun PinScreen(busy: Boolean, error: String?, onSubmit: (String) -> Unit) {
    var pin by remember { mutableStateOf("") }
    var reveal by remember { mutableStateOf(false) }
    val haptic = LocalHapticFeedback.current
    fun submit() { if (pin.isNotEmpty() && !busy) { onSubmit(pin); pin = "" } }

    GhostScaffold { pad ->
        Column(
            Modifier.fillMaxSize().padding(pad).padding(horizontal = 24.dp),
            verticalArrangement = Arrangement.Center,
            horizontalAlignment = Alignment.CenterHorizontally,
        ) {
            SectionLabel("AUTH_REQUIRED", Modifier.align(Alignment.Start))
            Spacer(Modifier.height(8.dp))
            Text("ENTER CODE", color = GhostText, style = MaterialTheme.typography.titleMedium)
            Spacer(Modifier.height(20.dp))

            Box(Modifier.fillMaxWidth().widthIn(max = 340.dp)) {
                MaskRow(pin, reveal) { reveal = !reveal }
            }

            Spacer(Modifier.height(24.dp))
            // Keypad caps its width so it stays comfortable on tablets/foldables and shrinks to
            // fit narrow phones. Keys are square, sized from the available row width.
            Box(Modifier.fillMaxWidth(), contentAlignment = Alignment.Center) {
                Keypad(
                    enabled = !busy,
                    onDigit = { if (!busy) { pin += it; haptic.performHapticFeedback(HapticFeedbackType.TextHandleMove) } },
                    onClear = { if (pin.isNotEmpty()) pin = "" },
                    onDelete = { if (pin.isNotEmpty()) pin = pin.dropLast(1) },
                    modifier = Modifier.widthIn(max = 340.dp),
                )
            }

            Spacer(Modifier.height(20.dp))
            GhostButton("OK", { submit() }, enabled = pin.isNotEmpty() && !busy,
                modifier = Modifier.fillMaxWidth().widthIn(max = 340.dp).height(56.dp))

            if (busy) {
                Spacer(Modifier.height(24.dp))
                CircularProgressIndicator(color = TerminalGreen, strokeWidth = 2.dp)
            }
            error?.let {
                Spacer(Modifier.height(16.dp))
                Text("! $it", color = Warning, textAlign = TextAlign.Center,
                    style = MaterialTheme.typography.bodyMedium)
            }
        }
    }
}

@Composable
private fun MaskRow(pin: String, reveal: Boolean, onToggleReveal: () -> Unit) {
    Row(verticalAlignment = Alignment.CenterVertically,
        horizontalArrangement = Arrangement.spacedBy(10.dp)) {
        Box(Modifier.weight(1f), contentAlignment = Alignment.Center) {
            when {
                pin.isEmpty() -> Text(" ", style = MaterialTheme.typography.titleMedium)
                reveal -> Text(pin, color = TerminalGreen, style = MaterialTheme.typography.titleMedium)
                else -> Row(horizontalArrangement = Arrangement.spacedBy(10.dp)) {
                    repeat(pin.length.coerceAtMost(8)) {
                        Text("●", color = TerminalGreen, style = MaterialTheme.typography.titleMedium)
                    }
                    if (pin.length > 8)
                        Text("…", color = TerminalGreen, style = MaterialTheme.typography.titleMedium)
                }
            }
        }
        // eye toggle to reveal what's being typed
        IconButton(onClick = onToggleReveal) {
            Icon(
                painter = painterResource(
                    if (reveal) R.drawable.ic_eye else R.drawable.ic_eye_off),
                contentDescription = if (reveal) "Hide code" else "Show code",
                tint = if (reveal) TerminalGreen else GhostTextDim,
            )
        }
    }
}

@Composable
private fun Keypad(
    enabled: Boolean,
    onDigit: (Char) -> Unit,
    onClear: () -> Unit,
    onDelete: () -> Unit,
    modifier: Modifier = Modifier,
) {
    val gap = 12.dp
    BoxWithConstraints(modifier) {
        // 3 columns; key height tracks width so keys stay square at any screen size.
        val keyW = (maxWidth - gap * 2) / 3
        Column(verticalArrangement = Arrangement.spacedBy(gap),
            horizontalAlignment = Alignment.CenterHorizontally) {
            listOf(listOf('1','2','3'), listOf('4','5','6'), listOf('7','8','9')).forEach { row ->
                Row(horizontalArrangement = Arrangement.spacedBy(gap)) {
                    row.forEach { ch -> DigitKey(ch.toString(), enabled, keyW) { onDigit(ch) } }
                }
            }
            // bottom row: CLEAR (left) · 0 (center) · DEL (right) — symmetric, small labels
            Row(horizontalArrangement = Arrangement.spacedBy(gap)) {
                ActionKey("CLEAR", enabled, keyW, onClear)
                DigitKey("0", enabled, keyW) { onDigit('0') }
                ActionKey("DEL", enabled, keyW, onDelete)
            }
        }
    }
}

@Composable
private fun DigitKey(label: String, enabled: Boolean, size: Dp, onClick: () -> Unit) {
    OutlinedButton(
        onClick = onClick, enabled = enabled, shape = RectangleShape,
        border = BorderStroke(1.dp, if (enabled) TerminalGreen else GhostBorder),
        colors = ButtonDefaults.outlinedButtonColors(
            contentColor = TerminalGreen, disabledContentColor = GhostBorder),
        contentPadding = PaddingValues(0.dp),
        modifier = Modifier.size(size),
    ) { Text(label, fontSize = 22.sp) }
}

@Composable
private fun ActionKey(label: String, enabled: Boolean, size: Dp, onClick: () -> Unit) {
    OutlinedButton(
        onClick = onClick, enabled = enabled, shape = RectangleShape,
        border = BorderStroke(1.dp, GhostBorder),
        colors = ButtonDefaults.outlinedButtonColors(
            contentColor = GhostTextDim, disabledContentColor = GhostBorder),
        contentPadding = PaddingValues(0.dp),
        modifier = Modifier.size(size),
    ) { Text(label, fontSize = 11.sp) }
}
