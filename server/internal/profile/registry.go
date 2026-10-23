package profile

import (
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/binary"
	"errors"
	"math/big"
)

// The box holds ONE real account (slot 0). RegistrySize is the FIXED number of entries the registry
// always holds on disk, the real ones (main PIN, and optionally a wipe PIN) padded with random
// filler. An attacker reading the registry sees RegistrySize identical-looking entries and cannot
// tell how many correspond to a real PIN, which one opens the account, or which one wipes it, or
// even that a wipe PIN exists at all. That indistinguishability is the whole point of the padding:
// the wipe PIN must not be identifiable as the wipe PIN from the stored form.
//
// How the stored form keeps that promise: an entry is not the PIN's hash and its action in the
// clear. It is an IDENTIFIER derived from the PIN's key (so a reader cannot tell it from random)
// and the action MASKED under a second key derived from the same PIN (so the action is readable
// only by whoever holds the PIN that selects it). Filler is random in both fields. Real entries
// sit at random indices. Nothing in the file says which entries are real or how many there are;
// the loader does not need to know (it only ever resolves). The first form of this file (GREG1)
// kept the action fields in the clear beside the hashes and packed real entries first, which the
// security audit of October 2026 showed gave the wipe hash away; GREG1 blobs are converted on
// load and rewritten.
const (
	RegistrySize = 10 // real entries (main [+ wipe]) padded with random filler
	hashLen      = 32
	actLen       = 8
)

// entry is one slot of the registry: a PIN-derived identifier and the masked action.
//
// NoSlot marks "no slot" for the open or wipe field. WipeAll marks the global crypto-erase.
const (
	NoSlot  = -1
	WipeAll = -2
)

type entry struct {
	id  []byte       // HKDF(PinKey, "ghost/registry/id"), hashLen bytes; random for filler
	act [actLen]byte // (int32 open || int32 wipe) XOR HKDF(PinKey, "ghost/registry/act"); random for filler
}

// Resolution is what Resolve returns. Valid is false for an unknown PIN (the caller rejects and
// rate-limits). For a valid PIN, Open is the slot to open (NoSlot for the wipe PIN) and Wipe is
// WipeAll when this PIN triggers the global crypto-erase.
type Resolution struct {
	Valid bool
	Open  int
	Wipe  int
}

// Registry resolves a PIN in constant time against a fixed number of entries.
type Registry struct {
	salt    []byte
	entries [RegistrySize]entry
	real    [RegistrySize]bool // in memory only, never persisted: which entries add() filled
	used    int                // how many real entries are filled; not persisted
	loaded  bool               // read from disk: the real set is unknown and add() refuses
	legacy  bool               // read from a GREG1 blob and converted: worth saving again
}

func NewRegistry(boxSalt []byte) (*Registry, error) {
	r := &Registry{salt: boxSalt}
	// Pre-fill every slot with random bytes so the on-disk form is full from the start and the
	// real count is hidden even before any PIN is added.
	for i := range r.entries {
		if err := r.entries[i].randomise(); err != nil {
			return nil, err
		}
	}
	return r, nil
}

func (e *entry) randomise() error {
	e.id = make([]byte, hashLen)
	if _, err := rand.Read(e.id); err != nil {
		return err
	}
	_, err := rand.Read(e.act[:])
	return err
}

// deriveID and derivePad are the two keys a PIN's key splits into, with domain separation.
func deriveID(k []byte) []byte {
	out, err := hkdf.Key(sha256.New, k, nil, "ghost/registry/id", hashLen)
	if err != nil {
		panic(err) // the lengths are constants
	}
	return out
}

func derivePad(k []byte) [actLen]byte {
	var p [actLen]byte
	out, err := hkdf.Key(sha256.New, k, nil, "ghost/registry/act", actLen)
	if err != nil {
		panic(err)
	}
	copy(p[:], out)
	return p
}

// encodeAct masks (open, wipe) under pad.
func encodeAct(open, wipe int, pad [actLen]byte) [actLen]byte {
	var a [actLen]byte
	binary.BigEndian.PutUint32(a[0:4], uint32(int32(open)))
	binary.BigEndian.PutUint32(a[4:8], uint32(int32(wipe)))
	for i := range a {
		a[i] ^= pad[i]
	}
	return a
}

// Legacy reports whether the registry came from a GREG1 blob and should be saved in the current form.
func (r *Registry) Legacy() bool { return r.legacy }

var (
	ErrFull      = errors.New("registry is full")
	ErrPinReused = errors.New("PIN already registered")
	ErrReadOnly  = errors.New("a registry loaded from disk only resolves; setup makes a new one")
)

// AddProfile registers the main account PIN for a slot (slot 0 in the single-account model).
func (r *Registry) AddProfile(pin string, slot int) error {
	return r.add(pin, slot, NoSlot)
}

// SetWipePin registers the global wipe PIN: entering it crypto-erases everything. It opens no
// profile (Resolve returns Open == NoSlot), so the caller shows the SAME response as a wrong PIN
// while the erase runs, the act of wiping is indistinguishable from a failed unlock.
func (r *Registry) SetWipePin(pin string) error {
	return r.add(pin, NoSlot, WipeAll)
}

func (r *Registry) add(pin string, open, wipe int) error {
	if r.loaded {
		return ErrReadOnly
	}
	if r.used >= RegistrySize {
		return ErrFull
	}
	k := PinKey(pin, r.salt)
	id := deriveID(k)
	for i := range r.entries {
		if r.real[i] && subtle.ConstantTimeCompare(r.entries[i].id, id) == 1 {
			return ErrPinReused
		}
	}
	// a random FREE index, so the real entries are not packed at the front
	idx := 0
	for {
		n, err := rand.Int(rand.Reader, big.NewInt(RegistrySize))
		if err != nil {
			return err
		}
		idx = int(n.Int64())
		if !r.real[idx] {
			break
		}
	}
	r.entries[idx] = entry{id: id, act: encodeAct(open, wipe, derivePad(k))}
	r.real[idx] = true
	r.used++
	return nil
}

// Resolve checks a PIN against ALL RegistrySize entries in constant time (real and filler alike),
// with no short-circuit, so neither timing nor comparison count reveals how many real PINs exist,
// which one matched, or whether the matched PIN opens or wipes. The matched entry's action is
// unmasked with the candidate's own pad; a miss unmasks random bytes and is discarded.
func (r *Registry) Resolve(pin string) Resolution {
	k := PinKey(pin, r.salt)
	cand := deriveID(k)
	pad := derivePad(k)
	valid := 0
	var sel [actLen]byte
	for i := range r.entries {
		m := subtle.ConstantTimeCompare(r.entries[i].id, cand)
		valid = subtle.ConstantTimeSelect(m, 1, valid)
		subtle.ConstantTimeCopy(m, sel[:], r.entries[i].act[:])
	}
	for i := range sel {
		sel[i] ^= pad[i]
	}
	open := int(int32(binary.BigEndian.Uint32(sel[0:4])))
	wipe := int(int32(binary.BigEndian.Uint32(sel[4:8])))
	open = subtle.ConstantTimeSelect(valid, open, NoSlot)
	wipe = subtle.ConstantTimeSelect(valid, wipe, NoSlot)
	return Resolution{Valid: valid == 1, Open: open, Wipe: wipe}
}
