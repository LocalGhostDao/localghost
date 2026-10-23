package roadtiles

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateFixture = flag.Bool("update-fixture", false, "rewrite testdata/road_fixture.lgr and road_index_fixture.bin")

// fixtureTile is the road tile the phone's decoder is tested against (app test resources hold a
// copy): the fine cell at lon 26.1, lat 44.4 with a named one-way primary, an unnamed footway, a
// street whose name has a non-ASCII letter, and (since the water rides in the tiles) a named pond
// with an island in it and a river.
func fixtureTile() Tile {
	return Tile{Cell: Cell{0, 2061, 1344}, Pieces: []Piece{
		{Class: ClassPrimary, Flags: FlagOneway, Name: "Șoseaua Colentina", Points: []uint16{0, 32768, 20000, 33000, Q, 34000}},
		{Class: ClassPath, Points: []uint16{100, 100, 500, 900}},
		{Class: ClassResidential, Flags: FlagBridge, Name: "Strada Mică", Points: []uint16{40000, 40000, 40000, 45000, 42000, 45000}},
		{Class: ClassWaterArea, Name: "Lacul Tei", Points: []uint16{50000, 50000, 60000, 50000, 60000, 58000, 50000, 58000}},
		{Class: ClassWaterArea, Flags: FlagHole, Name: "Lacul Tei", Points: []uint16{54000, 53000, 56000, 53000, 55000, 55000}},
		{Class: ClassRiver, Name: "Colentina", Points: []uint16{0, 60000, 30000, 61000, Q, 62000}},
	}}
}

func TestFixturesAreWhatTheEncoderWrites(t *testing.T) {
	tp := filepath.Join("testdata", "road_fixture.lgr")
	ip := filepath.Join("testdata", "road_index_fixture.bin")
	tb := fixtureTile().Encode()
	ix := NewIndex()
	ix.Set(Cell{0, 2061, 1344})
	ix.Set(Cell{1, 206, 134})
	ib := ix.Encode()
	if *updateFixture {
		os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(tp, tb, 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(ip, ib, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(tp)
	if err != nil {
		t.Fatalf("fixture missing (go test -run Fixtures -update-fixture): %v", err)
	}
	if !bytes.Equal(tb, want) {
		t.Fatal("the encoder no longer writes the tile fixture the phone is tested against")
	}
	wantIx, err := os.ReadFile(ip)
	if err != nil || !bytes.Equal(ib, wantIx) {
		t.Fatal("the index fixture differs")
	}
	tile, err := Decode(want)
	if err != nil || len(tile.Pieces) != 6 || tile.Pieces[0].Name != "Șoseaua Colentina" || tile.Pieces[4].Flags&FlagHole == 0 || tile.Pieces[5].Class != ClassRiver {
		t.Fatalf("fixture = %+v %v", tile, err)
	}
}
