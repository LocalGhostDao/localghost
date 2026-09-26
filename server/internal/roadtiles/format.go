// Package roadtiles cuts OpenStreetMap's roads into tiles the phone fetches only for what it is
// looking at, the way internal/landtiles does for the coast. The source is the planet, or
// Geofabrik's continent extracts (ODbL, "© OpenStreetMap contributors"), as PBF; the reader is
// internal/osmpbf. The world's roads are tens of gigabytes of vectors and must never reach a phone
// whole, so:
//
//   - MAJOR roads (motorway, trunk, primary, secondary) go into one-degree cells, the coast's grid:
//     what a screen 2.5° across should show , the motorways of a country, not its lanes;
//   - EVERY road, with its name, goes into 0.1-degree cells (a 3600×1800 grid): what a screen a
//     few kilometres across should show, streets included;
//   - the index says which cells of each grid have a tile, so the phone never asks for the sea.
//
// A way is clipped to the cells it crosses; a piece is its points quantised to 1/65535 of the
// cell's span (under two metres in a 1° cell, twenty centimetres in a 0.1° cell), four bytes a
// vertex, with its class, a one-way flag and, in the fine tiles, its name. Pure Go, the standard
// library only.
package roadtiles

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// Grids: level 1 is the coast's one-degree grid, level 0 the tenth-of-a-degree grid.
const (
	Levels = 2

	Q = 65535 // quantisation steps across a cell

	tileMagic  = 0x31524C47 // "GLR1" little-endian bytes: G L R 1
	indexMagic = 0x31584C47 // "GLX1"
)

// Grid geometry per level.
var (
	cellDeg = [Levels]float64{0.1, 1}
	cols    = [Levels]int{3600, 360}
	rows    = [Levels]int{1800, 180}
)

// CellDeg, Cols, Rows are the grid's geometry for a level.
func CellDeg(level int) float64 { return cellDeg[level] }
func Cols(level int) int        { return cols[level] }
func Rows(level int) int        { return rows[level] }

// Cell is a cell of one grid: X = floor((lon+180)/deg), Y = floor((lat+90)/deg).
type Cell struct {
	Level int
	X, Y  int
}

// Key is the cell's position in its level's index.
func (c Cell) Key() int { return c.Y*cols[c.Level] + c.X }

// Lon0/Lat0 are the cell's south-west corner.
func (c Cell) Lon0() float64 { return float64(c.X)*cellDeg[c.Level] - 180 }
func (c Cell) Lat0() float64 { return float64(c.Y)*cellDeg[c.Level] - 90 }

// CellAt is the cell of a point at a level.
func CellAt(level int, lon, lat float64) Cell {
	x := int((lon + 180) / cellDeg[level])
	y := int((lat + 90) / cellDeg[level])
	if x < 0 {
		x = 0
	}
	if x >= cols[level] {
		x = cols[level] - 1
	}
	if y < 0 {
		y = 0
	}
	if y >= rows[level] {
		y = rows[level] - 1
	}
	return Cell{level, x, y}
}

// TileName is the file a cell's tile is stored in: <level>/<x>_<y>.lgr.
func TileName(c Cell) string {
	if c.Level == 1 {
		return fmt.Sprintf("1/%03d_%03d.lgr", c.X, c.Y)
	}
	return fmt.Sprintf("0/%04d_%04d.lgr", c.X, c.Y)
}

// Road classes, in drawing order (drawn last = on top). The phone picks widths and colours by
// class and which classes to draw by zoom.
const (
	ClassMotorway    = 1
	ClassTrunk       = 2
	ClassPrimary     = 3
	ClassSecondary   = 4
	ClassTertiary    = 5
	ClassResidential = 6 // residential, unclassified, living_street, road
	ClassService     = 7
	ClassTrack       = 8
	ClassPath        = 9 // path, footway, cycleway, bridleway, steps, pedestrian
)

// MajorMax is the highest class that goes into the one-degree tiles.
const MajorMax = ClassSecondary

// Class maps an OSM highway value to a class, 0 for the values the map does not draw.
func Class(highway string) int {
	switch highway {
	case "motorway", "motorway_link":
		return ClassMotorway
	case "trunk", "trunk_link":
		return ClassTrunk
	case "primary", "primary_link":
		return ClassPrimary
	case "secondary", "secondary_link":
		return ClassSecondary
	case "tertiary", "tertiary_link":
		return ClassTertiary
	case "residential", "unclassified", "living_street", "road":
		return ClassResidential
	case "service":
		return ClassService
	case "track":
		return ClassTrack
	case "path", "footway", "cycleway", "bridleway", "steps", "pedestrian":
		return ClassPath
	}
	return 0
}

// Way flags.
const (
	FlagOneway = 1 << 0
	FlagNamed  = 1 << 1
	FlagTunnel = 1 << 2
	FlagBridge = 1 << 3
)

// Piece is one road (or the part of it inside a cell) as stored in a tile.
type Piece struct {
	Class  uint8
	Flags  uint8
	Name   string
	Points []uint16 // x,y pairs, quantised within the cell
}

// Tile is one cell's roads.
type Tile struct {
	Cell   Cell
	Pieces []Piece
}

// Points is the tile's vertex count.
func (t Tile) Points() int {
	n := 0
	for _, p := range t.Pieces {
		n += len(p.Points) / 2
	}
	return n
}

// Encode is the tile's wire form: magic u32, level u8, x u16, y u16, count u32; per piece: class u8,
// flags u8, name (u16 length + UTF-8, present when FlagNamed), n u16, n×(u16 x, u16 y). Little-endian.
func (t Tile) Encode() []byte {
	var b bytes.Buffer
	b.Grow(16 + 8*len(t.Pieces) + 4*t.Points())
	le := binary.LittleEndian
	var hdr [13]byte
	le.PutUint32(hdr[0:], tileMagic)
	hdr[4] = byte(t.Cell.Level)
	le.PutUint16(hdr[5:], uint16(t.Cell.X))
	le.PutUint16(hdr[7:], uint16(t.Cell.Y))
	le.PutUint32(hdr[9:], uint32(len(t.Pieces)))
	b.Write(hdr[:])
	var u2 [2]byte
	for _, p := range t.Pieces {
		flags := p.Flags &^ FlagNamed
		name := p.Name
		if len(name) > 255 {
			name = name[:255]
		}
		if name != "" {
			flags |= FlagNamed
		}
		b.WriteByte(p.Class)
		b.WriteByte(flags)
		if name != "" {
			le.PutUint16(u2[:], uint16(len(name)))
			b.Write(u2[:])
			b.WriteString(name)
		}
		n := len(p.Points) / 2
		if n > 65535 {
			n = 65535
		}
		le.PutUint16(u2[:], uint16(n))
		b.Write(u2[:])
		buf := make([]byte, 4*n)
		for i := 0; i < 2*n; i++ {
			le.PutUint16(buf[2*i:], p.Points[i])
		}
		b.Write(buf)
	}
	return b.Bytes()
}

// Decode reads a tile back (the phone's parser follows the same layout; this one is for tests and
// the cutter's own checks).
func Decode(p []byte) (Tile, error) {
	le := binary.LittleEndian
	if len(p) < 13 || le.Uint32(p) != tileMagic {
		return Tile{}, errors.New("not a road tile")
	}
	t := Tile{Cell: Cell{int(p[4]), int(le.Uint16(p[5:])), int(le.Uint16(p[7:]))}}
	if t.Cell.Level < 0 || t.Cell.Level >= Levels {
		return t, errors.New("bad level")
	}
	count := int(le.Uint32(p[9:]))
	off := 13
	for i := 0; i < count; i++ {
		if off+2 > len(p) {
			return t, errors.New("short tile")
		}
		pc := Piece{Class: p[off], Flags: p[off+1]}
		off += 2
		if pc.Flags&FlagNamed != 0 {
			if off+2 > len(p) {
				return t, errors.New("short name")
			}
			n := int(le.Uint16(p[off:]))
			off += 2
			if off+n > len(p) {
				return t, errors.New("short name")
			}
			pc.Name = string(p[off : off+n])
			off += n
		}
		if off+2 > len(p) {
			return t, errors.New("short piece")
		}
		n := int(le.Uint16(p[off:]))
		off += 2
		if off+4*n > len(p) {
			return t, errors.New("short points")
		}
		pc.Points = make([]uint16, 2*n)
		for k := range pc.Points {
			pc.Points[k] = le.Uint16(p[off+2*k:])
		}
		off += 4 * n
		t.Pieces = append(t.Pieces, pc)
	}
	return t, nil
}

// Index is which cells have a tile, per level: level 1 as one byte per cell (0/1), level 0 as a
// bitmap (one bit per cell, 810,000 bytes). Wire: magic u32, then the level-1 bytes (64,800), then
// the level-0 bitmap.
type Index struct {
	Major []byte // 64,800 bytes, 1 where a level-1 tile exists
	Fine  []byte // 6,480,000 bits
}

// NewIndex is an empty index.
func NewIndex() *Index {
	return &Index{Major: make([]byte, cols[1]*rows[1]), Fine: make([]byte, cols[0]*rows[0]/8)}
}

// Set marks a cell as having a tile.
func (ix *Index) Set(c Cell) {
	if c.Level == 1 {
		ix.Major[c.Key()] = 1
		return
	}
	k := c.Key()
	ix.Fine[k>>3] |= 1 << uint(k&7)
}

// Has reports whether a cell has a tile.
func (ix *Index) Has(c Cell) bool {
	if c.Level == 1 {
		return ix.Major[c.Key()] != 0
	}
	k := c.Key()
	return ix.Fine[k>>3]&(1<<uint(k&7)) != 0
}

// Encode is the index's wire form.
func (ix *Index) Encode() []byte {
	out := make([]byte, 4+len(ix.Major)+len(ix.Fine))
	binary.LittleEndian.PutUint32(out, indexMagic)
	copy(out[4:], ix.Major)
	copy(out[4+len(ix.Major):], ix.Fine)
	return out
}

// DecodeIndex reads an index back.
func DecodeIndex(b []byte) (*Index, error) {
	ix := NewIndex()
	if len(b) != 4+len(ix.Major)+len(ix.Fine) || binary.LittleEndian.Uint32(b) != indexMagic {
		return nil, errors.New("not a road tile index")
	}
	copy(ix.Major, b[4:])
	copy(ix.Fine, b[4+len(ix.Major):])
	return ix, nil
}

// Count is how many tiles each level has.
func (ix *Index) Count() (major, fine int) {
	for _, v := range ix.Major {
		if v != 0 {
			major++
		}
	}
	for _, v := range ix.Fine {
		for v != 0 {
			fine += int(v & 1)
			v >>= 1
		}
	}
	return
}

// cleanName is a way's name as stored: trimmed, one line, at most 255 bytes cut on a rune.
func cleanName(s string) string {
	s = strings.TrimSpace(strings.ReplaceAll(s, "\n", " "))
	if len(s) <= 255 {
		return s
	}
	n := 255
	for n > 0 && s[n]&0xC0 == 0x80 {
		n--
	}
	return s[:n]
}
