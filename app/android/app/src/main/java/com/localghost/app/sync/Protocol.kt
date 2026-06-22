package com.localghost.app.sync

enum class Stream(val wire: String) {
    CAMERA("camera"), VOICE("voice"), NOTE("note");
    companion object { fun from(wire: String) = entries.firstOrNull { it.wire == wire } }
}

enum class MediaKind(val wire: String) {
    PHOTO("photo"), VIDEO("video");
    companion object { fun from(wire: String) = entries.firstOrNull { it.wire == wire } }
}

data class Cursor(val dateTaken: Long, val id: Long) {
    companion object { val BEGINNING = Cursor(0L, 0L) }
}

sealed interface Command {
    data class SyncCamera(val kind: MediaKind, val after: Cursor) : Command
    data object RecordVoice : Command
    data object Idle : Command
}

data class CommandResult(
    val stream: Stream?,
    val kind: MediaKind?,
    val itemsSent: Int,
    val bytesSent: Long,
    val error: String? = null,
)
