package com.localghost.app.phrases

import android.content.Context
import org.json.JSONArray
import org.json.JSONObject

/**
 * The packs on disk: assets/phrases/index.json names them, assets/phrases/<lang>.json holds each.
 * Parsed once per process with the platform's org.json (no dependency), kept in memory , the whole
 * catalogue is a few hundred KB of text. A malformed pack is skipped and named in logcat, never
 * fatal: one broken file must not take the feature down for every other language.
 */
object PhrasePacks {
    @Volatile private var cache: List<PhrasePack>? = null

    fun all(ctx: Context): List<PhrasePack> {
        cache?.let { return it }
        synchronized(this) {
            cache?.let { return it }
            val loaded = load(ctx)
            cache = loaded
            return loaded
        }
    }

    fun byLang(ctx: Context, lang: String): PhrasePack? = all(ctx).firstOrNull { it.lang == lang }

    /** Every country any pack claims, with the packs that claim it , the picker's list. */
    fun countries(ctx: Context): Map<String, List<PhrasePack>> {
        val out = LinkedHashMap<String, MutableList<PhrasePack>>()
        for (p in all(ctx)) for (c in p.countries) out.getOrPut(c) { ArrayList() }.add(p)
        return out
    }

    private fun load(ctx: Context): List<PhrasePack> {
        val am = ctx.assets
        val names: List<String> = try {
            val idx = JSONObject(am.open("phrases/index.json").bufferedReader().use { it.readText() })
            val a = idx.optJSONArray("packs") ?: JSONArray()
            (0 until a.length()).map { a.optString(it) }.filter { it.isNotBlank() }
        } catch (e: Exception) {
            android.util.Log.w("LocalGhost", "phrases: index.json unreadable: ${e.message}")
            return emptyList()
        }
        val out = ArrayList<PhrasePack>(names.size)
        for (n in names) {
            try {
                val o = JSONObject(am.open("phrases/$n.json").bufferedReader().use { it.readText() })
                out.add(parse(o))
            } catch (e: Exception) {
                android.util.Log.w("LocalGhost", "phrases: pack $n skipped: ${e.message}")
            }
        }
        return out
    }

    fun parse(o: JSONObject): PhrasePack {
        fun strList(a: JSONArray?): List<String> =
            if (a == null) emptyList() else (0 until a.length()).map { a.optString(it) }.filter { it.isNotBlank() }
        val phrases = ArrayList<Phrase>()
        val pa = o.optJSONArray("phrases") ?: JSONArray()
        for (i in 0 until pa.length()) {
            val p = pa.optJSONObject(i) ?: continue
            val slots = strList(p.optJSONArray("slots")).mapNotNull { Slot.fromKey(it) }.toSet()
            val forms = HashMap<String, String>()
            p.optJSONObject("forms")?.let { f -> f.keys().forEach { k -> forms[k] = f.optString(k) } }
            phrases.add(Phrase(
                id = p.optString("id"),
                en = p.optString("en"),
                local = p.optString("local"),
                sayIt = p.optString("say"),
                roman = p.optString("roman", ""),
                slots = slots,
                weight = p.optInt("weight", 5).coerceIn(1, 10),
                situation = Situation.fromKey(p.optString("situation", "polite")) ?: Situation.POLITE,
                note = p.optString("note", ""),
                forms = forms,
                level = p.optInt("level", 1).coerceIn(1, Levels.MAX),
            ))
        }
        val emergency = ArrayList<EmergencyNumber>()
        val ea = o.optJSONArray("emergency") ?: JSONArray()
        for (i in 0 until ea.length()) {
            val e = ea.optJSONObject(i) ?: continue
            emergency.add(EmergencyNumber(e.optString("label"), e.optString("number")))
        }
        return PhrasePack(
            lang = o.optString("lang"),
            name = o.optString("name"),
            nativeName = o.optString("nativeName"),
            countries = strList(o.optJSONArray("countries")).map { it.uppercase() },
            ttsTag = o.optString("tts", o.optString("lang")),
            lateHours = o.optBoolean("lateHours", false),
            scriptNote = o.optString("scriptNote", ""),
            genderedSpeech = o.optBoolean("genderedSpeech", false),
            phrases = phrases,
            emergency = emergency,
            lateCountries = strList(o.optJSONArray("lateCountries")).map { it.uppercase() }.toSet(),
        )
    }
}

/** Country names for the codes the packs use , enough for a header line and a picker, no library. */
object CountryNames {
    private val names = mapOf(
        "ES" to "Spain", "MX" to "Mexico", "AR" to "Argentina", "CL" to "Chile", "CO" to "Colombia",
        "PE" to "Peru", "UY" to "Uruguay", "EC" to "Ecuador", "BO" to "Bolivia", "PY" to "Paraguay",
        "CR" to "Costa Rica", "PA" to "Panama", "DO" to "Dominican Republic", "GT" to "Guatemala",
        "CU" to "Cuba", "VE" to "Venezuela",
        "FR" to "France", "MC" to "Monaco", "BE" to "Belgium", "LU" to "Luxembourg", "SN" to "Senegal",
        "CI" to "Côte d'Ivoire", "CH" to "Switzerland",
        "IT" to "Italy", "SM" to "San Marino", "VA" to "Vatican City",
        "PT" to "Portugal", "BR" to "Brazil", "AO" to "Angola", "MZ" to "Mozambique", "CV" to "Cape Verde",
        "DE" to "Germany", "AT" to "Austria", "LI" to "Liechtenstein",
        "NL" to "Netherlands",
        "GR" to "Greece", "CY" to "Cyprus",
        "RO" to "Romania", "MD" to "Moldova",
        "TR" to "Türkiye",
        "PL" to "Poland", "CZ" to "Czechia", "SK" to "Slovakia",
        "HR" to "Croatia", "BA" to "Bosnia and Herzegovina", "ME" to "Montenegro", "RS" to "Serbia",
        "HU" to "Hungary",
        "SE" to "Sweden", "DK" to "Denmark", "NO" to "Norway",
        "JP" to "Japan", "KR" to "South Korea", "CN" to "China",
        "TH" to "Thailand", "ID" to "Indonesia", "VN" to "Vietnam",
    )
    fun of(code: String): String = names[code.uppercase()] ?: code.uppercase()
}
