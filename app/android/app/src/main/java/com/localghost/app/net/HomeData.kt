package com.localghost.app.net

import org.json.JSONArray
import org.json.JSONObject

/**
 * HOME AS THE BOX LAST SAID IT. The notifications poll, the trail's upload and /v1/home carry
 * home's numbers ("home": BTC, ETH and SOL with the day's change, CRYPTO50, the brief, the day's
 * most-told stories) and FOR YOU (places near the trail, this day in earlier years, the news that
 * touches what I said about myself). Pure, so the JVM tests read it.
 */
object HomeData {
    data class Price(val price: Double, val change24: Double?, val at: Long, val n: Int)
    data class Story(val id: Long, val title: String, val lead: String, val outlets: List<String>, val sources: Int, val lastSeen: Long)
    data class Place(val name: String, val kind: String, val km: Double, val bearing: String, val why: String, val new: Boolean)
    data class Day(val day: String, val yearsAgo: Int, val memoryId: Long, val title: String, val lead: String, val photos: Int, val place: String)
    data class Pick(val id: Long, val title: String, val lead: String, val why: String)
    /** One memory brought back: what the box noticed lately, else a distilled one, a place or a person. */
    data class Remember(val id: Long, val kind: String, val title: String, val body: String)
    data class ForYou(val at: Long, val from: Long, val places: List<Place>, val days: List<Day>, val stories: List<Pick>, val note: String,
                      val remember: Remember? = null) {
        val empty: Boolean get() = places.isEmpty() && days.isEmpty() && stories.isEmpty() && remember == null
    }
    data class Snap(val at: Long, val prices: Map<String, Price>, val marketCode: String, val marketValue: Double, val marketChange: Double,
                    val brief: String, val briefAt: Long, val briefStories: List<Long>, val top: List<Story>, val forYou: ForYou?,
                    val calendar: List<com.localghost.app.ui.CalendarText.Event> = emptyList())

    private fun strings(a: JSONArray?): List<String> = if (a == null) emptyList() else (0 until a.length()).map { a.optString(it) }.filter { it.isNotEmpty() }
    private fun <T> objs(a: JSONArray?, f: (JSONObject) -> T): List<T> =
        if (a == null) emptyList() else (0 until a.length()).mapNotNull { i -> a.optJSONObject(i)?.let(f) }

    /** The snapshot from its JSON; null when it is not one (no time on it). */
    fun parse(o: JSONObject?): Snap? {
        if (o == null || o.optLong("at") <= 0) return null
        val prices = HashMap<String, Price>()
        o.optJSONObject("prices")?.let { p ->
            p.keys().forEach { sym ->
                val r = p.optJSONObject(sym) ?: return@forEach
                val v = r.optDouble("price", 0.0)
                if (v > 0) prices[sym] = Price(v, if (r.optBoolean("hasChange")) r.optDouble("change24", 0.0) else null, r.optLong("at"), r.optInt("n"))
            }
        }
        val bs = o.optJSONArray("briefStories")
        return Snap(o.optLong("at"), prices, o.optString("marketCode"), o.optDouble("marketValue", 0.0), o.optDouble("marketChange", 0.0),
            o.optString("brief"), o.optLong("briefAt"), if (bs == null) emptyList() else (0 until bs.length()).map { bs.optDouble(it, 0.0).toLong() }.filter { it > 0 },
            objs(o.optJSONArray("top")) { s -> Story(s.optLong("id"), s.optString("title"), s.optString("lead"), strings(s.optJSONArray("outlets")), s.optInt("sources"), s.optLong("lastSeen")) },
            o.optJSONObject("forYou")?.let { forYou(it) },
            com.localghost.app.ui.CalendarText.parseEvents(o.optJSONArray("calendar")))
    }

    fun forYou(f: JSONObject): ForYou = ForYou(f.optLong("at"), f.optLong("from"),
        objs(f.optJSONArray("places")) { p -> Place(p.optString("name"), p.optString("kind"), p.optDouble("km", 0.0), p.optString("bearing"), p.optString("why"), p.optBoolean("new")) },
        objs(f.optJSONArray("days")) { d -> Day(d.optString("day"), d.optInt("yearsAgo"), d.optLong("memoryId"), d.optString("title"), d.optString("lead"), d.optInt("photos"), d.optString("place")) },
        objs(f.optJSONArray("stories")) { s -> Pick(s.optLong("id"), s.optString("title"), s.optString("lead"), s.optString("why")) },
        f.optString("note"), remember(f.optJSONObject("remember")))

    private fun remember(r: JSONObject?): Remember? {
        if (r == null) return null
        val m = Remember(r.optLong("id"), r.optString("kind"), r.optString("title"), r.optString("body"))
        return if (m.id > 0 && m.body.isNotBlank()) m else null
    }

    /** What the phone keeps of a snapshot on its storage, in the box's own shape: everything but
     *  FOR YOU (the places near my trail and my days' titles stay in memory, as the trail itself
     *  is sealed). */
    fun forDisk(s: Snap): String {
        val prices = JSONObject()
        s.prices.forEach { (sym, p) ->
            prices.put(sym, JSONObject().put("price", p.price).put("change24", p.change24 ?: 0.0).put("hasChange", p.change24 != null)
                .put("at", p.at).put("n", p.n))
        }
        val bs = JSONArray()
        s.briefStories.forEach { bs.put(it) }
        val top = JSONArray()
        s.top.forEach { t ->
            val outlets = JSONArray()
            t.outlets.forEach { outlets.put(it) }
            top.put(JSONObject().put("id", t.id).put("title", t.title).put("lead", t.lead).put("outlets", outlets)
                .put("sources", t.sources).put("lastSeen", t.lastSeen))
        }
        return JSONObject().put("at", s.at).put("prices", prices).put("marketCode", s.marketCode).put("marketValue", s.marketValue)
            .put("marketChange", s.marketChange).put("brief", s.brief).put("briefAt", s.briefAt).put("briefStories", bs).put("top", top).toString()
    }

    /** The newer of a kept brief and the snapshot's: the snapshot's when it was written later. */
    fun newerBrief(keptAt: Long, snap: Snap?): Boolean = snap != null && snap.brief.isNotBlank() && snap.briefAt > keptAt

    /** "1 year ago", "3 years ago". */
    fun yearsAgo(n: Int): String = if (n == 1) "1 year ago" else "$n years ago"

    /** The heading over the memory brought back: "the box noticed", "remembered". */
    fun rememberHeading(kind: String): String = when (kind) {
        "insight" -> "your box noticed"
        "place" -> "a place of yours"
        "person" -> "one of your people"
        else -> "remembered"
    }

    /** A place's line: "park · 1.2 km W". */
    fun placeLine(p: Place): String = listOf(p.kind, distance(p.km) + (if (p.bearing.isNotEmpty()) " " + p.bearing else ""))
        .filter { it.isNotBlank() }.joinToString(" · ")

    fun distance(km: Double): String = when {
        km < 1 -> "${Math.round(km * 1000 / 50) * 50} m"
        km < 10 -> "%.1f km".format(java.util.Locale.US, km)
        else -> "${Math.round(km)} km"
    }

    /** A day's line: "24 photos · Kassiopi", "the day's story", "". */
    fun dayLine(d: Day): String = listOf(
        when (d.photos) { 0 -> ""; 1 -> "1 photo"; else -> "${d.photos} photos" },
        d.place,
    ).filter { it.isNotBlank() }.joinToString(" · ")

    /** Where a day opens: its own page (the story, the photos, the outing, the notes; the map one
     *  tap on from there). */
    fun dayTarget(d: Day): String = "day:${d.day}"

    /** The places' heading: "near you · from your trail 12 min ago". */
    fun nearFrom(from: Long, nowS: Long): String {
        if (from <= 0) return "near you"
        val m = (nowS - from).coerceAtLeast(0) / 60
        return "near you · from your trail " + when {
            m < 1 -> "just now"
            m < 60 -> "$m min ago"
            else -> "${m / 60} h ago"
        }
    }
}
