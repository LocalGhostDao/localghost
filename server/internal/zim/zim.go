// Package zim reads Kiwix's ZIM files: the offline Wikipedia the box keeps (the mirror's set
// wikipedia, wikipedia_en_all_nopic.zim, about 50 GB). Only reading, only what the box needs: an
// entry by its path or its title, the titles that start with a prefix, redirects followed, and an
// entry's bytes. Clusters compressed with zstd (every ZIM Kiwix has made for years) are read with
// internal/zstd, the Go standard library's own decoder; uncompressed ones as they are; the old xz
// clusters are refused with a word. The standard library and nothing else.
//
// The format (openzim.org/wiki/ZIM_file_format), little-endian throughout:
//
//	header (80 bytes): magic 72173914, major and minor version, uuid, entry count, cluster count,
//	  where the path pointers, the title pointers, the cluster pointers and the MIME list are,
//	  the main page, the layout page, where the checksum is
//	MIME list: NUL-terminated strings, an empty one last
//	path pointers: an 8-byte offset of each entry, by (namespace, path)
//	title pointers: a 4-byte entry index each, by (namespace, title)
//	entry: MIME index (0xffff a redirect), parameter length, namespace, revision, then the cluster
//	  and blob (or, for a redirect, the entry it points to), the path and the title, NUL-ended
//	cluster: an info byte (low four bits the compression, 0x10 eight-byte offsets) and, once
//	  decompressed, the blobs' offsets then the blobs
//
// Namespaces: the newer files keep everything readable in 'C' (paths like "Bitcoin" and
// "Solana_(blockchain_platform)"), metadata in 'M', the well-known entries in 'W'; the older ones
// kept articles in 'A'.
package zim

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"

	"github.com/LocalGhostDao/localghost/server/internal/zstd"
)

const (
	magic        = 72173914
	headerSize   = 80
	redirectMime = 0xffff
	linkTarget   = 0xfffe
	deleted      = 0xfffd
	maxRedirects = 8
	clustersKept = 12  // decompressed clusters held (a Wikipedia cluster is about 1-2 MB)
	direntRead   = 512 // bytes read for an entry; more when its names are longer
)

// The bounds (variables, so a test can lower them): a compressed cluster longer than
// maxClusterLen is not one this reader decompresses whole; a blob from an uncompressed cluster
// longer than maxBlobLen is not one it loads. Kiwix's files keep their listings and search
// indexes (gigabytes) in uncompressed clusters, whose blobs are read in place and never whole.
var (
	maxClusterLen uint64 = 128 << 20
	maxBlobLen    uint64 = 256 << 20
)

// Header is the file's header.
type Header struct {
	Major, Minor  uint16
	EntryCount    uint32
	ClusterCount  uint32
	PathPtrPos    uint64
	TitlePtrPos   uint64
	ClusterPtrPos uint64
	MimeListPos   uint64
	MainPage      uint32
	LayoutPage    uint32
	ChecksumPos   uint64
}

// Entry is one entry of the file.
type Entry struct {
	Index     uint32 // its place in path order
	Namespace byte
	Path      string
	Title     string // the path when the file gives none
	Mime      string
	Redirect  bool
	Target    uint32 // the entry a redirect points to
	Cluster   uint32
	Blob      uint32
}

// File is an open ZIM file. Safe for use from several goroutines.
type File struct {
	r      io.ReaderAt
	closer io.Closer
	size   int64
	H      Header
	mimes  []string
	// the title order: the header's list of every entry when the file has one, else the
	// front articles' list (X/listing/titleOrdered/v1)
	titles   func(i uint32) (uint32, error)
	titleLen uint32

	mu    sync.Mutex
	cache map[uint32][]byte // cluster -> its decompressed bytes
	order []uint32
}

// Open opens a ZIM file.
func Open(path string) (*File, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}
	z, err := NewReader(f, st.Size())
	if err != nil {
		f.Close()
		return nil, err
	}
	z.closer = f
	return z, nil
}

// NewReader reads a ZIM file from r (size bytes long).
func NewReader(r io.ReaderAt, size int64) (*File, error) {
	z := &File{r: r, size: size, cache: map[uint32][]byte{}}
	var b [headerSize]byte
	if _, err := r.ReadAt(b[:], 0); err != nil {
		return nil, fmt.Errorf("zim: header: %w", err)
	}
	le := binary.LittleEndian
	if le.Uint32(b[0:]) != magic {
		return nil, errors.New("zim: not a ZIM file (wrong magic number)")
	}
	z.H = Header{
		Major: le.Uint16(b[4:]), Minor: le.Uint16(b[6:]),
		EntryCount: le.Uint32(b[24:]), ClusterCount: le.Uint32(b[28:]),
		PathPtrPos: le.Uint64(b[32:]), TitlePtrPos: le.Uint64(b[40:]), ClusterPtrPos: le.Uint64(b[48:]),
		MimeListPos: le.Uint64(b[56:]), MainPage: le.Uint32(b[64:]), LayoutPage: le.Uint32(b[68:]),
		ChecksumPos: le.Uint64(b[72:]),
	}
	h := z.H
	if h.Major != 5 && h.Major != 6 {
		return nil, fmt.Errorf("zim: version %d.%d is not one this reader knows", h.Major, h.Minor)
	}
	if !z.fits(h.PathPtrPos, 8*uint64(h.EntryCount)) || !z.fits(h.ClusterPtrPos, 8*uint64(h.ClusterCount)) || h.MimeListPos >= uint64(size) {
		return nil, errors.New("zim: the header points outside the file (truncated?)")
	}
	if err := z.readMimes(); err != nil {
		return nil, err
	}
	// the title order: the header's list when it is there and fits, else the front articles'
	if h.TitlePtrPos != 0 && h.TitlePtrPos != ^uint64(0) && z.fits(h.TitlePtrPos, 4*uint64(h.EntryCount)) {
		pos := h.TitlePtrPos
		z.titleLen = h.EntryCount
		z.titles = func(i uint32) (uint32, error) { return z.u32(pos + 4*uint64(i)) }
	} else if e, ok, err := z.FindPath('X', "listing/titleOrdered/v1"); err == nil && ok {
		// the listing is four bytes an entry (80 MB for the English Wikipedia) in an uncompressed
		// cluster, which Kiwix shares with the search indexes (gigabytes): read it where it lies,
		// never the cluster whole
		if e, err = z.Resolve(e); err != nil {
			return nil, err
		}
		if start, end, plain, err := z.blobRange(e); err != nil {
			return nil, err
		} else if plain {
			z.titleLen = uint32((end - start) / 4)
			z.titles = func(i uint32) (uint32, error) {
				if i >= z.titleLen {
					return 0, errors.New("zim: title index out of range")
				}
				return z.u32(start + 4*uint64(i))
			}
		} else {
			blob, err := z.Content(e)
			if err != nil {
				return nil, err
			}
			z.titleLen = uint32(len(blob) / 4)
			z.titles = func(i uint32) (uint32, error) {
				if i >= z.titleLen {
					return 0, errors.New("zim: title index out of range")
				}
				return le.Uint32(blob[4*i:]), nil
			}
		}
	}
	return z, nil
}

// Close closes the file.
func (z *File) Close() error {
	if z.closer != nil {
		return z.closer.Close()
	}
	return nil
}

func (z *File) fits(pos, n uint64) bool { return pos <= uint64(z.size) && n <= uint64(z.size)-pos }

func (z *File) u32(pos uint64) (uint32, error) {
	var b [4]byte
	if _, err := z.r.ReadAt(b[:], int64(pos)); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint32(b[:]), nil
}

func (z *File) u64(pos uint64) (uint64, error) {
	var b [8]byte
	if _, err := z.r.ReadAt(b[:], int64(pos)); err != nil {
		return 0, err
	}
	return binary.LittleEndian.Uint64(b[:]), nil
}

func (z *File) readMimes() error {
	n := int64(4096)
	for {
		if rest := z.size - int64(z.H.MimeListPos); n > rest {
			n = rest
		}
		b := make([]byte, n)
		got, err := z.r.ReadAt(b, int64(z.H.MimeListPos))
		if err != nil && err != io.EOF {
			return fmt.Errorf("zim: MIME list: %w", err)
		}
		b = b[:got]
		var mimes []string
		for start := 0; start < len(b); {
			i := bytes.IndexByte(b[start:], 0)
			if i < 0 {
				break
			}
			if i == 0 {
				z.mimes = mimes
				return nil
			}
			mimes = append(mimes, string(b[start:start+i]))
			start += i + 1
		}
		if int64(got) < n || n >= 1<<20 {
			return errors.New("zim: the MIME list does not end")
		}
		n *= 4
	}
}

// Title is the file's own title (metadata M/Title), "" when it has none.
func (z *File) Title() string { return z.Metadata("Title") }

// Metadata is a metadata value (M/<name>: Title, Language, Date, Creator, Description …).
func (z *File) Metadata(name string) string {
	ns := byte('M')
	e, ok, err := z.FindPath(ns, name)
	if err != nil || !ok {
		return ""
	}
	b, err := z.Content(e)
	if err != nil {
		return ""
	}
	return string(b)
}

// ArticleNamespace is where the articles are: 'C' in the newer files, 'A' in the older.
func (z *File) ArticleNamespace() byte {
	if z.H.Major >= 6 && z.H.Minor >= 1 {
		return 'C'
	}
	return 'A'
}

// EntryAt reads the entry at a place in path order.
func (z *File) EntryAt(i uint32) (Entry, error) {
	if i >= z.H.EntryCount {
		return Entry{}, fmt.Errorf("zim: entry %d of %d", i, z.H.EntryCount)
	}
	off, err := z.u64(z.H.PathPtrPos + 8*uint64(i))
	if err != nil {
		return Entry{}, err
	}
	e, err := z.direntAt(off)
	e.Index = i
	return e, err
}

func (z *File) direntAt(off uint64) (Entry, error) {
	n := direntRead
	for {
		if off >= uint64(z.size) {
			return Entry{}, errors.New("zim: an entry outside the file")
		}
		if rest := uint64(z.size) - off; uint64(n) > rest {
			n = int(rest)
		}
		b := make([]byte, n)
		got, err := z.r.ReadAt(b, int64(off))
		if err != nil && err != io.EOF {
			return Entry{}, err
		}
		b = b[:got]
		e, ok, perr := parseDirent(b, z.mimes)
		if perr != nil {
			return Entry{}, perr
		}
		if ok {
			return e, nil
		}
		if got < n || n >= 1<<16 {
			return Entry{}, errors.New("zim: an entry's names do not end")
		}
		n *= 4
	}
}

// parseDirent reads an entry from its bytes; ok is false when its names run past the end of b.
func parseDirent(b []byte, mimes []string) (Entry, bool, error) {
	le := binary.LittleEndian
	if len(b) < 12 {
		return Entry{}, false, nil
	}
	var e Entry
	mime := le.Uint16(b[0:])
	plen := int(b[2])
	e.Namespace = b[3]
	rest := b[8:]
	switch mime {
	case redirectMime:
		e.Redirect = true
		e.Target = le.Uint32(rest)
		rest = rest[4:]
	case linkTarget, deleted:
		// no cluster: nothing to read
	default:
		if len(rest) < 8 {
			return Entry{}, false, nil
		}
		if int(mime) < len(mimes) {
			e.Mime = mimes[mime]
		}
		e.Cluster, e.Blob = le.Uint32(rest), le.Uint32(rest[4:])
		rest = rest[8:]
	}
	i := bytes.IndexByte(rest, 0)
	if i < 0 {
		return Entry{}, false, nil
	}
	e.Path = string(rest[:i])
	rest = rest[i+1:]
	j := bytes.IndexByte(rest, 0)
	if j < 0 {
		return Entry{}, false, nil
	}
	e.Title = string(rest[:j])
	if e.Title == "" {
		e.Title = e.Path
	}
	_ = plen // the parameter data follows; nothing the box needs
	return e, true, nil
}

// cmp orders (namespace, name) the way the file's lists are sorted: the namespace byte, then the
// name's bytes.
func cmp(ns byte, name string, ens byte, ename string) int {
	if ns != ens {
		if ns < ens {
			return -1
		}
		return 1
	}
	return strings.Compare(name, ename)
}

// FindPath is the entry at a path in a namespace.
func (z *File) FindPath(ns byte, path string) (Entry, bool, error) {
	lo, hi := uint32(0), z.H.EntryCount
	for lo < hi {
		mid := lo + (hi-lo)/2
		e, err := z.EntryAt(mid)
		if err != nil {
			return Entry{}, false, err
		}
		switch c := cmp(ns, path, e.Namespace, e.Path); {
		case c == 0:
			return e, true, nil
		case c < 0:
			hi = mid
		default:
			lo = mid + 1
		}
	}
	return Entry{}, false, nil
}

func (z *File) titleEntry(i uint32) (Entry, error) {
	idx, err := z.titles(i)
	if err != nil {
		return Entry{}, err
	}
	return z.EntryAt(idx)
}

// titleLowerBound is the first place in title order not before (ns, title).
func (z *File) titleLowerBound(ns byte, title string) (uint32, error) {
	lo, hi := uint32(0), z.titleLen
	for lo < hi {
		mid := lo + (hi-lo)/2
		e, err := z.titleEntry(mid)
		if err != nil {
			return 0, err
		}
		if cmp(ns, title, e.Namespace, e.Title) > 0 {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	return lo, nil
}

// FindTitle is the entry with exactly this title in a namespace (the title order's, so in a file
// with only the front articles' list, a front article).
func (z *File) FindTitle(ns byte, title string) (Entry, bool, error) {
	if z.titles == nil {
		return Entry{}, false, nil
	}
	i, err := z.titleLowerBound(ns, title)
	if err != nil || i >= z.titleLen {
		return Entry{}, false, err
	}
	e, err := z.titleEntry(i)
	if err != nil {
		return Entry{}, false, err
	}
	return e, e.Namespace == ns && e.Title == title, nil
}

// TitlesWithPrefix is up to max entries whose titles start with prefix, in title order.
func (z *File) TitlesWithPrefix(ns byte, prefix string, max int) ([]Entry, error) {
	if z.titles == nil || max <= 0 {
		return nil, nil
	}
	i, err := z.titleLowerBound(ns, prefix)
	if err != nil {
		return nil, err
	}
	var out []Entry
	for ; i < z.titleLen && len(out) < max; i++ {
		e, err := z.titleEntry(i)
		if err != nil {
			return out, err
		}
		if e.Namespace != ns || !strings.HasPrefix(e.Title, prefix) {
			break
		}
		out = append(out, e)
	}
	return out, nil
}

// Resolve follows redirects to the entry with content.
func (z *File) Resolve(e Entry) (Entry, error) {
	for hop := 0; e.Redirect; hop++ {
		if hop >= maxRedirects {
			return Entry{}, fmt.Errorf("zim: more than %d redirects from %q", maxRedirects, e.Path)
		}
		t, err := z.EntryAt(e.Target)
		if err != nil {
			return Entry{}, err
		}
		e = t
	}
	return e, nil
}

// Content is an entry's bytes (a redirect is followed).
func (z *File) Content(e Entry) ([]byte, error) {
	e, err := z.Resolve(e)
	if err != nil {
		return nil, err
	}
	if e.Mime == "" && e.Cluster == 0 && e.Blob == 0 {
		return nil, fmt.Errorf("zim: %q has no content", e.Path)
	}
	// an uncompressed cluster (the newer files keep their listings and search indexes in a few of
	// them, gigabytes long): the one blob, read where it lies
	if start, end, plain, err := z.blobRange(e); err != nil {
		return nil, err
	} else if plain {
		if end-start > maxBlobLen {
			return nil, fmt.Errorf("zim: %q is %d MB, more than this reader loads", e.Path, (end-start)>>20)
		}
		b := make([]byte, end-start)
		if _, err := z.r.ReadAt(b, int64(start)); err != nil && err != io.EOF {
			return nil, err
		}
		return b, nil
	}
	c, err := z.cluster(e.Cluster)
	if err != nil {
		return nil, err
	}
	return blob(c.data, c.ext, e.Blob)
}

// blobRange is where an entry's blob lies in the file when its cluster is stored uncompressed
// (plain true): the cluster's offsets are read, not its contents. For a compressed cluster plain
// is false and the cluster is decompressed by the caller.
func (z *File) blobRange(e Entry) (start, end uint64, plain bool, err error) {
	if e.Cluster >= z.H.ClusterCount {
		return 0, 0, false, fmt.Errorf("zim: cluster %d of %d", e.Cluster, z.H.ClusterCount)
	}
	cstart, err := z.u64(z.H.ClusterPtrPos + 8*uint64(e.Cluster))
	if err != nil {
		return 0, 0, false, err
	}
	if cstart+1 >= uint64(z.size) {
		return 0, 0, false, fmt.Errorf("zim: cluster %d starts outside the file", e.Cluster)
	}
	var info [1]byte
	if _, err := z.r.ReadAt(info[:], int64(cstart)); err != nil {
		return 0, 0, false, err
	}
	if c := info[0] & 0x0f; c != 0 && c != 1 {
		return 0, 0, false, nil
	}
	ext := info[0]&0x10 != 0
	w := uint64(4)
	off := func(i uint64) (uint64, error) {
		if !ext {
			v, err := z.u32(cstart + 1 + 4*i)
			return uint64(v), err
		}
		return z.u64(cstart + 1 + 8*i)
	}
	if ext {
		w = 8
	}
	first, err := off(0)
	if err != nil {
		return 0, 0, false, err
	}
	if first < w || first%w != 0 {
		return 0, 0, false, errors.New("zim: a cluster without its offsets")
	}
	blobs := first/w - 1
	if uint64(e.Blob) >= blobs {
		return 0, 0, false, fmt.Errorf("zim: blob %d of %d", e.Blob, blobs)
	}
	a, err := off(uint64(e.Blob))
	if err != nil {
		return 0, 0, false, err
	}
	b, err := off(uint64(e.Blob) + 1)
	if err != nil {
		return 0, 0, false, err
	}
	if b < a || cstart+1+b > uint64(z.size) {
		return 0, 0, false, errors.New("zim: a blob outside its cluster")
	}
	return cstart + 1 + a, cstart + 1 + b, true, nil
}

type clusterData struct {
	data []byte
	ext  bool
}

func (z *File) cluster(n uint32) (clusterData, error) {
	if n >= z.H.ClusterCount {
		return clusterData{}, fmt.Errorf("zim: cluster %d of %d", n, z.H.ClusterCount)
	}
	z.mu.Lock()
	if d, ok := z.cache[n]; ok {
		z.mu.Unlock()
		return clusterData{data: d[1:], ext: d[0] == 1}, nil
	}
	z.mu.Unlock()
	start, err := z.u64(z.H.ClusterPtrPos + 8*uint64(n))
	if err != nil {
		return clusterData{}, err
	}
	end := z.H.ChecksumPos
	if n+1 < z.H.ClusterCount {
		if end, err = z.u64(z.H.ClusterPtrPos + 8*uint64(n+1)); err != nil {
			return clusterData{}, err
		}
	}
	if end == 0 || end > uint64(z.size) {
		end = uint64(z.size)
	}
	if start >= end {
		return clusterData{}, fmt.Errorf("zim: cluster %d spans %d..%d", n, start, end)
	}
	if end-start > maxClusterLen {
		// an uncompressed cluster this long is the listings and the search indexes; its blobs
		// are read in place (blobRange); a compressed one this long is not a file this reader knows
		return clusterData{}, fmt.Errorf("zim: cluster %d is %d MB, more than this reader decompresses whole", n, (end-start)>>20)
	}
	raw := make([]byte, end-start)
	if _, err := z.r.ReadAt(raw, int64(start)); err != nil && err != io.EOF {
		return clusterData{}, err
	}
	info := raw[0]
	ext := info&0x10 != 0
	var data []byte
	switch info & 0x0f {
	case 0, 1:
		data, err = readBlobs(bytes.NewReader(raw[1:]), ext)
	case 5:
		// one zstd frame; read only as far as the blobs go (what follows the frame in the file
		// is not this cluster's)
		data, err = readBlobs(zstd.NewReader(bytes.NewReader(raw[1:])), ext)
	case 4:
		return clusterData{}, fmt.Errorf("zim: cluster %d is xz-compressed, which this reader does not read (Kiwix's files since 2021 use zstd)", n)
	default:
		return clusterData{}, fmt.Errorf("zim: cluster %d: compression %d unknown", n, info&0x0f)
	}
	if err != nil {
		return clusterData{}, fmt.Errorf("zim: cluster %d: %w", n, err)
	}
	keep := make([]byte, 1+len(data))
	if ext {
		keep[0] = 1
	}
	copy(keep[1:], data)
	z.mu.Lock()
	if _, ok := z.cache[n]; !ok {
		z.cache[n] = keep
		z.order = append(z.order, n)
		if len(z.order) > clustersKept {
			delete(z.cache, z.order[0])
			z.order = z.order[1:]
		}
	}
	z.mu.Unlock()
	return clusterData{data: keep[1:], ext: ext}, nil
}

// readBlobs reads a cluster's offsets and blobs from its (decompressed) stream and stops there:
// the first offset says how many offsets there are, the last where the last blob ends.
func readBlobs(r io.Reader, ext bool) ([]byte, error) {
	w := 4
	if ext {
		w = 8
	}
	head := make([]byte, w)
	if _, err := io.ReadFull(r, head); err != nil {
		return nil, err
	}
	first := uint64(binary.LittleEndian.Uint32(head))
	if ext {
		first = binary.LittleEndian.Uint64(head)
	}
	if first < uint64(w) || first%uint64(w) != 0 || first > maxClusterLen {
		return nil, errors.New("a cluster without its offsets")
	}
	data := make([]byte, first)
	copy(data, head)
	if _, err := io.ReadFull(r, data[w:]); err != nil {
		return nil, err
	}
	last := uint64(binary.LittleEndian.Uint32(data[len(data)-w:]))
	if ext {
		last = binary.LittleEndian.Uint64(data[len(data)-w:])
	}
	if last < first || last > maxClusterLen {
		return nil, errors.New("a cluster whose blobs end before they start")
	}
	all := make([]byte, last)
	copy(all, data)
	if _, err := io.ReadFull(r, all[first:]); err != nil {
		return nil, err
	}
	return all, nil
}

// blob is a cluster's n-th blob: the offsets first (4 bytes each, 8 when extended), the first
// offset saying how many there are.
func blob(data []byte, ext bool, n uint32) ([]byte, error) {
	w := 4
	if ext {
		w = 8
	}
	off := func(i uint64) (uint64, bool) {
		p := i * uint64(w)
		if p+uint64(w) > uint64(len(data)) {
			return 0, false
		}
		if ext {
			return binary.LittleEndian.Uint64(data[p:]), true
		}
		return uint64(binary.LittleEndian.Uint32(data[p:])), true
	}
	first, ok := off(0)
	if !ok || first%uint64(w) != 0 {
		return nil, errors.New("zim: a cluster without its offsets")
	}
	blobs := first/uint64(w) - 1
	if uint64(n) >= blobs {
		return nil, fmt.Errorf("zim: blob %d of %d", n, blobs)
	}
	a, ok1 := off(uint64(n))
	b, ok2 := off(uint64(n) + 1)
	if !ok1 || !ok2 || a > b || b > uint64(len(data)) {
		return nil, errors.New("zim: a blob outside its cluster")
	}
	return data[a:b], nil
}
