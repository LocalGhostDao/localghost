package com.localghost.app.ui

import androidx.compose.foundation.background
import androidx.compose.foundation.border
import androidx.compose.foundation.layout.*
import androidx.compose.foundation.lazy.LazyColumn
import androidx.compose.material3.MaterialTheme
import androidx.compose.material3.Text
import androidx.compose.runtime.*
import androidx.compose.ui.Alignment
import androidx.compose.ui.Modifier
import androidx.compose.ui.graphics.RectangleShape
import androidx.compose.ui.platform.LocalContext
import androidx.compose.ui.unit.dp
import com.localghost.app.net.BoxClient
import com.localghost.app.net.HomeCache
import com.localghost.app.ui.theme.*

/**
 * WEATHER , the card's page. Opens on the forecast the phone kept (HomeCache), then asks the box
 * for the one at the phone's last fix. The sky now, two days as a line with the models' spread
 * as a band, every hour of the days the box forecast, the days with their sun, which model runs
 * the box read and what each model is, how far they disagreed, and how old the copy is. The
 * position goes to the box and nowhere else, as on HOME.
 */
@Composable
fun WeatherScreen() {
    val ctx = LocalContext.current
    var w by remember { mutableStateOf<BoxClient.Weather?>(HomeCache.weather(ctx)) }
    var nowS by remember { mutableLongStateOf(System.currentTimeMillis() / 1000) }
    var tick by remember { mutableIntStateOf(0) }
    var asked by remember { mutableStateOf(false) }
    LaunchedEffect(tick) {
        val fix = com.localghost.app.sync.LocationLog.last(ctx)
        if (fix != null) BoxClient.weather(ctx, fix.lat, fix.lon)?.let { w = it }
        asked = true
        nowS = System.currentTimeMillis() / 1000
    }
    val f = w
    LazyColumn(Modifier.fillMaxSize().padding(horizontal = 20.dp), verticalArrangement = Arrangement.spacedBy(10.dp)) {
        item {
            Spacer(Modifier.height(12.dp))
            Row(verticalAlignment = Alignment.CenterVertically) { SectionLabel("WEATHER"); InfoButton("weather") }
            Spacer(Modifier.height(4.dp))
            when {
                f == null && !asked -> LoadingRow()
                f == null -> Text("no position yet, or the box has not answered , the card fills once the trail has a fix and the box a forecast", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                f.noGeo -> Text("nothing pulled: the box's place list is missing · INTEGRATIONS › Weather", color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                f.place.isEmpty() -> Text(f.text.ifBlank { "no forecast on the box yet" }, color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                else -> {
                    val where = if (f.here) "where you are, near ${f.place}" else if (f.country.isNotBlank()) "${f.place}, ${f.country}" else f.place
                    Text(where, color = GhostText, style = MaterialTheme.typography.bodyMedium)
                    Text(WeatherText.ageLine(f.fetchedAt, f.readAt, nowS, f.source.isNotBlank()), color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                }
            }
        }
        if (f != null && !f.noGeo && f.place.isNotEmpty()) {
            // NOW, then two days as the line with the band
            item {
                val cur = f.current(nowS)
                Column(Modifier.fillMaxWidth().border(1.dp, GhostBorder, RectangleShape).background(VoidLighter).padding(14.dp)) {
                    if (cur != null) Text(HomeText.weatherNow(cur.tempC, Double.NaN, cur.code, cur.windKmh), color = GhostText, style = MaterialTheme.typography.titleMedium)
                    else Text(HomeText.weatherNow(f.tempC, f.feelsC, f.code, f.windKmh), color = GhostText, style = MaterialTheme.typography.titleMedium)
                    val extras = ArrayList<String>()
                    if (f.humidity > 0 && cur == null) extras.add("humidity ${f.humidity}%")
                    if (cur != null && cur.rainPct >= 5) extras.add("rain ${cur.rainPct}%")
                    if (cur != null && cur.spreadC >= 0.5) extras.add("models ±%.0f°".format(java.util.Locale.UK, cur.spreadC / 2))
                    if (extras.isNotEmpty()) Text(extras.joinToString(" · "), color = GhostTextDim, style = MaterialTheme.typography.labelMedium)
                    val next = WeatherHours.next(f.hours, nowS, f.utcOffset, 48)
                    if (next.size >= 6) {
                        Spacer(Modifier.height(10.dp))
                        WeatherLine(next, height = 96.dp, band = true)
                        val sp = WeatherText.spreadLine(next)
                        if (sp.isNotEmpty()) Text(sp + " (the band)", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                        else Text("the next two days: the temperature as the line, the chance of rain as the bars", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                    }
                }
            }
            // THE DAYS
            if (f.days.isNotEmpty()) {
                item {
                    SectionLabel("DAYS")
                    Spacer(Modifier.height(4.dp))
                    f.days.forEach { d -> Text(WeatherText.dayRow(d.date, d.maxC, d.minC, d.code, d.rainPct, d.precipMm, d.sunrise, d.sunset), color = GhostText, style = MaterialTheme.typography.labelMedium) }
                    if (f.days.any { it.sunrise.isNotEmpty() }) Text("↑ sunrise ↓ sunset, the place's own time", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                }
            }
            // EVERY HOUR, by day, from the hour the clock is in
            val from = WeatherHours.indexNow(f.hours, nowS, f.utcOffset).let { if (it < 0) 0 else it }
            val today = WeatherHours.keyAt(nowS, f.utcOffset).take(10)
            val days = WeatherText.byDay(f.hours.drop(from))
            days.forEach { (date, hs) ->
                item(key = "h-$date") {
                    Spacer(Modifier.height(2.dp))
                    SectionLabel(WeatherText.dayName(date, today).uppercase())
                    Spacer(Modifier.height(4.dp))
                    hs.forEach { h -> Text(WeatherText.hourRow(h), color = GhostText, style = MaterialTheme.typography.labelMedium) }
                }
            }
            if (f.hours.isNotEmpty() && f.hours.any { it.spreadC > 0 }) item {
                Text("± is half the spread between the models' temperatures for the hour", color = TerminalDim, style = MaterialTheme.typography.labelSmall)
            }
            // THE SOURCES
            item {
                Spacer(Modifier.height(2.dp))
                SectionLabel("SOURCES")
                Spacer(Modifier.height(4.dp))
                WeatherText.sourceRows(f.source).forEach { r ->
                    Text(r, color = GhostText, style = MaterialTheme.typography.labelMedium)
                    val note = WeatherText.modelNote(r.substringBefore(','))
                    if (note.isNotEmpty()) Text(note, color = TerminalDim, style = MaterialTheme.typography.labelSmall)
                }
                Spacer(Modifier.height(4.dp))
                Text(if (f.source.isNotBlank())
                    "the box reads the models' own grids from the weather centres, blends them per hour with the disagreement kept, and corrects the temperature for the ground's height here. Your position goes to the box and nowhere else."
                    else "the box pulls the same list of places for everyone, so no service learns where you are; the card is the nearest.",
                    color = TerminalDim, style = MaterialTheme.typography.labelSmall)
            }
            item {
                Spacer(Modifier.height(6.dp))
                GhostButton("refresh", onClick = { asked = false; tick++ })
            }
        }
        item { Spacer(Modifier.height(20.dp)) }
    }
}
