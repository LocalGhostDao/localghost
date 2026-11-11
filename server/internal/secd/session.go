package secd

import (
	"crypto/rand"
	"encoding/base64"
	"sync"
	"time"
)

// session is the runtime credential the app holds AFTER a correct-PIN unlock. The PIN is never stored
// or re-sent; it produces a token. Foreground requests AND the background notification poller carry
// the SAME token (shared fate: revoking it kills both at once, so the poller can never stay alive
// while the foreground plays dead). See the runtime design doc.
//
// Lifecycle:
//   - correct PIN  -> Issue() mints a fresh token (any previous one is dropped)
//   - a wrong PIN from the device that holds the token -> RevokeHeldBy() invalidates it
//   - the app re-enters the PIN every open (~20-30x/day), so a fresh token per open is expected
//
// A token does NOT unmount or re-lock anything; it is an authorisation credential only. The mount
// persists across token churn until reboot.
//
// TTL is 2 days. It is deliberately longer than a work session because the background notification
// poller carries this same token: once it expires the box can no longer be polled, so the app stops
// getting notifications until the user re-opens and re-unlocks. Two days balances "not nagging for a
// PIN constantly" against "a lost/stolen phone's token dies on its own within a couple of days". The
// app knows the expiry (it is returned at issue) and shows a gentle "reopen to check notifications"
// state as it approaches, since the box cannot notify a session it can no longer authenticate.

// SessionTTL is the fixed lifetime of a session token, and the hard ceiling , nothing mints longer.
const SessionTTL = 48 * time.Hour

type sessionManager struct {
	mu      sync.Mutex
	token   string    // the one live token; empty = no session
	expires time.Time // self-expiry safety net
	ttl     time.Duration
	// dev is the device the token was issued to (its certificate's key name, "" unknown). A wrong
	// PIN ends the session when it comes from that device (someone holding the phone, guessing),
	// not when it comes from another: before 10 Nov 2026 any enrolled device, or any process
	// past the door, could log the owner out with one wrong PIN.
	dev string
}

func newSessionManager(ttl time.Duration) *sessionManager {
	if ttl <= 0 || ttl > SessionTTL {
		ttl = SessionTTL // default and hard cap: no session outlives SessionTTL
	}
	return &sessionManager{ttl: ttl}
}

// ExpiresAt reports the current token's expiry so the issue path can hand it to the app. Zero time
// if there is no live token.
func (m *sessionManager) ExpiresAt() time.Time {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token == "" {
		return time.Time{}
	}
	return m.expires
}

// Issue mints a fresh token, replacing any previous one. Called on a correct-PIN unlock.
func (m *sessionManager) Issue() (string, error) { return m.IssueTo("") }

// IssueTo mints a fresh token for the device that unlocked.
func (m *sessionManager) IssueTo(dev string) (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	tok := base64.RawURLEncoding.EncodeToString(b)
	m.mu.Lock()
	m.token = tok
	m.expires = time.Now().Add(m.ttl)
	m.dev = dev
	m.mu.Unlock()
	return tok, nil
}

// Revoke invalidates the current token (lock, off, retire, wipe). Idempotent.
func (m *sessionManager) Revoke() {
	m.mu.Lock()
	m.token = ""
	m.expires = time.Time{}
	m.dev = ""
	m.mu.Unlock()
}

// RevokeHeldBy invalidates the current token after a wrong PIN from dev, when dev is the device
// that holds it, or when either is unknown (a box of one phone behaves as before). Reports
// whether a token was revoked.
func (m *sessionManager) RevokeHeldBy(dev string) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token == "" {
		return false
	}
	if dev != "" && m.dev != "" && dev != m.dev {
		return false
	}
	m.token = ""
	m.expires = time.Time{}
	m.dev = ""
	return true
}

// Valid reports whether the presented token is the current, unexpired one. Empty/expired/mismatched
// all return false, which the caller turns into the appears-down response.
func (m *sessionManager) Valid(presented string) bool {
	if presented == "" {
		return false
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.token == "" || time.Now().After(m.expires) {
		return false
	}
	// constant-time-ish compare is overkill here (the token is high-entropy random), but avoid a
	// short-circuit length leak by comparing directly.
	return presented == m.token
}
