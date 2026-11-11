package profile

import (
	"sync"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/auth"
	"github.com/LocalGhostDao/localghost/server/internal/wipe"
)

// wipeArmWindow is how long a wipe stays armed after the wipe PIN is entered, waiting for the main
// PIN to confirm it. Long enough to deliberately type the second PIN, short enough that an
// accidental arming lapses on its own. The wipe is a TWO-PART action , wipe PIN then main PIN , so a
// single stray entry of either PIN destroys nothing.
const wipeArmWindow = 2 * time.Minute

// Outcome is what an unlock attempt resulted in. A wipe looks identical to a wrong PIN, so an
// onlooker cannot tell the difference between a failed unlock and a deliberate erase.
type Outcome int

const (
	Reject    Outcome = iota // invalid PIN (or wipe PIN), rate-limited; show "wrong PIN"
	Open                     // open the account (slot 0)
	Throttled                // refused by the rate limiter (locked out / too soon)
)

// Decision is returned by Unlock. For Open, mount OpenSlot's container. Wiped is true when this
// unlock CONFIRMED and executed the wipe (for internal audit only; it must not change what the user
// sees , a wipe is presented exactly like a wrong PIN).
type Decision struct {
	Outcome  Outcome
	OpenSlot int
	Wiped    bool
	Err      error // for Throttled: ErrLockedOut / ErrTooSoon
}

// Accounts ties the registry, rate limiter, and wiper together. Unlock is the single entry point the
// daemon calls for every PIN attempt from a device.
type Accounts struct {
	reg   *Registry
	gate  *auth.Gate
	wiper *wipe.Wiper

	// armed tracks a pending wipe per device: deviceID -> expiry. Entering the wipe PIN arms; the
	// main PIN within the window confirms and erases. Per device so the confirming PIN must come from
	// the same device that armed, and a stray entry elsewhere cannot complete the sequence.
	mu    sync.Mutex
	armed map[string]time.Time
	now   func() time.Time // injectable for tests
}

func NewAccounts(reg *Registry, gate *auth.Gate, wiper *wipe.Wiper) *Accounts {
	return &Accounts{
		reg:   reg,
		gate:  gate,
		wiper: wiper,
		armed: make(map[string]time.Time),
		now:   time.Now,
	}
}

// AuthorizesLock reports whether pin is a valid MAIN PIN, WITHOUT any side effects , it does not arm,
// disarm, wipe, record a failure, or touch the rate-limit gate. It exists for the `off` command: off
// is a lock, so it must never be able to wipe or disturb a pending wipe. A wrong PIN and the wipe PIN
// both return false (off does nothing, indistinguishably); only the main PIN authorizes the lock.
//
// Deliberately does NOT go through the gate: off cannot lose data, so gating it would only let a
// coercer rate-limit-lock you out of locking your own box. And it does not consume the armed state ,
// off leaves a pending wipe exactly as it found it, so `off` then a later main-PIN unlock still
// confirms the wipe if one was armed.
func (a *Accounts) AuthorizesLock(pin string) bool {
	res := a.reg.Resolve(pin)
	return res.Valid && res.Open != NoSlot && res.Wipe != WipeAll
}
//
// The wipe is deliberately a TWO-PART sequence so it cannot fire by accident or be triggered by
// someone who only knows one PIN:
//   - the WIPE PIN alone does NOT erase. It arms a pending wipe (for this device, for wipeArmWindow)
//     and returns exactly a wrong-PIN reject , a single stray entry destroys nothing and shows nothing.
//   - the MAIN PIN, when a wipe is armed and still live on this device, CONFIRMS the wipe: it
//     crypto-erases everything and returns the same wrong-PIN reject, rather than opening.
//   - the MAIN PIN with nothing armed opens slot 0 as normal.
//   - any WRONG PIN cancels a pending wipe (fail safe) and is rejected.
//
// Every path returns Reject or Open , the armed state is never observable, so this adds no oracle to
// the appears-down model.
func (a *Accounts) Unlock(id, pin string) Decision {
	// The KDF runs on EVERY attempt, throttled or not, so the time an attempt takes says nothing
	// about the limiter's state (30 Sep 2026: a throttled attempt answered at once and an
	// evaluated one after the KDF, and the limiter's state was itself a tell, below).
	throttled := a.gate.CheckAllowed(id)
	res := a.reg.Resolve(pin)
	isMain := res.Valid && res.Open != NoSlot && res.Wipe != WipeAll

	// The one exception to the limiter: the main PIN confirming a live armed wipe. The wipe PIN
	// now counts as a failure, so a coercer's run of guesses before it must not be able to hold
	// the confirmation off until the arm lapses.
	if throttled != nil && !(isMain && a.armedLive(id)) {
		return Decision{Outcome: Throttled, Err: throttled}
	}

	if !res.Valid {
		a.gate.RecordFailure(id)
		a.disarm(id) // a wrong PIN cancels any pending wipe
		return Decision{Outcome: Reject}
	}

	if res.Wipe == WipeAll {
		// Wipe PIN: ARM, do not erase. It presents as a wrong PIN, so the limiter counts it as
		// one. It used to reset the failure count like a success, and that was an oracle: three
		// wrong guesses, a candidate, one more guess at once , throttled after a wrong candidate,
		// evaluated after the wipe PIN, which then shows in the time the guess takes.
		a.arm(id)
		a.gate.RecordFailure(id)
		return Decision{Outcome: Reject}
	}

	// Main PIN. If a wipe is armed and still live on this device, the main PIN confirms it.
	if a.consumeArmed(id) {
		_ = a.wiper.PanicWipe(allSlots())
		a.gate.RecordFailure(id) // presents as a wrong PIN, counts as one
		return Decision{Outcome: Reject, Wiped: true}
	}
	a.gate.RecordSuccess(id)
	return Decision{Outcome: Open, OpenSlot: res.Open}
}

// armedLive reports whether a live armed wipe exists for the device, without consuming it.
// Allowed reports what the limiter would say to id's next attempt, without making one (nil:
// may try now). For status lines and tests; Unlock is the only thing that counts an attempt.
func (a *Accounts) Allowed(id string) error { return a.gate.CheckAllowed(id) }

func (a *Accounts) armedLive(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.armed[id]
	return ok && a.now().Before(exp)
}

// arm sets (or refreshes) a pending wipe for a device.
func (a *Accounts) arm(id string) {
	a.mu.Lock()
	a.armed[id] = a.now().Add(wipeArmWindow)
	a.mu.Unlock()
}

// disarm clears any pending wipe for a device.
func (a *Accounts) disarm(id string) {
	a.mu.Lock()
	delete(a.armed, id)
	a.mu.Unlock()
}

// consumeArmed reports whether a live armed wipe exists for the device and, if so, clears it (the
// confirmation is single-use). An expired arm is treated as absent and cleaned up.
func (a *Accounts) consumeArmed(id string) bool {
	a.mu.Lock()
	defer a.mu.Unlock()
	exp, ok := a.armed[id]
	if !ok {
		return false
	}
	delete(a.armed, id)
	return a.now().Before(exp)
}

// allSlots is the set of data slots a wipe erases. The model is single-account, so this is just the
// one real slot; it stays a slice so the wiper interface (which takes a set) is unchanged.
func allSlots() []int {
	return []int{MainSlot}
}
