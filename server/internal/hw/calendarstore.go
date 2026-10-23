package hw

// THE CALENDAR , what the phone's own calendar store (Android's CalendarContract: every calendar
// the phone syncs, Google's, Samsung's, Exchange's) holds, read on the phone and sent to the box.
// The box asks no calendar service anything. One row per event instance (a weekly meeting is a
// row per week), the window the phone read replaced whole on every upload: rows the upload
// carried are written with the upload's generation, rows in the window it did not carry are
// deleted, so an event removed on the phone leaves the box at the next sync. Read-only: nothing
// here is ever written back.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// CalendarAttendee is one guest of an event.
type CalendarAttendee struct {
	Name   string `json:"name,omitempty"`
	Email  string `json:"email,omitempty"`
	Status string `json:"status,omitempty"` // accepted, declined, tentative, invited, none
}

// CalendarEvent is one instance of an event.
type CalendarEvent struct {
	Key        string             `json:"key"`
	Device     string             `json:"device,omitempty"`
	EventID    int64              `json:"id"`
	CalendarID int64              `json:"cal"`
	Calendar   string             `json:"calendar,omitempty"`
	Account    string             `json:"account,omitempty"`
	Title      string             `json:"title"`
	Location   string             `json:"location,omitempty"`
	Notes      string             `json:"notes,omitempty"`
	Begin      int64              `json:"begin"`
	End        int64              `json:"end"`
	AllDay     bool               `json:"allDay,omitempty"`
	TZ         string             `json:"tz,omitempty"`
	Status     string             `json:"status,omitempty"` // confirmed, tentative, cancelled
	Self       string             `json:"self,omitempty"`   // the person's own answer: accepted, declined, tentative, invited, none
	Busy       bool               `json:"busy,omitempty"`
	Organizer  string             `json:"organizer,omitempty"`
	Attendees  []CalendarAttendee `json:"attendees,omitempty"`
}

// Live is an event the person is going to: not cancelled, not declined.
func (e CalendarEvent) Live() bool { return e.Status != "cancelled" && e.Self != "declined" }

// CalendarInfo is one calendar on the phone.
type CalendarInfo struct {
	ID      int64  `json:"id"`
	Name    string `json:"name"`
	Account string `json:"account,omitempty"`
	Type    string `json:"type,omitempty"`
	Color   string `json:"color,omitempty"`
	Visible bool   `json:"visible"`
	Events  int    `json:"events"`
}

// CalendarMeta is the last upload's account, kept in settings (calendar_meta).
type CalendarMeta struct {
	At        int64          `json:"at"`
	Device    string         `json:"device"`
	From      int64          `json:"from"`
	To        int64          `json:"to"`
	Zone      string         `json:"zone,omitempty"`
	Offset    int            `json:"utcOffset,omitempty"`
	Events    int            `json:"events"`
	Removed   int            `json:"removed"`
	Calendars []CalendarInfo `json:"calendars"`
}

// CalendarKey is an instance's identity: the device, the event and its start.
func CalendarKey(device string, eventID, begin int64) string {
	return device + "|" + strconv.FormatInt(eventID, 10) + "|" + strconv.FormatInt(begin, 10)
}

const calendarCols = 18

// CalendarReplace writes an upload: every event upserted with the upload's generation, then the
// device's rows in [from, to) of an older generation deleted. No transaction is needed: a crash
// between the two leaves the old rows beside the new ones until the next upload.
func CalendarReplace(db *poltergres.ReadWrite, device string, from, to int64, evs []CalendarEvent, meta CalendarMeta) (int, error) {
	gen := time.Now().UnixNano()
	now := time.Now().Unix()
	for start := 0; start < len(evs); start += 200 {
		end := start + 200
		if end > len(evs) {
			end = len(evs)
		}
		var sb strings.Builder
		sb.WriteString("INSERT INTO calendar_events (key, device, event_id, calendar_id, calendar, account, title, location, notes, begin_ts, end_ts, all_day, tz, status, self_status, busy, organizer, attendees, gen, updated_at) VALUES ")
		args := make([]any, 0, (end-start)*calendarCols+2)
		for i, e := range evs[start:end] {
			if i > 0 {
				sb.WriteString(",")
			}
			att := "[]"
			if len(e.Attendees) > 0 {
				b, _ := json.Marshal(e.Attendees)
				att = string(b)
			}
			n := len(args)
			sb.WriteString("(")
			for j := 1; j <= calendarCols; j++ {
				if j > 1 {
					sb.WriteString(",")
				}
				sb.WriteString(fmt.Sprintf("$%d", n+j))
			}
			sb.WriteString(fmt.Sprintf(",%d,%d)", gen, now))
			args = append(args, CalendarKey(device, e.EventID, e.Begin), device, e.EventID, e.CalendarID, e.Calendar, e.Account, e.Title,
				e.Location, e.Notes, e.Begin, e.End, e.AllDay, e.TZ, e.Status, e.Self, e.Busy, e.Organizer, att)
		}
		sb.WriteString(" ON CONFLICT (key) DO UPDATE SET calendar_id = EXCLUDED.calendar_id, calendar = EXCLUDED.calendar, account = EXCLUDED.account, " +
			"title = EXCLUDED.title, location = EXCLUDED.location, notes = EXCLUDED.notes, end_ts = EXCLUDED.end_ts, all_day = EXCLUDED.all_day, " +
			"tz = EXCLUDED.tz, status = EXCLUDED.status, self_status = EXCLUDED.self_status, busy = EXCLUDED.busy, organizer = EXCLUDED.organizer, " +
			"attendees = EXCLUDED.attendees, gen = EXCLUDED.gen, updated_at = EXCLUDED.updated_at")
		if err := db.Exec(sb.String(), args...); err != nil {
			return 0, err
		}
	}
	rows, err := db.Query("DELETE FROM calendar_events WHERE device = $1 AND begin_ts >= $2 AND begin_ts < $3 AND gen <> $4 RETURNING 1", device, from, to, gen)
	if err != nil {
		return 0, err
	}
	removed := len(rows.Vals)
	meta.At, meta.Device, meta.From, meta.To, meta.Events, meta.Removed = now, device, from, to, len(evs), removed
	b, _ := json.Marshal(meta)
	if err := db.Exec("INSERT INTO settings (key, value) VALUES ('calendar_meta', $1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", string(b)); err != nil {
		return removed, err
	}
	return removed, nil
}

// CalendarMetaOf is the last upload's account; ok false when the phone never sent one.
func CalendarMetaOf(q Querier) (CalendarMeta, bool) {
	var m CalendarMeta
	rows, err := q.Query("SELECT value FROM settings WHERE key = 'calendar_meta'")
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return m, false
	}
	return m, json.Unmarshal([]byte(*rows.Vals[0][0]), &m) == nil
}

const calendarSelect = "SELECT key, device, event_id, calendar_id, calendar, account, title, location, notes, begin_ts, end_ts, all_day, tz, status, self_status, busy, organizer, attendees FROM calendar_events"

func scanCalendar(rows *poltergres.Rows) []CalendarEvent {
	out := []CalendarEvent{}
	for _, v := range rows.Vals {
		if len(v) < calendarCols {
			continue
		}
		s := func(i int) string {
			if v[i] == nil {
				return ""
			}
			return *v[i]
		}
		n := func(i int) int64 { x, _ := strconv.ParseInt(s(i), 10, 64); return x }
		b := func(i int) bool { return s(i) == "t" || s(i) == "true" }
		e := CalendarEvent{Key: s(0), Device: s(1), EventID: n(2), CalendarID: n(3), Calendar: s(4), Account: s(5), Title: s(6),
			Location: s(7), Notes: s(8), Begin: n(9), End: n(10), AllDay: b(11), TZ: s(12), Status: s(13), Self: s(14), Busy: b(15), Organizer: s(16)}
		if a := s(17); a != "" && a != "[]" {
			_ = json.Unmarshal([]byte(a), &e.Attendees)
		}
		out = append(out, e)
	}
	return out
}

// CalendarRange is every event overlapping [from, to), earliest first, at most limit.
func CalendarRange(q Querier, from, to int64, limit int) ([]CalendarEvent, error) {
	if limit <= 0 || limit > 5000 {
		limit = 2000
	}
	rows, err := q.Query(calendarSelect+" WHERE begin_ts < $2 AND (end_ts > $1 OR (end_ts <= begin_ts AND begin_ts >= $1)) ORDER BY begin_ts, all_day DESC, title LIMIT $3", from, to, limit)
	if err != nil {
		return nil, err
	}
	return scanCalendar(rows), nil
}

// CalendarSearch is the events whose title, place, notes or guests contain every term, within
// [from, to), nearest to now first is the caller's business; here earliest first.
func CalendarSearch(q Querier, terms []string, from, to int64, limit int) ([]CalendarEvent, error) {
	if len(terms) == 0 {
		return []CalendarEvent{}, nil
	}
	if limit <= 0 || limit > 500 {
		limit = 50
	}
	where := []string{"begin_ts >= $1", "begin_ts < $2"}
	args := []any{from, to}
	for _, t := range terms {
		args = append(args, "%"+strings.ToLower(t)+"%")
		i := len(args)
		where = append(where, fmt.Sprintf("(lower(title) LIKE $%d OR lower(location) LIKE $%d OR lower(notes) LIKE $%d OR lower(attendees) LIKE $%d)", i, i, i, i))
	}
	args = append(args, limit)
	rows, err := q.Query(calendarSelect+" WHERE "+strings.Join(where, " AND ")+fmt.Sprintf(" ORDER BY begin_ts LIMIT $%d", len(args)), args...)
	if err != nil {
		return nil, err
	}
	return scanCalendar(rows), nil
}

// CalendarUpcoming is what is next: an event under way (not all-day) and the ones starting in the
// next hours, live ones only, at most n.
func CalendarUpcoming(q Querier, now time.Time, hours, n int) ([]CalendarEvent, error) {
	evs, err := CalendarRange(q, now.Unix(), now.Add(time.Duration(hours)*time.Hour).Unix(), 200)
	if err != nil {
		return nil, err
	}
	out := []CalendarEvent{}
	for _, e := range evs {
		if !e.Live() || e.AllDay {
			continue // a day's all-day events are the DAY page's; HOME says what is next by the clock
		}
		out = append(out, e)
		if len(out) >= n {
			break
		}
	}
	return out, nil
}

// CalendarZone is the zone the phone's calendar was read in, else the box's (LocalZone).
func CalendarZone(q Querier) *time.Location {
	if m, ok := CalendarMetaOf(q); ok {
		if m.Zone != "" {
			if loc, err := time.LoadLocation(m.Zone); err == nil {
				return loc
			}
		}
		if m.Offset != 0 {
			return time.FixedZone("phone", m.Offset)
		}
	}
	return LocalZone(q)
}

// CalendarLine is one event in words, in the zone given: "Thu 16 Oct 10:00–11:00 Lunch with Paul
// · Dishoom" ("all day" for an all-day one, the date in UTC since that is how all-day events are
// kept).
func CalendarLine(e CalendarEvent, loc *time.Location, withDate bool) string {
	var when string
	if e.AllDay {
		d := time.Unix(e.Begin, 0).UTC()
		days := int((e.End - e.Begin + 3600) / 86400)
		when = "all day"
		if withDate {
			when = d.Format("Mon 2 Jan") + ", all day"
		}
		if days > 1 {
			when += fmt.Sprintf(" (%d days, to %s)", days, time.Unix(e.End-1, 0).UTC().Format("Mon 2 Jan"))
		}
	} else {
		b, en := time.Unix(e.Begin, 0).In(loc), time.Unix(e.End, 0).In(loc)
		when = b.Format("15:04")
		if en.After(b) {
			if en.YearDay() != b.YearDay() || en.Year() != b.Year() {
				when += "–" + en.Format("Mon 2 Jan 15:04")
			} else {
				when += "–" + en.Format("15:04")
			}
		}
		if withDate {
			when = b.Format("Mon 2 Jan") + " " + when
		}
	}
	s := when + " " + strings.TrimSpace(e.Title)
	if strings.TrimSpace(e.Title) == "" {
		s = when + " (no title)"
	}
	if l := strings.TrimSpace(e.Location); l != "" {
		s += " · " + l
	}
	var with []string
	others := 0
	for _, a := range e.Attendees {
		n := strings.TrimSpace(a.Name)
		if n == "" {
			n = a.Email
		}
		if n == "" || (a.Email != "" && strings.EqualFold(a.Email, e.Account)) {
			continue
		}
		others++
		if len(with) < 4 {
			with = append(with, n)
		}
	}
	if len(with) > 0 {
		s += " · with " + strings.Join(with, ", ")
		if extra := others - len(with); extra > 0 {
			s += fmt.Sprintf(" and %d more", extra)
		}
	}
	switch {
	case e.Status == "cancelled":
		s += " (cancelled)"
	case e.Self == "declined":
		s += " (declined)"
	case e.Self == "tentative" || e.Status == "tentative":
		s += " (tentative)"
	}
	return s
}

// The NotifStore wrappers, for secd.

func (s *NotifStore) CalendarReplace(slot int, device string, from, to int64, evs []CalendarEvent, meta CalendarMeta) (int, error) {
	c, err := s.pg(slot)
	if err != nil {
		return 0, err
	}
	return CalendarReplace(c, device, from, to, evs, meta)
}

func (s *NotifStore) CalendarRange(slot int, from, to int64, limit int) ([]CalendarEvent, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	return CalendarRange(c, from, to, limit)
}

func (s *NotifStore) CalendarSearch(slot int, terms []string, from, to int64, limit int) ([]CalendarEvent, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	return CalendarSearch(c, terms, from, to, limit)
}

func (s *NotifStore) CalendarMeta(slot int) (CalendarMeta, bool) {
	c, err := s.pg(slot)
	if err != nil {
		return CalendarMeta{}, false
	}
	return CalendarMetaOf(c)
}

// CalendarForget drops every row and the meta: what SETTINGS › CALENDAR › FORGET does.
func CalendarForget(db *poltergres.ReadWrite) error {
	if err := db.Exec("DELETE FROM calendar_events"); err != nil {
		return err
	}
	return db.Exec("DELETE FROM settings WHERE key = 'calendar_meta'")
}

func (s *NotifStore) CalendarForget(slot int) error {
	c, err := s.pg(slot)
	if err != nil {
		return err
	}
	return CalendarForget(c)
}
