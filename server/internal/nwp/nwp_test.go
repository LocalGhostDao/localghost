package nwp

import (
	"math"
	"path/filepath"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/weather"
)

func TestRunFileRoundTripAndTheBuckets(t *testing.T) {
	cells := []Cell{{ID: 1, Lat: 48.4, Lon: 10.0, Orog: 500}, {ID: 2, Lat: 51.5, Lon: -0.1, Orog: float32(math.NaN())}}
	r := NewRun("gfs", time.Date(2026, 10, 10, 6, 0, 0, 0, time.UTC), []int{0, 3, 6, 9, 12}, cells)
	r.Set(0, 0, Temp2m, 12.34)
	r.Set(0, 1, Temp2m, -3.456)
	r.Set(1, 1, WindU, 3.21)
	r.Set(1, 2, Cloud, 87.6)
	// GFS's buckets: 0-3, 0-6, then 6-9, 6-12
	r.Set(0, 1, Precip, 1.0)
	r.Set(0, 2, Precip, 2.5)
	r.Set(0, 3, Precip, 0.4)
	r.Set(0, 4, Precip, 0.6)
	r.PrecipFrom = []int{0, 0, 0, 6, 6}
	if v, ok := r.Value(0, 0, Temp2m); !ok || v != 12.34 {
		t.Fatalf("%v %v", v, ok)
	}
	if v, ok := r.Value(0, 1, Temp2m); !ok || v != -3.46 {
		t.Fatalf("rounded to hundredths: %v %v", v, ok)
	}
	if _, ok := r.Value(1, 0, Temp2m); ok {
		t.Fatal("a value never set is present")
	}
	if v, ok := r.Value(1, 1, WindU); !ok || v != 3.2 {
		t.Fatalf("wind %v %v", v, ok)
	}
	if v, ok := r.Value(1, 2, Cloud); !ok || v != 88 {
		t.Fatalf("cloud %v %v", v, ok)
	}
	for s, want := range []float64{0, 1.0, 2.5, 2.9, 3.1} {
		c, ok := r.Cumulative(0, s)
		if !ok || math.Abs(c-want) > 1e-9 {
			t.Fatalf("cumulative at step %d: %v %v, want %v", s, c, ok, want)
		}
	}
	r.Set(0, 4, Temp2m, 1e6)
	if v, _ := r.Value(0, 4, Temp2m); v != float64(math.MaxInt16)/100 {
		t.Fatalf("clamped %v", v)
	}
	dir := t.TempDir()
	p := Path(dir, "gfs", r.At)
	if filepath.Base(p) != "gfs-2026101006.wx" {
		t.Fatal(p)
	}
	if err := Save(p, r); err != nil {
		t.Fatal(err)
	}
	got, err := Load(p)
	if err != nil {
		t.Fatal(err)
	}
	if got.Model != "gfs" || !got.At.Equal(r.At) || len(got.Cells) != 2 || len(got.Vals) != len(r.Vals) || got.Cells[0].Orog != 500 || !math.IsNaN(float64(got.Cells[1].Orog)) {
		t.Fatalf("%+v", got)
	}
	for i := range r.Vals {
		if r.Vals[i] != got.Vals[i] {
			t.Fatalf("value %d differs", i)
		}
	}
	// the newest of each model, and the pruning
	older := NewRun("gfs", r.At.Add(-6*time.Hour), r.Steps, cells)
	_ = Save(Path(dir, "gfs", older.At), older)
	_ = Save(Path(dir, "ifs", r.At), NewRun("ifs", r.At, r.Steps, cells))
	runs, err := Available(dir, Models, 2)
	if err != nil || len(runs) != 3 || runs[0].Model != "ifs" || runs[1].Model != "gfs" || !runs[1].At.Equal(r.At) || !runs[2].At.Equal(older.At) {
		t.Fatalf("%v %v", runs, err)
	}
	if runs, _ := Available(dir, Models, 1); len(runs) != 2 {
		t.Fatalf("%d", len(runs))
	}
	if removed := Prune(dir, 1); len(removed) != 1 || removed[0] != "gfs-2026101000.wx" {
		t.Fatalf("pruned %v", removed)
	}
}

func TestCandidatesAreTheRunsPublishedByNow(t *testing.T) {
	ifs, _ := ModelByID("ifs")
	now := time.Date(2026, 10, 10, 14, 30, 0, 0, time.UTC) // 06Z is seven hours old at 13:00
	c := Candidates(ifs, now)
	if len(c) != 4 || c[0].Hour() != 6 || c[0].Day() != 10 || c[1].Hour() != 0 || c[2].Hour() != 18 || c[2].Day() != 9 || c[3].Hour() != 12 {
		t.Fatalf("%v", c)
	}
	now = time.Date(2026, 10, 10, 12, 59, 0, 0, time.UTC)
	if c := Candidates(ifs, now); c[0].Hour() != 0 || c[0].Day() != 10 {
		t.Fatalf("06Z is not seven hours old yet: %v", c)
	}
	icon, _ := ModelByID("icon-eu")
	if c := Candidates(icon, time.Date(2026, 10, 10, 1, 0, 0, 0, time.UTC)); c[0].Hour() != 18 || c[0].Day() != 9 || c[1].Hour() != 12 {
		t.Fatalf("%v", c)
	}
	// a metered box: the 00 and 12 runs only
	RunsADay = 2
	defer func() { RunsADay = 0 }()
	if c := Candidates(ifs, time.Date(2026, 10, 10, 14, 30, 0, 0, time.UTC)); len(c) != 4 || c[0].Hour() != 0 || c[0].Day() != 10 || c[1].Hour() != 12 || c[1].Day() != 9 {
		t.Fatalf("%v", c)
	}
}

func TestReadAtInterpolatesAndCorrectsTheHeight(t *testing.T) {
	cells := []Cell{{ID: 1, Lat: 48.4, Lon: 10.0, Orog: 700}}
	r := NewRun("ifs", time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), []int{0, 3, 6}, cells)
	r.Set(0, 0, Temp2m, 10)
	r.Set(0, 1, Temp2m, 13)
	r.Set(0, 2, Temp2m, 16)
	r.Set(0, 0, Dew2m, 8)
	r.Set(0, 1, Dew2m, 8)
	r.Set(0, 2, Dew2m, 8)
	r.Set(0, 1, Precip, 3.0) // 0-3 h
	r.Set(0, 2, Precip, 3.0) // 0-6 h: nothing more
	r.Set(0, 0, WindU, 3)
	r.Set(0, 0, WindV, 4)
	r.Set(0, 1, WindU, 3)
	r.Set(0, 1, WindV, 4)
	r.Set(0, 2, WindU, 3)
	r.Set(0, 2, WindV, 4)
	r.Set(0, 1, Cloud, 50)
	r.Set(0, 2, Cloud, 100)
	// at lead 1 h: a third of the way from 10 to 13, the rain 1 mm an hour over 0-3
	mh, ok := readAt(r, cellReading{r, 0}, 1, 1, 0, false)
	if !ok || math.Abs(mh.temp-11) > 1e-9 || !mh.hasPrecip || math.Abs(mh.precip-1.0) > 1e-9 || !mh.hasWind || mh.hasCloud {
		t.Fatalf("%+v %v", mh, ok)
	}
	// at lead 4 h: between 3 and 6, no rain, the cloud two thirds of the way
	mh, _ = readAt(r, cellReading{r, 0}, 4, 1, 0, false)
	if math.Abs(mh.temp-14) > 1e-9 || mh.precip != 0 || math.Abs(mh.cloud-200.0/3) > 1e-9 || math.Abs(mh.windU-3) > 1e-9 {
		t.Fatalf("%+v", mh)
	}
	// the town is at 500 m, the model's ground at 700 m: 200 m lower, 1.3° warmer
	mh, _ = readAt(r, cellReading{r, 0}, 3, 1, 500, true)
	if math.Abs(mh.temp-14.3) > 1e-9 {
		t.Fatalf("height: %+v", mh)
	}
	if _, ok := readAt(r, cellReading{r, 0}, 7, 1, 0, false); ok {
		t.Fatal("past the last step")
	}
	if mh, ok := readAt(r, cellReading{r, 0}, 6, 1, 0, false); !ok || mh.temp != 16 {
		t.Fatalf("the last step itself: %+v %v", mh, ok)
	}
}

func TestBlendLeavesTheLoneModelOut(t *testing.T) {
	at := time.Now()
	h := blend(at, []modelHour{
		{weight: 1, temp: 10, hasPrecip: true, precip: 0.5, hasWind: true, windU: 3, windV: 4, hasCloud: true, cloud: 50},
		{weight: 1, temp: 11, hasPrecip: true, precip: 0, hasWind: true, windU: 3, windV: 4, hasCloud: true, cloud: 100},
		{weight: 2, temp: 20, hasPrecip: true, precip: 0},
	})
	if !h.ok || h.models != 2 || math.Abs(h.temp-10.5) > 1e-9 || h.spread != 1 || h.rainPct != 50 || math.Abs(h.precip-0.25) > 1e-9 || math.Abs(h.windKmh-18) > 1e-9 || h.cloud != 75 {
		t.Fatalf("%+v", h)
	}
	// two models cannot tell who is alone: both in, weighted
	h = blend(at, []modelHour{{weight: 1, temp: 10}, {weight: 3, temp: 20}})
	if h.models != 2 || math.Abs(h.temp-17.5) > 1e-9 || h.spread != 10 || h.rainPct != -1 {
		t.Fatalf("%+v", h)
	}
	if h := blend(at, nil); h.ok {
		t.Fatal("nothing from nothing")
	}
}

func TestTheWordsAndTheAir(t *testing.T) {
	if Humidity(20, 20) != 100 || Humidity(20, 10) < 50 || Humidity(20, 10) > 54 {
		t.Fatalf("humidity %d", Humidity(20, 10))
	}
	// a windy 10° feels colder, a humid 30° warmer
	if FeelsLike(10, 60, 40) > 6 || FeelsLike(30, 80, 0) < 32 {
		t.Fatalf("%v %v", FeelsLike(10, 60, 40), FeelsLike(30, 80, 0))
	}
	for _, c := range []struct {
		t, d, p, cloud, wind float64
		want                 int
	}{
		{20, 5, 0, 5, 10, 0}, {20, 5, 0, 30, 10, 1}, {20, 5, 0, 60, 10, 2}, {20, 5, 0, 95, 10, 3},
		{20, 5, 0.3, 95, 10, 51}, {20, 5, 1.5, 95, 10, 61}, {20, 5, 5, 95, 10, 63}, {20, 5, 9, 95, 10, 65},
		{-2, -3, 0.5, 95, 10, 71}, {-2, -3, 2, 95, 10, 73}, {-2, -3, 4, 95, 10, 75}, {1, 0, 2, 95, 10, 66},
		{8, 8, 0, 90, 3, 45}, {8, 8, 0, 90, 20, 3},
	} {
		if got := Code(c.t, c.d, c.p, c.cloud, c.wind); got != c.want {
			t.Fatalf("%+v: %d", c, got)
		}
	}
}

func TestSunTimes(t *testing.T) {
	// London, the longest day of 2026: the sun up a little before 04:43 BST, down a little after 21:21
	rise, set, ok := SunTimes(time.Date(2026, 6, 21, 0, 0, 0, 0, time.UTC), 51.5074, -0.1278)
	if !ok {
		t.Fatal("no sun in London")
	}
	if d := rise.Sub(time.Date(2026, 6, 21, 3, 43, 0, 0, time.UTC)); d < -3*time.Minute || d > 3*time.Minute {
		t.Fatalf("sunrise %v", rise)
	}
	if d := set.Sub(time.Date(2026, 6, 21, 20, 21, 0, 0, time.UTC)); d < -3*time.Minute || d > 3*time.Minute {
		t.Fatalf("sunset %v", set)
	}
	// Neu-Ulm on 10 October 2026: about 07:30 and 18:36 local (CEST), 05:30 and 16:36 UTC
	rise, set, _ = SunTimes(time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), 48.39, 10.01)
	if d := rise.Sub(time.Date(2026, 10, 10, 5, 30, 0, 0, time.UTC)); d < -6*time.Minute || d > 6*time.Minute {
		t.Fatalf("Neu-Ulm sunrise %v", rise)
	}
	if d := set.Sub(time.Date(2026, 10, 10, 16, 38, 0, 0, time.UTC)); d < -6*time.Minute || d > 6*time.Minute {
		t.Fatalf("Neu-Ulm sunset %v", set)
	}
	// Longyearbyen in December: no sun
	if _, _, ok := SunTimes(time.Date(2026, 12, 21, 0, 0, 0, 0, time.UTC), 78.2, 15.6); ok {
		t.Fatal("a polar night with a sunrise")
	}
}

type fixedZone string

func (z fixedZone) Zone(lat, lon float64) string { return string(z) }

type flatHeights float64

func (h flatHeights) Height(lat, lon float64) (float64, bool) { return float64(h), true }

func TestComputeMakesTheForecastThePhoneReads(t *testing.T) {
	now := time.Date(2026, 10, 10, 10, 30, 0, 0, time.UTC) // 12:30 in Berlin
	places := []weather.Place{{ID: 1, Name: "Neu-Ulm", Country: "DE", Lat: 48.39, Lon: 10.01, Population: 60000},
		{ID: 2, Name: "Nowhere", Country: "XX", Lat: -40, Lon: 170}}
	cells := []Cell{{ID: 1, Lat: 48.39, Lon: 10.01, Orog: 480}, {ID: 2, Lat: -40, Lon: 170, Orog: 10}}
	steps := threeHourly
	mk := func(model string, at time.Time, base float64) *Run {
		r := NewRun(model, at, steps, cells)
		for c := range cells {
			for s, h := range steps {
				hour := at.Add(time.Duration(h) * time.Hour).UTC().Hour()
				r.Set(c, s, Temp2m, base+6*math.Sin(float64(hour-6)/24*2*math.Pi))
				r.Set(c, s, Dew2m, base-6)
				r.Set(c, s, Cloud, 40)
				r.Set(c, s, WindU, 2)
				r.Set(c, s, WindV, 0)
				if s > 0 {
					r.Set(c, s, Precip, float64(h)*0.1) // 0.1 mm every hour, accumulated
				}
			}
		}
		return r
	}
	icon := mk("icon-eu", time.Date(2026, 10, 10, 6, 0, 0, 0, time.UTC), 12)
	iconBefore := mk("icon-eu", time.Date(2026, 10, 10, 3, 0, 0, 0, time.UTC), 12)
	ifs := mk("ifs", time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), 13)
	ifsBefore := mk("ifs", time.Date(2026, 10, 9, 18, 0, 0, 0, time.UTC), 13)
	gfs := mk("gfs", time.Date(2026, 10, 10, 0, 0, 0, 0, time.UTC), 14)
	gfsBefore := mk("gfs", time.Date(2026, 10, 9, 18, 0, 0, 0, time.UTC), 14)
	fs := Compute([]*Run{icon, iconBefore, ifs, ifsBefore, gfs, gfsBefore}, places, now, fixedZone("Europe/Berlin"), flatHeights(480))
	if len(fs) != 2 {
		t.Fatalf("%d forecasts", len(fs))
	}
	f := fs[0]
	if f.Place.Name != "Neu-Ulm" || f.Timezone != "Europe/Berlin" || f.UTCOffset != 7200 || f.FetchedAt != now.Unix() {
		t.Fatalf("%+v", f)
	}
	if f.Source != "icon-eu 2026-10-10 06Z · ifs 2026-10-10 00Z · gfs 2026-10-10 00Z" {
		t.Fatalf("source %q", f.Source)
	}
	if len(f.Hours) != weather.HourDays*24 || f.Hours[0].At != "2026-10-10T00:00" || f.Hours[71].At != "2026-10-12T23:00" {
		t.Fatalf("hours %d %s", len(f.Hours), f.Hours[0].At)
	}
	// at midnight local (22:00 UTC the day before) the runs before serve: IFS's and GFS's
	// 18Z, a degree apart; ICON's 03Z does not reach back
	h0 := f.Hours[0]
	if h0.SpreadC != 1 || h0.TempC < 5 || h0.TempC > 15 || h0.RainPct != 100 || h0.PrecipMM != 0.1 || h0.WindKmh != 7.2 || h0.Code != 51 {
		t.Fatalf("hour 0 %+v", h0)
	}
	// at 08:00 local (06:00 UTC) all three: ICON's 06Z from its first step
	if h := f.Hours[8]; h.SpreadC != 2 {
		t.Fatalf("hour 8 %+v", h)
	}
	if len(f.Days) != weather.Days || f.Days[0].Date != "2026-10-10" || f.Days[0].MaxC <= f.Days[0].MinC || f.Days[0].RainPct != 100 || f.Days[0].PrecipMM != 2.4 || f.Days[0].Sunrise[:3] != "07:" || f.Days[0].Sunset[:3] != "18:" {
		t.Fatalf("day 0 %+v", f.Days[0])
	}
	if f.Days[0].Code != 51 {
		t.Fatalf("a day of drizzle: %+v", f.Days[0])
	}
	if f.Now.At != "2026-10-10T12:30" || f.Now.Humidity < 40 || f.Now.Humidity > 60 || f.Now.Code != 51 || f.Now.FeelsC == 0 {
		t.Fatalf("now %+v", f.Now)
	}
	// the place outside Europe: IFS and GFS only
	g := fs[1]
	if g.Timezone != "Europe/Berlin" || len(g.Hours) != 72 || g.Hours[12].SpreadC != 1 {
		t.Fatalf("nowhere %+v", g.Hours[12])
	}
	// the last day: the models' runs reach 120 h, enough for four days from midnight
	if len(g.Days) != 4 {
		t.Fatalf("%d days", len(g.Days))
	}
	// without any run covering a place, no forecast
	if out := Compute([]*Run{icon}, places[1:], now, nil, nil); len(out) != 0 {
		t.Fatalf("outside ICON's domain: %d", len(out))
	}
	// with no zone set, the sun's hour from the longitude
	out := Compute([]*Run{ifs}, places[1:], now, nil, nil)
	if len(out) != 1 || out[0].Timezone != "UTC+11" || out[0].UTCOffset != 11*3600 {
		t.Fatalf("%+v", out[0].Timezone)
	}
	d := weather.Describe(f, 0, now)
	if !contains(d, "the box's own forecast from icon-eu 2026-10-10 06Z") {
		t.Fatalf("%s", d)
	}
}

func contains(s, sub string) bool { return len(s) >= len(sub) && (s == sub || indexOf(s, sub) >= 0) }

func indexOf(s, sub string) int {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return i
		}
	}
	return -1
}

func TestWindowSamplesItsOwnGrid(t *testing.T) {
	// a 3×3 window at 0.5°, one step, the temperature a plane rising east and south
	w := &Window{Ni: 3, Nj: 3, Lat1: 48, Lon1: 10, Di: 0.5, Dj: 0.5, Vals: make([]int16, int(NumFields)*9)}
	for i := range w.Vals {
		w.Vals[i] = missing
	}
	for j := 0; j < 3; j++ {
		for i := 0; i < 3; i++ {
			w.Vals[w.at(0, Temp2m, j, i)] = int16(100 * (10 + i + 2*j))
		}
	}
	if v, ok := w.sample(0, Temp2m, 48, 10); !ok || v != 10 {
		t.Fatalf("%v %v", v, ok)
	}
	if v, ok := w.sample(0, Temp2m, 47.75, 10.25); !ok || math.Abs(v-11.5) > 1e-9 {
		t.Fatalf("the middle of the first cell: %v %v", v, ok)
	}
	if v, ok := w.sample(0, Temp2m, 47, 11); !ok || v != 16 {
		t.Fatalf("the far corner: %v %v", v, ok)
	}
	if _, ok := w.sample(0, Temp2m, 46.9, 10); ok {
		t.Fatal("south of the window")
	}
	if _, ok := w.sample(0, Dew2m, 48, 10); ok {
		t.Fatal("a field never filled")
	}
	if !w.Covers(47.5, 10.5) || w.Covers(48.1, 10) {
		t.Fatal("covers")
	}
	if !math.IsNaN(w.orogAt(47.5, 10.5)) {
		t.Fatal("no ground yet")
	}
	w.Orog = []float32{0, 1, 2, 3, 4, 5, 6, 7, 8}
	if w.orogAt(47.5, 10.5) != 4 || w.orogAt(47, 11) != 8 {
		t.Fatalf("%v %v", w.orogAt(47.5, 10.5), w.orogAt(47, 11))
	}
	// the window survives the run file
	cells := []Cell{{ID: 1, Lat: 47.5, Lon: 10.5}}
	r := NewRun("icon-d2", time.Date(2026, 10, 10, 6, 0, 0, 0, time.UTC), []int{3}, cells)
	r.Win = w
	path := Path(t.TempDir(), "icon-d2", r.At)
	if err := Save(path, r); err != nil {
		t.Fatal(err)
	}
	got, err := Load(path)
	if err != nil || got.Win == nil || got.Win.Ni != 3 || got.Win.Orog[4] != 4 {
		t.Fatalf("%v %+v", err, got.Win)
	}
	if v, _ := got.Win.sample(0, Temp2m, 47, 11); v != 16 {
		t.Fatal("the window's values after the file")
	}
	// a window of the wrong size is dropped, the run kept
	got.Win.Vals = got.Win.Vals[:5]
	_ = Save(path, got)
	if again, err := Load(path); err != nil || again.Win != nil {
		t.Fatalf("%v %v", err, again.Win)
	}
}
