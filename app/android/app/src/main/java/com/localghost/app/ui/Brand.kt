package com.localghost.app.ui

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.PaddingValues
import androidx.compose.foundation.layout.WindowInsets
import androidx.compose.foundation.layout.asPaddingValues
import androidx.compose.foundation.layout.fillMaxSize
import androidx.compose.foundation.layout.systemBars
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.material3.ButtonDefaults
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedButton
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Shadow
import androidx.compose.ui.geometry.Offset
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.unit.dp
import com.localghost.app.ui.theme.GhostBorder
import com.localghost.app.ui.theme.TerminalDim
import com.localghost.app.ui.theme.TerminalGreen
import com.localghost.app.ui.theme.Void

/**
 * Root container. Provides real system-bar insets as the PaddingValues passed to content,
 * so nothing draws under the clock/battery/nav bar. Screens apply this padding.
 */
@Composable
fun GhostScaffold(content: @Composable (PaddingValues) -> Unit) {
    Box(Modifier.fillMaxSize().background(Void)) {
        content(WindowInsets.systemBars.asPaddingValues())
    }
}

@Composable
fun GhostButton(
    label: String,
    onClick: () -> Unit,
    modifier: Modifier = Modifier,
    enabled: Boolean = true,
) {
    OutlinedButton(
        onClick = onClick,
        modifier = modifier,
        enabled = enabled,
        shape = RectangleShape,
        border = BorderStroke(1.dp, if (enabled) TerminalGreen else GhostBorder),
        colors = ButtonDefaults.outlinedButtonColors(
            contentColor = TerminalGreen,
            disabledContentColor = GhostBorder,
        ),
    ) { Text("[ $label ]") }
}

@Composable
fun SectionLabel(text: String, modifier: Modifier = Modifier) {
    Text("> $text", color = TerminalDim,
        style = MaterialTheme.typography.labelMedium, modifier = modifier)
}

@Composable
fun TerminalPrompt(text: String) {
    Text("ghost@localghost:~ $text", color = TerminalGreen,
        style = MaterialTheme.typography.bodyMedium)
}

/** Phosphor glow for hero text. */
fun glow(base: TextStyle): TextStyle =
    base.copy(shadow = Shadow(color = TerminalGreen, offset = Offset(0f, 0f), blurRadius = 18f))
