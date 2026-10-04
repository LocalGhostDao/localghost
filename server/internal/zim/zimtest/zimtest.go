// Package zimtest writes small ZIM files for the tests: the layout Kiwix's files have (header,
// MIME list, entries, path and title pointers, clusters, checksum), one cluster stored as it is and
// one in a zstd frame (raw blocks, eight-byte offsets), so the reader's paths are all walked.
package zimtest

import (
	"bytes"
	"encoding/binary"
	"sort"
)

// Item is one entry: content, or a redirect to another path in the same namespace.
type Item struct {
	NS       byte
	Path     string
	Title    string // "" when the same as the path
	Mime     string
	Body     []byte
	Redirect string
}

// Options shape the file the way the newer Kiwix files are shaped.
type Options struct {
	// TitleListing: no title pointer list in the header (0xffff…); the title order is the entry
	// X/listing/titleOrdered/v1, in an uncompressed cluster of its own (eight-byte offsets)
	// shared with X/fulltext/xapian, a blob of Filler zero bytes, the way the search index is.
	TitleListing bool
	Filler       int
}

// Build writes a ZIM file (version 6.minor) holding the items.
func Build(items []Item, minor uint16) []byte { return BuildWith(items, minor, Options{}) }

// BuildWith is Build with Options.
func BuildWith(items []Item, minor uint16, opt Options) []byte {
	le := binary.LittleEndian
	if opt.TitleListing {
		items = append(append([]Item(nil), items...),
			Item{NS: 'X', Path: "listing/titleOrdered/v1", Mime: "application/octet-stream+zimlisting"},
			Item{NS: 'X', Path: "fulltext/xapian", Mime: "application/octet-stream+xapian", Body: make([]byte, opt.Filler)})
	}
	// path order
	sorted := append([]Item(nil), items...)
	sort.SliceStable(sorted, func(i, j int) bool {
		if sorted[i].NS != sorted[j].NS {
			return sorted[i].NS < sorted[j].NS
		}
		return sorted[i].Path < sorted[j].Path
	})
	index := map[string]int{}
	for i, it := range sorted {
		index[string(it.NS)+"/"+it.Path] = i
	}
	title := func(it Item) string {
		if it.Title != "" {
			return it.Title
		}
		return it.Path
	}
	byTitle := make([]int, len(sorted))
	for i := range byTitle {
		byTitle[i] = i
	}
	sort.SliceStable(byTitle, func(a, b int) bool {
		x, y := sorted[byTitle[a]], sorted[byTitle[b]]
		if x.NS != y.NS {
			return x.NS < y.NS
		}
		return title(x) < title(y)
	})
	if opt.TitleListing {
		var lst bytes.Buffer
		for _, i := range byTitle {
			binary.Write(&lst, le, uint32(i))
		}
		sorted[index["X/listing/titleOrdered/v1"]].Body = lst.Bytes()
	}
	// MIME list
	var mimes []string
	mimeIdx := map[string]int{}
	for _, it := range sorted {
		if it.Redirect == "" {
			if _, ok := mimeIdx[it.Mime]; !ok {
				mimeIdx[it.Mime] = len(mimes)
				mimes = append(mimes, it.Mime)
			}
		}
	}
	// blobs: the first half of the contents in cluster 0, the rest in cluster 1; the X entries
	// (the listings and the indexes) in cluster 2, stored as they are
	type place struct{ cluster, blob uint32 }
	places := make([]place, len(sorted))
	var c0, c1, c2 [][]byte
	var contents []int
	for i, it := range sorted {
		if it.Redirect == "" && it.NS != 'X' {
			contents = append(contents, i)
		}
	}
	for k, i := range contents {
		if k < (len(contents)+1)/2 {
			places[i] = place{0, uint32(len(c0))}
			c0 = append(c0, sorted[i].Body)
		} else {
			places[i] = place{1, uint32(len(c1))}
			c1 = append(c1, sorted[i].Body)
		}
	}
	for i, it := range sorted {
		if it.Redirect == "" && it.NS == 'X' {
			places[i] = place{2, uint32(len(c2))}
			c2 = append(c2, it.Body)
		}
	}
	var out bytes.Buffer
	out.Write(make([]byte, 80))
	mimePos := out.Len()
	for _, m := range mimes {
		out.WriteString(m)
		out.WriteByte(0)
	}
	out.WriteByte(0)
	offsets := make([]uint64, len(sorted))
	for i, it := range sorted {
		offsets[i] = uint64(out.Len())
		var b [16]byte
		if it.Redirect != "" {
			le.PutUint16(b[0:], 0xffff)
			b[3] = it.NS
			le.PutUint32(b[8:], uint32(index[string(it.NS)+"/"+it.Redirect]))
			out.Write(b[:12])
		} else {
			le.PutUint16(b[0:], uint16(mimeIdx[it.Mime]))
			b[3] = it.NS
			le.PutUint32(b[8:], places[i].cluster)
			le.PutUint32(b[12:], places[i].blob)
			out.Write(b[:16])
		}
		out.WriteString(it.Path)
		out.WriteByte(0)
		if it.Title != "" && it.Title != it.Path {
			out.WriteString(it.Title)
		}
		out.WriteByte(0)
	}
	pathPos := out.Len()
	for _, o := range offsets {
		binary.Write(&out, le, o)
	}
	titlePos := out.Len()
	for _, i := range byTitle {
		binary.Write(&out, le, uint32(i))
	}
	clusterStarts := []uint64{uint64(out.Len())}
	out.WriteByte(1) // stored as it is, four-byte offsets
	out.Write(clusterBody(c0, false))
	clusterStarts = append(clusterStarts, uint64(out.Len()))
	out.WriteByte(5 | 0x10) // zstd, eight-byte offsets
	out.Write(rawZstd(clusterBody(c1, true)))
	nClusters := uint32(2)
	if len(c2) > 0 {
		clusterStarts = append(clusterStarts, uint64(out.Len()))
		out.WriteByte(1 | 0x10) // stored as it is, eight-byte offsets: the big one
		out.Write(clusterBody(c2, true))
		nClusters = 3
	}
	clusterPtrPos := out.Len()
	for _, s := range clusterStarts {
		binary.Write(&out, le, s)
	}
	checksumPos := out.Len()
	out.Write(make([]byte, 16))
	b := out.Bytes()
	le.PutUint32(b[0:], 72173914)
	le.PutUint16(b[4:], 6)
	le.PutUint16(b[6:], minor)
	le.PutUint32(b[24:], uint32(len(sorted)))
	le.PutUint32(b[28:], nClusters)
	le.PutUint64(b[32:], uint64(pathPos))
	le.PutUint64(b[40:], uint64(titlePos))
	if opt.TitleListing {
		le.PutUint64(b[40:], ^uint64(0)) // no list in the header: the X listing is the title order
	}
	le.PutUint64(b[48:], uint64(clusterPtrPos))
	le.PutUint64(b[56:], uint64(mimePos))
	le.PutUint32(b[64:], 0)
	le.PutUint32(b[68:], 0xffffffff)
	le.PutUint64(b[72:], uint64(checksumPos))
	return b
}

func clusterBody(blobs [][]byte, ext bool) []byte {
	w := 4
	if ext {
		w = 8
	}
	var b bytes.Buffer
	off := uint64(w * (len(blobs) + 1))
	put := func(v uint64) {
		if ext {
			binary.Write(&b, binary.LittleEndian, v)
		} else {
			binary.Write(&b, binary.LittleEndian, uint32(v))
		}
	}
	for _, x := range blobs {
		put(off)
		off += uint64(len(x))
	}
	put(off)
	for _, x := range blobs {
		b.Write(x)
	}
	return b.Bytes()
}

// rawZstd wraps data in a zstd frame of raw blocks (single segment, the content size given).
func rawZstd(data []byte) []byte {
	var b bytes.Buffer
	b.Write([]byte{0x28, 0xb5, 0x2f, 0xfd})
	b.WriteByte(0xa0) // four-byte content size, single segment
	binary.Write(&b, binary.LittleEndian, uint32(len(data)))
	for start := 0; ; {
		end := start + 100<<10
		if end > len(data) {
			end = len(data)
		}
		n := end - start
		hdr := uint32(n) << 3
		if end == len(data) {
			hdr |= 1
		}
		b.Write([]byte{byte(hdr), byte(hdr >> 8), byte(hdr >> 16)})
		b.Write(data[start:end])
		if end == len(data) {
			break
		}
		start = end
	}
	return b.Bytes()
}
