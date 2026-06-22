package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.Composable
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.unit.dp
import com.localghost.app.ui.theme.*

/** How the OS currently regards a permission we need. BLOCKED = permanently denied; the
 *  in-app prompt no longer appears, only system settings can grant it. */
enum class PermState { GRANTED, DENIED, BLOCKED }

@Composable
fun PermissionBanner(state: PermState, onAct: () -> Unit) {
    if (state == PermState.GRANTED) return
    val msg: String
    val action: String
    when (state) {
        PermState.BLOCKED -> {
            msg = "ghost.framed can't read your camera roll — access is blocked in system settings."
            action = "OPEN SETTINGS"
        }
        else -> {
            msg = "ghost.framed can't read your camera roll. Nothing syncs until you grant access."
            action = "GRANT"
        }
    }
    Row(
        Modifier.fillMaxWidth().padding(horizontal = 12.dp, vertical = 6.dp)
            .border(1.dp, Warning, RectangleShape).background(VoidLighter)
            .clickable { onAct() }.padding(12.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text(msg, color = Warning, style = MaterialTheme.typography.labelMedium,
            modifier = Modifier.weight(1f))
        Spacer(Modifier.width(10.dp))
        Text("[ $action ]", color = Warning, style = MaterialTheme.typography.labelMedium)
    }
}
