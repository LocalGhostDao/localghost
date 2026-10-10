package com.localghost.app.phrases

import android.content.Context
import com.localghost.app.net.BoxClient
import org.json.JSONArray
import org.json.JSONObject

/**
 * The home brief the lock-screen card shows at home: kept on the phone (the card is drawn by an
 * alarm and a receiver, with no box at hand), fetched from the box with the notification poll
 * every quarter hour and when the app opens. The box picked the stories and made the prices; the
 * phone only chooses which to show (HomeBriefText).
 */
object HomeBrief {
    private const val PREFS = "lg_home_brief"

    data class Kept(val at: Long, val cards: List<HomeBriefText.Card>, val prices: String, val chip: String, val weather: String = "")

    fun kept(ctx: Context): Kept? = runCatching {
        val s = ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE).getString("brief", null) ?: return null
        val o = JSONObject(s)
        val arr = o.optJSONArray("cards") ?: JSONArray()
        Kept(o.optLong("at"), (0 until arr.length()).map { i ->
            val c = arr.getJSONObject(i)
            HomeBriefText.Card(c.optString("id"), c.optString("h"), c.optString("s"), c.optString("o"))
        }, o.optString("prices"), o.optString("chip"), o.optString("weather"))
    }.getOrNull()

    private fun keep(ctx: Context, k: Kept) {
        val arr = JSONArray()
        k.cards.forEach { c -> arr.put(JSONObject().put("id", c.id).put("h", c.headline).put("s", c.summary).put("o", c.outlets)) }
        ctx.getSharedPreferences(PREFS, Context.MODE_PRIVATE).edit()
            .putString("brief", JSONObject().put("at", k.at).put("cards", arr).put("prices", k.prices).put("chip", k.chip).put("weather", k.weather).toString()).apply()
    }

    /** The weather where the phone last was, as one line for the widget: the box's own table,
     *  asked with the phone's last fix (which goes to the box and nowhere else); "" without a fix
     *  or a forecast. */
    private suspend fun weatherLine(app: Context): String {
        val fix = com.localghost.app.sync.LocationLog.last(app) ?: return ""
        val w = BoxClient.weather(app, fix.lat, fix.lon) ?: return ""
        if (!w.ok || w.tempC.isNaN()) return ""
        val today = w.days.firstOrNull()
        val nowS = System.currentTimeMillis() / 1000
        return HomeBriefText.weatherLine(w.place, w.tempNow(nowS), w.codeNow(nowS), today?.maxC ?: Double.NaN, today?.minC ?: Double.NaN, today?.code ?: -1, today?.rainPct ?: 0)
    }

    /** Ask the box for the stories and the prices, keep what came, redraw the card. A box out of
     *  reach keeps the last brief (the card says how old it is when pulled open). */
    suspend fun fetch(ctx: Context): Boolean {
        val app = ctx.applicationContext
        val now = System.currentTimeMillis() / 1000
        val old = kept(app)
        // home's snapshot (a few kilobytes) has what the lock screen shows: the day's most-told
        // stories, BTC and ETH; the whole news and rates only from a box that has no snapshot
        val snap = BoxClient.home(app)
        if (snap != null && (snap.top.isNotEmpty() || snap.prices.isNotEmpty())) {
            val cards = if (snap.top.isNotEmpty()) HomeBriefText.cards(snap.top.map { s ->
                HomeBriefText.Story(s.id, s.title, s.lead, s.outlets, s.lastSeen, s.sources)
            }, now) else old?.cards ?: emptyList()
            val prices = snap.prices.map { (sym, p) -> HomeBriefText.Price(sym, p.price, p.change24, p.at) }
            val weather = weatherLine(app).ifEmpty { old?.weather ?: "" }
            keep(app, Kept(now, cards, if (prices.isNotEmpty()) HomeBriefText.prices(prices) else old?.prices ?: "",
                if (prices.isNotEmpty()) HomeBriefText.chip(prices) else old?.chip ?: "", weather))
            if (PhraseState.lockScreenOn(app)) PhraseSurface.refresh(app)
            return true
        }
        val news = BoxClient.news(app, since = now - 86_400, keep = true)
        val rates = BoxClient.rates(app, keep = true)
        if (news == null && rates == null) return false
        val cards = news?.let { n ->
            HomeBriefText.cards(n.stories.map { s ->
                HomeBriefText.Story(s.id, s.title, s.summary, s.items.map { it.outlet }, s.lastSeen, s.sources)
            }, now)
        } ?: old?.cards ?: emptyList()
        val prices = rates?.index?.map { HomeBriefText.Price(it.symbol, it.price, it.change24, it.at) }
        keep(app, Kept(now, cards, prices?.let { HomeBriefText.prices(it) } ?: old?.prices ?: "", prices?.let { HomeBriefText.chip(it) } ?: old?.chip ?: "", old?.weather ?: ""))
        if (PhraseState.lockScreenOn(app)) PhraseSurface.refresh(app)
        return true
    }

    /** Whether the phone is at home: the network country is home's, or unknown (a phone with no
     *  SIM on Wi-Fi is most likely at home; abroad the network names the country). */
    fun atHome(ctx: Context): Boolean {
        val home = PhraseOffer.homeCountry(ctx)
        val where = CountryDetect.detect(ctx)
        if (where.source == "unknown" || where.country.isEmpty()) return true
        return home.isNotEmpty() && where.country.equals(home, ignoreCase = true)
    }
}
