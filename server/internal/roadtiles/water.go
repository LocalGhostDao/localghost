package roadtiles

// THE WATER, in the same tiles as the roads. OpenStreetMap's inland water (lakes, reservoirs,
// the banks of wide rivers as areas; rivers, canals and streams as lines) is cut from the same PBF
// files in the same passes, into the same cells, as pieces with classes of their own (10 to 13),
// so a phone fetches one tile for a cell and has its streets and its water. An area clipped to a
// cell is a ring; the phone fills a tile's rings even-odd, so an island in a lake (an inner ring)
// is a hole without anyone saying so. Lakes in OSM are mostly multipolygon RELATIONS (their shore
// is many ways); those are read from the end of the file (osmpbf.ScanTail), their member ways
// spooled in the ways pass, and their rings stitched and cut after it.

import (
	"bufio"
	"encoding/binary"
	"math"
	"os"
	"sort"
)

// Water classes, drawn under the roads.
const (
	ClassWaterArea = 10 // a lake, a reservoir, a dock, a river's banks: a ring, filled
	ClassRiver     = 11
	ClassCanal     = 12
	ClassStream    = 13
)

// FlagHole marks an area ring that came from a relation's inner member (an island). The phone's
// even-odd fill needs no mark; it is here for a reader that wants to know.
const FlagHole = 1 << 4

// waterMinArea1 is the smallest area (deg²) that goes into the one-degree tiles: about a
// kilometre and a half across. Everything goes into the fine tiles.
const waterMinArea1 = 2e-4

// WaterLine is the class of a waterway drawn as a line, 0 for the values the map does not draw.
func WaterLine(tags map[string]string) int {
	switch tags["waterway"] {
	case "river":
		return ClassRiver
	case "canal":
		return ClassCanal
	case "stream":
		return ClassStream
	}
	return 0
}

// WaterArea says whether the tags make an area water: a closed way or a multipolygon relation.
func WaterArea(tags map[string]string) bool {
	if v, ok := tags["natural"]; ok && v == "water" {
		return true
	}
	switch tags["landuse"] {
	case "reservoir", "basin":
		return true
	}
	switch tags["waterway"] {
	case "riverbank", "dock":
		return true
	}
	return false
}

// waterRelation says whether a relation is a water multipolygon.
func waterRelation(tags map[string]string) bool {
	return tags["type"] == "multipolygon" && WaterArea(tags)
}

// closedWay says whether a way's refs come back to their start.
func closedWay(refs []int64) bool { return len(refs) >= 4 && refs[0] == refs[len(refs)-1] }

// ringArea is a ring's area in deg² (lon,lat pairs, not repeating the first point), unsigned.
func ringArea(r []float64) float64 {
	n := len(r) / 2
	if n < 3 {
		return 0
	}
	a := 0.0
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		a += r[2*i]*r[2*j+1] - r[2*j]*r[2*i+1]
	}
	return math.Abs(a) / 2
}

// clipRing is Sutherland–Hodgman: the ring clipped to the rectangle, as a ring (possibly empty).
func clipRing(r []float64, x0, y0, x1, y1 float64) []float64 {
	out := r
	for side := 0; side < 4 && len(out) >= 6; side++ {
		in := out
		out = make([]float64, 0, len(in)+8)
		n := len(in) / 2
		inside := func(x, y float64) bool {
			switch side {
			case 0:
				return x >= x0
			case 1:
				return x <= x1
			case 2:
				return y >= y0
			}
			return y <= y1
		}
		cross := func(ax, ay, bx, by float64) (float64, float64) {
			switch side {
			case 0, 1:
				v := x0
				if side == 1 {
					v = x1
				}
				t := (v - ax) / (bx - ax)
				return v, ay + t*(by-ay)
			}
			v := y0
			if side == 3 {
				v = y1
			}
			t := (v - ay) / (by - ay)
			return ax + t*(bx-ax), v
		}
		px, py := in[2*n-2], in[2*n-1]
		pin := inside(px, py)
		for i := 0; i < n; i++ {
			cx, cy := in[2*i], in[2*i+1]
			cin := inside(cx, cy)
			if cin {
				if !pin {
					x, y := cross(px, py, cx, cy)
					out = append(out, x, y)
				}
				out = append(out, cx, cy)
			} else if pin {
				x, y := cross(px, py, cx, cy)
				out = append(out, x, y)
			}
			px, py, pin = cx, cy, cin
		}
	}
	if len(out) < 6 {
		return nil
	}
	return out
}

// pointInRing is ray casting: whether (x, y) lies inside the ring.
func pointInRing(r []float64, x, y float64) bool {
	n := len(r) / 2
	in := false
	for i, j := 0, n-1; i < n; j, i = i, i+1 {
		xi, yi := r[2*i], r[2*i+1]
		xj, yj := r[2*j], r[2*j+1]
		if (yi > y) != (yj > y) && x < (xj-xi)*(y-yi)/(yj-yi)+xi {
			in = !in
		}
	}
	return in
}

// cutPoly cuts a ring (lon,lat pairs, the first point not repeated) into a level's cells: each
// cell the ring touches gets the clipped ring as a piece; a cell wholly inside the ring gets the
// whole cell as a four-point ring. Quantised points that coincide are dropped; a clipped sliver
// of fewer than three points is not a piece.
func cutPoly(level int, ring []float64, class, flags uint8, name string, cells *cellBuffers) error {
	n := len(ring) / 2
	if n < 3 {
		return nil
	}
	minX, minY, maxX, maxY := ring[0], ring[1], ring[0], ring[1]
	for i := 1; i < n; i++ {
		minX = math.Min(minX, ring[2*i])
		maxX = math.Max(maxX, ring[2*i])
		minY = math.Min(minY, ring[2*i+1])
		maxY = math.Max(maxY, ring[2*i+1])
	}
	c0 := CellAt(level, minX, minY)
	c1 := CellAt(level, maxX, maxY)
	deg := cellDeg[level]
	for cy := c0.Y; cy <= c1.Y; cy++ {
		for cx := c0.X; cx <= c1.X; cx++ {
			c := Cell{level, cx, cy}
			x0, y0 := c.Lon0(), c.Lat0()
			x1, y1 := x0+deg, y0+deg
			var clipped []float64
			if minX >= x0 && maxX <= x1 && minY >= y0 && maxY <= y1 {
				clipped = ring // wholly inside one cell
			} else {
				clipped = clipRing(ring, x0, y0, x1, y1)
				if clipped == nil {
					// no edge crosses the cell: inside the ring whole, or outside it
					if pointInRing(ring, x0+deg/2, y0+deg/2) {
						clipped = []float64{x0, y0, x1, y0, x1, y1, x0, y1}
					} else {
						continue
					}
				}
			}
			if ringArea(clipped) < 1e-12 {
				continue
			}
			run := make([]uint16, 0, len(clipped)+2)
			for i := 0; i < len(clipped); i += 2 {
				qx, qy := quant(c, clipped[i], clipped[i+1])
				if k := len(run); k >= 2 && run[k-2] == qx && run[k-1] == qy {
					continue
				}
				run = append(run, qx, qy)
			}
			if k := len(run); k >= 4 && run[0] == run[k-2] && run[1] == run[k-1] {
				run = run[:k-2]
			}
			if len(run) < 6 {
				continue
			}
			if err := cells.add(c, encodePiece(class, flags, name, run)); err != nil {
				return err
			}
		}
	}
	return nil
}

// --- the relations' member ways, spooled in the ways pass and read back after it ---

// memberSpool keeps the coordinates of the ways a water relation names, as they pass by in the
// ways pass, in one append-only file with an in-memory table of where each way starts.
type memberSpool struct {
	path string
	f    *os.File
	w    *bufio.Writer
	off  map[int64]int64
	pos  int64
}

func newMemberSpool(path string) (*memberSpool, error) {
	f, err := os.Create(path)
	if err != nil {
		return nil, err
	}
	return &memberSpool{path: path, f: f, w: bufio.NewWriterSize(f, 1<<20), off: map[int64]int64{}}, nil
}

// add spools one way: n uint32, then n × (lon, lat) float64.
func (m *memberSpool) add(id int64, coords []float64) error {
	m.off[id] = m.pos
	var b [8]byte
	binary.LittleEndian.PutUint32(b[:4], uint32(len(coords)/2))
	if _, err := m.w.Write(b[:4]); err != nil {
		return err
	}
	for _, v := range coords {
		binary.LittleEndian.PutUint64(b[:], math.Float64bits(v))
		if _, err := m.w.Write(b[:]); err != nil {
			return err
		}
	}
	m.pos += 4 + 8*int64(len(coords))
	return nil
}

func (m *memberSpool) finish() error { return m.w.Flush() }

// get reads one way's coordinates back; nil when the way was not in the file.
func (m *memberSpool) get(id int64) []float64 {
	off, ok := m.off[id]
	if !ok {
		return nil
	}
	var b [4]byte
	if _, err := m.f.ReadAt(b[:], off); err != nil {
		return nil
	}
	n := int(binary.LittleEndian.Uint32(b[:]))
	if n <= 0 || n > 1<<22 {
		return nil
	}
	buf := make([]byte, 16*n)
	if _, err := m.f.ReadAt(buf, off+4); err != nil {
		return nil
	}
	out := make([]float64, 2*n)
	for i := range out {
		out[i] = math.Float64frombits(binary.LittleEndian.Uint64(buf[8*i:]))
	}
	return out
}

func (m *memberSpool) close() {
	_ = m.f.Close()
	_ = os.Remove(m.path)
}

// waterRel is one water relation as the tail scan kept it.
type waterRel struct {
	name  string
	outer []int64
	inner []int64
}

// stitchRings joins member ways end to end into closed rings (lon,lat pairs, the first point not
// repeated). A way whose chain never closes is dropped. The ways' direction is whatever OSM has;
// a way is reversed when its end meets the chain.
func stitchRings(ways [][]float64) [][]float64 {
	type key struct{ x, y int64 }
	k := func(x, y float64) key { return key{int64(math.Round(x * 1e7)), int64(math.Round(y * 1e7))} }
	used := make([]bool, len(ways))
	// endpoints → ways
	ends := map[key][]int{}
	for i, w := range ways {
		if len(w) < 4 {
			used[i] = true
			continue
		}
		ends[k(w[0], w[1])] = append(ends[k(w[0], w[1])], i)
		ends[k(w[len(w)-2], w[len(w)-1])] = append(ends[k(w[len(w)-2], w[len(w)-1])], i)
	}
	var rings [][]float64
	for i := range ways {
		if used[i] {
			continue
		}
		used[i] = true
		ring := append([]float64{}, ways[i]...)
		start := k(ring[0], ring[1])
		for guard := 0; guard < len(ways)+1; guard++ {
			tail := k(ring[len(ring)-2], ring[len(ring)-1])
			if tail == start {
				break
			}
			next := -1
			for _, j := range ends[tail] {
				if !used[j] {
					next = j
					break
				}
			}
			if next < 0 {
				break
			}
			used[next] = true
			w := ways[next]
			if k(w[0], w[1]) != tail {
				// reverse it so its start meets the chain's tail
				rev := make([]float64, len(w))
				for p := 0; p < len(w); p += 2 {
					rev[len(w)-2-p], rev[len(w)-1-p] = w[p], w[p+1]
				}
				w = rev
			}
			ring = append(ring, w[2:]...)
		}
		if k(ring[len(ring)-2], ring[len(ring)-1]) == start && len(ring) >= 8 {
			rings = append(rings, ring[:len(ring)-2])
		}
	}
	return rings
}

// cutWaterRing emits an area ring at the fine level and, when it is big enough, the major one.
func cutWaterRing(ring []float64, flags uint8, name string, cells *cellBuffers) error {
	if err := cutPoly(0, ring, ClassWaterArea, flags, name, cells); err != nil {
		return err
	}
	if ringArea(ring) >= waterMinArea1 {
		return cutPoly(1, ring, ClassWaterArea, flags, name, cells)
	}
	return nil
}

// sortedKeys is a map's keys in order (so a build is the same bytes twice).
func sortedKeys(m map[int64]*waterRel) []int64 {
	out := make([]int64, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Slice(out, func(a, b int) bool { return out[a] < out[b] })
	return out
}
