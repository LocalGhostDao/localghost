// Package dem gives the height of the ground anywhere the box has elevation tiles for: the
// Copernicus DEM at 90 m (GLO-90, the mirror's set elevation, one GeoTIFF per degree square, under
// <volume>/geo/elevation), read on the box with this package's own GeoTIFF reader. A height is
// asked of the disk, never of a service: where the person walked stays on the box.
//
// The tiles are named by their south-west corner ("…_N51_00_W001_00_DEM.tif" is 51°N to 52°N,
// 1°W to 0°), which is how the set is indexed; a tile is read whole when first needed (a few MB)
// and the last few are kept. The mirror carries the tiles in packs of a 30-degree block each
// (pack.go: GLO-90_N30_W030.heights), read the same way, a tile out of its section of the pack.
package dem

import (
	"io"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
)

const tilesKept = 12

var reCorner = regexp.MustCompile(`(?i)_([NS])(\d{2})(?:_\d{2})?_([EW])(\d{3})(?:_\d{2})?_`)

// Corner is the south-west corner a tile's file name gives; ok is false for another file.
func Corner(name string) (lat, lon int, ok bool) {
	m := reCorner.FindStringSubmatch(name)
	if m == nil {
		return 0, 0, false
	}
	a, _ := strconv.Atoi(m[2])
	b, _ := strconv.Atoi(m[4])
	if strings.EqualFold(m[1], "S") {
		a = -a
	}
	if strings.EqualFold(m[3], "W") {
		b = -b
	}
	if a < -90 || a >= 90 || b < -180 || b >= 180 {
		return 0, 0, false
	}
	return a, b, true
}

// Set is the tiles in a folder: loose GeoTIFFs, and the tiles of the packs there (pack.go).
type Set struct {
	Dir   string
	files map[[2]int]source
	packs int

	mu    sync.Mutex
	cache map[[2]int]*Tile
	order [][2]int
	bad   map[[2]int]bool
}

// source is where a tile's bytes are: a file, whole (off 0, n its size) or a section of a pack.
type source struct {
	path   string
	off, n int64
}

// Open indexes the tiles in a folder (none is an empty set, not an error). A tile in a pack and
// loose as well is read from the pack.
func Open(dir string) *Set {
	s := &Set{Dir: dir, files: map[[2]int]source{}, cache: map[[2]int]*Tile{}, bad: map[[2]int]bool{}}
	ents, _ := os.ReadDir(dir)
	var packs []string
	for _, e := range ents {
		n := e.Name()
		if e.IsDir() || strings.HasPrefix(n, ".") {
			continue
		}
		switch {
		case strings.HasSuffix(strings.ToLower(n), ".tif"):
			if lat, lon, ok := Corner(n); ok {
				st, err := e.Info()
				if err != nil {
					continue
				}
				s.files[[2]int{lat, lon}] = source{filepath.Join(dir, n), 0, st.Size()}
			}
		case strings.HasSuffix(n, ".heights"):
			packs = append(packs, filepath.Join(dir, n))
		}
	}
	for _, p := range packs {
		f, err := os.Open(p)
		if err != nil {
			continue
		}
		st, err := f.Stat()
		var idx []PackEntry
		if err == nil {
			idx, err = ReadPackIndex(f, st.Size())
		}
		f.Close()
		if err != nil {
			continue // not a pack, or a cut one: the loose tiles still serve
		}
		s.packs++
		for _, e := range idx {
			s.files[[2]int{e.Lat, e.Lon}] = source{p, e.Off, e.Len}
		}
	}
	return s
}

// Packs is how many packs the set read.
func (s *Set) Packs() int {
	if s == nil {
		return 0
	}
	return s.packs
}

// Tiles is how many tiles the set holds.
func (s *Set) Tiles() int {
	if s == nil {
		return 0
	}
	return len(s.files)
}

// Height is the ground's height in metres at a place; ok is false where the set has no tile (the
// sea, mostly, or a part of the world not fetched) or the tile has no value there.
func (s *Set) Height(lat, lon float64) (float64, bool) {
	if s == nil || len(s.files) == 0 || math.IsNaN(lat) || math.IsNaN(lon) {
		return 0, false
	}
	k := [2]int{int(math.Floor(lat)), int(math.Floor(lon))}
	t := s.tile(k)
	if t == nil {
		return 0, false
	}
	return t.At(lat, lon)
}

func (s *Set) tile(k [2]int) *Tile {
	s.mu.Lock()
	defer s.mu.Unlock()
	if t, ok := s.cache[k]; ok {
		return t
	}
	src, ok := s.files[k]
	if !ok || s.bad[k] {
		return nil
	}
	f, err := os.Open(src.path)
	if err != nil {
		s.bad[k] = true
		return nil
	}
	defer f.Close()
	t, err := ReadTile(io.NewSectionReader(f, src.off, src.n), src.n)
	if err != nil {
		s.bad[k] = true
		return nil
	}
	s.cache[k] = t
	s.order = append(s.order, k)
	if len(s.order) > tilesKept {
		delete(s.cache, s.order[0])
		s.order = s.order[1:]
	}
	return t
}

// Climb is what a line of heights climbed and descended, counting a change only once it passes
// the threshold from the last turn (so the model's and the trail's jitter does not add up), and
// the line's highest and lowest; ok is false with fewer than two heights.
func Climb(heights []float64, threshold float64) (up, down, high, low float64, ok bool) {
	if len(heights) < 2 {
		return 0, 0, 0, 0, false
	}
	high, low = heights[0], heights[0]
	ref := heights[0] // the last turning point
	dir := 0          // +1 climbing, -1 descending, 0 not yet known
	ext := heights[0] // the extreme reached in the current direction
	for _, h := range heights[1:] {
		high, low = math.Max(high, h), math.Min(low, h)
		switch dir {
		case 0:
			if h-ref >= threshold {
				dir, ext = 1, h
			} else if ref-h >= threshold {
				dir, ext = -1, h
			}
		case 1:
			if h > ext {
				ext = h
			} else if ext-h >= threshold {
				up += ext - ref
				ref, dir, ext = ext, -1, h
			}
		case -1:
			if h < ext {
				ext = h
			} else if h-ext >= threshold {
				down += ref - ext
				ref, dir, ext = ext, 1, h
			}
		}
	}
	switch dir {
	case 1:
		up += ext - ref
	case -1:
		down += ref - ext
	}
	return up, down, high, low, true
}
