package grib2

import (
	"compress/bzip2"
	"compress/gzip"
	"encoding/json"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// The fixtures were written by ecCodes (testdata/gen.py): every packing the reader does, a
// bitmap, an interval field, several messages in one file, a bz2 one as DWD serves them.
// expected.json.gz holds the values ecCodes reads back, so the reader is checked against the
// reference library, not against itself.
type expected struct {
	PackingType string     `json:"packingType"`
	Bits        int        `json:"bitsPerValue"`
	Ni          int        `json:"Ni"`
	Nj          int        `json:"Nj"`
	La1         float64    `json:"la1"`
	Lo1         float64    `json:"lo1"`
	Step        any        `json:"step"`
	Category    int        `json:"category"`
	Number      int        `json:"number"`
	Values      []*float64 `json:"values"`
	Messages    int        `json:"messages"`
	ScanNorth   bool       `json:"scanNorth"`
}

func loadExpected(t *testing.T) map[string]expected {
	t.Helper()
	f, err := os.Open(filepath.Join("testdata", "expected.json.gz"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]expected
	if err := json.NewDecoder(zr).Decode(&m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestEveryPackingReadsAsEcCodesDoes(t *testing.T) {
	exp := loadExpected(t)
	for name, e := range exp {
		t.Run(name, func(t *testing.T) {
			f, err := os.Open(filepath.Join("testdata", name))
			if err != nil {
				t.Fatal(err)
			}
			defer f.Close()
			var r *Reader
			if filepath.Ext(name) == ".bz2" {
				r = NewReader(bzip2.NewReader(f))
			} else {
				r = NewReader(f)
			}
			m, err := r.Next()
			if err != nil {
				t.Fatal(err)
			}
			// ecCodes writes the first longitude in 0..360 (350 for 10° W), as the centres do
			if m.Grid.Ni != e.Ni || m.Grid.Nj != e.Nj || m.Grid.Lat1 != e.La1 || math.Mod(m.Grid.Lon1+360, 360) != math.Mod(e.Lo1+360, 360) || m.Grid.Di != m.Grid.Dj || m.Grid.ScanNorth != e.ScanNorth || m.Grid.ScanWest {
				t.Fatalf("grid %+v", m.Grid)
			}
			if m.Param.Category != e.Category || m.Param.Number != e.Number {
				t.Fatalf("param %+v", m.Param)
			}
			if s, ok := e.Step.(float64); ok && m.Param.Step != int(s) {
				t.Fatalf("step %d, want %v", m.Param.Step, e.Step)
			}
			if s, ok := e.Step.(string); ok && (!m.Param.Interval || s != "3-6" || m.Param.StepFrom != 3 || m.Param.Step != 6) {
				t.Fatalf("interval %+v, want %s", m.Param, s)
			}
			if m.RefTime.Year() != 2026 || m.RefTime.Month() != 10 || m.RefTime.Day() != 10 || (m.RefTime.Hour() != 6 && m.RefTime.Hour() != 0) {
				t.Fatalf("reference time %v", m.RefTime)
			}
			vals, err := m.Values()
			if err != nil {
				t.Fatal(err)
			}
			if len(vals) != len(e.Values) {
				t.Fatalf("%d values, want %d", len(vals), len(e.Values))
			}
			tol := 1e-3
			if e.Bits >= 20 {
				tol = 2e-3
			}
			if e.Bits >= 24 {
				tol = 1e-2 // float32 over a 24-bit range
			}
			for i, want := range e.Values {
				got := vals[i]
				if want == nil {
					if !isNaN(got) {
						t.Fatalf("value %d: %v, want missing", i, got)
					}
					continue
				}
				if isNaN(got) || math.Abs(float64(got)-*want) > tol {
					t.Fatalf("value %d: %v, want %v (%s, %d bits)", i, got, *want, e.PackingType, e.Bits)
				}
			}
			// the rest of the messages in a multi-message file
			n := 1
			for {
				m2, err := r.Next()
				if err != nil {
					break
				}
				n++
				if m2.Grid.Ni != e.Ni {
					t.Fatalf("message %d grid %+v", n, m2.Grid)
				}
				if _, err := m2.Values(); err != nil {
					t.Fatalf("message %d: %v", n, err)
				}
			}
			if e.Messages > 0 && n != e.Messages {
				t.Fatalf("%d messages, want %d", n, e.Messages)
			}
		})
	}
}

func TestSampleIsBilinearAndWraps(t *testing.T) {
	// a 4×3 global grid, 90° apart, lon 0..270, lat 90..-90 (north to south)
	g := Grid{Ni: 4, Nj: 3, Lat1: 90, Lon1: 0, Lat2: -90, Lon2: 270, Di: 90, Dj: 90}
	vals := []float32{0, 1, 2, 3, 10, 11, 12, 13, 20, 21, 22, 23}
	v, ok := g.Sample(vals, 90, 0)
	if !ok || v != 0 {
		t.Fatalf("%v %v", v, ok)
	}
	v, ok = g.Sample(vals, 0, 90)
	if !ok || v != 11 {
		t.Fatalf("%v %v", v, ok)
	}
	// halfway between the equator's 90 and 180 columns
	v, ok = g.Sample(vals, 0, 135)
	if !ok || v != 11.5 {
		t.Fatalf("%v %v", v, ok)
	}
	// across the seam: lon 315 is between the 270 column (13) and the 0 column (10)
	v, ok = g.Sample(vals, 0, 315)
	if !ok || v != 11.5 {
		t.Fatalf("seam %v %v", v, ok)
	}
	// a negative longitude is the same point
	v2, _ := g.Sample(vals, 0, -45)
	if v2 != v {
		t.Fatalf("-45: %v, want %v", v2, v)
	}
	// between rows
	v, ok = g.Sample(vals, 45, 0)
	if !ok || v != 5 {
		t.Fatalf("%v %v", v, ok)
	}
	// a regional grid does not answer past its edge
	r := Grid{Ni: 41, Nj: 33, Lat1: 50, Lon1: -10, Lat2: 42, Lon2: 0, Di: 0.25, Dj: 0.25}
	rv := make([]float32, 41*33)
	for i := range rv {
		rv[i] = float32(i)
	}
	if _, ok := r.Sample(rv, 45, 1); ok {
		t.Fatal("east of the grid answered")
	}
	if _, ok := r.Sample(rv, 51, -5); ok {
		t.Fatal("north of the grid answered")
	}
	v, ok = r.Sample(rv, 42, 0)
	if !ok || v != float32(41*33-1) {
		t.Fatalf("the last corner: %v %v", v, ok)
	}
	v, ok = r.Sample(rv, 49.875, -9.875)
	if !ok || v != (0+1+41+42)/4.0 {
		t.Fatalf("the first cell's middle: %v %v", v, ok)
	}
	// a missing neighbour: no answer
	rv[0] = Missing
	if _, ok := r.Sample(rv, 49.875, -9.875); ok {
		t.Fatal("a missing corner answered")
	}
	// south-to-north scanning
	n := Grid{Ni: 4, Nj: 3, Lat1: -90, Lon1: 0, Lat2: 90, Lon2: 270, Di: 90, Dj: 90, ScanNorth: true}
	v, ok = n.Sample(vals, 90, 0)
	if !ok || v != 20 {
		t.Fatalf("scan north %v %v", v, ok)
	}
}

func TestBitsReader(t *testing.T) {
	r := bits{b: []byte{0xA5, 0xFF, 0x00}}
	if r.read(4) != 0xA || r.read(4) != 0x5 || r.read(12) != 0xFF0 || r.read(4) != 0 {
		t.Fatal("bits")
	}
	r = bits{b: []byte{0x80}}
	if r.read(1) != 1 || r.read(1) != 0 {
		t.Fatal("single bits")
	}
	r.align()
	if r.pos != 8 {
		t.Fatal("align")
	}
	if signMagBits(0x80|5, 8) != -5 || signMagBits(5, 8) != 5 {
		t.Fatal("sign-magnitude")
	}
}
