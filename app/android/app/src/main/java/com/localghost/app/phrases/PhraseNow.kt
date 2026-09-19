package com.localghost.app.phrases

import android.content.Context
import java.util.Calendar

/** Everything a surface needs to draw this moment. Null pack = we know where we are but have no
 *  words for it yet (or do not know where we are at all); the surfaces say so honestly. */
data class Now(
    val where: Whereabouts,
    val pack: PhrasePack?,
    val alternatives: List<PhrasePack>, // other languages of the same country, for the switcher
    val late: Boolean,
    val pick: PhraseEngine.Pick?,
    val slotKey: String,
    val form: SpeakerForm,
) {
    val phrase: Phrase? get() = pick?.phrase
    val slot: Slot get() = pick?.slot ?: Slot.MORNING
    val headline: String
        get() {
            val place = if (where.country.isEmpty()) "somewhere" else CountryNames.of(where.country)
            return "${slot.label} · $place"
        }
}

object PhraseNow {
    /** The current moment, resolved from the phone's clock and whereabouts and the person's choices.
     *  [previewCountry] lets the screen look at another country (learn before you fly) without
     *  touching what the lock screen shows. */
    fun resolve(ctx: Context, previewCountry: String = "", cal: Calendar = Calendar.getInstance()): Now {
        var where = if (previewCountry.isNotEmpty()) Whereabouts(previewCountry.uppercase(), "preview") else CountryDetect.detect(ctx)
        val packs = PhrasePacks.all(ctx)
        var langs = PhraseEngine.langsFor(where.country, packs)
        val chosen = PhraseState.langOverride(ctx)
        if (langs.isEmpty() && packs.isNotEmpty() && previewCountry.isEmpty()) {
            // PRACTICE , at home (London has no pack, and never will) or anywhere without one, the
            // card is not blank: it speaks the language you pinned, else the last one you were
            // travelling in, else a different pack each day. A lock screen that says "no phrases
            // for the United Kingdom" teaches nobody anything.
            val sorted = packs.sortedBy { it.lang }
            val practice = sorted.firstOrNull { it.lang == chosen }
                ?: sorted.firstOrNull { it.lang == PhraseState.lastLang(ctx) }
                ?: sorted[cal.get(Calendar.DAY_OF_YEAR) % sorted.size]
            langs = listOf(practice) + sorted.filter { it.lang != practice.lang }
            where = Whereabouts(practice.countries.firstOrNull() ?: "", "practice")
        }
        val pack = langs.firstOrNull { it.lang == chosen } ?: langs.firstOrNull()
        val form = PhraseState.speakerForm(ctx)
        if (pack == null) {
            return Now(where, null, emptyList(), false,
                null, CountryDetect.slotKey(PhraseEngine.slotFor(cal.get(Calendar.HOUR_OF_DAY), cal.get(Calendar.MINUTE), false), cal), form)
        }
        if (where.source != "practice" && where.source != "preview") PhraseState.setLastLang(ctx, pack.lang)
        val late = PhraseEngine.isLate(pack, where.country)
        val hour = cal.get(Calendar.HOUR_OF_DAY)
        val minute = cal.get(Calendar.MINUTE)
        val slot = PhraseEngine.slotFor(hour, minute, late)
        val key = CountryDetect.slotKey(slot, cal)
        val pick = PhraseEngine.pick(pack, late, hour, minute, PhraseState.manualNext(ctx, key))
        return Now(where, pack, langs, late, pick, key, form)
    }

    /** Milliseconds until the surfaces should redraw: the next rotation tick or the slot boundary,
     *  whichever comes first. Never less than a minute. */
    fun nextRefreshDelayMs(late: Boolean, cal: Calendar = Calendar.getInstance()): Long {
        val hour = cal.get(Calendar.HOUR_OF_DAY)
        val minute = cal.get(Calendar.MINUTE)
        val toEnd = PhraseEngine.minutesToSlotEnd(hour, minute, late)
        val into = PhraseEngine.minutesIntoSlot(hour, minute, late)
        val toTick = PhraseEngine.ROTATE_MINUTES - (into % PhraseEngine.ROTATE_MINUTES)
        val minutes = minOf(toEnd, toTick).coerceAtLeast(1)
        val secondsPast = cal.get(Calendar.SECOND)
        return (minutes * 60L - secondsPast + 5) * 1000L
    }
}
