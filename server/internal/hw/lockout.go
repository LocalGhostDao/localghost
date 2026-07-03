//go:build tpm

package hw

// Dictionary-attack lockout is the REAL anti-brute-force wall on the first-unlock-per-boot unseal
// path , the one root cannot reset without the lockout authorization. The software limiter in
// ghost.secd no longer walls anything; it only absorbs repeated-identical PINs so they never reach
// the TPM as fresh attempts. So the numbers here ARE the brute-force budget for the security-
// critical path, and they are set once at provision.
//
// The lockout authorization is set to pinAuth(pin) , the SAME value already used as each sealed
// object's authValue (see tpm.go). That keeps it to one secret the owner must remember (the PIN),
// introduces no new stored credential, and means a change-PIN must re-key this auth too (old->new)
// while a resetup, having lost the PIN, must go through `tpm2 clear -c platform` to reset the
// lockout hierarchy before re-provisioning.
//
// GLOBAL, not per-app: TPM 2.0 has ONE dictionary-attack counter for the whole device. This is safe
// here only because LocalGhost is the sole TPM tenant on the box; ForeignPersistentHandles is the
// check that guards that assumption before we touch the global policy.
//
// NOT validated in CI here (no TPM in the build env). Built against the go-tpm command API; the
// exact capability-response field names must be confirmed against the pinned go-tpm on the box:
// go test -tags tpm ./internal/hw against /dev/tpmrm0.

import (
	"fmt"
	"strings"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
)

// DA policy. This is the anti-brute-force wall on the per-boot unseal path , the one root cannot
// reset without the lockout auth. The budget must survive HONEST operator error (a re-run of setup,
// a fat-fingered unlock) without bricking the box for a day, while still making guessing hopeless.
//
// 32 tries, then lockout; a failed entry ages out after recoveryTime. At 32 tries per 24h a 6-digit
// PIN is still ~1,500 years in expectation and a 4-digit is ~17 years , brute-forcing a TPM-sealed
// key is not the threat this defends against anyway (an attacker with the box unsealing at 32/day is
// already deep past the appears-down wall). The old value of 5 was tuned as if setup auth failures
// were attacker guesses; they are not, and 5 turned a second `--apply` into a 24h lockout. Setup no
// longer spends from this budget at all (see SetupLockout's pre-auth guard), so 32 is pure headroom
// for the unlock path.
//
// PTT CAVEAT, stated plainly: on Intel PTT firmware TPMs, recoveryTime/lockoutRecoveryTime are not
// reliably honoured , PTT tracks DA state internally, may not surface it via getcap, and often
// clears only on a cold power cycle or a firmware TPM-clear, NOT on a timed wait. So on PTT, treat a
// full lockout as "needs a reboot/clear", not "wait 24h". The numbers below are the discrete-TPM
// contract; PTT does its own thing.
const (
	daMaxTries           = 32
	daRecoverySec        = 2 * 60 * 60      // an accumulated failure ages out after 2h
	daLockoutRecoverySec = 24 * 60 * 60     // discrete-TPM full-lockout recovery; PTT ignores this
)

// Our persisted objects live at 0x81010000+slot (see NewTPMSealedKey); the parent is transient.
const (
	ourHandleLo uint32 = 0x81010000
	ourHandleHi uint32 = 0x8101000F
)

// TCG-reserved provisioning handles. The platform (systemd-cryptenroll, clevis, tpm2-tss FAPI, the
// kernel RM) places Storage Root Keys here automatically; no application put them there and every
// TPM user coexists with them. The TCG "TPM v2.0 Provisioning Guidance" reserves 0x81000001 for the
// RSA SRK and 0x81000002 for the ECC SRK. They are `noda` (exempt from dictionary-attack lockout by
// construction), so LocalGhost's global DA policy never governs them anyway. Treating them as foreign
// tenants made a stock Debian box fail the sole-tenant check; they are shared infrastructure.
//
// Deliberately NOT excluded: 0x81010001. The TCG guidance also lists it (RSA EK cert handle), but it
// collides with LocalGhost's OWN window , our sealed keys live at 0x81010000+slot, so slot 1 IS
// 0x81010001. Excluding it would make the check treat our own slot-1 key as platform infrastructure
// and skip it. Our window already covers that handle as ours; the EK-address coincidence must not
// override that, or a real collision would be silently masked.
var reservedProvisioningHandles = map[uint32]bool{
	0x81000001: true, // RSA SRK
	0x81000002: true, // ECC SRK
}

// ForeignPersistentHandles returns every persistent handle on the TPM that LocalGhost did not
// create. An empty slice means we are the sole tenant and it is safe to set the GLOBAL DA policy.
// A non-empty slice is not fatal by itself , the operator decides , but setting a tight global
// lockout (and any future `tpm2 clear` during resetup) would affect those objects too.
func ForeignPersistentHandles(device string) ([]uint32, error) {
	tpm, err := transport.OpenTPM(device)
	if err != nil {
		return nil, fmt.Errorf("open tpm: %w", err)
	}
	defer tpm.Close()

	rsp, err := tpm2.GetCapability{
		Capability:    tpm2.TPMCapHandles,
		Property:      uint32(tpm2.TPMHTPersistent) << 24, // start of the persistent range
		PropertyCount: 128,
	}.Execute(tpm)
	if err != nil {
		return nil, fmt.Errorf("get persistent handles: %w", err)
	}
	handles, err := rsp.CapabilityData.Data.Handles()
	if err != nil {
		return nil, fmt.Errorf("decode handle list: %w", err)
	}

	var foreign []uint32
	for _, h := range handles.Handle {
		v := uint32(h)
		if reservedProvisioningHandles[v] {
			continue // platform SRK/EK, shared infrastructure , not a competing tenant
		}
		if v < ourHandleLo || v > ourHandleHi {
			foreign = append(foreign, v)
		}
	}
	return foreign, nil
}

// SetupLockout binds the lockout hierarchy to pinAuth(pin) and applies the DA policy.
//
// Idempotency is enforced BEFORE any auth attempt, because the failure mode we are avoiding is
// exactly a second `--apply` spending from the DA budget and locking the box. The order is: (1) if
// the TPM is already in DA lockout, bail immediately with recovery guidance and touch nothing; (2)
// try to claim the auth from empty (fresh TPM); (3) if that fails NOT due to lockout, the auth is
// already set , confirm it is ours with a single param re-apply. At no point do we retry an auth
// that returned lockout, and a re-run against an already-provisioned box short-circuits at step 1.
func SetupLockout(device, pin string) error {
	want := pinAuth(pin)

	tpm, err := transport.OpenTPM(device)
	if err != nil {
		return fmt.Errorf("open tpm: %w", err)
	}
	defer tpm.Close()

	// Step 1: refuse early if already locked, spending no auth. This is the guard that makes a
	// re-run safe , without it, the empty->want attempt below would itself re-arm the lockout.
	if inLockout, lerr := tpmInLockout(tpm); lerr == nil && inLockout {
		return fmt.Errorf(
			"TPM is already in dictionary-attack lockout , NOT re-attempting auth (that would re-arm "+
				"the timer). On a discrete TPM this clears after the recovery window; on Intel PTT it "+
				"typically needs a cold power cycle or a firmware TPM-clear. Do not re-run --apply until "+
				"getcap shows it clear")
	}

	// Step 2: try to claim the lockout auth from empty (the fresh-TPM case).
	_, changeErr := tpm2.HierarchyChangeAuth{
		AuthHandle: tpm2.AuthHandle{
			Handle: tpm2.TPMRHLockout,
			Auth:   tpm2.PasswordAuth(nil), // empty on a fresh TPM
		},
		NewAuth: tpm2.TPM2BAuth{Buffer: want},
	}.Execute(tpm)

	if changeErr != nil {
		// Already set to something. Two very different worlds, and conflating them is what turned a
		// re-run into a 24h lockout: (a) the auth is OURS and this is an idempotent re-run, or (b)
		// the hierarchy is in DA LOCKOUT and refuses to evaluate ANY auth right now. Probing (a) with
		// applyDAParams when the truth is (b) counts as another failed authorization and RE-ARMS the
		// 24h recovery timer , the exact trap this box fell into. So check for lockout FIRST and bail
		// without touching auth, rather than blindly re-applying.
		if isDALockout(changeErr) {
			return fmt.Errorf(
				"TPM is in dictionary-attack lockout; the lockout hierarchy will not evaluate auth "+
					"for up to 24h and every attempt re-arms that timer. STOP re-running provisioning. "+
					"Wait the recovery window out untouched, then either `ghost-tpmreset` (if a prior "+
					"run set the auth to pinAuth(PIN)) or clear the TPM from firmware: %w", changeErr)
		}
		// Not in lockout, so the auth is simply set to a value. Confirm it is OURS by using `want` to
		// apply DA params , ONE attempt, and if it is a lockout response we surface that too rather
		// than looping.
		daErr := applyDAParams(tpm, want)
		if daErr == nil {
			return nil // idempotent: auth already ours, params re-applied
		}
		if isDALockout(daErr) {
			return fmt.Errorf("TPM entered dictionary-attack lockout while confirming the lockout "+
				"auth; STOP and wait out the 24h window before any further attempt: %w", daErr)
		}
		return fmt.Errorf(
			"TPM lockout auth is already set to a value that is not pinAuth(PIN); reset it with "+
				"`ghost-tpmreset` (if you know the PIN that set it) or clear the TPM from firmware "+
				"before provisioning: change=%v apply=%v",
			changeErr, daErr)
	}

	// Fresh set succeeded; now apply the DA parameters authorised by the new auth.
	if err := applyDAParams(tpm, want); err != nil {
		return fmt.Errorf("set DA parameters: %w", err)
	}
	return nil
}

// applyDAParams sets maxTries/recovery/lockoutRecovery, authorised by the lockout auth.
//
// Hand-rolled wire format, stated plainly: go-tpm's direct API (v0.9.8) has not implemented the
// TPM2_DictionaryAttackParameters command struct , the compiler, not this comment, is the proof ,
// and the alternative was importing the whole legacy package for one call. The command is a fixed
// layout (TPM 2.0 Part 3, section 25.3) with a password session, which is the only session kind
// this file ever uses, so the auth area below is the complete case, not a simplification:
//
//	tag=TPM_ST_SESSIONS | size | cc=0x13A | lockoutHandle |
//	authSize | [ TPM_RS_PW | nonce(empty) | attrs=continueSession | hmac=lockoutAuth ] |
//	newMaxTries | newRecoveryTime | lockoutRecovery
//
// The response for a parameterless-return command is just the 10-byte header; responseCode 0 is
// success. Anything else is returned raw in hex , mapping TPM_RC space to prose is the library's
// job, and the one caller only needs works/does-not.
func applyDAParams(tpm transport.TPMCloser, lockoutAuth []byte) error {
	const (
		tagSessions   = uint16(0x8002)
		ccDAParams    = uint32(0x0000013A)
		rhLockout     = uint32(0x4000000A)
		rsPW          = uint32(0x40000009)
		attrsContinue = byte(0x01)
	)
	auth := make([]byte, 0, 16+len(lockoutAuth))
	auth = be32(auth, rsPW)
	auth = be16(auth, 0) // empty nonce
	auth = append(auth, attrsContinue)
	auth = be16(auth, uint16(len(lockoutAuth)))
	auth = append(auth, lockoutAuth...)

	body := make([]byte, 0, 64)
	body = be32(body, ccDAParams)
	body = be32(body, rhLockout)
	body = be32(body, uint32(len(auth)))
	body = append(body, auth...)
	body = be32(body, daMaxTries)
	body = be32(body, daRecoverySec)
	body = be32(body, daLockoutRecoverySec)

	cmd := make([]byte, 0, 6+len(body))
	cmd = be16(cmd, tagSessions)
	cmd = be32(cmd, uint32(6+len(body)))
	cmd = append(cmd, body...)

	rsp, err := tpm.Send(cmd)
	if err != nil {
		return fmt.Errorf("DictionaryAttackParameters send: %w", err)
	}
	if len(rsp) < 10 {
		return fmt.Errorf("DictionaryAttackParameters: short response (%d bytes)", len(rsp))
	}
	if rc := uint32(rsp[6])<<24 | uint32(rsp[7])<<16 | uint32(rsp[8])<<8 | uint32(rsp[9]); rc != 0 {
		return fmt.Errorf("DictionaryAttackParameters: TPM_RC 0x%08X", rc)
	}
	return nil
}

func be16(b []byte, v uint16) []byte { return append(b, byte(v>>8), byte(v)) }
func be32(b []byte, v uint32) []byte {
	return append(b, byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// ChangeLockoutAuth re-keys the lockout hierarchy from pinAuth(old) to pinAuth(new). Call this from
// the change-PIN path in the SAME operation as the reseal, so the lockout auth never drifts away
// from the PIN. If it fails, the caller must roll the reseal back.
func ChangeLockoutAuth(device, oldPin, newPin string) error {
	tpm, err := transport.OpenTPM(device)
	if err != nil {
		return fmt.Errorf("open tpm: %w", err)
	}
	defer tpm.Close()

	_, err = tpm2.HierarchyChangeAuth{
		AuthHandle: tpm2.AuthHandle{
			Handle: tpm2.TPMRHLockout,
			Auth:   tpm2.PasswordAuth(pinAuth(oldPin)),
		},
		NewAuth: tpm2.TPM2BAuth{Buffer: pinAuth(newPin)},
	}.Execute(tpm)
	if err != nil {
		return fmt.Errorf("re-key lockout auth: %w", err)
	}
	return nil
}

// ResetLockoutAuth clears the lockout hierarchy's authorization back to empty, authorized by the
// CURRENT lockout auth = pinAuth(pin). This exists because a stripped-down tpm2_clear build cannot
// pass a lockout auth on the CLI (its -c takes only the bare hierarchy), and because SetupLockout,
// re-run with a different PIN than the one that first set the auth, drives the lockout hierarchy
// into DA lockout , at which point the ONLY software route back is to authorize with the known
// pinAuth. After this returns nil, `SetupLockout` starts from empty lockout auth again and a
// provisioning re-run completes.
//
// Preconditions the caller cannot skip: the TPM must be OUT of DA lockout (recovery is 24h and every
// failed attempt re-arms it, so wait the window out first), and `pin` must be the PIN whose pinAuth
// currently owns the lockout hierarchy. A wrong PIN returns an auth failure and ticks the DA counter;
// a still-throttled TPM returns TPM_RC_LOCKOUT. Both are surfaced verbatim so the caller can tell
// "wrong PIN" from "wait longer".
func ResetLockoutAuth(device, pin string) error {
	tpm, err := transport.OpenTPM(device)
	if err != nil {
		return fmt.Errorf("open tpm: %w", err)
	}
	defer tpm.Close()

	_, err = tpm2.HierarchyChangeAuth{
		AuthHandle: tpm2.AuthHandle{
			Handle: tpm2.TPMRHLockout,
			Auth:   tpm2.PasswordAuth(pinAuth(pin)),
		},
		NewAuth: tpm2.TPM2BAuth{Buffer: nil}, // back to empty
	}.Execute(tpm)
	if err != nil {
		return fmt.Errorf("reset lockout auth to empty (device out of DA lockout? correct PIN?): %w", err)
	}
	return nil
}

// isDALockout reports whether a TPM error is TPM_RC_LOCKOUT (0x921): the DA-protected hierarchy is
// throttled and will not evaluate authorization until the recovery window (LOCKOUT_RECOVERY, 24h
// here) elapses. Detected on the raw code AND the string, because errors reach here both as go-tpm's
// typed rc from HierarchyChangeAuth and as applyDAParams's own fmt.Errorf("TPM_RC 0x%08X"). The
// distinction matters: a lockout response must NEVER be retried , each retry re-arms the timer.
func isDALockout(err error) bool {
	if err == nil {
		return false
	}
	// go-tpm surfaces the code inside the error string; matching on the string is version-proof,
	// where a typed cast against a specific rc type is not (the type name has moved across go-tpm
	// releases). The three spellings below cover the typed error's Error() output and applyDAParams's
	// own fmt.Errorf, so no cast is needed.
	s := err.Error()
	return strings.Contains(s, "0x00000921") ||
		strings.Contains(s, "TPM_RC_LOCKOUT") ||
		strings.Contains(s, "0x921")
}

// tpmInLockout reports whether the TPM's DA logic is currently in lockout, read from the permanent
// properties (TPM_PT_PERMANENT, inLockout bit 0x00000004). This is a BEST-EFFORT guard, not a
// guarantee, and the honesty is the point:
//
//   - On a discrete TPM it is reliable and lets SetupLockout refuse a re-run before spending auth.
//   - On Intel PTT (this box's fTPM) it is NOT reliable: PTT has been observed reporting inLockout=0
//     via getcap while still returning TPM_RC_LOCKOUT to actual auth attempts. So a false here does
//     NOT prove the TPM will accept auth. It is the reason SetupLockout also relies on isDALockout
//     to bail on the FIRST lockout response, rather than trusting this pre-check alone.
//
// A read error returns (false, err) and the caller proceeds to the auth attempt, where isDALockout
// is the backstop , we never let a getcap failure block provisioning outright.
func tpmInLockout(tpm transport.TPMCloser) (bool, error) {
	const (
		ptPermanent    = 0x00000200 | 0x0000000A // TPM_PT_PERMANENT (PT_FIXED group base + offset)
		inLockoutBit   = 0x00000004
	)
	rsp, err := tpm2.GetCapability{
		Capability:    tpm2.TPMCapTPMProperties,
		Property:      ptPermanent,
		PropertyCount: 1,
	}.Execute(tpm)
	if err != nil {
		return false, fmt.Errorf("read permanent properties: %w", err)
	}
	props, err := rsp.CapabilityData.Data.TPMProperties()
	if err != nil {
		return false, fmt.Errorf("decode properties: %w", err)
	}
	for _, p := range props.TPMProperty {
		if uint32(p.Property) == ptPermanent {
			return p.Value&inLockoutBit != 0, nil
		}
	}
	return false, nil
}
