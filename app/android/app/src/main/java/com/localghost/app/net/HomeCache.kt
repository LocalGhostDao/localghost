package com.localghost.app.net

import android.content.Context
import org.json.JSONObject
import java.io.File

/**
 * THE LATEST, KEPT ON THE PHONE, so HOME, CRYPTO and the lock screen open on it at once instead of
 * waiting for the box: the last /v1/rates and /v1/news the phone read (as the box sent them), and
 * the last snapshot of home that came back on the notifications poll, the trail's upload or
 * /v1/home. FOR YOU is kept in memory only, for as long as the app runs: places near my trail and
 * my days' titles do not go on the phone's storage (the trail itself is sealed there).
 */
object HomeCache {
    private const val RATES = "rates.json"
    private const val NEWS = "news.json"
    private const val SNAP = "snap.json"
    private const val WEATHER = "weather.json"

    @Volatile var forYou: HomeData.ForYou? = null
        private set

    private fun dir(ctx: Context): File = File(ctx.applicationContext.filesDir, "home").apply { mkdirs() }

    /** Writes a file whole or not at all (a half-written copy would read as nothing). */
    private fun put(ctx: Context, name: String, body: String) {
        try {
            val d = dir(ctx)
            val tmp = File(d, "$name.part")
            tmp.writeText(body)
            if (!tmp.renameTo(File(d, name))) tmp.delete()
        } catch (_: Exception) {
        }
    }

    private fun get(ctx: Context, name: String): JSONObject? = try {
        val f = File(dir(ctx), name)
        if (f.exists()) JSONObject(f.readText()) else null
    } catch (_: Exception) { null }

    fun putRates(ctx: Context, r: JSONObject) = put(ctx, RATES, r.toString())
    fun putNews(ctx: Context, r: JSONObject) = put(ctx, NEWS, r.toString())

    /** A snapshot from the box: kept (but FOR YOU), FOR YOU held in memory; returns it parsed. */
    fun putSnap(ctx: Context, o: JSONObject?): HomeData.Snap? {
        val s = HomeData.parse(o) ?: return null
        put(ctx, SNAP, HomeData.forDisk(s))
        s.forYou?.let { forYou = it }
        return s
    }

    /** The last /v1/weather the phone read, with the hour it read it, so the card and the WEATHER
     *  page open on it (the forecast for where the phone last was; the source line names the place). */
    fun putWeather(ctx: Context, r: JSONObject, readAt: Long) = put(ctx, WEATHER, JSONObject().put("readAt", readAt).put("body", r).toString())
    fun weather(ctx: Context): BoxClient.Weather? = get(ctx, WEATHER)?.let { o ->
        runCatching { o.optJSONObject("body")?.let { BoxClient.weatherFrom(it, o.optLong("readAt")) } }.getOrNull()
    }

    fun rates(ctx: Context): BoxClient.Rates? = get(ctx, RATES)?.let { runCatching { BoxClient.ratesFrom(it) }.getOrNull() }
    fun news(ctx: Context): BoxClient.News? = get(ctx, NEWS)?.let { runCatching { BoxClient.newsFrom(it) }.getOrNull() }
    fun snap(ctx: Context): HomeData.Snap? = HomeData.parse(get(ctx, SNAP))?.let { s -> if (s.forYou == null) s.copy(forYou = forYou) else s }

    /** The snapshot's prices as the fast lane's, for home's card (null without any). */
    fun fastOf(s: HomeData.Snap?): BoxClient.Fast? {
        if (s == null || s.prices.isEmpty()) return null
        val at = s.prices.values.maxOf { it.at } * 1000
        return BoxClient.Fast(at, s.prices.mapValues { it.value.price to it.value.change24 }, s.prices.mapValues { it.value.n })
    }

    /** The news as home shows it: the kept stories, with the snapshot's brief when it is newer;
     *  without kept stories, the snapshot's top stories (title, lead, outlets). */
    fun newsWith(kept: BoxClient.News?, s: HomeData.Snap?): BoxClient.News? {
        if (s == null) return kept
        val base = kept ?: BoxClient.News(s.top.map { t ->
            BoxClient.NewsStory(t.id, t.title, t.lead, t.sources, t.lastSeen, t.lastSeen,
                t.outlets.map { o -> BoxClient.NewsItem("", o, "", "", "", 0) })
        }, 0, 0)
        return if (HomeData.newerBrief(base.briefAt, s)) base.copy(brief = s.brief, briefAt = s.briefAt, briefStories = s.briefStories) else base
    }

    /** The market line's numbers, from the snapshot when the kept rates have none or are older. */
    fun marketOf(r: BoxClient.Rates?, s: HomeData.Snap?): BoxClient.Market? {
        val m = r?.market
        if (s == null || s.marketValue <= 0) return m
        val newest: Long = r?.index?.maxOfOrNull { it.at } ?: 0L
        if (m != null && m.value > 0 && newest >= s.at) return m
        return BoxClient.Market(s.marketCode, s.marketValue, s.marketChange, m?.constituents ?: 0, m?.priced ?: 0, m?.month ?: "")
    }
}
