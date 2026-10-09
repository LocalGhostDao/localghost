package com.localghost.app.chat

import android.net.Uri

/** Something attached to a chat message — passed as immediate context AND ingested to the
 *  box index (deduped by content hash against camera sync, so no double work). */
data class Attachment(val uri: Uri, val name: String, val kind: Kind) {
    enum class Kind { IMAGE, VOICE }
}

data class Message(
    val role: Role,
    val text: String,
    val memoriesUsed: List<String> = emptyList(),
    val attachments: List<Attachment> = emptyList(),
    // The model's REASONING, streamed live and kept after the answer. Rendered collapsed behind a
    // "thinking…" toggle. The box saves it with the answer (from 30 Sep 2026), so a reopened chat
    // shows it too; older answers reload without it.
    val reasoning: String = "",
    // What the PHONE found on the web for this turn and handed to the box, numbered in the order
    // the box saw them, so a "[2]" in the answer is a link the person can open. Kept with the
    // reply, not the question: it is part of how the answer was made.
    val web: List<com.localghost.app.net.WebSearch.Hit> = emptyList(),
    // What is happening before the first word ("searching the web on this phone…", "3 found, 2
    // read , asking your box…", "reading 1,300 words on the CPU , about 35s"). Shown only while
    // the answer is empty; the first reasoning or answer chunk replaces the message without it.
    val status: String = "",
    // THE TRAIL: every step the turn took, in order, the phone's and the box's ("asked your box what
    // to look for", "sent 'ferry Corfu Albania' to Brave", "the box's Wikipedia: Kassiopi", "the
    // box's own numbers"). Shown under the answer behind a toggle, so what the answer was made of
    // is a tap away after the status line has gone.
    val steps: List<String> = emptyList(),
) {
    enum class Role { USER, GHOST }
}
