package com.localghost.app.ui

/** The SOURCES page's words: a state's mark, the job's line, a fetch's region and question. Pure, for the tests. */
object SourcesText {
    fun mark(state: String): String = when (state) {
        "ready" -> "●"; "partial", "importing" -> "◐"; "missing" -> "○"; "off" -> "✕"; else -> "?"
    }

    /** The state as a word on a card. */
    fun word(state: String): String = when (state) {
        "ready" -> "ready"; "partial" -> "partly"; "importing" -> "importing"; "missing" -> "not on the box"
        "off" -> "off"; else -> "unknown"
    }

    /** Each integration's glyph on its card and in the menu. */
    fun glyph(id: String): String = when (id) {
        "wikipedia" -> "W"; "news" -> "¶"; "crypto" -> "◈"; "weather" -> "☂"; "maps" -> "◎"; "speech" -> "◍"; else -> "⊛"
    }

    /** The one-word mark of a place an integration draws from (a feed, an exchange). */
    fun fromMark(state: String): String = when (state) {
        "ok" -> "●"; "late" -> "◐"; "flaky" -> "◐"; "off" -> "○"; else -> "·"
    }

    /** "from 12 places", "from one place", "" with none. */
    fun fromCount(n: Int): String = when (n) { 0 -> ""; 1 -> "from one place"; else -> "from $n places" }

    /** "fetching the maps · 12 min, running", "fetched the maps in 1 h 03 min", "the fetch of wiki stopped (exit 1)". */
    fun job(step: String, startedAt: Long, endedAt: Long, running: Boolean, exit: Int, nowS: Long): String {
        val what = when (step) { "wiki" -> "Wikipedia"; "maps" -> "the maps"; "speech" -> "the speech engine"; "engine" -> "the chat engine"; "weights" -> "the models"; else -> step }
        val took = span((if (running) nowS else endedAt) - startedAt)
        return when {
            running -> "fetching $what from the mirror · $took so far"
            exit == 0 -> "fetched $what in $took"
            else -> "the fetch of $what stopped after $took (exit $exit)"
        }
    }

    /** The heights' region around a position: 30 degrees of latitude and 40 of longitude, clipped
     *  to the world, as fetch_geo.sh reads it ("36:66,-20:20"). */
    fun region(lat: Double, lon: Double): String {
        val la0 = Math.floor(lat - 15).toInt().coerceIn(-90, 89)
        val la1 = Math.ceil(lat + 15).toInt().coerceIn(la0 + 1, 90)
        val lo0 = Math.floor(lon - 20).toInt().coerceIn(-180, 179)
        val lo1 = Math.ceil(lon + 20).toInt().coerceIn(lo0 + 1, 180)
        return "$la0:$la1,$lo0:$lo1"
    }

    fun span(secs: Long): String = when {
        secs < 60 -> "${secs.coerceAtLeast(0)} s"
        secs < 3600 -> "${secs / 60} min"
        else -> "${secs / 3600} h ${"%02d".format((secs % 3600) / 60)} min"
    }

    fun confirm(id: String): String = when (id) {
        "wikipedia" -> "The English Wikipedia without pictures: about 50 GB from the mirror, then an import of some hours into the box's database. The box stays usable; the file goes once it is in."
        "maps" -> "The coastline, the roads, the places, the time zones and the heights of your part of the world, from the mirror: tens of gigabytes the first time. The box cuts the tiles after; the map and the days redraw by themselves."
        "speech" -> "whisper.cpp and a speech model from the mirror, built on the box. A few minutes."
        "weather" -> "The maps bring the places with their populations; the weather pulls within the hour after."
        else -> "From the mirror, checked against its signed list, in the background."
    }
}
