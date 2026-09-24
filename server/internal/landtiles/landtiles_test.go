package landtiles

import (
	"bytes"
	"encoding/binary"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// rect is a clockwise rectangle (the shapefile's outer-ring orientation).
func rect(x0, y0, x1, y1 float64) ring { return ring{x0, y0, x0, y1, x1, y1, x1, y0} }

// ccw reverses a ring (a hole, in the shapefile's convention).
func ccw(r ring) ring {
	out := make(ring, 0, len(r))
	for i := r.n() - 1; i >= 0; i-- {
		out = append(out, r[2*i], r[2*i+1])
	}
	return out
}

func TestSplitPreservesAreaAndCutsOnWholeDegrees(t *testing.T) {
	// An L-shape across four cells: concave, so a naive clip would join the arms.
	l := ring{20.5, 38.5, 20.5, 39.5, 20.8, 39.5, 20.8, 38.8, 21.5, 38.8, 21.5, 38.5}
	want := math.Abs(l.area())
	got := 0.0
	cells := map[Cell]int{}
	split(l, func(c Cell, r ring) {
		cells[c]++
		got += math.Abs(r.area())
		minX, minY, maxX, maxY := r.bbox()
		if minX < c.Lon0()-1e-9 || maxX > c.Lon0()+1+1e-9 || minY < c.Lat0()-1e-9 || maxY > c.Lat0()+1+1e-9 {
			t.Fatalf("piece outside its cell %v: %v %v %v %v", c, minX, minY, maxX, maxY)
		}
		if r.area()*l.area() < 0 {
			t.Fatal("orientation flipped")
		}
	})
	if math.Abs(got-want) > 1e-9 {
		t.Fatalf("area %v, want %v", got, want)
	}
	// 20.5..21.5 × 38.5..39.5 touches cells lon 20,21 × lat 38,39, but the upper-right one is empty
	if len(cells) != 3 || cells[Cell{200, 128}] == 0 || cells[Cell{201, 128}] == 0 || cells[Cell{200, 129}] == 0 {
		t.Fatalf("cells = %v", cells)
	}
	// a ring that only touches the next cell's edge stays in its own cell
	n := 0
	split(rect(20.2, 39.2, 21.0, 39.8), func(c Cell, r ring) {
		n++
		if c != (Cell{200, 129}) {
			t.Fatalf("edge-touching ring went to %v", c)
		}
	})
	if n != 1 {
		t.Fatalf("pieces = %d", n)
	}
}

func TestQuantizeAndEncodeRoundTrip(t *testing.T) {
	c := Cell{200, 129} // lon 20..21, lat 39..40
	q := quantize(rect(20.1, 39.1, 21.0, 39.3), c)
	near := func(v uint16, f float64) bool { return math.Abs(float64(v)-f*Q) <= 1 }
	if q == nil || !near(q[0], 0.1) || !near(q[5], 0.3) {
		t.Fatalf("q = %v", q)
	}
	// the east edge sits on the border: x == Q exactly
	if q[4] != Q || q[6] != Q {
		t.Fatalf("border not at Q: %v", q)
	}
	// repeated points collapse; a ring of fewer than three distinct points is nothing
	if quantize(ring{20.1, 39.1, 20.1, 39.1, 20.1000001, 39.1}, c) != nil {
		t.Fatal("degenerate ring kept")
	}
	tile := Tile{Cell: c, Rings: [][]uint16{q, {1, 2, 3, 4, 5, 6}}}
	back, err := Decode(tile.Encode())
	if err != nil || back.Cell != c || len(back.Rings) != 2 || back.Points() != 7 || back.Rings[1][5] != 6 {
		t.Fatalf("round trip: %+v %v", back, err)
	}
	if _, err := Decode([]byte("nope")); err == nil {
		t.Fatal("garbage decoded")
	}
}

// writeShp writes a minimal polygon shapefile: each record is a list of closed rings.
func writeShp(t *testing.T, path string, records [][]ring, extra [][]byte) {
	t.Helper()
	var body bytes.Buffer
	le, be := binary.LittleEndian, binary.BigEndian
	recNo := 1
	put := func(content []byte) {
		var h [8]byte
		be.PutUint32(h[0:], uint32(recNo))
		be.PutUint32(h[4:], uint32(len(content)/2))
		body.Write(h[:])
		body.Write(content)
		recNo++
	}
	for _, rec := range records {
		var c bytes.Buffer
		w32 := func(v int) { var b [4]byte; le.PutUint32(b[:], uint32(v)); c.Write(b[:]) }
		wf := func(v float64) { var b [8]byte; le.PutUint64(b[:], math.Float64bits(v)); c.Write(b[:]) }
		w32(5)
		for i := 0; i < 4; i++ {
			wf(0) // bbox (unused by the reader)
		}
		total := 0
		for _, r := range rec {
			total += r.n() + 1
		}
		w32(len(rec))
		w32(total)
		off := 0
		for _, r := range rec {
			w32(off)
			off += r.n() + 1
		}
		for _, r := range rec {
			for i := 0; i < r.n(); i++ {
				wf(r[2*i])
				wf(r[2*i+1])
			}
			wf(r[0]) // closed, as shapefiles are
			wf(r[1])
		}
		put(c.Bytes())
	}
	for _, e := range extra {
		put(e)
	}
	var hdr [100]byte
	be.PutUint32(hdr[0:], 9994)
	be.PutUint32(hdr[24:], uint32((100+body.Len())/2))
	le.PutUint32(hdr[28:], 1000)
	le.PutUint32(hdr[32:], 5)
	if err := os.WriteFile(path, append(hdr[:], body.Bytes()...), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestBuildFromAShapefile(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "land-polygons-complete-4326")
	os.MkdirAll(sub, 0o755)
	shp := filepath.Join(sub, "land_polygons.shp")
	nullShape := []byte{0, 0, 0, 0}
	pointShape := make([]byte, 20)
	binary.LittleEndian.PutUint32(pointShape, 1)
	writeShp(t, shp, [][]ring{
		{rect(20.10, 39.10, 20.30, 39.30)},                        // an island inside one cell
		{rect(10, 40, 12, 42)},                                    // four cells of solid land
		{rect(20.5, 37.5, 21.5, 38.5)},                            // a square across four cells
		{rect(30, 50, 31, 51), ccw(rect(30.4, 50.4, 30.6, 50.6))}, // a full cell with a lake
	}, [][]byte{nullShape, pointShape})
	if FindShapefile(dir) != shp {
		t.Fatalf("FindShapefile = %q", FindShapefile(dir))
	}
	out := filepath.Join(dir, "landtiles")
	if !Stale(shp, out) {
		t.Fatal("no tiles yet is stale")
	}
	st, err := Build(shp, out, nil)
	if err != nil {
		t.Fatal(err)
	}
	if st.Records != 4 || st.LandCells != 4 || st.CoastCell != 1+4+1 {
		t.Fatalf("stats = %+v", st)
	}
	if Stale(shp, out) {
		t.Fatal("fresh tiles are not stale")
	}
	ib, _ := os.ReadFile(filepath.Join(out, "index.bin"))
	if len(ib) != 4+Cols*Rows || binary.LittleEndian.Uint32(ib) != indexMagic {
		t.Fatalf("index len %d", len(ib))
	}
	idx := ib[4:]
	at := func(lon, lat int) byte { return idx[Cell{lon + 180, lat + 90}.Key()] }
	if at(10, 40) != Land || at(11, 41) != Land || at(20, 39) != Coast || at(30, 50) != Coast || at(0, 0) != Water || at(21, 38) != Coast {
		t.Fatalf("index: %d %d %d %d %d %d", at(10, 40), at(11, 41), at(20, 39), at(30, 50), at(0, 0), at(21, 38))
	}
	// the lake cell keeps both rings; the island tile holds the island only
	b, err := os.ReadFile(filepath.Join(out, TileName(Cell{210, 140})))
	if err != nil {
		t.Fatal(err)
	}
	lake, _ := Decode(b)
	if len(lake.Rings) != 2 {
		t.Fatalf("lake cell rings = %d", len(lake.Rings))
	}
	b, _ = os.ReadFile(filepath.Join(out, TileName(Cell{200, 129})))
	isl, _ := Decode(b)
	if len(isl.Rings) != 1 || isl.Points() != 4 {
		t.Fatalf("island tile = %+v", isl)
	}
	// solid land and water have no files
	if _, err := os.Stat(filepath.Join(out, TileName(Cell{190, 130}))); err == nil {
		t.Fatal("an all-land cell has a file")
	}
	// a rebuild swaps the directory whole
	if _, err := Build(shp, out, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(out + ".tmp"); err == nil {
		t.Fatal("tmp left behind")
	}
}

func TestScanRejectsNonShapefiles(t *testing.T) {
	p := filepath.Join(t.TempDir(), "x.shp")
	os.WriteFile(p, make([]byte, 120), 0o644)
	if err := ScanShapefile(p, func(int, []ring) error { return nil }); err == nil {
		t.Fatal("zeros accepted as a shapefile")
	}
}
