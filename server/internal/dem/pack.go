package dem

// A PACK OF TILES. The world's GLO-90 is some 26,000 GeoTIFFs, one a degree square, and a set of
// that many files costs more than its bytes: the mirror's manifest lists every one (megabytes the
// phone and every box read), a fetch is thousands of requests, a folder of them is slow to list.
// A pack is the tiles of one 30-degree block in one file, each tile's bytes as they were (the
// GeoTIFF reader reads a tile straight out of the pack, through a section of it), with an index
// at the front saying which corner lies at which offset. The mirror's set elevation carries packs
// (GLO-90_N30_W030.heights is 30 to 60 north, 30 west to 0), a box fetches the blocks that cover
// the part of the world it asked for, and a folder may hold packs and loose tiles together.
//
// The format, little-endian, made by ghost-heights pack and read by Open:
//
//	"LGHEIGHTS1\n" and five zero bytes          16 bytes
//	count                                       uint32
//	count index entries:
//	    lat, lon of the south-west corner       int16, int16
//	    offset, length of the tile's bytes      uint64, uint64
//	    name length, name                       uint16, bytes (the tile's file name)
//	the tiles' bytes, in index order
//
// The same tiles in the same order give the same bytes (no times, no owners), so a pack built
// again from the same source matches the manifest.

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

const packMagic = "LGHEIGHTS1\n\x00\x00\x00\x00\x00"

// PackBlock is the side of a pack's block in degrees.
const PackBlock = 30

// PackEntry is one tile of a pack.
type PackEntry struct {
	Lat, Lon int
	Off, Len int64
	Name     string
}

// maxPackEntries bounds an index read from disk (a corrupt header must not ask for the moon).
const maxPackEntries = 1 << 20

// ReadPackIndex reads a pack's index.
func ReadPackIndex(r io.ReaderAt, size int64) ([]PackEntry, error) {
	var h [20]byte
	if _, err := r.ReadAt(h[:], 0); err != nil {
		return nil, fmt.Errorf("pack: header: %w", err)
	}
	if string(h[:16]) != packMagic {
		return nil, errors.New("pack: not a heights pack")
	}
	n := binary.LittleEndian.Uint32(h[16:])
	if n > maxPackEntries {
		return nil, fmt.Errorf("pack: %d entries", n)
	}
	br := io.NewSectionReader(r, 20, size-20)
	out := make([]PackEntry, 0, n)
	var fixed [22]byte
	for i := uint32(0); i < n; i++ {
		if _, err := io.ReadFull(br, fixed[:]); err != nil {
			return nil, fmt.Errorf("pack: index: %w", err)
		}
		e := PackEntry{
			Lat: int(int16(binary.LittleEndian.Uint16(fixed[0:]))),
			Lon: int(int16(binary.LittleEndian.Uint16(fixed[2:]))),
			Off: int64(binary.LittleEndian.Uint64(fixed[4:])),
			Len: int64(binary.LittleEndian.Uint64(fixed[12:])),
		}
		nl := int(binary.LittleEndian.Uint16(fixed[20:]))
		name := make([]byte, nl)
		if _, err := io.ReadFull(br, name); err != nil {
			return nil, fmt.Errorf("pack: index: %w", err)
		}
		e.Name = string(name)
		if e.Off < 0 || e.Len <= 0 || e.Off+e.Len > size {
			return nil, fmt.Errorf("pack: %s lies outside the file", e.Name)
		}
		out = append(out, e)
	}
	return out, nil
}

// WritePack writes the tiles named (GeoTIFF files, their corners from their names) as one pack to
// w, in corner order. A file whose name gives no corner is an error; two files of one corner too.
func WritePack(w io.Writer, paths []string) (int, error) {
	type src struct {
		path string
		e    PackEntry
	}
	var tiles []src
	seen := map[[2]int]string{}
	for _, p := range paths {
		name := filepath.Base(p)
		lat, lon, ok := Corner(name)
		if !ok {
			return 0, fmt.Errorf("%s: no corner in the name", name)
		}
		if other, dup := seen[[2]int{lat, lon}]; dup {
			return 0, fmt.Errorf("%s and %s are the same corner", other, name)
		}
		seen[[2]int{lat, lon}] = name
		st, err := os.Stat(p)
		if err != nil {
			return 0, err
		}
		if st.Size() == 0 {
			return 0, fmt.Errorf("%s is empty", name)
		}
		if len(name) > 65535 {
			return 0, fmt.Errorf("%s: a name too long", name)
		}
		tiles = append(tiles, src{p, PackEntry{Lat: lat, Lon: lon, Len: st.Size(), Name: name}})
	}
	sort.Slice(tiles, func(i, j int) bool {
		if tiles[i].e.Lat != tiles[j].e.Lat {
			return tiles[i].e.Lat < tiles[j].e.Lat
		}
		return tiles[i].e.Lon < tiles[j].e.Lon
	})
	// the index's size decides where the bytes start
	off := int64(20)
	for _, t := range tiles {
		off += 22 + int64(len(t.e.Name))
	}
	for i := range tiles {
		tiles[i].e.Off = off
		off += tiles[i].e.Len
	}
	bw := make([]byte, 0, 64)
	bw = append(bw, packMagic...)
	bw = binary.LittleEndian.AppendUint32(bw, uint32(len(tiles)))
	for _, t := range tiles {
		bw = binary.LittleEndian.AppendUint16(bw, uint16(int16(t.e.Lat)))
		bw = binary.LittleEndian.AppendUint16(bw, uint16(int16(t.e.Lon)))
		bw = binary.LittleEndian.AppendUint64(bw, uint64(t.e.Off))
		bw = binary.LittleEndian.AppendUint64(bw, uint64(t.e.Len))
		bw = binary.LittleEndian.AppendUint16(bw, uint16(len(t.e.Name)))
		bw = append(bw, t.e.Name...)
	}
	if _, err := w.Write(bw); err != nil {
		return 0, err
	}
	for _, t := range tiles {
		f, err := os.Open(t.path)
		if err != nil {
			return 0, err
		}
		n, err := io.Copy(w, io.LimitReader(f, t.e.Len))
		f.Close()
		if err != nil {
			return 0, err
		}
		if n != t.e.Len {
			return 0, fmt.Errorf("%s changed while packing", t.e.Name)
		}
	}
	return len(tiles), nil
}

// BlockOf is the south-west corner of the 30-degree block a tile's corner lies in.
func BlockOf(lat, lon int) (int, int) {
	return floorDiv(lat, PackBlock) * PackBlock, floorDiv(lon, PackBlock) * PackBlock
}

func floorDiv(a, b int) int {
	q := a / b
	if a%b != 0 && a < 0 {
		q--
	}
	return q
}

// PackName is a block's pack file name: GLO-90_N30_W030.heights for 30 north, 30 west.
func PackName(blockLat, blockLon int) string {
	ns, ew := "N", "E"
	if blockLat < 0 {
		ns, blockLat = "S", -blockLat
	}
	if blockLon < 0 {
		ew, blockLon = "W", -blockLon
	}
	return fmt.Sprintf("GLO-90_%s%02d_%s%03d.heights", ns, blockLat, ew, blockLon)
}

// PackCorner is the block a pack's name covers (its south-west corner, PackBlock degrees each
// way); ok is false for another file.
func PackCorner(name string) (lat, lon int, ok bool) {
	if !strings.HasSuffix(name, ".heights") {
		return 0, 0, false
	}
	return Corner(strings.TrimSuffix(name, ".heights") + "_")
}
