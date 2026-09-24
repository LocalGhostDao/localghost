package landtiles

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"time"
)

// ScanShapefile streams the polygons of an ESRI shapefile (.shp, the geometry file; the .dbf and
// .shx are not needed), calling fn with each record's rings as lon,lat pairs. Polygon, PolygonZ and
// PolygonM are read (Z and M are skipped); every other shape type is skipped. A ring's closing
// point (shapefiles repeat the first point) is dropped. The format is small enough to read by
// hand: a 100-byte header, then records of a big-endian header and a little-endian body.
func ScanShapefile(path string, fn func(rec int, rings []ring) error) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	br := bufio.NewReaderSize(f, 1<<20)
	var hdr [100]byte
	if _, err := io.ReadFull(br, hdr[:]); err != nil {
		return fmt.Errorf("shapefile header: %w", err)
	}
	if binary.BigEndian.Uint32(hdr[0:]) != 9994 {
		return errors.New("not a shapefile (file code is not 9994)")
	}
	var buf []byte
	var rh [8]byte
	le := binary.LittleEndian
	for rec := 0; ; rec++ {
		if _, err := io.ReadFull(br, rh[:]); err != nil {
			if err == io.EOF {
				return nil
			}
			return fmt.Errorf("record %d header: %w", rec, err)
		}
		size := int(binary.BigEndian.Uint32(rh[4:])) * 2
		if size < 4 || size > 1<<31-1 {
			return fmt.Errorf("record %d: bad length %d", rec, size)
		}
		if cap(buf) < size {
			buf = make([]byte, size)
		}
		buf = buf[:size]
		if _, err := io.ReadFull(br, buf); err != nil {
			return fmt.Errorf("record %d: %w", rec, err)
		}
		st := le.Uint32(buf)
		if st != 5 && st != 15 && st != 25 {
			continue // null shapes and anything that is not a polygon
		}
		if size < 44 {
			return fmt.Errorf("record %d: short polygon", rec)
		}
		parts := int(le.Uint32(buf[36:]))
		points := int(le.Uint32(buf[40:]))
		pOff := 44 + 4*parts
		if parts < 0 || points < 0 || pOff+16*points > size {
			return fmt.Errorf("record %d: %d parts, %d points do not fit %d bytes", rec, parts, points, size)
		}
		rings := make([]ring, 0, parts)
		for p := 0; p < parts; p++ {
			start := int(le.Uint32(buf[44+4*p:]))
			end := points
			if p+1 < parts {
				end = int(le.Uint32(buf[44+4*(p+1):]))
			}
			if start < 0 || end > points || end-start < 4 {
				continue
			}
			r := make(ring, 0, 2*(end-start))
			for i := start; i < end; i++ {
				o := pOff + 16*i
				r = append(r, math.Float64frombits(le.Uint64(buf[o:])), math.Float64frombits(le.Uint64(buf[o+8:])))
			}
			if n := len(r); n >= 4 && r[0] == r[n-2] && r[1] == r[n-1] {
				r = r[:n-2]
			}
			if r.n() >= 3 {
				rings = append(rings, r)
			}
		}
		if len(rings) > 0 {
			if err := fn(rec, rings); err != nil {
				return err
			}
		}
	}
}

// Stats is what a build produced.
type Stats struct {
	Records   int
	CoastCell int
	LandCells int
	Points    int
	Bytes     int64
	Took      time.Duration
}

func (s Stats) String() string {
	return fmt.Sprintf("%d polygons → %d coast tiles (%d points, %.1f MB), %d all-land cells, in %s",
		s.Records, s.CoastCell, s.Points, float64(s.Bytes)/1e6, s.LandCells, s.Took.Round(time.Second))
}

type acc struct {
	area  float64
	rings [][]uint16
}

// Build cuts the shapefile into tiles under outDir: index.bin plus one .lgt per coast cell. It
// writes into outDir+".tmp" and swaps the directory in only when complete, so a reader sees the
// old tiles or the new ones, never half. progress (may be nil) is called now and then.
func Build(shp, outDir string, progress func(string)) (Stats, error) {
	t0 := time.Now()
	var st Stats
	cells := map[int]*acc{}
	last := time.Now()
	err := ScanShapefile(shp, func(rec int, rings []ring) error {
		st.Records++
		for _, r := range rings {
			split(r, func(c Cell, piece ring) {
				a := cells[c.Key()]
				if a == nil {
					a = &acc{}
					cells[c.Key()] = a
				}
				ar := piece.area()
				a.area += ar
				if math.Abs(ar) < emptyArea {
					return
				}
				if q := quantize(piece, c); q != nil {
					a.rings = append(a.rings, q)
				}
			})
		}
		if progress != nil && time.Since(last) > 10*time.Second {
			last = time.Now()
			progress(fmt.Sprintf("%d polygons read, %d cells touched", st.Records, len(cells)))
		}
		return nil
	})
	if err != nil {
		return st, err
	}
	tmp := outDir + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(tmp, 0o755); err != nil {
		return st, err
	}
	idx := make([]byte, Cols*Rows)
	for key, a := range cells {
		c := Cell{key % Cols, key / Cols}
		land := math.Abs(a.area)
		switch {
		case land >= fullFrac:
			idx[key] = Land
			st.LandCells++
		case len(a.rings) > 0 && land > emptyArea:
			t := Tile{Cell: c, Rings: a.rings}
			b := t.Encode()
			if err := os.WriteFile(filepath.Join(tmp, TileName(c)), b, 0o644); err != nil {
				return st, err
			}
			idx[key] = Coast
			st.CoastCell++
			st.Points += t.Points()
			st.Bytes += int64(len(b))
		}
	}
	if err := os.WriteFile(filepath.Join(tmp, "index.bin"), EncodeIndex(idx), 0o644); err != nil {
		return st, err
	}
	old := outDir + ".old"
	_ = os.RemoveAll(old)
	if _, err := os.Stat(outDir); err == nil {
		if err := os.Rename(outDir, old); err != nil {
			return st, err
		}
	}
	if err := os.Rename(tmp, outDir); err != nil {
		return st, err
	}
	_ = os.RemoveAll(old)
	st.Took = time.Since(t0)
	return st, nil
}

// FindShapefile looks for the land polygons under dir: land_polygons.shp in dir or one level down
// (the zip unpacks into land-polygons-complete-4326/). "" when there is none.
func FindShapefile(dir string) string {
	for _, p := range []string{filepath.Join(dir, "land_polygons.shp")} {
		if fileOK(p) {
			return p
		}
	}
	if es, err := os.ReadDir(dir); err == nil {
		for _, e := range es {
			if e.IsDir() {
				if p := filepath.Join(dir, e.Name(), "land_polygons.shp"); fileOK(p) {
					return p
				}
			}
		}
	}
	return ""
}

// Stale reports whether the tiles under outDir are missing or older than the shapefile.
func Stale(shp, outDir string) bool {
	si, err := os.Stat(shp)
	if err != nil {
		return false
	}
	ii, err := os.Stat(filepath.Join(outDir, "index.bin"))
	return err != nil || ii.ModTime().Before(si.ModTime())
}

func fileOK(p string) bool {
	fi, err := os.Stat(p)
	return err == nil && !fi.IsDir() && fi.Size() > 100
}
