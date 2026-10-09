package com.localghost.app.ui

/**
 * WHAT EACH PANEL IS MADE OF. Every section that shows numbers or picks has an ⓘ, and this is what
 * it opens: where the data comes from, how it is made, how often, and what leaves the phone or the
 * box for it. Plain words, no marketing; the same facts the box's documentation gives. Pure, so
 * the tests read it and the texts stay in one place.
 */
object Explain {
    data class Topic(val key: String, val title: String, val paragraphs: List<String>)

    val topics: Map<String, Topic> = listOf(
        Topic("prices", "The prices", listOf(
            "BTC and ETH are the box's own price: it reads each exchange's ticker itself, every minute for the hundred largest coins and every five seconds for BTC, ETH and SOL, and blends each coin from every market it trades in: a market's last price converted to dollars through its quote currency, weighted by the market's 24-hour volume in the coin and by how fresh the price is, a price far off the coin's last value left out. No price service is asked, and nothing of yours goes to an exchange: the requests are the same public ones a browser makes.",
            "The day's change compares the price now with the price a day ago from the box's own minute series. CRYPTO50 is the box's index of crypto as a whole: the top fifty coins by market value, weighted by their average daily dollar volume over the previous calendar month, the weights fixed for the month, the value chained day to day from 1000 at its start so a change of constituents never jumps it; the box computes it after every batch of prices.",
            "The list of which coins exist and their ranks comes from Coinbase's public listing, read hourly; the pound, euro and other rates come from the ECB's daily reference rates, read once a working day. Both are fetched by the phone on Wi-Fi or by the box itself when the phone is away.",
            "A price older than a few minutes is said so under it. Box Status › feeds shows every feed's age and health.")),
        Topic("news", "The news", listOf(
            "The box reads a list of feeds you keep (SOURCES › News › feeds): each is an RSS or Atom address a publication offers. The phone fetches them on Wi-Fi and hands the bytes to the box; when the phone is away the box fetches them itself, every two hours. Nothing of yours goes with a fetch.",
            "Stories are the same event told by several outlets: the box groups entries by their titles and words, and counts the outlets. The day's brief on HOME is the box's model writing the most-told stories of the last day in a few points; each point opens its story in NEWS with every outlet's entry and a link to the article, which the phone opens, never the box.",
            "A feed that answers nothing for twelve hours in a row is switched off and said so; you can switch it back on, or take it off the list. A new feed you add is fetched within two hours, sooner on pull-to-refresh.")),
        Topic("wikipedia", "Wikipedia on the box", listOf(
            "The box holds the English Wikipedia without pictures, one file from the LocalGhost mirror (about 50 GB, every byte checked against the mirror's signed list), imported once into the box's own database: the title, the first paragraphs and the body of every article, and every redirect. The file goes once it is in.",
            "A question like \"what is X\" or a name in a question is looked up there first: by title, by a redirect, by a place with its qualifier, by prefix, by likeness of spelling, by the words of a lead. An article found means the web is not asked. The WIKIPEDIA page is the same lookup by hand, and shows an article whole with its sections.",
            "A newer edition published on the mirror is fetched and imported in place of the old one when you ask for it on SOURCES. Nothing is read from Wikipedia's own servers.")),
        Topic("weather", "The weather", listOf(
            "Once a day the box asks Open-Meteo for the forecast of the world's three thousand largest places, the same list every day whoever and wherever you are. Where you are is then looked up on the box, nearest place first, so no weather service learns where you are, nor that you asked.",
            "HOME shows the forecast nearest the phone's last position (its own, which goes to your box and nowhere else); the chat answers \"what's the weather like\" the same way. The hour of the pull, the places and how far the nearest one is are shown under it; the forecast is as fresh as the morning's pull.",
            "The place list is the box's own GeoNames (SOURCES › Maps); without it there is nothing to pull, and SOURCES says so.")),
        Topic("maps", "The maps and the heights", listOf(
            "The map is drawn on the phone from the box's own tiles: the coastline from OpenStreetMap's land polygons, the roads from Geofabrik's extracts cut on the box into tiles, the places from GeoNames, the time zones from a grid the box builds, and the ground's height from the Copernicus DEM at 90 m, in packs of a 30-degree block each. All of it comes from the LocalGhost mirror, signed, at setup or when you ask on SOURCES; no map service is asked at any time.",
            "The trail is the phone's own fixes, sealed on the phone and opened by your box PIN. A day's path, its stays and its moves are drawn by the box from the trail, the photos' positions and the roads; the climb comes from the heights under the line.",
            "The heights are asked for by region (yours by default: the box's part of the world), since the whole world is tens of gigabytes.")),
        Topic("memories", "Memories", listOf(
            "Memories are what the box keeps about you in words: distilled from your chats, check-ins and voice notes by the box's model; the people, one memory each, built from facts added over time and written up by the model from those facts only; outings and trips, folded from the days away; the days, told from the photos, the trail, the health sync and what you said; places counted over time; and what the box noticed.",
            "Every memory says where it came from. One you edit by hand stands as you wrote it: the box adds to its facts but never rewrites your words, and a memory edited by hand is the one kept when the box merges duplicates.",
            "Nothing of this leaves the box. The chat reads the memories that match a question first, before anything else.")),
        Topic("checkin", "The check-in", listOf(
            "Once a day, how you feel and why, in your words or your voice, kept as a journal entry on the box. The box's guesses, marked with a dot, come from the day's shape (sleep, steps, places) and one tap unticks them; a guess left standing is recorded as a guess, not a feeling.",
            "After the check-in the box writes the day up from the photos, the trail, the health sync, your voice notes and what you said. Notes said later in the day are added to the check-in. The strip shows the last two weeks; a day is a tap away.",
            "There are no streaks and no reminders beyond one ask a day.")),
        Topic("health", "Health", listOf(
            "Steps, sleep, exercise, heart rate, distance, calories, floors and weight come from the phone's Health Connect store (where Samsung Health and others write), read by the app with the permissions you grant and sent to your box in day batches. The box keeps them as a time series and tells each day into the journal, which is how sleep and movement reach the memories and the check-in's guesses.",
            "Nothing goes to any service: the phone reads, the box keeps.")),
        Topic("gallery", "The gallery", listOf(
            "Photos and videos sync from the phone to the box on Wi-Fi (deduplicated by content, so nothing is sent twice) and are indexed there: previews, captions and tags written by the box's own model, faces and places from what the photo carries. Search in the chat and in the gallery reads that index.",
            "The originals stay on the box; the phone keeps previews. A photo you attach to a chat is indexed the same way.")),
        Topic("sources", "Sources", listOf(
            "Everything the box draws on beyond your own archive, with its state. A fetch brings a set from the LocalGhost mirror, the one place a box takes files from after setup: the mirror's list is signed, every file's hash is checked, and a file that does not match is refused. The box reaches nothing else.",
            "A fetch runs on the box in the background (the same tools/update.sh the operator runs by hand) and this page follows it; the box stays usable meanwhile.")),
        Topic("speech", "Speech", listOf(
            "Voice notes and questions asked aloud are transcribed on the box by whisper.cpp with a speech model from the mirror, on the CPU so the chat's model keeps the GPU. The people's names from your memories are given to it first and corrected after, so a name is spelt your way.",
            "A question asked aloud is heard and forgotten: no file, no row. A voice note is kept with its words.")),
    ).associateBy { it.key }

    fun of(key: String): Topic? = topics[if (key == "crypto") "prices" else key]
}
