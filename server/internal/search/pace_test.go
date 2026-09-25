package search

import (
	"errors"
	"testing"
	"time"
)

func TestPaceAsksOnceAMinuteAndAssumesSlowWhenUnsure(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	calls, onGPU := 0, true
	var err error
	logs := 0
	p := &Pace{
		Probe: func() (bool, error) { calls++; return onGPU, err },
		Log:   func(string, ...any) { logs++ },
		now:   func() time.Time { return now },
	}
	if p.Slow() || calls != 1 || logs != 1 {
		t.Fatalf("gpu: slow=%v calls=%d logs=%d", p.Slow(), calls, logs)
	}
	onGPU = false
	now = now.Add(30 * time.Second)
	if p.Slow() || calls != 1 {
		t.Fatal("asked again within the minute")
	}
	now = now.Add(31 * time.Second)
	if !p.Slow() || calls != 2 || logs != 2 {
		t.Fatalf("cpu not seen: calls=%d logs=%d", calls, logs)
	}
	now = now.Add(2 * time.Minute)
	onGPU, err = true, errors.New("oracled not answering")
	if !p.Slow() || logs != 2 {
		t.Fatal("an unanswered probe must read as slow (and it is no change, so no log)")
	}
	// and is asked again after ten seconds, not a minute: the model came up
	err = nil
	now = now.Add(11 * time.Second)
	if p.Slow() || calls != 4 || logs != 3 {
		t.Fatalf("after the model loaded: slow=%v calls=%d logs=%d", p.Slow(), calls, logs)
	}
	var none *Pace
	if none.Slow() {
		t.Fatal("no pace is the GPU budget")
	}
}

func TestDeadlinesFollowThePace(t *testing.T) {
	slow := &Pace{Probe: func() (bool, error) { return false, nil }}
	fast := &Pace{Probe: func() (bool, error) { return true, nil }}
	cases := []struct {
		got, want time.Duration
	}{
		{(&VisionOracle{}).deadline(), 2 * time.Minute},
		{(&VisionOracle{Pace: fast}).deadline(), 2 * time.Minute},
		{(&VisionOracle{Pace: slow}).deadline(), 15 * time.Minute},
		{(&VisionOracle{Pace: slow, Timeout: 3 * time.Minute, SlowTimeout: 20 * time.Minute}).deadline(), 20 * time.Minute},
		{(&VisionOracle{Pace: slow, Timeout: 30 * time.Minute, SlowTimeout: 20 * time.Minute}).deadline(), 30 * time.Minute}, // never shorter
		{(&TagOracle{}).deadline(), time.Minute},
		{(&TagOracle{Pace: slow}).deadline(), 8 * time.Minute},
	}
	for i, c := range cases {
		if c.got != c.want {
			t.Errorf("case %d: %v, want %v", i, c.got, c.want)
		}
	}
}
