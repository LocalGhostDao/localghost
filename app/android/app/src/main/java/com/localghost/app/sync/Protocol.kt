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
    companion object {
        val BEGINNING = Cursor(0L, 0L)

        /**
         * The position after a confirmed item: its own date and id, except that the date never
         * passes [nowMs]. The date is the photo's own (MediaStore reads it from the EXIF), and
         * the roll is walked by it: one picture in the camera folder dated next year (a camera
         * clock set wrong, a file put there with a date of its own) used to carry the cursor to
         * next year with it, and nothing taken before that date was offered again , the sync
         * stopped, saying nothing, until the date came round. Held at now, the picture itself
         * is offered again each run (the box answers that it has it, by hash, and it is
         * skipped) and everything taken after it goes through.
         */
        fun afterConfirmed(dateTaken: Long, id: Long, nowMs: Long): Cursor = Cursor(minOf(dateTaken, nowMs), id)

        /**
         * The position the box kept for this phone, as the next run takes it. One that lies more
         * than a day ahead of now was carried there by a future-dated picture before the rule
         * above, and says nothing about what was sent on the way: the run starts from the
         * beginning. That costs reading the roll once (the box answers by hash what it has, and
         * nothing it has is sent again) and leaves no picture behind the cursor.
         */
        fun fromBox(ts: Long, id: Long, nowMs: Long): Cursor =
            if (ts > nowMs + 86_400_000L) BEGINNING else Cursor(ts, id)
    }
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
