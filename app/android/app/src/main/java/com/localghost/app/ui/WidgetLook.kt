package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.horizontalScroll
import androidx.compose.foundation.layout.Box
import androidx.compose.foundation.layout.Column
import androidx.compose.foundation.layout.Row
import androidx.compose.foundation.layout.Spacer
import androidx.compose.foundation.layout.fillMaxWidth
import androidx.compose.foundation.layout.height
import androidx.compose.foundation.layout.padding
import androidx.compose.foundation.layout.width
import androidx.compose.foundation.rememberScrollState
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Slider
import androidx.compose.material3.SliderDefaults
import androidx.compose.material3.Switch
import androidx.compose.material3.SwitchDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.runtime.getValue
import androidx.compose.runtime.mutableStateOf
import androidx.compose.runtime.remember
import androidx.compose.runtime.setValue
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Brush
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.text.font.FontFamily
import androidx.compose.ui.unit.dp
import androidx.compose.ui.unit.sp
import com.localghost.app.phrases.PhraseNow
import com.localghost.app.phrases.PhraseState
import com.localghost.app.phrases.PhraseSurface
import com.localghost.app.ui.theme.GhostBorder
import com.localghost.app.ui.theme.GhostText
import com.localghost.app.ui.theme.GhostTextDim
import com.localghost.app.ui.theme.TerminalGreen
import com.localghost.app.ui.theme.Void
import com.localghost.app.ui.theme.VoidLighter

/**
 * The widget's look, edited live: opacity of the void behind the words, the text size, which
 * lines show, the tint. A preview above the controls draws the current phrase the way the widget
 * will, over a wallpaper-ish gradient so the opacity means something; every change is saved and
 * pushed to the placed widgets at once (a redraw from the snapshot, cheap), so the real thing on
 * the home screen changes under the sheet too.
 */
@Composable
fun WidgetLookEditor() {
    val ctx = LocalContext.current
    var look by remember { mutableStateOf(PhraseState.widgetLook(ctx)) }
    val snap = remember { PhraseSurface.current(ctx) ?: PhraseSurface.Snapshot.of(PhraseNow.resolve(ctx)) }
    val card = remember(snap) { snap.cards.getOrNull(snap.index(ctx)) }
    fun apply(w: PhraseState.WidgetLook) {
        look = w
        PhraseState.setWidgetLook(ctx, w)
        Thread { PhraseSurface.updateWidgets(ctx.applicationContext) }.start()
    }

    // PREVIEW , the widget as the launcher would draw it, over something that looks like a photo.
    val accent = Color(look.accent)
    val accentDim = Color(look.accentDim)
    val sz = look.sizes
    Box(Modifier.fillMaxWidth().background(
        Brush.linearGradient(listOf(Color(0xFF3A5A8C), Color(0xFF8C5A3A), Color(0xFF2E7D5B))))
        .padding(16.dp)) {
        Column(Modifier.fillMaxWidth()
            .background(Void.copy(alpha = look.opacity / 100f))
            .border(1.dp, GhostBorder.copy(alpha = look.opacity / 100f), RectangleShape)
            .padding(14.dp)) {
            if (look.showHead) Text("› ${snap.headline.lowercase()} · ${snap.lang}" + (if (snap.total > 0) " · ${snap.known}/${snap.total}" else ""),
                color = accentDim, fontFamily = FontFamily.Monospace, fontSize = sz[3].sp, maxLines = 1)
            Spacer(Modifier.height(6.dp))
            Text(card?.local ?: snap.headline.substringAfter("· ", "where are we?"), color = GhostText, fontSize = sz[0].sp, maxLines = 2)
            if (look.showSay) {
                Spacer(Modifier.height(4.dp))
                Text(card?.let { it.say + (if (it.roman.isNotEmpty()) "  ·  " + it.roman else "") } ?: snap.why.substringBefore(" ,"),
                    color = accent, fontFamily = FontFamily.Monospace, fontSize = sz[1].sp, maxLines = 2)
            }
            if (look.showEn) {
                Spacer(Modifier.height(2.dp))
                Text(card?.en ?: "tap to choose", color = GhostTextDim, fontSize = sz[2].sp, maxLines = 1)
            }
            if (look.showButtons && card != null) {
                Spacer(Modifier.height(8.dp))
                Row {
                    Text("[ say ]", color = accent, fontFamily = FontFamily.Monospace, fontSize = sz[3].sp, modifier = Modifier.padding(end = 18.dp))
                    Text("[ next ]", color = accent, fontFamily = FontFamily.Monospace, fontSize = sz[3].sp, modifier = Modifier.padding(end = 18.dp))
                    Text("[ got it ]", color = accent, fontFamily = FontFamily.Monospace, fontSize = sz[3].sp)
                }
            }
        }
    }

    Spacer(Modifier.height(14.dp))
    Text("opacity · ${look.opacity}%" + when {
        look.opacity == 0 -> " · words on the wallpaper"
        look.opacity < 40 -> " · a tint"
        look.opacity < 85 -> " · see-through"
        else -> " · solid"
    }, color = GhostText, style = MaterialTheme.typography.bodyMedium)
    Slider(value = look.opacity / 100f, onValueChange = { apply(look.copy(opacity = (it * 100).toInt())) },
        colors = SliderDefaults.colors(thumbColor = TerminalGreen, activeTrackColor = TerminalGreen, inactiveTrackColor = VoidLighter))

    Spacer(Modifier.height(10.dp))
    Text("text size", color = GhostText, style = MaterialTheme.typography.bodyMedium)
    Spacer(Modifier.height(6.dp))
    LookChips(listOf("small", "normal", "large"), look.size) { apply(look.copy(size = it)) }

    Spacer(Modifier.height(10.dp))
    Text("tint", color = GhostText, style = MaterialTheme.typography.bodyMedium)
    Spacer(Modifier.height(6.dp))
    LookChips(listOf("phosphor", "white", "amber", "ice"), look.tint) { apply(look.copy(tint = it)) }

    Spacer(Modifier.height(10.dp))
    Text("lines", color = GhostText, style = MaterialTheme.typography.bodyMedium)
    LookSwitch("the header , where you are, the language, how many you know", look.showHead) { apply(look.copy(showHead = it)) }
    LookSwitch("how to say it", look.showSay) { apply(look.copy(showSay = it)) }
    LookSwitch("what it means", look.showEn) { apply(look.copy(showEn = it)) }
    LookSwitch("the buttons , say, next, got it", look.showButtons) { apply(look.copy(showButtons = it)) }
    Spacer(Modifier.height(6.dp))
    Text("the phrase itself always shows · the widget grows and shrinks with its lines, so a two-line widget fits the lock screen's smallest slot with the header and buttons off",
        color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
}

@Composable
private fun LookChips(options: List<String>, selected: String, onSelect: (String) -> Unit) {
    Row(Modifier.horizontalScroll(rememberScrollState())) {
        options.forEach { o ->
            val on = o == selected
            Text(o, color = if (on) Void else TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.padding(end = 8.dp)
                    .border(1.dp, TerminalGreen, RectangleShape)
                    .background(if (on) TerminalGreen else Void)
                    .clickable { onSelect(o) }
                    .padding(horizontal = 10.dp, vertical = 6.dp))
        }
    }
}

@Composable
private fun LookSwitch(label: String, checked: Boolean, onChange: (Boolean) -> Unit) {
    Row(Modifier.fillMaxWidth(), verticalAlignment = Alignment.CenterVertically) {
        Text(label, color = GhostTextDim, style = MaterialTheme.typography.labelMedium, modifier = Modifier.weight(1f))
        Spacer(Modifier.width(8.dp))
        Switch(checked = checked, onCheckedChange = onChange,
            colors = SwitchDefaults.colors(checkedThumbColor = Void, checkedTrackColor = TerminalGreen,
                uncheckedThumbColor = GhostTextDim, uncheckedTrackColor = VoidLighter, uncheckedBorderColor = GhostBorder))
    }
}
