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
    // "thinking…" toggle; not persisted by the box (chat history reloads with it empty, which is
    // honest , the box stores the conversation, not the scratchpad).
    val reasoning: String = "",
    // What the PHONE found on the web for this turn and handed to the box, numbered in the order
    // the box saw them, so a "[2]" in the answer is a link the person can open. Kept with the
    // reply, not the question: it is part of how the answer was made.
    val web: List<com.localghost.app.net.WebSearch.Hit> = emptyList(),
    // What is happening before the first word ("searching the web on this phone…", "3 found, 2
    // read , asking your box…", "reading 1,300 words on the CPU , about 35s"). Shown only while
    // the answer is empty; the first reasoning or answer chunk replaces the message without it.
    val status: String = "",
) {
    enum class Role { USER, GHOST }
}
