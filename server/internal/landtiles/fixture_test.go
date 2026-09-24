package landtiles

import (
	"bytes"
	"flag"
	"os"
	"path/filepath"
	"testing"
)

var updateFixture = flag.Bool("update-fixture", false, "rewrite testdata/tile_fixture.lgt")

// fixtureTile is the tile the phone's decoder is tested against (app test resources hold a copy):
// cell lon 20..21, lat 39..40 with an island inside it and a mainland corner cut by its east and
// north borders.
func fixtureTile() Tile {
	c := Cell{200, 129}
	t := Tile{Cell: c}
	for _, r := range []ring{rect(20.10, 39.10, 20.30, 39.30), rect(20.8, 39.5, 21.6, 40.4)} {
		split(r, func(got Cell, piece ring) {
			if got == c {
				if q := quantize(piece, c); q != nil {
					t.Rings = append(t.Rings, q)
				}
			}
		})
	}
	return t
}

func TestTileFixtureIsWhatTheEncoderWrites(t *testing.T) {
	p := filepath.Join("testdata", "tile_fixture.lgt")
	b := fixtureTile().Encode()
	if *updateFixture {
		os.MkdirAll("testdata", 0o755)
		if err := os.WriteFile(p, b, 0o644); err != nil {
			t.Fatal(err)
		}
	}
	want, err := os.ReadFile(p)
	if err != nil {
		t.Fatalf("fixture missing (go test -run Fixture -update-fixture): %v", err)
	}
	if !bytes.Equal(b, want) {
		t.Fatal("the encoder no longer writes the fixture the phone is tested against")
	}
	tile, _ := Decode(want)
	if len(tile.Rings) != 2 || tile.Points() != 8 {
		t.Fatalf("fixture = %+v", tile)
	}
}
