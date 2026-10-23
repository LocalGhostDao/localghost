package profile

import (
	"bufio"
	"encoding/binary"
	"fmt"
	"math/big"
	"os"

	"crypto/rand"
)

// Registry persistence. The on-disk form contains ALL RegistrySize entries, real ones and random
// filler, in a fixed layout: magic, salt length, salt, then for every entry exactly hashLen bytes
// of PIN-derived identifier and actLen bytes of masked action. No count, no flag, nothing that
// says which entries are real (see registry.go); the file is the same size and the same shape of
// random-looking bytes whether the box has a wipe PIN or not.
//
// The file lives in the UNENCRYPTED state area (it must be readable before any account is unlocked,
// since it is what decides which account a PIN opens). It contains no PIN and no key: a reader
// still has to brute force Argon2id over the salt to learn anything, which is exactly the work the
// TPM lockout (on the real key) is there to punish.
//
// GREG1 was the first form: the PIN's key itself as the identifier, and open/wipe as two int32 in
// the clear, real entries first. It is read for conversion only (the stored key is enough to
// derive the new fields) and never written again.

const (
	registryMagic       = "GREG2"
	registryMagicLegacy = "GREG1"
)

// Save writes the registry to path atomically (write temp, rename), preserving all entries.
func (r *Registry) Save(path string) error {
	tmp := path + ".tmp"
	f, err := os.OpenFile(tmp, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		return err
	}
	w := bufio.NewWriter(f)

	if _, err := w.WriteString(registryMagic); err != nil {
		f.Close()
		return err
	}
	// salt length + salt
	if err := binary.Write(w, binary.BigEndian, uint16(len(r.salt))); err != nil {
		f.Close()
		return err
	}
	if _, err := w.Write(r.salt); err != nil {
		f.Close()
		return err
	}
	// every entry, fixed order, nothing else
	for i := range r.entries {
		e := r.entries[i]
		if len(e.id) != hashLen {
			f.Close()
			return fmt.Errorf("entry %d has bad id length %d", i, len(e.id))
		}
		if _, err := w.Write(e.id); err != nil {
			f.Close()
			return err
		}
		if _, err := w.Write(e.act[:]); err != nil {
			f.Close()
			return err
		}
	}
	if err := w.Flush(); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		return err
	}
	r.legacy = false
	return nil
}

// LoadRegistry reads a registry written by Save. A loaded registry only resolves: the real set is
// not in the file (by design), so add() refuses. A GREG1 blob is converted in memory; Legacy()
// then says so and the caller saves it again in the current form.
func LoadRegistry(path string) (*Registry, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	br := bufio.NewReader(f)

	magic := make([]byte, len(registryMagic))
	if _, err := readFull(br, magic); err != nil {
		return nil, err
	}
	legacy := false
	switch string(magic) {
	case registryMagic:
	case registryMagicLegacy:
		legacy = true
	default:
		return nil, fmt.Errorf("not a registry file")
	}
	var saltLen uint16
	if err := binary.Read(br, binary.BigEndian, &saltLen); err != nil {
		return nil, err
	}
	salt := make([]byte, saltLen)
	if _, err := readFull(br, salt); err != nil {
		return nil, err
	}

	r := &Registry{salt: salt, loaded: true}
	if legacy {
		if err := r.loadLegacy(br); err != nil {
			return nil, err
		}
		return r, nil
	}
	for i := range r.entries {
		id := make([]byte, hashLen)
		if _, err := readFull(br, id); err != nil {
			return nil, err
		}
		var act [actLen]byte
		if _, err := readFull(br, act[:]); err != nil {
			return nil, err
		}
		r.entries[i] = entry{id: id, act: act}
	}
	return r, nil
}

// loadLegacy converts a GREG1 body: each stored hash IS the PIN's key, so the identifier and the
// pad derive from it; filler (open and wipe both NoSlot) becomes fresh random bytes; the real
// entries, packed first in GREG1, are scattered to random indices.
func (r *Registry) loadLegacy(br *bufio.Reader) error {
	type legacyEntry struct {
		hash       []byte
		open, wipe int32
	}
	var real []legacyEntry
	for i := 0; i < RegistrySize; i++ {
		h := make([]byte, hashLen)
		if _, err := readFull(br, h); err != nil {
			return err
		}
		var open, wipe int32
		if err := binary.Read(br, binary.BigEndian, &open); err != nil {
			return err
		}
		if err := binary.Read(br, binary.BigEndian, &wipe); err != nil {
			return err
		}
		if int(open) != NoSlot || int(wipe) != NoSlot {
			real = append(real, legacyEntry{hash: h, open: open, wipe: wipe})
		}
	}
	for i := range r.entries {
		if err := r.entries[i].randomise(); err != nil {
			return err
		}
	}
	var taken [RegistrySize]bool
	for _, le := range real {
		idx := 0
		for {
			n, err := rand.Int(rand.Reader, big.NewInt(RegistrySize))
			if err != nil {
				return err
			}
			idx = int(n.Int64())
			if !taken[idx] {
				break
			}
		}
		taken[idx] = true
		r.entries[idx] = entry{id: deriveID(le.hash), act: encodeAct(int(le.open), int(le.wipe), derivePad(le.hash))}
	}
	r.legacy = true
	return nil
}

func readFull(r *bufio.Reader, b []byte) (int, error) {
	got := 0
	for got < len(b) {
		n, err := r.Read(b[got:])
		got += n
		if err != nil {
			return got, err
		}
	}
	return got, nil
}
