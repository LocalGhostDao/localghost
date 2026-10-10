package com.localghost.app.ui

import android.content.Intent
import android.os.PowerManager
import android.provider.Settings
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import com.localghost.app.ui.theme.*

/**
 * PERMISSIONS: everything the phone asks the OS for, in one place, with the reason next to each
 * line and where it stands. The same rows the welcome showed (MainActivity.welcomeGrants), the
 * same chain behind GRANT ALL (one dialog after another, in the order the OS allows), plus the
 * two that live outside that chain: Health Connect, which has its own sheet, and the battery,
 * which is a setting the OS keeps rather than a grant. A line the OS will not ask about again is
 * marked SETTINGS and OPEN APP SETTINGS goes to the one place it can be turned on.
 */
@Composable
fun PermissionsScreen(grants: List<Grant>, asking: Boolean, onGrantAll: () -> Unit, onAppSettings: () -> Unit) {
    val ctx = LocalContext.current
    val allGranted = grants.all { it.state == PermState.GRANTED }
    val anyBlocked = grants.any { it.state == PermState.BLOCKED }
    Column(Modifier.fillMaxSize().padding(horizontal = 20.dp).verticalScroll(rememberScrollState())) {
        Spacer(Modifier.height(12.dp))
        Row(verticalAlignment = Alignment.CenterVertically) {
            SectionLabel("PERMISSIONS")
            InfoButton("permissions")
        }
        Text("what the phone asks the OS for, and why · each one stays a switch", color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        Spacer(Modifier.height(14.dp))
        grants.forEach { g ->
            GrantRow(g)
            Spacer(Modifier.height(8.dp))
        }
        Spacer(Modifier.height(6.dp))
        GhostButton(when {
            asking -> "ASKING…"
            allGranted -> "ALL GRANTED"
            else -> "GRANT ALL"
        }, onClick = onGrantAll, modifier = Modifier.fillMaxWidth(), enabled = !asking && !allGranted)
        if (anyBlocked) {
            Spacer(Modifier.height(8.dp))
            GhostButton("OPEN APP SETTINGS", onClick = onAppSettings, modifier = Modifier.fillMaxWidth())
            Spacer(Modifier.height(4.dp))
            Text("a line marked SETTINGS was refused twice; the OS only lets you turn it on from there",
                color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        } else {
            Spacer(Modifier.height(4.dp))
            Text("[ app settings on the phone › ]", color = TerminalGreen, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier.clickable { onAppSettings() }.padding(vertical = 4.dp))
        }

        Spacer(Modifier.height(24.dp))
        SectionLabel("HEALTH CONNECT")
        Text("steps, sleep, exercise, heart rate and the rest, read from Health Connect and sent to your box every six hours · its own sheet, not the OS dialog",
            color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        Spacer(Modifier.height(8.dp))
        HealthSection()

        Spacer(Modifier.height(24.dp))
        SectionLabel("BATTERY")
        BatteryRow(ctx)
        Spacer(Modifier.height(24.dp))
    }
}

/** The battery: not a grant but the one OS setting that quietly stops the trail and the sync
 *  when the app is in the background. Read on every visit; the row opens the OS list. */
@Composable
private fun BatteryRow(ctx: android.content.Context) {
    var tick by remember { mutableIntStateOf(0) }
    val unrestricted = remember(tick) {
        runCatching { (ctx.getSystemService(android.content.Context.POWER_SERVICE) as PowerManager).isIgnoringBatteryOptimizations(ctx.packageName) }.getOrDefault(false)
    }
    val colour = if (unrestricted) TerminalGreen else Warning
    Row(Modifier.fillMaxWidth().border(1.dp, if (unrestricted) TerminalDim else GhostBorder, RectangleShape).background(VoidLighter)
        .clickable {
            runCatching { ctx.startActivity(Intent(Settings.ACTION_IGNORE_BATTERY_OPTIMIZATION_SETTINGS).addFlags(Intent.FLAG_ACTIVITY_NEW_TASK)) }
            tick++
        }.padding(12.dp), verticalAlignment = Alignment.CenterVertically) {
        Text("◈", color = colour, style = MaterialTheme.typography.titleMedium)
        Spacer(Modifier.width(12.dp))
        Column(Modifier.weight(1f)) {
            Text("battery, unrestricted", color = GhostText, style = MaterialTheme.typography.bodyMedium)
            Text("the trail's quarter-hour points and the sync keep going with the app closed; 'optimised' lets the OS stop them",
                color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
        }
        Spacer(Modifier.width(8.dp))
        Text(if (unrestricted) "[ ON ]" else "[ OPEN ]", color = colour, style = MaterialTheme.typography.labelMedium)
    }
}
