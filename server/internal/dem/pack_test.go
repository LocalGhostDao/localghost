package dem

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"
)

// Tiles packed into one file, read back out of their sections; the pack and a loose tile in one
// folder; the same tiles give the same bytes; a block's name and corner round-trip.
func TestPackOfTiles(t *testing.T) {
	src := t.TempDir()
	write := func(lat, lon int, name string) {
		s := tiffSpec{w: 12, h: 12, cw: 8, ch: 8, bo: binary.LittleEndian, format: 3, bits: 32, comp: 8, pred: 3, lat: lat, lon: lon}
		if err := os.WriteFile(filepath.Join(src, name), s.build(), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write(51, -1, "Copernicus_DSM_COG_30_N51_00_W001_00_DEM.tif")
	write(38, 20, "Copernicus_DSM_COG_30_N38_00_E020_00_DEM.tif")
	write(-12, 130, "Copernicus_DSM_COG_30_S12_00_E130_00_DEM.tif")
	paths := []string{filepath.Join(src, "Copernicus_DSM_COG_30_S12_00_E130_00_DEM.tif"),
		filepath.Join(src, "Copernicus_DSM_COG_30_N51_00_W001_00_DEM.tif"),
		filepath.Join(src, "Copernicus_DSM_COG_30_N38_00_E020_00_DEM.tif")}
	var a, b bytes.Buffer
	if n, err := WritePack(&a, paths); err != nil || n != 3 {
		t.Fatal(n, err)
	}
	if _, err := WritePack(&b, []string{paths[2], paths[0], paths[1]}); err != nil || !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("the same tiles in another order gave other bytes")
	}
	idx, err := ReadPackIndex(bytes.NewReader(a.Bytes()), int64(a.Len()))
	if err != nil || len(idx) != 3 || idx[0].Lat != -12 || idx[1].Lat != 38 || idx[2].Lon != -1 || idx[2].Name != "Copernicus_DSM_COG_30_N51_00_W001_00_DEM.tif" {
		t.Fatalf("%+v %v", idx, err)
	}
	// a folder with the pack and one loose tile of its own
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, PackName(30, -30)), a.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
	s := tiffSpec{w: 12, h: 12, cw: 8, ch: 8, bo: binary.LittleEndian, format: 3, bits: 32, comp: 8, pred: 3, lat: 40, lon: 10}
	os.WriteFile(filepath.Join(dir, "Copernicus_DSM_COG_30_N40_00_E010_00_DEM.tif"), s.build(), 0o644)
	os.WriteFile(filepath.Join(dir, "broken.heights"), []byte("not a pack"), 0o644)
	set := Open(dir)
	if set.Tiles() != 4 || set.Packs() != 1 {
		t.Fatal(set.Tiles(), set.Packs())
	}
	for _, c := range [][2]float64{{51.5, -0.5}, {38.5, 20.5}, {-11.5, 130.5}, {40.5, 10.5}} {
		if _, ok := set.Height(c[0], c[1]); !ok {
			t.Fatalf("no height at %v", c)
		}
	}
	// the value a tile gives is the tile's, through the section: row 0 col 0 of the N51 tile
	if h, ok := set.Height(52-0.5/12, -1+0.5/12); !ok || h != 0 {
		t.Fatal(h, ok)
	}
	if h, ok := set.Height(52-1.5/12, -1+2.5/12); !ok || h < 101.9 || h > 102.1 {
		t.Fatal(h, ok)
	}
	// a cut pack: the index refuses it, the folder's loose tiles still serve
	cut := t.TempDir()
	os.WriteFile(filepath.Join(cut, PackName(30, -30)), a.Bytes()[:a.Len()-10], 0o644)
	if Open(cut).Tiles() != 0 {
		t.Fatal("a cut pack served tiles")
	}
	if _, err := ReadPackIndex(bytes.NewReader(a.Bytes()[:a.Len()-10]), int64(a.Len()-10)); err == nil {
		t.Fatal("a cut pack read")
	}
	// two tiles of one corner, or a name without one, refuse
	dup := filepath.Join(src, "copy_N51_00_W001_00_DEM.tif")
	os.WriteFile(dup, []byte("x"), 0o644)
	if _, err := WritePack(&bytes.Buffer{}, []string{paths[1], dup}); err == nil {
		t.Fatal("two of one corner")
	}
	if _, err := WritePack(&bytes.Buffer{}, []string{filepath.Join(src, "NOTICE.txt")}); err == nil {
		t.Fatal("a name without a corner")
	}
	// blocks and names
	for _, c := range []struct{ lat, lon, bl, bo int }{{51, -1, 30, -30}, {38, 20, 30, 0}, {-12, 130, -30, 120}, {0, 0, 0, 0}, {-1, -1, -30, -30}, {89, 179, 60, 150}, {-90, -180, -90, -180}} {
		if bl, bo := BlockOf(c.lat, c.lon); bl != c.bl || bo != c.bo {
			t.Fatalf("block of %d,%d: %d,%d", c.lat, c.lon, bl, bo)
		}
	}
	if PackName(30, -30) != "GLO-90_N30_W030.heights" || PackName(-30, 120) != "GLO-90_S30_E120.heights" || PackName(0, 0) != "GLO-90_N00_E000.heights" {
		t.Fatal(PackName(30, -30), PackName(-30, 120), PackName(0, 0))
	}
	if lat, lon, ok := PackCorner("GLO-90_S30_E120.heights"); !ok || lat != -30 || lon != 120 {
		t.Fatal(lat, lon, ok)
	}
	if _, _, ok := PackCorner("GLO-90_S30_E120.tif"); ok {
		t.Fatal("not a pack name")
	}
}
