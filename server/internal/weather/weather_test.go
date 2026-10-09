package weather

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/egress"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// One request a batch, the coordinates at two decimals in the order asked, every field the
// forecast needs, in each place's own time zone.
func TestURL(t *testing.T) {
	u := URL([]Place{{Lat: 51.5074, Lon: -0.1278}, {Lat: 44.4268, Lon: 26.1025}})
	for _, want := range []string{"latitude=51.51,44.43", "longitude=-0.13,26.10", "forecast_days=4", "timezone=auto", "current=temperature_2m", "daily=weather_code"} {
		if !strings.Contains(u, want) {
			t.Fatalf("%s missing from %s", want, u)
		}
	}
	if !strings.HasPrefix(u, BaseURL) {
		t.Fatal(u)
	}
}

const london = `{"latitude":51.5,"longitude":-0.12,"timezone":"Europe/London",
"current":{"time":"2026-10-03T09:15","temperature_2m":14.3,"apparent_temperature":12.1,"relative_humidity_2m":77,"precipitation":0,"weather_code":3,"wind_speed_10m":18.4},
"daily":{"time":["2026-10-03","2026-10-04","2026-10-05","2026-10-06"],"weather_code":[3,61,2,0],"temperature_2m_max":[16.2,14.8,15.1,17],"temperature_2m_min":[9.4,10.2,8.1,7.7],
"precipitation_probability_max":[10,80,null,0],"precipitation_sum":[0,6.4,0.2,0],"sunrise":["2026-10-03T07:05","2026-10-04T07:07","2026-10-05T07:08","2026-10-06T07:10"],"sunset":["2026-10-03T18:35","2026-10-04T18:33","2026-10-05T18:31","2026-10-06T18:28"]}}`

// An array for a batch, an object for a single place; the days keep their order, a missing
// rain chance is -1 not 0, the sun times are the clock only.
func TestParse(t *testing.T) {
	places := []Place{{ID: 2643743, Name: "London", Country: "GB", Lat: 51.5074, Lon: -0.1278}}
	fs, err := Parse([]byte(london), places, 1_000_000)
	if err != nil || len(fs) != 1 {
		t.Fatal(err, len(fs))
	}
	f := fs[0]
	if f.Place.Name != "London" || f.Timezone != "Europe/London" || f.FetchedAt != 1_000_000 {
		t.Fatalf("%+v", f)
	}
	if f.Now.TempC != 14.3 || f.Now.FeelsC != 12.1 || f.Now.Humidity != 77 || f.Now.Code != 3 || f.Now.WindKmh != 18.4 {
		t.Fatalf("%+v", f.Now)
	}
	if len(f.Days) != 4 || f.Days[1].Code != 61 || f.Days[1].RainPct != 80 || f.Days[1].PrecipMM != 6.4 || f.Days[2].RainPct != -1 || f.Days[0].Sunrise != "07:05" || f.Days[0].Sunset != "18:35" {
		t.Fatalf("%+v", f.Days)
	}
	two := []Place{places[0], {ID: 683506, Name: "Bucharest", Country: "RO", Lat: 44.43, Lon: 26.1}}
	fs, err = Parse([]byte("["+london+","+london+"]"), two, 5)
	if err != nil || len(fs) != 2 || fs[1].Place.Name != "Bucharest" {
		t.Fatal(err, len(fs))
	}
	if _, err := Parse([]byte("["+london+"]"), two, 5); err == nil {
		t.Fatal("one answer for two places must fail")
	}
	if _, err := Parse([]byte(`{"error":true,"reason":"too many"}`), places, 5); err == nil {
		t.Fatal("an error object is not a forecast")
	}
}

// The paragraph the model quotes: where, how old, now, the days by name, the time zone.
func TestDescribe(t *testing.T) {
	fs, _ := Parse([]byte(london), []Place{{ID: 1, Name: "London", Country: "GB", Lat: 51.5074, Lon: -0.1278}}, 1_000_000)
	now := time.Unix(1_000_000+3*3600, 0)
	d := Describe(fs[0], 0, now)
	for _, want := range []string{"Weather for London, GB, from the box's daily pull 3 h ago.", "Now: overcast, 14.3°C (feels 12.1°C), humidity 77%, wind 18.4 km/h.",
		"Today: overcast, 9.4–16.2°C, rain 10%, sun 07:05–18:35.", "Tomorrow: light rain, 10.2–14.8°C, rain 80% (6.4 mm).", "Monday: partly cloudy, 8.1–15.1°C (0.2 mm).", "Tuesday: clear, 7.7–17°C, rain 0%.", "Local time zone Europe/London."} {
		if !strings.Contains(d, want) {
			t.Fatalf("%q missing from\n%s", want, d)
		}
	}
	if far := Describe(fs[0], 38.4, now); !strings.Contains(far, "London, GB (38 km away, the nearest place the box has)") {
		t.Fatal(far)
	}
}

func TestDue(t *testing.T) {
	now := time.Unix(2_000_000, 0)
	if !Due(State{}, now) || !Due(State{Places: 10, FetchedAt: now.Unix(), OldestAt: now.Unix() - int64(Every/time.Second)}, now) {
		t.Fatal("empty or an oldest row a day old: due")
	}
	if Due(State{Places: 10, FetchedAt: now.Unix(), OldestAt: now.Unix() - 3600}, now) {
		t.Fatal("an hour old: not due")
	}
}

// fakeGet answers every batch with one London per place asked, or fails.
type fakeGet struct {
	calls int
	urls  []string
	fail  bool
}

func (g *fakeGet) GetCapped(_ context.Context, id, url string, _ int64) (egress.Fetched, error) {
	g.calls++
	g.urls = append(g.urls, url)
	if g.fail {
		return egress.Fetched{ID: id, Status: 429, Error: "status 429"}, nil
	}
	n := strings.Count(url[strings.Index(url, "latitude="):strings.Index(url, "&longitude")], ",") + 1
	parts := make([]string, n)
	for i := range parts {
		parts[i] = london
	}
	return egress.Fetched{ID: id, Status: 200, Body: "[" + strings.Join(parts, ",") + "]", TookMs: 120}, nil
}

// Against Postgres: the places come from the geo set largest first, a pass saves one row a
// place in batches of Batch, Nearest finds the closest within NearKm and nothing beyond, ByName
// takes a pulled place or falls to the geo set, Load and Due read the table.
func TestPassNearestByNamePG(t *testing.T) {
	dir := os.Getenv("GHOST_PG_SOCKET_DIR")
	if dir == "" {
		t.Skip("GHOST_PG_SOCKET_DIR not set; no Postgres to test against")
	}
	port := 5432
	if p, err := strconv.Atoi(os.Getenv("GHOST_PG_PORT")); err == nil {
		port = p
	}
	user := os.Getenv("GHOST_PG_USER")
	if user == "" {
		user = "postgres"
	}
	admin := poltergres.NewReadWrite(dir, port, user, "", "postgres")
	_ = admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_weather")
	if err := admin.ExecSimple("CREATE DATABASE lgtest_weather"); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", "lgtest_weather")
	if _, err := hw.ConvergeSchema(db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	// 150 places over the threshold (two batches), one cell each; one under it; one that is not
	// a populated place; one in Place 0's cell, smaller, which the cell's winner stands for
	for i := 0; i < 150; i++ {
		lat := 40.0 + float64(i)*0.5 // a line up the map, 55 km apart, a cell each
		if err := db.Exec("INSERT INTO geo_points (geonameid, name, lat, lon, kind, country, population) VALUES ($1,$2,$3,$4,'P','XX',$5)",
			1000+i, "Place "+strconv.Itoa(i), lat, 10.0, 100_000+int64(150-i)*1000); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Exec("INSERT INTO geo_points (geonameid, name, lat, lon, kind, country, population) VALUES (1, 'Hamlet', 40.1, 10.0, 'P', 'XX', 400)")
	_ = db.Exec("INSERT INTO geo_points (geonameid, name, lat, lon, kind, country, population) VALUES (2, 'Mount Big', 40.2, 10.0, 'T', 'XX', 900000)")
	_ = db.Exec("INSERT INTO geo_points (geonameid, name, lat, lon, kind, country, population) VALUES (3, 'Suburb', 40.3, 10.2, 'P', 'XX', 60000)")
	places, err := Places(db)
	if err != nil || len(places) != 150 || places[0].Name != "Place 0" || places[149].Name != "Place 149" {
		t.Fatal(err, len(places))
	}
	for _, p := range places {
		if p.Name == "Suburb" {
			t.Fatal("a cell keeps its largest place alone")
		}
	}
	if st := Load(db); st.Places != 0 || !Due(st, time.Now()) {
		t.Fatalf("empty table: %+v", st)
	}
	now := time.Now().Add(-time.Hour).Truncate(time.Second) // recent: a row older than Stale is no answer
	// the next batch with nothing pulled: the hundred largest, due
	batch, due, err := NextBatch(db, now)
	if err != nil || !due || len(batch) != Batch || batch[0].Name != "Place 0" || batch[99].Name != "Place 99" {
		t.Fatalf("%v %v %d", err, due, len(batch))
	}
	g := &fakeGet{}
	r := Pass(context.Background(), db, g, places, now, func(time.Duration) {})
	if r.Batches != 2 || r.Failed != 0 || r.Places != 150 || g.calls != 2 || len(r.Fetches) != 2 || !r.Fetches[0].OK || r.Fetches[0].Items != 100 || r.Fetches[1].Items != 50 {
		t.Fatalf("%+v calls %d", r, g.calls)
	}
	if lats := g.urls[0][strings.Index(g.urls[0], "latitude="):strings.Index(g.urls[0], "&longitude")]; strings.Count(lats, ",") != 99 {
		t.Fatalf("the first batch asks for 100 places: %s", lats)
	}
	st := Load(db)
	if st.Places != 150 || st.FetchedAt != now.Unix() || st.OldestAt != now.Unix() || Due(st, now.Add(time.Hour)) || !Due(st, now.Add(Every)) {
		t.Fatalf("%+v", st)
	}
	// every row an hour old: the next batch is the hundred largest again, not due; a day on, due;
	// one row made old goes first
	if batch, due, err := NextBatch(db, now.Add(time.Hour)); err != nil || due || len(batch) != Batch || batch[0].Name != "Place 0" {
		t.Fatalf("%v %v %d", err, due, len(batch))
	}
	if _, due, _ := NextBatch(db, now.Add(Every)); !due {
		t.Fatal("a day on: due")
	}
	_ = db.Exec("UPDATE weather_places SET fetched_at = $1 WHERE name = 'Place 42'", now.Unix()-7200)
	if batch, due, _ := NextBatch(db, now.Add(time.Hour)); due || batch[0].Name != "Place 42" || batch[1].Name != "Place 0" {
		t.Fatalf("the oldest first: %s %s %v", batch[0].Name, batch[1].Name, due)
	}
	// 20 km from Place 3 (41.5, 10.0): that one, with the distance
	f, km, ok := Nearest(db, 41.5, 10.25)
	if !ok || f.Place.Name != "Place 3" || km < 19 || km > 22 {
		t.Fatalf("%v %v %+v", ok, km, f.Place)
	}
	// the open sea: nothing within NearKm
	if _, _, ok := Nearest(db, 40.0, 20.0); ok {
		t.Fatal("nothing is within 120 km of (40, 20)")
	}
	// by name: a pulled place, then the geo set (the hamlet, 11 km from Place 0), then nothing
	if f, km, ok := ByName(db, "place 7"); !ok || f.Place.Name != "Place 7" || km != 0 {
		t.Fatalf("%v %v %+v", ok, km, f.Place)
	}
	if f, km, ok := ByName(db, "Hamlet"); !ok || f.Place.Name != "Place 0" || km < 10 || km > 12 {
		t.Fatalf("%v %v %+v", ok, km, f.Place)
	}
	if _, _, ok := ByName(db, "Atlantis"); ok {
		t.Fatal("no such place")
	}
	if _, _, ok := ByName(db, "Mount Big"); ok {
		t.Fatal("a mountain is not a populated place")
	}
	// a failing service: every batch counted, nothing saved over the good rows
	bad := &fakeGet{fail: true}
	r = Pass(context.Background(), db, bad, places[:100], now.Add(Every), func(time.Duration) {})
	if r.Batches != 1 || r.Failed != 1 || r.Places != 0 || r.LastErr != "status 429" || len(r.Fetches) != 1 || r.Fetches[0].OK {
		t.Fatalf("%+v", r)
	}
	if st := Load(db); st.Places != 150 || st.FetchedAt != now.Unix() {
		t.Fatalf("the good rows stay: %+v", st)
	}
	// the stored forecast round-trips
	var stored Forecast
	rows, err := db.Query("SELECT forecast FROM weather_places WHERE geonameid = 1000")
	if err != nil || len(rows.Vals) != 1 || json.Unmarshal([]byte(*rows.Vals[0][0]), &stored) != nil || stored.Timezone != "Europe/London" || len(stored.Days) != 4 {
		t.Fatal(err, stored)
	}
}
