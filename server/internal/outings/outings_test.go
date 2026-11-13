package outings

import (
	"strconv"
	"strings"
	"testing"
	"time"
)

const day = 86400

var t0 = time.Date(2026, 9, 1, 9, 0, 0, 0, time.UTC).Unix()

// London, the home cell, on sixteen separate mornings; then a five-day island trip with photos
// every few hours; then home again.
func fixture() []Frame {
	var fs []Frame
	tag := func(names ...string) []Tag {
		var out []Tag
		for _, n := range names {
			c := "object"
			switch n {
			case "beach", "harbour", "old town", "village":
				c = "place"
			case "sea", "sunset", "cliffs":
				c = "nature"
			case "boat", "ferry":
				c = "vehicle"
			case "taverna", "octopus":
				c = "food"
			case "dog":
				c = "animal"
			case "black and white":
				c = "style"
			}
			out = append(out, Tag{n, c})
		}
		return out
	}
	for d := 0; d < 16; d++ {
		for k := 0; k < 3; k++ {
			fs = append(fs, Frame{Hash: "L" + itoa(d*10+k), TakenAt: t0 + int64(d)*day + int64(k)*3600, Lat: 51.5074 + float64(k)*0.001, Lon: -0.1278, HasGPS: true,
				Place: "Europe / United Kingdom / England / London", Tags: tag("street", "coffee", "black and white")})
		}
	}
	trip := time.Date(2026, 9, 19, 11, 0, 0, 0, time.UTC).Unix()
	for d := 0; d < 5; d++ {
		for k := 0; k < 6; k++ {
			tags := tag("beach", "sea", "boat")
			if k%2 == 0 {
				tags = append(tags, tag("taverna", "sunset")...)
			}
			if d == 2 {
				tags = append(tags, tag("dog", "harbour")...)
			}
			place := "Europe / Greece / Ionian Islands / Antipaxos"
			if d == 2 {
				place = "Europe / Greece / Ionian Islands / Gaios"
			}
			fs = append(fs, Frame{Hash: "T" + itoa(d*10+k), TakenAt: trip + int64(d)*day + int64(k)*7200, Lat: 39.15 + float64(k)*0.002, Lon: 20.22, HasGPS: true, Place: place, Tags: tags})
		}
	}
	back := time.Date(2026, 9, 26, 9, 0, 0, 0, time.UTC).Unix()
	for k := 0; k < 4; k++ {
		fs = append(fs, Frame{Hash: "B" + itoa(k), TakenAt: back + int64(k)*3600, Lat: 51.5074, Lon: -0.1278, HasGPS: true,
			Place: "Europe / United Kingdom / England / London", Tags: tag("street", "coffee")})
	}
	// two photos with no time at all: ignored
	fs = append(fs, Frame{Hash: "X1"}, Frame{Hash: "X2", Lat: 1, Lon: 1, HasGPS: true})
	return fs
}

func itoa(i int) string { return strconv.Itoa(i) }

func TestBuildFindsHomeAndTheTrip(t *testing.T) {
	outs, home := Build(fixture())
	if !home.Known || home.Days != 17 || home.Lat < 51.5 || home.Lat > 51.51 {
		t.Fatalf("home = %+v", home)
	}
	var trip *Outing
	homeRuns := 0
	for i := range outs {
		if outs[i].Away {
			if trip != nil {
				t.Fatalf("two away outings: %+v and %+v", *trip, outs[i])
			}
			trip = &outs[i]
		} else {
			homeRuns++
		}
	}
	if trip == nil {
		t.Fatal("the island trip was not an outing")
	}
	if trip.Photos != 30 || trip.Days != 5 || trip.Place != "Antipaxos" || trip.Country != "Greece" {
		t.Fatalf("trip = photos %d days %d place %q country %q", trip.Photos, trip.Days, trip.Place, trip.Country)
	}
	if trip.Ref != "outing:2026-09-19" {
		t.Fatalf("ref = %s", trip.Ref)
	}
	if len(trip.Places) != 2 || trip.Places[0] != "Antipaxos" || trip.Places[1] != "Gaios" {
		t.Fatalf("places = %v", trip.Places)
	}
	if trip.FromHomeM < 2_000_000 || trip.FromHomeM > 2_600_000 {
		t.Fatalf("from home = %.0f m, London to the Ionian is ~2,300 km", trip.FromHomeM)
	}
	if trip.Tags[0].Tag != "beach" && trip.Tags[0].Tag != "boat" && trip.Tags[0].Tag != "sea" {
		t.Fatalf("top tag = %+v", trip.Tags[0])
	}
	if len(trip.Covers) != 6 || trip.Covers[0] != trip.Cover {
		t.Fatalf("covers = %v cover = %s", trip.Covers, trip.Cover)
	}
	// the cover carries the most tags: a day-2 even frame (beach sea boat taverna sunset dog harbour)
	if !strings.HasPrefix(trip.Cover, "T2") {
		t.Fatalf("cover = %s, want a day-2 frame", trip.Cover)
	}
	// home runs: sixteen consecutive mornings split by the ten-day span cap, plus the one after the trip
	if homeRuns < 2 || homeRuns > 4 {
		t.Fatalf("home runs = %d", homeRuns)
	}
	if trip.Title() != "Antipaxos · 19-23 Sep 2026" {
		t.Fatalf("title = %q", trip.Title())
	}
	trip.DistanceM = 27300
	body := trip.Body()
	for _, want := range []string{"30 photos over 5 days around Antipaxos, Greece.", "Mostly ", "beach", "Places: Antipaxos, Gaios.", "27 km on the move.", "from home."} {
		if !strings.Contains(body, want) {
			t.Fatalf("body %q lacks %q", body, want)
		}
	}
	if strings.Contains(body, "black and white") {
		t.Fatal("a style tag is not something you like")
	}
}

func TestBuildSplitsOnDistanceAndSilence(t *testing.T) {
	var fs []Frame
	// three photos in one place, then three 50 km away an hour later (a drive), then silence, then more
	for k := 0; k < 3; k++ {
		fs = append(fs, Frame{Hash: "a" + itoa(k), TakenAt: t0 + int64(k)*600, Lat: 39.15, Lon: 20.22, HasGPS: true})
	}
	for k := 0; k < 3; k++ {
		fs = append(fs, Frame{Hash: "b" + itoa(k), TakenAt: t0 + 3600 + int64(k)*600, Lat: 39.60, Lon: 20.22, HasGPS: true})
	}
	for k := 0; k < 3; k++ {
		fs = append(fs, Frame{Hash: "c" + itoa(k), TakenAt: t0 + 3*day + int64(k)*600, Lat: 39.60, Lon: 20.22, HasGPS: true})
	}
	outs, home := Build(fs)
	if home.Known {
		t.Fatal("three days is not a home")
	}
	if len(outs) != 3 {
		t.Fatalf("outings = %d, want 3 (place, distance, silence)", len(outs))
	}
	for _, o := range outs {
		if o.Away {
			t.Fatal("nothing is away when home is unknown")
		}
	}
	if outs[0].Title() != "39.15, 20.22 · 1 Sep 2026" {
		t.Fatalf("title without a place = %q", outs[0].Title())
	}
	// two photos alone are not a memory
	small, _ := Build(fs[:2])
	if len(small) != 0 {
		t.Fatalf("two photos made %d outings", len(small))
	}
}

func TestDateRange(t *testing.T) {
	d := func(s string) int64 { x, _ := time.Parse("2006-01-02", s); return x.Unix() + 3600 }
	cases := map[[2]string]string{
		{"2026-09-21", "2026-09-21"}: "21 Sep 2026",
		{"2026-09-19", "2026-09-23"}: "19-23 Sep 2026",
		{"2026-08-28", "2026-09-02"}: "28 Aug - 2 Sep 2026",
		{"2025-12-30", "2026-01-02"}: "30 Dec 2025 - 2 Jan 2026",
	}
	for in, want := range cases {
		if got := DateRange(d(in[0]), d(in[1])); got != want {
			t.Errorf("%v: %q, want %q", in, got, want)
		}
	}
}

// homeMornings is London on eight mornings, three photos each: enough days for a home.
func homeMornings(from time.Time) []Frame {
	var fs []Frame
	for d := 0; d < 8; d++ {
		for k := 0; k < 3; k++ {
			fs = append(fs, Frame{Hash: "H" + itoa(d*10+k), TakenAt: from.Unix() + int64(d)*day + int64(k)*600, Lat: 51.5074, Lon: -0.1278, HasGPS: true,
				Place: "Europe / United Kingdom / England / London"})
		}
	}
	return fs
}

func at(y int, m time.Month, d, h, min int) int64 {
	return time.Date(y, m, d, h, min, 0, 0, time.UTC).Unix()
}

// Two places on one day, more than splitKm apart, are two outings and were one memory: both had
// the ref of the day, and the later was written over the earlier at every pass.
func TestTwoOutingsOnOneDayHaveARefEach(t *testing.T) {
	var fs []Frame
	for k := 0; k < 3; k++ {
		fs = append(fs, Frame{Hash: "a" + itoa(k), TakenAt: t0 + int64(k)*600, Lat: 39.15, Lon: 20.22, HasGPS: true})
	}
	for k := 0; k < 3; k++ {
		fs = append(fs, Frame{Hash: "b" + itoa(k), TakenAt: t0 + 3600 + int64(k)*600, Lat: 39.60, Lon: 20.22, HasGPS: true})
	}
	for k := 0; k < 3; k++ {
		fs = append(fs, Frame{Hash: "c" + itoa(k), TakenAt: t0 + 7200 + int64(k)*600, Lat: 40.10, Lon: 20.22, HasGPS: true})
	}
	for k := 0; k < 3; k++ {
		fs = append(fs, Frame{Hash: "d" + itoa(k), TakenAt: t0 + 3*day + int64(k)*600, Lat: 40.10, Lon: 20.22, HasGPS: true})
	}
	outs, _ := Build(fs)
	if len(outs) != 4 {
		t.Fatalf("outings = %d, want 4", len(outs))
	}
	want := []string{"outing:2026-09-01", "outing:2026-09-01.2", "outing:2026-09-01.3", "outing:2026-09-04"}
	seen := map[string]bool{}
	for i, o := range outs {
		if o.Ref != want[i] {
			t.Fatalf("ref %d = %q, want %q", i, o.Ref, want[i])
		}
		if seen[o.Ref] {
			t.Fatalf("ref %q twice", o.Ref)
		}
		seen[o.Ref] = true
	}
	if outs[0].Hashes[0] != "a0" || outs[1].Hashes[0] != "b0" {
		t.Fatalf("the first of the day keeps the day's ref: %v %v", outs[0].Hashes, outs[1].Hashes)
	}
}

// A weekend away in the country the person lives in: two photos in York on the Friday evening,
// six in Whitby on the Saturday (66 km on), one in Bridlington on the Sunday morning (52 km on).
// The two short stops were dropped, and the weekend was one day in Whitby.
func TestAShortStopJoinsTheOutingBesideIt(t *testing.T) {
	fs := homeMornings(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	york := "Europe / United Kingdom / England / York"
	whitby := "Europe / United Kingdom / England / Whitby"
	brid := "Europe / United Kingdom / England / Bridlington"
	fs = append(fs,
		Frame{Hash: "y0", TakenAt: at(2026, 10, 16, 19, 30), Lat: 53.96, Lon: -1.08, HasGPS: true, Place: york},
		Frame{Hash: "y1", TakenAt: at(2026, 10, 16, 20, 10), Lat: 53.96, Lon: -1.08, HasGPS: true, Place: york})
	for k := 0; k < 6; k++ {
		fs = append(fs, Frame{Hash: "w" + itoa(k), TakenAt: at(2026, 10, 17, 10+k, 0), Lat: 54.486, Lon: -0.615, HasGPS: true, Place: whitby})
	}
	fs = append(fs, Frame{Hash: "b0", TakenAt: at(2026, 10, 18, 9, 40), Lat: 54.085, Lon: -0.19, HasGPS: true, Place: brid})
	// and things that must stay as they were: two photos at home on a day of their own, and one
	// photo far away with nothing within a day and a half of it
	fs = append(fs,
		Frame{Hash: "h0", TakenAt: at(2026, 10, 25, 9, 0), Lat: 51.5074, Lon: -0.1278, HasGPS: true},
		Frame{Hash: "h1", TakenAt: at(2026, 10, 25, 9, 5), Lat: 51.5074, Lon: -0.1278, HasGPS: true},
		Frame{Hash: "e0", TakenAt: at(2026, 11, 3, 12, 0), Lat: 55.95, Lon: -3.19, HasGPS: true})
	outs, home := Build(fs)
	if !home.Known {
		t.Fatal("no home")
	}
	var away []Outing
	for _, o := range outs {
		if o.Away {
			away = append(away, o)
		}
		for _, h := range o.Hashes {
			if h == "h0" || h == "e0" {
				t.Fatalf("%s is in an outing: %+v", h, o)
			}
		}
	}
	if len(away) != 1 {
		t.Fatalf("away outings = %d, want the weekend alone: %+v", len(away), away)
	}
	w := away[0]
	if w.Photos != 9 || w.Start != at(2026, 10, 16, 19, 30) || w.End != at(2026, 10, 18, 9, 40) || w.Days != 3 {
		t.Fatalf("the weekend = photos %d, %s to %s, days %d", w.Photos, time.Unix(w.Start, 0).UTC(), time.Unix(w.End, 0).UTC(), w.Days)
	}
	if w.Ref != "outing:2026-10-16" {
		t.Fatalf("ref = %q, the outing begins on the Friday", w.Ref)
	}
	if w.Place != "Whitby" || len(w.Places) != 3 || w.Places[0] != "Whitby" || w.Places[1] != "York" || w.Places[2] != "Bridlington" {
		t.Fatalf("place %q places %v", w.Place, w.Places)
	}
	// the centre is Whitby's: nine photos, and the three from the stops do not pull it south
	if w.Lat < 54.48 || w.Lat > 54.49 || w.Lon < -0.62 || w.Lon > -0.61 {
		t.Fatalf("centre = %.3f, %.3f, want Whitby's", w.Lat, w.Lon)
	}
}

// Short stops in a row, each too few to be a memory, make one between them; and a group that is
// an outing in its own right is never given to another.
func TestShortStopsInARowMakeAnOuting(t *testing.T) {
	fs := homeMornings(time.Date(2026, 10, 1, 9, 0, 0, 0, time.UTC))
	fs = append(fs,
		Frame{Hash: "p0", TakenAt: at(2026, 10, 20, 11, 0), Lat: 52.20, Lon: 0.12, HasGPS: true, Place: "Europe / United Kingdom / England / Cambridge"},
		Frame{Hash: "p1", TakenAt: at(2026, 10, 20, 11, 30), Lat: 52.20, Lon: 0.12, HasGPS: true, Place: "Europe / United Kingdom / England / Cambridge"},
		Frame{Hash: "q0", TakenAt: at(2026, 10, 20, 15, 0), Lat: 52.63, Lon: 1.30, HasGPS: true, Place: "Europe / United Kingdom / England / Norwich"},
		Frame{Hash: "q1", TakenAt: at(2026, 10, 21, 10, 0), Lat: 52.63, Lon: 1.30, HasGPS: true, Place: "Europe / United Kingdom / England / Norwich"})
	// then two outings in their own right the same week, 60 km apart: they stay two
	for k := 0; k < 4; k++ {
		fs = append(fs, Frame{Hash: "m" + itoa(k), TakenAt: at(2026, 10, 24, 10+k, 0), Lat: 50.82, Lon: -0.14, HasGPS: true})
	}
	for k := 0; k < 4; k++ {
		fs = append(fs, Frame{Hash: "n" + itoa(k), TakenAt: at(2026, 10, 25, 10+k, 0), Lat: 50.72, Lon: -1.88, HasGPS: true})
	}
	outs, _ := Build(fs)
	var away []Outing
	for _, o := range outs {
		if o.Away {
			away = append(away, o)
		}
	}
	if len(away) != 3 {
		t.Fatalf("away outings = %d, want 3: %+v", len(away), away)
	}
	e := away[0]
	if e.Photos != 4 || e.Days != 2 || len(e.Places) != 2 {
		t.Fatalf("the two stops together = photos %d days %d places %v", e.Photos, e.Days, e.Places)
	}
	// neither stop is the other's aside: the centre is between Cambridge and Norwich
	if e.Lon < 0.6 || e.Lon > 0.8 {
		t.Fatalf("centre lon = %.2f, want between the two", e.Lon)
	}
	if away[1].Photos != 4 || away[2].Photos != 4 {
		t.Fatalf("the two outings of four stayed two: %d and %d", away[1].Photos, away[2].Photos)
	}
}
