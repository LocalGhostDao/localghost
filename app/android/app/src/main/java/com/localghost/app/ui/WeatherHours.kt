package com.localghost.app.ui

import java.time.Instant
import java.time.ZoneOffset
import java.time.format.DateTimeFormatter

/**
 * THE HOURS of a forecast, read on the phone. The box pulls each place once in sixteen hours and
 * keeps three days of hours with the pull, so the sky now is the hour the clock is in at the
 * place, not the pull's own "now" (which is what the card showed as "the weather of eight hours
 * ago"); the next day is a line. Pure Kotlin, so the tests can read it.
 */
/** One hour of the forecast, the place's local time ("2026-10-10T14:00"); the box's row. */
data class WeatherHour(val at: String, val tempC: Double, val rainPct: Int, val precipMm: Double, val code: Int, val windKmh: Double)

object WeatherHours {
    /** The pull's own current block is trusted this long; after it the hour speaks. */
    const val FRESH_S = 90 * 60L

    private val keyFmt: DateTimeFormatter = DateTimeFormatter.ofPattern("yyyy-MM-dd'T'HH:00")

    /** The hour key the clock is in at the place: "2026-10-10T14:00". */
    fun keyAt(nowS: Long, utcOffset: Int): String =
        Instant.ofEpochSecond(nowS).atOffset(ZoneOffset.ofTotalSeconds(utcOffset.coerceIn(-18 * 3600, 18 * 3600))).format(keyFmt)

    /** The index of the hour the clock is in, or -1. */
    fun indexNow(hours: List<WeatherHour>, nowS: Long, utcOffset: Int): Int {
        val k = keyAt(nowS, utcOffset)
        return hours.indexOfFirst { it.at == k }
    }

    /** The hour to call "now" once the pull is older than [FRESH_S]; null when the pull is fresh
     *  (its own block speaks) or the clock is past the hours kept. */
    fun current(hours: List<WeatherHour>, fetchedAt: Long, nowS: Long, utcOffset: Int): WeatherHour? {
        if (fetchedAt > 0 && nowS - fetchedAt < FRESH_S) return null
        val i = indexNow(hours, nowS, utcOffset)
        return if (i < 0) null else hours[i]
    }

    /** The next [n] hours from the one the clock is in (that one first); empty when none. */
    fun next(hours: List<WeatherHour>, nowS: Long, utcOffset: Int, n: Int = 24): List<WeatherHour> {
        val i = indexNow(hours, nowS, utcOffset)
        if (i < 0) return emptyList()
        return hours.subList(i, minOf(hours.size, i + n))
    }

    /** The hour of day of an hour key, "14". */
    fun hourOf(at: String): String = if (at.length >= 13) at.substring(11, 13) else at

    /** Which of [hs] get a label under the line: the first, then every sixth. */
    fun labelled(hs: List<WeatherHour>): List<Int> = hs.indices.filter { it % 6 == 0 }

    /** The line's y for a temperature between the hours' lowest and highest, 0 at the top. A
     *  flat day (all the same) sits in the middle. */
    fun y(tempC: Double, lo: Double, hi: Double): Float {
        if (hi - lo < 0.5) return 0.5f
        return (1.0 - (tempC - lo) / (hi - lo)).toFloat().coerceIn(0f, 1f)
    }
}
