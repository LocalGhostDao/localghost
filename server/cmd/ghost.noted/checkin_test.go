package main

import (
	"testing"
	"time"
)

// A check-in written after midnight for the day before is filed under the day it names; one for
// the day it is written on keeps its time; anything else is untouched.
func TestCheckinDayTs(t *testing.T) {
	oneAM := time.Date(2026, 10, 10, 1, 4, 0, 0, time.UTC).Unix()
	if got := checkinDayTs("Daily check-in 2026-10-09", oneAM); got != time.Date(2026, 10, 9, 12, 0, 0, 0, time.UTC).Unix() {
		t.Fatalf("yesterday's check-in filed at %d, want the 9th's noon", got)
	}
	tenPM := time.Date(2026, 10, 10, 22, 0, 0, 0, time.UTC).Unix()
	if got := checkinDayTs("Daily check-in 2026-10-10", tenPM); got != tenPM {
		t.Fatalf("today's check-in moved to %d", got)
	}
	if got := checkinDayTs("a note of my own", oneAM); got != oneAM {
		t.Fatalf("a plain note moved to %d", got)
	}
	if got := checkinDayTs("Daily check-in whenever", oneAM); got != oneAM {
		t.Fatalf("a check-in with no date moved to %d", got)
	}
}
