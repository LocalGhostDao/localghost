package osmpbf

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
)

// A small encoder, enough to write the files the reader must read (tests, fixtures).

type enc struct{ b []byte }

func (e *enc) varint(v uint64) {
	for v >= 0x80 {
		e.b = append(e.b, byte(v)|0x80)
		v >>= 7
	}
	e.b = append(e.b, byte(v))
}
func (e *enc) tag(num, wire int) { e.varint(uint64(num<<3 | wire)) }
func (e *enc) bytesField(num int, b []byte) {
	e.tag(num, 2)
	e.varint(uint64(len(b)))
	e.b = append(e.b, b...)
}
func (e *enc) uintField(num int, v uint64) { e.tag(num, 0); e.varint(v) }
func (e *enc) packed(num int, vs []uint64) {
	var p enc
	for _, v := range vs {
		p.varint(v)
	}
	e.bytesField(num, p.b)
}
func zz(v int64) uint64 { return uint64((v << 1) ^ (v >> 63)) }

// TestNode and TestWay are the encoder's inputs (coordinates in degrees).
type TestNode struct {
	ID       int64
	Lat, Lon float64
}
type TestWay struct {
	ID   int64
	Tags map[string]string
	Refs []int64
}

// EncBlock is one block to encode: nodes (written as DenseNodes) and ways.
type EncBlock struct {
	Nodes []TestNode
	Ways  []TestWay
}

// Encode writes a PBF with one OSMHeader and the given blocks, zlib-compressed like the real thing.
// For tests and fixtures, not a general writer (no relations, no metadata).
func Encode(blocks ...EncBlock) []byte {
	var out bytes.Buffer
	writeBlob := func(typ string, payload []byte) {
		var z bytes.Buffer
		zw := zlib.NewWriter(&z)
		zw.Write(payload)
		zw.Close()
		var blob enc
		blob.uintField(2, uint64(len(payload)))
		blob.bytesField(3, z.Bytes())
		var hdr enc
		hdr.bytesField(1, []byte(typ))
		hdr.uintField(3, uint64(len(blob.b)))
		var l [4]byte
		binary.BigEndian.PutUint32(l[:], uint32(len(hdr.b)))
		out.Write(l[:])
		out.Write(hdr.b)
		out.Write(blob.b)
	}
	var h enc
	h.bytesField(4, []byte("OsmSchema-V0.6"))
	h.bytesField(4, []byte("DenseNodes"))
	h.bytesField(16, []byte("osmpbf_test"))
	writeBlob("OSMHeader", h.b)
	for _, bl := range blocks {
		// string table: index 0 is the empty string by convention
		strs := []string{""}
		idx := map[string]uint64{"": 0}
		str := func(s string) uint64 {
			if i, ok := idx[s]; ok {
				return i
			}
			idx[s] = uint64(len(strs))
			strs = append(strs, s)
			return idx[s]
		}
		var groups []enc
		if len(bl.Nodes) > 0 {
			var ids, lats, lons []uint64
			var pid, plat, plon int64
			for _, n := range bl.Nodes {
				lat := int64(n.Lat * 1e7) // granularity 100 → nanodegrees / 100
				lon := int64(n.Lon * 1e7)
				ids = append(ids, zz(n.ID-pid))
				lats = append(lats, zz(lat-plat))
				lons = append(lons, zz(lon-plon))
				pid, plat, plon = n.ID, lat, lon
			}
			var dense enc
			dense.packed(1, ids)
			dense.packed(8, lats)
			dense.packed(9, lons)
			var g enc
			g.bytesField(2, dense.b)
			groups = append(groups, g)
		}
		if len(bl.Ways) > 0 {
			var g enc
			for _, w := range bl.Ways {
				var we enc
				we.uintField(1, uint64(w.ID))
				var ks, vs []uint64
				for k, v := range w.Tags {
					ks = append(ks, str(k))
					vs = append(vs, str(v))
				}
				we.packed(2, ks)
				we.packed(3, vs)
				var refs []uint64
				var prev int64
				for _, r := range w.Refs {
					refs = append(refs, zz(r-prev))
					prev = r
				}
				we.packed(8, refs)
				g.bytesField(3, we.b)
			}
			groups = append(groups, g)
		}
		var st enc
		for _, s := range strs {
			st.bytesField(1, []byte(s))
		}
		var pb enc
		pb.bytesField(1, st.b)
		for _, g := range groups {
			pb.bytesField(2, g.b)
		}
		pb.uintField(17, 100)
		writeBlob("OSMData", pb.b)
	}
	return out.Bytes()
}
