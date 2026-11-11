package com.localghost.app.net

/**
 * The last look at a search before it leaves the phone. The box's model writes the searches
 * (the plan) and the box holds them to its own rule first; this is the same rule again, here,
 * where the request is actually made, so that a search never depends on one check or on a model
 * having done as it was told. A search is withheld when it speaks in the first person, holds an
 * e-mail address, or holds seven digits or more in a run (a phone number, an account). Pure;
 * the words are tested.
 */
object QueryGuard {
    private val firstPerson = Regex(
        "\\b(my|mine|myself|our|ours|i'm|i’m|i've|i’ve|i'd|i'll)\\b|" +
        "\\b(?:i|we)\\s+(?:am|are|was|were|have|had|did|do|went|met|live|lived|work|worked|need|want|bought|got|saw|said|told|pay|paid)\\b|" +
        "\\b(?:did|do|have|had|can|could|should|would|will|am|was)\\s+(?:i|we)\\b",
        RegexOption.IGNORE_CASE)
    private val email = Regex("[^\\s@]+@[^\\s@]+\\.[^\\s@]+")
    private val digitRun = Regex("\\d[\\d .()+-]{5,}\\d")

    /** Whether [query] may be sent to a search engine. */
    fun ok(query: String): Boolean {
        val q = query.trim()
        if (q.isEmpty() || q.length > 160) return false
        if (firstPerson.containsMatchIn(q) || email.containsMatchIn(q)) return false
        if (digitRun.findAll(q).any { run -> run.value.count { it.isDigit() } >= 7 }) return false
        return true
    }

    /** The searches that may leave, in their order. */
    fun filter(queries: List<String>): List<String> = queries.filter { ok(it) }
}
