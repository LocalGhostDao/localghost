package hw

import (
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/apparedis"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// The calendar rows: an upload written, a second upload of the same window replacing it (an event
// gone from the phone leaves the box, one outside the window stays), the range, the search, what
// is next, the meta and its zone.
func TestCalendarStoreAgainstPostgres(t *testing.T) {
	dir := os.Getenv("GHOST_PG_SOCKET_DIR")
	if dir == "" {
		t.Skip("GHOST_PG_SOCKET_DIR not set; no Postgres to test against")
	}
	port := 5432
	if p, err := strconv.Atoi(os.Getenv("GHOST_PG_PORT")); err == nil {
		port = p
	}
	user := os.Getenv("GHOST_PG_USER")
	if user == "" {
		user = "postgres"
	}
	admin := poltergres.NewReadWrite(dir, port, user, "", "postgres")
	_ = admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_calendar")
	if err := admin.ExecSimple("CREATE DATABASE lgtest_calendar"); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", "lgtest_calendar")
	if _, err := ConvergeSchema(db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	tmp := t.TempDir()
	ns := &NotifStore{rw: map[int]*poltergres.ReadWrite{0: db}, rd: map[int]*apparedis.ReadWrite{}, pgSocketFor: func(int) string { return tmp + "/postgres" }}

	if _, ok := ns.CalendarMeta(0); ok {
		t.Fatal("no upload yet, no meta")
	}
	base := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC).Unix()
	from, to := base-30*86400, base+30*86400
	lunch := CalendarEvent{EventID: 1, CalendarID: 3, Calendar: "Personal", Account: "vlad@example.org", Title: "Lunch with Paul", Location: "Dishoom",
		Begin: base + 12*3600, End: base + 13*3600, Status: "confirmed", Self: "accepted", Busy: true,
		Attendees: []CalendarAttendee{{Name: "Paul", Email: "paul@example.org", Status: "accepted"}, {Email: "vlad@example.org"}}}
	standup := CalendarEvent{EventID: 2, CalendarID: 4, Calendar: "Work", Title: "Standup", Begin: base + 9*3600, End: base + 9*3600 + 900, Status: "confirmed", Self: "declined"}
	holiday := CalendarEvent{EventID: 3, CalendarID: 3, Title: "Romania", Begin: base + 86400, End: base + 4*86400, AllDay: true}
	gone := CalendarEvent{EventID: 4, CalendarID: 3, Title: "Dentist", Begin: base + 2*86400 + 15*3600, End: base + 2*86400 + 16*3600}
	old := CalendarEvent{EventID: 5, CalendarID: 3, Title: "Old thing", Begin: base - 200*86400, End: base - 200*86400 + 3600}

	if _, err := ns.CalendarReplace(0, "phone-a", base-400*86400, base-100*86400, []CalendarEvent{old}, CalendarMeta{}); err != nil {
		t.Fatal(err)
	}
	removed, err := ns.CalendarReplace(0, "phone-a", from, to, []CalendarEvent{lunch, standup, holiday, gone}, CalendarMeta{Zone: "Europe/London"})
	if err != nil || removed != 0 {
		t.Fatalf("first upload: removed %d err %v", removed, err)
	}
	lunch.Title = "Lunch with Paul and Ana"
	removed, err = ns.CalendarReplace(0, "phone-a", from, to, []CalendarEvent{lunch, standup, holiday},
		CalendarMeta{Zone: "Europe/London", Calendars: []CalendarInfo{{ID: 3, Name: "Personal", Visible: true, Events: 2}}})
	if err != nil || removed != 1 {
		t.Fatalf("second upload must remove the dentist: removed %d err %v", removed, err)
	}
	all, err := ns.CalendarRange(0, base-400*86400, base+400*86400, 0)
	if err != nil || len(all) != 4 {
		t.Fatalf("range: %d %v", len(all), err)
	}
	if all[0].Title != "Old thing" {
		t.Fatalf("an event outside the replaced window stays, earliest first: %+v", all[0])
	}
	day, _ := ns.CalendarRange(0, base, base+86400, 0)
	if len(day) != 2 || day[0].Title != "Standup" || day[1].Title != "Lunch with Paul and Ana" {
		t.Fatalf("the day: %+v", day)
	}
	if len(day[1].Attendees) != 2 || day[1].Attendees[0].Name != "Paul" || !day[1].Busy || day[1].Calendar != "Personal" {
		t.Fatalf("the row round trip: %+v", day[1])
	}
	// the holiday spans the 17th to the 19th; asking for the 18th finds it
	if d, _ := ns.CalendarRange(0, base+2*86400, base+3*86400, 0); len(d) != 1 || !d[0].AllDay {
		t.Fatalf("a multi-day event overlaps the days inside it: %+v", d)
	}
	hits, err := ns.CalendarSearch(0, []string{"paul"}, from, to, 0)
	if err != nil || len(hits) != 1 {
		t.Fatalf("search by guest and title: %+v %v", hits, err)
	}
	if hits, _ := ns.CalendarSearch(0, []string{"dishoom", "ana"}, from, to, 0); len(hits) != 1 {
		t.Fatalf("every term must match: %+v", hits)
	}
	if hits, _ := ns.CalendarSearch(0, []string{"dentist"}, from, to, 0); len(hits) != 0 {
		t.Fatalf("the removed event is gone: %+v", hits)
	}
	next, err := CalendarUpcoming(db, time.Unix(base+8*3600, 0), 36, 3)
	if err != nil || len(next) != 1 || next[0].EventID != 1 {
		t.Fatalf("what is next skips the declined standup and the all-day holiday: %+v %v", next, err)
	}
	m, ok := ns.CalendarMeta(0)
	if !ok || m.Device != "phone-a" || m.Events != 3 || m.Removed != 1 || len(m.Calendars) != 1 || m.From != from {
		t.Fatalf("meta: %+v", m)
	}
	if z := CalendarZone(db); z.String() != "Europe/London" {
		t.Fatalf("zone: %s", z)
	}
	line := CalendarLine(day[1], CalendarZone(db), true)
	if !strings.HasPrefix(line, "Fri 16 Oct 13:00–14:00 Lunch with Paul and Ana · Dishoom · with Paul") || strings.Contains(line, "vlad@") {
		t.Fatalf("line: %q", line)
	}
	_ = admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_calendar")
}

func TestCalendarLine(t *testing.T) {
	loc := time.UTC
	b := time.Date(2026, 10, 16, 0, 0, 0, 0, time.UTC).Unix()
	e := CalendarEvent{Title: "Romania", Begin: b, End: b + 3*86400, AllDay: true}
	if got := CalendarLine(e, loc, true); got != "Fri 16 Oct, all day (3 days, to Sun 18 Oct) Romania" {
		t.Fatalf("%q", got)
	}
	e = CalendarEvent{Begin: b + 23*3600, End: b + 25*3600, Status: "cancelled",
		Attendees: []CalendarAttendee{{Name: "A"}, {Name: "B"}, {Name: "C"}, {Name: "D"}, {Name: "E"}, {Name: "F"}}}
	if got := CalendarLine(e, loc, false); got != "23:00–Sat 17 Oct 01:00 (no title) · with A, B, C, D and 2 more (cancelled)" {
		t.Fatalf("%q", got)
	}
}
