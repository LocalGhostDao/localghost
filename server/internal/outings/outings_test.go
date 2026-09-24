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
