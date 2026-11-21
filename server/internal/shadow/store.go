package shadow

import (
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// DB is the part of the database connection this package uses (poltergres.ReadWrite has it).
type DB interface {
	Query(sql string, args ...any) (*poltergres.Rows, error)
	Exec(sql string, args ...any) error
}

const (
	// MaxMute is the longest a detector can be muted: "no longer than a few days", and it goes
	// on recording while it is.
	MaxMute = 3 * 24 * time.Hour
	// ResetEvery is how often every threshold goes back to its baseline, whatever the person set
	// (arbiter capture: the configuration is shaped by the same wish for less friction).
	ResetEvery = 90 * 24 * time.Hour
	// captureRun is how many changes in a row, each toward quiet, the capture detector names.
	captureRun = 3

	service = "ghost.shadowd"
)

// Observation is one time a detector named something.
type Observation struct {
	ID        int64   `json:"id"`
	Detector  string  `json:"detector"`
	Subject   string  `json:"subject"` // what it is about; a detector speaks once per subject
	At        int64   `json:"at"`      // unix seconds
	Title     string  `json:"title"`
	Body      string  `json:"body"`
	Data      string  `json:"data"` // JSON: the data behind it
	Score     float64 `json:"score"`
	Threshold float64 `json:"threshold"`
	Muted     bool    `json:"muted"`
	Link      string  `json:"link,omitempty"`
}

// DetectorState is a detector as it stands for the person: its threshold, its mute, its history.
type DetectorState struct {
	Detector
	Threshold  float64 `json:"threshold"`
	MutedUntil int64   `json:"mutedUntil,omitempty"`
	Fired      int     `json:"fired"`
	LastFired  int64   `json:"lastFired,omitempty"`
}

func setting(db DB, key string) (string, bool) {
	rows, err := db.Query("SELECT value FROM settings WHERE key = $1", key)
	if err != nil || len(rows.Vals) != 1 || rows.Vals[0][0] == nil {
		return "", false
	}
	return *rows.Vals[0][0], true
}

func setSetting(db DB, key, value string) error {
	return db.Exec("INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", key, value)
}

func fmtF(v float64) string { return strconv.FormatFloat(v, 'f', -1, 64) }

func logConfig(db DB, now time.Time, detector, what, old, new string, quieter bool) error {
	return db.Exec("INSERT INTO shadow_config (ts, detector, what, old, new, quieter) VALUES ($1, $2, $3, $4, $5, $6)",
		now.Unix(), detector, what, old, new, quieter)
}

// Threshold is a detector's threshold as it stands: what the person set, held inside the
// bounds, or the baseline.
func Threshold(db DB, id string) float64 {
	d, ok := DetectorByID(id)
	if !ok {
		return 0
	}
	if s, ok := setting(db, "shadow.threshold."+id); ok {
		if v, err := strconv.ParseFloat(s, 64); err == nil && v >= d.Min && v <= d.Max {
			return v
		}
	}
	return d.Baseline
}

// ErrBounds is a threshold outside what a detector publishes, or a detector with none to move.
var ErrBounds = errors.New("shadow: outside the detector's published bounds")

// ErrUnknown is a detector that is not in the fleet.
var ErrUnknown = errors.New("shadow: no such detector")

// SetThreshold moves a detector's threshold inside its bounds and logs the change. It cannot be
// moved past Max: no detector can be configured into silence.
func SetThreshold(db DB, id string, v float64, now time.Time) error {
	d, ok := DetectorByID(id)
	if !ok {
		return ErrUnknown
	}
	if d.Min == d.Max || v < d.Min || v > d.Max || v != v {
		return ErrBounds
	}
	old := Threshold(db, id)
	if old == v {
		return nil
	}
	if err := setSetting(db, "shadow.threshold."+id, fmtF(v)); err != nil {
		return err
	}
	return logConfig(db, now, id, "threshold", fmtF(old), fmtF(v), v > old)
}

// MutedUntil is when a detector's mute ends (unix seconds), 0 when it is not muted.
func MutedUntil(db DB, id string, now time.Time) int64 {
	s, ok := setting(db, "shadow.mute."+id)
	if !ok {
		return 0
	}
	until, err := strconv.ParseInt(s, 10, 64)
	if err != nil || until <= now.Unix() {
		return 0
	}
	if max := now.Add(MaxMute).Unix(); until > max {
		return max // a value written by hand does not outlast the rule
	}
	return until
}

// Mute quiets a detector for a while, three days at most; 0 lifts the mute. The detector goes
// on recording: what it sees while muted is kept and marked, and is not sent to the phone.
func Mute(db DB, id string, d time.Duration, now time.Time) (int64, error) {
	if _, ok := DetectorByID(id); !ok {
		return 0, ErrUnknown
	}
	if d < 0 {
		d = 0
	}
	if d > MaxMute {
		d = MaxMute
	}
	old := MutedUntil(db, id, now)
	until := int64(0)
	if d > 0 {
		until = now.Add(d).Unix()
	}
	if err := setSetting(db, "shadow.mute."+id, strconv.FormatInt(until, 10)); err != nil {
		return 0, err
	}
	// lifting a mute is not a step toward quiet; setting or lengthening one is
	return until, logConfig(db, now, id, "mute", strconv.FormatInt(old, 10), strconv.FormatInt(until, 10), until > old)
}

// LogNotificationsMute records that the whole daemon's notifications were muted until a time
// (the phone's per-service mute, which secd holds to MaxMute for this service). It is a step
// toward quiet like any other and is read by the capture detector.
func LogNotificationsMute(db DB, until int64, now time.Time) error {
	return logConfig(db, now, "*", "notifications", "", strconv.FormatInt(until, 10), true)
}

// Observe records what a detector named, once per subject, and sends it to the phone when the
// detector announces and is not muted. It reports whether this was the first time.
func Observe(db DB, o Observation, now time.Time) (bool, error) {
	d, ok := DetectorByID(o.Detector)
	if !ok {
		return false, ErrUnknown
	}
	if o.Subject == "" || o.Title == "" {
		return false, errors.New("shadow: an observation has a subject and a title")
	}
	if o.Data == "" {
		o.Data = "{}"
	}
	muted := MutedUntil(db, o.Detector, now) > 0
	rows, err := db.Query(`INSERT INTO shadow_observations (detector, subject, ts, title, body, data, score, threshold, muted, link)
		VALUES ($1, $2, $3, $4, $5, $6, $7, $8, $9, $10) ON CONFLICT (detector, subject) DO NOTHING RETURNING id`,
		o.Detector, o.Subject, now.Unix(), o.Title, o.Body, o.Data, o.Score, o.Threshold, muted, o.Link)
	if err != nil {
		return false, err
	}
	if len(rows.Vals) == 0 {
		return false, nil // said before
	}
	if !d.Announce || muted {
		return true, nil
	}
	link := o.Link
	if link == "" {
		link = LinkFor(o)
	}
	return true, db.Exec(`INSERT INTO notifications (service, kind, title, body, seen, options, created, link)
		VALUES ($1, 'observation', $2, $3, FALSE, '', now(), $4)`, service, o.Title, o.Body, link)
}

// LinkFor is where a tap on an observation's notification lands in the app: the chat's audit
// trail for something named about a chat ("shadow:chat:<id>"), the detector's page otherwise
// ("shadow:<detector>"). An app from before it had the page falls back to Box Status.
func LinkFor(o Observation) string {
	if rest, ok := strings.CutPrefix(o.Subject, "chat:"); ok {
		id := rest
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			id = rest[:i]
		}
		if _, err := strconv.ParseInt(id, 10, 64); err == nil {
			return "shadow:chat:" + id
		}
	}
	return "shadow:" + o.Detector
}

func scanObservations(rows *poltergres.Rows) []Observation {
	out := []Observation{}
	for _, v := range rows.Vals {
		if len(v) < 11 {
			continue
		}
		s := func(i int) string {
			if v[i] == nil {
				return ""
			}
			return *v[i]
		}
		o := Observation{Detector: s(1), Subject: s(2), Title: s(4), Body: s(5), Data: s(6), Muted: s(9) == "t" || s(9) == "true", Link: s(10)}
		o.ID, _ = strconv.ParseInt(s(0), 10, 64)
		o.At, _ = strconv.ParseInt(s(3), 10, 64)
		o.Score, _ = strconv.ParseFloat(s(7), 64)
		o.Threshold, _ = strconv.ParseFloat(s(8), 64)
		out = append(out, o)
	}
	return out
}

const observationCols = "id, detector, subject, ts, title, body, data, score, threshold, muted, link"

// Observations is a detector's history, newest first ("" for the whole fleet's).
func Observations(db DB, detector string, limit int) ([]Observation, error) {
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	var rows *poltergres.Rows
	var err error
	if detector == "" {
		rows, err = db.Query("SELECT "+observationCols+" FROM shadow_observations ORDER BY id DESC LIMIT $1", limit)
	} else {
		rows, err = db.Query("SELECT "+observationCols+" FROM shadow_observations WHERE detector = $1 ORDER BY id DESC LIMIT $2", detector, limit)
	}
	if err != nil {
		return nil, err
	}
	return scanObservations(rows), nil
}

// ObservationsAbout is what was named about one thing (a mail, a chat), for that thing's page.
func ObservationsAbout(db DB, subjectPrefix string) ([]Observation, error) {
	rows, err := db.Query("SELECT "+observationCols+" FROM shadow_observations WHERE subject = $1 OR subject LIKE $2 ORDER BY id",
		subjectPrefix, likeEscape(subjectPrefix)+"/%")
	if err != nil {
		return nil, err
	}
	return scanObservations(rows), nil
}

func likeEscape(s string) string {
	return strings.NewReplacer(`\`, `\\`, `%`, `\%`, `_`, `\_`).Replace(s)
}

// State is the fleet as it stands, in the catalogue's order.
func State(db DB, now time.Time) []DetectorState {
	fired := map[string][2]int64{}
	if rows, err := db.Query("SELECT detector, count(*), max(ts) FROM shadow_observations GROUP BY detector"); err == nil {
		for _, v := range rows.Vals {
			if len(v) == 3 && v[0] != nil && v[1] != nil && v[2] != nil {
				n, _ := strconv.ParseInt(*v[1], 10, 64)
				last, _ := strconv.ParseInt(*v[2], 10, 64)
				fired[*v[0]] = [2]int64{n, last}
			}
		}
	}
	out := make([]DetectorState, 0, len(Detectors))
	for _, d := range Detectors {
		f := fired[d.ID]
		out = append(out, DetectorState{Detector: d, Threshold: Threshold(db, d.ID), MutedUntil: MutedUntil(db, d.ID, now), Fired: int(f[0]), LastFired: f[1]})
	}
	return out
}

// ConfigChange is one line of the settings' trail.
type ConfigChange struct {
	At       int64  `json:"at"`
	Detector string `json:"detector"`
	What     string `json:"what"` // threshold, mute, reset
	Old      string `json:"old"`
	New      string `json:"new"`
	Quieter  bool   `json:"quieter"`
}

// ConfigTrail is every change made to the fleet's settings, newest first.
func ConfigTrail(db DB, limit int) ([]ConfigChange, error) {
	if limit <= 0 || limit > 500 {
		limit = 100
	}
	rows, err := db.Query("SELECT ts, detector, what, old, new, quieter FROM shadow_config ORDER BY id DESC LIMIT $1", limit)
	if err != nil {
		return nil, err
	}
	out := []ConfigChange{}
	for _, v := range rows.Vals {
		if len(v) < 6 || v[0] == nil {
			continue
		}
		c := ConfigChange{Detector: deref(v[1]), What: deref(v[2]), Old: deref(v[3]), New: deref(v[4]), Quieter: deref(v[5]) == "t"}
		c.At, _ = strconv.ParseInt(*v[0], 10, 64)
		out = append(out, c)
	}
	return out, nil
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// NextReset is when the thresholds next go back to the baseline (unix seconds). The first call
// starts the clock.
func NextReset(db DB, now time.Time) int64 {
	if s, ok := setting(db, "shadow.reset_at"); ok {
		if at, err := strconv.ParseInt(s, 10, 64); err == nil && at > 0 {
			return at + int64(ResetEvery/time.Second)
		}
	}
	_ = setSetting(db, "shadow.reset_at", strconv.FormatInt(now.Unix(), 10))
	return now.Add(ResetEvery).Unix()
}

// ResetIfDue puts every threshold back to its baseline once ResetEvery has passed since the
// last time, logs each one it moved, and says so. The schedule is the project's, not the
// person's: it is not a setting.
func ResetIfDue(db DB, now time.Time) ([]string, error) {
	if NextReset(db, now) > now.Unix() {
		return nil, nil
	}
	var moved []string
	for _, d := range Detectors {
		if d.Min == d.Max {
			continue
		}
		old := Threshold(db, d.ID)
		if old == d.Baseline {
			continue
		}
		if err := db.Exec("DELETE FROM settings WHERE key = $1", "shadow.threshold."+d.ID); err != nil {
			return moved, err
		}
		if err := logConfig(db, now, d.ID, "reset", fmtF(old), fmtF(d.Baseline), false); err != nil {
			return moved, err
		}
		moved = append(moved, d.Name)
	}
	if err := setSetting(db, "shadow.reset_at", strconv.FormatInt(now.Unix(), 10)); err != nil {
		return moved, err
	}
	if len(moved) == 0 {
		return nil, nil
	}
	data, _ := json.Marshal(map[string]any{"moved": moved})
	_, err := Observe(db, Observation{
		Detector: DetCapture, Subject: "reset:" + now.UTC().Format("2006-01-02"),
		Title: "the detectors are back at their baseline",
		Body: fmt.Sprintf("Every ninety days the thresholds here go back to where the project publishes them, whatever was set since. This time that moved %s. You can set them again, inside the same bounds.",
			joinWords(moved)),
		Data: string(data), Score: float64(len(moved)), Threshold: 1,
	}, now)
	return moved, err
}

// Capture is the arbiter-capture detector: the last few changes to the settings each made a
// detector quieter. It reads the trail since the last reset and speaks once per run of changes.
func Capture(db DB, now time.Time) (bool, error) {
	rows, err := db.Query("SELECT id, detector, what, quieter FROM shadow_config WHERE what <> 'reset' AND id > COALESCE((SELECT max(id) FROM shadow_config WHERE what = 'reset'), 0) ORDER BY id DESC LIMIT $1", captureRun)
	if err != nil {
		return false, err
	}
	if len(rows.Vals) < captureRun {
		return false, nil
	}
	var names []string
	seen := map[string]bool{}
	for _, v := range rows.Vals {
		if len(v) < 4 || deref(v[3]) != "t" {
			return false, nil // one of them made a detector louder, or lifted a mute
		}
		if d, ok := DetectorByID(deref(v[1])); ok && !seen[d.ID] {
			seen[d.ID] = true
			names = append(names, d.Name)
		}
	}
	data, _ := json.Marshal(map[string]any{"detectors": names, "changes": captureRun})
	return Observe(db, Observation{
		Detector: DetCapture, Subject: "upto:" + deref(rows.Vals[0][0]),
		Title: "the detectors are being tuned toward quiet",
		Body: fmt.Sprintf("The last %d changes made here each made a detector quieter (%s). Each one is reasonable. Together they are how something built to disagree ends up agreeing. The thresholds go back to their baseline on %s whatever is set.",
			captureRun, joinWords(names), time.Unix(NextReset(db, now), 0).UTC().Format("2 January 2006")),
		Data: string(data), Score: captureRun, Threshold: captureRun,
	}, now)
}

func joinWords(w []string) string {
	switch len(w) {
	case 0:
		return ""
	case 1:
		return w[0]
	}
	return strings.Join(w[:len(w)-1], ", ") + " and " + w[len(w)-1]
}
