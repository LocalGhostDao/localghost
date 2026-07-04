package hw

import (
	"crypto/rand"
	"crypto/sha256"
	"errors"
	"fmt"

	"github.com/google/go-tpm/tpm2"
	"github.com/google/go-tpm/tpm2/transport"
)

// TPMSealedKey implements auth.SealedKey against a real TPM 2.0 (e.g. Intel PTT / fTPM). It
// seals each account's master key under a PIN-bound policy so that:
//   - the key is non-extractable: it exists in the clear only briefly inside the TPM during unseal
//   - the TPM enforces its OWN dictionary-attack lockout on wrong PINs, which root cannot reset
//     without the lockout authorization (this is the defence the software Gate cannot provide)
//
// Each slot gets its own sealed object persisted at a distinct NV/parent handle, so the three
// accounts' keys are fully independent (no shared secret, coercing one reveals nothing about another).
//
// HONEST CAVEATS (kept next to the code, not buried): fTPM is firmware, weaker than a discrete TPM
// and has had glitching/side-channel breaks; it raises the bar, it is not absolute against a
// determined physical attacker. And the TPM stops brute force and key extraction but does NOT stop
// active root from scraping the PIN out of process memory during a legitimate unlock. We shrink that
// window (mlock, zeroise, no swap), we do not close it.
//
// This is NOT validated in CI here (no TPM in the build env). It is built against the go-tpm API and
// must be exercised on the box: go test ./internal/hw against /dev/tpmrm0.

type TPMSealedKey struct {
	device  string // /dev/tpmrm0
	slot    int
	// persistentHandle is where this slot's sealed blob lives. Distinct per slot.
	persistentHandle tpm2.TPMHandle
}

// NewTPMSealedKey binds a SealedKey to one slot. The persistent handle is derived from the slot so
// each account uses its own TPM object.
func NewTPMSealedKey(device string, slot int) *TPMSealedKey {
	return &TPMSealedKey{
		device:           device,
		slot:             slot,
		persistentHandle: tpm2.TPMHandle(0x81010000 + uint32(slot)),
	}
}

func (t *TPMSealedKey) open() (transport.TPMCloser, error) {
	return transport.OpenTPM(t.device)
}

// pinPolicy builds the auth policy digest that binds unseal to the PIN. We use the PIN as the
// object's authValue and a PolicyAuthValue session, so the TPM checks the PIN itself and applies its
// DA lockout on failure. The PIN is hashed into the authValue so its length is uniform.
func pinAuth(pin string) []byte {
	h := sha256.Sum256([]byte("localghost/pin/" + pin))
	return h[:]
}

// Reseal creates (or replaces) the sealed object for this slot, binding the given key under the PIN.
// Used at enrollment and PIN change. The key never leaves this process in the clear except to be
// sealed; caller should zeroise its copy after.
func (t *TPMSealedKey) Reseal(pin string, key []byte) error {
	if len(key) == 0 {
		return errors.New("refusing to seal an empty key")
	}
	tpm, err := t.open()
	if err != nil {
		return fmt.Errorf("open tpm: %w", err)
	}
	defer tpm.Close()

	// Create a primary storage key under the owner hierarchy as the parent.
	primary, err := tpm2.CreatePrimary{
		PrimaryHandle: tpm2.TPMRHOwner,
		InPublic:      tpm2.New2B(tpm2.ECCSRKTemplate),
	}.Execute(tpm)
	if err != nil {
		return fmt.Errorf("create primary: %w", err)
	}
	defer flush(tpm, primary.ObjectHandle)

	// Seal the key as a keyedhash object whose userWithAuth requires the PIN authValue.
	sealed, err := tpm2.Create{
		ParentHandle: tpm2.AuthHandle{
			Handle: primary.ObjectHandle,
			Name:   primary.Name,
			Auth:   tpm2.PasswordAuth(nil),
		},
		InSensitive: tpm2.TPM2BSensitiveCreate{
			Sensitive: &tpm2.TPMSSensitiveCreate{
				UserAuth: tpm2.TPM2BAuth{Buffer: pinAuth(pin)},
				Data:     tpm2.NewTPMUSensitiveCreate(&tpm2.TPM2BSensitiveData{Buffer: key}),
			},
		},
		InPublic: tpm2.New2B(tpm2.TPMTPublic{
			Type:    tpm2.TPMAlgKeyedHash,
			NameAlg: tpm2.TPMAlgSHA256,
			ObjectAttributes: tpm2.TPMAObject{
				FixedTPM:     true,
				FixedParent:  true,
				UserWithAuth: true, // the PIN authValue gates use
			},
		}),
	}.Execute(tpm)
	if err != nil {
		return fmt.Errorf("seal: %w", err)
	}

	// Load and persist it at this slot's handle so it survives reboots. Evict any prior object first.
	_ = evict(tpm, t.persistentHandle)
	loaded, err := tpm2.Load{
		ParentHandle: tpm2.AuthHandle{
			Handle: primary.ObjectHandle,
			Name:   primary.Name,
			Auth:   tpm2.PasswordAuth(nil),
		},
		InPrivate: sealed.OutPrivate,
		InPublic:  sealed.OutPublic,
	}.Execute(tpm)
	if err != nil {
		return fmt.Errorf("load sealed: %w", err)
	}
	defer flush(tpm, loaded.ObjectHandle)

	_, err = tpm2.EvictControl{
		Auth:             tpm2.TPMRHOwner,
		ObjectHandle:     &tpm2.NamedHandle{Handle: loaded.ObjectHandle, Name: loaded.Name},
		PersistentHandle: t.persistentHandle,
	}.Execute(tpm)
	if err != nil {
		return fmt.Errorf("persist: %w", err)
	}
	return nil
}

// Unseal returns the slot's master key if the PIN satisfies the object's auth. A wrong PIN is
// rejected by the TPM, which increments its DA counter and eventually locks out; we surface the
// error so the caller treats it like any other failed unlock (and never as a normal open).
func (t *TPMSealedKey) Unseal(pin string) ([]byte, error) {
	tpm, err := t.open()
	if err != nil {
		return nil, fmt.Errorf("open tpm: %w", err)
	}
	defer tpm.Close()

	// Read the persisted object's name so we can authorize against it.
	readPub, err := tpm2.ReadPublic{ObjectHandle: t.persistentHandle}.Execute(tpm)
	if err != nil {
		return nil, fmt.Errorf("no sealed key for slot %d: %w", t.slot, err)
	}

	unsealed, err := tpm2.Unseal{
		ItemHandle: tpm2.AuthHandle{
			Handle: t.persistentHandle,
			Name:   readPub.Name,
			Auth:   tpm2.PasswordAuth(pinAuth(pin)),
		},
	}.Execute(tpm)
	if err != nil {
		// Could be a wrong PIN (TPM DA lockout will bite after maxTries) or no object. Either way
		// this is a failed unlock; do not leak which.
		return nil, fmt.Errorf("unseal failed: %w", err)
	}
	out := make([]byte, len(unsealed.OutData.Buffer))
	copy(out, unsealed.OutData.Buffer)
	return out, nil
}

// Evict removes the persisted object (used by crypto-erase: destroying the sealed key makes the
// account's data unrecoverable, since the data key needs this key to unwrap).
func (t *TPMSealedKey) Evict() error {
	tpm, err := t.open()
	if err != nil {
		return err
	}
	defer tpm.Close()
	return evict(tpm, t.persistentHandle)
}

func evict(tpm transport.TPM, h tpm2.TPMHandle) error {
	// Read to get the name; if it does not exist, nothing to do.
	rp, err := tpm2.ReadPublic{ObjectHandle: h}.Execute(tpm)
	if err != nil {
		return nil
	}
	_, err = tpm2.EvictControl{
		Auth:             tpm2.TPMRHOwner,
		ObjectHandle:     &tpm2.NamedHandle{Handle: h, Name: rp.Name},
		PersistentHandle: h,
	}.Execute(tpm)
	return err
}

func flush(tpm transport.TPM, h tpm2.TPMHandle) {
	_, _ = tpm2.FlushContext{FlushHandle: h}.Execute(tpm)
}

// randomKey makes a fresh account master key (used at enrollment before the first Reseal).
func randomKey() ([]byte, error) {
	b := make([]byte, 32)
	_, err := rand.Read(b)
	return b, err
}

// --- Sealer interface conformance ---
// TPMSealedKey predates the Sealer interface (its methods are Reseal/Unseal/Evict). These thin
// adapters make it satisfy Sealer so the runtime tier selection (SelectSealer) can return it
// alongside SoftwareSealer without the caller knowing which tier it holds.

// Seal wraps the AMK in the TPM under the PIN (interface name for Reseal).
func (t *TPMSealedKey) Seal(pin string, amk []byte) error { return t.Reseal(pin, amk) }

// ReKey re-seals the SAME key under a new PIN. Unseal with old, reseal with new; the AMK is
// unchanged so the LUKS container is untouched , matches the software tier's ReKey semantics.
func (t *TPMSealedKey) ReKey(oldPin, newPin string) error {
	amk, err := t.Unseal(oldPin)
	if err != nil {
		return err
	}
	defer func() {
		for i := range amk {
			amk[i] = 0
		}
	}()
	return t.Reseal(newPin, amk)
}

// Destroy evicts the sealed object (interface name for Evict).
func (t *TPMSealedKey) Destroy() error { return t.Evict() }
