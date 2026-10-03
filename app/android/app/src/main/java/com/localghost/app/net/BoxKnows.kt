package com.localghost.app.net

/**
 * WHAT THE BOX KEEPS, judged on the phone when the box's plan did not come in time: the price of
 * a coin, the top coins, crypto as a whole, a rate between currencies, and the weather where the
 * phone is. The box keeps all of these (a minute old, from seven exchanges; Coinbase's rank list;
 * the ECB's table; a daily pull of the forecast for the world's larger places) and puts them in its
 * answer, so the phone does not search the web for them. The weather matters most: a weather
 * question naming no place could only be searched with the phone's position, which never leaves
 * it; a question naming a place may still go to the web, the name says nothing about where the
 * phone is. The box's own judgement (/v1/chat/plan, boxHas) knows every coin it follows and every
 * place it pulled; this one knows the common ones. Pure, for the JVM tests.
 */
object BoxKnows {
    private val coin = Regex("\\b(btc|bitcoin|eth|ether|ethereum|sol|solana|xrp|ripple|doge|dogecoin|ada|cardano|ltc|litecoin|crypto|cryptos|cryptocurrenc(y|ies))\\b", RegexOption.IGNORE_CASE)
    private val priceWords = Regex("\\b(price|prices|worth|cost|trading|value|how much|doing|up|down|today|now|chart|market cap|usd|dollars?|euros?|pounds?)\\b|[$€£]", RegexOption.IGNORE_CASE)
    private val top = Regex("\\b(top|biggest|largest|leading)\\s+(\\d{1,3}\\s+)?(coins?|cryptos?|crypto ?currenc(y|ies)|tokens?|altcoins?)\\b", RegexOption.IGNORE_CASE)
    private val fiat = Regex("\\b(eur|euros?|usd|dollars?|gbp|pounds?|sterling|ron|lei|chf|francs?|jpy|yen)\\b", RegexOption.IGNORE_CASE)
    private val rate = Regex("\\b(exchange rate|convert|in (euros?|dollars|pounds|lei|yen|francs))\\b|\\bto (eur|usd|gbp|ron|chf|jpy)\\b", RegexOption.IGNORE_CASE)
    private val weatherQ = Regex("\\b(weather|forecast|rain|raining|temperature|how (hot|cold|warm) is it|umbrella|sunny|snow|snowing|wind|windy|humid|humidity)\\b", RegexOption.IGNORE_CASE)
    private val placeAfter = Regex("\\b(?:[Ii]n|[Aa]t|[Ff]or|[Aa]round|[Nn]ear)\\s+\\p{Lu}[\\p{L}.'-]*")
    private val timeAfter = Regex("(?:[Ii]n|[Aa]t|[Ff]or|[Aa]round|[Nn]ear)\\s+(?:Today|Tomorrow|Tonight|This|Next|The|Now|Noon|Midnight|Monday|Tuesday|Wednesday|Thursday|Friday|Saturday|Sunday)")

    /** A weather question that names no place: "weather in Rome" names one (a capitalised word
     *  after in/at/for/around/near that is not a time), "is it going to rain tomorrow" does not. */
    fun weatherHere(q: String): Boolean =
        weatherQ.containsMatchIn(q) && placeAfter.findAll(q).all { timeAfter.matches(it.value) }

    fun covers(q: String): Boolean {
        if (weatherHere(q)) return true
        if (top.containsMatchIn(q)) return true
        if (coin.containsMatchIn(q) && (priceWords.containsMatchIn(q) || q.trim().split(Regex("\\s+")).size <= 3)) return true
        return rate.containsMatchIn(q) && fiat.findAll(q).count() >= 1
    }
}
