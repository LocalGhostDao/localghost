// Package countrycells says which map cells a country covers, so the phone can ask for "all of
// Greece" instead of a radius around where it has been. The countries are Natural Earth's admin-0
// polygons (the world.geojson every box has under <mount>/geo); the cells are the road tiles'
// grids (internal/roadtiles: level 0 a tenth of a degree, level 1 a degree, the coast's grid).
//
// A cell counts when the country overlaps it at all: its centre is inside the country, or the
// country's border passes through it (every vertex's cell, and the cells an edge crosses). The
// first alone would miss a thin coast; the second alone would miss the interior. Rasterised once
// at load with a scanline per cell row and even-odd filling, so holes (Lesotho in South Africa)
// come out right.
package countrycells

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"math/bits"
	"os"
	"sort"
	"strings"

	"github.com/LocalGhostDao/localghost/server/internal/roadtiles"
)

// Country is one admin-0 unit and the cells it overlaps, as road-tile index keys (level 0 for
// Fine, level 1 for Major), sorted.
type Country struct {
	Code  string // ISO 3166-1 alpha-2 where Natural Earth has one, else its ADM0_A3
	Name  string
	Fine  []int32
	Major []int32
	// the polygons themselves, each with its bounding box, for At: a point's country
	polys []polygon
	boxes [][4]float64 // minLon, minLat, maxLon, maxLat
}

// Atlas is every country, by code, in name order.
type Atlas struct {
	Countries []Country
	byCode    map[string]int
}

// Get is the country with this code, or nil.
func (a *Atlas) Get(code string) *Country {
	if a == nil {
		return nil
	}
	i, ok := a.byCode[strings.ToUpper(code)]
	if !ok {
		return nil
	}
	return &a.Countries[i]
}

// Load reads a Natural Earth admin-0 GeoJSON and rasterises every country.
func Load(path string) (*Atlas, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return Read(f)
}

type feature struct {
	Properties map[string]any `json:"properties"`
	Geometry   struct {
		Type        string          `json:"type"`
		Coordinates json.RawMessage `json:"coordinates"`
	} `json:"geometry"`
}

// Read is Load over a reader. Features are decoded one at a time (the 10m world is tens of
// megabytes; the whole of it as one value would be several times that).
func Read(r io.Reader) (*Atlas, error) {
	dec := json.NewDecoder(r)
	// walk to the "features" array
	found := false
	for !found {
		tok, err := dec.Token()
		if err != nil {
			return nil, fmt.Errorf("not a FeatureCollection: %w", err)
		}
		if s, ok := tok.(string); ok && s == "features" {
			if tok, err = dec.Token(); err != nil {
				return nil, err
			}
			if d, ok := tok.(json.Delim); !ok || d != '[' {
				return nil, errors.New("features is not an array")
			}
			found = true
		}
	}
	a := &Atlas{byCode: map[string]int{}}
	for dec.More() {
		var ft feature
		if err := dec.Decode(&ft); err != nil {
			return nil, fmt.Errorf("feature %d: %w", len(a.Countries), err)
		}
		code, name := identify(ft.Properties)
		if code == "" {
			continue
		}
		polys, err := polygons(ft.Geometry.Type, ft.Geometry.Coordinates)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", name, err)
		}
		c := Country{Code: code, Name: name, polys: polys, boxes: boxesOf(polys)}
		c.Fine, c.Major = Rasterise(polys)
		if prev, dup := a.byCode[code]; dup {
			// Natural Earth has one feature per country; should two share a code, keep the union
			a.Countries[prev].Fine = union(a.Countries[prev].Fine, c.Fine)
			a.Countries[prev].Major = union(a.Countries[prev].Major, c.Major)
			a.Countries[prev].polys = append(a.Countries[prev].polys, c.polys...)
			a.Countries[prev].boxes = append(a.Countries[prev].boxes, c.boxes...)
			continue
		}
		a.byCode[code] = len(a.Countries)
		a.Countries = append(a.Countries, c)
	}
	sort.SliceStable(a.Countries, func(i, j int) bool { return a.Countries[i].Name < a.Countries[j].Name })
	for i := range a.Countries {
		a.byCode[a.Countries[i].Code] = i
	}
	return a, nil
}

// identify picks the code and the name from a feature's properties (Natural Earth's are upper
// case; read either way). ISO_A2_EH is the alpha-2 with Natural Earth's own fixes (ISO_A2 is
// "-99" for France and Norway); a unit with no alpha-2 at all keeps its ADM0_A3.
func identify(props map[string]any) (code, name string) {
	get := func(keys ...string) string {
		for _, k := range keys {
			for pk, pv := range props {
				if strings.EqualFold(pk, k) {
					if s, ok := pv.(string); ok && s != "" && s != "-99" {
						return s
					}
				}
			}
		}
		return ""
	}
	code = get("ISO_A2_EH", "ISO_A2", "ADM0_A3", "ADM0_ISO")
	name = get("NAME", "ADMIN", "NAME_LONG")
	if name == "" {
		name = code
	}
	return strings.ToUpper(code), name
}

// polygon is one polygon's rings (the first outer, the rest holes), each as lon,lat pairs.
type polygon [][][2]float64

func polygons(typ string, raw json.RawMessage) ([]polygon, error) {
	switch typ {
	case "Polygon":
		var p polygon
		if err := json.Unmarshal(raw, &p); err != nil {
			return nil, err
		}
		return []polygon{p}, nil
	case "MultiPolygon":
		var ps []polygon
		if err := json.Unmarshal(raw, &ps); err != nil {
			return nil, err
		}
		return ps, nil
	case "":
		return nil, nil
	default:
		return nil, nil // points and lines are not a country
	}
}

// Rasterise is the fine and major cells a set of polygons overlaps.
func Rasterise(polys []polygon) (fine, major []int32) {
	cols, rows := roadtiles.Cols(0), roadtiles.Rows(0)
	deg := roadtiles.CellDeg(0)
	mask := make([]uint64, (cols*rows+63)/64)
	set := func(x, y int) {
		if x < 0 {
			x = 0
		}
		if x >= cols {
			x = cols - 1
		}
		if y < 0 {
			y = 0
		}
		if y >= rows {
			y = rows - 1
		}
		k := y*cols + x
		mask[k>>6] |= 1 << uint(k&63)
	}
	cellOf := func(lon, lat float64) (int, int) {
		return int(math.Floor((lon + 180) / deg)), int(math.Floor((lat + 90) / deg))
	}
	for _, p := range polys {
		// 1. centres inside: a scanline through each row's cell centres, even-odd over all rings
		crossings := map[int][]float64{}
		for _, ring := range p {
			n := len(ring)
			if n < 3 {
				continue
			}
			for i := 0; i < n; i++ {
				a, b := ring[i], ring[(i+1)%n]
				lo, hi := a, b
				if lo[1] > hi[1] {
					lo, hi = hi, lo
				}
				if lo[1] == hi[1] {
					continue
				}
				// rows whose centre latitude is in [lo.lat, hi.lat)
				y0 := int(math.Ceil((lo[1]+90)/deg - 0.5))
				y1 := int(math.Ceil((hi[1]+90)/deg-0.5)) - 1
				for y := y0; y <= y1; y++ {
					latc := (float64(y)+0.5)*deg - 90
					lon := lo[0] + (latc-lo[1])*(hi[0]-lo[0])/(hi[1]-lo[1])
					crossings[y] = append(crossings[y], lon)
				}
			}
		}
		for y, xs := range crossings {
			if y < 0 || y >= rows {
				continue
			}
			sort.Float64s(xs)
			for i := 0; i+1 < len(xs); i += 2 {
				x0 := int(math.Ceil((xs[i]+180)/deg - 0.5))
				x1 := int(math.Ceil((xs[i+1]+180)/deg-0.5)) - 1
				for x := x0; x <= x1; x++ {
					set(x, y)
				}
			}
		}
		// 2. the border's cells: every vertex, and every cell an edge passes through
		for _, ring := range p {
			n := len(ring)
			for i := 0; i < n; i++ {
				a, b := ring[i], ring[(i+1)%n]
				x, y := cellOf(a[0], a[1])
				set(x, y)
				steps := int(math.Ceil(math.Max(math.Abs(b[0]-a[0]), math.Abs(b[1]-a[1])) / (deg / 2)))
				for s := 1; s < steps; s++ {
					t := float64(s) / float64(steps)
					x, y := cellOf(a[0]+(b[0]-a[0])*t, a[1]+(b[1]-a[1])*t)
					set(x, y)
				}
			}
		}
	}
	// collect, and derive the major grid
	mcols := roadtiles.Cols(1)
	ratio := int(math.Round(roadtiles.CellDeg(1) / deg))
	majorSet := map[int32]bool{}
	for w, word := range mask {
		for word != 0 {
			bit := w*64 + bits.TrailingZeros64(word)
			word &= word - 1
			fine = append(fine, int32(bit))
			x, y := bit%cols, bit/cols
			majorSet[int32((y/ratio)*mcols+x/ratio)] = true
		}
	}
	major = make([]int32, 0, len(majorSet))
	for k := range majorSet {
		major = append(major, k)
	}
	sort.Slice(major, func(i, j int) bool { return major[i] < major[j] })
	return fine, major
}

// At is the country a point is in, by Natural Earth's polygons (even-odd over every ring, so a
// hole, Lesotho in South Africa, is the other country's); ok false at sea or outside every
// polygon. Enclaves list before the country around them when both claim a point, since a hole
// does not claim it: the first polygon that holds the point wins, which is the enclave's.
func (a *Atlas) At(lat, lon float64) (code, name string, ok bool) {
	if a == nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		return "", "", false
	}
	for i := range a.Countries {
		c := &a.Countries[i]
		for j, p := range c.polys {
			b := c.boxes[j]
			if lon < b[0] || lon > b[2] || lat < b[1] || lat > b[3] {
				continue
			}
			if inside(p, lon, lat) {
				return c.Code, c.Name, true
			}
		}
	}
	return "", "", false
}

// inside is the even-odd test over all of a polygon's rings (the outer and its holes).
func inside(p polygon, x, y float64) bool {
	in := false
	for _, ring := range p {
		n := len(ring)
		for i, j := 0, n-1; i < n; j, i = i, i+1 {
			xi, yi, xj, yj := ring[i][0], ring[i][1], ring[j][0], ring[j][1]
			if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
				in = !in
			}
		}
	}
	return in
}

func boxesOf(polys []polygon) [][4]float64 {
	out := make([][4]float64, len(polys))
	for i, p := range polys {
		b := [4]float64{180, 90, -180, -90}
		for _, ring := range p {
			for _, pt := range ring {
				b[0], b[1] = math.Min(b[0], pt[0]), math.Min(b[1], pt[1])
				b[2], b[3] = math.Max(b[2], pt[0]), math.Max(b[3], pt[1])
			}
		}
		out[i] = b
	}
	return out
}

func union(a, b []int32) []int32 {
	seen := map[int32]bool{}
	for _, v := range a {
		seen[v] = true
	}
	for _, v := range b {
		seen[v] = true
	}
	out := make([]int32, 0, len(seen))
	for v := range seen {
		out = append(out, v)
	}
	sort.Slice(out, func(i, j int) bool { return out[i] < out[j] })
	return out
}
