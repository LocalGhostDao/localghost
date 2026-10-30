package profile

import (
	"sort"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/auth"
	"github.com/LocalGhostDao/localghost/server/internal/wipe"
)

func quickGateAccounts(t *testing.T) *Accounts {
	t.Helper()
	s, err := NewSetup([]byte("box-salt"))
	must(t, err)
	must(t, s.SetMain("1111"))
	must(t, s.SetWipe("3333"))
	reg, err := s.Finalize()
	must(t, err)
	pol := auth.DefaultPolicy()
	pol.BaseDelay = 150 * time.Millisecond // the default's shape, faster
	return NewAccounts(reg, auth.NewGate(pol, auth.NewMemoryStore()), wipe.NewWiper(wipe.NewKeyVault(), nil, nil))
}

// PROBE (free-time audit, 30 Sep 2026): the wipe PIN presents as a wrong PIN, but does the rate
// limiter treat it as one? Three wrong guesses, then a candidate, then one more guess at once:
// if the candidate was the wipe PIN, the counter was reset and the next guess is evaluated (the
// KDF runs, ~Argon2 time); if it was wrong, the next guess is throttled (answered at once).
func TestProbeGateTellsTheWipePin(t *testing.T) {
	try := func(candidate string) (Outcome, time.Duration) {
		a := quickGateAccounts(t)
		for i := 0; i < 3; i++ {
			a.Unlock("dev", "0000")
		}
		time.Sleep(200 * time.Millisecond) // wait out the first cooldown
		a.Unlock("dev", candidate)
		t0 := time.Now()
		d := a.Unlock("dev", "9999")
		return d.Outcome, time.Since(t0)
	}
	oWipe, tWipe := try("3333")
	oWrong, tWrong := try("5555")
	t.Logf("after the wipe PIN: next guess outcome=%v in %v", oWipe, tWipe.Round(time.Millisecond))
	t.Logf("after a wrong PIN: next guess outcome=%v in %v", oWrong, tWrong.Round(time.Millisecond))
	if oWipe != oWrong {
		t.Logf("TELL: the limiter treats the wipe PIN differently from a wrong one")
	}
}

// After the fix: the wipe PIN counts like a wrong PIN, so the guess after it is treated the same
// either way; and a throttled guess takes the KDF's time like an evaluated one.
func TestGateTreatsTheWipePinAsWrong(t *testing.T) {
	next := func(candidate string) (Outcome, time.Duration) {
		a := quickGateAccounts(t)
		for i := 0; i < 3; i++ {
			a.Unlock("dev", "0000")
		}
		time.Sleep(200 * time.Millisecond)
		a.Unlock("dev", candidate)
		t0 := time.Now()
		d := a.Unlock("dev", "9999")
		return d.Outcome, time.Since(t0)
	}
	// The tell this guards against is a guess answered WITHOUT the KDF (microseconds against
	// Argon2's tens of milliseconds), so the check is against the KDF's own time on this machine,
	// measured here, not the two paths against each other: on a shared CI runner one Argon2 run
	// takes twice another (117 ms against 69 ms, run #75) and that is a noisy neighbour, not a
	// tell. Five runs each, interleaved so a burst of load lands on both, the median kept.
	var kdf []time.Duration
	for i := 0; i < 3; i++ {
		t0 := time.Now()
		PinKey("9999", []byte("box-salt"))
		kdf = append(kdf, time.Since(t0))
	}
	sort.Slice(kdf, func(i, j int) bool { return kdf[i] < kdf[j] })
	ref := kdf[1]
	var oWipe, oWrong Outcome
	var dWipe, dWrong []time.Duration
	for i := 0; i < 5; i++ {
		ow, dw := next("3333")
		or, dr := next("5555")
		if i > 0 && (ow != oWipe || or != oWrong) {
			t.Fatalf("outcomes vary between runs: wipe %v/%v, wrong %v/%v", oWipe, ow, oWrong, or)
		}
		oWipe, oWrong = ow, or
		dWipe, dWrong = append(dWipe, dw), append(dWrong, dr)
	}
	sort.Slice(dWipe, func(i, j int) bool { return dWipe[i] < dWipe[j] })
	sort.Slice(dWrong, func(i, j int) bool { return dWrong[i] < dWrong[j] })
	tWipe, tWrong := dWipe[2], dWrong[2]
	if oWipe != oWrong {
		t.Fatalf("the limiter still tells: after wipe %v, after wrong %v", oWipe, oWrong)
	}
	// both ran the KDF: each at least a fair share of one Argon2 run here, and neither more
	// than a few times the other (a skipped KDF is a hundred times quicker, not twice)
	floor := ref * 4 / 10
	lo, hi := tWipe, tWrong
	if lo > hi {
		lo, hi = hi, lo
	}
	if lo < floor || float64(hi) > 4*float64(lo) {
		t.Fatalf("times differ: after wipe %v, after wrong %v (one KDF run here: %v)", tWipe, tWrong, ref)
	}
}

// The owner's confirmation still works after a coercer's run of wrong guesses: the wipe PIN, typed
// once the cooldown allows, is the twelfth failure and locks the limiter for an hour, and the main
// PIN still confirms the armed wipe through that lockout.
func TestWipeConfirmsThroughTheLimiter(t *testing.T) {
	s, err := NewSetup([]byte("box-salt"))
	must(t, err)
	must(t, s.SetMain("1111"))
	must(t, s.SetWipe("3333"))
	reg, err := s.Finalize()
	must(t, err)
	pol := auth.DefaultPolicy()
	pol.BaseDelay, pol.MaxDelay = time.Millisecond, 20*time.Millisecond
	a := NewAccounts(reg, auth.NewGate(pol, auth.NewMemoryStore()), wipe.NewWiper(wipe.NewKeyVault(), nil, nil))
	for i := 0; i < 11; i++ {
		time.Sleep(25 * time.Millisecond)
		a.Unlock("dev", "0000")
	}
	time.Sleep(25 * time.Millisecond)
	if d := a.Unlock("dev", "3333"); d.Outcome != Reject {
		t.Fatalf("the wipe PIN was not taken: %+v", d)
	}
	if d := a.Unlock("dev", "9999"); d.Outcome != Throttled {
		t.Fatalf("twelve failures should lock the limiter: %+v", d)
	}
	if d := a.Unlock("dev", "1111"); !d.Wiped {
		t.Fatalf("the confirmation was held off by the limiter: %+v", d)
	}
}
