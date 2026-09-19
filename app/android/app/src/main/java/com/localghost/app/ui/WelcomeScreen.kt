package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Switch
import androidx.compose.material3.SwitchDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import com.localghost.app.ui.theme.*

/** One thing the phone asks the OS for, and why, with where it stands right now. */
data class Grant(val glyph: String, val title: String, val why: String, val state: PermState)

/**
 * The first screen, before any code is scanned: every permission the app will ever want, asked in
 * one place with the reason next to each. Nothing here needs a box. The phrases on the lock screen
 * and the location trail start the moment the person continues, box or no box; the sync and the
 * camera wait for one. Each line stays a switch the person can flip later in the OS settings.
 */
@Composable
fun WelcomeScreen(
    grants: List<Grant>,
    asking: Boolean,
    lockScreenOn: Boolean,
    onLockScreen: (Boolean) -> Unit,
    trailOn: Boolean,
    onTrail: (Boolean) -> Unit,
    onGrant: () -> Unit,
    onSettings: () -> Unit,
    onContinue: () -> Unit,
    onNoBox: () -> Unit,
) {
    val allGranted = grants.all { it.state == PermState.GRANTED }
    val anyBlocked = grants.any { it.state == PermState.BLOCKED }
    GhostScaffold { pad ->
        Column(
            Modifier.fillMaxSize().padding(pad).padding(24.dp).verticalScroll(rememberScrollState()),
            horizontalAlignment = Alignment.Start,
        ) {
            Spacer(Modifier.height(8.dp))
            SectionLabel("BEFORE ANYTHING ELSE")
            Spacer(Modifier.height(8.dp))
            Text("The phone asks for what it needs once, here, with the reason next to each line. " +
                "Every one of them stays a switch you can flip later. Nothing leaves this phone " +
                "except to a box you enrol yourself.",
                color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)

            Spacer(Modifier.height(20.dp))
            grants.forEach { g ->
                GrantRow(g)
                Spacer(Modifier.height(8.dp))
            }

            Spacer(Modifier.height(12.dp))
            GhostButton(
                when {
                    asking -> "ASKING..."
                    allGranted -> "ALL GRANTED"
                    else -> "GRANT ACCESS"
                },
                onClick = onGrant, modifier = Modifier.fillMaxWidth(), enabled = !asking && !allGranted,
            )
            if (anyBlocked) {
                Spacer(Modifier.height(8.dp))
                GhostButton("OPEN APP SETTINGS", onClick = onSettings, modifier = Modifier.fillMaxWidth())
                Spacer(Modifier.height(4.dp))
                Text("A line marked SETTINGS was refused twice; the OS only lets you turn it on from there.",
                    color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            }

            Spacer(Modifier.height(24.dp))
            SectionLabel("FROM THE FIRST MINUTE")
            Spacer(Modifier.height(4.dp))
            ToggleRow("phrase on the lock screen",
                "the sentence you are likely to need right now, in the language around you · silent, changes with the hour",
                lockScreenOn, onLockScreen)
            ToggleRow("location trail",
                "a point every quarter hour, kept on this phone · drawn on your box's map when you have one",
                trailOn, onTrail)

            Spacer(Modifier.height(24.dp))
            GhostButton("CONTINUE", onClick = onContinue, modifier = Modifier.fillMaxWidth(), enabled = !asking)
            Spacer(Modifier.height(8.dp))
            Text("Next: scan the codes on your box, or type its address.",
                color = GhostTextDim, style = MaterialTheme.typography.labelMedium)

            Spacer(Modifier.height(20.dp))
            GhostButton("NO BOX YET , USE THE PHONE ALONE", onClick = onNoBox, modifier = Modifier.fillMaxWidth(), enabled = !asking)
            Spacer(Modifier.height(4.dp))
            Text("Phrases, the trail and on-phone models work without a box. Enrol one whenever.",
                color = GhostTextDim, style = MaterialTheme.typography.labelMedium)

            Spacer(Modifier.height(24.dp))
            Text("The only cloud is you.", color = TerminalDim,
                style = MaterialTheme.typography.labelMedium,
                textAlign = TextAlign.Center, modifier = Modifier.fillMaxWidth())
        }
    }
}

@Composable
private fun GrantRow(g: Grant) {
    val (chip, colour) = when (g.state) {
        PermState.GRANTED -> "[ ON ]" to TerminalGreen
        PermState.DENIED -> "[ OFF ]" to GhostTextDim
        PermState.BLOCKED -> "[ SETTINGS ]" to Warning
    }
    Row(
        Modifier.fillMaxWidth().border(1.dp, if (g.state == PermState.GRANTED) TerminalDim else GhostBorder, RectangleShape)
            .background(VoidLighter).padding(12.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(g.glyph, color = colour, style = MaterialTheme.typography.titleMedium)
        Spacer(Modifier.width(12.dp))
        Column(Modifier.weight(1f)) {
            Text(g.title, color = GhostText, style = MaterialTheme.typography.bodyMedium)
            Text(g.why, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        }
        Spacer(Modifier.width(8.dp))
        Text(chip, color = colour, style = MaterialTheme.typography.labelMedium)
    }
}

@Composable
private fun ToggleRow(label: String, sub: String, checked: Boolean, onChange: (Boolean) -> Unit) {
    Row(Modifier.fillMaxWidth().padding(vertical = 6.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Text(label, color = GhostText, style = MaterialTheme.typography.bodyMedium)
            Text(sub, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        }
        Spacer(Modifier.width(8.dp))
        Switch(
            checked = checked, onCheckedChange = onChange,
            colors = SwitchDefaults.colors(
                checkedThumbColor = Void, checkedTrackColor = TerminalGreen,
                uncheckedThumbColor = GhostTextDim, uncheckedTrackColor = VoidLighter,
                uncheckedBorderColor = GhostBorder,
            ),
        )
    }
}
