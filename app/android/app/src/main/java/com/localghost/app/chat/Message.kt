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
) {
    enum class Role { USER, GHOST }
}
