package profile

import "testing"

func collect(warm func(Stage) bool) []Progress {
	var got []Progress
	_ = StreamUnlock(warm, func(Stage) error { return nil }, func(p Progress) { got = append(got, p) })
	return got
}

func TestColdStreamsEveryStage(t *testing.T) {
	got := collect(func(Stage) bool { return false })
	for _, p := range got {
		if (p.Stage == StageMount || p.Stage == StageStartDB) && p.State == Skipped {
			t.Fatal("a cold account must actually run mount/DB, not skip")
		}
	}
}

func TestHotSkipsHeavyStages(t *testing.T) {
	got := collect(func(Stage) bool { return true })
	for _, p := range got {
		if p.Stage == StageMount && p.State != Skipped {
			t.Fatal("a warm account should skip the mount stage")
		}
	}
}

func TestColdUnlocksEmitIdenticalStream(t *testing.T) {
	// The stream is presentation only; it does not encode what the PIN did. Two cold unlocks must
	// therefore be byte-identical, which is what keeps a wipe-PIN entry indistinguishable from a
	// normal failed unlock at the presentation layer.
	a := collect(func(Stage) bool { return false })
	b := collect(func(Stage) bool { return false })
	if len(a) != len(b) {
		t.Fatal("streams differ in length")
	}
	for i := range a {
		if a[i] != b[i] {
			t.Fatalf("stage %d differs: %+v vs %+v", i, a[i], b[i])
		}
	}
}

func TestStageOrderIsFixed(t *testing.T) {
	got := collect(func(Stage) bool { return false })
	// Extract the stage sequence (first appearance of each).
	want := []Stage{StageResolve, StageUnseal, StageMount, StageStartDB, StageStartCache, StageDaemons, StageModel, StageReady}
	seen := make([]Stage, 0, len(want))
	last := Stage(-1)
	for _, p := range got {
		if p.Stage != last {
			seen = append(seen, p.Stage)
			last = p.Stage
		}
	}
	if len(seen) != len(want) {
		t.Fatalf("stage count: got %d want %d", len(seen), len(want))
	}
	for i := range want {
		if seen[i] != want[i] {
			t.Fatalf("stage %d: got %v want %v", i, seen[i], want[i])
		}
	}
}
