package secd

import (
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/profile"
)

// UnlockBackend is what the unlock flow needs from the box to turn a PIN into a mounted, running
// account. It is the single seam between the HTTP server and the hardware: the default build wires a
// simulation (so ghost.secd compiles and the app flow is testable with no TPM), and the `tpm` build
// wires the real TPM + dm-crypt + per-account Postgres/Redis path.
//
// The flow mirrors the unlock stages exactly:
//   Resolve  decide what the PIN means (main account, wipe, or reject)
//   Unseal   ask the TPM to release the account's master key for the resolved slot (PIN-bound; the
//            TPM enforces its own lockout, so a wrong PIN is punished in hardware)
//   Mount    dm-crypt map + filesystem mount of that slot's container with the unsealed key
//   StartDB  bring up the account's Postgres inside the mounted volume
//   StartCache bring up the account's Redis inside the mounted volume
//
// The wipe PIN triggers a global crypto-erase and then resolves to a reject; the backend performs
// the erase during Resolve so the timing and response are identical to a wrong PIN (an onlooker
// cannot tell a wipe from a failed unlock).
type UnlockBackend interface {
	// Resolve maps the PIN to an outcome. It returns the slot to open (or NoSlot on reject) and
	// whether the account was wiped (the wipe PIN). It must run in constant-ish time regardless of
	// outcome so timing does not leak which PIN was entered.
	Resolve(pin string) (openSlot int, mainWiped bool, err error)

	// Unseal releases the slot's master key from the TPM. The PIN is supplied again as the TPM auth
	// value; the hardware checks it and enforces the dictionary-attack lockout.
	Unseal(slot int, pin string) (key []byte, err error)

	// Mount maps and mounts the slot's encrypted container using key. Returns when the filesystem is
	// ready. key is zeroised by the caller after this returns.
	Mount(slot int, key []byte) error

	// StartDB and StartCache bring up the per-account Postgres and Redis inside the mounted volume.
	StartDB(slot int) error
	StartCache(slot int) error

	// Warm reports whether the slot is already mounted and running (a hot unlock), so the heavy
	// stages report Skipped instead of re-running.
	Warm(slot int) bool

	// Lock spins the slot back down: stop its services, stop Redis and Postgres, unmount the container
	// and luksClose the mapping so the volume key leaves the kernel. It emits a Progress per teardown
	// stage (profile.LockStages) so the app can show the spin-down the same way it shows the mount ,
	// making it visible that the next open is a genuine cold start. Idempotent: locking an already-cold
	// slot walks the same stages, each a no-op Complete. This is the deliberate "lock now" action,
	// distinct from the wipe (which destroys keys).
	Lock(slot int, emit func(profile.Progress)) error

	// AuthorizesLock reports whether pin is a valid main PIN, with NO side effects (no arm, no wipe, no
	// failure recorded). It is the auth for the `off` command, which locks the box from the local
	// control socket. off can never wipe, so it uses this instead of Resolve.
	AuthorizesLock(pin string) bool
}

// runUnlock drives the backend through the unlock stages, emitting progress. It is shared by both
// backend builds so the stage sequence and timing-uniformity logic live in one place. The PIN is
// resolved first (the security decision); a reject ends the stream with a failure. The key is
// zeroised immediately after Mount consumes it.
func runUnlock(b UnlockBackend, pin string, emit func(profile.Progress)) (openSlot int, err error) {
	openSlot = profile.NoSlot

	// RESOLVE
	emit(profile.Progress{Stage: profile.StageResolve, State: profile.Running})
	slot, _, rerr := b.Resolve(pin)
	if rerr != nil || slot == profile.NoSlot {
		emit(profile.Progress{Stage: profile.StageResolve, State: profile.Errored})
		if rerr != nil {
			return profile.NoSlot, rerr
		}
		return profile.NoSlot, errReject
	}
	emit(profile.Progress{Stage: profile.StageResolve, State: profile.Complete})

	warm := b.Warm(slot)
	heavy := func(stage profile.Stage, do func() error) error {
		if warm {
			emit(profile.Progress{Stage: stage, State: profile.Skipped})
			return nil
		}
		emit(profile.Progress{Stage: stage, State: profile.Running})
		// Log enter/exit + duration per stage. This makes the WHOLE unlock sequence visible in the
		// journal , the recurring failure mode here has been a stage hanging or erroring silently, only
		// surfacing (if at all) to the app. With this, one unlock attempt shows exactly which stage ran,
		// how long it took, and where it stopped.
		secdLog.Info("unlock stage begin", "fn", "unlock", "slot", slot, "stage", stage)
		t0 := time.Now()
		if err := do(); err != nil {
			emit(profile.Progress{Stage: stage, State: profile.Errored})
			secdLog.Error("unlock stage FAILED", "fn", "unlock", "slot", slot, "stage", stage, "after", time.Since(t0).String(), "err", err)
			return err
		}
		secdLog.Info("unlock stage ok", "fn", "unlock", "slot", slot, "stage", stage, "took", time.Since(t0).String())
		emit(profile.Progress{Stage: stage, State: profile.Complete})
		return nil
	}

	// UNSEAL. Hybrid verification: on the COLD path (first unlock since boot) we must unseal to get
	// the key for the mount, and that unseal is the TPM hardware-lockout-protected PIN check. On the
	// WARM path (already mounted, a later app-open) the volume key is already resident, so we do NOT
	// need the key and we do NOT hit the TPM again , Resolve above already verified the PIN against the
	// registry hash in constant time. Re-unsealing every app-open would add TPM wear and complicate the
	// DA lockout counter for no security gain (the key is already in the kernel). So skip it when warm.
	var key []byte
	if !warm {
		emit(profile.Progress{Stage: profile.StageUnseal, State: profile.Running})
		k, uerr := b.Unseal(slot, pin)
		if uerr != nil {
			emit(profile.Progress{Stage: profile.StageUnseal, State: profile.Errored})
			return profile.NoSlot, uerr
		}
		emit(profile.Progress{Stage: profile.StageUnseal, State: profile.Complete})
		key = k
	} else {
		emit(profile.Progress{Stage: profile.StageUnseal, State: profile.Skipped})
	}

	// MOUNT (consumes + zeroises the key). Skipped when warm (heavy() emits Skipped and the do() never
	// runs, so the nil key on the warm path is never used).
	if err := heavy(profile.StageMount, func() error { return b.Mount(slot, key) }); err != nil {
		zeroise(key)
		secdLog.Error("unlock failed at MOUNT", "fn", "unlock", "slot", slot, "err", err)
		return profile.NoSlot, err
	}
	zeroise(key)

	// START_DB, START_CACHE. Log the real error here , it is otherwise only returned to the app, so
	// the server-side journal shows NOTHING about why unlock failed (which made this exact failure
	// invisible to journalctl and impossible to diagnose on the box).
	if err := heavy(profile.StageStartDB, func() error { return b.StartDB(slot) }); err != nil {
		secdLog.Error("unlock failed at START_DB", "fn", "unlock", "slot", slot, "err", err)
		return profile.NoSlot, err
	}
	if err := heavy(profile.StageStartCache, func() error { return b.StartCache(slot) }); err != nil {
		secdLog.Error("unlock failed at START_CACHE", "fn", "unlock", "slot", slot, "err", err)
		return profile.NoSlot, err
	}

	// DAEMONS, READY
	emit(profile.Progress{Stage: profile.StageDaemons, State: profile.Complete})
	emit(profile.Progress{Stage: profile.StageReady, State: profile.Complete})
	return slot, nil
}

func zeroise(b []byte) {
	for i := range b {
		b[i] = 0
	}
}
