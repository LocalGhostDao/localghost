package framed

// The pipeline. secd streams uploads into <mount>/frames/incoming/ (bytes only, no decoding at the
// front door); this engine drains that folder one file at a time:
//
//	read -> hash -> dedupe -> EXIF -> MOVE original to archive/YYYY/MM/DD/<hash>.<ext> -> previews -> record
//
// The move is os.Rename on the same filesystem: atomic, and the archived file is byte-identical to
// what the phone sent , the raw original is never re-encoded, resized, or touched. Previews and
// thumbs are derived COPIES living next door. If anything after the move fails (preview, DB), the
// original is already safe in the archive and a rescan is idempotent (hash dedupe), so a crash
// mid-photo loses work, never a photo.
//
// Location batches arrive the same way as JSON spool files in <mount>/frames/incoming-locations/;
// each is inserted (idempotent on (ts,source)) and the affected days' GeoJSON paths are rebuilt.

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"image"
	"io"
	"io/fs"
	"image/jpeg"
	_ "image/png" // registered so image.Decode handles PNG uploads
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/exif"
	"github.com/LocalGhostDao/localghost/server/internal/geo"
)

// Dirs is the frames layout under <mount>/frames.
type Dirs struct {
	Incoming    string // raw uploads land here (secd writes, framed drains)
	IncomingLoc string // location batch spool
	Archive     string // originals, untouched, YYYY/MM/DD/<hash>.<ext>
	Preview     string // 1600px long-edge JPEG q80
	Thumb       string // 320px long-edge JPEG q80
	Paths       string // <day>.geojson
}

// DefaultDirs lays out <mount>/frames.
func DefaultDirs(mount string) Dirs {
	root := filepath.Join(mount, "frames")
	return Dirs{
		Incoming:    filepath.Join(root, "incoming"),
		IncomingLoc: filepath.Join(root, "incoming-locations"),
		Archive:     filepath.Join(root, "archive"),
		Preview:     filepath.Join(root, "preview"),
		Thumb:       filepath.Join(root, "thumb"),
		Paths:       filepath.Join(root, "paths"),
	}
}

// EnsureDirs creates the layout (idempotent, called on daemon start).
func (d Dirs) EnsureDirs() error {
	for _, p := range []string{d.Incoming, d.IncomingLoc, d.Archive, d.Preview, d.Thumb, d.Paths} {
		if err := os.MkdirAll(p, 0o750); err != nil {
			return err
		}
	}
	return nil
}

const (
	previewEdge = 1600
	thumbEdge   = 320
	jpegQuality = 80
)

// Pipeline drains intake folders and maintains the archive, previews, and day paths.
type Pipeline struct {
	dirs  Dirs
	store *Store
	log   *slog.Logger
	// Bundled ffmpeg (tools/bundle_ffmpeg.sh): binary + its private library closure ON the
	// volume, so the OS disk carries no media software. Empty = fall back to PATH.
	ffmpegBin string
	ffmpegLib string
	// mu serializes drains: the poll tick and an operator's `ghost-cli ghost.framed drain` can fire
	// concurrently, and two goroutines processing the same spool file double-read it, collide on the
	// archive rename, and write duplicate previews. Hash dedup makes it harmless to the DATA, but the
	// races are noisy and pure waste , one drain at a time.
	mu sync.Mutex
	// notifySearch, when set, hands each archived photo to the search layer (ghost.searchd ingest).
	// Best-effort by design: search indexing is derived state , a failure is logged and the photo is
	// still archived; searchd's rebuild re-covers anything missed.
	notifySearch func(archivePath string, takenAt int64)
	// resolvePlace, when non-nil, reverse-geocodes GPS frames , DB-backed (geo_points, imported by
	// `ghost-cli ghost.framed geo-import`). Nil means no geo data yet: empty place strings,
	// reprocess backfills after an import.
	resolvePlace func(lat, lon float64) geo.Place
}

func NewPipeline(dirs Dirs, store *Store, log *slog.Logger) *Pipeline {
	return &Pipeline{dirs: dirs, store: store, log: log}
}

// OnArchived registers the search-layer notify hook.
func (p *Pipeline) OnArchived(fn func(archivePath string, takenAt int64)) { p.notifySearch = fn }

// WithPlaceResolver installs the reverse geocoder function (the DB-backed store resolver).
func (p *Pipeline) WithPlaceResolver(fn func(lat, lon float64) geo.Place) { p.resolvePlace = fn }

// DrainIncoming processes every pending upload, oldest first, one at a time. Returns how many were
// processed. Called on start (resume after lock/crash) and on each poll tick.
func (p *Pipeline) DrainIncoming() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries, err := os.ReadDir(p.dirs.Incoming)
	if err != nil {
		return 0
	}
	// Oldest first so the archive order matches arrival order.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	done := 0
	failed := 0
	daysTouched := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.HasSuffix(e.Name(), ".part") {
			// .part = secd still writing , normally renamed on completion. But if secd died mid-spool
			// (crash, lock during upload) the .part is orphaned FOREVER: framed skips .part files, so
			// without this sweep they accumulate on the volume until the disk guard notices. An hour
			// is generous , no legitimate spool write takes that long (4GiB cap / any sane link).
			if fi, ferr := e.Info(); ferr == nil && time.Since(fi.ModTime()) > time.Hour {
				if rerr := os.Remove(filepath.Join(p.dirs.Incoming, e.Name())); rerr == nil {
					p.log.Info("swept orphaned .part (secd died mid-spool)", "fn", "DrainIncoming", "name", e.Name(), "age", time.Since(fi.ModTime()).Round(time.Minute).String())
				}
			}
			continue
		}
		day, err := p.processOne(filepath.Join(p.dirs.Incoming, e.Name()))
		if err != nil {
			// Rate-limit failure logging: with a large stuck backlog (e.g. a systemic permission
			// problem across thousands of files), one line per file per tick wrote GIGABYTES of
			// identical warnings. First few get detail; the rest become one summary line below.
			failed++
			if failed <= 3 {
				p.log.Warn("process failed, leaving in incoming for retry", "fn", "DrainIncoming",
					"file", e.Name(), "err", err)
			}
			continue
		}
		if day != "" {
			daysTouched[day] = true
		}
		done++
	}
	if failed > 3 {
		p.log.Warn("drain failures suppressed", "fn", "DrainIncoming", "failedTotal", failed,
			"note", "first 3 logged in detail; same root cause likely for all")
	}
	for day := range daysTouched {
		p.RebuildDay(day)
	}
	return done
}

// processOne handles a single upload. Returns the YYYY-MM-DD day the photo belongs to (for path
// rebuild), or "" if the file was a duplicate or not an image.
func (p *Pipeline) processOne(path string) (string, error) {
	// MEMORY DISCIPLINE: never load a whole file blind. Videos run to hundreds of MB (upload cap is
	// 4GiB), and the old ReadFile here meant one long clip could balloon the daemon's heap on a box
	// that also needs RAM for the model. Everything cheap works from a STREAM or the HEAD: the hash
	// streams, the sniffer needs 12 bytes, EXIF lives in the leading segment. Only PHOTOS (tens of MB
	// at worst, needed in full for preview decode) get read whole , and only after the sniff says so.
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	fileBytes := fi.Size()

	src, err := os.Open(path)
	if err != nil {
		return "", err
	}
	head := make([]byte, 64<<10) // sniff needs 12 bytes; JPEG/HEIC EXIF sits in the leading segment
	n, _ := io.ReadFull(src, head)
	head = head[:n]

	hasher := sha256.New()
	hasher.Write(head)
	if _, err := io.Copy(hasher, src); err != nil { // stream the rest , constant memory at any size
		_ = src.Close()
		return "", fmt.Errorf("hash stream: %w", err)
	}
	_ = src.Close()
	hash := hex.EncodeToString(hasher.Sum(nil)[:16]) // 128 bits is plenty for a personal archive's identity

	if dup, err := p.store.HasFrame(hash); err == nil && dup {
		// Already archived (re-sent upload). Drop the incoming copy; the archive has the bytes.
		_ = os.Remove(path)
		p.log.Debug("duplicate dropped", "fn", "processOne", "hash", hash)
		return "", nil
	}

	// Sniff from the head, then load FULL bytes only for photos (preview decode needs pixels).
	sniff := Sniff(head)
	var raw []byte
	if sniff.Kind == KindPhoto || sniff.Kind == KindUnknown {
		if raw, err = os.ReadFile(path); err != nil {
			return "", err
		}
	}

	// Decode config only , cheap validation + format sniff without decoding pixels yet.
	cfgFmt := ""
	if raw != nil {
		if _, f, derr := image.DecodeConfig(bytes.NewReader(raw)); derr == nil {
			cfgFmt = f
		}
	}
	meta := exif.Parse(head) // EXIF lives in the leading segment; zero values if absent

	taken := meta.TakenAt
	takenSrc := "exif" // how taken was determined , recorded so consumers know what to trust
	if taken.IsZero() {
		// The spool NAME may carry the phone's taken-timestamp hint (…-t<epochMillis>, appended by
		// secd from the X-Ghost-Taken header). This is the primary fallback for VIDEOS , they have no
		// EXIF, and their container (moov atom) parser is future work , and for stills whose EXIF was
		// stripped. Hint over mtime: mtime is when the UPLOAD landed, the hint is when it was SHOT.
		if ms := takenHintFromName(path); ms > 0 {
			taken = time.UnixMilli(ms).UTC()
			takenSrc = "hint"
		}
	}
	if taken.IsZero() {
		// Fall back to file mtime (the upload's) , honest approximation, better than epoch. Recorded
		// as "mtime" so the where-was-I query NEVER trusts it: an mtime taken_at is upload time, and
		// one such row would poison MAX(taken_at) and make the phone skip its whole remaining roll.
		takenSrc = "mtime"
		if fi, err := os.Stat(path); err == nil {
			taken = fi.ModTime().UTC()
		} else {
			taken = time.Now().UTC()
		}
	}
	day := taken.UTC().Format("2006-01-02")

	// MOVE the original, untouched, into the archive. Same filesystem, so os.Rename is atomic , the
	// photo is never in two places or zero places.
	// sniff (from the head, above) is the authority for kind; cfgFmt for the preview path.
	ext := sniff.Ext
	if ext == ".bin" && cfgFmt != "" {
		// image.DecodeConfig recognised a still the sniffer did not name , prefer its extension.
		ext = extFor(cfgFmt, path)
	}
	archDir := filepath.Join(p.dirs.Archive, taken.UTC().Format("2006/01/02"))
	if err := os.MkdirAll(archDir, 0o750); err != nil {
		return "", err
	}
	archPath := filepath.Join(archDir, hash+ext)
	if err := os.Rename(path, archPath); err != nil {
		return "", err
	}

	// Derived previews. A failure here is logged, not fatal , the original is safe, previews can be
	// regenerated by the reprocess command. Only stills get image previews; video thumbnailing (a
	// frame grab) is a separate capability, not attempted here.
	prevPath, thumbPath := "", ""
	kindStr := "unknown"
	switch sniff.Kind {
	case KindPhoto:
		kindStr = "photo"
		if cfgFmt == "jpeg" || cfgFmt == "png" {
			prevPath, thumbPath = p.makePreviews(raw, hash, meta.Orientation)
		} else {
			p.log.Info("photo archived without preview (decoder does not handle this still format)",
				"fn", "processOne", "hash", hash, "mime", sniff.MIME)
		}
	case KindVideo:
		kindStr = "video"
		// FRAME GRAB , one second in, one frame out, through ffmpeg if the box has it. Best
		// effort by design: no ffmpeg means videos keep their play glyph and everything else
		// works; with it, the gallery and map treat videos as first-class citizens. The grab
		// feeds the SAME makePreviews path as photos, so preview + thumb + upright all apply.
		if jpg, gerr := p.grabVideoFrame(archPath); gerr != nil {
			p.log.Info("video archived (no still preview)", "fn", "processOne", "hash", hash, "mime", sniff.MIME, "note", gerr.Error())
		} else {
			prevPath, thumbPath = p.makePreviews(jpg, hash, 1)
		}
	default:
		p.log.Warn("archived unrecognised media type", "fn", "processOne", "hash", hash, "ext", ext)
	}

	place := ""
	if meta.HasGPS && p.resolvePlace != nil {
		place = p.resolvePlace(meta.Lat, meta.Lon).String()
	}
	f := Frame{
		Hash: hash, TakenAt: taken.UTC().Unix(), Place: place,
		Lat: meta.Lat, Lon: meta.Lon, HasGPS: meta.HasGPS,
		ArchivePath: archPath, PreviewPath: prevPath, ThumbPath: thumbPath,
		Bytes: fileBytes, Source: "phone", ReceivedAt: time.Now().UTC().Unix(),
		Kind: kindStr, MIME: sniff.MIME, TakenSrc: takenSrc,
	}

	// The JOURNAL ENTRY , framed's line in the shared ingestion diary synthd distills from. Written
	// with what framed knows at archive time (kind, when, where); captions and tags arrive later
	// through other daemons and enrich the memory at distillation, not the entry.
	{
		when := time.Unix(f.TakenAt, 0).UTC().Format("Mon Jan 2 2006, 15:04")
		title := f.Kind + " archived , " + when
		body := "A " + f.Kind + " from " + when + "."
		if f.Place != "" {
			body = "A " + f.Kind + " taken at " + f.Place + " on " + when + "."
			title = f.Kind + " at " + f.Place
		}
		if jerr := p.store.InsertJournal(f.Hash, f.TakenAt, title, body); jerr != nil {
			p.log.Warn("journal entry failed", "fn", "processOne", "hash", f.Hash, "err", jerr)
		}
	}
	if err := p.store.InsertFrame(f); err != nil {
		// Archive holds the bytes; the record can be replayed by reprocess. Loud, not fatal.
		p.log.Error("frame record failed (photo IS archived; run reprocess)", "fn", "processOne",
			"hash", hash, "err", err)
	}
	p.log.Info("archived", "fn", "processOne", "hash", hash, "day", day, "gps", meta.HasGPS,
		"bytes", len(raw))
	if p.notifySearch != nil {
		p.notifySearch(archPath, taken.UTC().Unix())
	}
	if meta.HasGPS {
		return day, nil
	}
	return "", nil
}

// makePreviews decodes once and writes the 1600px preview and 320px thumb as JPEG q80.
func (p *Pipeline) makePreviews(raw []byte, hash string, orientation int) (prev, thumb string) {
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		p.log.Warn("preview decode failed", "fn", "makePreviews", "hash", hash, "err", err)
		return "", ""
	}
	// JPEG first (pure Go, always works), then converted to WebP when cwebp is on the box , Go has no
	// WebP ENCODER (stdlib and x/image decode only; pure-Go third-party encoders are lossless-only,
	// which is LARGER than JPEG for photos). cwebp is one `apt install webp` away; when absent the
	// gallery serves the JPEGs and nothing breaks. ~30% smaller thumbs when present.
	write := func(dir string, edge int) string {
		out := filepath.Join(dir, hash+".jpg")
		f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
		if err != nil {
			return ""
		}
		// Downscale FIRST, orient the small image , the axis-aligned transforms commute with the
		// box average, and a 1600px rotate costs a fraction of a 12MP one. The ORIGINAL in the
		// archive keeps its bytes and its EXIF untouched, as always; only the derived copies are
		// baked to display orientation (they carry no EXIF, so baking is the only correct option).
		if err := jpeg.Encode(f, applyOrientation(downscale(img, edge), orientation), &jpeg.Options{Quality: jpegQuality}); err != nil {
			_ = f.Close()
			return ""
		}
		_ = f.Close()
		if webp := toWebP(out); webp != "" {
			_ = os.Remove(out) // webp replaced it; keep exactly one preview file per size
			return webp
		}
		return out
	}
	return write(p.dirs.Preview, previewEdge), write(p.dirs.Thumb, thumbEdge)
}

// toWebP converts a JPEG on disk to WebP next to it using the cwebp binary. Empty string when cwebp
// is not installed or fails , the caller keeps the JPEG. Deliberately an OPTIONAL enhancer: no Go
// dependency, no hard requirement on the box, one `apt install webp` turns it on.
func toWebP(jpgPath string) string {
	bin, err := exec.LookPath("cwebp")
	if err != nil {
		return ""
	}
	out := strings.TrimSuffix(jpgPath, ".jpg") + ".webp"
	cmd := exec.Command(bin, "-quiet", "-q", "78", jpgPath, "-o", out)
	if err := cmd.Run(); err != nil {
		_ = os.Remove(out)
		return ""
	}
	return out
}

// locBatch is the spool file secd writes for a location upload.
type locBatch struct {
	Source string       `json:"source"`
	Points []TrackPoint `json:"points"`
}

// DrainLocations ingests spooled location batches and rebuilds the days they touch.
func (p *Pipeline) DrainLocations() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	entries, err := os.ReadDir(p.dirs.IncomingLoc)
	if err != nil {
		return 0
	}
	done := 0
	failed := 0
	daysTouched := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".part") {
			continue
		}
		full := filepath.Join(p.dirs.IncomingLoc, e.Name())
		b, err := os.ReadFile(full)
		if err != nil {
			// Same rate-limited visibility as DrainIncoming: a systemic problem (permissions) across
			// many spooled batches must not be silent, and must not flood either.
			failed++
			if failed <= 3 {
				p.log.Warn("location batch unreadable, leaving for retry", "fn", "DrainLocations",
					"file", e.Name(), "err", err)
			}
			continue
		}
		var batch locBatch
		if err := json.Unmarshal(b, &batch); err != nil {
			p.log.Warn("bad location batch, dropping", "fn", "DrainLocations", "file", e.Name(), "err", err)
			_ = os.Remove(full)
			continue
		}
		src := batch.Source
		if src == "" {
			src = "watch"
		}
		if err := p.store.InsertPoints(src, batch.Points); err != nil {
			p.log.Warn("point insert failed, leaving for retry", "fn", "DrainLocations", "err", err)
			continue
		}
		_ = os.Remove(full)
		for _, pt := range batch.Points {
			daysTouched[time.Unix(pt.TS, 0).UTC().Format("2006-01-02")] = true
		}
		done++
	}
	if failed > 3 {
		p.log.Warn("location drain failures suppressed", "fn", "DrainLocations", "failedTotal", failed)
	}
	for day := range daysTouched {
		p.RebuildDay(day)
	}
	return done
}

// RebuildDay reassembles one day's GeoJSON from the stored points and geotagged frames. Idempotent,
// full rewrite each time , a day is small, correctness beats cleverness.
func (p *Pipeline) RebuildDay(day string) {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return
	}
	start := t.UTC().Unix()
	end := t.UTC().Add(24 * time.Hour).Unix()
	pts, err := p.store.DayPoints(start, end)
	if err != nil {
		p.log.Warn("day points query failed", "fn", "rebuildDay", "day", day, "err", err)
		return
	}
	photos, err := p.store.DayPhotos(start, end)
	if err != nil {
		p.log.Warn("day photos query failed", "fn", "rebuildDay", "day", day, "err", err)
		return
	}
	if len(pts) == 0 && len(photos) == 0 {
		return
	}
	doc, err := BuildDayPath(t, pts, photos)
	if err != nil {
		return
	}
	out := filepath.Join(p.dirs.Paths, day+".geojson")
	if err := os.WriteFile(out, doc, 0o640); err != nil {
		p.log.Warn("path write failed", "fn", "rebuildDay", "day", day, "err", err)
		return
	}
	p.log.Info("day path rebuilt", "fn", "rebuildDay", "day", day, "trackPoints", len(pts),
		"photoPoints", len(photos))
}

// PendingCounts reports the intake backlog for the ctlsock queue command.
func (p *Pipeline) PendingCounts() (frames, locations int) {
	if es, err := os.ReadDir(p.dirs.Incoming); err == nil {
		for _, e := range es {
			if !e.IsDir() && !strings.HasSuffix(e.Name(), ".part") {
				frames++
			}
		}
	}
	if es, err := os.ReadDir(p.dirs.IncomingLoc); err == nil {
		for _, e := range es {
			if !e.IsDir() && !strings.HasSuffix(e.Name(), ".part") {
				locations++
			}
		}
	}
	return
}

func extFor(format, orig string) string {
	switch format {
	case "jpeg":
		return ".jpg"
	case "png":
		return ".png"
	default:
		if e := strings.ToLower(filepath.Ext(orig)); e != "" && len(e) <= 6 {
			return e
		}
		return ".bin"
	}
}

// takenHintFromName extracts the -t<epochMillis> suffix secd appends to spool names when the phone
// supplied X-Ghost-Taken. Zero when absent or malformed.
func takenHintFromName(path string) int64 {
	base := filepath.Base(path)
	i := strings.LastIndex(base, "-t")
	if i < 0 || i+2 >= len(base) {
		return 0
	}
	ms, err := strconv.ParseInt(base[i+2:], 10, 64)
	if err != nil || ms <= 0 {
		return 0
	}
	return ms
}

// Reprocess walks the ARCHIVE and converges every derived thing back to the current code: frame
// records re-inserted (InsertFrame is ON CONFLICT DO NOTHING, so existing rows cost one no-op),
// previews and thumbs re-derived, the search layer re-notified, and every GPS day's path rebuilt.
// This is the command the pipeline's own comments promised for months ("photo IS archived; run
// reprocess") before it existed. Two failure modes drove finally writing it, both observed live:
// a degraded-DB window archived hours of photos whose records and search notifies all failed , and
// searchd's rebuild CANNOT cover that, it walks search.originals and regenerates derived state, it
// does not discover archive files that never got ingested , and the EXIF orientation fix landed
// with every pre-existing portrait thumb baked sideways.
//
// forcePreviews re-derives even when the preview files exist (the orientation-fix case: the files
// are there, they are just wrong). Preview files are hash-named, so re-derivation overwrites in
// place and existing DB paths stay valid. Originals are READ, never written , the archive-untouched
// rule holds here as everywhere.
//
// Runs under the drain mutex: reprocess and a spool drain racing the same store is noise we do not
// need. Bounded work per file (head read for sniff+EXIF; full read only for photos), progress
// logged every 200 so a multi-thousand-photo pass is visible, not silent.
func (p *Pipeline) Reprocess(forcePreviews bool) (scanned, recorded, previewed, notified int) {
	p.mu.Lock()
	defer p.mu.Unlock()
	gpsDays := map[string]bool{}
	_ = filepath.WalkDir(p.dirs.Archive, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() {
			return nil
		}
		scanned++
		if scanned%200 == 0 {
			p.log.Info("reprocess progress", "fn", "Reprocess", "scanned", scanned,
				"recorded", recorded, "previewed", previewed)
		}
		ext := filepath.Ext(d.Name())
		hash := strings.TrimSuffix(d.Name(), ext)
		fi, serr := os.Stat(path)
		if serr != nil {
			return nil
		}
		f, ferr := os.Open(path)
		if ferr != nil {
			p.log.Warn("reprocess: unreadable, skipped", "fn", "Reprocess", "path", path, "err", ferr)
			return nil
		}
		head := make([]byte, 64*1024)
		n, _ := io.ReadFull(f, head)
		_ = f.Close()
		head = head[:n]
		sn := Sniff(head)
		meta := exif.Parse(head)

		taken := meta.TakenAt
		takenSrc := "exif"
		if taken.IsZero() {
			// The archive PATH is the day processOne filed it under , derived from the best taken
			// source available at archive time (exif, then the spool-name hint, then mtime). The
			// spool name is gone (renamed to <hash><ext>), so the path IS the surviving record of
			// that decision; midnight UTC of its day is the honest reconstruction.
			rel, rerr := filepath.Rel(p.dirs.Archive, path)
			if rerr == nil {
				if ts, perr := time.Parse("2006/01/02", filepath.ToSlash(filepath.Dir(rel))); perr == nil {
					taken = ts.UTC()
					takenSrc = "archive-path"
				}
			}
		}
		if taken.IsZero() {
			taken = fi.ModTime().UTC()
			takenSrc = "mtime"
		}

		kindStr := "unknown"
		prevPath, thumbPath := "", ""
		switch sn.Kind {
		case KindPhoto:
			kindStr = "photo"
			existingPrev := filepath.Join(p.dirs.Preview, hash+".jpg")
			existingPrevWebp := filepath.Join(p.dirs.Preview, hash+".webp")
			have := fileExists(existingPrev) || fileExists(existingPrevWebp)
			if forcePreviews || !have {
				if raw, rerr := os.ReadFile(path); rerr == nil {
					prevPath, thumbPath = p.makePreviews(raw, hash, meta.Orientation)
					if prevPath != "" {
						previewed++
					}
				} else {
					p.log.Warn("reprocess: photo unreadable for previews", "fn", "Reprocess", "path", path, "err", rerr)
				}
			} else {
				if fileExists(existingPrevWebp) {
					prevPath = existingPrevWebp
				} else {
					prevPath = existingPrev
				}
				tj, tw := filepath.Join(p.dirs.Thumb, hash+".jpg"), filepath.Join(p.dirs.Thumb, hash+".webp")
				if fileExists(tw) {
					thumbPath = tw
				} else if fileExists(tj) {
					thumbPath = tj
				}
			}
		case KindVideo:
			kindStr = "video"
			existingThumb := fileExists(filepath.Join(p.dirs.Thumb, hash+".jpg")) ||
				fileExists(filepath.Join(p.dirs.Thumb, hash+".webp"))
			if forcePreviews || !existingThumb {
				if jpg, gerr := p.grabVideoFrame(path); gerr == nil {
					prevPath, thumbPath = p.makePreviews(jpg, hash, 1)
					if thumbPath != "" {
						previewed++
					}
				}
			} else {
				tj, tw := filepath.Join(p.dirs.Thumb, hash+".jpg"), filepath.Join(p.dirs.Thumb, hash+".webp")
				if fileExists(tw) {
					thumbPath = tw
				} else if fileExists(tj) {
					thumbPath = tj
				}
				pj, pw := filepath.Join(p.dirs.Preview, hash+".jpg"), filepath.Join(p.dirs.Preview, hash+".webp")
				if fileExists(pw) {
					prevPath = pw
				} else if fileExists(pj) {
					prevPath = pj
				}
			}
		}

		place := ""
		if meta.HasGPS && p.resolvePlace != nil {
			place = p.resolvePlace(meta.Lat, meta.Lon).String()
		}
		frame := Frame{
			Hash: hash, TakenAt: taken.UTC().Unix(), Place: place,
			Lat: meta.Lat, Lon: meta.Lon, HasGPS: meta.HasGPS,
			ArchivePath: path, PreviewPath: prevPath, ThumbPath: thumbPath,
			Bytes: fi.Size(), Source: "reprocess", ReceivedAt: time.Now().UTC().Unix(),
			Kind: kindStr, MIME: sn.MIME, TakenSrc: takenSrc,
		}

	// The JOURNAL ENTRY , framed's line in the shared ingestion diary synthd distills from. Written
	// with what framed knows at archive time (kind, when, where); captions and tags arrive later
	// through other daemons and enrich the memory at distillation, not the entry.
	{
		when := time.Unix(frame.TakenAt, 0).UTC().Format("Mon Jan 2 2006, 15:04")
		title := frame.Kind + " archived , " + when
		body := "A " + frame.Kind + " from " + when + "."
		if frame.Place != "" {
			body = "A " + frame.Kind + " taken at " + frame.Place + " on " + when + "."
			title = frame.Kind + " at " + frame.Place
		}
		if jerr := p.store.InsertJournal(frame.Hash, frame.TakenAt, title, body); jerr != nil {
			p.log.Warn("journal entry failed", "fn", "Reprocess", "hash", frame.Hash, "err", jerr)
		}
	}
		if err := p.store.InsertFrame(frame); err != nil {
			p.log.Warn("reprocess: frame record failed", "fn", "Reprocess", "hash", hash, "err", err)
		} else {
			recorded++
		}
		if p.notifySearch != nil && kindStr == "photo" {
			p.notifySearch(path, taken.UTC().Unix())
			notified++
		}
		if meta.HasGPS {
			gpsDays[taken.UTC().Format("2006-01-02")] = true
		}
		return nil
	})
	for day := range gpsDays {
		p.RebuildDay(day)
	}
	p.log.Info("reprocess complete", "fn", "Reprocess", "scanned", scanned, "recorded", recorded,
		"previewed", previewed, "notified", notified, "gpsDays", len(gpsDays))
	return scanned, recorded, previewed, notified
}

func fileExists(p string) bool {
	_, err := os.Stat(p)
	return err == nil
}

// SetFFmpeg points the pipeline at a bundled ffmpeg (binary + private lib dir). Set when the
// bundle exists; the volume's copy always outranks whatever the OS happens to have.
func (p *Pipeline) SetFFmpeg(bin, lib string) { p.ffmpegBin, p.ffmpegLib = bin, lib }

// grabVideoFrame pulls one frame (t=1s, falling back to t=0 for sub-second clips) as JPEG bytes.
// Bundled ffmpeg first (with ITS libraries via LD_LIBRARY_PATH); PATH as the fallback.
func (p *Pipeline) grabVideoFrame(videoPath string) ([]byte, error) {
	ff, env := p.ffmpegBin, []string(nil)
	if ff != "" {
		env = append(os.Environ(), "LD_LIBRARY_PATH="+p.ffmpegLib)
	} else {
		var err error
		ff, err = exec.LookPath("ffmpeg")
		if err != nil {
			return nil, fmt.Errorf("no ffmpeg (bundle one: tools/bundle_ffmpeg.sh <mount>)")
		}
	}
	for _, seek := range []string{"1", "0"} {
		cmd := exec.Command(ff, "-hide_banner", "-loglevel", "error",
			"-ss", seek, "-i", videoPath, "-frames:v", "1", "-f", "image2", "-c:v", "mjpeg", "pipe:1")
		cmd.Env = env
		out, cerr := cmd.Output()
		if cerr == nil && len(out) > 0 {
			return out, nil
		}
	}
	return nil, fmt.Errorf("ffmpeg produced no frame")
}
