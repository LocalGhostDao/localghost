package framed

// Store records frames and location points in Postgres via the native poltergres client (extended
// protocol, parameterized). Every value travels as a bound parameter, NEVER interpolated into SQL ,
// so the sqlQuote string-building this file used to carry is gone, and injection is structurally
// impossible rather than something we guard against. The store holds a ghost_rw connection (it writes
// frames and points); reads use the same connection.

import (
	"fmt"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// Frame is one archived photo's record.
type Frame struct {
	Hash        string
	TakenAt     int64
	Lat, Lon    float64
	HasGPS      bool
	ArchivePath string
	PreviewPath string
	ThumbPath   string
	Bytes       int64
	Source      string
	ReceivedAt  int64
	Kind        string // "photo" | "video" | "unknown", from content sniffing
	MIME        string // best-effort content type, e.g. "image/jpeg", "video/mp4"
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

// InsertFrame records one archived photo. ON CONFLICT DO NOTHING makes reprocessing idempotent , the
// hash is the identity, so re-uploading the same photo is a no-op.
func (s *Store) InsertFrame(f Frame) error {
	return s.db.Exec(
		`INSERT INTO frames (hash, taken_at, lat, lon, has_gps, archive_path, preview_path, thumb_path, bytes, source, received_at, kind, mime)
		 VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13)
		 ON CONFLICT (hash) DO NOTHING`,
		f.Hash, f.TakenAt, f.Lat, f.Lon, f.HasGPS,
		f.ArchivePath, f.PreviewPath, f.ThumbPath, f.Bytes, f.Source, f.ReceivedAt, f.Kind, f.MIME)
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
