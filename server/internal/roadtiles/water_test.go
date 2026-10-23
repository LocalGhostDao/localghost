package roadtiles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/LocalGhostDao/localghost/server/internal/osmpbf"
)

func TestClipRingAndPointInRing(t *testing.T) {
	// a square from (0.05,0.05) to (0.15,0.15) clipped to the cell [0,0.1)²: its south-west quarter
	sq := []float64{0.05, 0.05, 0.15, 0.05, 0.15, 0.15, 0.05, 0.15}
	c := clipRing(sq, 0, 0, 0.1, 0.1)
	if len(c) != 8 || ringArea(c) < 0.0025-1e-12 || ringArea(c) > 0.0025+1e-12 {
		t.Fatalf("clipped %v area %g", c, ringArea(c))
	}
	if clipRing(sq, 0.2, 0.2, 0.3, 0.3) != nil {
		t.Fatal("a ring outside the cell clips to nothing")
	}
	if !pointInRing(sq, 0.1, 0.1) || pointInRing(sq, 0.2, 0.1) {
		t.Fatal("point in ring")
	}
	if WaterLine(map[string]string{"waterway": "canal"}) != ClassCanal || WaterLine(map[string]string{"waterway": "drain"}) != 0 ||
		!WaterArea(map[string]string{"landuse": "reservoir"}) || WaterArea(map[string]string{"natural": "wetland"}) ||
		!waterRelation(map[string]string{"type": "multipolygon", "natural": "water"}) || waterRelation(map[string]string{"natural": "water"}) {
		t.Fatal("tags")
	}
}

func TestStitchRings(t *testing.T) {
	// three ways around a triangle, the middle one reversed; a fourth that joins nothing
	a := []float64{0, 0, 1, 0}
	b := []float64{1, 1, 1, 0} // reversed: runs from (1,1) to (1,0)
	c := []float64{1, 1, 0, 0}
	d := []float64{5, 5, 6, 6}
	rings := stitchRings([][]float64{a, b, c, d})
	if len(rings) != 1 || len(rings[0]) != 6 || ringArea(rings[0]) != 0.5 {
		t.Fatalf("rings %v", rings)
	}
}

// A pond as a closed way, a river that crosses a fine cell border, a stream (fine tiles only), a
// lake as a multipolygon relation with an island, all beside a road, in one build.
func TestBuildCutsTheWater(t *testing.T) {
	nodes := []osmpbf.TestNode{
		// the road
		{ID: 1, Lat: 44.431, Lon: 26.101}, {ID: 2, Lat: 44.432, Lon: 26.103},
		// the pond, a closed way inside the fine cell (26.1..26.2, 44.4..44.5)
		{ID: 10, Lat: 44.440, Lon: 26.140}, {ID: 11, Lat: 44.440, Lon: 26.145}, {ID: 12, Lat: 44.444, Lon: 26.145}, {ID: 13, Lat: 44.444, Lon: 26.140},
		// the river, west to east across 26.2
		{ID: 20, Lat: 44.45, Lon: 26.15}, {ID: 21, Lat: 44.46, Lon: 26.25},
		// the stream
		{ID: 30, Lat: 44.41, Lon: 26.11}, {ID: 31, Lat: 44.42, Lon: 26.12},
		// the lake: a square 26.30..26.34 × 44.30..44.34 as two ways; the island inside it
		{ID: 40, Lat: 44.30, Lon: 26.30}, {ID: 41, Lat: 44.30, Lon: 26.34}, {ID: 42, Lat: 44.34, Lon: 26.34}, {ID: 43, Lat: 44.34, Lon: 26.30},
		{ID: 50, Lat: 44.31, Lon: 26.31}, {ID: 51, Lat: 44.31, Lon: 26.32}, {ID: 52, Lat: 44.32, Lon: 26.32}, {ID: 53, Lat: 44.32, Lon: 26.31},
	}
	ways := []osmpbf.TestWay{
		{ID: 100, Tags: map[string]string{"highway": "residential", "name": "Strada Mică"}, Refs: []int64{1, 2}},
		{ID: 200, Tags: map[string]string{"natural": "water", "water": "pond", "name": "Lacul Mic"}, Refs: []int64{10, 11, 12, 13, 10}},
		{ID: 201, Tags: map[string]string{"waterway": "river", "name": "Dâmbovița"}, Refs: []int64{20, 21}},
		{ID: 202, Tags: map[string]string{"waterway": "stream"}, Refs: []int64{30, 31}},
		{ID: 300, Refs: []int64{40, 41, 42}},         // half the lake's shore
		{ID: 301, Refs: []int64{42, 43, 40}},         // the other half
		{ID: 302, Refs: []int64{50, 51, 52, 53, 50}}, // the island
	}
	rels := []osmpbf.TestRelation{{ID: 900, Tags: map[string]string{"type": "multipolygon", "natural": "water", "name": "Lacul Mare"},
		Members: []osmpbf.Member{{Type: 1, Ref: 300, Role: "outer"}, {Type: 1, Ref: 301, Role: "outer"}, {Type: 1, Ref: 302, Role: "inner"}}}}
	pbf := osmpbf.Encode(osmpbf.EncBlock{Nodes: nodes}, osmpbf.EncBlock{Ways: ways}, osmpbf.EncBlock{Relations: rels})
	dir := t.TempDir()
	p := filepath.Join(dir, "x.osm.pbf")
	os.WriteFile(p, pbf, 0o644)
	out := filepath.Join(dir, "roadtiles")
	var log []string
	st, err := Build([]string{p}, out, Options{Workers: 2, NoGraph: true, Progress: func(s string) { log = append(log, s) }})
	if err != nil {
		t.Fatalf("%v\n%v", err, log)
	}
	if st.Ways != 1 || st.WaterWays != 3 || st.WaterRels != 1 {
		t.Fatalf("stats: %+v", st)
	}
	if Stale([]string{p}, out) {
		t.Fatal("fresh tiles with water are not stale")
	}
	os.Remove(filepath.Join(out, "water.ok"))
	if StaleWhy([]string{p}, out) == "" {
		t.Fatal("tiles without the water mark are stale")
	}
	read := func(c Cell) Tile {
		b, err := os.ReadFile(filepath.Join(out, TileName(c)))
		if err != nil {
			t.Fatalf("%v: %v", c, err)
		}
		tile, err := Decode(b)
		if err != nil {
			t.Fatal(err)
		}
		return tile
	}
	byClass := func(tl Tile, cls uint8) []Piece {
		var out []Piece
		for _, pc := range tl.Pieces {
			if pc.Class == cls {
				out = append(out, pc)
			}
		}
		return out
	}
	// the fine cell with the road, the pond, the stream and the river's west half
	f := read(Cell{0, 2061, 1344})
	if len(byClass(f, ClassResidential)) != 1 || len(byClass(f, ClassStream)) != 1 || len(byClass(f, ClassRiver)) != 1 {
		t.Fatalf("fine 2061: %+v", f.Pieces)
	}
	pond := byClass(f, ClassWaterArea)
	if len(pond) != 1 || pond[0].Name != "Lacul Mic" || len(pond[0].Points) != 8 {
		t.Fatalf("pond: %+v", pond)
	}
	if r := byClass(f, ClassRiver)[0]; r.Name != "Dâmbovița" || r.Points[len(r.Points)-2] != Q {
		t.Fatalf("the river leaves on the east border: %+v", r)
	}
	if len(byClass(read(Cell{0, 2062, 1344}), ClassRiver)) != 1 {
		t.Fatal("the river's east half")
	}
	// the major cell: the river, no stream, no pond (too small), the lake (big enough); its island
	// is too small for the major grid, so the lake is whole there
	m := read(Cell{1, 206, 134})
	if len(byClass(m, ClassRiver)) != 1 || len(byClass(m, ClassStream)) != 0 {
		t.Fatalf("major: %+v", m.Pieces)
	}
	if areas := byClass(m, ClassWaterArea); len(areas) != 1 || areas[0].Name != "Lacul Mare" || areas[0].Flags&FlagHole != 0 {
		t.Fatalf("the lake in the major tile: %+v", areas)
	}
	// the lake in the fine grid: its square sits in the cell 26.3..26.4 × 44.3..44.4, the island a
	// hole inside it
	l := read(Cell{0, 2063, 1343})
	areas := byClass(l, ClassWaterArea)
	if len(areas) != 2 {
		t.Fatalf("lake fine pieces: %+v", areas)
	}
	holes := 0
	for _, a := range areas {
		if a.Name != "Lacul Mare" {
			t.Fatalf("named after the relation: %+v", a)
		}
		if a.Flags&FlagHole != 0 {
			holes++
		}
	}
	if holes != 1 {
		t.Fatalf("one hole: %+v", areas)
	}
}
