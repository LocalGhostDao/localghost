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

    /** The phrases for a slot, in the order the surfaces walk them: greeting first, then by weight;
     *  at equal weight a phrase that BELONGS to this hour beats one that is true at any hour (one
     *  coffee outranks thank you at eight, the bill outranks it at eleven), and what is left ties
     *  by pack order, which a native speaker chose. Emergency lines never appear in the rotation ,
     *  they live in their own place on the screen. */
    fun order(pack: PhrasePack, slot: Slot): List<Phrase> {
        val inSlot = pack.phrases.filter { it.inSlot(slot) && it.situation != Situation.EMERGENCY }
        val greeting = inSlot.filter { it.situation == Situation.GREETINGS && slot in it.slots }
            .maxByOrNull { it.weight }
        val rest = inSlot.filter { it !== greeting }
            .sortedWith(compareByDescending<Phrase> { it.weight }.thenByDescending { if (slot in it.slots) 1 else 0 })
        return if (greeting == null) rest else listOf(greeting) + rest
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

    fun pick(pack: PhrasePack, late: Boolean, hour: Int, minute: Int, manualNext: Int): Pick {
        val slot = slotFor(hour, minute, late)
        val list = order(pack, slot)
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
