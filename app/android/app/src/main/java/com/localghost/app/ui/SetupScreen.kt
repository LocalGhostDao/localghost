package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.rememberScrollState
import androidx.compose.foundation.shape.RoundedCornerShape
import androidx.compose.foundation.text.KeyboardOptions
import androidx.compose.foundation.verticalScroll
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.OutlinedTextField
import androidx.compose.material3.OutlinedTextFieldDefaults
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.input.KeyboardType
import androidx.compose.ui.text.style.TextAlign
import androidx.compose.ui.unit.dp
import com.localghost.app.ui.theme.*

/**
 * First-run enrollment. The box's QR carries everything the phone needs , address, fingerprint, and
 * the one-time pairing code , so the normal path is: scan, name the device, enrol. The pairing code
 * is never a field here; it is not typed, not shown, and consumed straight from the scan (the code is
 * a secret, and the only secret you ever type is your PIN, later, on the lock screen). Address and
 * fingerprint stay editable for the rare typed/DDNS case, and the fingerprint is the pinned identity.
 */
@Composable
fun SetupScreen(
    busy: Boolean,
    error: String?,
    prefilledUrl: String = "",
    prefilledCode: String = "",
    prefilledName: String = "",
    onScanQr: () -> Unit,
    onEnroll: (url: String, code: String, deviceName: String, fingerprint: String) -> Unit,
    prefilledFingerprint: String = "",
    onLocalOnly: () -> Unit = {},
) {
    var url by remember(prefilledUrl) { mutableStateOf(prefilledUrl) }
    // The pairing code rides in from the scan and is never surfaced as a field.
    val code = prefilledCode
    var name by remember(prefilledName) { mutableStateOf(prefilledName) }
    var fingerprint by remember(prefilledFingerprint) { mutableStateOf(prefilledFingerprint) }

    val canSubmit = url.isNotBlank() && code.isNotBlank() && name.isNotBlank() &&
        fingerprint.isNotBlank() && !busy

    GhostScaffold { pad ->
        Column(
            Modifier.fillMaxSize().padding(pad).padding(24.dp).verticalScroll(rememberScrollState()),
            horizontalAlignment = Alignment.Start,
        ) {
            Spacer(Modifier.height(8.dp))
            SectionLabel("CONNECT TO YOUR BOX")
            Spacer(Modifier.height(8.dp))
            Text("Scan the box's QR to enrol this device. Nothing of your life is stored here.",
                 color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)

            Spacer(Modifier.height(20.dp))
            GhostButton("SCAN QR FROM THE BOX", onScanQr, modifier = Modifier.fillMaxWidth())

            Spacer(Modifier.height(20.dp))
            Field("BOX ADDRESS", url, { url = it }, "https://your-box.local:8443",
                keyboard = KeyboardType.Uri)

            Spacer(Modifier.height(20.dp))
            Field("DEVICE NAME", name, { name = it }, "e.g. Vlad's S26")

            Spacer(Modifier.height(20.dp))
            Field("BOX FINGERPRINT", fingerprint, { fingerprint = it },
                "SHA-256 (from the QR)", keyboard = KeyboardType.Ascii)

            if (error != null) {
                Spacer(Modifier.height(16.dp))
                ErrorBanner(error)
            }

            Spacer(Modifier.height(28.dp))
            GhostButton(
                if (busy) "ENROLLING..." else "ENROL THIS DEVICE",
                { if (canSubmit) onEnroll(url.trim(), code.trim(), name.trim(), fingerprint.trim()) },
                modifier = Modifier.fillMaxWidth(),
                enabled = canSubmit,
            )

            Spacer(Modifier.height(20.dp))
            Text("The only cloud is you.", color = TerminalDim,
                style = MaterialTheme.typography.labelMedium,
                textAlign = TextAlign.Center, modifier = Modifier.fillMaxWidth())

            Spacer(Modifier.height(16.dp))
            GhostButton("SKIP , USE THE PHONE ALONE", onLocalOnly, modifier = Modifier.fillMaxWidth())
            Spacer(Modifier.height(4.dp))
            Text("No box needed for the phrases, the location trail and on-phone models; chat has no " +
                 "history without one. Enrol a box whenever, the trail catches up.",
                 color = GhostTextDim, style = MaterialTheme.typography.labelMedium,
                 modifier = Modifier.fillMaxWidth())
        }
    }
}

/** A compact, legible error surface: a bordered card so a failure stands out from the form without a
 *  wall of red text. The message itself should be short , the caller keeps enrol errors terse. */
@Composable
private fun ErrorBanner(message: String) {
    Row(
        Modifier
            .fillMaxWidth()
            .background(Warning.copy(alpha = 0.10f), RoundedCornerShape(12.dp))
            .padding(horizontal = 14.dp, vertical = 12.dp),
        verticalAlignment = Alignment.CenterVertically,
    ) {
        Text("!", color = Warning, style = MaterialTheme.typography.titleMedium)
        Spacer(Modifier.width(10.dp))
        Text(message, color = Warning, style = MaterialTheme.typography.bodyMedium)
    }
}

@Composable
private fun Field(
    label: String,
    value: String,
    onChange: (String) -> Unit,
    hint: String,
    keyboard: KeyboardType = KeyboardType.Text,
) {
    Column(Modifier.fillMaxWidth()) {
        Text(label, color = TerminalDim, style = MaterialTheme.typography.labelMedium)
        Spacer(Modifier.height(4.dp))
        OutlinedTextField(
            value = value,
            onValueChange = onChange,
            placeholder = { Text(hint, color = GhostTextDim) },
            singleLine = true,
            keyboardOptions = KeyboardOptions(keyboardType = keyboard),
            shape = RoundedCornerShape(12.dp),
            colors = OutlinedTextFieldDefaults.colors(
                focusedTextColor = GhostText,
                unfocusedTextColor = GhostText,
                focusedBorderColor = TerminalGreen,
                unfocusedBorderColor = GhostBorder,
                cursorColor = TerminalGreen,
            ),
            modifier = Modifier.fillMaxWidth(),
        )
    }
}
