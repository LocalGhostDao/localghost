package framed

// Store records frames and location points in Postgres via the native poltergres client (extended
// protocol, parameterized). Every value travels as a bound parameter, NEVER interpolated into SQL ,
// so the sqlQuote string-building this file used to carry is gone, and injection is structurally
// impossible rather than something we guard against. The store holds a ghost_rw connection (it writes
// frames and points); reads use the same connection.

import (
	"bufio"
	"fmt"
	"github.com/LocalGhostDao/localghost/server/internal/outings"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/geo"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// Frame is one archived photo's record.
type Frame struct {
	Hash        string
	TakenAt     int64
	Lat, Lon    float64
	Place       string // reverse-geocoded hierarchy; "" until geo data exists
	HasGPS      bool
	ArchivePath string
	PreviewPath string
	ThumbPath   string
	Bytes       int64
	Source      string
	ReceivedAt  int64
	Kind        string // "photo" | "video" | "unknown", from content sniffing
	MIME        string // best-effort content type, e.g. "image/jpeg", "video/mp4"
	TakenSrc    string // "exif" | "hint" | "mtime" , how TakenAt was determined; mtime is NOT trusted for sync resume
	// Which phone uploaded this , empty for older rows and local imports (unknown, not guessed).
	Device string
}

// Store wraps a ghost_rw poltergres connection for one slot's database.
type Store struct {
	db *poltergres.ReadWrite
}

// NewStore builds the store. sockDir is the pg socket directory (<mount>/postgres, i.e.
// hw.SocketForMount(mount)); poltergres appends the .s.PGSQL.<port> socket filename. One convention across
// the box: the caller resolves the socket DIR, poltergres owns the socket FILENAME. Connects as ghost_rw.
func NewStore(sockDir string, port int, rwUser, rwPass, dbName string) *Store {
	return &Store{db: poltergres.NewReadWrite(sockDir, port, rwUser, rwPass, dbName)}
}

// NewStoreDB wraps an existing connection (tests against a real Postgres share one).
func NewStoreDB(db *poltergres.ReadWrite) *Store { return &Store{db: db} }

// Ping verifies the connection (and thus that ghost_rw can authenticate).
func (s *Store) Ping() error { return s.db.Ping() }

// InsertFrame records one archived photo. The hash is the identity, so re-uploading the same photo
// is a no-op , but a REPROCESS is not a re-upload: it is the current code re-reading the original,
// and when the code got better (a longer metadata head, a tolerant EXIF parser, a decoder the box
// did not have last time) the row must be allowed to improve. Every column below converges
// MONOTONICALLY: a fact replaces an absence, a stronger source replaces a weaker one, and nothing
// a person or an earlier ingest knew is ever overwritten by "unknown". The old clause backfilled
// place only, which meant a frame archived without GPS stayed off the map forever, however many
// times reprocess re-read its coordinates. The kind, the type and the archive path are the one
// exception to "never replace a fact": they are read from the bytes by the current sniff, which is
// the authority (a HEIC the old sniff called a video is a photo, and is renamed to .heic).
func (s *Store) InsertFrame(f Frame) error {
	return s.db.Exec(
		`INSERT INTO frames (hash, taken_at, lat, lon, has_gps, archive_path, preview_path, thumb_path, bytes, source, received_at, kind, mime, taken_src, place, device, pipe_ver)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17)
		 ON CONFLICT (hash) DO UPDATE SET
		   pipe_ver = GREATEST(frames.pipe_ver, EXCLUDED.pipe_ver),
		   place = CASE WHEN frames.place = '' AND EXCLUDED.place <> '' THEN EXCLUDED.place ELSE frames.place END,
		   lat = CASE WHEN NOT frames.has_gps AND EXCLUDED.has_gps THEN EXCLUDED.lat ELSE frames.lat END,
		   lon = CASE WHEN NOT frames.has_gps AND EXCLUDED.has_gps THEN EXCLUDED.lon ELSE frames.lon END,
		   has_gps = frames.has_gps OR EXCLUDED.has_gps,
		   taken_at = CASE WHEN `+takenRank("EXCLUDED")+` > `+takenRank("frames")+` THEN EXCLUDED.taken_at ELSE frames.taken_at END,
		   taken_src = CASE WHEN `+takenRank("EXCLUDED")+` > `+takenRank("frames")+` THEN EXCLUDED.taken_src ELSE frames.taken_src END,
		   preview_path = CASE WHEN frames.preview_path = '' AND EXCLUDED.preview_path <> '' THEN EXCLUDED.preview_path ELSE frames.preview_path END,
		   thumb_path = CASE WHEN frames.thumb_path = '' AND EXCLUDED.thumb_path <> '' THEN EXCLUDED.thumb_path ELSE frames.thumb_path END,
		   kind = CASE WHEN EXCLUDED.kind <> 'unknown' THEN EXCLUDED.kind ELSE frames.kind END,
		   mime = CASE WHEN EXCLUDED.mime <> '' THEN EXCLUDED.mime ELSE frames.mime END,
		   archive_path = CASE WHEN EXCLUDED.archive_path <> '' THEN EXCLUDED.archive_path ELSE frames.archive_path END,
		   device = CASE WHEN frames.device = '' AND EXCLUDED.device <> '' THEN EXCLUDED.device ELSE frames.device END
		 WHERE (frames.place = '' AND EXCLUDED.place <> '')
		    OR (NOT frames.has_gps AND EXCLUDED.has_gps)
		    OR (`+takenRank("EXCLUDED")+` > `+takenRank("frames")+`)
		    OR (frames.preview_path = '' AND EXCLUDED.preview_path <> '')
		    OR (frames.thumb_path = '' AND EXCLUDED.thumb_path <> '')
		    OR (frames.kind <> EXCLUDED.kind AND EXCLUDED.kind <> 'unknown')
		    OR (frames.mime <> EXCLUDED.mime AND EXCLUDED.mime <> '')
		    OR (frames.archive_path <> EXCLUDED.archive_path AND EXCLUDED.archive_path <> '')
		    OR (frames.device = '' AND EXCLUDED.device <> '')
		    OR (frames.pipe_ver < EXCLUDED.pipe_ver)`,
		f.Hash, f.TakenAt, f.Lat, f.Lon, f.HasGPS,
		f.ArchivePath, f.PreviewPath, f.ThumbPath, f.Bytes, f.Source, f.ReceivedAt, f.Kind, f.MIME, f.TakenSrc, f.Place, f.Device, PipelineVersion)
}

// ForgetFrame removes every row the box keeps for a frame: searchd's original (its chunks go with
// it) and the jobs queued for it, its tags, its journal line, the frame. The file is the caller's.
func (s *Store) ForgetFrame(hash string) error {
	return s.db.Exec(`WITH o AS (SELECT id FROM search.originals WHERE source = 'image' AND sha256 BETWEEN decode($1 || repeat('00', 16), 'hex') AND decode($1 || repeat('ff', 16), 'hex')),
		j AS (DELETE FROM search.jobs WHERE kind IN ('caption','tag') AND (payload->>'origId') IN (SELECT id::text FROM o)),
		c AS (DELETE FROM search.citations WHERE orig_source = 'image' AND orig_id IN (SELECT id FROM o)),
		x AS (DELETE FROM search.originals WHERE id IN (SELECT id FROM o)),
		t AS (DELETE FROM frame_tags WHERE hash = $1),
		e AS (DELETE FROM journal_entries WHERE source = 'ghost.framed' AND ref = $1)
		DELETE FROM frames WHERE hash = $1`, hash)
}

// Audit is one row of the archive's own stock-take: what a frame has and what it is missing,
// against the running pipeline. Read once at start by Converge, never guessed.
type Audit struct {
	Hash        string
	Kind        string
	ArchivePath string
	PreviewPath string
	ThumbPath   string
	TakenAt     int64
	PipeVer     int
	Described   bool // frames.description set (the caption's SCENE)
	Titled      bool // frames.display_name set (date + first tags)
	Tagged      bool // at least one tag row, tombstones included: a tag the user removed is a decision, not a gap
	Categorised bool // every live model tag carries a category (the digest's grouping); vacuously true with no tags
}

// Audit reads every frame's stage facts in one query. 40k rows is a few MB and well under a
// second; a start-up check must not cost more than that or nobody will leave it on.
func (s *Store) Audit() ([]Audit, error) {
	rows, err := s.db.Query(`
		SELECT f.hash, f.kind, f.archive_path, f.preview_path, f.thumb_path, f.taken_at, f.pipe_ver,
		       f.description <> '', f.display_name <> '',
		       EXISTS (SELECT 1 FROM frame_tags t WHERE t.hash = f.hash),
		       NOT EXISTS (SELECT 1 FROM frame_tags t WHERE t.hash = f.hash AND t.category = '' AND t.source <> 'user_removed')
		FROM frames f ORDER BY f.taken_at DESC`)
	if err != nil {
		return nil, err
	}
	out := make([]Audit, 0, len(rows.Vals))
	for _, r := range rows.Vals {
		if len(r) < 11 || r[0] == nil {
			continue
		}
		a := Audit{Hash: *r[0], Kind: deref(r[1]), ArchivePath: deref(r[2]), PreviewPath: deref(r[3]), ThumbPath: deref(r[4]),
			TakenAt: atoi64(deref(r[5])), PipeVer: int(atoi64(deref(r[6]))),
			Described: deref(r[7]) == "t", Titled: deref(r[8]) == "t", Tagged: deref(r[9]) == "t", Categorised: deref(r[10]) == "t"}
		out = append(out, a)
	}
	return out, nil
}

// takenRank orders the taken_at sources by how much they can be trusted, as a SQL expression over
// the given row alias. A reprocess may only REPLACE a capture time with one from a stronger
// source: exif (the camera's own clock, zone-exact when the offset tag is present) over the
// phone's MediaStore hint, over a video container's clock (some camera apps write it in local
// time and say nothing). The archive path and upload mtime both rank zero: the path is midnight
// of a day that was itself derived from an earlier decision, and mtime is not a capture time at
// all , neither may ever replace anything, including each other. Server-side constant, no user
// input, so a literal is the honest form.
func takenRank(alias string) string {
	return `(CASE ` + alias + `.taken_src WHEN 'exif' THEN 3 WHEN 'hint' THEN 2 WHEN 'moov' THEN 1 ELSE 0 END)`
}

// HasFrame reports whether a hash is already archived (dedupe before doing any work).
func (s *Store) HasFrame(hash string) (bool, error) {
	rows, err := s.db.Query("SELECT 1 FROM frames WHERE hash = $1", hash)
	if err != nil {
		return false, err
	}
	return len(rows.Vals) > 0, nil
}

// FrameTakenAt is the taken time the row converged to (false when there is no row).
func (s *Store) FrameTakenAt(hash string) (int64, bool) {
	rows, err := s.db.Query("SELECT taken_at FROM frames WHERE hash = $1", hash)
	if err != nil || len(rows.Vals) == 0 || len(rows.Vals[0]) == 0 || rows.Vals[0][0] == nil {
		return 0, false
	}
	ts, perr := strconv.ParseInt(*rows.Vals[0][0], 10, 64)
	return ts, perr == nil
}

// InsertPoints records a batch of location samples. Each row is parameterized; a multi-row VALUES with
// bound params keeps it one round trip. (ts, source) is the identity, so a re-sent batch is idempotent.
func (s *Store) InsertPoints(source string, pts []TrackPoint) error {
	if len(pts) == 0 {
		return nil
	}
	sql := "INSERT INTO location_points (ts, lat, lon, source) VALUES "
	args := make([]any, 0, len(pts)*4)
	for i, p := range pts {
		if i > 0 {
			sql += ", "
		}
		b := i * 4
		sql += fmt.Sprintf("($%d,$%d,$%d,$%d)", b+1, b+2, b+3, b+4)
		args = append(args, p.TS, p.Lat, p.Lon, source)
	}
	sql += " ON CONFLICT (ts, source) DO NOTHING"
	return s.db.Exec(sql, args...)
}

// TrailRow is one stored location point and where it came from (the phone, a watch, a Google
// Timeline import), and how the phone took it (Via), for the trail diagnostic.
type TrailRow struct {
	TrackPoint
	Source string
	Via    string
}

// SpooledPoint is one point of a location batch as secd spools it: where and when, and how the
// phone took it ("w" the quarter-hour fix, "p" another app's fix, "a" the app opening; "" when
// the sender does not say).
type SpooledPoint struct {
	TrackPoint
	Via string `json:"via,omitempty"`
}

// ViaName is what a via is called in the report.
func ViaName(via string) string {
	switch via {
	case "w":
		return "quarter-hour"
	case "p":
		return "other apps' fixes"
	case "a":
		return "app opened"
	case "":
		return "unmarked"
	}
	return via
}

// InsertSpooled stores a location batch's points with how each was taken; a point already stored
// (same second, same source) is left as it is.
func (s *Store) InsertSpooled(source string, pts []SpooledPoint) error {
	if len(pts) == 0 {
		return nil
	}
	sql := "INSERT INTO location_points (ts, lat, lon, source, via) VALUES "
	args := make([]any, 0, len(pts)*5)
	for i, p := range pts {
		if i > 0 {
			sql += ", "
		}
		b := i * 5
		sql += fmt.Sprintf("($%d,$%d,$%d,$%d,$%d)", b+1, b+2, b+3, b+4, b+5)
		args = append(args, p.TS, p.Lat, p.Lon, source, p.Via)
	}
	sql += " ON CONFLICT (ts, source) DO NOTHING"
	return s.db.Exec(sql, args...)
}

// TrailRows returns the stored points in [from, to) with their source, time-ordered.
func (s *Store) TrailRows(from, to int64) ([]TrailRow, error) {
	rows, err := s.db.Query("SELECT ts, lat, lon, source, via FROM location_points WHERE ts >= $1 AND ts < $2 ORDER BY ts", from, to)
	if err != nil {
		return nil, err
	}
	out := make([]TrailRow, 0, len(rows.Vals))
	for _, r := range rows.Vals {
		if len(r) != 5 || r[0] == nil || r[1] == nil || r[2] == nil {
			continue
		}
		tr := TrailRow{TrackPoint: TrackPoint{TS: atoi64(*r[0]), Lat: atof(*r[1]), Lon: atof(*r[2])}}
		if r[3] != nil {
			tr.Source = *r[3]
		}
		if r[4] != nil {
			tr.Via = *r[4]
		}
		out = append(out, tr)
	}
	return out, nil
}

// TrailReport is the trail diagnostic for one day: every stored point that makes a long hop (at
// least minHopM from the last point the rules kept) or that the glitch rules drop, with its time, place, source
// and the hop's length and speed, under a line of counts. What `ghost-cli ghost.framed trail
// day=YYYY-MM-DD` prints: the way to see which points draw a line across the map and why they
// were kept.
func TrailReport(day time.Time, rows []TrailRow, minHopM float64) string {
	return TrailReportAt(day, rows, minHopM, time.Now())
}

// TrailReportAt is TrailReport as of now: it also says when the day's last point was (a quiet
// evening at home is a point an hour, and a list of long hops cannot show that the points still
// come) and every gap of an hour or more between points.
func TrailReportAt(day time.Time, rows []TrailRow, minHopM float64, now time.Time) string {
	dayStart := time.Date(day.UTC().Year(), day.UTC().Month(), day.UTC().Day(), 0, 0, 0, 0, time.UTC).Unix()
	dayEnd := dayStart + 86400
	pts := make([]TrackPoint, len(rows))
	for i, r := range rows {
		pts[i] = r.TrackPoint
	}
	kept, _ := CleanTrack(pts)
	keptN := map[TrackPoint]int{}
	for _, k := range kept {
		keptN[k]++
	}
	bySource := map[string]int{}
	byVia := map[string]map[string]int{}
	var b strings.Builder
	var lines []string
	var prev *TrackPoint
	inDay, dropped := 0, 0
	for i := range rows {
		r := rows[i]
		ok := keptN[r.TrackPoint] > 0
		if ok {
			keptN[r.TrackPoint]--
		}
		if r.TS >= dayStart && r.TS < dayEnd {
			inDay++
			bySource[r.Source]++
			if byVia[r.Source] == nil {
				byVia[r.Source] = map[string]int{}
			}
			byVia[r.Source][r.Via]++
			if !ok {
				dropped++
			}
			hop, kmh := 0.0, 0.0
			if prev != nil {
				hop = HaversineM(*prev, r.TrackPoint)
				if dt := r.TS - prev.TS; dt > 0 {
					kmh = hop / float64(dt) * 3.6
				}
			}
			if !ok || hop >= minHopM {
				verdict := "kept"
				if !ok {
					verdict = "DROPPED"
				}
				line := fmt.Sprintf("  %s  %.5f,%.5f  %-16s hop %6.1f km at %5.0f km/h  %s",
					time.Unix(r.TS, 0).UTC().Format("15:04:05"), r.Lat, r.Lon, r.Source, hop/1000, kmh, verdict)
				if r.Via != "" {
					line += " · " + ViaName(r.Via)
				}
				lines = append(lines, line)
			}
		}
		p := r.TrackPoint
		if ok {
			prev = &p
		}
	}
	srcs := make([]string, 0, len(bySource))
	for s, n := range bySource {
		src := fmt.Sprintf("%s %d", s, n)
		// how the phone took them, when it says: "phone-84bcf711 51: 30 quarter-hour, 18 other apps' fixes"
		if v := byVia[s]; len(v) > 1 || v[""] == 0 {
			var parts []string
			for _, via := range []string{"w", "p", "a", ""} {
				if v[via] > 0 {
					parts = append(parts, fmt.Sprintf("%d %s", v[via], ViaName(via)))
				}
			}
			for via, k := range v {
				if via != "w" && via != "p" && via != "a" && via != "" {
					parts = append(parts, fmt.Sprintf("%d %s", k, via))
				}
			}
			src += ": " + strings.Join(parts, ", ")
		}
		srcs = append(srcs, src)
	}
	sort.Strings(srcs)
	fmt.Fprintf(&b, "%s: %d points (%s), %d dropped by the glitch rules; hops of %.0f km or more, and every dropped point (UTC):\n",
		day.Format("2006-01-02"), inDay, strings.Join(srcs, ", "), dropped, minHopM/1000)
	if len(lines) == 0 {
		b.WriteString("  none\n")
	}
	for _, l := range lines {
		b.WriteString(l + "\n")
	}
	// when the points were: the last one, and the silences
	var inDayTS []int64
	for _, r := range rows {
		if r.TS >= dayStart && r.TS < dayEnd {
			inDayTS = append(inDayTS, r.TS)
		}
	}
	if n := len(inDayTS); n > 0 {
		last := time.Unix(inDayTS[n-1], 0).UTC()
		fmt.Fprintf(&b, "first point %s, last point %s", time.Unix(inDayTS[0], 0).UTC().Format("15:04:05"), last.Format("15:04:05"))
		if now.After(last) && now.Unix() < dayEnd+86400 {
			fmt.Fprintf(&b, " (%s before now)", gapWords(now.Sub(last)))
		}
		b.WriteString("\n")
		var gaps []string
		for i := 1; i < n; i++ {
			if d := time.Duration(inDayTS[i]-inDayTS[i-1]) * time.Second; d >= time.Hour {
				gaps = append(gaps, fmt.Sprintf("%s to %s (%s)", time.Unix(inDayTS[i-1], 0).UTC().Format("15:04"), time.Unix(inDayTS[i], 0).UTC().Format("15:04"), gapWords(d)))
			}
		}
		if len(gaps) > 0 {
			b.WriteString("gaps of an hour or more: " + strings.Join(gaps, ", ") + "\n")
		} else {
			b.WriteString("no gap of an hour or more between points\n")
		}
	}
	return b.String()
}

// gapWords is "45 min" or "2 h 10 min".
func gapWords(d time.Duration) string {
	m := int(d.Minutes())
	if m < 60 {
		return fmt.Sprintf("%d min", m)
	}
	return fmt.Sprintf("%d h %d min", m/60, m%60)
}

// DayPoints returns the day's track points (UTC bounds), time-ordered.
func (s *Store) DayPoints(dayStart, dayEnd int64) ([]TrackPoint, error) {
	rows, err := s.db.Query(
		"SELECT ts, lat, lon FROM location_points WHERE ts >= $1 AND ts < $2 ORDER BY ts",
		dayStart, dayEnd)
	if err != nil {
		return nil, err
	}
	pts := make([]TrackPoint, 0, len(rows.Vals))
	for _, r := range rows.Vals {
		if len(r) != 3 || r[0] == nil || r[1] == nil || r[2] == nil {
			continue
		}
		pts = append(pts, TrackPoint{TS: atoi64(*r[0]), Lat: atof(*r[1]), Lon: atof(*r[2])})
	}
	return pts, nil
}

// DeletePoints removes the trail points recorded at these seconds, from every source: the person
// said they were not there (a trail question). Returns how many rows went.
func (s *Store) DeletePoints(ts []int64) (int, error) {
	n := 0
	for _, t := range ts {
		rows, err := s.db.Query("SELECT count(*) FROM location_points WHERE ts = $1", t)
		if err != nil {
			return n, err
		}
		if err := s.db.Exec("DELETE FROM location_points WHERE ts = $1", t); err != nil {
			return n, err
		}
		if len(rows.Vals) == 1 && rows.Vals[0][0] != nil {
			n += int(atoi64(*rows.Vals[0][0]))
		}
	}
	return n, nil
}

// KeepStretch records a yes to a trail question: the stretch [from, to] is not asked about again.
func (s *Store) KeepStretch(from, to int64) error {
	return s.db.Exec(`INSERT INTO trail_kept (ts_from, ts_to, at) VALUES ($1, $2, extract(epoch from now())::bigint)
		ON CONFLICT (ts_from, ts_to) DO NOTHING`, from, to)
}

// KeptStretches returns the stretches said yes to that overlap [from, to).
func (s *Store) KeptStretches(from, to int64) ([][2]int64, error) {
	rows, err := s.db.Query("SELECT ts_from, ts_to FROM trail_kept WHERE ts_to >= $1 AND ts_from < $2", from, to)
	if err != nil {
		return nil, err
	}
	out := make([][2]int64, 0, len(rows.Vals))
	for _, r := range rows.Vals {
		if len(r) == 2 && r[0] != nil && r[1] != nil {
			out = append(out, [2]int64{atoi64(*r[0]), atoi64(*r[1])})
		}
	}
	return out, nil
}

// DayPhotos returns the day's geotagged frames for the map.
func (s *Store) DayPhotos(dayStart, dayEnd int64) ([]PhotoPoint, error) {
	rows, err := s.db.Query(
		"SELECT hash, taken_at, lat, lon FROM frames WHERE has_gps AND taken_at >= $1 AND taken_at < $2 ORDER BY taken_at",
		dayStart, dayEnd)
	if err != nil {
		return nil, err
	}
	out := make([]PhotoPoint, 0, len(rows.Vals))
	for _, r := range rows.Vals {
		if len(r) != 4 || r[0] == nil {
			continue
		}
		out = append(out, PhotoPoint{
			Hash: *r[0], TakenAt: atoi64(deref(r[1])), Lat: atof(deref(r[2])), Lon: atof(deref(r[3])),
		})
	}
	return out, nil
}

// DayFrameCount is the day summary's headline number.
func (s *Store) DayFrameCount(dayStart, dayEnd int64) (int, error) {
	rows, err := s.db.Query(
		"SELECT count(*) FROM frames WHERE received_at >= $1 AND received_at < $2", dayStart, dayEnd)
	if err != nil || len(rows.Vals) == 0 || len(rows.Vals[0]) == 0 || rows.Vals[0][0] == nil {
		return 0, err
	}
	return int(atoi64(*rows.Vals[0][0])), nil
}

// --- on-box reverse geocoding, DB-backed (geo_points / geo_names, imported from GeoNames TSVs) ---

// ImportGeo streams GeoNames files from dir into postgres. The RAM geocoder this replaces had to
// filter the dataset down to stay loadable; postgres does not care, so the filter widens to the
// whole populated-place class plus parks, reserves, and the interesting physical features , at
// allCountries scale that is millions of rows, a couple of GB on a 7TB volume, and the nearest
// named point drops from "the one town cities1000 knew" to typically a village or suburb within
// a few km. geonameid is the PK, ON CONFLICT DO NOTHING, so re-import after a newer dump is safe.
// Batched inserts (500-row VALUES), progress logged every 100k , a full allCountries import is
// minutes of background work, run once.
func (s *Store) ImportGeo(dir string, log *slog.Logger) (points int64, names int64, err error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, 0, err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		p := filepath.Join(dir, e.Name())
		switch {
		case strings.HasPrefix(e.Name(), "admin1"):
			names += s.importCodes(p, "1:")
		case strings.HasPrefix(e.Name(), "admin2"):
			names += s.importCodes(p, "2:")
		case strings.HasPrefix(e.Name(), "countryInfo"):
			names += s.importCountries(p)
		case strings.HasSuffix(e.Name(), ".txt"):
			n, ierr := s.importPoints(p, log)
			points += n
			if ierr != nil {
				return points, names, ierr
			}
		}
	}
	return points, names, nil
}

// spotCodes is the S kind: the GeoNames codes the interests (internal/outings) can point a
// person at , beaches, harbours, castles, ruins, monasteries, museums, caves , none of which the
// geocoder needs, all of which "what is near me that I would like" does.
var spotCodes = func() map[string]bool {
	m := map[string]bool{}
	for _, c := range outings.SpotCodes() {
		m[c] = true
	}
	return m
}()

func geoKind(fclass, fcode string) byte {
	switch {
	case fclass == "P":
		return 'P'
	case fclass == "A" && (strings.HasPrefix(fcode, "PCL") || fcode == "ADM1"):
		return 'A' // a country, a dependency, a first-level region: the map's big labels
	case fclass == "L" && (strings.HasPrefix(fcode, "PRK") || strings.HasPrefix(fcode, "RES")):
		return 'K'
	case fclass == "H" && (fcode == "FLLS" || fcode == "LK" || fcode == "BAY" || fcode == "GLCR"),
		fclass == "T" && (fcode == "PK" || fcode == "MT" || fcode == "VLC"),
		fclass == "R" && fcode == "TRL":
		return 'F'
	case spotCodes[fcode] && fclass != "A" && fclass != "P":
		return 'S'
	}
	return 0
}

const geoCols = 11

// labelRank orders the map's labels: what to show first when a view holds thousands of names. A
// country outranks everything in it; a capital its cities; a region's seat its towns; a place with
// no population known (most villages) ranks 1 , shown when the view is small enough to have room.
// Anything the map should never label (a neighbourhood, an abandoned place, a section of a city)
// ranks 0 and is left out.
func labelRank(kind byte, fcode string, pop int64) int64 {
	switch kind {
	case 'A':
		switch {
		case strings.HasPrefix(fcode, "PCL"):
			return 1_000_000_000_000 + pop // a country: above anything in it
		case fcode == "ADM1":
			return 2*pop + 50_000 // a region reads above a town of the same size
		}
		return 0
	case 'P':
		switch fcode {
		case "PPLC":
			return 8*pop + 1_000_000 // the capital: above a bigger city in the same country
		case "PPLA":
			return 3*pop + 100_000 // a region's seat
		case "PPLA2":
			return 2*pop + 10_000
		case "PPLX", "PPLQ", "PPLW", "PPLH":
			return 0 // a section of a city, abandoned, destroyed, historical: not a label
		}
		if pop > 0 {
			return pop
		}
		return 1
	}
	return 0
}

func (s *Store) importPoints(path string, log *slog.Logger) (int64, error) {
	f, err := os.Open(path)
	if err != nil {
		return 0, nil // absent/unreadable file is a skip, not a failure
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	sc.Buffer(make([]byte, 0, 512*1024), 512*1024)
	const batch = 500
	vals := make([]any, 0, batch*geoCols)
	rows := 0
	var total int64
	flush := func() error {
		if rows == 0 {
			return nil
		}
		var sb strings.Builder
		sb.WriteString("INSERT INTO geo_points (geonameid,name,lat,lon,kind,fcode,country,admin1,admin2,population,rank) VALUES ")
		for i := 0; i < rows; i++ {
			if i > 0 {
				sb.WriteByte(',')
			}
			base := i * geoCols
			sb.WriteByte('(')
			for k := 1; k <= geoCols; k++ {
				if k > 1 {
					sb.WriteByte(',')
				}
				sb.WriteString("$" + strconv.Itoa(base+k))
			}
			sb.WriteByte(')')
		}
		// DO UPDATE, not DO NOTHING , re-importing a NEWER GeoNames dump must refresh renamed and
		// moved places, or "update" quietly means "append". Deletions are not propagated (a
		// removed geonameid lingers); named limit, acceptable for place data.
		sb.WriteString(" ON CONFLICT (geonameid) DO UPDATE SET name = EXCLUDED.name, lat = EXCLUDED.lat, lon = EXCLUDED.lon, kind = EXCLUDED.kind, fcode = EXCLUDED.fcode, country = EXCLUDED.country, admin1 = EXCLUDED.admin1, admin2 = EXCLUDED.admin2, population = EXCLUDED.population, rank = EXCLUDED.rank")
		if err := s.db.Exec(sb.String(), vals...); err != nil {
			return err
		}
		total += int64(rows)
		vals = vals[:0]
		rows = 0
		return nil
	}
	for sc.Scan() {
		c := strings.Split(sc.Text(), "\t")
		if len(c) < 12 {
			continue
		}
		kind := geoKind(c[6], c[7])
		if kind == 0 {
			continue
		}
		id, e0 := strconv.ParseInt(c[0], 10, 64)
		lat, e1 := strconv.ParseFloat(c[4], 64)
		lon, e2 := strconv.ParseFloat(c[5], 64)
		if e0 != nil || e1 != nil || e2 != nil {
			continue
		}
		pop, _ := strconv.ParseInt(c[14], 10, 64)
		if pop < 0 {
			pop = 0
		}
		vals = append(vals, id, c[1], lat, lon, string(kind), c[7], c[8], c[10], c[11], pop, labelRank(kind, c[7], pop))
		rows++
		if rows >= batch {
			if err := flush(); err != nil {
				return total, err
			}
			if total%100000 < batch {
				log.Info("geo import progress", "fn", "ImportGeo", "file", filepath.Base(path), "rows", total)
			}
		}
	}
	if err := flush(); err != nil {
		return total, err
	}
	log.Info("geo file imported", "fn", "ImportGeo", "file", filepath.Base(path), "rows", total)
	return total, nil
}

func (s *Store) importCodes(path, prefix string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var n int64
	for sc.Scan() {
		c := strings.Split(sc.Text(), "\t")
		if len(c) >= 2 {
			if s.db.Exec("INSERT INTO geo_names (code,name) VALUES ($1,$2) ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name",
				prefix+c[0], c[1]) == nil {
				n++
			}
		}
	}
	return n
}

var geoContinents = map[string]string{
	"AF": "Africa", "AS": "Asia", "EU": "Europe", "NA": "North America",
	"OC": "Oceania", "SA": "South America", "AN": "Antarctica",
}

func (s *Store) importCountries(path string) int64 {
	f, err := os.Open(path)
	if err != nil {
		return 0
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	var n int64
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, "#") {
			continue
		}
		c := strings.Split(line, "\t")
		if len(c) >= 9 {
			_ = s.db.Exec("INSERT INTO geo_names (code,name) VALUES ($1,$2) ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name", "c:"+c[0], c[4])
			_ = s.db.Exec("INSERT INTO geo_names (code,name) VALUES ($1,$2) ON CONFLICT (code) DO UPDATE SET name = EXCLUDED.name", "k:"+c[0], geoContinents[c[8]])
			n += 2
		}
	}
	return n
}

// GeoReady reports whether the geo tables hold data , the wiring check for the resolver.
func (s *Store) GeoReady() bool {
	rows, err := s.db.Query("SELECT 1 FROM geo_points LIMIT 1")
	return err == nil && len(rows.Vals) > 0
}

// GeoWithoutPopulations reports a geo set imported before the box kept populations (the column
// came on 4 October 2026; an older import has every row at 0): rows, but none with a population.
// Such a set names places but gives the weather nothing to pull from and the map's labels no
// order; importing the same files again fills the column.
func (s *Store) GeoWithoutPopulations() bool {
	rows, err := s.db.Query("SELECT (SELECT 1 FROM geo_points LIMIT 1) IS NOT NULL AND (SELECT 1 FROM geo_points WHERE population > 0 LIMIT 1) IS NULL")
	if err != nil || len(rows.Vals) == 0 || len(rows.Vals[0]) == 0 || rows.Vals[0][0] == nil {
		return false
	}
	return *rows.Vals[0][0] == "t" || *rows.Vals[0][0] == "true"
}

// ResolvePlace is the DB-backed reverse geocode: expanding-bbox nearest lookups per kind, exact
// haversine over the candidate set in Go. Radii are HONESTY CAPS, not precision , precision is the
// distance to the nearest row, and with allCountries loaded that is typically a suburb or village
// within a few km. Beyond the cap the field stays empty rather than claiming a far-away name.
func (s *Store) ResolvePlace(lat, lon float64) geo.Place {
	var out geo.Place
	if lat == 0 && lon == 0 {
		return out
	}
	if p, ok := s.geoNearest(lat, lon, 'P', 120); ok {
		out.Locality = p.name
		out.Country = s.geoName("c:" + p.country)
		out.Continent = s.geoName("k:" + p.country)
		out.Admin1 = s.geoName("1:" + p.country + "." + p.admin1)
		out.Admin2 = s.geoName("2:" + p.country + "." + p.admin1 + "." + p.admin2)
		if out.Country == "" {
			out.Country = p.country
		}
	}
	if p, ok := s.geoNearest(lat, lon, 'K', 15); ok {
		out.Park = p.name
	}
	if p, ok := s.geoNearest(lat, lon, 'F', 2.5); ok {
		out.Feature = p.name
	}
	return out
}

type geoRow struct {
	name, country, admin1, admin2 string
	lat, lon                      float64
}

func (s *Store) geoName(code string) string {
	rows, err := s.db.Query("SELECT name FROM geo_names WHERE code = $1", code)
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return ""
	}
	return *rows.Vals[0][0]
}

func (s *Store) geoNearest(lat, lon float64, kind byte, maxKm float64) (geoRow, bool) {
	cosLat := math.Cos(lat * math.Pi / 180)
	if cosLat < 0.05 {
		cosLat = 0.05
	}
	// Expanding bbox, ascending and CLIPPED to the cap. Candidates come back ordered by
	// approximate squared-degree distance (cos-corrected), so LIMIT 400 keeps the CLOSEST 400 ,
	// in dense areas (central London is thousands of rows in the first window) an unordered
	// LIMIT could exclude the true nearest. Exact haversine in Go then picks among them.
	capDeg := maxKm/111.0 + 0.05
	windows := make([]float64, 0, 3)
	for _, w := range []float64{0.05, 0.3, capDeg} {
		if w > capDeg {
			w = capDeg
		}
		if len(windows) == 0 || w > windows[len(windows)-1] {
			windows = append(windows, w)
		}
	}
	for _, degWin := range windows {
		dLat := degWin
		dLon := degWin / cosLat
		rows, err := s.db.Query(
			`SELECT name, country, admin1, admin2, lat, lon FROM geo_points
			 WHERE kind = $1 AND lat BETWEEN $2 AND $3 AND lon BETWEEN $4 AND $5
			 ORDER BY (lat-$6)*(lat-$6) + (lon-$7)*(lon-$7)*$8 LIMIT 400`,
			string(kind), lat-dLat, lat+dLat, lon-dLon, lon+dLon, lat, lon, cosLat*cosLat)
		if err != nil {
			return geoRow{}, false
		}
		best := geoRow{}
		bestD := maxKm + 1
		for _, v := range rows.Vals {
			if len(v) < 6 || v[0] == nil || v[4] == nil || v[5] == nil {
				continue
			}
			rlat, _ := strconv.ParseFloat(*v[4], 64)
			rlon, _ := strconv.ParseFloat(*v[5], 64)
			d := haversineKm(lat, lon, rlat, rlon)
			if d < bestD {
				bestD = d
				best = geoRow{name: *v[0], lat: rlat, lon: rlon}
				if v[1] != nil {
					best.country = *v[1]
				}
				if v[2] != nil {
					best.admin1 = *v[2]
				}
				if v[3] != nil {
					best.admin2 = *v[3]
				}
			}
		}
		if bestD <= maxKm {
			return best, true
		}
	}
	return geoRow{}, false
}

func haversineKm(lat1, lon1, lat2, lon2 float64) float64 {
	dLat := (lat2 - lat1) * math.Pi / 180
	dLon := (lon2 - lon1) * math.Pi / 180
	a := math.Sin(dLat/2)*math.Sin(dLat/2) +
		math.Cos(lat1*math.Pi/180)*math.Cos(lat2*math.Pi/180)*math.Sin(dLon/2)*math.Sin(dLon/2)
	return 2 * 6371.0 * math.Asin(math.Min(1, math.Sqrt(a)))
}

// InsertJournal writes framed's journal entry for one frame , the ingestion diary ghost.synthd
// distills memories from. Idempotent by (source, ref): reprocess re-writes every entry as a no-op,
// except when the frame's kind changed ("video archived" for what is a photo): the line's first
// word is the kind, and a line whose kind is wrong is replaced, place and all.
func (s *Store) InsertJournal(hash string, ts int64, title, body string) error {
	return s.db.Exec(
		`INSERT INTO journal_entries (source, ref, ts, title, body, created_at) VALUES ('ghost.framed', $1, $2, $3, $4, $5)
		 ON CONFLICT (source, ref) DO UPDATE SET title = EXCLUDED.title, body = EXCLUDED.body
		 WHERE split_part(journal_entries.title, ' ', 1) <> split_part(EXCLUDED.title, ' ', 1)`,
		hash, ts, title, body, time.Now().UnixMilli())
}

// SetState publishes one JSON value under framed's name in daemon_state, for the status screens.
// Overwrite, never append: the row is "what is true now", and the phone polls it.
// Setting reads one shared settings row ("" when none).
func (s *Store) Setting(key string) (string, error) {
	rows, err := s.db.Query("SELECT value FROM settings WHERE key = $1", key)
	if err != nil {
		return "", err
	}
	if len(rows.Vals) == 0 || len(rows.Vals[0]) == 0 || rows.Vals[0][0] == nil {
		return "", nil
	}
	return *rows.Vals[0][0], nil
}

// SetSetting writes one shared settings row.
func (s *Store) SetSetting(key, value string) error {
	return s.db.Exec("INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", key, value)
}

func (s *Store) SetState(key string, value []byte) error {
	return s.db.Exec(
		`INSERT INTO daemon_state (daemon, key, value, updated_at) VALUES ('ghost.framed', $1, $2, $3)
		 ON CONFLICT (daemon, key) DO UPDATE SET value = EXCLUDED.value, updated_at = EXCLUDED.updated_at`,
		key, string(value), time.Now().UTC().Unix())
}

// WeeklyHighlight picks the best day of the last 7 (most geotagged photos, place named) and, if
// this ISO week has not been announced yet, drops a notification , framed offering the person a
// look back, not an engagement hook: once a week, factual, and mutable like every notification.
func (s *Store) WeeklyHighlight() error {
	// the ISO week: "2006-W02" is a Go layout, so its "02" was the day of the month, the key
	// changed every day and the same week was announced every day (found 2 Oct 2026)
	y, w := time.Now().UTC().ISOWeek()
	wk := fmt.Sprintf("%d-W%02d", y, w)
	rows, err := s.db.Query("SELECT value FROM settings WHERE key = 'framed_week_highlight'")
	if err == nil && len(rows.Vals) == 1 && rows.Vals[0][0] != nil && *rows.Vals[0][0] == wk {
		return nil // this week already highlighted
	}
	since := time.Now().AddDate(0, 0, -7).Unix()
	rows, err = s.db.Query(`
		SELECT to_char(to_timestamp(taken_at), 'YYYY-MM-DD') AS d, count(*) AS n,
		       min(place) FILTER (WHERE place <> '') AS p
		FROM frames WHERE kind = 'photo' AND taken_at >= $1
		GROUP BY d ORDER BY n DESC LIMIT 1`, since)
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil || rows.Vals[0][1] == nil {
		return err // nothing this week , no notification, silence is honest
	}
	day := *rows.Vals[0][0]
	n := *rows.Vals[0][1]
	place := ""
	if len(rows.Vals[0]) > 2 && rows.Vals[0][2] != nil && *rows.Vals[0][2] != "" {
		parts := strings.Split(*rows.Vals[0][2], " / ")
		place = " around " + parts[len(parts)-1]
	}
	t, terr := time.Parse("2006-01-02", day)
	dayName := day
	if terr == nil {
		dayName = t.Weekday().String()
	}
	if err := s.db.Exec(
		"INSERT INTO notifications (service, kind, title, body, seen, options, created, link) VALUES ('ghost.framed','highlight',$1,$2,FALSE,'',now(),$3)",
		"your week in frames",
		dayName+" was the big one , "+n+" photos"+place+". Tap for the day.", "day:"+day); err != nil {
		return err
	}
	return s.db.Exec(
		"INSERT INTO settings (key, value) VALUES ('framed_week_highlight',$1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", wk)
}

// StayName names a position for the day route (dayroute.Namer): the spot it is at , a beach, a
// harbour, a museum, one of the S rows geo-import keeps for the interests , within 300 m, else the
// nearest populated place within 5 km as "near <place>". Empty when the box has no geo data.
func (s *Store) StayName(lat, lon float64) (name, kind string) {
	cosLat := math.Cos(lat * math.Pi / 180)
	if cosLat < 0.05 {
		cosLat = 0.05
	}
	const win = 0.004 // ~440 m of latitude
	rows, err := s.db.Query(
		`SELECT name, fcode, lat, lon FROM geo_points
		 WHERE kind = 'S' AND lat BETWEEN $1 AND $2 AND lon BETWEEN $3 AND $4
		 ORDER BY (lat-$5)*(lat-$5) + (lon-$6)*(lon-$6)*$7 LIMIT 50`,
		lat-win, lat+win, lon-win/cosLat, lon+win/cosLat, lat, lon, cosLat*cosLat)
	if err == nil {
		bestD := 0.3
		for _, v := range rows.Vals {
			if len(v) < 4 || v[0] == nil || v[2] == nil || v[3] == nil {
				continue
			}
			rlat, _ := strconv.ParseFloat(*v[2], 64)
			rlon, _ := strconv.ParseFloat(*v[3], 64)
			if d := haversineKm(lat, lon, rlat, rlon); d < bestD {
				bestD = d
				name = *v[0]
				kind = "spot"
				if v[1] != nil {
					if k := outings.KindName(*v[1]); k != "" {
						kind = k
					}
				}
			}
		}
		if name != "" {
			return name, kind
		}
	}
	if r, ok := s.geoNearest(lat, lon, 'P', 5); ok {
		return r.name, "near"
	}
	return "", ""
}

// DaySteps is the day's step count from the phone's health sync, 0 when unknown.
func (s *Store) DaySteps(day string) float64 {
	rows, err := s.db.Query("SELECT value FROM health_metrics WHERE day = $1 AND metric = 'steps'", day)
	if err != nil || len(rows.Vals) == 0 || len(rows.Vals[0]) == 0 || rows.Vals[0][0] == nil {
		return 0
	}
	v, _ := strconv.ParseFloat(*rows.Vals[0][0], 64)
	return v
}
