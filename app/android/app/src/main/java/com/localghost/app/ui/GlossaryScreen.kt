package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.foundation.lazy.items
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Modifier
import androidx.compose.ui.text.font.FontWeight
import androidx.compose.ui.unit.dp
import com.localghost.app.ui.theme.*

private enum class Register { TECHNICAL, PLAIN }

private data class Term(val name: String, val technical: String, val plain: String)

private data class Section(val label: String, val terms: List<Term>)

private val GLOSSARY = listOf(
    Section("HOW IT WORKS", listOf(
        Term("The split",
            "Two machines. The phone is a stateless client. The box (your hardware) holds the " +
            "model, the index, and all extracted data. The phone authenticates over mTLS, " +
            "streams raw media up, and renders results. It persists no index, no model, no " +
            "memories, no sync cursor.",
            "There are two computers: your phone and a small box you own at home. The box does " +
            "all the thinking and keeps everything. Your phone is just a window into it. If you " +
            "lose the phone, your data is still safe at home on the box."),
        Term("Settings live on the box",
            "Settings, codes, and persona state are box-owned and persona-scoped. The phone " +
            "reads them from ghost.secd and writes changes back; it caches only for offline " +
            "display. Every enrolled device therefore sees the same state.",
            "Your settings aren't stored on the phone. They live on the box, so all your " +
            "devices agree, and a new phone just picks up where you left off."),
        Term("On-phone model (offline)",
            "A small model your box serves to the phone, run locally via llama.cpp. Used only when the box is " +
            "unreachable, or when you force local mode. It has NO access to your life-index " +
            "(that's on the box), so it answers generic questions with limited context. The " +
            "box's full-context RAG stays primary.",
            "Your box can hand the phone a small AI to keep on it. If the box goes offline, the phone " +
            "uses that for general questions. " +
            "It can't see your life (that stays on the box), so it's a backup, not the real " +
            "thing. When the box is back, it takes over again."),
        Term("Why split it",
            "Privacy through architecture, not policy. The phone can be seized, lost, or " +
            "compromised; it holds nothing of value. The box is yours, in your home, behind " +
            "your own keys. Nobody (including us) is in the loop.",
            "Phones get lost and stolen. So nothing important is kept on the phone. Everything " +
            "lives on the box at home, where only you can reach it. No company can see it, " +
            "because no company is involved."),
    )),
    Section("SYNC", listOf(
        Term("Sync",
            "The phone copies new camera-roll photos and videos to the box. Originals stay on " +
            "the phone; the box receives copies for extraction. Runs every 15 minutes over " +
            "Wi-Fi (configurable to mobile) and when you open the app. A cursor on the box " +
            "tracks position, so only new items move.",
            "Your phone sends copies of new photos and videos to the box so it can understand " +
            "them. Your originals stay on your phone. It happens quietly every 15 minutes on " +
            "Wi-Fi, and when you open the app."),
        Term("Ingest",
            "The act of the box receiving and storing a synced item before extraction. Raw " +
            "bytes in; structured memory out.",
            "When the box takes in a photo or video you sent and files it away to look at."),
        Term("Index",
            "The searchable store the daemons build from your media. What retrieval runs " +
            "against. Lives on the box only.",
            "A kind of organised memory the box builds of your life, so it can answer questions " +
            "about it. Kept on the box."),
        Term("Per-device cursor",
            "Sync position is tracked per (persona, device, stream). Each phone advances its " +
            "own cursor; they do not interfere. Enrol two phones and each syncs independently.",
            "If you use more than one phone, the box remembers separately how far each one has " +
            "sent, so they don't trip over each other."),
        Term("Dedup",
            "Content-addressed dedup. Each ingested item is hashed; a hash already present is " +
            "linked, not re-extracted. The same photo from two devices is one memory, " +
            "attributed to both.",
            "If two of your phones have the same photo, the box keeps it once, not twice, but " +
            "remembers it came from both."),
    )),
    Section("DAEMONS", listOf(
        Term("Daemon",
            "A background process on the box. A daemon does one job and stays out of the way. It is " +
            "without surveillance. Each has one job and runs continuously.",
            "A small program on the box that runs on its own and does one job, quietly, all the " +
            "time. Like a helper that never sleeps and never reports to anyone."),
        Term("ghost.framed",
            "Extracts moments from photos and video. Reads EXIF, decodes frames, produces " +
            "structured journal entries.",
            "Looks at your photos and videos and writes down what happened in them."),
        Term("ghost.voiced",
            "Captures and transcribes voice notes.",
            "Records and writes out your voice notes."),
        Term("ghost.cued",
            "Surfaces reflections (patterns worth your attention drawn from the index).",
            "Notices patterns in your life and points them out when useful."),
        Term("ghost.shadowd",
            "Scans messages for manipulation patterns and flags them.",
            "Watches for messages trying to manipulate you and warns you."),
        Term("ghost.synthd",
            "The chat brain. Retrieves relevant memories, injects them as context, and runs " +
            "the local model to answer. RAG over your life.",
            "The part you chat with. It looks up the right memories and answers using them."),
        Term("ghost.watchd",
            "Watches the other daemons. Health, liveness, honesty.",
            "Keeps an eye on the other helpers to make sure they're working."),
        Term("ghost.secd",
            "The keystone. Authentication, enrollment, persona custody, key management. " +
            "Nothing else runs until it does.",
            "The guard at the door. Handles your code, proves the phone is yours, and protects " +
            "the keys."),
    )),
    Section("SECURITY", listOf(
        Term("mTLS",
            "Mutual TLS. Both ends prove identity with certificates: the box proves it's your " +
            "box, the phone proves it's your enrolled phone. No shared passwords on the wire.",
            "A two-way handshake so the phone and box each prove who they are before talking. " +
            "Stops anyone pretending to be either one."),
        Term("Code / PIN",
            "The code is not authentication (the certificate is). It selects which persona the box " +
            "mounts. It derives a key; the box tries to open a volume with it. Right key opens " +
            "the real persona; a decoy key opens a decoy.",
            "Your code doesn't unlock the app so much as choose which set of data the box opens. " +
            "Different codes can open different things."),
        Term("Code behaviours",
            "Each code carries a behaviour: MOUNT REAL opens the persona it belongs to, MOUNT " +
            "DECOY opens a fallback persona, WIPE is a GLOBAL panic erase that destroys the " +
            "master key-encrypting key so every persona's volume becomes noise at once, from " +
            "any persona. \"Real\" is relative to the persona you are in. There is no absolute flag.",
            "Each code does something: open your space, open a fake space, or wipe everything. " +
            "What counts as \"yours\" depends on which code you used. Decoy and wipe codes can be " +
            "changed but never deleted. A wipe code is global: entered from any persona it " +
            "erases everything, every persona at once."),
        Term("Codes are persona-scoped",
            "The code list you can see and manage belongs only to the mounted persona. The box " +
            "cannot enumerate another persona's codes while this one is open. It lacks the " +
            "keys. No view aggregates across personas.",
            "You can only see the codes for the space you're currently in. The box genuinely " +
            "can't show codes from another space, and nobody can prove other spaces exist."),
        Term("Persona / decoy",
            "Separate encrypted volumes on the box. A duress or decoy code mounts a plausible " +
            "alternative while your real data stays sealed. The number of personas is " +
            "unprovable.",
            "You can have a real space and a fake one. Under pressure, a different code opens " +
            "the fake, and nobody can tell the real one exists."),
        Term("Crypto-erase",
            "Wiping by destroying the key, not the bytes. Without the wrapping key the data is " +
            "noise. Instant, irreversible, complete.",
            "To erase, the box throws away the key. The data is still there but unreadable " +
            "forever (like shredding the only translation of a locked book)."),
        Term("Change code = wipe",
            "Re-keying derives the persona key from a new code. The old wrapping key is " +
            "destroyed; the old data cannot be carried forward. Sovereignty means the data is " +
            "bound to the key. There is no recovery. That is the design.",
            "Changing your code makes a new key and destroys the old one. The old data goes " +
            "with it. This is on purpose: it means your code truly controls your data."),
    )),
)

@Composable
fun GlossaryScreen() {
    var register by remember { mutableStateOf(Register.PLAIN) }

    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp),
        verticalArrangement = Arrangement.spacedBy(14.dp)) {
        item {
            Spacer(Modifier.height(12.dp))
            SectionLabel("GLOSSARY")
            Spacer(Modifier.height(8.dp))
            Text("How everything works, and what every word means. Two readings, pick yours.",
                color = GhostTextDim, style = MaterialTheme.typography.bodyMedium)
            Spacer(Modifier.height(12.dp))
            Row {
                Toggle("PLAIN", register == Register.PLAIN) { register = Register.PLAIN }
                Spacer(Modifier.width(12.dp))
                Toggle("TECHNICAL", register == Register.TECHNICAL) { register = Register.TECHNICAL }
            }
            Spacer(Modifier.height(4.dp))
        }

        GLOSSARY.forEach { section ->
            item {
                Spacer(Modifier.height(6.dp))
                SectionLabel(section.label)
            }
            items(section.terms) { term -> TermCard(term, register) }
        }
        item { Spacer(Modifier.height(24.dp)) }
    }
}

@Composable
private fun Toggle(label: String, selected: Boolean, onClick: () -> Unit) {
    androidx.compose.material3.OutlinedButton(
        onClick = onClick,
        shape = RectangleShape,
        border = androidx.compose.foundation.BorderStroke(1.dp, if (selected) TerminalGreen else GhostBorder),
        colors = androidx.compose.material3.ButtonDefaults.outlinedButtonColors(
            contentColor = if (selected) Void else GhostTextDim,
            containerColor = if (selected) TerminalGreen else Void,
        ),
    ) { Text("[ $label ]", style = MaterialTheme.typography.labelMedium) }
}

@Composable
private fun TermCard(term: Term, register: Register) {
    Column(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape)
        .background(VoidLighter).padding(14.dp)) {
        Text(term.name, color = TerminalGreen,
            style = MaterialTheme.typography.titleMedium.copy(fontWeight = FontWeight.Medium))
        Spacer(Modifier.height(6.dp))
        Text(if (register == Register.TECHNICAL) term.technical else term.plain,
            color = GhostText, style = MaterialTheme.typography.bodyMedium)
    }
}
