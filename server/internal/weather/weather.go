// Package weather is the forecast for the world's larger places, pulled by the box as one fixed
// list, so that the forecast where the person is can be looked up on the box and no weather
// service ever learns where that is. Until 3 October 2026 the phone asked Open-Meteo for the
// forecast at its own position (rounded to a kilometre) when a question named no place; the
// review that noticed it was right, "never a location" has to be true. Now the box asks for the
// same places whoever and wherever the person is, and the question "what's the weather like" is
// answered from the box's own table by the trail's newest point, a named place from the box's
// own GeoNames.
//
// THE LIST. Until 9 October 2026 it was the three thousand largest places (a hundred thousand
// people or more), which crowded into the countries with the biggest cities and left Neu-Ulm
// with nothing within 120 km. Now the world is cut into half-degree cells (some 55 km a side)
// and each cell keeps its largest town of fifteen thousand people or more; the largest MaxPlaces
// of those cells are the list. Towns in the same cell share a sky anyway, and a cell's winner is
// within reach of its neighbours' questions, so the same count covers far more of the inhabited
// world. The list is the geo set's (tools/fetch_geo.sh); without it there is nothing to pull.
//
// THE PACE. Open-Meteo (open-meteo.com) answers without an account or a key and takes many
// places in one request; the free tier is for non-commercial use under ten thousand calls a
// day, five thousand an hour, six hundred a minute, a request of more than ten weather variables
// counted as more than one call (the eighteen asked here: 1.8). Counting every place in a
// request as a call, the sternest reading, the list is pulled a hundred places at a time, one
// batch every two minutes, the hundred longest unpulled first: a place is pulled again once its
// row is Every old, so a day's pull is MaxPlaces × 1.8 × (24 h / Every) calls spread over the
// hours, under every limit. Which batch goes first is the row's age and nothing else, never
// where the person is.
//
// THE HOURS. Since 10 October 2026 a pull carries the hourly forecast of the next three days
// as well as the current conditions and the days: a row sixteen hours old still says what the
// sky is doing now, from its hour, where the current block alone said what it did at the pull
// (the person saw "the weather of eight hours ago"), and the phone can draw the next day as a
// line. Pulling every place more often would put the list past the service's day; the hours
// give the freshness without.
package weather

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/egress"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

const (
	// MinPopulation, Cell and MaxPlaces bound the list: every GeoNames populated place of at
	// least this many people, the largest in each Cell-degree cell, the largest MaxPlaces of
	// those.
	MinPopulation = 15_000
	Cell          = 0.5
	MaxPlaces     = 6000
	// Batch is how many places go in one request (Open-Meteo takes a comma list; a hundred
	// keeps the URL short and the answer under a megabyte); BatchEvery is the pace between
	// batches, which keeps a day's pull under the service's hourly and minute limits.
	Batch      = 100
	BatchEvery = 2 * time.Minute
	// Days is the forecast length kept: today and the three after; HourDays is how many of those
	// days' hours are kept (from the pull's midnight: a row Every old still has a day ahead).
	Days     = 4
	HourDays = 3
	// Every is how often a place is pulled again; Stale is when a row is too old to answer with.
	Every = 16 * time.Hour
	Stale = 36 * time.Hour
	// NearKm is how far a place may be from the point asked about and still be its weather:
	// the forecast grid is tens of kilometres, a city a hundred kilometres away is another sky.
	NearKm = 120.0
	// Source names the pull in the fetch log and the chat's context record.
	Source = "open-meteo"
	// Pause is the breath between batches: a courtesy to a free service, not a rate limit.
	Pause = 1500 * time.Millisecond
)

// BaseURL is Open-Meteo's forecast endpoint; a test points it at its own server.
var BaseURL = "https://api.open-meteo.com/v1/forecast"

// Place is one of the places pulled.
type Place struct {
	ID         int64   `json:"id"`
	Name       string  `json:"name"`
	Country    string  `json:"country"`
	Lat        float64 `json:"lat"`
	Lon        float64 `json:"lon"`
	Population int64   `json:"population"`
}

// Now is the current conditions at a place.
type Now struct {
	At       string  `json:"at"` // the place's local time, as the service gives it
	TempC    float64 `json:"tempC"`
	FeelsC   float64 `json:"feelsC"`
	Humidity int     `json:"humidity"`
	PrecipMM float64 `json:"precipMm"`
	Code     int     `json:"code"` // WMO weather code
	WindKmh  float64 `json:"windKmh"`
}

// Day is one day of the forecast.
type Day struct {
	Date     string  `json:"date"`
	Code     int     `json:"code"`
	MaxC     float64 `json:"maxC"`
	MinC     float64 `json:"minC"`
	RainPct  int     `json:"rainPct"`
	PrecipMM float64 `json:"precipMm"`
	Sunrise  string  `json:"sunrise,omitempty"` // "07:12", the place's local time
	Sunset   string  `json:"sunset,omitempty"`
}

// Hour is one hour of the forecast.
type Hour struct {
	At       string  `json:"at"` // the place's local time, "2026-10-10T14:00"
	TempC    float64 `json:"tempC"`
	RainPct  int     `json:"rainPct"` // -1 when the service gave none
	PrecipMM float64 `json:"precipMm"`
	Code     int     `json:"code"`
	WindKmh  float64 `json:"windKmh"`
}

// Forecast is what is kept for a place.
type Forecast struct {
	Place     Place  `json:"place"`
	Timezone  string `json:"timezone"`
	Now       Now    `json:"now"`
	Days      []Day  `json:"days"`
	Hours     []Hour `json:"hours,omitempty"` // HourDays × 24 from the pull day's midnight, local
	FetchedAt int64  `json:"fetchedAt"`
	UTCOffset int    `json:"utcOffset,omitempty"` // seconds east of UTC at the place, from the service
}

// Current is the conditions now: the current block when the pull is under ninety minutes old,
// else the hour of the forecast the clock is in (the pull's own "now" is the pull's, not the
// person's). ok is false when the row has nothing for this hour.
func (f Forecast) Current(now time.Time) (Now, bool) {
	if f.FetchedAt > 0 && now.Unix()-f.FetchedAt < 90*60 {
		return f.Now, true
	}
	h, ok := f.HourAt(now)
	if !ok {
		return f.Now, f.FetchedAt > 0
	}
	return Now{At: h.At, TempC: h.TempC, FeelsC: h.TempC, Humidity: 0, PrecipMM: h.PrecipMM, Code: h.Code, WindKmh: h.WindKmh}, true
}

// HourAt is the forecast hour the clock is in, at the place (its UTC offset), from the hours
// kept.
func (f Forecast) HourAt(now time.Time) (Hour, bool) {
	if len(f.Hours) == 0 {
		return Hour{}, false
	}
	local := now.UTC().Add(time.Duration(f.UTCOffset) * time.Second)
	want := local.Format("2006-01-02T15") + ":00"
	for _, h := range f.Hours {
		if h.At == want {
			return h, true
		}
	}
	return Hour{}, false
}

// HoursFrom is the next n hours from the hour the clock is in, for a line of the day ahead.
func (f Forecast) HoursFrom(now time.Time, n int) []Hour {
	if len(f.Hours) == 0 || n <= 0 {
		return nil
	}
	local := now.UTC().Add(time.Duration(f.UTCOffset) * time.Second)
	want := local.Format("2006-01-02T15") + ":00"
	for i, h := range f.Hours {
		if h.At == want {
			end := i + n
			if end > len(f.Hours) {
				end = len(f.Hours)
			}
			return f.Hours[i:end]
		}
	}
	return nil
}

// Querier is the slice of a connection the lookups need.
type Querier interface {
	Query(sql string, args ...any) (*poltergres.Rows, error)
}

// placesSQL is the list: the largest populated place of MinPopulation or more in each Cell-degree
// cell, the largest MaxPlaces of those, largest first.
const placesSQL = `SELECT geonameid, name, country, lat, lon, population FROM (
		SELECT DISTINCT ON (floor(lat / $3), floor(lon / $3)) geonameid, name, country, lat, lon, population
		FROM geo_points WHERE kind = 'P' AND population >= $1
		ORDER BY floor(lat / $3), floor(lon / $3), population DESC, geonameid) c
	ORDER BY population DESC, geonameid LIMIT $2`

// Places is the list from the box's GeoNames: the largest place of MinPopulation or more in each
// Cell-degree cell, the largest MaxPlaces of those, largest first. Empty when the geo set is not
// on the box.
func Places(db Querier) ([]Place, error) {
	rows, err := db.Query(placesSQL, MinPopulation, MaxPlaces, Cell)
	if err != nil {
		return nil, err
	}
	return readPlaces(rows), nil
}

// NextBatch is the Batch places longest unpulled (never pulled first, then the oldest rows), and
// whether they are due: the oldest of them Every old or never pulled. Nothing when the geo set
// is not on the box. The age of a row is the only order: never where the person is.
func NextBatch(db Querier, now time.Time) (batch []Place, due bool, err error) {
	rows, err := db.Query(`SELECT g.geonameid, g.name, g.country, g.lat, g.lon, g.population, coalesce(w.fetched_at, 0)
		FROM (`+placesSQL+`) g LEFT JOIN weather_places w ON w.geonameid = g.geonameid
		ORDER BY coalesce(w.fetched_at, 0), g.population DESC, g.geonameid LIMIT $4`, MinPopulation, MaxPlaces, Cell, Batch)
	if err != nil {
		return nil, false, err
	}
	batch = readPlaces(rows)
	if len(batch) == 0 {
		return nil, false, nil
	}
	oldest, _ := strconv.ParseInt(deref(rows.Vals[0][6]), 10, 64)
	return batch, oldest == 0 || now.Unix()-oldest >= int64(Every/time.Second), nil
}

func readPlaces(rows *poltergres.Rows) []Place {
	out := make([]Place, 0, len(rows.Vals))
	for _, v := range rows.Vals {
		if len(v) < 6 || v[0] == nil {
			continue
		}
		var p Place
		p.ID, _ = strconv.ParseInt(deref(v[0]), 10, 64)
		p.Name = deref(v[1])
		p.Country = deref(v[2])
		p.Lat, _ = strconv.ParseFloat(deref(v[3]), 64)
		p.Lon, _ = strconv.ParseFloat(deref(v[4]), 64)
		p.Population, _ = strconv.ParseInt(deref(v[5]), 10, 64)
		out = append(out, p)
	}
	return out
}

// URL is the request for one batch of places: current conditions and Days days, each place in
// its own time zone, every number metric.
func URL(batch []Place) string {
	lats := make([]string, len(batch))
	lons := make([]string, len(batch))
	for i, p := range batch {
		lats[i] = strconv.FormatFloat(p.Lat, 'f', 2, 64)
		lons[i] = strconv.FormatFloat(p.Lon, 'f', 2, 64)
	}
	return BaseURL + "?latitude=" + strings.Join(lats, ",") + "&longitude=" + strings.Join(lons, ",") +
		"&current=temperature_2m,apparent_temperature,relative_humidity_2m,precipitation,weather_code,wind_speed_10m" +
		"&hourly=temperature_2m,precipitation_probability,precipitation,weather_code,wind_speed_10m" +
		"&daily=weather_code,temperature_2m_max,temperature_2m_min,precipitation_probability_max,precipitation_sum,sunrise,sunset" +
		"&forecast_days=" + strconv.Itoa(Days) + "&timezone=auto"
}

// answer is Open-Meteo's shape for one place.
type answer struct {
	Latitude  float64 `json:"latitude"`
	Longitude float64 `json:"longitude"`
	Timezone  string  `json:"timezone"`
	UTCOffset int     `json:"utc_offset_seconds"`
	Hourly    struct {
		Time    []string   `json:"time"`
		Temp    []float64  `json:"temperature_2m"`
		RainPct []*float64 `json:"precipitation_probability"`
		Precip  []float64  `json:"precipitation"`
		Code    []int      `json:"weather_code"`
		Wind    []float64  `json:"wind_speed_10m"`
	} `json:"hourly"`
	Current struct {
		Time     string  `json:"time"`
		Temp     float64 `json:"temperature_2m"`
		Feels    float64 `json:"apparent_temperature"`
		Humidity float64 `json:"relative_humidity_2m"`
		Precip   float64 `json:"precipitation"`
		Code     int     `json:"weather_code"`
		Wind     float64 `json:"wind_speed_10m"`
	} `json:"current"`
	Daily struct {
		Time    []string   `json:"time"`
		Code    []int      `json:"weather_code"`
		Max     []float64  `json:"temperature_2m_max"`
		Min     []float64  `json:"temperature_2m_min"`
		RainPct []*float64 `json:"precipitation_probability_max"`
		Precip  []float64  `json:"precipitation_sum"`
		Sunrise []string   `json:"sunrise"`
		Sunset  []string   `json:"sunset"`
	} `json:"daily"`
}

// Parse reads one batch's answer (an array for several places, an object for one) and pairs it
// with the places asked, in order; the service answers in the order asked.
func Parse(body []byte, batch []Place, fetchedAt int64) ([]Forecast, error) {
	body = []byte(strings.TrimSpace(string(body)))
	var answers []answer
	if len(body) > 0 && body[0] == '{' {
		var one answer
		if err := json.Unmarshal(body, &one); err != nil {
			return nil, err
		}
		if one.Daily.Time == nil && one.Timezone == "" {
			return nil, errors.New("no forecast in the answer")
		}
		answers = []answer{one}
	} else if err := json.Unmarshal(body, &answers); err != nil {
		return nil, err
	}
	if len(answers) != len(batch) {
		return nil, fmt.Errorf("asked for %d places, the answer has %d", len(batch), len(answers))
	}
	out := make([]Forecast, 0, len(batch))
	for i, a := range answers {
		f := Forecast{Place: batch[i], Timezone: a.Timezone, FetchedAt: fetchedAt, UTCOffset: a.UTCOffset}
		for j, at := range a.Hourly.Time {
			if j >= HourDays*24 {
				break
			}
			h := Hour{At: at, RainPct: -1}
			if j < len(a.Hourly.Temp) {
				h.TempC = a.Hourly.Temp[j]
			}
			if j < len(a.Hourly.RainPct) && a.Hourly.RainPct[j] != nil {
				h.RainPct = int(math.Round(*a.Hourly.RainPct[j]))
			}
			if j < len(a.Hourly.Precip) {
				h.PrecipMM = a.Hourly.Precip[j]
			}
			if j < len(a.Hourly.Code) {
				h.Code = a.Hourly.Code[j]
			}
			if j < len(a.Hourly.Wind) {
				h.WindKmh = a.Hourly.Wind[j]
			}
			f.Hours = append(f.Hours, h)
		}
		f.Now = Now{At: a.Current.Time, TempC: a.Current.Temp, FeelsC: a.Current.Feels, Humidity: int(math.Round(a.Current.Humidity)),
			PrecipMM: a.Current.Precip, Code: a.Current.Code, WindKmh: a.Current.Wind}
		for j, date := range a.Daily.Time {
			d := Day{Date: date}
			if j < len(a.Daily.Code) {
				d.Code = a.Daily.Code[j]
			}
			if j < len(a.Daily.Max) {
				d.MaxC = a.Daily.Max[j]
			}
			if j < len(a.Daily.Min) {
				d.MinC = a.Daily.Min[j]
			}
			if j < len(a.Daily.RainPct) && a.Daily.RainPct[j] != nil {
				d.RainPct = int(math.Round(*a.Daily.RainPct[j]))
			} else {
				d.RainPct = -1
			}
			if j < len(a.Daily.Precip) {
				d.PrecipMM = a.Daily.Precip[j]
			}
			if j < len(a.Daily.Sunrise) {
				d.Sunrise = clock(a.Daily.Sunrise[j])
			}
			if j < len(a.Daily.Sunset) {
				d.Sunset = clock(a.Daily.Sunset[j])
			}
			f.Days = append(f.Days, d)
		}
		out = append(out, f)
	}
	return out, nil
}

// clock is the time of day of an ISO local timestamp ("2026-10-03T07:12" → "07:12").
func clock(iso string) string {
	if i := strings.IndexByte(iso, 'T'); i >= 0 && len(iso) >= i+6 {
		return iso[i+1 : i+6]
	}
	return ""
}

// Save writes the forecasts, one row a place, replacing the day before's.
func Save(db *poltergres.ReadWrite, fs []Forecast) error {
	for _, f := range fs {
		b, err := json.Marshal(f)
		if err != nil {
			return err
		}
		if err := db.Exec(`INSERT INTO weather_places (geonameid, name, country, lat, lon, population, fetched_at, forecast)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
			ON CONFLICT (geonameid) DO UPDATE SET name = EXCLUDED.name, country = EXCLUDED.country, lat = EXCLUDED.lat,
			  lon = EXCLUDED.lon, population = EXCLUDED.population, fetched_at = EXCLUDED.fetched_at, forecast = EXCLUDED.forecast`,
			f.Place.ID, f.Place.Name, f.Place.Country, f.Place.Lat, f.Place.Lon, f.Place.Population, f.FetchedAt, string(b)); err != nil {
			return err
		}
	}
	return nil
}

// State is what the table holds: how many places, and when the newest pull was.
type State struct {
	Places    int   `json:"places"`
	FetchedAt int64 `json:"fetchedAt"` // the newest row's; 0 when empty
	OldestAt  int64 `json:"oldestAt"`
}

// Load reads the table's state.
func Load(db Querier) State {
	var st State
	rows, err := db.Query("SELECT count(*), coalesce(max(fetched_at), 0), coalesce(min(fetched_at), 0) FROM weather_places")
	if err != nil || len(rows.Vals) == 0 || len(rows.Vals[0]) < 3 {
		return st
	}
	n, _ := strconv.Atoi(deref(rows.Vals[0][0]))
	st.Places = n
	st.FetchedAt, _ = strconv.ParseInt(deref(rows.Vals[0][1]), 10, 64)
	st.OldestAt, _ = strconv.ParseInt(deref(rows.Vals[0][2]), 10, 64)
	return st
}

// Due says whether the next batch is wanted, from the table's oldest row: nothing in the table
// or a row Every old. NextBatch says it for the list proper (a place not yet in the table is a
// row of age nothing); this is the glance for the status pages.
func Due(st State, now time.Time) bool {
	return st.OldestAt == 0 || now.Unix()-st.OldestAt >= int64(Every/time.Second)
}

// Nearest is the forecast of the pulled place closest to a point, within NearKm, with the
// distance; ok false when none is that close (the open sea, a desert, a box without the geo set).
func Nearest(db Querier, lat, lon float64) (f Forecast, km float64, ok bool) {
	cosLat := math.Cos(lat * math.Pi / 180)
	if cosLat < 0.05 {
		cosLat = 0.05
	}
	dLat := NearKm / 111.0
	dLon := NearKm / (111.0 * cosLat)
	// a row too old to answer with is no answer (a place that fell off the list keeps its row)
	rows, err := db.Query(`SELECT forecast FROM weather_places WHERE lat BETWEEN $1 AND $2 AND lon BETWEEN $3 AND $4 AND fetched_at > $5`,
		lat-dLat, lat+dLat, lon-dLon, lon+dLon, time.Now().Add(-Stale).Unix())
	if err != nil {
		return f, 0, false
	}
	best := math.MaxFloat64
	for _, v := range rows.Vals {
		if len(v) == 0 || v[0] == nil {
			continue
		}
		var c Forecast
		if json.Unmarshal([]byte(*v[0]), &c) != nil {
			continue
		}
		if d := haversineKm(lat, lon, c.Place.Lat, c.Place.Lon); d < best {
			best, f = d, c
		}
	}
	if best > NearKm {
		return f, 0, false
	}
	return f, best, true
}

// ByName is the forecast for a place named by the person: the pulled place of that name
// (largest first), else the box's GeoNames for the name and the pulled place nearest it.
func ByName(db Querier, name string) (f Forecast, km float64, ok bool) {
	name = strings.TrimSpace(name)
	if name == "" {
		return f, 0, false
	}
	rows, err := db.Query(`SELECT forecast FROM weather_places WHERE lower(name) = lower($1) ORDER BY population DESC LIMIT 1`, name)
	if err == nil && len(rows.Vals) > 0 && len(rows.Vals[0]) > 0 && rows.Vals[0][0] != nil {
		if json.Unmarshal([]byte(*rows.Vals[0][0]), &f) == nil {
			return f, 0, true
		}
	}
	rows, err = db.Query(`SELECT lat, lon FROM geo_points WHERE kind = 'P' AND lower(name) = lower($1) ORDER BY population DESC LIMIT 1`, name)
	if err != nil || len(rows.Vals) == 0 || len(rows.Vals[0]) < 2 || rows.Vals[0][0] == nil {
		return f, 0, false
	}
	lat, _ := strconv.ParseFloat(deref(rows.Vals[0][0]), 64)
	lon, _ := strconv.ParseFloat(deref(rows.Vals[0][1]), 64)
	return Nearest(db, lat, lon)
}

// Describe is the forecast as one paragraph the model can quote and the person can read: where
// it is for (and how far that place is from the point asked about), now, then the days.
func Describe(f Forecast, km float64, now time.Time) string {
	var sb strings.Builder
	where := f.Place.Name
	if f.Place.Country != "" {
		where += ", " + f.Place.Country
	}
	sb.WriteString("Weather for " + where)
	if km >= 1 {
		sb.WriteString(fmt.Sprintf(" (%.0f km away, the nearest place the box has)", km))
	}
	if f.FetchedAt > 0 {
		sb.WriteString(", from the box's pull " + ago(now.Unix()-f.FetchedAt) + " ago")
	}
	sb.WriteString(". ")
	n, _ := f.Current(now)
	if _, fromHour := f.HourAt(now); fromHour && now.Unix()-f.FetchedAt >= 90*60 {
		sb.WriteString("Now (this hour of the forecast): ")
	} else {
		sb.WriteString("Now: ")
	}
	sb.WriteString(CodeWords(n.Code) + ", " + num(n.TempC) + "°C")
	if n.FeelsC != 0 && math.Abs(n.FeelsC-n.TempC) >= 1 {
		sb.WriteString(" (feels " + num(n.FeelsC) + "°C)")
	}
	if n.Humidity > 0 {
		sb.WriteString(", humidity " + strconv.Itoa(n.Humidity) + "%")
	}
	sb.WriteString(", wind " + num(n.WindKmh) + " km/h. ")
	names := []string{"Today", "Tomorrow"}
	for i, d := range f.Days {
		label := dayName(d.Date)
		if i < len(names) {
			label = names[i]
		}
		sb.WriteString(label + ": " + CodeWords(d.Code) + ", " + num(d.MinC) + "–" + num(d.MaxC) + "°C")
		if d.RainPct >= 0 {
			sb.WriteString(", rain " + strconv.Itoa(d.RainPct) + "%")
		}
		if d.PrecipMM > 0 {
			sb.WriteString(" (" + num(d.PrecipMM) + " mm)")
		}
		if i == 0 && d.Sunrise != "" && d.Sunset != "" {
			sb.WriteString(", sun " + d.Sunrise + "–" + d.Sunset)
		}
		sb.WriteString(". ")
	}
	if f.Timezone != "" {
		sb.WriteString("Local time zone " + f.Timezone + ".")
	}
	return strings.TrimSpace(sb.String())
}

// CodeWords is a WMO weather code in words, the ones Open-Meteo uses.
func CodeWords(c int) string {
	switch c {
	case 0:
		return "clear"
	case 1:
		return "mostly clear"
	case 2:
		return "partly cloudy"
	case 3:
		return "overcast"
	case 45, 48:
		return "fog"
	case 51, 53, 55:
		return "drizzle"
	case 56, 57:
		return "freezing drizzle"
	case 61:
		return "light rain"
	case 63:
		return "rain"
	case 65:
		return "heavy rain"
	case 66, 67:
		return "freezing rain"
	case 71:
		return "light snow"
	case 73:
		return "snow"
	case 75:
		return "heavy snow"
	case 77:
		return "snow grains"
	case 80:
		return "light showers"
	case 81:
		return "showers"
	case 82:
		return "violent showers"
	case 85, 86:
		return "snow showers"
	case 95:
		return "thunderstorm"
	case 96, 99:
		return "thunderstorm with hail"
	}
	return "unknown"
}

// Fetcher is what a pass needs from the network: a GET with an id for the fetch log
// (egress.Client is one).
type Fetcher interface {
	GetCapped(ctx context.Context, id, url string, cap int64) (egress.Fetched, error)
}

// Result is what a pass did.
type Result struct {
	Places  int           `json:"places"`
	Batches int           `json:"batches"`
	Failed  int           `json:"failed"` // batches that came back wrong or not at all
	LastErr string        `json:"lastErr,omitempty"`
	Took    time.Duration `json:"-"`
	TookMs  int64         `json:"tookMs"`
	Fetches []Fetch       `json:"-"`
}

// Fetch is one batch's outcome, for the fetch log.
type Fetch struct {
	Status int
	OK     bool
	TookMs int
	Bytes  int
	Items  int
	Error  string
}

// Pass pulls every place in batches and saves what came; sleep is the pause between batches
// (nil: Pause).
func Pass(ctx context.Context, db *poltergres.ReadWrite, get Fetcher, places []Place, now time.Time, sleep func(time.Duration)) Result {
	if sleep == nil {
		sleep = func(d time.Duration) {
			select {
			case <-ctx.Done():
			case <-time.After(d):
			}
		}
	}
	t0 := time.Now()
	var r Result
	for start := 0; start < len(places); start += Batch {
		if ctx.Err() != nil {
			break
		}
		end := start + Batch
		if end > len(places) {
			end = len(places)
		}
		batch := places[start:end]
		r.Batches++
		f, err := get.GetCapped(ctx, Source, URL(batch), 6<<20) // a hundred places with their hours is a few megabytes
		if err != nil {
			break // the context ended
		}
		fx := Fetch{Status: f.Status, TookMs: f.TookMs, Bytes: len(f.Body), Error: f.Error}
		if f.Status != 200 || f.Body == "" {
			r.Failed++
			if fx.Error == "" {
				fx.Error = "status " + strconv.Itoa(f.Status)
			}
			r.LastErr = fx.Error
			r.Fetches = append(r.Fetches, fx)
			if start+Batch < len(places) {
				sleep(Pause)
			}
			continue
		}
		fs, perr := Parse([]byte(f.Body), batch, now.Unix())
		if perr != nil {
			r.Failed++
			fx.Error = perr.Error()
			r.LastErr = fx.Error
			r.Fetches = append(r.Fetches, fx)
			if start+Batch < len(places) {
				sleep(Pause)
			}
			continue
		}
		if serr := Save(db, fs); serr != nil {
			r.Failed++
			fx.Error = "save: " + serr.Error()
			r.LastErr = fx.Error
			r.Fetches = append(r.Fetches, fx)
			continue
		}
		fx.OK = true
		fx.Items = len(fs)
		r.Places += len(fs)
		r.Fetches = append(r.Fetches, fx)
		if start+Batch < len(places) {
			sleep(Pause)
		}
	}
	r.Took = time.Since(t0)
	r.TookMs = r.Took.Milliseconds()
	return r
}

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	const R = 6371.0
	toRad := math.Pi / 180
	dLat := (lat2 - lat1) * toRad
	dLon := (lon2 - lon1) * toRad
	a := math.Sin(dLat/2)*math.Sin(dLat/2) + math.Cos(lat1*toRad)*math.Cos(lat2*toRad)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * R * math.Asin(math.Sqrt(a))
}

func num(v float64) string {
	if v == math.Trunc(v) {
		return strconv.Itoa(int(v))
	}
	return strconv.FormatFloat(v, 'f', 1, 64)
}

func dayName(date string) string {
	t, err := time.Parse("2006-01-02", date)
	if err != nil {
		return date
	}
	return t.Weekday().String()
}

func ago(sec int64) string {
	switch {
	case sec < 90:
		return "a minute"
	case sec < 3600:
		return strconv.Itoa(int(sec/60)) + " min"
	case sec < 2*3600:
		return "an hour"
	case sec < 48*3600:
		return strconv.Itoa(int(sec/3600)) + " h"
	}
	return strconv.Itoa(int(sec/86400)) + " days"
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
