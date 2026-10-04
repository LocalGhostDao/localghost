package com.localghost.app.ui

/**
 * THE KINDS OF MEMORY, as MEMORIES' chips: me (from my note and the check-ins), what the box
 * distilled (from the chats and notes, my people, my places, what it noticed), each of those on
 * its own, the trips, the days, the outings, and mine (written by hand). Pure, for the tests.
 * Under "all", a day or an outing that is part of a trip (or a day part of an outing away) is
 * shown by the whole, not on its own: [shown] says which rows the list carries.
 */
object MemoryKinds {
    /** Whether a row shows under chip [id]: its kind matches, and under "all" it is not a part of
     *  something else that is in the list itself. */
    fun shown(id: String, kind: String, partOf: String): Boolean =
        matches(id, kind) && (id != "all" || partOf.isEmpty())

    data class Kind(val id: String, val label: String, val kinds: Set<String>)

    val all = listOf(
        Kind("all", "all", emptySet()),
        Kind("me", "me", setOf("me")),
        // what the box distilled: from the chats and notes, the people, the places, what it noticed
        Kind("distilled", "distilled", setOf("distilled", "person", "place", "insight")),
        Kind("people", "people", setOf("person")),
        Kind("places", "places", setOf("place")),
        Kind("noticed", "noticed", setOf("insight")),
        Kind("trips", "trips", setOf("trip")),
        Kind("days", "days", setOf("day", "episode")),
        Kind("outings", "outings", setOf("outing")),
        Kind("yours", "written by me", setOf("user")),
    )

    /** Whether a memory of [kind] shows under the chip [id]. */
    fun matches(id: String, kind: String): Boolean {
        if (id == "all") return true
        return all.firstOrNull { it.id == id }?.kinds?.contains(kind) ?: true
    }

    /** How many memories each chip holds. */
    fun counts(kinds: List<String>): Map<String, Int> =
        all.associate { k -> k.id to (if (k.id == "all") kinds.size else kinds.count { it in k.kinds }) }
}

/** The about-me note's line under it. Pure, for the tests. */
object AboutText {
    fun status(name: String, me: Int, people: Int, pending: Boolean, hasNote: Boolean): String = when {
        !hasNote -> "nothing written yet"
        pending -> "the box reads it within ten minutes (on the GPU)"
        else -> listOfNotNull(
            name.takeIf { it.isNotBlank() }?.let { "you are $it" },
            if (me > 0) "$me about you" else null,
            if (people > 0) (if (people == 1) "1 person" else "$people people") else null,
        ).joinToString(" · ").ifEmpty { "read by the box" }
    }
}
