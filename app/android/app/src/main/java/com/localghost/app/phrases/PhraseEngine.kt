package com.localghost.app.phrases

/**
 * The context engine: country + clock -> the one phrase you need right now, and the order of the
 * ones behind it. Pure functions over plain data , no Android in here, so it runs in a unit test
 * and behaves identically on the widget, the lock screen and the screen.
 *
 * Two rules do most of the work:
 *  1. Every part of the day OPENS with its greeting , the first thing you say to anyone.
 *  2. After that, heaviest first: the phrase you are most likely to need in this slot leads, and the
 *     rest follow in weight order, so "one coffee, please" is second at eight in the morning and
 *     "the bill, please" is second at ten at night.
 * On top, a slow rotation so the same card does not sit on the lock screen for five hours: the
 * cursor moves one phrase every [ROTATE_MINUTES], and the person can tap NEXT to move it by hand.
 */
object PhraseEngine {
    const val ROTATE_MINUTES = 25

    /** Which part of the day it is. Late-hours countries shift everything by about an hour and
     *  stretch the evening , dinner in Madrid is at ten, not seven, and the bill at half past
     *  eleven is still an evening phrase, not a night one. */
    fun slotFor(hour: Int, minute: Int, late: Boolean): Slot {
        val t = hour * 60 + minute
        return if (!late) when {
            t < 5 * 60 -> Slot.NIGHT
            t < 11 * 60 -> Slot.MORNING
            t < 14 * 60 -> Slot.MIDDAY
            t < 18 * 60 -> Slot.AFTERNOON
            t < 23 * 60 -> Slot.EVENING
            else -> Slot.NIGHT
        } else when {
            t < 60 -> Slot.EVENING // the hour after midnight is still dinner's tail
            t < 6 * 60 -> Slot.NIGHT
            t < 12 * 60 -> Slot.MORNING
            t < 15 * 60 -> Slot.MIDDAY
            t < 20 * 60 -> Slot.AFTERNOON
            else -> Slot.EVENING
        }
    }

    /** Minutes until the current slot ends, for scheduling the next refresh. Never zero. */
    fun minutesToSlotEnd(hour: Int, minute: Int, late: Boolean): Int {
        val now = slotFor(hour, minute, late)
        var t = hour * 60 + minute
        var n = 0
        while (n < 24 * 60) {
            t = (t + 1) % (24 * 60)
            n++
            if (slotFor(t / 60, t % 60, late) != now) return n
        }
        return 60
    }

    /** After this many cards you have not learned yet, one you have comes round again, so a known
     *  phrase is met about once an hour and not forgotten by October. */
    const val REVIEW_EVERY = 4

    /** Fewer unlearned cards than this in a slot, and the next level joins early: a walk of two
     *  cards is not a walk. */
    const val MIN_DUE = 5

    /** The person's level band in a pack: 1, climbing one level each time the level below is
     *  mostly known ([Levels.DONE_SHARE]). Emergency lines do not count either way. */
    fun band(pack: PhrasePack, known: Set<String>): Int {
        var b = 1
        while (b < Levels.MAX) {
            val lvl = pack.phrases.filter { it.level == b && it.situation != Situation.EMERGENCY }
            if (lvl.isNotEmpty() && lvl.count { it.id in known } < lvl.size * Levels.DONE_SHARE) break
            b++
        }
        return b
    }

    /** The phrases for a slot, in the order the surfaces walk them: greeting first, then by weight;
     *  at equal weight a phrase that BELONGS to this hour beats one that is true at any hour (one
     *  coffee outranks thank you at eight, the bill outranks it at eleven), then the simpler level,
     *  and what is left ties by pack order, which a native speaker chose. Emergency lines never
     *  appear in the rotation , they live in their own place on the screen.
     *
     *  With [known] (phrase ids the person has marked, or drilled solid), the walk is the cards
     *  STILL TO LEARN within the person's level [band], with one known card slipped in after every
     *  [REVIEW_EVERY] as a review; which known card comes round shifts with [dayKey], so Tuesday's
     *  reviews are not Monday's. The greeting always leads, known or not: it is the first thing
     *  you say to anyone, and the card people glance at most. Everything known and nothing left:
     *  the walk is the known cards, heaviest first , the phrasebook, not a blank. */
    fun order(pack: PhrasePack, slot: Slot, known: Set<String> = emptySet(), dayKey: Int = 0): List<Phrase> {
        val inSlot = pack.phrases.filter { it.inSlot(slot) && it.situation != Situation.EMERGENCY }
        val greeting = inSlot.filter { it.situation == Situation.GREETINGS && slot in it.slots }
            .maxByOrNull { it.weight }
        val byWeight = compareByDescending<Phrase> { it.weight }
            .thenByDescending { if (slot in it.slots) 1 else 0 }
            .thenBy { it.level }
        val rest = inSlot.filter { it !== greeting }
        var b = band(pack, known)
        var due = rest.filter { it.id !in known && it.level <= b }
        while (due.size < MIN_DUE && b < Levels.MAX) { // a thin slot borrows from the next level
            b++
            due = rest.filter { it.id !in known && it.level <= b }
        }
        due = due.sortedWith(byWeight)
        val review = rest.filter { it.id in known }.sortedWith(byWeight)
        if (due.isEmpty()) return listOfNotNull(greeting) + review
        if (review.isEmpty()) return listOfNotNull(greeting) + due
        val out = ArrayList<Phrase>(due.size + due.size / REVIEW_EVERY + 2)
        greeting?.let { out.add(it) }
        var r = ((dayKey % review.size) + review.size) % review.size
        var reviews = 0
        for ((i, p) in due.withIndex()) {
            out.add(p)
            if ((i + 1) % REVIEW_EVERY == 0 && reviews < review.size) { // each known card at most once a walk
                out.add(review[r]); r = (r + 1) % review.size; reviews++
            }
        }
        return out
    }

    /** Known phrases in a pack, for the progress lines, per level: level -> (known, total). */
    fun progress(pack: PhrasePack, known: Set<String>): Map<Int, Pair<Int, Int>> {
        val out = LinkedHashMap<Int, Pair<Int, Int>>()
        for (l in 1..Levels.MAX) {
            val lvl = pack.phrases.filter { it.level == l && it.situation != Situation.EMERGENCY }
            if (lvl.isNotEmpty()) out[l] = lvl.count { it.id in known } to lvl.size
        }
        return out
    }

    /** Where the rotation cursor sits right now: minutes into the slot over ROTATE_MINUTES, plus the
     *  person's manual NEXT taps, modulo the list. The greeting reappears at the top of every slot
     *  because the cursor restarts with the slot. */
    fun cursor(minutesIntoSlot: Int, manualNext: Int, count: Int): Int {
        if (count <= 0) return 0
        val auto = (minutesIntoSlot / ROTATE_MINUTES).coerceAtLeast(0)
        return ((auto + manualNext) % count + count) % count
    }

    /** Minutes since the current slot began , the inverse of [minutesToSlotEnd]. */
    fun minutesIntoSlot(hour: Int, minute: Int, late: Boolean): Int {
        val now = slotFor(hour, minute, late)
        var t = hour * 60 + minute
        var n = 0
        while (n < 24 * 60) {
            val prev = (t - 1 + 24 * 60) % (24 * 60)
            if (slotFor(prev / 60, prev % 60, late) != now) return n
            t = prev
            n++
        }
        return 0
    }

    /** The whole picture for a moment: what to show, and what comes next. */
    data class Pick(val slot: Slot, val list: List<Phrase>, val index: Int) {
        val phrase: Phrase? get() = list.getOrNull(index)
        val next: Phrase? get() = if (list.isEmpty()) null else list[(index + 1) % list.size]
    }

    fun pick(pack: PhrasePack, late: Boolean, hour: Int, minute: Int, manualNext: Int,
             known: Set<String> = emptySet(), dayKey: Int = 0): Pick {
        val slot = slotFor(hour, minute, late)
        val list = order(pack, slot, known, dayKey)
        return Pick(slot, list, cursor(minutesIntoSlot(hour, minute, late), manualNext, list.size))
    }

    /** Which languages a country speaks, from the packs that claim it. Multi-language countries
     *  (Switzerland, Belgium) return several; the first is the default and the person can switch. */
    fun langsFor(country: String, packs: Collection<PhrasePack>): List<PhrasePack> {
        val c = country.uppercase()
        if (c.isEmpty()) return emptyList()
        return packs.filter { c in it.countries }.sortedBy { it.countries.indexOf(c) }
    }

    /** Whether this country keeps late hours under this pack. */
    fun isLate(pack: PhrasePack, country: String): Boolean =
        pack.lateHours || country.uppercase() in pack.lateCountries
}
