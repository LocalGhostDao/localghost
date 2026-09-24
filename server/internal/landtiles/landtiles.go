// Package landtiles cuts a high-resolution land-polygon dataset into one-degree tiles the phone
// fetches only for what it is looking at. The map's base is Natural Earth's 10m countries: right
// for a continent, a smudge for an island , Paxos is a handful of vertices. OpenStreetMap's land
// polygons (osmdata.openstreetmap.de, ODbL, "© OpenStreetMap contributors") draw every cove, but
// the world at that resolution is tens of millions of points, which is exactly what must not go
// to a phone whole. So the box cuts it once into a 360×180 grid of one-degree cells:
//
//   - a cell with no land is water and has no file;
//   - a cell entirely covered by land has no file either, just a mark in the index;
//   - every other cell (a coast runs through it) gets a small binary tile: its land rings clipped
//     to the cell, each vertex quantised to 1/65535 of a degree , under two metres , as two
//     uint16s, four bytes a vertex.
//
// The index is 64,800 bytes, one per cell (0 water, 1 coast, 2 land), so the phone knows before it
// asks which cells to fetch, which to paint solid, and which to leave as sea. All artificial edges
// the cutting creates lie exactly on cell borders (the cutter only ever splits on whole degrees),
// so the phone strokes a coastline by skipping the edges that run along a border; the fill needs
// no such care. Pure Go, the standard library only: the shapefile is read by hand.
package landtiles

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

const (
	Cols = 360
	Rows = 180
	Q    = 65535 // quantisation steps per degree

	Water = 0
	Coast = 1
	Land  = 2

	tileMagic  = 0x4C475431 // "LGT1"
	indexMagic = 0x4C474931 // "LGI1"

	fullFrac  = 0.99999 // a cell this covered by land is land
	emptyArea = 1e-12   // a ring with less area than this (deg²) is a clipping sliver
)

// ring is a closed polygon ring as interleaved x,y (lon,lat) pairs, not repeating the first point.
type ring []float64

// Cell is the index of a one-degree cell: X = floor(lon)+180, Y = floor(lat)+90.
type Cell struct{ X, Y int }

// Key is the cell's position in the index.
func (c Cell) Key() int { return c.Y*Cols + c.X }

// Lon0/Lat0 are the cell's south-west corner in degrees.
func (c Cell) Lon0() float64 { return float64(c.X - 180) }
func (c Cell) Lat0() float64 { return float64(c.Y - 90) }

func (r ring) n() int { return len(r) / 2 }

func (r ring) bbox() (minX, minY, maxX, maxY float64) {
	minX, minY = math.Inf(1), math.Inf(1)
	maxX, maxY = math.Inf(-1), math.Inf(-1)
	for i := 0; i < len(r); i += 2 {
		x, y := r[i], r[i+1]
		minX, maxX = math.Min(minX, x), math.Max(maxX, x)
		minY, maxY = math.Min(minY, y), math.Max(maxY, y)
	}
	return
}

// area is the signed shoelace area in square degrees (positive counter-clockwise, x right, y up).
func (r ring) area() float64 {
	n := r.n()
	if n < 3 {
		return 0
	}
	s := 0.0
	for i := 0; i < n; i++ {
		j := (i + 1) % n
		s += r[2*i]*r[2*j+1] - r[2*j]*r[2*i+1]
	}
	return s / 2
}

// clipHalf keeps the part of r on one side of the line axis = v (axis 0: x, 1: y), keeping
// coordinates <= v when less, >= v otherwise. One Sutherland–Hodgman pass; the ring's orientation
// is preserved and the new edges lie exactly on the line.
func clipHalf(r ring, axis int, v float64, less bool) ring {
	n := r.n()
	if n == 0 {
		return nil
	}
	inside := func(i int) bool {
		c := r[2*i+axis]
		if less {
			return c <= v
		}
		return c >= v
	}
	out := make(ring, 0, len(r)+8)
	cross := func(i, j int) (float64, float64) {
		xi, yi, xj, yj := r[2*i], r[2*i+1], r[2*j], r[2*j+1]
		if axis == 0 {
			t := (v - xi) / (xj - xi)
			return v, yi + t*(yj-yi)
		}
		t := (v - yi) / (yj - yi)
		return xi + t*(xj-xi), v
	}
	prev := n - 1
	prevIn := inside(prev)
	for i := 0; i < n; i++ {
		curIn := inside(i)
		switch {
		case curIn && prevIn:
			out = append(out, r[2*i], r[2*i+1])
		case curIn && !prevIn:
			x, y := cross(prev, i)
			out = append(out, x, y, r[2*i], r[2*i+1])
		case !curIn && prevIn:
			x, y := cross(prev, i)
			out = append(out, x, y)
		}
		prev, prevIn = i, curIn
	}
	if len(out) < 6 {
		return nil
	}
	return out
}

// cellSpan is the range of whole-degree cells a coordinate interval covers: a ring that only
// touches the next cell's edge (max exactly on an integer) does not belong to it.
func cellSpan(lo, hi float64) (int, int) {
	a := int(math.Floor(lo))
	b := int(math.Ceil(hi)) - 1
	if b < a {
		b = a
	}
	return a, b
}

// split cuts r into one-degree cells by recursive bisection on whole degrees and hands each piece
// to emit with its cell. Every cut is on an integer line, so every artificial edge lies on a cell
// border. A ring inside one cell is emitted as is; the recursion clips only what straddles.
func split(r ring, emit func(Cell, ring)) {
	if r.n() < 3 {
		return
	}
	minX, minY, maxX, maxY := r.bbox()
	x0, x1 := cellSpan(minX, maxX)
	y0, y1 := cellSpan(minY, maxY)
	if x0 == x1 && y0 == y1 {
		if x0 < -180 || x0 >= 180 || y0 < -90 || y0 >= 90 {
			return
		}
		emit(Cell{x0 + 180, y0 + 90}, r)
		return
	}
	if x1-x0 >= y1-y0 {
		mid := float64((x0 + x1 + 1) / 2)
		if l := clipHalf(r, 0, mid, true); l != nil {
			split(l, emit)
		}
		if h := clipHalf(r, 0, mid, false); h != nil {
			split(h, emit)
		}
		return
	}
	mid := float64((y0 + y1 + 1) / 2)
	if l := clipHalf(r, 1, mid, true); l != nil {
		split(l, emit)
	}
	if h := clipHalf(r, 1, mid, false); h != nil {
		split(h, emit)
	}
}

// quantize turns a ring clipped to cell c into uint16 pairs relative to the cell's corner, dropping
// repeated points; nil when fewer than three remain.
func quantize(r ring, c Cell) []uint16 {
	lon0, lat0 := c.Lon0(), c.Lat0()
	q := func(v float64) uint16 {
		f := math.Round(v * Q)
		if f < 0 {
			return 0
		}
		if f > Q {
			return Q
		}
		return uint16(f)
	}
	out := make([]uint16, 0, len(r))
	for i := 0; i < len(r); i += 2 {
		x, y := q(r[i]-lon0), q(r[i+1]-lat0)
		if n := len(out); n >= 2 && out[n-2] == x && out[n-1] == y {
			continue
		}
		out = append(out, x, y)
	}
	if n := len(out); n >= 4 && out[0] == out[n-2] && out[1] == out[n-1] {
		out = out[:n-2]
	}
	if len(out) < 6 {
		return nil
	}
	return out
}

// Tile is one coast cell: its rings, quantised.
type Tile struct {
	Cell  Cell
	Rings [][]uint16
}

// Points is the tile's vertex count.
func (t Tile) Points() int {
	n := 0
	for _, r := range t.Rings {
		n += len(r) / 2
	}
	return n
}

// Encode is the tile's wire form: magic, x, y, ring count, then per ring its vertex count and the
// vertices as little-endian uint16 pairs.
func (t Tile) Encode() []byte {
	var b bytes.Buffer
	b.Grow(16 + 4*t.Points() + 4*len(t.Rings))
	le := binary.LittleEndian
	var hdr [12]byte
	le.PutUint32(hdr[0:], tileMagic)
	le.PutUint16(hdr[4:], uint16(t.Cell.X))
	le.PutUint16(hdr[6:], uint16(t.Cell.Y))
	le.PutUint32(hdr[8:], uint32(len(t.Rings)))
	b.Write(hdr[:])
	var n4 [4]byte
	for _, r := range t.Rings {
		le.PutUint32(n4[:], uint32(len(r)/2))
		b.Write(n4[:])
		buf := make([]byte, 2*len(r))
		for i, v := range r {
			le.PutUint16(buf[2*i:], v)
		}
		b.Write(buf)
	}
	return b.Bytes()
}

// Decode reads a tile back (the phone's parser follows the same layout; this one is for tests).
func Decode(p []byte) (Tile, error) {
	le := binary.LittleEndian
	if len(p) < 12 || le.Uint32(p) != tileMagic {
		return Tile{}, errors.New("not a land tile")
	}
	t := Tile{Cell: Cell{int(le.Uint16(p[4:])), int(le.Uint16(p[6:]))}}
	count := int(le.Uint32(p[8:]))
	off := 12
	for i := 0; i < count; i++ {
		if off+4 > len(p) {
			return t, errors.New("short tile")
		}
		n := int(le.Uint32(p[off:]))
		off += 4
		if n < 0 || off+4*n > len(p) {
			return t, errors.New("short ring")
		}
		r := make([]uint16, 2*n)
		for k := range r {
			r[k] = le.Uint16(p[off+2*k:])
		}
		off += 4 * n
		t.Rings = append(t.Rings, r)
	}
	return t, nil
}

// EncodeIndex is the 64,800-byte index behind its magic.
func EncodeIndex(idx []byte) []byte {
	out := make([]byte, 4+len(idx))
	binary.LittleEndian.PutUint32(out, indexMagic)
	copy(out[4:], idx)
	return out
}

// TileName is the file a coast cell is stored in.
func TileName(c Cell) string { return fmt.Sprintf("%03d_%03d.lgt", c.X, c.Y) }
