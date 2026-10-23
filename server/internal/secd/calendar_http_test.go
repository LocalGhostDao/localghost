package secd

import (
	"strings"
	"testing"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
)

// An upload is checked and trimmed: a window of at most three years, events starting inside it,
// statuses from the known set, long strings cut on a rune boundary.
func TestCleanCalendar(t *testing.T) {
	u := calendarUpload{From: 1000, To: 1000 + 86400, Events: []hw.CalendarEvent{
		{EventID: 1, Begin: 500, End: 2000, Title: "began before the window"},
		{EventID: 2, Begin: 2000, End: 1000, Title: strings.Repeat("é", 400), Status: "maybe", Self: "accepted",
			Attendees: []hw.CalendarAttendee{{Name: "Paul", Status: "yes"}}},
		{EventID: 0, Begin: 3000, Title: "no id"},
		{EventID: 3, Begin: 1000 + 86400, Title: "at the window's end"},
	}}
	if _, ok := cleanCalendar(&u); !ok {
		t.Fatal("a good upload")
	}
	if u.Device != "phone" || len(u.Events) != 1 {
		t.Fatalf("device default and events kept: %q %d", u.Device, len(u.Events))
	}
	e := u.Events[0]
	if len(e.Title) > 500 || !strings.HasSuffix(e.Title, "é") || e.End != e.Begin || e.Status != "" || e.Self != "accepted" || e.Attendees[0].Status != "" {
		t.Fatalf("trimmed: %d %q %+v", len(e.Title), e.Title[len(e.Title)-2:], e)
	}
	bad := calendarUpload{From: 1000, To: 1000 + 4*366*86400}
	if _, ok := cleanCalendar(&bad); ok {
		t.Fatal("a window over three years is refused")
	}
	if _, ok := cleanCalendar(&calendarUpload{}); ok {
		t.Fatal("no window is refused")
	}
}
