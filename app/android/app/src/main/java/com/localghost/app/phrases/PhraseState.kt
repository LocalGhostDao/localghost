package com.localghost.app.phrases

import android.content.Context
import android.telephony.TelephonyManager
import java.util.Calendar
import java.util.TimeZone

/**
 * What the person chose, and where we are. Plain SharedPreferences: this is per-phone convenience
 * (a country override, a speaker form, the lock-screen switch), not archive.
 */
object PhraseState {
    private const val PREFS = "lg_phrases"
    private fun p(ctx: Context) = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE)

    /** A country the person pinned (Switzerland while on a French SIM), or "" for automatic. */
    fun countryOverride(ctx: Context): String = p(ctx).getString("country", "") ?: ""
    fun setCountryOverride(ctx: Context, c: String) = p(ctx).edit().putString("country", c.uppercase()).apply()

    /** A language the person pinned within a multi-language country, or "" for the country's first. */
    fun langOverride(ctx: Context): String = p(ctx).getString("lang", "") ?: ""
    fun setLangOverride(ctx: Context, l: String) = p(ctx).edit().putString("lang", l).apply()

    /** The language the card last spoke in a country that HAS a pack , what practice mode at home
     *  falls back to, so the trip's language follows you home. */
    fun lastLang(ctx: Context): String = p(ctx).getString("last_lang", "") ?: ""
    fun setLastLang(ctx: Context, l: String) {
        if (l.isNotEmpty() && lastLang(ctx) != l) p(ctx).edit().putString("last_lang", l).apply()
    }

    fun speakerForm(ctx: Context): SpeakerForm =
        SpeakerForm.entries.firstOrNull { it.name == p(ctx).getString("form", "") } ?: SpeakerForm.NEUTRAL
    fun setSpeakerForm(ctx: Context, f: SpeakerForm) = p(ctx).edit().putString("form", f.name).apply()

    /** Whether ghost.phrased is ON at all. Off until the person says yes , to the offer that
     *  appears when the phone lands in a country that is not home and has a pack, or to the
     *  switch in settings. Off means: no drawer entry, no lock-screen card, no refresh alarm;
     *  the packs sit in the app and cost nothing. A widget placed by hand still draws. */
    fun enabled(ctx: Context): Boolean = p(ctx).getBoolean("enabled", false)
    fun setEnabled(ctx: Context, on: Boolean) = p(ctx).edit().putBoolean("enabled", on).apply()

    /** The lock-screen card (a silent public notification). Off until the person turns it on ,
     *  a permanent notification nobody asked for is the opposite of the ethos. Only ever true
     *  together with [enabled]. */
    fun lockScreenOn(ctx: Context): Boolean = enabled(ctx) && p(ctx).getBoolean("lockscreen", false)
    fun setLockScreenOn(ctx: Context, on: Boolean) = p(ctx).edit().putBoolean("lockscreen", on).apply()

    /** Countries the offer has been made for (accepted or declined): it is made once per
     *  country, never again for the same one. */
    fun offered(ctx: Context, cc: String): Boolean = p(ctx).getBoolean("offered.${cc.uppercase()}", false)
    fun setOffered(ctx: Context, cc: String) = p(ctx).edit().putBoolean("offered.${cc.uppercase()}", true).apply()

    /** The country an offer is currently open for ("" when none): the in-app banner's cue, and
     *  what TURN ON acts on. */
    fun offerPending(ctx: Context): String = p(ctx).getString("offer_pending", "") ?: ""
    fun setOfferPending(ctx: Context, cc: String) = p(ctx).edit().putString("offer_pending", cc.uppercase()).apply()

    /** Promote the card to a LIVE UPDATE where the phone offers it (Android 16+: status-bar chip,
     *  top of the lock screen, always-on display, Samsung's Now Bar). On by default: it is the
     *  same silent card, shown where a glance lands. The OS still needs the person's per-app
     *  permission for live updates, which the PHRASES screen links to. */
    fun liveUpdate(ctx: Context): Boolean = p(ctx).getBoolean("live", true)
    fun setLiveUpdate(ctx: Context, on: Boolean) = p(ctx).edit().putBoolean("live", on).apply()

    /**
     * How the widget looks: opacity of the void behind the words (a lock screen shows a photo
     * through it), text size, which lines show, and the tint. One look for every widget placed;
     * edited from the widget's own configure screen (long-press › settings, or at placement) and
     * from PHRASES › widget › look. Read by [PhraseSurface.updateWidgets] on every redraw.
     */
    data class WidgetLook(
        val opacity: Int = 85,          // 0..100, the background's alpha in percent
        val size: String = "normal",    // small | normal | large
        val showHead: Boolean = true,   // "› morning · Ελληνικά · 36/178"
        val showSay: Boolean = true,    // the pronunciation line
        val showEn: Boolean = true,     // the meaning
        val showButtons: Boolean = true, // [ say ] [ next ]
        val tint: String = "phosphor",  // phosphor | white | amber | ice
    ) {
        /** The accent colour, ARGB. */
        val accent: Int get() = when (tint) {
            "white" -> 0xFFF2F2F2.toInt()
            "amber" -> 0xFFFFB000.toInt()
            "ice" -> 0xFF7FDBFF.toInt()
            else -> 0xFF33FF00.toInt()
        }
        /** The pronunciation line's colour: the accent, a little dimmer for the pale tints. */
        val accentDim: Int get() = when (tint) {
            "white" -> 0xFFB8B8B8.toInt()
            "amber" -> 0xFFC98A00.toInt()
            "ice" -> 0xFF5FA8C8.toInt()
            else -> 0xFF1A8000.toInt()
        }
        /** Text sizes in sp: local, say, en, head/buttons. */
        val sizes: IntArray get() = when (size) {
            "small" -> intArrayOf(18, 11, 11, 10)
            "large" -> intArrayOf(27, 15, 14, 12)
            else -> intArrayOf(22, 13, 12, 11)
        }
        val alpha255: Int get() = (opacity.coerceIn(0, 100) * 255) / 100
    }

    fun widgetLook(ctx: Context): WidgetLook {
        val pr = p(ctx)
        return WidgetLook(
            opacity = pr.getInt("w.opacity", 85),
            size = pr.getString("w.size", "normal") ?: "normal",
            showHead = pr.getBoolean("w.head", true),
            showSay = pr.getBoolean("w.say", true),
            showEn = pr.getBoolean("w.en", true),
            showButtons = pr.getBoolean("w.buttons", true),
            tint = pr.getString("w.tint", "phosphor") ?: "phosphor",
        )
    }

    fun setWidgetLook(ctx: Context, w: WidgetLook) = p(ctx).edit()
        .putInt("w.opacity", w.opacity.coerceIn(0, 100)).putString("w.size", w.size)
        .putBoolean("w.head", w.showHead).putBoolean("w.say", w.showSay).putBoolean("w.en", w.showEn)
        .putBoolean("w.buttons", w.showButtons).putString("w.tint", w.tint).apply()

    /** Manual NEXT taps, scoped to the slot they were made in: a new part of the day starts at
     *  its greeting again. Stored as "<yyyyddd>-<slot>:<count>". */
    fun manualNext(ctx: Context, slotKey: String): Int {
        val v = p(ctx).getString("next", "") ?: ""
        val i = v.lastIndexOf(':')
        if (i < 0 || v.substring(0, i) != slotKey) return 0
        return v.substring(i + 1).toIntOrNull() ?: 0
    }
    fun bumpManualNext(ctx: Context, slotKey: String) {
        val n = manualNext(ctx, slotKey) + 1
        p(ctx).edit().putString("next", "$slotKey:$n").apply()
    }
    fun setManualNext(ctx: Context, slotKey: String, n: Int) = p(ctx).edit().putString("next", "$slotKey:$n").apply()
    fun resetManualNext(ctx: Context) = p(ctx).edit().remove("next").apply()

    /** Below this a phrase is still being learned; at it and above it is KNOWN , out of the walk
     *  except as a review, counted in the progress lines. Three in a row in the drill, or one tap
     *  of GOT IT / a ✓ in the list, which set it straight to this. */
    const val KNOWN_AT = 3

    /** Flashcard scores per phrase id, PER LANGUAGE: "good_morning" is an id in every pack, and
     *  knowing it in Greek says nothing about Japanese. The first build keyed by id alone; those
     *  scores are read as a fallback so nobody's week of breakfasts is lost. */
    fun drillScore(ctx: Context, lang: String, id: String): Int {
        val pr = p(ctx)
        return pr.getInt("drill.$lang.$id", pr.getInt("drill.$id", 0))
    }
    fun setDrillScore(ctx: Context, lang: String, id: String, n: Int) =
        p(ctx).edit().putInt("drill.$lang.$id", n.coerceIn(0, 9)).apply()

    fun isKnown(ctx: Context, lang: String, id: String): Boolean = drillScore(ctx, lang, id) >= KNOWN_AT
    fun setKnown(ctx: Context, lang: String, id: String, known: Boolean) =
        setDrillScore(ctx, lang, id, if (known) KNOWN_AT else 0)

    /** Every phrase id the person knows in this language , the engine's [PhraseEngine.order] input. */
    fun known(ctx: Context, lang: String): Set<String> {
        val prefix = "drill.$lang."
        val scoped = HashMap<String, Int>()
        val legacy = HashMap<String, Int>()
        for ((k, v) in p(ctx).all) {
            if (v !is Int) continue
            if (k.startsWith(prefix)) scoped[k.substring(prefix.length)] = v
            else if (k.startsWith("drill.") && k.indexOf('.', 6) < 0) legacy[k.substring(6)] = v // pre-language key
        }
        val out = HashSet<String>()
        for ((id, n) in legacy) if (id !in scoped && n >= KNOWN_AT) out.add(id)
        for ((id, n) in scoped) if (n >= KNOWN_AT) out.add(id)
        return out
    }

    /** Mark a whole set at once (the LEVELS section's "I know these"). One commit, not one per id. */
    fun setKnownAll(ctx: Context, lang: String, ids: Collection<String>, known: Boolean) {
        val e = p(ctx).edit()
        for (id in ids) e.putInt("drill.$lang.$id", if (known) KNOWN_AT else 0)
        e.apply()
    }
}

/** Where the phone is, without asking it where the person is. */
data class Whereabouts(val country: String, val source: String)

object CountryDetect {
    /**
     * Where the phone is, best source first: the person's own pin; the country of the last
     * location fix when it is recent (the trail's fix, geocoded on the phone , see LocationLog);
     * the mobile network the phone is registered on, read with no permission , the same fact the
     * status bar shows as the carrier name; the SIM's home country; the time zone. A person on
     * Wi-Fi only with a foreign SIM and location off gets their home country, which is why the
     * override exists.
     */
    fun detect(ctx: Context): Whereabouts {
        PhraseState.countryOverride(ctx).takeIf { it.isNotEmpty() }?.let { return Whereabouts(it, "you chose it") }
        val fix = com.localghost.app.sync.LocationLog.lastCountry(ctx)
        // Six hours: long enough to survive a flight's worth of no fixes, short enough that the
        // country on the card is the one outside the window, not the one before the flight.
        if (fix != null && System.currentTimeMillis() / 1000 - fix.second < 6 * 3600) {
            return Whereabouts(fix.first, "your location")
        }
        try {
            val tm = ctx.getSystemService(Context.TELEPHONY_SERVICE) as? TelephonyManager
            tm?.networkCountryIso?.takeIf { it.length == 2 }?.let { return Whereabouts(it.uppercase(), "mobile network") }
            tm?.simCountryIso?.takeIf { it.length == 2 }?.let { return Whereabouts(it.uppercase(), "SIM card") }
        } catch (_: Exception) { }
        zoneCountry(TimeZone.getDefault().id)?.let { return Whereabouts(it, "time zone") }
        return Whereabouts("", "unknown")
    }

    /** The time zone's country for the zones a traveller with these packs is likely to be in. */
    fun zoneCountry(zone: String): String? = when (zone) {
        "Europe/Madrid", "Atlantic/Canary", "Africa/Ceuta" -> "ES"
        "Europe/Paris" -> "FR"
        "Europe/Rome" -> "IT"
        "Europe/Lisbon", "Atlantic/Madeira", "Atlantic/Azores" -> "PT"
        "Europe/Berlin", "Europe/Busingen" -> "DE"
        "Europe/Vienna" -> "AT"
        "Europe/Zurich" -> "CH"
        "Europe/Amsterdam" -> "NL"
        "Europe/Brussels" -> "BE"
        "Europe/Luxembourg" -> "LU"
        "Europe/Athens" -> "GR"
        "Asia/Nicosia", "Asia/Famagusta", "Europe/Nicosia" -> "CY"
        "Europe/Bucharest" -> "RO"
        "Europe/Chisinau" -> "MD"
        "Europe/Istanbul" -> "TR"
        "Europe/Warsaw" -> "PL"
        "Europe/Prague" -> "CZ"
        "Europe/Bratislava" -> "SK"
        "Europe/Zagreb" -> "HR"
        "Europe/Sarajevo" -> "BA"
        "Europe/Podgorica" -> "ME"
        "Europe/Belgrade" -> "RS"
        "Europe/Budapest" -> "HU"
        "Europe/Stockholm" -> "SE"
        "Europe/Copenhagen" -> "DK"
        "Europe/Oslo" -> "NO"
        "Asia/Tokyo" -> "JP"
        "Asia/Seoul" -> "KR"
        "Asia/Shanghai", "Asia/Chongqing", "Asia/Harbin", "Asia/Urumqi" -> "CN"
        "Asia/Bangkok" -> "TH"
        "Asia/Jakarta", "Asia/Makassar", "Asia/Jayapura", "Asia/Pontianak" -> "ID"
        "Asia/Ho_Chi_Minh", "Asia/Saigon" -> "VN"
        "America/Mexico_City", "America/Cancun", "America/Tijuana", "America/Monterrey", "America/Merida", "America/Chihuahua", "America/Hermosillo", "America/Mazatlan" -> "MX"
        "America/Argentina/Buenos_Aires", "America/Buenos_Aires", "America/Argentina/Cordoba", "America/Argentina/Mendoza" -> "AR"
        "America/Santiago", "America/Punta_Arenas" -> "CL"
        "America/Bogota" -> "CO"
        "America/Lima" -> "PE"
        "America/Montevideo" -> "UY"
        "America/Guayaquil" -> "EC"
        "America/La_Paz" -> "BO"
        "America/Asuncion" -> "PY"
        "America/Costa_Rica" -> "CR"
        "America/Panama" -> "PA"
        "America/Santo_Domingo" -> "DO"
        "America/Guatemala" -> "GT"
        "America/Havana" -> "CU"
        "America/Caracas" -> "VE"
        "America/Sao_Paulo", "America/Manaus", "America/Bahia", "America/Fortaleza", "America/Recife", "America/Belem", "America/Cuiaba", "America/Campo_Grande", "America/Porto_Velho", "America/Rio_Branco", "America/Noronha" -> "BR"
        "Africa/Luanda" -> "AO"
        "Africa/Maputo" -> "MZ"
        "Atlantic/Cape_Verde" -> "CV"
        "Africa/Dakar" -> "SN"
        "Africa/Abidjan" -> "CI"
        else -> null
    }

    /** A stable key for "this slot on this day", for scoping the manual NEXT taps. */
    fun slotKey(slot: Slot, cal: Calendar = Calendar.getInstance()): String =
        "${cal.get(Calendar.YEAR)}${cal.get(Calendar.DAY_OF_YEAR)}-${slot.name}"
}
