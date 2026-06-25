package com.localghost.app.ui

import androidx.compose.foundation.BorderStroke
import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.clickable
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.material3.*
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.Color
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.input.PasswordVisualTransformation
import androidx.compose.ui.unit.dp
import androidx.compose.ui.window.Dialog
import com.localghost.app.net.DeviceInfo
import com.localghost.app.net.PinBehaviour
import com.localghost.app.net.PinEntry
import com.localghost.app.ui.theme.*

@Composable
fun PinManagementScreen(
    pins: Loadable<List<PinEntry>>,
    devices: Loadable<List<DeviceInfo>>,
    onAddPin: (pin: String, behaviour: PinBehaviour, label: String) -> Unit,
    onRemovePin: (id: String) -> Unit,
) {
    var showAdd by remember { mutableStateOf(false) }

    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp),
        verticalArrangement = Arrangement.spacedBy(12.dp)) {
        item {
            Spacer(Modifier.height(12.dp))
            SectionLabel("CODES, THIS PERSONA")
            Spacer(Modifier.height(8.dp))
            Text("These are the codes for the persona you are in. The box cannot show codes " +
                 "from any other persona. It does not hold their keys while this one is open. " +
                 "What you see is all there is, here. Decoy and wipe codes are permanent: you " +
                 "can change them, never remove them. The wipe code is global, it erases " +
                 "every persona at once, from wherever you enter it.",
                 color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
            Spacer(Modifier.height(6.dp))
        }
        when (pins) {
            is Loadable.Loading -> item { LoadingRow("reading codes…") }
            is Loadable.Failed -> item { ErrorLine(pins.reason) }
            is Loadable.Loaded -> items(pins.value) { p -> PinRow(p, onRemovePin) }
        }
        item {
            Spacer(Modifier.height(8.dp))
            GhostButton("ADD CODE", { showAdd = true }, modifier = Modifier.fillMaxWidth())
        }
        item {
            Spacer(Modifier.height(24.dp))
            SectionLabel("DEVICES")
            Spacer(Modifier.height(8.dp))
            Text("Each device enrols separately and syncs on its own cursor. The box dedups " +
                 "by content, so the same photo from two devices is one memory.",
                 color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
            Spacer(Modifier.height(6.dp))
        }
        when (devices) {
            is Loadable.Loading -> item { LoadingRow("reading devices…") }
            is Loadable.Failed -> item { ErrorLine(devices.reason) }
            is Loadable.Loaded -> items(devices.value) { d -> DeviceRow(d) }
        }
        item { Spacer(Modifier.height(24.dp)) }
    }

    if (showAdd) {
        AddPinDialog(
            onAdd = { pin, beh, label -> showAdd = false; onAddPin(pin, beh, label) },
            onDismiss = { showAdd = false },
        )
    }
}

private fun behaviourColor(b: PinBehaviour) = when (b) {
    PinBehaviour.MOUNT_REAL -> TerminalGreen
    PinBehaviour.MOUNT_DECOY -> GhostTextDim
    PinBehaviour.WIPE -> Warning
}

private fun behaviourLabel(b: PinBehaviour) = when (b) {
    PinBehaviour.MOUNT_REAL -> "MOUNT REAL, opens this persona"
    PinBehaviour.MOUNT_DECOY -> "MOUNT DECOY, opens a fallback"
    PinBehaviour.WIPE -> "WIPE (erases EVERYTHING, all personas)"
}

@Composable
private fun PinRow(p: PinEntry, onRemove: (String) -> Unit) {
    // WIPE and decoy pins are permanent escape hatches, they can be re-keyed but never
    // removed, so a persona always keeps its panic options. Only plain MOUNT_REAL pins
    // (and never the last one) are removable.
    val removable = p.behaviour == PinBehaviour.MOUNT_REAL
    Row(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape)
        .background(VoidLighter).padding(14.dp), verticalAlignment = Alignment.CenterVertically) {
        Column(Modifier.weight(1f)) {
            Row(verticalAlignment = Alignment.CenterVertically) {
                Text(p.hint, color = GhostText, style = MaterialTheme.typography.titleMedium)
                Spacer(Modifier.width(10.dp))
                Text(p.label, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            }
            Spacer(Modifier.height(4.dp))
            Text(behaviourLabel(p.behaviour), color = behaviourColor(p.behaviour),
                style = MaterialTheme.typography.labelMedium)
            if (!removable) {
                Spacer(Modifier.height(2.dp))
                Text("permanent, change only", color = TerminalDim,
                    style = MaterialTheme.typography.labelMedium)
            }
        }
        if (removable) {
            Text("REMOVE", color = Warning, style = MaterialTheme.typography.labelMedium,
                modifier = Modifier
                    .clickable { onRemove(p.id) }
                    .border(1.dp, GhostBorder, RectangleShape)
                    .padding(horizontal = 10.dp, vertical = 6.dp))
        }
    }
}

@Composable
private fun DeviceRow(d: DeviceInfo) {
    Column(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape)
        .background(VoidLighter).padding(14.dp)) {
        Row(verticalAlignment = Alignment.CenterVertically) {
            Text(d.name, color = TerminalGreen, style = MaterialTheme.typography.titleMedium)
            if (d.thisDevice) {
                Spacer(Modifier.width(8.dp))
                Text("[ this device ]", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            }
        }
        Spacer(Modifier.height(4.dp))
        Text("${d.photos} photos · ${d.videos} videos · last sync ${d.lastSync}",
            color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
    }
}

@Composable
private fun AddPinDialog(
    onAdd: (String, PinBehaviour, String) -> Unit,
    onDismiss: () -> Unit,
) {
    var pin by remember { mutableStateOf("") }
    var label by remember { mutableStateOf("") }
    var behaviour by remember { mutableStateOf(PinBehaviour.MOUNT_DECOY) }
    val valid = pin.length >= 4

    Dialog(onDismissRequest = onDismiss) {
        Column(Modifier.fillMaxWidth().border(1.dp, TerminalDim, RectangleShape)
            .background(Void).padding(20.dp)) {
            Text("> ADD CODE", color = TerminalGreen, style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(12.dp))
            Text("Adds a code to the persona you are in. Pick what it does when entered.",
                color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
            Spacer(Modifier.height(12.dp))
            field("code (min 4 digits)", pin, KeyboardType.NumberPassword, mask = true) { pin = it }
            Spacer(Modifier.height(8.dp))
            field("label (optional)", label, KeyboardType.Text, mask = false) { label = it }
            Spacer(Modifier.height(14.dp))
            Text("BEHAVIOUR", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
            Spacer(Modifier.height(6.dp))
            BehaviourOption("MOUNT REAL, opens this persona", behaviour == PinBehaviour.MOUNT_REAL,
                TerminalGreen) { behaviour = PinBehaviour.MOUNT_REAL }
            BehaviourOption("MOUNT DECOY, opens a fallback", behaviour == PinBehaviour.MOUNT_DECOY,
                GhostText) { behaviour = PinBehaviour.MOUNT_DECOY }
            BehaviourOption("WIPE (erases EVERYTHING, all personas)", behaviour == PinBehaviour.WIPE,
                Warning) { behaviour = PinBehaviour.WIPE }
            Spacer(Modifier.height(16.dp))
            Row(Modifier.fillMaxWidth(), horizontalArrangement = Arrangement.End) {
                GhostButton("CANCEL", onDismiss)
                Spacer(Modifier.width(12.dp))
                GhostButton("ADD", { if (valid) onAdd(pin, behaviour, label.ifBlank { "code" }) },
                    enabled = valid)
            }
        }
    }
}

@Composable
private fun BehaviourOption(label: String, selected: Boolean, color: Color, onClick: () -> Unit) {
    Row(Modifier.fillMaxWidth()
        .padding(vertical = 4.dp)
        .border(1.dp, if (selected) color else GhostBorder, RectangleShape)
        .clickable { onClick() }
        .padding(12.dp),
        verticalAlignment = Alignment.CenterVertically) {
        Text(if (selected) "◉" else "○", color = if (selected) color else GhostTextDim,
            style = MaterialTheme.typography.bodyMedium, modifier = Modifier.padding(end = 10.dp))
        Text(label, color = if (selected) color else GhostTextDim,
            style = MaterialTheme.typography.bodyMedium)
    }
}

@Composable
private fun field(label: String, value: String, kind: KeyboardType, mask: Boolean, onChange: (String) -> Unit) {
    OutlinedTextField(
        value = value, onValueChange = onChange, singleLine = true,
        label = { Text(label, color = GhostTextDim) },
        keyboardOptions = KeyboardOptions(keyboardType = kind),
        visualTransformation = if (mask) PasswordVisualTransformation() else androidx.compose.ui.text.input.VisualTransformation.None,
        modifier = Modifier.fillMaxWidth(),
        colors = OutlinedTextFieldDefaults.colors(
            focusedTextColor = GhostText, unfocusedTextColor = GhostText, cursorColor = TerminalGreen,
            focusedBorderColor = TerminalGreen, unfocusedBorderColor = GhostBorder,
            focusedContainerColor = VoidLighter, unfocusedContainerColor = VoidLighter),
    )
}
