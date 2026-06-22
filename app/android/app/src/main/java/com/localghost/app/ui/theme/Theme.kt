package com.localghost.app.ui.theme

import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.darkColorScheme
import androidx.compose.runtime.Composable

private val scheme = darkColorScheme(
    primary = TerminalGreen,
    onPrimary = Void,
    background = Void,
    onBackground = GhostText,
    surface = VoidLighter,
    onSurface = GhostText,
    error = Warning,
)

@Composable
fun LocalGhostTheme(content: @Composable () -> Unit) {
    MaterialTheme(colorScheme = scheme, typography = GhostTypography, content = content)
}
