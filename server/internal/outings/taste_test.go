package outings

import (
	"strings"
	"testing"
)

func tasteFixture() Taste {
	rows := []TasteRow{
		{"sea", "nature", 40, 900}, {"beach", "place", 38, 700}, {"boat", "vehicle", 30, 400},
		{"street", "place", 25, 300}, {"coffee", "food", 22, 100}, {"dog", "animal", 9, 40},
		{"castle", "place", 6, 30}, {"black and white", "style", 50, 2000}, {"text", "text", 45, 500},
		{"once", "object", 1, 1}, {"", "object", 20, 20},
		{"p1", "place", 8, 8}, {"p2", "place", 8, 8}, {"p3", "place", 8, 8}, {"p4", "place", 5, 5}, {"p5", "place", 5, 5},
	}
	return BuildTaste(rows, 100, 5000, 1700000000)
}

func TestTasteRanksByDaysAndFoldsIntoInterests(t *testing.T) {
	ts := tasteFixture()
	if len(ts.Likes) == 0 || ts.Likes[0].Tag != "sea" || ts.Likes[1].Tag != "beach" || ts.Likes[2].Tag != "boat" {
		t.Fatalf("likes = %+v", ts.Likes)
	}
	if ts.Likes[0].Share != 0.4 {
		t.Fatalf("share = %v", ts.Likes[0].Share)
	}
	for _, l := range ts.Likes {
		if l.Tag == "black and white" || l.Tag == "text" || l.Tag == "once" || l.Tag == "" {
			t.Fatalf("%q should not be a like", l.Tag)
		}
	}
	places := 0
	for _, l := range ts.Likes {
		if l.Category == "place" {
			places++
		}
	}
	if places != maxPerCategory {
		t.Fatalf("place likes = %d, want the cap %d", places, maxPerCategory)
	}
	for _, l := range ts.Likes {
		if l.Tag == "p4" || l.Tag == "p5" {
			t.Fatal("the seventh and eighth place tags fall to the cap")
		}
	}
	if len(ts.Interests) == 0 || ts.Interests[0].Name != "beaches" {
		t.Fatalf("interests = %+v", ts.Interests)
	}
	// beaches = sea (0.40) + beach (0.38); harbours = boat (0.30); old towns = street (0.25)
	if ts.Interests[0].Weight < 0.77 || ts.Interests[0].Weight > 0.79 || ts.Interests[1].Name != "harbours" {
		t.Fatalf("interest weights = %+v", ts.Interests[:2])
	}
	if !strings.HasPrefix(ts.Summary, "You photograph sea, beach and boat most , sea on 40% of your days") || !strings.Contains(ts.Summary, "Places to your taste: beaches, harbours") {
		t.Fatalf("summary = %q", ts.Summary)
	}
	empty := BuildTaste(nil, 0, 0, 0)
	if empty.Summary != "no photos with a date yet" {
		t.Fatalf("empty = %q", empty.Summary)
	}
}

func TestRankPrefersTheTasteNearbyAndNew(t *testing.T) {
	ts := tasteFixture()
	here := [2]float64{39.15, 20.22}
	spots := []Spot{
		{Name: "Voutoumi", FCode: "BCH", Lat: 39.158, Lon: 20.238},  // beach, 2 km
		{Name: "Far beach", FCode: "BCH", Lat: 39.25, Lon: 20.22},   // beach, 11 km
		{Name: "Gaios", FCode: "HBR", Lat: 39.20, Lon: 20.19},       // harbour, 6 km
		{Name: "A castle", FCode: "CSTL", Lat: 39.16, Lon: 20.23},   // castle, 1 km, but castles are a weak like (6 days)
		{Name: "Somewhere", FCode: "PPL", Lat: 39.17, Lon: 20.22},   // village, old towns via "street"
		{Name: "Too far", FCode: "BCH", Lat: 39.50, Lon: 20.22},     // 39 km, outside
		{Name: "An airport", FCode: "AIRP", Lat: 39.16, Lon: 20.22}, // no interest lists it
	}
	been := func(lat, lon float64) int {
		if lat == 39.158 { // Voutoumi: photographed before
			return 12
		}
		return 0
	}
	got := Rank(spots, ts, here[0], here[1], 15, been)
	names := func() []string {
		var n []string
		for _, g := range got {
			n = append(n, g.Name)
		}
		return n
	}
	if len(got) != 5 {
		t.Fatalf("got %v", names())
	}
	// Voutoumi is the best beach and nearest, but photographed before: halved , the far beach and
	// the harbour still cannot beat 0.78*sqrt(1-2/15)*0.5 ≈ 0.37? harbour: 0.30*sqrt(1-6/15) ≈ 0.23; far beach: 0.78*sqrt(1-11/15) ≈ 0.40.
	if got[0].Name != "Far beach" || got[1].Name != "Voutoumi" {
		t.Fatalf("order = %v (scores %.2f %.2f)", names(), got[0].Score, got[1].Score)
	}
	for _, g := range got {
		switch g.Name {
		case "Voutoumi":
			if g.BeenThere != 12 || !strings.Contains(g.Why, "12 photos within a kilometre") || g.Kind != "beach" || g.Bearing != "NE" {
				t.Fatalf("Voutoumi = %+v", g)
			}
		case "Far beach":
			if !strings.Contains(g.Why, "new to you") || !strings.HasPrefix(g.Why, "you photograph sea and beach (78% of your days)") || g.Bearing != "N" {
				t.Fatalf("far beach = %+v", g)
			}
		case "Somewhere":
			if g.Interest != "old towns and villages" || g.Kind != "village" {
				t.Fatalf("village = %+v", g)
			}
		case "Too far", "An airport":
			t.Fatalf("%s should not be suggested", g.Name)
		}
	}
	// at most four per interest
	var many []Spot
	for i := 0; i < 10; i++ {
		many = append(many, Spot{Name: "b" + string(rune('0'+i)), FCode: "BCH", Lat: 39.15 + float64(i)*0.01, Lon: 20.22})
	}
	if n := len(Rank(many, ts, here[0], here[1], 15, nil)); n != maxPerInterest {
		t.Fatalf("beaches suggested = %d, want %d", n, maxPerInterest)
	}
	// no taste, no suggestions
	if n := len(Rank(spots, Taste{}, here[0], here[1], 15, nil)); n != 0 {
		t.Fatalf("suggestions without a taste = %d", n)
	}
}

func TestSpotCodesLeaveOutWhatGeoImportAlreadyKeeps(t *testing.T) {
	codes := SpotCodes()
	has := func(c string) bool {
		for _, x := range codes {
			if x == c {
				return true
			}
		}
		return false
	}
	for _, c := range []string{"BCH", "HBR", "CSTL", "MSTY", "MUS", "CAVE"} {
		if !has(c) {
			t.Fatalf("%s missing from the spot codes", c)
		}
	}
	for _, c := range []string{"PRK", "LK", "PK", "TRL", "PPL"} {
		if has(c) {
			t.Fatalf("%s is already a K/F/P kind, not a spot", c)
		}
	}
	if KindName("BCH") != "beach" || KindName("XYZ") != "xyz" {
		t.Fatal("kind names")
	}
}
