package roadtiles

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/LocalGhostDao/localghost/server/internal/osmpbf"
)

func TestTileAndIndexRoundTrip(t *testing.T) {
	c := Cell{0, 2061, 1344} // lon 26.1, lat 44.4
	tile := Tile{Cell: c, Pieces: []Piece{
		{Class: ClassPrimary, Flags: FlagOneway, Name: "Bulevardul Unirii", Points: []uint16{0, 100, 3000, 200, Q, 300}},
		{Class: ClassPath, Points: []uint16{5, 5, 9, 9}},
	}}
	back, err := Decode(tile.Encode())
	if err != nil || back.Cell != c || len(back.Pieces) != 2 || back.Pieces[0].Name != "Bulevardul Unirii" ||
		back.Pieces[0].Flags&FlagOneway == 0 || back.Pieces[0].Points[4] != Q || back.Pieces[1].Class != ClassPath || back.Points() != 5 {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	if _, err := Decode([]byte("nope")); err == nil {
		t.Fatal("garbage decoded")
	}
	ix := NewIndex()
	ix.Set(c)
	ix.Set(Cell{1, 206, 134})
	ix2, err := DecodeIndex(ix.Encode())
	if err != nil || !ix2.Has(c) || !ix2.Has(Cell{1, 206, 134}) || ix2.Has(Cell{0, 2062, 1344}) || ix2.Has(Cell{1, 207, 134}) {
		t.Fatalf("index: %v", err)
	}
	if m, f := ix2.Count(); m != 1 || f != 1 {
		t.Fatalf("count %d %d", m, f)
	}
	if CellAt(0, 26.1025, 44.4268) != c || CellAt(1, 26.1025, 44.4268) != (Cell{1, 206, 134}) {
		t.Fatalf("CellAt: %v %v", CellAt(0, 26.1025, 44.4268), CellAt(1, 26.1025, 44.4268))
	}
	if TileName(c) != "0/2061_1344.lgr" || TileName(Cell{1, 206, 134}) != "1/206_134.lgr" {
		t.Fatal("tile names")
	}
	if Class("motorway_link") != ClassMotorway || Class("living_street") != ClassResidential || Class("steps") != ClassPath || Class("construction") != 0 || Class("") != 0 {
		t.Fatal("classes")
	}
}

func TestCutSplitsOnCellBordersOnly(t *testing.T) {
	dir := t.TempDir()
	cb := newCellBuffers(dir, 1<<30)
	// a road from lon 26.05 to 26.35 at lat 44.45: crosses the 0.1° borders at 26.1, 26.2, 26.3
	coords := []float64{26.05, 44.45, 26.12, 44.46, 26.35, 44.45}
	if err := cut(0, coords, ClassPrimary, 0, "Road", cb); err != nil {
		t.Fatal(err)
	}
	if err := cb.flushAll(); err != nil {
		t.Fatal(err)
	}
	got := map[Cell]Tile{}
	for _, x := range []int{2060, 2061, 2062, 2063} {
		c := Cell{0, x, 1344}
		body, err := os.ReadFile(rawPath(dir, c))
		if err != nil {
			t.Fatalf("cell %v has no piece", c)
		}
		count, _ := countPieces(body)
		hdr := Tile{Cell: c}.Encode()[:13]
		hdr[9] = byte(count)
		tl, err := Decode(append(hdr, body...))
		if err != nil || len(tl.Pieces) != 1 {
			t.Fatalf("cell %v: %v %+v", c, err, tl)
		}
		got[c] = tl
	}
	// every piece starts or ends on a border (x == 0 or x == Q) except the road's own ends
	first := got[Cell{0, 2060, 1344}].Pieces[0].Points
	if first[len(first)-2] != Q {
		t.Fatalf("first piece does not end on the east border: %v", first)
	}
	mid := got[Cell{0, 2061, 1344}].Pieces[0].Points
	if mid[0] != 0 || mid[len(mid)-2] != Q || len(mid) != 6 {
		t.Fatalf("middle piece: %v", mid) // enters west, has the 26.12 vertex, leaves east
	}
	last := got[Cell{0, 2063, 1344}].Pieces[0].Points
	if last[0] != 0 || last[len(last)-2] == Q {
		t.Fatalf("last piece: %v", last)
	}
	if got[Cell{0, 2061, 1344}].Pieces[0].Name != "Road" {
		t.Fatal("name lost")
	}
	// a road inside one cell is one piece, untouched
	cb2 := newCellBuffers(t.TempDir(), 1<<30)
	if err := cut(1, []float64{20.1, 39.1, 20.2, 39.2, 20.3, 39.1}, ClassMotorway, FlagOneway, "A1", cb2); err != nil {
		t.Fatal(err)
	}
	if len(cb2.bufs) != 1 || len(cb2.bufs[Cell{1, 200, 129}]) == 0 {
		t.Fatalf("one cell expected: %v", cb2.bufs)
	}
}

func TestBuildFromAPBF(t *testing.T) {
	// Bucharest-ish: a primary road that crosses a 0.1° border, a residential street inside one
	// cell, a footway, a building (not a road) and a way whose node is missing.
	pbf := osmpbf.Encode(
		osmpbf.EncBlock{Nodes: []osmpbf.TestNode{
			{ID: 1, Lat: 44.45, Lon: 26.05}, {ID: 2, Lat: 44.46, Lon: 26.12}, {ID: 3, Lat: 44.45, Lon: 26.15},
			{ID: 4, Lat: 44.431, Lon: 26.101}, {ID: 5, Lat: 44.432, Lon: 26.103},
			{ID: 6, Lat: 44.44, Lon: 26.11}, {ID: 7, Lat: 44.441, Lon: 26.111},
		}},
		osmpbf.EncBlock{Ways: []osmpbf.TestWay{
			{ID: 100, Tags: map[string]string{"highway": "primary", "name": "Șoseaua Colentina", "oneway": "yes"}, Refs: []int64{1, 2, 3}},
			{ID: 101, Tags: map[string]string{"highway": "residential", "name": "Strada Mică"}, Refs: []int64{4, 5}},
			{ID: 102, Tags: map[string]string{"highway": "footway"}, Refs: []int64{6, 7}},
			{ID: 103, Tags: map[string]string{"building": "yes"}, Refs: []int64{4, 5, 6}},
			{ID: 104, Tags: map[string]string{"highway": "tertiary"}, Refs: []int64{6, 999}},
		}},
	)
	dir := t.TempDir()
	p := filepath.Join(dir, "romania-latest.osm.pbf")
	os.WriteFile(p, pbf, 0o644)
	out := filepath.Join(dir, "roadtiles")
	if !Stale([]string{p}, out) {
		t.Fatal("no tiles yet is stale")
	}
	var log []string
	st, err := Build([]string{p}, out, Options{Workers: 2, Progress: func(s string) { log = append(log, s) }})
	if err != nil {
		t.Fatalf("%v\n%v", err, log)
	}
	// 104 has a missing node and keeps one point: dropped; 103 is not a road: 3 road ways
	if st.Ways != 3 || st.MajorTiles != 1 || st.FineTiles != 2 {
		t.Fatalf("stats: %+v", st)
	}
	if Stale([]string{p}, out) {
		t.Fatal("fresh tiles are not stale")
	}
	ib, _ := os.ReadFile(filepath.Join(out, "index.bin"))
	ix, err := DecodeIndex(ib)
	if err != nil || !ix.Has(Cell{1, 206, 134}) || !ix.Has(Cell{0, 2060, 1344}) || !ix.Has(Cell{0, 2061, 1344}) || ix.Has(Cell{0, 2062, 1344}) {
		t.Fatalf("index: %v", err)
	}
	// the major tile holds the primary road only, whole (it stays inside the 1° cell), no name
	// (no ref), one-way
	b, _ := os.ReadFile(filepath.Join(out, TileName(Cell{1, 206, 134})))
	major, err := Decode(b)
	if err != nil || len(major.Pieces) != 1 || major.Pieces[0].Class != ClassPrimary || major.Pieces[0].Name != "" ||
		major.Pieces[0].Flags&FlagOneway == 0 || len(major.Pieces[0].Points) != 6 {
		t.Fatalf("major: %+v %v", major, err)
	}
	// the fine tile at 26.1 holds the primary's second half, the street and the footway, with names
	b, _ = os.ReadFile(filepath.Join(out, TileName(Cell{0, 2061, 1344})))
	fine, err := Decode(b)
	if err != nil || len(fine.Pieces) != 3 {
		t.Fatalf("fine: %+v %v", fine, err)
	}
	names := map[string]uint8{}
	for _, pc := range fine.Pieces {
		names[pc.Name] = pc.Class
	}
	if names["Șoseaua Colentina"] != ClassPrimary || names["Strada Mică"] != ClassResidential || names[""] != ClassPath {
		t.Fatalf("fine pieces: %v", names)
	}
	// the other fine tile holds the primary's first half, ending on its east border
	b, _ = os.ReadFile(filepath.Join(out, TileName(Cell{0, 2060, 1344})))
	west, _ := Decode(b)
	if len(west.Pieces) != 1 || west.Pieces[0].Points[len(west.Pieces[0].Points)-2] != Q {
		t.Fatalf("west piece: %+v", west)
	}
	// nothing left behind, and a rebuild swaps the directory whole
	if _, err := os.Stat(out + ".tmp"); err == nil {
		t.Fatal("tmp left behind")
	}
	if _, err := Build([]string{p}, out, Options{Workers: 1}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out + ".old"); err == nil {
		t.Fatal("old left behind")
	}
	if got := FindPBFs(dir); len(got) != 1 || got[0] != p {
		t.Fatalf("FindPBFs: %v", got)
	}
}

func TestBitmapAndNodeFile(t *testing.T) {
	b := newBitmap()
	for _, id := range []int64{0, 1, 63, 64, 1 << 20, 5_000_000_000} {
		b.set(id)
	}
	if !b.has(64) || b.has(65) || !b.has(5_000_000_000) || b.has(5_000_000_001) || b.count() != 6 {
		t.Fatal("bitmap")
	}
	p := filepath.Join(t.TempDir(), "nodes.bin")
	var buf []byte
	for _, r := range []struct {
		id       int64
		lat, lon int32
	}{{10, 444268000, 261025000}, {20, -338688000, 1512093000}, {30, 0, 0}} {
		var rec [16]byte
		putRec(rec[:], r.id, r.lat, r.lon)
		buf = append(buf, rec[:]...)
	}
	os.WriteFile(p, buf, 0o644)
	nf, err := openNodes(p)
	if err != nil {
		t.Fatal(err)
	}
	defer nf.close()
	if lat, lon, ok := nf.lookup(20); !ok || lat != -33.8688 || lon != 151.2093 {
		t.Fatalf("lookup: %v %v %v", lat, lon, ok)
	}
	if _, _, ok := nf.lookup(15); ok {
		t.Fatal("found a node that is not there")
	}
	if lat, _, ok := nf.lookup(10); !ok || lat != 44.4268 {
		t.Fatal("first")
	}
}
