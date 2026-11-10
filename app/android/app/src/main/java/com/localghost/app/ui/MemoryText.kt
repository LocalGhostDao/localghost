package com.localghost.app.ui

/** A memory in words: where it came from, and the day its own page can open. Pure, for the tests. */
object MemoryText {
    /** "yours", "from your photos · 24 photos · 2 days", "a day, from your trail and photos · ...".
     *  [outingLine] is MemRow.outingLine (null when the memory is not an outing with photos),
     *  [dayLine] the day's route line from its meta ("" when none). */
    fun origin(kind: String, outingLine: String?, dayLine: String): String = when (kind) {
        "user" -> "yours"
        "me" -> "about me, from my note"
        "person" -> "one of my people"
        "place" -> "a place, counted from your trail and photos"
        "insight" -> "noticed by your box"
        "outing" -> if (outingLine != null) "from your photos · $outingLine" else "from your photos"
        "trip" -> if (outingLine != null) "a trip, from your outings and days · $outingLine" else "a trip, from your outings and days"
        "day" -> if (dayLine.isNotBlank()) "a day, from your trail and photos · $dayLine" else "a day, from your trail and photos"
        "episode" -> "a day"
        else -> "distilled"
    }

    /** The day a memory's page can open, from what it was made of ("day:2026-10-03",
     *  "outing:2026-09-28", "trip:2026-09-12", its first day); "" for any other memory. */
    fun dayOf(ref: String): String {
        val d = ref.substringAfter(':', "")
        return if ((ref.startsWith("day:") || ref.startsWith("outing:") || ref.startsWith("trip:")) && DayText.valid(d)) d else ""
    }

    /** What a part of a trip is called in the trip's list: "the day", "an outing". */
    fun partLabel(kind: String): String = when (kind) {
        "day", "episode" -> "day"
        "outing" -> "outing"
        else -> kind
    }

    /** READ AGAIN. The box checks a distilled memory, and each fact about a person, against the
     *  record it was made from (synthd's verify.go) and keeps what it found in the meta: a
     *  "doubt" on the memory ({at, found, ref}) or on a fact ({t, ref, v, doubt}). These say it
     *  in words for the page and the list. */

    /** The record a ref names, in words: "the conversation", "the note", "the voice note". */
    fun recordName(ref: String): String = when {
        ref.startsWith("chat:") -> "the conversation"
        ref.startsWith("voice:") -> "the voice note"
        ref.startsWith("checkin:") -> "the check-in"
        ref.isEmpty() -> "the record"
        else -> "the note"
    }

    /** The line a doubted memory shows under its body; null when the memory is not in doubt.
     *  [found] is what the record says instead ("" when the record is gone), [at] unix ms. */
    fun doubtLine(ref: String, found: String, at: Long, day: (Long) -> String): String {
        val when_ = if (at > 0) " on " + day(at) else ""
        return if (found.isBlank()) "the box read ${recordName(ref)} again$when_ and could not find this in it"
        else "the box read ${recordName(ref)} again$when_ and is not sure of this. It says: $found"
    }

    /** The line a confirmed memory shows; null when never read again. */
    fun confirmedLine(at: Long, day: (Long) -> String): String? =
        if (at > 0) "read again on " + day(at) + ", as written" else null

    /** A doubted fact of a person, for the list under their memory: "fact , the record says …". */
    fun factDoubt(fact: String, found: String): String =
        if (found.isBlank() || found == "the record does not say this") "$fact , not found in the record it came from"
        else "$fact , the record says: $found"

    /** What the page says of a kind, for the line under the title: "A MEMORY", "AN OUTING", "A DAY",
     *  "ONE OF MY PEOPLE", "A PLACE", "NOTICED", "ABOUT ME", "WRITTEN BY ME". */
    fun kindLabel(kind: String): String = when (kind) {
        "outing" -> "AN OUTING"
        "trip" -> "A TRIP"
        "day", "episode" -> "A DAY"
        "person" -> "ONE OF MY PEOPLE"
        "place" -> "A PLACE"
        "insight" -> "NOTICED BY THE BOX"
        "me" -> "ABOUT ME"
        "user" -> "WRITTEN BY ME"
        else -> "A MEMORY"
    }
}
