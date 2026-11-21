package secd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/shadow"
)

// GHOST.SHADOWD AS THE PHONE SEES IT (internal/shadow does the work; docs/SHADOW.md). The
// catalogue with every entry, built or not; the fleet with each detector's threshold, mute and
// history; what was named about one thing; a chat's audit trail. And the person's two acts on
// a detector, which are all there are: move its threshold inside its published bounds, mute it
// for three days at most. There is no route that switches one off.

const shadowService = "ghost.shadowd"

// shadowMuteLength holds a mute of ghost.shadowd's notifications to the charter's three days:
// "forever" and a week are three days too. Every other scope is left as asked. (The mute of
// everything, "*", is the phone's own quiet and is not shortened: what shadowd names is kept
// either way and is there when the quiet ends.)
func shadowMuteLength(scope string, forever bool, d time.Duration) (time.Duration, bool) {
	if scope != shadowService {
		return d, forever
	}
	if forever || d > shadow.MaxMute {
		return shadow.MaxMute, false
	}
	return d, false
}

type shadowDoc struct {
	Catalogue []shadow.Entry         `json:"catalogue"`
	Detectors []shadow.DetectorState `json:"detectors"`
	// NextReset is when every threshold goes back to its baseline (unix seconds); ResetDays how
	// often; MaxMuteHours the longest mute.
	NextReset    int64                 `json:"nextReset"`
	ResetDays    int                   `json:"resetDays"`
	MaxMuteHours int                   `json:"maxMuteHours"`
	Changes      []shadow.ConfigChange `json:"changes"`
	// Arbiter is which model the cold read is read by: a second model of the box's own (has,
	// model, up), or the main model with nothing about the person. Absent when ghost.oracled
	// does not answer.
	Arbiter *oracle.ArbiterInfo `json:"arbiter,omitempty"`
}

type shadowObservationsDoc struct {
	Observations []shadow.Observation `json:"observations"`
}

type shadowTrailDoc struct {
	Chat  int64               `json:"chat"`
	Trail []shadow.TrailEntry `json:"trail"`
	// Reading: a read the person asked for (POST /v1/shadow/read) is going. Asked: the last one
	// asked for, with when it finished, how many answers it read, and why none could be.
	Reading bool          `json:"reading"`
	Asked   *shadow.Asked `json:"asked,omitempty"`
}

type shadowThresholdRequest struct {
	Detector string  `json:"detector"`
	Value    float64 `json:"value"`
}

type shadowThresholdDoc struct {
	OK        bool    `json:"ok"`
	Threshold float64 `json:"threshold"`
}

type shadowMuteRequest struct {
	Detector string  `json:"detector"`
	Hours    float64 `json:"hours"` // 0 lifts the mute; more than 72 is 72
}

type shadowMuteDoc struct {
	OK         bool  `json:"ok"`
	MutedUntil int64 `json:"mutedUntil"`
}

// handleShadow , GET /v1/shadow , the catalogue and the fleet as they stand.
func (s *Server) handleShadow(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	db, err := s.notif.DB(mounted)
	if err != nil {
		s.appearsDown(w)
		return
	}
	now := time.Now()
	changes, _ := shadow.ConfigTrail(db, 50)
	doc := shadowDoc{
		Catalogue: shadow.Catalogue, Detectors: shadow.State(db, now),
		NextReset: shadow.NextReset(db, now), ResetDays: int(shadow.ResetEvery / (24 * time.Hour)), MaxMuteHours: int(shadow.MaxMute / time.Hour),
		Changes: changes,
	}
	runDir := fmt.Sprintf("%s/mnt/slot%d/run", s.cfg.StateDir, mounted)
	if a, err := oracle.NewClient(runDir, 2*time.Second).Arbiter(); err == nil {
		doc.Arbiter = &a
	}
	writeJSON(w, doc)
}

// handleShadowObservations , GET /v1/shadow/observations?detector=&about=&limit= , a detector's
// history (or the fleet's), newest first; or with about=mail:7 (chat:12), what was named about
// that one thing, for its own page.
func (s *Server) handleShadowObservations(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	db, err := s.notif.DB(mounted)
	if err != nil {
		s.appearsDown(w)
		return
	}
	q := r.URL.Query()
	var obs []shadow.Observation
	if about := strings.TrimSpace(q.Get("about")); about != "" {
		if len(about) > 64 {
			s.appearsDown(w)
			return
		}
		obs, err = shadow.ObservationsAbout(db, about)
	} else {
		det := strings.TrimSpace(q.Get("detector"))
		if _, known := shadow.DetectorByID(det); det != "" && !known {
			http.Error(w, "no such detector", http.StatusBadRequest)
			return
		}
		limit, _ := strconv.Atoi(q.Get("limit"))
		obs, err = shadow.Observations(db, det, limit)
	}
	if err != nil {
		s.appearsDown(w)
		return
	}
	writeJSON(w, shadowObservationsDoc{Observations: obs})
}

// handleShadowTrail , GET /v1/shadow/chat?id= , a chat's audit trail: each of the box's answers
// in order, what it asked there, what it had been given, its reasoning as saved, and the
// arbiter's record of it ("after a conversation the user can ask the daemon what it asked and
// why").
func (s *Server) handleShadowTrail(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	id, perr := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	db, err := s.notif.DB(mounted)
	if perr != nil || id <= 0 || err != nil {
		s.appearsDown(w)
		return
	}
	trail, err := shadow.Trail(db, id)
	if err != nil {
		s.appearsDown(w)
		return
	}
	doc := shadowTrailDoc{Chat: id, Trail: trail}
	if a, ok := shadow.AskedFor(db, id); ok {
		doc.Asked, doc.Reading = &a, a.Reading(time.Now())
	}
	writeJSON(w, doc)
}

type shadowReadRequest struct {
	Chat int64 `json:"chat"`
}

type shadowReadDoc struct {
	OK bool `json:"ok"`
	// Started: the reading began with this call. Reading: one is going (this one, or one asked
	// for before). The chat's audit trail (GET /v1/shadow/chat) says when it is done and what
	// came of it.
	Started bool   `json:"started"`
	Reading bool   `json:"reading"`
	Why     string `json:"why,omitempty"`
}

// handleShadowRead , POST /v1/shadow/read {chat} , the arbiter reads one chat now, because the
// person asked: its two newest answers that have not been read, however fresh, and the
// conversation's conduct. It answers at once; the reading goes on in ghost.shadowd for a minute
// or several (a request held open that long does not survive a phone), and the chat's audit
// trail carries `reading` while it does and `asked` with how it ended.
func (s *Server) handleShadowRead(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	var q shadowReadRequest
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&q) != nil || q.Chat <= 0 {
		http.Error(w, "{\"chat\":id}", http.StatusBadRequest)
		return
	}
	runDir := fmt.Sprintf("%s/mnt/slot%d/run", s.cfg.StateDir, mounted)
	resp, err := ctlsock.NewClientTimeout(shadowService, runDir, 10*time.Second).Call("read", map[string]any{"chat": q.Chat, "later": true})
	var out shadowReadDoc
	switch {
	case err != nil:
		out.Why = "ghost.shadowd did not answer"
	case !resp.OK:
		out.Why = resp.Err
	default:
		_ = json.Unmarshal(resp.Data, &out)
		out.OK = true
	}
	writeJSON(w, out)
}

// handleShadowThreshold , POST /v1/shadow/threshold {detector, value} , moves a detector's
// threshold inside its published bounds. Outside them, or for a detector with nothing to tune,
// it is refused with the bounds in the answer. Every change is logged (arbiter capture).
func (s *Server) handleShadowThreshold(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	var q shadowThresholdRequest
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&q) != nil {
		http.Error(w, "{\"detector\":\"…\",\"value\":n}", http.StatusBadRequest)
		return
	}
	db, err := s.notif.DB(mounted)
	if err != nil {
		s.appearsDown(w)
		return
	}
	switch err := shadow.SetThreshold(db, q.Detector, q.Value, time.Now()); {
	case errors.Is(err, shadow.ErrUnknown):
		http.Error(w, "no such detector", http.StatusBadRequest)
	case errors.Is(err, shadow.ErrBounds):
		d, _ := shadow.DetectorByID(q.Detector)
		if d.Min == d.Max {
			http.Error(w, "this detector has no threshold to move", http.StatusBadRequest)
			return
		}
		http.Error(w, "the threshold moves between "+strconv.FormatFloat(d.Min, 'f', -1, 64)+" and "+strconv.FormatFloat(d.Max, 'f', -1, 64), http.StatusBadRequest)
	case err != nil:
		s.appearsDown(w)
	default:
		writeJSON(w, shadowThresholdDoc{OK: true, Threshold: shadow.Threshold(db, q.Detector)})
	}
}

// handleShadowMute , POST /v1/shadow/mute {detector, hours} , quiets one detector for up to
// three days (more is three days; 0 lifts it). The detector goes on recording while muted.
func (s *Server) handleShadowMute(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	var q shadowMuteRequest
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&q) != nil || q.Hours != q.Hours || q.Hours < 0 {
		http.Error(w, "{\"detector\":\"…\",\"hours\":n}", http.StatusBadRequest)
		return
	}
	db, err := s.notif.DB(mounted)
	if err != nil {
		s.appearsDown(w)
		return
	}
	if q.Hours > 72 {
		q.Hours = 72
	}
	until, err := shadow.Mute(db, q.Detector, time.Duration(q.Hours*float64(time.Hour)), time.Now())
	if errors.Is(err, shadow.ErrUnknown) {
		http.Error(w, "no such detector", http.StatusBadRequest)
		return
	}
	if err != nil {
		s.appearsDown(w)
		return
	}
	writeJSON(w, shadowMuteDoc{OK: true, MutedUntil: until})
}
