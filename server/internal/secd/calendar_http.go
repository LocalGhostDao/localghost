package secd

// THE CALENDAR , the app's side of hw/calendarstore.go. The phone reads its own calendar store
// (every calendar it syncs) and posts a window of it here; the box keeps it and answers the
// agenda, a day and a search. Read-only: nothing here writes to any calendar anywhere.

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
)

// calendarUpload is what the phone posts.
type calendarUpload struct {
	Device    string             `json:"device"`
	Zone      string             `json:"zone"`
	Offset    int                `json:"utcOffset"`
	From      int64              `json:"from"`
	To        int64              `json:"to"`
	Calendars []hw.CalendarInfo  `json:"calendars"`
	Events    []hw.CalendarEvent `json:"events"`
}

const (
	calendarMaxBody   = 24 << 20
	calendarMaxEvents = 30000
	calendarMaxWindow = 3 * 366 * 86400
)

// calClip keeps at most n bytes of s, on a rune boundary.
func calClip(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

func calOneOf(s string, allowed ...string) string {
	for _, a := range allowed {
		if s == a {
			return s
		}
	}
	return ""
}

// cleanCalendar checks an upload and trims what it carries to sizes the box keeps; ok false when
// it is not an upload at all.
func cleanCalendar(u *calendarUpload) (string, bool) {
	u.Device = calClip(u.Device, 64)
	if u.Device == "" {
		u.Device = "phone"
	}
	if u.From <= 0 || u.To <= u.From || u.To-u.From > calendarMaxWindow {
		return "from and to are a window of at most three years", false
	}
	if len(u.Events) > calendarMaxEvents {
		return "too many events in one upload", false
	}
	u.Zone = calClip(u.Zone, 64)
	if u.Offset < -14*3600 || u.Offset > 14*3600 {
		u.Offset = 0
	}
	if len(u.Calendars) > 200 {
		u.Calendars = u.Calendars[:200]
	}
	for i := range u.Calendars {
		c := &u.Calendars[i]
		c.Name, c.Account, c.Type, c.Color = calClip(c.Name, 200), calClip(c.Account, 200), calClip(c.Type, 100), calClip(c.Color, 16)
	}
	kept := u.Events[:0]
	for _, e := range u.Events {
		// the window is replaced by start time, so an instance that starts outside it is not this
		// upload's to keep (the phone reads by overlap; the one before `from` is last window's)
		if e.Begin < u.From || e.Begin >= u.To || e.EventID <= 0 {
			continue
		}
		if e.End < e.Begin {
			e.End = e.Begin
		}
		e.Title, e.Location, e.Notes = calClip(e.Title, 500), calClip(e.Location, 500), calClip(e.Notes, 8000)
		e.Calendar, e.Account, e.Organizer, e.TZ = calClip(e.Calendar, 200), calClip(e.Account, 200), calClip(e.Organizer, 200), calClip(e.TZ, 64)
		e.Status = calOneOf(e.Status, "confirmed", "tentative", "cancelled")
		e.Self = calOneOf(e.Self, "accepted", "declined", "tentative", "invited", "none")
		if len(e.Attendees) > 100 {
			e.Attendees = e.Attendees[:100]
		}
		for j := range e.Attendees {
			a := &e.Attendees[j]
			a.Name, a.Email = calClip(a.Name, 200), calClip(a.Email, 200)
			a.Status = calOneOf(a.Status, "accepted", "declined", "tentative", "invited", "none")
		}
		kept = append(kept, e)
	}
	u.Events = kept
	return "", true
}

// handleCalendarUpload , POST /v1/calendar/upload , a window of the phone's calendar, replacing
// what the box held for that window from that phone.
func (s *Server) handleCalendarUpload(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, calendarMaxBody))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "a calendar upload is at most 24 MB")
		return
	}
	var u calendarUpload
	if err := json.Unmarshal(body, &u); err != nil {
		writeErr(w, http.StatusBadRequest, "not a calendar upload")
		return
	}
	if why, ok := cleanCalendar(&u); !ok {
		writeErr(w, http.StatusBadRequest, why)
		return
	}
	meta := hw.CalendarMeta{Zone: u.Zone, Offset: u.Offset, Calendars: u.Calendars}
	removed, err := s.notif.CalendarReplace(mounted, u.Device, u.From, u.To, u.Events, meta)
	if err != nil {
		secdLog.Warn("calendar upload failed", "fn", "handleCalendarUpload", "err", err)
		s.appearsDown(w)
		return
	}
	secdLog.Info("calendar upload", "events", len(u.Events), "removed", removed, "calendars", len(u.Calendars))
	writeJSON(w, map[string]any{"ok": true, "events": len(u.Events), "removed": removed})
}

// handleCalendar , GET /v1/calendar?from=&to=&q=&limit= , the events overlapping [from, to)
// (unix seconds), or with q the ones matching every word of it (the window then defaults to two
// years either side of now). No window: the next thirty days. The meta comes with it either way.
func (s *Server) handleCalendar(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	qv := r.URL.Query()
	from, _ := strconv.ParseInt(qv.Get("from"), 10, 64)
	to, _ := strconv.ParseInt(qv.Get("to"), 10, 64)
	limit, _ := strconv.Atoi(qv.Get("limit"))
	terms := strings.Fields(calClip(qv.Get("q"), 200))
	now := time.Now().Unix()
	var evs []hw.CalendarEvent
	var err error
	if len(terms) > 0 {
		if from <= 0 || to <= from {
			from, to = now-2*366*86400, now+2*366*86400
		}
		if len(terms) > 8 {
			terms = terms[:8]
		}
		evs, err = s.notif.CalendarSearch(mounted, terms, from, to, limit)
	} else {
		if from <= 0 || to <= from {
			from, to = now, now+30*86400
		}
		if to-from > calendarMaxWindow {
			to = from + calendarMaxWindow
		}
		evs, err = s.notif.CalendarRange(mounted, from, to, limit)
	}
	if err != nil {
		secdLog.Warn("calendar read failed", "fn", "handleCalendar", "err", err)
		s.appearsDown(w)
		return
	}
	out := map[string]any{"ok": true, "events": evs, "from": from, "to": to}
	if m, ok := s.notif.CalendarMeta(mounted); ok {
		out["meta"] = m
	}
	writeJSON(w, out)
}

// handleCalendarForget , POST /v1/calendar/forget , drops every event and the meta. The phone
// stops sending when its permission is withdrawn; this is the box's half.
func (s *Server) handleCalendarForget(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	if err := s.notif.CalendarForget(mounted); err != nil {
		s.appearsDown(w)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
