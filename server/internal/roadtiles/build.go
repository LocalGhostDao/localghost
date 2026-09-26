package roadtiles

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io/fs"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/osmpbf"
)

// Options tune a build.
type Options struct {
	Workers  int          // PBF decoding goroutines (0 = the CPU count)
	Work     string       // scratch directory (node files, cell buffers); default outDir+".work"
	Progress func(string) // told what is happening, now and then
	BufferMB int          // cell buffers held in memory before they are flushed to disk (default 512)
}

// Stats is what a build produced.
type Stats struct {
	Files      int
	Ways       int64 // road ways read
	MajorTiles int
	FineTiles  int
	Points     int64
	Bytes      int64
	Took       time.Duration
}

func (s Stats) String() string {
	return fmt.Sprintf("%d files, %d road ways → %d major tiles + %d fine tiles (%d points, %.1f GB) in %s",
		s.Files, s.Ways, s.MajorTiles, s.FineTiles, s.Points, float64(s.Bytes)/1e9, s.Took.Round(time.Second))
}

// Build cuts the roads of the PBF files into tiles under outDir: index.bin, 1/<x>_<y>.lgr (major
// roads, one-degree cells) and 0/<x>_<y>.lgr (every road with its name, tenth-of-a-degree cells).
// Three passes over each file , the node ids the roads need, those nodes' coordinates, the roads
// themselves , then one pass over the cells. Written beside outDir and swapped in whole when
// complete, so a reader sees the old tiles or the new ones, never half.
//
// Memory: a bitmap over the node id space (about 1.6 GB for the planet's ids, the same for a small
// extract, since ids are global), the cell buffers (BufferMB), and the OS page cache for the node
// file (16 bytes per road node: ~10 GB for Europe; a box with less RAM than that still finishes,
// slower). Disk: the node files (removed after each PBF) and the cell buffers (about the size of
// the finished tiles) under Work.
func Build(pbfs []string, outDir string, opt Options) (Stats, error) {
	t0 := time.Now()
	var st Stats
	if len(pbfs) == 0 {
		return st, errors.New("no PBF files")
	}
	say := opt.Progress
	if say == nil {
		say = func(string) {}
	}
	work := opt.Work
	if work == "" {
		work = strings.TrimRight(outDir, "/") + ".work"
	}
	cellsDir := filepath.Join(work, "cells")
	_ = os.RemoveAll(cellsDir)
	if err := os.MkdirAll(cellsDir, 0o755); err != nil {
		return st, err
	}
	bufMB := opt.BufferMB
	if bufMB <= 0 {
		bufMB = 512
	}
	cells := newCellBuffers(cellsDir, int64(bufMB)<<20)
	for _, pbf := range pbfs {
		name := filepath.Base(pbf)
		if _, err := osmpbf.ReadHeader(pbf); err != nil {
			return st, fmt.Errorf("%s: %w", name, err)
		}
		st.Files++
		// pass 1: which nodes the roads need
		say(name + ": pass 1 of 3, finding the roads' nodes")
		need := newBitmap()
		var roadWays int64
		err := osmpbf.Scan(pbf, opt.Workers, func(b *osmpbf.Block) error {
			for i := range b.Ways {
				w := &b.Ways[i]
				if Class(w.Tags["highway"]) == 0 || len(w.Refs) < 2 {
					continue
				}
				roadWays++
				for _, r := range w.Refs {
					need.set(r)
				}
			}
			return nil
		}, pct(say, name+": pass 1"))
		if err != nil {
			return st, fmt.Errorf("%s: %w", name, err)
		}
		say(fmt.Sprintf("%s: %d road ways, %d nodes to find", name, roadWays, need.count()))
		if roadWays == 0 {
			continue
		}
		// pass 2: those nodes' coordinates, in id order, to a flat file
		say(name + ": pass 2 of 3, collecting the nodes")
		nodePath := filepath.Join(work, "nodes-"+name+".bin")
		nf, err := os.Create(nodePath)
		if err != nil {
			return st, err
		}
		nw := bufio.NewWriterSize(nf, 4<<20)
		var lastID int64 = math.MinInt64
		var found int64
		unsorted := false
		var rec [16]byte
		err = osmpbf.Scan(pbf, opt.Workers, func(b *osmpbf.Block) error {
			for i, id := range b.NodeIDs {
				if !need.has(id) {
					continue
				}
				if id <= lastID {
					unsorted = true
				}
				lastID = id
				putRec(rec[:], id, int32(b.NodeLat[i]/100), int32(b.NodeLon[i]/100))
				if _, err := nw.Write(rec[:]); err != nil {
					return err
				}
				found++
			}
			return nil
		}, pct(say, name+": pass 2"))
		if err == nil {
			err = nw.Flush()
		}
		if cerr := nf.Close(); err == nil {
			err = cerr
		}
		if err != nil {
			return st, fmt.Errorf("%s: %w", name, err)
		}
		need = nil
		if unsorted {
			return st, fmt.Errorf("%s: nodes are not in id order (this cutter reads sorted files: the planet and Geofabrik's extracts are; `osmium sort` fixes another)", name)
		}
		say(fmt.Sprintf("%s: %d nodes collected", name, found))
		nodes, err := openNodes(nodePath)
		if err != nil {
			return st, err
		}
		// pass 3: the roads, cut into cells
		say(name + ": pass 3 of 3, cutting the roads")
		var missing int64
		coords := make([]float64, 0, 1024)
		err = osmpbf.Scan(pbf, opt.Workers, func(b *osmpbf.Block) error {
			for i := range b.Ways {
				w := &b.Ways[i]
				class := Class(w.Tags["highway"])
				if class == 0 || len(w.Refs) < 2 {
					continue
				}
				coords = coords[:0]
				for _, r := range w.Refs {
					lat, lon, ok := nodes.lookup(r)
					if !ok {
						missing++
						continue
					}
					coords = append(coords, lon, lat)
				}
				if len(coords) < 4 {
					continue
				}
				st.Ways++
				var flags uint8
				switch w.Tags["oneway"] {
				case "yes", "1", "-1", "true":
					flags |= FlagOneway
				}
				if v := w.Tags["tunnel"]; v != "" && v != "no" {
					flags |= FlagTunnel
				}
				if v := w.Tags["bridge"]; v != "" && v != "no" {
					flags |= FlagBridge
				}
				name := cleanName(w.Tags["name"])
				if name == "" {
					name = cleanName(w.Tags["ref"])
				}
				if err := cut(0, coords, uint8(class), flags, name, cells); err != nil {
					return err
				}
				if class <= MajorMax {
					ref := cleanName(w.Tags["ref"])
					if err := cut(1, coords, uint8(class), flags, ref, cells); err != nil {
						return err
					}
				}
			}
			return nil
		}, pct(say, name+": pass 3"))
		nodes.close()
		_ = os.Remove(nodePath)
		if err != nil {
			return st, fmt.Errorf("%s: %w", name, err)
		}
		if missing > 0 {
			say(fmt.Sprintf("%s: %d node refs not in the file (ways at the extract's edge), pieces cut short", name, missing))
		}
	}
	if err := cells.flushAll(); err != nil {
		return st, err
	}
	// pass 4: the cells become tiles
	say("writing the tiles")
	tmp := strings.TrimRight(outDir, "/") + ".tmp"
	_ = os.RemoveAll(tmp)
	for lvl := 0; lvl < Levels; lvl++ {
		if err := os.MkdirAll(filepath.Join(tmp, fmt.Sprint(lvl)), 0o755); err != nil {
			return st, err
		}
	}
	ix := NewIndex()
	last := time.Now()
	n := 0
	err := filepath.WalkDir(cellsDir, func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(d.Name(), ".raw") {
			return err
		}
		c, ok := cellOfRaw(p)
		if !ok {
			return nil
		}
		body, err := os.ReadFile(p)
		if err != nil {
			return err
		}
		count, points := countPieces(body)
		if count == 0 {
			return nil
		}
		le := binary.LittleEndian
		var hdr [13]byte
		le.PutUint32(hdr[0:], tileMagic)
		hdr[4] = byte(c.Level)
		le.PutUint16(hdr[5:], uint16(c.X))
		le.PutUint16(hdr[7:], uint16(c.Y))
		le.PutUint32(hdr[9:], uint32(count))
		out := filepath.Join(tmp, TileName(c))
		f, err := os.Create(out)
		if err != nil {
			return err
		}
		_, werr := f.Write(hdr[:])
		if werr == nil {
			_, werr = f.Write(body)
		}
		if cerr := f.Close(); werr == nil {
			werr = cerr
		}
		if werr != nil {
			return werr
		}
		ix.Set(c)
		if c.Level == 1 {
			st.MajorTiles++
		} else {
			st.FineTiles++
		}
		st.Points += int64(points)
		st.Bytes += int64(13 + len(body))
		n++
		if time.Since(last) > 10*time.Second {
			last = time.Now()
			say(fmt.Sprintf("%d tiles written", n))
		}
		return nil
	})
	if err != nil {
		return st, err
	}
	if err := os.WriteFile(filepath.Join(tmp, "index.bin"), ix.Encode(), 0o644); err != nil {
		return st, err
	}
	old := strings.TrimRight(outDir, "/") + ".old"
	_ = os.RemoveAll(old)
	if _, err := os.Stat(outDir); err == nil {
		if err := os.Rename(outDir, old); err != nil {
			return st, err
		}
	}
	if err := os.Rename(tmp, outDir); err != nil {
		return st, err
	}
	_ = os.RemoveAll(old)
	_ = os.RemoveAll(cellsDir)
	st.Took = time.Since(t0)
	return st, nil
}

// pct turns a scan's byte progress into a line now and then.
func pct(say func(string), what string) func(read, total int64) {
	last := time.Now()
	return func(read, total int64) {
		if time.Since(last) < 15*time.Second || total <= 0 {
			return
		}
		last = time.Now()
		say(fmt.Sprintf("%s: %d%%", what, read*100/total))
	}
}

// Stale reports whether the tiles under outDir are missing or older than any of the PBF files.
func Stale(pbfs []string, outDir string) bool {
	ii, err := os.Stat(filepath.Join(outDir, "index.bin"))
	if err != nil {
		return len(pbfs) > 0
	}
	for _, p := range pbfs {
		if fi, err := os.Stat(p); err == nil && fi.ModTime().After(ii.ModTime()) {
			return true
		}
	}
	return false
}

// FindPBFs lists the .osm.pbf files under dir, sorted.
func FindPBFs(dir string) []string {
	es, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range es {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".osm.pbf") {
			if fi, err := e.Info(); err == nil && fi.Size() > 128 {
				out = append(out, filepath.Join(dir, e.Name()))
			}
		}
	}
	sort.Strings(out)
	return out
}

// --- the node id bitmap ---

type bitmap struct {
	w []uint64
}

func newBitmap() *bitmap { return &bitmap{w: make([]uint64, 1<<16)} }

func (b *bitmap) set(id int64) {
	if id < 0 {
		return
	}
	i := int(id >> 6)
	if i >= len(b.w) {
		n := i + i/8 + 1024
		nw := make([]uint64, n)
		copy(nw, b.w)
		b.w = nw
	}
	b.w[i] |= 1 << uint(id&63)
}

func (b *bitmap) has(id int64) bool {
	if id < 0 {
		return false
	}
	i := int(id >> 6)
	return i < len(b.w) && b.w[i]&(1<<uint(id&63)) != 0
}

func (b *bitmap) count() int64 {
	var n int64
	for _, v := range b.w {
		for v != 0 {
			n += int64(v & 1)
			v >>= 1
		}
	}
	return n
}

// --- the node file: (id int64, lat int32, lon int32) records, sorted by id, mapped read-only ---

// putRec writes one node record (coordinates in 1e-7 degrees, OSM's own unit).
func putRec(rec []byte, id int64, lat, lon int32) {
	binary.LittleEndian.PutUint64(rec[0:], uint64(id))
	binary.LittleEndian.PutUint32(rec[8:], uint32(lat))
	binary.LittleEndian.PutUint32(rec[12:], uint32(lon))
}

type nodeFile struct {
	m []byte
	n int
}

func openNodes(path string) (*nodeFile, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if fi.Size() == 0 {
		return &nodeFile{}, nil
	}
	m, err := syscall.Mmap(int(f.Fd()), 0, int(fi.Size()), syscall.PROT_READ, syscall.MAP_SHARED)
	if err != nil {
		return nil, fmt.Errorf("mmap node file: %w", err)
	}
	return &nodeFile{m: m, n: int(fi.Size() / 16)}, nil
}

func (nf *nodeFile) close() {
	if nf.m != nil {
		_ = syscall.Munmap(nf.m)
		nf.m = nil
	}
}

// lookup finds a node's coordinates in degrees.
func (nf *nodeFile) lookup(id int64) (lat, lon float64, ok bool) {
	lo, hi := 0, nf.n
	le := binary.LittleEndian
	for lo < hi {
		mid := int(uint(lo+hi) >> 1)
		v := int64(le.Uint64(nf.m[mid*16:]))
		if v < id {
			lo = mid + 1
		} else {
			hi = mid
		}
	}
	if lo < nf.n && int64(le.Uint64(nf.m[lo*16:])) == id {
		off := lo * 16
		return float64(int32(le.Uint32(nf.m[off+8:]))) / 1e7, float64(int32(le.Uint32(nf.m[off+12:]))) / 1e7, true
	}
	return 0, 0, false
}

// --- per-cell buffers, flushed to <cells>/<level>/<xx>/<x>_<y>.raw ---

type cellBuffers struct {
	dir   string
	max   int64
	total int64
	bufs  map[Cell][]byte
}

func newCellBuffers(dir string, max int64) *cellBuffers {
	return &cellBuffers{dir: dir, max: max, bufs: map[Cell][]byte{}}
}

func rawPath(dir string, c Cell) string {
	if c.Level == 1 {
		return filepath.Join(dir, "1", fmt.Sprintf("%03d_%03d.raw", c.X, c.Y))
	}
	return filepath.Join(dir, "0", fmt.Sprintf("%02d", c.X/100), fmt.Sprintf("%04d_%04d.raw", c.X, c.Y))
}

// cellOfRaw is the cell a raw file belongs to.
func cellOfRaw(p string) (Cell, bool) {
	base := strings.TrimSuffix(filepath.Base(p), ".raw")
	var x, y int
	if _, err := fmt.Sscanf(base, "%d_%d", &x, &y); err != nil {
		return Cell{}, false
	}
	if strings.Contains(p, string(filepath.Separator)+"1"+string(filepath.Separator)) {
		return Cell{1, x, y}, true
	}
	return Cell{0, x, y}, true
}

func (cb *cellBuffers) add(c Cell, piece []byte) error {
	cb.bufs[c] = append(cb.bufs[c], piece...)
	cb.total += int64(len(piece))
	if len(cb.bufs[c]) >= 1<<20 {
		if err := cb.flush(c); err != nil {
			return err
		}
	}
	if cb.total > cb.max {
		return cb.flushAll()
	}
	return nil
}

func (cb *cellBuffers) flush(c Cell) error {
	b := cb.bufs[c]
	if len(b) == 0 {
		return nil
	}
	p := rawPath(cb.dir, c)
	if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(p, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(b)
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	cb.total -= int64(len(b))
	delete(cb.bufs, c)
	return nil
}

func (cb *cellBuffers) flushAll() error {
	for c := range cb.bufs {
		if err := cb.flush(c); err != nil {
			return err
		}
	}
	cb.total = 0
	return nil
}

// countPieces walks a tile body and returns its piece and point counts (0, 0 if malformed).
func countPieces(body []byte) (count, points int) {
	le := binary.LittleEndian
	off := 0
	for off < len(body) {
		if off+2 > len(body) {
			return 0, 0
		}
		flags := body[off+1]
		off += 2
		if flags&FlagNamed != 0 {
			if off+2 > len(body) {
				return 0, 0
			}
			off += 2 + int(le.Uint16(body[off:]))
		}
		if off+2 > len(body) {
			return 0, 0
		}
		n := int(le.Uint16(body[off:]))
		off += 2 + 4*n
		if off > len(body) {
			return 0, 0
		}
		count++
		points += n
	}
	return count, points
}

// --- cutting a way into a level's cells ---

// cut walks a way (lon,lat pairs in degrees) through the cells of a level, emitting each cell's
// piece as it leaves the cell. Every cut lies exactly on a cell border.
func cut(level int, coords []float64, class, flags uint8, name string, cells *cellBuffers) error {
	deg := cellDeg[level]
	n := len(coords) / 2
	cur := CellAt(level, coords[0], coords[1])
	run := make([]uint16, 0, 2*n+8)
	push := func(c Cell, x, y float64) {
		qx, qy := quant(c, x, y)
		if k := len(run); k >= 2 && run[k-2] == qx && run[k-1] == qy {
			return
		}
		run = append(run, qx, qy)
	}
	emit := func(c Cell) error {
		if len(run) < 4 {
			run = run[:0]
			return nil
		}
		piece := encodePiece(class, flags, name, run)
		run = run[:0]
		return cells.add(c, piece)
	}
	push(cur, coords[0], coords[1])
	for i := 1; i < n; i++ {
		ax, ay := coords[2*i-2], coords[2*i-1]
		bx, by := coords[2*i], coords[2*i+1]
		for guard := 0; guard < 100000; guard++ {
			if CellAt(level, bx, by) == cur {
				push(cur, bx, by)
				break
			}
			// leave cur along a→b: the first border crossed
			x0, y0 := cur.Lon0(), cur.Lat0()
			x1, y1 := x0+deg, y0+deg
			dx, dy := bx-ax, by-ay
			tx, ty := math.Inf(1), math.Inf(1)
			nx, ny := cur.X, cur.Y
			if dx > 0 {
				tx = (x1 - ax) / dx
				nx = cur.X + 1
			} else if dx < 0 {
				tx = (x0 - ax) / dx
				nx = cur.X - 1
			}
			if dy > 0 {
				ty = (y1 - ay) / dy
				ny = cur.Y + 1
			} else if dy < 0 {
				ty = (y0 - ay) / dy
				ny = cur.Y - 1
			}
			t := tx
			next := Cell{level, nx, cur.Y}
			if ty < tx {
				t = ty
				next = Cell{level, cur.X, ny}
			}
			if t < 0 {
				t = 0
			}
			if t > 1 {
				t = 1
			}
			ex, ey := ax+t*dx, ay+t*dy
			push(cur, ex, ey)
			if err := emit(cur); err != nil {
				return err
			}
			if next.X < 0 || next.X >= cols[level] || next.Y < 0 || next.Y >= rows[level] {
				// off the grid (the poles, the antimeridian): the way ends here
				return nil
			}
			cur = next
			push(cur, ex, ey)
			ax, ay = ex, ey
		}
	}
	return emit(cur)
}

// quant is a point's position in a cell, in Q steps.
func quant(c Cell, x, y float64) (uint16, uint16) {
	deg := cellDeg[c.Level]
	q := func(v float64) uint16 {
		f := math.Round(v / deg * Q)
		if f < 0 {
			return 0
		}
		if f > Q {
			return Q
		}
		return uint16(f)
	}
	return q(x - c.Lon0()), q(y - c.Lat0())
}

// encodePiece is a piece's bytes as they sit in a tile body.
func encodePiece(class, flags uint8, name string, pts []uint16) []byte {
	le := binary.LittleEndian
	n := len(pts) / 2
	if n > 65535 {
		n = 65535
	}
	flags &^= FlagNamed
	if name != "" {
		flags |= FlagNamed
	}
	out := make([]byte, 0, 6+len(name)+4*n)
	out = append(out, class, flags)
	if name != "" {
		out = le.AppendUint16(out, uint16(len(name)))
		out = append(out, name...)
	}
	out = le.AppendUint16(out, uint16(n))
	for i := 0; i < 2*n; i++ {
		out = le.AppendUint16(out, pts[i])
	}
	return out
}
