package secd

import (
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/shadow"
)

// ghost.shadowd's notifications can be muted for three days at most, whatever is asked: mute,
// not disable. Every other service's mute is as asked.
func TestShadowdIsMutedForThreeDaysAtMost(t *testing.T) {
	for _, c := range []struct {
		scope   string
		forever bool
		d       time.Duration
		wantD   time.Duration
		wantF   bool
	}{
		{"ghost.shadowd", true, 0, shadow.MaxMute, false},
		{"ghost.shadowd", false, 7 * 24 * time.Hour, shadow.MaxMute, false},
		{"ghost.shadowd", false, time.Hour, time.Hour, false},
		{"ghost.synthd", true, 0, 0, true},
		{"ghost.synthd", false, 7 * 24 * time.Hour, 7 * 24 * time.Hour, false},
		{"*", true, 0, 0, true},
	} {
		if d, f := shadowMuteLength(c.scope, c.forever, c.d); d != c.wantD || f != c.wantF {
			t.Errorf("%s forever=%v %v: got %v %v, want %v %v", c.scope, c.forever, c.d, d, f, c.wantD, c.wantF)
		}
	}
}
