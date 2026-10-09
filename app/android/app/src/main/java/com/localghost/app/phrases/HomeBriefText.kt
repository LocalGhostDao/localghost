package com.localghost.app.phrases

/**
 * THE LOCK SCREEN AT HOME. Away, the card teaches the language around you; at home it carries the
 * news the box picked and the two prices: the most-told stories of the last day (several outlets
 * telling the same thing is what makes a story worth a glance), the newest first among equals,
 * and one line of BTC and ETH from the box's own minute index with the change over 24 hours.
 * Pure, so the JVM tests read it; HomeBrief keeps it and fetches it from the box.
 */
object HomeBriefText {
    data class Story(val id: Long, val title: String, val summary: String, val outlets: List<String>, val lastSeen: Long, val sources: Int)
    data class Price(val symbol: String, val usd: Double, val change24: Double?, val at: Long)

    /** One card of the brief. */
    data class Card(val id: String, val headline: String, val summary: String, val outlets: String)

    /** At most [n] stories: those of the last [withinS] seconds, the most-told first, then the newest. */
    fun pick(stories: List<Story>, now: Long, n: Int = 6, withinS: Long = 86_400): List<Story> =
        stories.filter { it.title.isNotBlank() && now - it.lastSeen <= withinS }
            .sortedWith(compareByDescending<Story> { it.sources }.thenByDescending { it.lastSeen })
            .take(n)

    fun cards(stories: List<Story>, now: Long, n: Int = 6): List<Card> = pick(stories, now, n).map { s ->
        // the lead alone: the lock screen has room for a sentence, the points wait in NEWS
        Card("news:" + s.id, s.title.trim(), com.localghost.app.ui.NewsText.lead(s.summary).takeIf { it != s.title.trim() } ?: "", outlets(s.outlets, s.lastSeen, now))
    }

    fun outlets(names: List<String>, lastSeen: Long, now: Long): String {
        val d = names.filter { it.isNotBlank() }.distinct()
        val shown = if (d.size <= 3) d else d.take(3) + "${d.size - 3} more"
        val age = (now - lastSeen).coerceAtLeast(0)
        val ago = when {
            age < 3600 -> "${age / 60} min ago"
            age < 48 * 3600 -> "${age / 3600} h ago"
            else -> "${age / 86400} days ago"
        }
        return (shown + ago).joinToString(" · ")
    }

    /** "BTC 65,000 +1.2% · ETH 3,250 -0.4%" , BTC and ETH, in that order, whichever the box has. */
    fun prices(prices: List<Price>): String = listOf("BTC", "ETH").mapNotNull { sym ->
        val p = prices.firstOrNull { it.symbol == sym && it.usd > 0 } ?: return@mapNotNull null
        sym + " " + money(p.usd) + (p.change24?.let { " " + "%+.1f".format(java.util.Locale.US, it) + "%" } ?: "")
    }.joinToString(" · ")

    /** The prices without their changes, for a line with little room: "BTC 65,000 · ETH 3,250". */
    fun short(prices: String): String = prices.split(" · ").joinToString(" · ") { p ->
        p.split(" ").take(2).joinToString(" ")
    }

    /** The prices one a line, for the widget's big line: "BTC 65,000 +1.2%" over "ETH 3,250 -0.4%". */
    fun stacked(prices: String): String = prices.replace(" · ", "\n")

    /** The widget's foot abroad: the prices with their changes on one line, the top story on the
     *  next; "" with neither. */
    fun foot(prices: String, story: String): String {
        val lines = ArrayList<String>(2)
        if (prices.isNotBlank()) lines.add(prices.trim())
        if (story.isNotBlank()) lines.add("news · " + story.trim())
        return lines.joinToString("\n")
    }

    /** The status-bar chip: "BTC 65.0k" (a live update's chip has room for a few characters). */
    fun chip(prices: List<Price>): String {
        val b = prices.firstOrNull { it.symbol == "BTC" && it.usd > 0 } ?: return ""
        return if (b.usd >= 1000) "BTC " + "%.1f".format(java.util.Locale.US, b.usd / 1000) + "k" else "BTC " + money(b.usd)
    }

    fun money(v: Double): String = when {
        v <= 0 -> "0"
        v < 1 -> "%.4f".format(java.util.Locale.US, v)
        v < 1000 -> "%.2f".format(java.util.Locale.US, v)
        else -> "%,d".format(java.util.Locale.US, Math.round(v))
    }

    /** The card pulled open: the summary, the outlets, the prices when they are not the title
     *  already ("" then), then what comes next and after. */
    fun expanded(cards: List<Card>, index: Int, prices: String, asOf: String): String {
        val c = cards.getOrNull(index) ?: return prices
        val sb = StringBuilder(400)
        if (c.summary.isNotEmpty()) sb.append(c.summary).append('\n')
        sb.append(c.outlets)
        if (prices.isNotEmpty()) sb.append("\n\n").append(prices).append(if (asOf.isNotEmpty()) "  ·  $asOf" else "")
        val n = cards.size
        if (n > 1) {
            sb.append('\n')
            sb.append('\n').append("next   ").append(cards[(index + 1) % n].headline)
            if (n > 2) sb.append('\n').append("then   ").append(cards[(index + 2) % n].headline)
        }
        return sb.toString()
    }
}
