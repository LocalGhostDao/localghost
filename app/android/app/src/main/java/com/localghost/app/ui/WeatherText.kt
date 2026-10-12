package com.localghost.app.ui

import java.util.Locale

/**
 * THE WEATHER PAGE'S WORDS, pure so the tests read them. The card on HOME says the sky now and
 * the next day as a line; the page says the rest, hour by hour and day by day, which models the
 * box read and how far they disagreed, and how old what the phone holds is.
 */
object WeatherText {
    /** "Tuesday 13 Oct" from "2026-10-13"; the date itself when it does not parse. */
    fun dayLabel(date: String): String = try {
        val d = java.time.LocalDate.parse(date)
        d.dayOfWeek.getDisplayName(java.time.format.TextStyle.FULL, Locale.UK) + " " + d.dayOfMonth + " " +
            d.month.getDisplayName(java.time.format.TextStyle.SHORT, Locale.UK)
    } catch (_: Exception) { date }

    /** "today", "tomorrow", or the day's label, against [today] ("2026-10-12"). */
    fun dayName(date: String, today: String): String = when {
        date == today -> "today"
        date == try { java.time.LocalDate.parse(today).plusDays(1).toString() } catch (_: Exception) { "" } -> "tomorrow"
        else -> dayLabel(date)
    }

    /** One hour as a row: "14:00  18°  ±1  30%  0.4 mm  12 km/h  partly cloudy". The spread shows
     *  for a forecast the box computed (it is 0 for a pulled one); rain shows from 5%. */
    fun hourRow(h: WeatherHour): String {
        val sb = StringBuilder()
        sb.append(WeatherHours.hourOf(h.at)).append(":00  ")
        sb.append("%.0f°".format(Locale.UK, h.tempC))
        if (h.spreadC >= 0.5) sb.append(" ±%.0f".format(Locale.UK, h.spreadC / 2))
        if (h.rainPct >= 5) sb.append("  ").append(h.rainPct).append("%")
        if (h.precipMm >= 0.1) sb.append("  %.1f mm".format(Locale.UK, h.precipMm))
        if (h.windKmh >= 10) sb.append("  %.0f km/h".format(Locale.UK, h.windKmh))
        HomeText.weatherWord(h.code).takeIf { it.isNotEmpty() }?.let { sb.append("  ").append(it) }
        return sb.toString()
    }

    /** One day as a row: "Tue 13 Oct  18/9  rain 60%  2.4 mm  ↑07:38 ↓18:41". */
    fun dayRow(date: String, maxC: Double, minC: Double, code: Int, rainPct: Int, precipMm: Double = 0.0, sunrise: String = "", sunset: String = ""): String {
        val sb = StringBuilder(dayLabel(date))
        sb.append("  %.0f/%.0f".format(Locale.UK, maxC, minC))
        HomeText.weatherWord(code).takeIf { it.isNotEmpty() }?.let { sb.append("  ").append(it) }
        if (rainPct >= 5) sb.append("  ").append(rainPct).append("%")
        if (precipMm >= 0.1) sb.append("  %.1f mm".format(Locale.UK, precipMm))
        if (sunrise.isNotEmpty() && sunset.isNotEmpty()) sb.append("  ↑").append(sunrise).append(" ↓").append(sunset)
        return sb.toString()
    }

    /** How far the models stood apart over [hs]: "the models agree to within 1°" or "the models
     *  differ by up to 4°, most at Tue 03:00"; "" for a pulled forecast (no spread kept). */
    fun spreadLine(hs: List<WeatherHour>): String {
        val with = hs.filter { it.spreadC > 0 }
        if (with.isEmpty()) return ""
        val worst = with.maxByOrNull { it.spreadC }!!
        if (worst.spreadC < 1.5) return "the models agree to within 1°"
        val day = try { java.time.LocalDate.parse(worst.at.take(10)).dayOfWeek.getDisplayName(java.time.format.TextStyle.SHORT, Locale.UK) } catch (_: Exception) { worst.at.take(10) }
        return "the models differ by up to %.0f°, most at %s %s:00".format(Locale.UK, worst.spreadC, day, WeatherHours.hourOf(worst.at))
    }

    /** The box's source line as rows: "icon-d2 2026-10-10 09Z · ifs 2026-10-10 06Z" →
     *  ["ICON-D2, the 09Z run of 2026-10-10", …]; a pulled forecast gives one row saying so. */
    fun sourceRows(source: String): List<String> {
        if (source.isBlank()) return listOf("pulled from the weather service for the box's place list, the same for everyone")
        return source.split(" · ").mapNotNull { r ->
            val p = r.trim().split(" ")
            if (p.size < 3) null else "${p[0].uppercase()}, the ${p.last()} run of ${p[1]}"
        }
    }

    /** What a model is, for the row under its name. */
    fun modelNote(id: String): String = when (id.lowercase()) {
        "icon-d2" -> "DWD, 2.2 km over Germany and its neighbours, hourly, eight runs a day"
        "icon-eu" -> "DWD, 6.5 km over Europe, hourly to two days"
        "icon" -> "DWD, 13 km, the world"
        "ifs" -> "ECMWF, 25 km, the world, three-hourly"
        "gfs" -> "NOAA, 25 km, the world, three-hourly"
        "ukv" -> "the Met Office, 2 km over the UK, with your key"
        else -> ""
    }

    /** "computed on the box 42 min ago · read by this phone 3 min ago"; the second half only
     *  when the phone's copy is older than a minute. */
    fun ageLine(fetchedAt: Long, readAt: Long, nowS: Long, computed: Boolean): String {
        fun ago(s: Long): String = when {
            s < 60 -> "just now"
            s < 3600 -> "${s / 60} min ago"
            s < 48 * 3600 -> "${s / 3600} h ago"
            else -> "${s / 86400} days ago"
        }
        val verb = if (computed) "computed on the box" else "pulled by the box"
        val a = if (fetchedAt <= 0) "not $verb yet" else "$verb ${ago(nowS - fetchedAt)}"
        if (readAt <= 0 || nowS - readAt < 60) return a
        return "$a · this phone's copy from ${ago(nowS - readAt)}"
    }

    /** The hours of [hs] grouped by their date, in order. */
    fun byDay(hs: List<WeatherHour>): List<Pair<String, List<WeatherHour>>> {
        val out = ArrayList<Pair<String, MutableList<WeatherHour>>>()
        hs.forEach { h ->
            val d = h.at.take(10)
            if (out.isEmpty() || out.last().first != d) out.add(d to ArrayList())
            out.last().second.add(h)
        }
        return out
    }
}
