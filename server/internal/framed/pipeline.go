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
	"image"
	"image/jpeg"
	_ "image/png" // registered so image.Decode handles PNG uploads
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/exif"
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
	// notifySearch, when set, hands each archived photo to the search layer (ghost.searchd ingest).
	// Best-effort by design: search indexing is derived state , a failure is logged and the photo is
	// still archived; searchd's rebuild re-covers anything missed.
	notifySearch func(archivePath string, takenAt int64)
}

func NewPipeline(dirs Dirs, store *Store, log *slog.Logger) *Pipeline {
	return &Pipeline{dirs: dirs, store: store, log: log}
}

// OnArchived registers the search-layer notify hook.
func (p *Pipeline) OnArchived(fn func(archivePath string, takenAt int64)) { p.notifySearch = fn }

// DrainIncoming processes every pending upload, oldest first, one at a time. Returns how many were
// processed. Called on start (resume after lock/crash) and on each poll tick.
func (p *Pipeline) DrainIncoming() int {
	entries, err := os.ReadDir(p.dirs.Incoming)
	if err != nil {
		return 0
	}
	// Oldest first so the archive order matches arrival order.
	sort.Slice(entries, func(i, j int) bool { return entries[i].Name() < entries[j].Name() })
	done := 0
	daysTouched := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".part") {
			continue // .part = secd still writing; picked up next tick once renamed
		}
		day, err := p.processOne(filepath.Join(p.dirs.Incoming, e.Name()))
		if err != nil {
			p.log.Warn("process failed, leaving in incoming for retry", "fn", "DrainIncoming",
				"file", e.Name(), "err", err)
			continue
		}
		if day != "" {
			daysTouched[day] = true
		}
		done++
	}
	for day := range daysTouched {
		p.RebuildDay(day)
	}
	return done
}

// processOne handles a single upload. Returns the YYYY-MM-DD day the photo belongs to (for path
// rebuild), or "" if the file was a duplicate or not an image.
func (p *Pipeline) processOne(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	hash := hex.EncodeToString(sum[:16]) // 128 bits is plenty for a personal archive's identity

	if dup, err := p.store.HasFrame(hash); err == nil && dup {
		// Already archived (re-sent upload). Drop the incoming copy; the archive has the bytes.
		_ = os.Remove(path)
		p.log.Debug("duplicate dropped", "fn", "processOne", "hash", hash)
		return "", nil
	}

	// Decode config only , cheap validation + format sniff without decoding pixels yet.
	cfgFmt := ""
	if _, f, err := image.DecodeConfig(bytes.NewReader(raw)); err == nil {
		cfgFmt = f
	}
	meta := exif.Parse(raw) // zero values if absent; a photo without EXIF still archives

	taken := meta.TakenAt
	if taken.IsZero() {
		// Fall back to file mtime (the upload's) , honest approximation, better than epoch.
		if fi, err := os.Stat(path); err == nil {
			taken = fi.ModTime().UTC()
		} else {
			taken = time.Now().UTC()
		}
	}
	day := taken.UTC().Format("2006-01-02")

	// MOVE the original, untouched, into the archive. Same filesystem, so os.Rename is atomic , the
	// photo is never in two places or zero places.
	// Identify the file by its CONTENT, not its (extensionless) spool name. This decides photo vs
	// video, the archive extension, and whether image previews even apply.
	sniff := Sniff(raw)
	// cfgFmt still comes from the image decoder for the preview path; sniff is the authority for kind.
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
			prevPath, thumbPath = p.makePreviews(raw, hash)
		} else {
			p.log.Info("photo archived without preview (decoder does not handle this still format)",
				"fn", "processOne", "hash", hash, "mime", sniff.MIME)
		}
	case KindVideo:
		kindStr = "video"
		p.log.Info("video archived (no still preview)", "fn", "processOne", "hash", hash, "mime", sniff.MIME)
	default:
		p.log.Warn("archived unrecognised media type", "fn", "processOne", "hash", hash, "ext", ext)
	}

	f := Frame{
		Hash: hash, TakenAt: taken.UTC().Unix(),
		Lat: meta.Lat, Lon: meta.Lon, HasGPS: meta.HasGPS,
		ArchivePath: archPath, PreviewPath: prevPath, ThumbPath: thumbPath,
		Bytes: int64(len(raw)), Source: "phone", ReceivedAt: time.Now().UTC().Unix(),
		Kind: kindStr, MIME: sniff.MIME,
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
func (p *Pipeline) makePreviews(raw []byte, hash string) (prev, thumb string) {
	img, _, err := image.Decode(bytes.NewReader(raw))
	if err != nil {
		p.log.Warn("preview decode failed", "fn", "makePreviews", "hash", hash, "err", err)
		return "", ""
	}
	write := func(dir string, edge int) string {
		out := filepath.Join(dir, hash+".jpg")
		f, err := os.OpenFile(out, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
		if err != nil {
			return ""
		}
		defer f.Close()
		if err := jpeg.Encode(f, downscale(img, edge), &jpeg.Options{Quality: jpegQuality}); err != nil {
			return ""
		}
		return out
	}
	return write(p.dirs.Preview, previewEdge), write(p.dirs.Thumb, thumbEdge)
}

// locBatch is the spool file secd writes for a location upload.
type locBatch struct {
	Source string       `json:"source"`
	Points []TrackPoint `json:"points"`
}

// DrainLocations ingests spooled location batches and rebuilds the days they touch.
func (p *Pipeline) DrainLocations() int {
	entries, err := os.ReadDir(p.dirs.IncomingLoc)
	if err != nil {
		return 0
	}
	done := 0
	daysTouched := map[string]bool{}
	for _, e := range entries {
		if e.IsDir() || strings.HasSuffix(e.Name(), ".part") {
			continue
		}
		full := filepath.Join(p.dirs.IncomingLoc, e.Name())
		b, err := os.ReadFile(full)
		if err != nil {
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
