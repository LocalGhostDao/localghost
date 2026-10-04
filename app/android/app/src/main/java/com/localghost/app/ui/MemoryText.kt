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
