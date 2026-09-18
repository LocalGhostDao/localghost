package framed

// Store records frames and location points in Postgres via the native poltergres client (extended
// protocol, parameterized). Every value travels as a bound parameter, NEVER interpolated into SQL ,
// so the sqlQuote string-building this file used to carry is gone, and injection is structurally
// impossible rather than something we guard against. The store holds a ghost_rw connection (it writes
// frames and points); reads use the same connection.

import (
	"bufio"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
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

// Ping verifies the connection (and thus that ghost_rw can authenticate).
func (s *Store) Ping() error { return s.db.Ping() }

// InsertFrame records one archived photo. The hash is the identity, so re-uploading the same photo
// is a no-op , but a REPROCESS is not a re-upload: it is the current code re-reading the original,
// and when the code got better (a longer metadata head, a tolerant EXIF parser, a decoder the box
// did not have last time) the row must be allowed to improve. Every column below converges
// MONOTONICALLY: a fact replaces an absence, a stronger source replaces a weaker one, and nothing
// a person or an earlier ingest knew is ever overwritten by "unknown". The old clause backfilled
// place only, which meant a frame archived without GPS stayed off the map forever, however many
// times reprocess re-read its coordinates.
func (s *Store) InsertFrame(f Frame) error {
	return s.db.Exec(
		`INSERT INTO frames (hash, taken_at, lat, lon, has_gps, archive_path, preview_path, thumb_path, bytes, source, received_at, kind, mime, taken_src, place, device)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16)
		 ON CONFLICT (hash) DO UPDATE SET
		   place = CASE WHEN frames.place = '' AND EXCLUDED.place <> '' THEN EXCLUDED.place ELSE frames.place END,
		   lat = CASE WHEN NOT frames.has_gps AND EXCLUDED.has_gps THEN EXCLUDED.lat ELSE frames.lat END,
		   lon = CASE WHEN NOT frames.has_gps AND EXCLUDED.has_gps THEN EXCLUDED.lon ELSE frames.lon END,
		   has_gps = frames.has_gps OR EXCLUDED.has_gps,
		   taken_at = CASE WHEN `+takenRank("EXCLUDED")+` > `+takenRank("frames")+` THEN EXCLUDED.taken_at ELSE frames.taken_at END,
		   taken_src = CASE WHEN `+takenRank("EXCLUDED")+` > `+takenRank("frames")+` THEN EXCLUDED.taken_src ELSE frames.taken_src END,
		   preview_path = CASE WHEN frames.preview_path = '' AND EXCLUDED.preview_path <> '' THEN EXCLUDED.preview_path ELSE frames.preview_path END,
		   thumb_path = CASE WHEN frames.thumb_path = '' AND EXCLUDED.thumb_path <> '' THEN EXCLUDED.thumb_path ELSE frames.thumb_path END,
		   kind = CASE WHEN frames.kind = 'unknown' AND EXCLUDED.kind <> 'unknown' THEN EXCLUDED.kind ELSE frames.kind END,
		   mime = CASE WHEN frames.mime = '' AND EXCLUDED.mime <> '' THEN EXCLUDED.mime ELSE frames.mime END,
		   device = CASE WHEN frames.device = '' AND EXCLUDED.device <> '' THEN EXCLUDED.device ELSE frames.device END
		 WHERE (frames.place = '' AND EXCLUDED.place <> '')
		    OR (NOT frames.has_gps AND EXCLUDED.has_gps)
		    OR (`+takenRank("EXCLUDED")+` > `+takenRank("frames")+`)
		    OR (frames.preview_path = '' AND EXCLUDED.preview_path <> '')
		    OR (frames.thumb_path = '' AND EXCLUDED.thumb_path <> '')
		    OR (frames.kind = 'unknown' AND EXCLUDED.kind <> 'unknown')
		    OR (frames.mime = '' AND EXCLUDED.mime <> '')
		    OR (frames.device = '' AND EXCLUDED.device <> '')`,
		f.Hash, f.TakenAt, f.Lat, f.Lon, f.HasGPS,
		f.ArchivePath, f.PreviewPath, f.ThumbPath, f.Bytes, f.Source, f.ReceivedAt, f.Kind, f.MIME, f.TakenSrc, f.Place, f.Device)
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

func geoKind(fclass, fcode string) byte {
	switch {
	case fclass == "P":
		return 'P'
	case fclass == "L" && (strings.HasPrefix(fcode, "PRK") || strings.HasPrefix(fcode, "RES")):
		return 'K'
	case fclass == "H" && (fcode == "FLLS" || fcode == "LK" || fcode == "BAY" || fcode == "GLCR"),
		fclass == "T" && (fcode == "PK" || fcode == "MT" || fcode == "VLC"),
		fclass == "R" && fcode == "TRL":
		return 'F'
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
	vals := make([]any, 0, batch*9)
	rows := 0
	var total int64
	flush := func() error {
		if rows == 0 {
			return nil
		}
		var sb strings.Builder
		sb.WriteString("INSERT INTO geo_points (geonameid,name,lat,lon,kind,fcode,country,admin1,admin2) VALUES ")
		for i := 0; i < rows; i++ {
			if i > 0 {
				sb.WriteByte(',')
			}
			base := i * 9
			sb.WriteString("($" + strconv.Itoa(base+1) + ",$" + strconv.Itoa(base+2) + ",$" + strconv.Itoa(base+3) +
				",$" + strconv.Itoa(base+4) + ",$" + strconv.Itoa(base+5) + ",$" + strconv.Itoa(base+6) +
				",$" + strconv.Itoa(base+7) + ",$" + strconv.Itoa(base+8) + ",$" + strconv.Itoa(base+9) + ")")
		}
		// DO UPDATE, not DO NOTHING , re-importing a NEWER GeoNames dump must refresh renamed and
		// moved places, or "update" quietly means "append". Deletions are not propagated (a
		// removed geonameid lingers); named limit, acceptable for place data.
		sb.WriteString(" ON CONFLICT (geonameid) DO UPDATE SET name = EXCLUDED.name, lat = EXCLUDED.lat, lon = EXCLUDED.lon, kind = EXCLUDED.kind, fcode = EXCLUDED.fcode, country = EXCLUDED.country, admin1 = EXCLUDED.admin1, admin2 = EXCLUDED.admin2")
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
		vals = append(vals, id, c[1], lat, lon, string(kind), c[7], c[8], c[10], c[11])
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
// distills memories from. Idempotent by (source, ref): reprocess re-writes every entry as a no-op.
func (s *Store) InsertJournal(hash string, ts int64, title, body string) error {
	return s.db.Exec(
		"INSERT INTO journal_entries (source, ref, ts, title, body, created_at) VALUES ('ghost.framed', $1, $2, $3, $4, $5) ON CONFLICT (source, ref) DO NOTHING",
		hash, ts, title, body, time.Now().UnixMilli())
}

// WeeklyHighlight picks the best day of the last 7 (most geotagged photos, place named) and, if
// this ISO week has not been announced yet, drops a notification , framed offering the person a
// look back, not an engagement hook: once a week, factual, and mutable like every notification.
func (s *Store) WeeklyHighlight() error {
	wk := time.Now().UTC().Format("2006-W02")
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
		"INSERT INTO notifications (service, kind, title, body, seen, options, created) VALUES ('ghost.framed','highlight',$1,$2,FALSE,'',now())",
		"your week in frames",
		dayName+" was the big one , "+n+" photos"+place+". They are on your MAP."); err != nil {
		return err
	}
	return s.db.Exec(
		"INSERT INTO settings (key, value) VALUES ('framed_week_highlight',$1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", wk)
}
