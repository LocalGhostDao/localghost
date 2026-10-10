package com.localghost.app.ui

import java.util.Locale

/**
 * THE HOME SCREEN'S WORDS. Home is where the app opens: BTC and ETH always (the other coins a tap
 * away, on CRYPTO), the day's news as one point per story (the box's brief, written from the
 * summaries of the most-told stories, each point opening its story), and a box to ask from. The
 * box made every number and sentence; the phone only picks and writes them. Pure, so the JVM
 * tests read it.
 */
object HomeText {
    /**
     * A coin as home and CRYPTO show it: the box's own price where it has one, the list's
     * otherwise. [supply] is Coinbase's circulating supply; the cap is the shown price times it.
     */
    data class Coin(val rank: Int, val symbol: String, val name: String, val usd: Double, val change24: Double?, val cap: Double, val supply: Double = 0.0)

    /** One point of the brief and the story it tells (null when the points and stories do not pair up). */
    data class Point(val text: String, val story: Long?)

    /**
     * The brief as points: its "- " lines, the nth paired with the nth of [stories] when the counts
     * agree. A brief from before the points is one point of its own.
     */
    fun points(brief: String, stories: List<Long>): List<Point> {
        val t = NewsText.told(brief)
        val lines = if (t.points.isEmpty()) listOfNotNull(t.lead.takeIf { it.isNotEmpty() }) else listOfNotNull(t.lead.takeIf { it.isNotEmpty() }) + t.points
        val paired = t.lead.isEmpty() && t.points.size == stories.size
        return lines.mapIndexed { i, s -> Point(s, if (paired) stories[i] else null) }
    }

    /** The box's prices with the fast lane's over them (BTC, ETH, SOL, seconds old). */
    fun withFast(live: Map<String, Pair<Double, Double?>>, fast: Map<String, Pair<Double, Double?>>): Map<String, Pair<Double, Double?>> =
        live + fast.filterValues { it.first > 0 }

    /** "84,498", "2,692", "0.2512", "169.71". */
    fun money(v: Double): String = when {
        v <= 0 -> "0"
        v < 1 -> "%.4f".format(Locale.US, v)
        v < 1000 -> "%.2f".format(Locale.US, v)
        else -> "%,d".format(Locale.US, Math.round(v))
    }

    /** "+0.9%", "-1.2%"; "" when the box has no change to say. */
    fun change(c: Double?): String = c?.let { "%+.1f%%".format(Locale.US, it) } ?: ""

    /** A market cap: "1.69T", "412.0B", "88.1M". */
    fun cap(v: Double): String = when {
        v >= 1e12 -> "%.2fT".format(Locale.US, v / 1e12)
        v >= 1e9 -> "%.1fB".format(Locale.US, v / 1e9)
        v >= 1e6 -> "%.1fM".format(Locale.US, v / 1e6)
        v > 0 -> money(v)
        else -> ""
    }

    /** The two coins home always shows, BTC first; the rest wait for CRYPTO. */
    fun pinned(coins: List<Coin>): List<Coin> = listOf("BTC", "ETH").mapNotNull { s -> coins.firstOrNull { it.symbol == s && it.usd > 0 } }

    /**
     * The list CRYPTO shows: the rank list's order, each coin at the box's own price (a minute old)
     * where the box follows it, the list's hourly price otherwise; at most [n].
     */
    fun merge(ranks: List<Coin>, live: Map<String, Pair<Double, Double?>>, n: Int = 50): List<Coin> =
        ranks.sortedBy { it.rank }.take(n).map { c ->
            val l = live[c.symbol]
            val m = if (l != null && l.first > 0) c.copy(usd = l.first, change24 = l.second ?: c.change24) else c
            if (m.supply > 0 && m.usd > 0) m.copy(cap = m.usd * m.supply) else m
        }

    /** The home row for a pinned coin with no rank list yet: the box's own price alone. */
    fun liveOnly(symbol: String, usd: Double, change24: Double?): Coin = Coin(0, symbol, symbol, usd, change24, 0.0)

    /** "updated 4 s ago", "updated 2 min ago" for the prices; "" before the first. */
    fun updated(atSec: Long, nowSec: Long): String {
        if (atSec <= 0) return ""
        val d = (nowSec - atSec).coerceAtLeast(0)
        return "updated " + when {
            d < 2 -> "just now"
            d < 90 -> "$d s ago"
            d < 90 * 60 -> "${(d + 30) / 60} min ago"
            else -> "${(d + 1800) / 3600} h ago"
        }
    }

    /** The prices' line under BTC and ETH: when, and from how many exchanges ("" for none). */
    fun pricesLine(atSec: Long, nowSec: Long, exchanges: Int): String =
        listOf(updated(atSec, nowSec), if (exchanges > 1) "$exchanges exchanges" else "").filter { it.isNotEmpty() }.joinToString(" · ")

    /** What "write now" says when the box did not write a brief (its own reason, short). */
    fun briefNot(why: String): String = if (why.isBlank()) "the box did not answer" else why

    /** "written 23 min ago" for the brief; "" before the first. */
    fun written(at: Long, now: Long): String {
        if (at <= 0) return ""
        val d = (now - at).coerceAtLeast(0)
        return "written " + when {
            d < 90 -> "just now"
            d < 90 * 60 -> "${(d + 30) / 60} min ago"
            d < 48 * 3600 -> "${(d + 1800) / 3600} h ago"
            else -> "${d / 86400} days ago"
        }
    }

    /** What home says in the brief's place before the box has written one. */
    fun noBrief(stories: Int): String = when (stories) {
        0 -> "no news on the box yet: pull down to fetch the feeds"
        else -> "the box writes the day's brief once a few stories have their summaries"
    }

    /** A WMO weather code in a word or two (Open-Meteo gives the code; the box keeps it). */
    fun weatherWord(code: Int): String = when (code) {
        0 -> "clear"; 1 -> "mostly clear"; 2 -> "partly cloudy"; 3 -> "overcast"
        45, 48 -> "fog"; 51, 53, 55 -> "drizzle"; 56, 57 -> "freezing drizzle"
        61, 63, 65 -> "rain"; 66, 67 -> "freezing rain"; 71, 73, 75 -> "snow"; 77 -> "snow grains"
        80, 81, 82 -> "showers"; 85, 86 -> "snow showers"; 95 -> "thunderstorm"; 96, 99 -> "thunderstorm with hail"
        else -> ""
    }

    /** "24° feels 26 · partly cloudy · wind 12 km/h" for the card's main line. */
    fun weatherNow(tempC: Double, feelsC: Double, code: Int, windKmh: Double): String {
        if (tempC.isNaN()) return ""
        val sb = StringBuilder("%.0f°".format(java.util.Locale.UK, tempC))
        if (!feelsC.isNaN() && Math.abs(feelsC - tempC) >= 1.5) sb.append(" feels %.0f".format(java.util.Locale.UK, feelsC))
        weatherWord(code).takeIf { it.isNotEmpty() }?.let { sb.append(" · ").append(it) }
        if (windKmh >= 20) sb.append(" · wind %.0f km/h".format(java.util.Locale.UK, windKmh))
        return sb.toString()
    }

    /** One day of the forecast in a few characters: "Thu 28/19 rain 60%". */
    fun weatherDay(date: String, maxC: Double, minC: Double, code: Int, rainPct: Int): String {
        val day = try {
            java.text.SimpleDateFormat("EEE", java.util.Locale.UK).format(java.text.SimpleDateFormat("yyyy-MM-dd", java.util.Locale.UK).parse(date)!!)
        } catch (_: Exception) { date.takeLast(2) }
        val w = weatherWord(code)
        return "$day %.0f/%.0f".format(java.util.Locale.UK, maxC, minC) + (if (w.isNotEmpty()) " $w" else "") + (if (rainPct >= 30) " $rainPct%" else "")
    }

    /** Under the card: where it is from and how fresh. */
    fun weatherSource(place: String, country: String, fetchedAt: Long, places: Long, nowS: Long): String {
        val where = if (country.isNotBlank()) "$place, $country" else place
        val age = nowS - fetchedAt
        val fresh = when {
            fetchedAt == 0L -> "not pulled yet"
            age < 3600 -> "pulled ${age / 60} min ago"
            age < 48 * 3600 -> "pulled ${age / 3600} h ago"
            else -> "pulled ${age / 86400} days ago"
        }
        return "$where · nearest of ${"%,d".format(places)} places the box pulls every sixteen hours · $fresh"
    }
}
