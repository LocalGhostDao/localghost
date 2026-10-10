package com.localghost.app.checkin

/**
 * The DAILY CHECK-IN's vocabulary and its small rules, kept apart from the screen so they can be
 * tested. Pure Kotlin: no Android.
 *
 * The feelings come in the four quadrants of the usual mood meter (pleasant or not, lots of energy
 * or little) plus a row for where your head is. The card shows a QUICK ROW first (the box's guesses
 * from the day, then your own usual ones, then a few common ones) and the full grouped list behind
 * "more", so a check-in is two taps on most days and still has the word for the odd one.
 *
 * PRESELECTED: the box's first two guesses (short sleep, a long walk, a day by the sea...) are
 * ticked before you look, marked as the box's, one tap to untick. The check-in text records them
 * ("Preselected: tired") so a later look at the moods can tell a guess left standing from a
 * feeling picked.
 */
object Feelings {
    data class Group(val label: String, val hint: String, val feelings: List<String>)

    val groups = listOf(
        Group("bright", "pleasant, lots of energy",
            listOf("happy", "excited", "energised", "proud", "inspired", "playful", "confident", "hopeful")),
        Group("easy", "pleasant, not much energy",
            listOf("calm", "content", "grateful", "relaxed", "rested", "loved", "connected", "peaceful")),
        Group("tense", "unpleasant, lots of energy",
            listOf("stressed", "anxious", "frustrated", "irritable", "restless", "overwhelmed", "angry", "worried")),
        Group("heavy", "unpleasant, not much energy",
            listOf("tired", "low", "sad", "lonely", "bored", "drained", "disappointed", "unwell")),
        Group("mind", "where your head is",
            listOf("focused", "curious", "reflective", "nostalgic", "distracted", "unsure")),
    )

    val all: List<String> = groups.flatMap { it.feelings }

    /** Up to this many feelings a day: enough for a mixed day, few enough to mean something. */
    const val MAX_PICKS = 4

    /** How many of the box's guesses are ticked before the person looks. */
    const val PRESELECT = 2

    /** The quick row's length, and what fills it when the box and the history say little. */
    const val QUICK = 8
    val common = listOf("calm", "happy", "tired", "stressed", "focused", "grateful", "anxious", "low")

    /** The person's own usual feelings, most picked first, from past check-ins ("a, b, c" each). */
    fun usual(history: List<String>): List<String> {
        val count = LinkedHashMap<String, Int>()
        for (line in history) {
            for (raw in line.split(',')) {
                val f = raw.trim().lowercase()
                if (f.isEmpty() || f.startsWith("(")) continue // "(unspecified)"
                count[f] = (count[f] ?: 0) + 1
            }
        }
        return count.entries.sortedByDescending { it.value }.map { it.key }
    }

    /** The quick row: the box's guesses, then the usual ones, then the common ones; no repeats. */
    fun quick(suggested: List<String>, usual: List<String>, n: Int = QUICK): List<String> {
        val out = ArrayList<String>()
        for (f in suggested + usual + common) {
            if (out.size >= n) break
            if (f !in out) out.add(f)
        }
        return out
    }

    /** What is ticked before the person looks: the box's first guesses. */
    fun preselect(suggested: List<String>): List<String> = suggested.distinct().take(PRESELECT)

    /** Tap on a feeling: untick it, or tick it if there is room. Returns the new list. */
    fun toggle(picked: List<String>, f: String): List<String> = when {
        f in picked -> picked - f
        picked.size >= MAX_PICKS -> picked
        else -> picked + f
    }

    /** Chips into rows that fit a phone's width: at most [maxChars] of text a row (a chip is its
     *  word plus two for its brackets), and never more than [maxPer]. */
    fun rows(words: List<String>, maxChars: Int = 30, maxPer: Int = 4): List<List<String>> {
        val out = ArrayList<List<String>>()
        var row = ArrayList<String>()
        var len = 0
        for (w in words) {
            val add = w.length + 3
            if (row.isNotEmpty() && (len + add > maxChars || row.size >= maxPer)) {
                out.add(row); row = ArrayList(); len = 0
            }
            row.add(w); len += add
        }
        if (row.isNotEmpty()) out.add(row)
        return out
    }

    /** The group a feeling belongs to ("bright", "easy", "tense", "heavy", "mind"), "" for a word
     *  the card never offered (an old check-in, a hand-written one). */
    fun groupOf(f: String): String = groups.firstOrNull { f.trim().lowercase() in it.feelings }?.label ?: ""

    /** The group's mark, for the strip of days and the rows of past check-ins: the pleasant
     *  quadrants point up, the unpleasant down, the mind row is hollow, nothing picked is a dot. */
    fun mark(group: String): String = when (group) {
        "bright" -> "▲"
        "easy" -> "●"
        "tense" -> "◆"
        "heavy" -> "▼"
        "mind" -> "◇"
        else -> "·"
    }

    /** The feelings of a check-in line ("calm, tired"), in the order picked; "(unspecified)" is none. */
    fun picks(feelings: String): List<String> =
        feelings.split(',').map { it.trim().lowercase() }.filter { it.isNotEmpty() && !it.startsWith("(") }

    /** The group a day is told by: the first feeling the person picked themselves (the box's guesses
     *  left standing count after those), "" when none was picked. */
    fun tone(feelings: String, preselected: String = ""): String {
        val p = picks(feelings)
        val guessed = picks(preselected).toSet()
        val own = p.firstOrNull { it !in guessed } ?: p.firstOrNull() ?: return ""
        return groupOf(own)
    }

    /** One cell of the strip: a day, what it was told by, whether it was checked in at all. */
    data class Cell(val day: String, val tone: String, val checked: Boolean)

    /** The last [n] days ending on [today], oldest first, each with its tone from [toneByDay]. A
     *  day without a check-in is unchecked and shows as a dot: no streaks, no count, no guilt. */
    fun strip(today: String, toneByDay: Map<String, String>, n: Int = 14): List<Cell> {
        val out = ArrayList<Cell>(n)
        for (i in n - 1 downTo 0) {
            val d = shiftDay(today, -i)
            val t = toneByDay[d]
            out.add(Cell(d, t ?: "", t != null))
        }
        return out
    }

    /** The day of the month a cell shows under its mark ("4"), and the weekday's initial. */
    fun dayNumber(day: String): String = day.substringAfterLast('-').trimStart('0').ifEmpty { "0" }

    fun weekdayInitial(day: String): String {
        val c = calendarOf(day) ?: return ""
        return arrayOf("S", "M", "T", "W", "T", "F", "S")[c.get(java.util.Calendar.DAY_OF_WEEK) - 1]
    }

    /** "Fri 3 Oct" for a past check-in's row. */
    fun shortDay(day: String): String {
        val c = calendarOf(day) ?: return day
        return java.text.SimpleDateFormat("EEE d MMM", java.util.Locale.UK).format(c.time)
    }

    /** What recurred in the check-ins given (a month's, say): "calm ×6 · tired ×4 · focused ×3",
     *  the three most picked; "" when fewer than two check-ins have a feeling. */
    fun recurring(history: List<String>, n: Int = 3): String {
        val count = LinkedHashMap<String, Int>()
        var lines = 0
        for (line in history) {
            val p = picks(line)
            if (p.isEmpty()) continue
            lines++
            for (f in p) count[f] = (count[f] ?: 0) + 1
        }
        if (lines < 2) return ""
        return count.entries.sortedByDescending { it.value }.take(n).joinToString(" · ") { "${it.key} ×${it.value}" }
    }

    private fun calendarOf(day: String): java.util.Calendar? {
        val m = Regex("""^(\d{4})-(\d{2})-(\d{2})$""").find(day) ?: return null
        val c = java.util.Calendar.getInstance(java.util.TimeZone.getTimeZone("UTC"), java.util.Locale.UK)
        c.clear()
        c.set(m.groupValues[1].toInt(), m.groupValues[2].toInt() - 1, m.groupValues[3].toInt())
        return c
    }

    private fun shiftDay(day: String, n: Int): String {
        val c = calendarOf(day) ?: return day
        c.add(java.util.Calendar.DAY_OF_MONTH, n)
        return "%04d-%02d-%02d".format(java.util.Locale.US, c.get(java.util.Calendar.YEAR),
            c.get(java.util.Calendar.MONTH) + 1, c.get(java.util.Calendar.DAY_OF_MONTH))
    }

    /** m:ss for a voice note's length. */
    fun clock(ms: Long): String {
        val s = (ms + 500) / 1000
        return "%d:%02d".format(s / 60, s % 60)
    }

    /**
     * The check-in as the journal keeps it (POST /v1/notes). The box parses these lines back
     * (CheckinHistory) and synthd reads "Feeling:" into the day's summary; keep the line names.
     */
    fun checkinText(day: String, picked: List<String>, preselected: List<String>, why: String,
                    voiceId: String?, voiceMs: Long): String {
        val b = StringBuilder("Daily check-in ").append(day)
        b.append("\nFeeling: ").append(if (picked.isEmpty()) "(unspecified)" else picked.joinToString(", "))
        if (preselected.isNotEmpty()) b.append("\nPreselected: ").append(preselected.joinToString(", "))
        if (why.isNotBlank()) b.append("\nWhy: ").append(why.trim())
        if (!voiceId.isNullOrBlank()) b.append("\nVoice: ").append(voiceId).append(' ').append(clock(voiceMs))
        return b.toString()
    }

    /** Before this hour the page opens on yesterday when yesterday is still empty: at one in the
     *  morning the day being told is the one just ended, not the one the clock has started. */
    const val SMALL_HOURS = 5

    /** The day the check-in page opens on: [yesterday] in the small hours while neither it nor
     *  today is checked in, [today] otherwise. The person can still pick any empty day. */
    fun defaultDay(today: String, yesterday: String, hour: Int, checked: Set<String>): String =
        if (hour < SMALL_HOURS && yesterday !in checked && today !in checked) yesterday else today

    /** The day as a word for the page's lines: "today", "yesterday", else "that day" (the heading
     *  carries the date). [ago] is DayText.ago(day, today). */
    fun dayWord(ago: String): String = when (ago) {
        "today", "yesterday" -> ago
        else -> "that day"
    }

    /** The page's subtitle: present tense for today, past for a day gone. */
    fun subtitle(ago: String): String = when (ago) {
        "today" -> "how are you feeling today, and why · kept on your box"
        else -> "how were you feeling ${dayWord(ago)}, and why · kept on your box"
    }
}
