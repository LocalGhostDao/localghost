package main

// THE WEATHER, FROM THE BOX. tallyd pulls the forecast of the world's larger places, each once a day,
// (internal/weather), the same list whoever and wherever the person is, so no weather service
// learns where they are. A weather question is answered from that table: the place the question
// names (the box's GeoNames finds it, then the nearest pulled place), else the phone's own fix
// when the question came with one, else the trail's newest point. The phone used to ask
// Open-Meteo itself with its position at two decimals; it asks nobody now (the plan says the box
// has it, boxhas.go), and a question naming no place with nothing on the box gets told so.

import (
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"github.com/LocalGhostDao/localghost/server/internal/weather"
)

var (
	// the words that make a question a weather one, the phone's list
	weatherHint = regexp.MustCompile(`(?i)\b(weather|forecast|rain|raining|temperature|how (hot|cold|warm) is it|umbrella|sunny|snow|snowing|wind|windy|humid|humidity)\b`)
	// the place after "in", "at", "for", "around", "near", up to a time word or the end
	placeAfter = regexp.MustCompile(`(?i)\b(?:in|at|for|around|near)\s+([\p{L}][\p{L} .'-]{1,40}?)(?:\s+(?:today|tomorrow|tonight|this|next|on|at|now|right)\b|[?.!,]|$)`)
	timeWords  = regexp.MustCompile(`(?i)\b(today|tomorrow|tonight|now|this|next|the|week|weekend|morning|afternoon|evening|night|hour|hours|day|days|moment)\b`)
	manySpaces = regexp.MustCompile(`\s+`)
)

// weatherQuestion says whether the question is about the weather.
func weatherQuestion(prompt string) bool { return weatherHint.MatchString(prompt) }

// placeOf is the place a weather question names, "" when it names none ("weather for tomorrow"
// names a day, not a place).
func placeOf(q string) string {
	for _, m := range placeAfter.FindAllStringSubmatch(q, -1) {
		raw := strings.TrimSpace(m[1])
		place := strings.TrimSpace(manySpaces.ReplaceAllString(timeWords.ReplaceAllString(raw, " "), " "))
		place = strings.TrimRight(place, ".,")
		if n := len([]rune(place)); n >= 2 && n <= 40 && !weatherHint.MatchString(place) {
			return place
		}
	}
	return ""
}

// weatherAnswer is the forecast a weather question gets, with where it was looked up. The
// named place first; a question naming none is answered for the phone's fix, else for the
// trail's newest point. ok false when the box has nothing for it.
func weatherAnswer(db *poltergres.ReadWrite, prompt string, here *hereT, now time.Time) (text, why string, ok bool) {
	if db == nil {
		return "", "", false
	}
	if place := placeOf(prompt); place != "" {
		f, km, found := weather.ByName(db, place)
		if !found {
			return "", "", false
		}
		return weather.Describe(f, km, now), "the box's daily weather pull, for " + place, true
	}
	if here.valid() {
		if f, km, found := weather.Nearest(db, here.Lat, here.Lon); found {
			return weather.Describe(f, km, now), "the box's daily weather pull, for where the phone is", true
		}
	}
	if ts, lat, lon := hw.TrailNewest(db); ts > 0 {
		if f, km, found := weather.Nearest(db, lat, lon); found {
			return weather.Describe(f, km, now), "the box's daily weather pull, for the trail's newest point", true
		}
	}
	return "", "", false
}

// weatherItems is the chat's weather source: nothing unless the question is about the weather.
// With nothing on the box it says so, so the model does not make a forecast up.
func weatherItems(db *poltergres.ReadWrite, prompt string, here *hereT, now time.Time) []ctxItem {
	if db == nil || !weatherQuestion(prompt) {
		return nil
	}
	text, why, ok := weatherAnswer(db, prompt, here, now)
	if ok {
		return []ctxItem{{When: now.UTC().Format("2006-01-02"), Source: "weather", Snippet: text, Why: why}}
	}
	if place := placeOf(prompt); place != "" {
		return []ctxItem{{Source: "weather", Snippet: "the box has no forecast for " + place + " (it pulls the largest town of every 55 km cell of the world, " + strconv.Itoa(weather.MaxPlaces) + " places; this one is not within " + strconv.Itoa(int(weather.NearKm)) + " km of any pulled yet); say so rather than guess", Why: "a weather question about a place the box has no forecast for"}}
	}
	st := weather.Load(db)
	if st.Places == 0 {
		return []ctxItem{{Source: "weather", Snippet: "the box has no forecast yet (it pulls the world's larger places a hundred every two minutes, from a few minutes after tallyd starts, once the geo set is on the box); say so rather than guess", Why: "a weather question with no forecast on the box"}}
	}
	return []ctxItem{{Source: "weather", Snippet: "the box does not know where the phone is (no fix came with the question and the trail is empty), so it cannot say the weather there; ask for a place by name", Why: "a weather question naming no place, with no position on the box"}}
}

// weatherCovers says whether the box answers a weather question: a question naming no place
// always (the phone never sends its position to a weather service for it, whatever the box
// holds), a named place when the box has a forecast for it (else the web may answer, the place's
// name says nothing about where the phone is).
func weatherCovers(runDir, prompt string) (string, bool) {
	if !weatherQuestion(prompt) {
		return "", false
	}
	db := chatStore(filepath.Dir(runDir))
	if place := placeOf(prompt); place != "" {
		if db == nil {
			return "", false
		}
		if _, _, found := weather.ByName(db, place); found {
			return "the box's daily weather pull, for " + place, true
		}
		return "", false
	}
	return "the box's daily weather pull", true
}
