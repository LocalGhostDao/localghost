// Package osmpbf reads OpenStreetMap's PBF files , the planet, or Geofabrik's continent and
// country extracts , with the standard library alone. The format is small: a sequence of blobs,
// each a 4-byte big-endian length, a BlobHeader (protobuf: type, datasize) and a Blob (protobuf:
// the bytes, raw or zlib), and inside an OSMData blob a PrimitiveBlock (protobuf: a string table
// and groups of dense nodes, ways and relations, with delta-coded packed integers). A protobuf
// decoder for the six wire types this needs is a hundred lines; encoding/binary and compress/zlib
// do the rest.
//
// What the road cutter needs is what is decoded: every dense node's id and coordinates (tags are
// skipped), every way's id, tags and node refs. Relations are skipped. Blocks are decompressed and
// parsed in parallel and handed to the caller in file order, which is what a two-pass pipeline over
// a sorted file relies on.
package osmpbf

import (
	"bytes"
	"compress/zlib"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"runtime"
	"sync"
)

// Way is one OSM way: its tags (already resolved through the block's string table) and node refs.
type Way struct {
	ID   int64
	Tags map[string]string
	Refs []int64
}

// Block is one PrimitiveBlock's nodes and ways. Node coordinates are in nanodegrees, as OSM stores
// them (lat/lon × 1e9); NodeIDs, NodeLat and NodeLon run in parallel.
type Block struct {
	NodeIDs []int64
	NodeLat []int64
	NodeLon []int64
	Ways    []Way
}

// Header is what the OSMHeader blob says about the file.
type Header struct {
	RequiredFeatures []string
	OptionalFeatures []string
	WritingProgram   string
	Source           string
}

// ErrFeature is returned when the file needs something this reader does not do (a compression
// other than zlib, a required feature it does not know).
var ErrFeature = errors.New("osmpbf: unsupported")

// --- protobuf wire ---

type reader struct {
	b   []byte
	pos int
	err error
}

func (r *reader) varint() uint64 {
	var v uint64
	var shift uint
	for {
		if r.pos >= len(r.b) {
			r.err = io.ErrUnexpectedEOF
			return 0
		}
		c := r.b[r.pos]
		r.pos++
		v |= uint64(c&0x7f) << shift
		if c < 0x80 {
			return v
		}
		shift += 7
		if shift > 63 {
			r.err = errors.New("osmpbf: varint too long")
			return 0
		}
	}
}

func zigzag(u uint64) int64 { return int64(u>>1) ^ -int64(u&1) }

// field reads the next tag; ok false at the end.
func (r *reader) field() (num int, wire int, ok bool) {
	if r.pos >= len(r.b) || r.err != nil {
		return 0, 0, false
	}
	t := r.varint()
	if r.err != nil {
		return 0, 0, false
	}
	return int(t >> 3), int(t & 7), true
}

// bytes reads a length-delimited field's payload.
func (r *reader) bytes() []byte {
	n := r.varint()
	if r.err != nil {
		return nil
	}
	if uint64(len(r.b)-r.pos) < n {
		r.err = io.ErrUnexpectedEOF
		return nil
	}
	out := r.b[r.pos : r.pos+int(n)]
	r.pos += int(n)
	return out
}

// skip passes over a field of the given wire type.
func (r *reader) skip(wire int) {
	switch wire {
	case 0:
		r.varint()
	case 1:
		r.pos += 8
	case 2:
		r.bytes()
	case 5:
		r.pos += 4
	default:
		r.err = fmt.Errorf("osmpbf: wire type %d", wire)
	}
	if r.pos > len(r.b) {
		r.err = io.ErrUnexpectedEOF
	}
}

// packedVarints appends the varints of a packed field (or the one varint of an unpacked one).
func (r *reader) packedVarints(wire int, dst []uint64) []uint64 {
	if wire == 0 {
		return append(dst, r.varint())
	}
	p := &reader{b: r.bytes()}
	for p.pos < len(p.b) && p.err == nil {
		dst = append(dst, p.varint())
	}
	if p.err != nil {
		r.err = p.err
	}
	return dst
}

// --- the file ---

// blob is one raw blob from the file, with its header type.
type blob struct {
	typ  string
	data []byte
}

func readBlob(f io.Reader) (blob, error) {
	var lenb [4]byte
	if _, err := io.ReadFull(f, lenb[:]); err != nil {
		return blob{}, err
	}
	hl := int(binary.BigEndian.Uint32(lenb[:]))
	if hl <= 0 || hl > 64<<10 {
		return blob{}, fmt.Errorf("osmpbf: blob header length %d", hl)
	}
	hb := make([]byte, hl)
	if _, err := io.ReadFull(f, hb); err != nil {
		return blob{}, fmt.Errorf("osmpbf: blob header: %w", err)
	}
	r := &reader{b: hb}
	var typ string
	size := -1
	for {
		num, wire, ok := r.field()
		if !ok {
			break
		}
		switch num {
		case 1:
			typ = string(r.bytes())
		case 3:
			size = int(r.varint())
		default:
			r.skip(wire)
		}
	}
	if r.err != nil {
		return blob{}, fmt.Errorf("osmpbf: blob header: %w", r.err)
	}
	if size < 0 || size > 32<<20 {
		return blob{}, fmt.Errorf("osmpbf: blob size %d", size)
	}
	data := make([]byte, size)
	if _, err := io.ReadFull(f, data); err != nil {
		return blob{}, fmt.Errorf("osmpbf: blob: %w", err)
	}
	return blob{typ: typ, data: data}, nil
}

// unblob decompresses a Blob message to its payload.
func unblob(data []byte) ([]byte, error) {
	r := &reader{b: data}
	var raw, z []byte
	rawSize := -1
	for {
		num, wire, ok := r.field()
		if !ok {
			break
		}
		switch num {
		case 1:
			raw = r.bytes()
		case 2:
			rawSize = int(r.varint())
		case 3:
			z = r.bytes()
		case 4, 5, 6, 7:
			return nil, fmt.Errorf("%w: blob compression field %d (only raw and zlib are read)", ErrFeature, num)
		default:
			r.skip(wire)
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	if raw != nil {
		return raw, nil
	}
	if z == nil {
		return nil, errors.New("osmpbf: empty blob")
	}
	zr, err := zlib.NewReader(bytes.NewReader(z))
	if err != nil {
		return nil, err
	}
	defer zr.Close()
	if rawSize < 0 {
		rawSize = 4 * len(z)
	}
	out := make([]byte, rawSize)
	n, err := io.ReadFull(zr, out)
	if err != nil && err != io.ErrUnexpectedEOF {
		return nil, err
	}
	return out[:n], nil
}

func parseHeader(payload []byte) (Header, error) {
	var h Header
	r := &reader{b: payload}
	for {
		num, wire, ok := r.field()
		if !ok {
			break
		}
		switch num {
		case 4:
			h.RequiredFeatures = append(h.RequiredFeatures, string(r.bytes()))
		case 5:
			h.OptionalFeatures = append(h.OptionalFeatures, string(r.bytes()))
		case 16:
			h.WritingProgram = string(r.bytes())
		case 17:
			h.Source = string(r.bytes())
		default:
			r.skip(wire)
		}
	}
	if r.err != nil {
		return h, r.err
	}
	for _, f := range h.RequiredFeatures {
		switch f {
		case "OsmSchema-V0.6", "DenseNodes", "Sort.Type_then_ID", "Has_Metadata":
		default:
			return h, fmt.Errorf("%w: required feature %q", ErrFeature, f)
		}
	}
	return h, nil
}

// parseBlock decodes a PrimitiveBlock.
func parseBlock(payload []byte) (*Block, error) {
	r := &reader{b: payload}
	var strs [][]byte
	var groups [][]byte
	granularity, latOff, lonOff := int64(100), int64(0), int64(0)
	for {
		num, wire, ok := r.field()
		if !ok {
			break
		}
		switch num {
		case 1:
			st := &reader{b: r.bytes()}
			for {
				n, w, ok := st.field()
				if !ok {
					break
				}
				if n == 1 {
					strs = append(strs, st.bytes())
				} else {
					st.skip(w)
				}
			}
			if st.err != nil {
				return nil, st.err
			}
		case 2:
			groups = append(groups, r.bytes())
		case 17:
			granularity = int64(r.varint())
		case 19:
			latOff = int64(r.varint())
		case 20:
			lonOff = int64(r.varint())
		default:
			r.skip(wire)
		}
	}
	if r.err != nil {
		return nil, r.err
	}
	b := &Block{}
	var tmp []uint64
	for _, g := range groups {
		gr := &reader{b: g}
		for {
			num, wire, ok := gr.field()
			if !ok {
				break
			}
			switch num {
			case 1: // a plain Node
				nr := &reader{b: gr.bytes()}
				var id, lat, lon int64
				for {
					n, w, ok := nr.field()
					if !ok {
						break
					}
					switch n {
					case 1:
						id = zigzag(nr.varint())
					case 8:
						lat = zigzag(nr.varint())
					case 9:
						lon = zigzag(nr.varint())
					default:
						nr.skip(w)
					}
				}
				if nr.err != nil {
					return nil, nr.err
				}
				b.NodeIDs = append(b.NodeIDs, id)
				b.NodeLat = append(b.NodeLat, latOff+granularity*lat)
				b.NodeLon = append(b.NodeLon, lonOff+granularity*lon)
			case 2: // DenseNodes
				dr := &reader{b: gr.bytes()}
				var ids, lats, lons []uint64
				for {
					n, w, ok := dr.field()
					if !ok {
						break
					}
					switch n {
					case 1:
						ids = dr.packedVarints(w, ids)
					case 8:
						lats = dr.packedVarints(w, lats)
					case 9:
						lons = dr.packedVarints(w, lons)
					default:
						dr.skip(w)
					}
				}
				if dr.err != nil {
					return nil, dr.err
				}
				if len(lats) != len(ids) || len(lons) != len(ids) {
					return nil, errors.New("osmpbf: dense nodes: id/lat/lon counts differ")
				}
				var id, lat, lon int64
				for i := range ids {
					id += zigzag(ids[i])
					lat += zigzag(lats[i])
					lon += zigzag(lons[i])
					b.NodeIDs = append(b.NodeIDs, id)
					b.NodeLat = append(b.NodeLat, latOff+granularity*lat)
					b.NodeLon = append(b.NodeLon, lonOff+granularity*lon)
				}
			case 3: // a Way
				wr := &reader{b: gr.bytes()}
				var w Way
				var keys, vals []uint64
				tmp = tmp[:0]
				for {
					n, wt, ok := wr.field()
					if !ok {
						break
					}
					switch n {
					case 1:
						w.ID = int64(wr.varint())
					case 2:
						keys = wr.packedVarints(wt, keys)
					case 3:
						vals = wr.packedVarints(wt, vals)
					case 8:
						tmp = wr.packedVarints(wt, tmp)
					default:
						wr.skip(wt)
					}
				}
				if wr.err != nil {
					return nil, wr.err
				}
				if len(keys) != len(vals) {
					return nil, errors.New("osmpbf: way: key/val counts differ")
				}
				if len(keys) > 0 {
					w.Tags = make(map[string]string, len(keys))
					for i := range keys {
						if int(keys[i]) < len(strs) && int(vals[i]) < len(strs) {
							w.Tags[string(strs[keys[i]])] = string(strs[vals[i]])
						}
					}
				}
				w.Refs = make([]int64, len(tmp))
				var ref int64
				for i, d := range tmp {
					ref += zigzag(d)
					w.Refs[i] = ref
				}
				b.Ways = append(b.Ways, w)
			default:
				gr.skip(wire)
			}
		}
		if gr.err != nil {
			return nil, gr.err
		}
	}
	return b, nil
}

// ReadHeader returns the file's OSMHeader.
func ReadHeader(path string) (Header, error) {
	f, err := os.Open(path)
	if err != nil {
		return Header{}, err
	}
	defer f.Close()
	bl, err := readBlob(f)
	if err != nil {
		return Header{}, err
	}
	if bl.typ != "OSMHeader" {
		return Header{}, fmt.Errorf("osmpbf: first blob is %q, not OSMHeader", bl.typ)
	}
	payload, err := unblob(bl.data)
	if err != nil {
		return Header{}, err
	}
	return parseHeader(payload)
}

// Scan reads every OSMData block of the file, decoding blocks in parallel on workers goroutines
// (0 = the CPU count) and calling fn with each in FILE ORDER. fn returning an error stops the
// scan. progress, when set, is told the bytes read so far now and then.
func Scan(path string, workers int, fn func(*Block) error, progress func(read, total int64)) error {
	if workers <= 0 {
		workers = runtime.NumCPU()
	}
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	total := int64(0)
	if fi, err := f.Stat(); err == nil {
		total = fi.Size()
	}
	type job struct {
		data []byte
		out  chan result
	}
	jobs := make(chan job, workers*2)
	order := make(chan chan result, workers*4)
	var wg sync.WaitGroup
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := range jobs {
				payload, err := unblob(j.data)
				var b *Block
				if err == nil {
					b, err = parseBlock(payload)
				}
				j.out <- result{b, err}
			}
		}()
	}
	readErr := make(chan error, 1)
	stop := make(chan struct{})
	go func() {
		defer close(order)
		defer close(jobs)
		var read int64
		var lastProg int64
		first := true
		for {
			select {
			case <-stop:
				readErr <- nil
				return
			default:
			}
			bl, err := readBlob(f)
			if err == io.EOF {
				readErr <- nil
				return
			}
			if err != nil {
				readErr <- err
				return
			}
			read += int64(4 + len(bl.data)) // approximate: the header is a few dozen bytes
			if first {
				first = false
				if bl.typ != "OSMHeader" {
					readErr <- fmt.Errorf("osmpbf: first blob is %q, not OSMHeader", bl.typ)
					return
				}
				payload, err := unblob(bl.data)
				if err == nil {
					_, err = parseHeader(payload)
				}
				if err != nil {
					readErr <- err
					return
				}
				continue
			}
			if bl.typ != "OSMData" {
				continue
			}
			out := make(chan result, 1)
			order <- out
			jobs <- job{bl.data, out}
			if progress != nil && read-lastProg > 64<<20 {
				lastProg = read
				progress(read, total)
			}
		}
	}()
	var fnErr error
	for out := range order {
		res := <-out
		if fnErr != nil {
			continue // drain what is in flight; the reader has been told to stop
		}
		if res.err != nil {
			fnErr = res.err
		} else if err := fn(res.b); err != nil {
			fnErr = err
		}
		if fnErr != nil {
			close(stop)
		}
	}
	wg.Wait()
	if err := <-readErr; err != nil && fnErr == nil {
		return err
	}
	return fnErr
}

type result struct {
	b   *Block
	err error
}
