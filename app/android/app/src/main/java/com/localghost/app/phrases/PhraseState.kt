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

    fun speakerForm(ctx: Context): SpeakerForm =
        SpeakerForm.entries.firstOrNull { it.name == p(ctx).getString("form", "") } ?: SpeakerForm.NEUTRAL
    fun setSpeakerForm(ctx: Context, f: SpeakerForm) = p(ctx).edit().putString("form", f.name).apply()

    /** The lock-screen card (a silent public notification). Off until the person turns it on ,
     *  a permanent notification nobody asked for is the opposite of the ethos. */
    fun lockScreenOn(ctx: Context): Boolean = p(ctx).getBoolean("lockscreen", false)
    fun setLockScreenOn(ctx: Context, on: Boolean) = p(ctx).edit().putBoolean("lockscreen", on).apply()

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
    fun resetManualNext(ctx: Context) = p(ctx).edit().remove("next").apply()

    /** Flashcard scores per phrase id: how many times in a row it was "got it". */
    fun drillScore(ctx: Context, id: String): Int = p(ctx).getInt("drill.$id", 0)
    fun setDrillScore(ctx: Context, id: String, n: Int) = p(ctx).edit().putInt("drill.$id", n.coerceIn(0, 9)).apply()
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
        com.localghost.app.sync.LocationLog.lastCountry(ctx)?.let { (cc, ts) ->
            // Six hours: long enough to survive a flight's worth of no fixes, short enough that
            // the country on the card is the one outside the window, not the one before the flight.
            if (System.currentTimeMillis() / 1000 - ts < 6 * 3600) return Whereabouts(cc, "your location")
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
