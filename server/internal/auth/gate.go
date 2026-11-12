package auth

import (
	"errors"
	"time"
)

// Errors a caller can branch on.
var (
	ErrLockedOut = errors.New("locked out: too many failed attempts")
	ErrTooSoon   = errors.New("too soon: wait before retrying")
	ErrBadPIN    = errors.New("incorrect PIN")
)

// Policy tunes the brute-force defence. Defaults via DefaultPolicy.
type Policy struct {
	// Escalating delay between *allowed* attempts: required wait grows once Failed exceeds
	// FreeAttempts, doubling each failure from BaseDelay up to MaxDelay.
	FreeAttempts int
	BaseDelay    time.Duration
	MaxDelay     time.Duration

	// Hard lockout: after MaxAttempts consecutive failures, refuse all attempts for LockoutFor.
	MaxAttempts int
	LockoutFor  time.Duration
}

func DefaultPolicy() Policy {
	return Policy{
		FreeAttempts: 3,                // first few feel instant for a fat-fingered owner
		BaseDelay:    1 * time.Second,  // then 1s, 2s, 4s, 8s ...
		MaxDelay:     5 * time.Minute,  // capped
		MaxAttempts:  12,               // then a hard wall
		LockoutFor:   1 * time.Hour,
	}
}

// Gate enforces the policy around a pass/fail check. With the fixed-slot profile model an unknown
// PIN is genuinely invalid, so Verify (below) is the primary path again, rate-limiting guesses.
// It is the box-side defence against a phone attacker with no box root. It is NOT a defence against box root, which can read the credential
// and bypass this entirely; see the package and tpm.go notes.
type Gate struct {
	policy Policy
	store  AttemptStore
	now    func() time.Time // injectable for tests
}

func NewGate(policy Policy, store AttemptStore) *Gate {
	return &Gate{policy: policy, store: store, now: time.Now}
}

// requiredWait is the cooldown that must elapse after the last attempt, given how many failures
// have accrued. Zero while within the free attempts.
func (g *Gate) requiredWait(failed int) time.Duration {
	over := failed - g.policy.FreeAttempts
	if over < 0 {
		return 0
	}
	d := g.policy.BaseDelay
	for i := 0; i < over; i++ {
		d *= 2
		if d >= g.policy.MaxDelay {
			return g.policy.MaxDelay
		}
	}
	return d
}

// Verify evaluates a PIN attempt for id against cred, applying the policy. The order matters: the
// cheap lockout and cooldown checks run BEFORE the expensive KDF, so an attacker cannot spam
// Argon2id as a DoS and cannot burn a guess faster than the cooldown allows.
//
// On ErrTooSoon the attempt did not count as a guess (the KDF never ran). On ErrBadPIN it did.
func (g *Gate) Verify(id, pin string, cred Credential) error {
	now := g.now()
	st := g.store.Get(id)

	// Hard lockout.
	if !st.LockedUntil.IsZero() && now.Before(st.LockedUntil) {
		return ErrLockedOut
	}

	// Cooldown since last attempt.
	if wait := g.requiredWait(st.Failed); wait > 0 {
		if next := st.LastAttempt.Add(wait); now.Before(next) {
			return ErrTooSoon
		}
	}

	// Only now spend CPU on the KDF.
	if cred.Verify(pin) {
		// Success: clear state.
		g.store.Put(id, AttemptState{})
		return nil
	}

	// Failure: record it, maybe trip the hard lockout.
	st.Failed++
	st.LastAttempt = now
	if st.Failed >= g.policy.MaxAttempts {
		st.LockedUntil = now.Add(g.policy.LockoutFor)
	}
	g.store.Put(id, st)
	return ErrBadPIN
}

// Throttle rate-limits an attempt WITHOUT a correctness notion, for the deniable profile model
// (profile/) where every PIN opens a profile and there is no wrong PIN. Call it on every unlock
// attempt: it returns ErrTooSoon / ErrLockedOut if the source id is hammering, otherwise nil, and
// records the attempt. Because it does not depend on success or failure, it leaks nothing about
// whether a PIN was "real", it only caps how fast a source may probe the pool (stopping
// enumeration and eviction-spraying).
//
// Use Throttle instead of Verify when profiles are deniable; Verify still suits any classic
// pass-or-fail secret (e.g. an admin action) where a wrong answer is meaningful.
func (g *Gate) Throttle(id string) error {
	now := g.now()
	st := g.store.Get(id)

	if !st.LockedUntil.IsZero() && now.Before(st.LockedUntil) {
		return ErrLockedOut
	}
	if wait := g.requiredWait(st.Failed); wait > 0 {
		if next := st.LastAttempt.Add(wait); now.Before(next) {
			return ErrTooSoon
		}
	}
	// Count every attempt the same way. There is no success that resets it here; instead the
	// caller resets via Allow() after a genuine, infrequent checkpoint if desired. Uniformity is
	// the point: a wipe-PIN attempt and a wrong-PIN attempt are indistinguishable.
	st.Failed++
	st.LastAttempt = now
	if st.Failed >= g.policy.MaxAttempts {
		st.LockedUntil = now.Add(g.policy.LockoutFor)
	}
	g.store.Put(id, st)
	return nil
}

// Reset clears the throttle state for an id (e.g. after a long quiet period). Optional.
func (g *Gate) Reset(id string) { g.store.Put(id, AttemptState{}) }

// CheckAllowed reports whether id may attempt now, applying lockout and cooldown WITHOUT deciding
// correctness. Use it when validity is determined elsewhere (the profile registry): call
// CheckAllowed, resolve the PIN, then RecordSuccess or RecordFailure. Returns ErrLockedOut or
// ErrTooSoon if the attempt must be refused.
func (g *Gate) CheckAllowed(id string) error {
	now := g.now()
	st := g.store.Get(id)
	if !st.LockedUntil.IsZero() && now.Before(st.LockedUntil) {
		return ErrLockedOut
	}
	if wait := g.requiredWait(st.Failed); wait > 0 {
		if next := st.LastAttempt.Add(wait); now.Before(next) {
			return ErrTooSoon
		}
	}
	return nil
}

// Failures is how many invalid attempts stand against id (0 after a success or a reset). It
// counts nothing itself: for status lines and tests, which otherwise have only CheckAllowed to
// ask, and its answer turns on how long ago the last attempt was.
func (g *Gate) Failures(id string) int { return g.store.Get(id).Failed }

// RecordSuccess clears the failure state for id after a valid PIN.
func (g *Gate) RecordSuccess(id string) { g.store.Put(id, AttemptState{}) }

// RecordFailure records an invalid attempt, escalating the cooldown and tripping the hard lockout
// at the threshold.
func (g *Gate) RecordFailure(id string) {
	now := g.now()
	st := g.store.Get(id)
	st.Failed++
	st.LastAttempt = now
	if st.Failed >= g.policy.MaxAttempts {
		st.LockedUntil = now.Add(g.policy.LockoutFor)
	}
	g.store.Put(id, st)
}

// RetryAfter tells a caller how long until id may next attempt. Useful for an honest error to the
// phone ("try again in 2m"). Zero means it may attempt now.
func (g *Gate) RetryAfter(id string) time.Duration {
	now := g.now()
	st := g.store.Get(id)
	if !st.LockedUntil.IsZero() && now.Before(st.LockedUntil) {
		return st.LockedUntil.Sub(now)
	}
	wait := g.requiredWait(st.Failed)
	if wait == 0 {
		return 0
	}
	if next := st.LastAttempt.Add(wait); now.Before(next) {
		return next.Sub(now)
	}
	return 0
}
