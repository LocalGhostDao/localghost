package com.localghost.app.ui

/**
 * Text another app shared into LocalGhost, as the person is shown it before it becomes a note.
 * Any app on the phone can send text here; nothing is filed until the person has seen it and
 * said so. Pure, for the tests.
 */
object ShareText {
    const val SHOWN = 600

    /** What the dialog shows: the text's start as written (control characters made visible as
     *  spaces, so nothing hides), and how much more there is. */
    fun preview(text: String): String {
        val clean = buildString {
            for (ch in text) append(if (ch == '\n' || !ch.isISOControl()) ch else ' ')
        }.trim()
        val lines = clean.count { it == '\n' } + 1
        val head = if (clean.length <= SHOWN) clean else clean.substring(0, SHOWN).trimEnd() + "…"
        val size = if (clean.length <= SHOWN) "${clean.length} characters" else "${clean.length} characters, the first $SHOWN shown"
        return head + "\n\n" + size + (if (lines > 1) ", $lines lines" else "") + ". Shared from another app."
    }
}
