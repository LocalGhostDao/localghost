package countrycells

import (
	"strings"
	"testing"
)

// A square country with a hole and a small island, on coordinates that do not sit on cell
// borders: the interior by centre, the border by the edge walk, the island by its vertices.
const fixture = `{"type":"FeatureCollection","features":[
{"type":"Feature","properties":{"NAME":"Squareland","ISO_A2":"-99","ISO_A2_EH":"SQ","ADM0_A3":"SQL"},
 "geometry":{"type":"MultiPolygon","coordinates":[
  [[[10.02,40.02],[12.02,40.02],[12.02,42.02],[10.02,42.02],[10.02,40.02]],
   [[10.52,40.52],[11.02,40.52],[11.02,41.02],[10.52,41.02],[10.52,40.52]]],
  [[[20.05,45.02],[20.07,45.02],[20.07,45.04],[20.05,45.04],[20.05,45.02]]]
 ]}},
{"type":"Feature","properties":{"NAME":"Antarctica","ISO_A2":"AQ"},
 "geometry":{"type":"Polygon","coordinates":[[[-180,-90],[180,-90],[180,-80.02],[-180,-80.02],[-180,-90]]]}},
{"type":"Feature","properties":{"NAME":"A point"},"geometry":{"type":"Point","coordinates":[1,1]}}
]}`

func TestRasteriseSquareWithHoleAndIsland(t *testing.T) {
	a, err := Read(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	// name order; the point has no code and is skipped
	if len(a.Countries) != 2 || a.Countries[0].Name != "Antarctica" || a.Countries[1].Code != "SQ" {
		t.Fatalf("countries: %+v", a.Countries)
	}
	sq := a.Get("sq")
	if sq == nil || sq.Name != "Squareland" {
		t.Fatalf("Get: %+v", sq)
	}
	// 21x21 cells (the far edges at .02 pull one more column and row in) less the hole's 4x4 interior
	// that no border touches, plus the island's one cell
	if len(sq.Fine) != 21*21-16+1 {
		t.Fatalf("fine cells: %d", len(sq.Fine))
	}
	has := func(keys []int32, k int32) bool {
		for _, v := range keys {
			if v == k {
				return true
			}
		}
		return false
	}
	// the hole's interior is out, its border cells are in
	if has(sq.Fine, 1307*3600+1907) {
		t.Fatal("a cell inside the hole counted")
	}
	if !has(sq.Fine, 1305*3600+1905) || !has(sq.Fine, 1300*3600+1900) || !has(sq.Fine, 1320*3600+1920) {
		t.Fatal("a border cell missing")
	}
	// the island is smaller than a cell: its vertices put it in
	if !has(sq.Fine, 1350*3600+2000) {
		t.Fatal("the island's cell missing")
	}
	// major: 3x3 for the square, one for the island
	if len(sq.Major) != 10 || !has(sq.Major, 130*360+190) || !has(sq.Major, 135*360+200) {
		t.Fatalf("major cells: %v", sq.Major)
	}
	// the dateline clamps instead of falling off the grid
	aq := a.Get("AQ")
	if len(aq.Fine) != 3600*100 || len(aq.Major) != 360*10 {
		t.Fatalf("antarctica: %d fine, %d major", len(aq.Fine), len(aq.Major))
	}
}

func TestIdentify(t *testing.T) {
	code, name := identify(map[string]any{"iso_a2": "-99", "ADM0_A3": "FRA", "ADMIN": "France"})
	if code != "FRA" || name != "France" {
		t.Fatalf("%s %s", code, name)
	}
	code, _ = identify(map[string]any{"ISO_A2": "gr", "NAME": "Greece"})
	if code != "GR" {
		t.Fatal(code)
	}
	if code, _ := identify(map[string]any{"NAME": "nothing"}); code != "" {
		t.Fatal("a feature with no code must be skipped")
	}
}

// A point's country: inside the square, in its hole (nobody's), on the island, at sea, and in
// Antarctica's band.
func TestAt(t *testing.T) {
	a, err := Read(strings.NewReader(fixture))
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		lat, lon float64
		code     string
	}{
		{41.5, 11.5, "SQ"},   // inside
		{40.75, 10.75, ""},   // the hole
		{45.03, 20.06, "SQ"}, // the island
		{41.5, 13.0, ""},     // at sea, inside the bounding box's latitude band
		{-85, 30, "AQ"},      // the band
		{0, 0, ""},
	} {
		code, _, ok := a.At(c.lat, c.lon)
		if code != c.code || ok != (c.code != "") {
			t.Errorf("At(%v, %v) = %q %v, want %q", c.lat, c.lon, code, ok, c.code)
		}
	}
	if _, _, ok := (*Atlas)(nil).At(41.5, 11.5); ok {
		t.Fatal("a nil atlas holds no country")
	}
}
