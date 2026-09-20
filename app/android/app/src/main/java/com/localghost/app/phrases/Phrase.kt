package com.localghost.app.phrases

/**
 * ghost.phrased , the phrase you are likely to need, in the language around you, at this hour.
 *
 * The model is deliberately small and flat so a native speaker can correct a pack in a text editor:
 * one JSON file per language under assets/phrases/, one object per phrase. Everything the surfaces
 * show comes from here; nothing is fetched, nothing leaves the phone.
 */

/** The parts of a day the engine reasons in. Boundaries are decided by [PhraseEngine.slotFor]. */
enum class Slot(val label: String, val glyph: String) {
    MORNING("MORNING", "◐"),
    MIDDAY("MIDDAY", "●"),
    AFTERNOON("AFTERNOON", "◑"),
    EVENING("EVENING", "◒"),
    NIGHT("NIGHT", "○");

    companion object {
        fun fromKey(k: String): Slot? = entries.firstOrNull { it.name.equals(k, ignoreCase = true) }
    }
}

/** Where a phrase belongs in the phrasebook. Order here is the order the book lists them. */
enum class Situation(val label: String) {
    GREETINGS("greetings"),
    POLITE("being polite"),
    NUMBERS("numbers & time"),
    CAFE("café"),
    RESTAURANT("restaurant"),
    SHOP("shops"),
    TRANSPORT("getting around"),
    HOTEL("hotel"),
    OUT("out and about"),
    SMALLTALK("small talk"),
    OPINION("what you think"),
    LOCAL("sounding local"),
    HELP("when stuck"),
    EMERGENCY("emergency");

    companion object {
        fun fromKey(k: String): Situation? = entries.firstOrNull { it.name.equals(k, ignoreCase = true) }
    }
}

/**
 * How far into a language a phrase sits. The rotation only shows a level once the one below it is
 * mostly KNOWN (see [PhraseEngine.band]), so a pack of two hundred phrases still opens with good
 * morning and one coffee, and a person who already has those gets the next layer instead of the
 * same ten cards for a fortnight. Packs that predate levels are all level 1.
 */
object Levels {
    const val MAX = 4
    private val names = arrayOf("", "survival", "getting by", "conversation", "sounding local")
    fun name(level: Int): String = names.getOrElse(level.coerceIn(1, MAX)) { "" }
    /** A level is done when this share of its phrases is known; then the next one joins the walk. */
    const val DONE_SHARE = 0.6
}

/** The speaker's grammatical form, for languages where a phrase changes with who says it
 *  (obrigado/obrigada, khrap/kha). NEUTRAL uses the pack's default line. */
enum class SpeakerForm(val label: String) {
    NEUTRAL("default"),
    MASCULINE("masculine"),
    FEMININE("feminine"),
}

/**
 * One phrase.
 * @property en        what you mean, in English
 * @property local     the phrase in the local script, the default speaker form
 * @property sayIt     how to say it, written the way an English reader would sound it out ,
 *                     hyphenated syllables, the STRESSED one in capitals
 * @property roman     a romanised spelling for non-Latin scripts; empty for Latin-script languages
 * @property slots     the parts of the day this phrase is useful in; empty means any time
 * @property weight    1..10, how badly you need this one in its slot; the greeting of a slot is 10
 * @property situation the phrasebook chapter
 * @property note      a short cultural aside, or empty
 * @property forms     speaker-form overrides: "masculine" / "feminine" -> local line (and optional
 *                     "masculineSay" / "feminineSay" for the pronunciation)
 * @property level     1..[Levels.MAX]: survival, getting by, conversation, sounding local
 */
data class Phrase(
    val id: String,
    val en: String,
    val local: String,
    val sayIt: String,
    val roman: String = "",
    val slots: Set<Slot> = emptySet(),
    val weight: Int = 5,
    val situation: Situation = Situation.POLITE,
    val note: String = "",
    val forms: Map<String, String> = emptyMap(),
    val level: Int = 1,
) {
    /** The local line for a speaker form, falling back to the default. */
    fun localFor(form: SpeakerForm): String = when (form) {
        SpeakerForm.MASCULINE -> forms["masculine"] ?: local
        SpeakerForm.FEMININE -> forms["feminine"] ?: local
        SpeakerForm.NEUTRAL -> local
    }

    fun sayItFor(form: SpeakerForm): String = when (form) {
        SpeakerForm.MASCULINE -> forms["masculineSay"] ?: sayIt
        SpeakerForm.FEMININE -> forms["feminineSay"] ?: sayIt
        SpeakerForm.NEUTRAL -> sayIt
    }

    fun inSlot(s: Slot): Boolean = slots.isEmpty() || s in slots
}

/** One emergency number line: what it reaches and the digits. */
data class EmergencyNumber(val label: String, val number: String)

/**
 * One language pack.
 * @property lang        BCP-47-ish code, the asset file name (es, pt, ja)
 * @property name        the language in English
 * @property nativeName  the language in itself
 * @property countries   ISO 3166-1 alpha-2 codes where this is a language you will meet
 * @property ttsTag      the locale tag handed to TextToSpeech (pt-PT, ja-JP)
 * @property lateHours   true where dinner starts after nine and the day runs late (ES, IT, GR...)
 * @property scriptNote  one line about the writing system, for the card header; empty for Latin
 */
data class PhrasePack(
    val lang: String,
    val name: String,
    val nativeName: String,
    val countries: List<String>,
    val ttsTag: String,
    val lateHours: Boolean,
    val scriptNote: String,
    val genderedSpeech: Boolean,
    val phrases: List<Phrase>,
    val emergency: List<EmergencyNumber>,
    /** Which countries in [countries] keep late hours, when it is per country rather than per language. */
    val lateCountries: Set<String> = emptySet(),
)
