package com.localghost.app.ui.theme

import androidx.compose.material3.Typography
import androidx.compose.ui.text.TextStyle
import androidx.compose.ui.text.font.FontFamily

// JetBrains Mono optional: drop OFL .ttf files in res/font and swap this for FontFamily(...).
val GhostFont: FontFamily = FontFamily.Monospace

private val base = Typography()
val GhostTypography = Typography(
    displayLarge = base.displayLarge.copy(fontFamily = GhostFont),
    titleLarge = base.titleLarge.copy(fontFamily = GhostFont),
    titleMedium = base.titleMedium.copy(fontFamily = GhostFont),
    bodyLarge = base.bodyLarge.copy(fontFamily = GhostFont),
    bodyMedium = base.bodyMedium.copy(fontFamily = GhostFont),
    labelLarge = base.labelLarge.copy(fontFamily = GhostFont),
    labelMedium = base.labelMedium.copy(fontFamily = GhostFont),
    labelSmall = base.labelSmall.copy(fontFamily = GhostFont),
)
