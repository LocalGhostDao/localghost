package profile

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"
)

func TestRegistrySaveLoadRoundTrip(t *testing.T) {
	salt := []byte("box-salt-1234567")
	s, err := NewSetup(salt)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.SetMain("1111"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWipe("3333"); err != nil { // wipe PIN, crypto-erases everything
		t.Fatal(err)
	}
	reg, err := s.Finalize()
	if err != nil {
		t.Fatal(err)
	}

	path := filepath.Join(t.TempDir(), "registry.blob")
	if err := reg.Save(path); err != nil {
		t.Fatalf("save: %v", err)
	}
	loaded, err := LoadRegistry(path)
	if err != nil {
		t.Fatalf("load: %v", err)
	}

	// Every PIN must resolve identically after a round-trip.
	for _, tc := range []struct {
		pin       string
		wantValid bool
		wantOpen  int
	}{
		{"1111", true, MainSlot},
		{"3333", true, NoSlot}, // wipe PIN opens nothing
		{"9999", false, NoSlot},
	} {
		r := loaded.Resolve(tc.pin)
		if r.Valid != tc.wantValid || (tc.wantValid && r.Open != tc.wantOpen) {
			t.Fatalf("pin %s: got valid=%v open=%d, want valid=%v open=%d",
				tc.pin, r.Valid, r.Open, tc.wantValid, tc.wantOpen)
		}
	}
	// The wipe PIN must still carry its WipeAll behaviour after a round-trip.
	if r := loaded.Resolve("3333"); r.Wipe != WipeAll {
		t.Fatalf("wipe PIN lost its WipeAll action after round-trip: %d", r.Wipe)
	}
}

func TestRegistryFileIsFixedSizeRegardlessOfRealCount(t *testing.T) {
	salt := []byte("box-salt-1234567")
	// size(withWipe) builds a registry with a main PIN, optionally a wipe PIN, saves it, returns
	// the file size. The on-disk size must be identical whether or not a wipe PIN exists, so the
	// stored form never reveals that a wipe PIN is present.
	size := func(withWipe bool) int64 {
		s, _ := NewSetup(salt)
		_ = s.SetMain("0000")
		if withWipe {
			_ = s.SetWipe("9999")
		}
		reg, _ := s.Finalize()
		p := filepath.Join(t.TempDir(), "r.blob")
		_ = reg.Save(p)
		fi, _ := os.Stat(p)
		return fi.Size()
	}
	if size(false) != size(true) {
		t.Fatal("registry file size leaks whether a wipe PIN exists")
	}
}

// writeLegacy writes a GREG1 blob the way the first form did: the PIN's key as the identifier,
// open/wipe as int32 in the clear, real entries first, random filler after.
func writeLegacy(t *testing.T, path string, salt []byte, pins []struct {
	pin        string
	open, wipe int
}) {
	var b []byte
	b = append(b, registryMagicLegacy...)
	b = binary.BigEndian.AppendUint16(b, uint16(len(salt)))
	b = append(b, salt...)
	for i := 0; i < RegistrySize; i++ {
		if i < len(pins) {
			b = append(b, PinKey(pins[i].pin, salt)...)
			o, w := int32(pins[i].open), int32(pins[i].wipe)
			b = binary.BigEndian.AppendUint32(b, uint32(o))
			b = binary.BigEndian.AppendUint32(b, uint32(w))
			continue
		}
		h := make([]byte, hashLen)
		rand.Read(h)
		b = append(b, h...)
		none := int32(NoSlot)
		b = binary.BigEndian.AppendUint32(b, uint32(none))
		b = binary.BigEndian.AppendUint32(b, uint32(none))
	}
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
}

// The audit's finding: GREG1 stored each entry's action in the clear, so the wipe hash was
// visible. GREG2 may not: every entry's trailing bytes are mutually indistinguishable, a blob with a
// wipe PIN cannot be classified against one without, the real entries are not packed first, and
// the loader needs no count.
func TestRegistryBlobRevealsNoRoles(t *testing.T) {
	salt := []byte("box-salt-1234567")
	build := func(withWipe bool) []byte {
		s, _ := NewSetup(salt)
		_ = s.SetMain("1111")
		if withWipe {
			_ = s.SetWipe("9999")
		}
		reg, _ := s.Finalize()
		p := filepath.Join(t.TempDir(), "r.blob")
		if err := reg.Save(p); err != nil {
			t.Fatal(err)
		}
		b, _ := os.ReadFile(p)
		return b
	}
	for _, withWipe := range []bool{false, true} {
		b := build(withWipe)
		if string(b[:5]) != registryMagic {
			t.Fatalf("magic %q", b[:5])
		}
		body := b[5+2+len(salt):]
		if len(body) != RegistrySize*(hashLen+actLen) {
			t.Fatalf("body %d bytes", len(body))
		}
		seen := map[string]bool{}
		for i := 0; i < RegistrySize; i++ {
			tail := body[i*(hashLen+actLen)+hashLen : (i+1)*(hashLen+actLen)]
			for _, known := range []string{"00000000ffffffff", "fffffffffffffffe", "ffffffffffffffff", "0000000000000000"} {
				if hex.EncodeToString(tail) == known {
					t.Fatalf("entry %d action in the clear: %x", i, tail)
				}
			}
			if seen[string(tail)] {
				t.Fatalf("two entries share action bytes %x", tail)
			}
			seen[string(tail)] = true
			// the stored identifier is not the PIN's key: a reader with the salt cannot match a
			// guessed PIN against it without the derivation (and never learns the role)
			id := body[i*(hashLen+actLen) : i*(hashLen+actLen)+hashLen]
			if bytes.Equal(id, PinKey("1111", salt)) || bytes.Equal(id, PinKey("9999", salt)) {
				t.Fatalf("entry %d stores the PIN's key itself", i)
			}
		}
	}
	// the main entry is not always first: over a few builds the index moves
	first := 0
	for n := 0; n < 12; n++ {
		s, _ := NewSetup(salt)
		_ = s.SetMain("1111")
		reg, _ := s.Finalize()
		for i := range reg.entries {
			if reg.real[i] {
				if i == 0 {
					first++
				}
			}
		}
	}
	if first == 12 {
		t.Fatal("the real entry always sits first")
	}
	// resolution is unchanged, and a loaded registry refuses add
	s, _ := NewSetup(salt)
	_ = s.SetMain("1111")
	_ = s.SetWipe("9999")
	reg, _ := s.Finalize()
	p := filepath.Join(t.TempDir(), "r.blob")
	_ = reg.Save(p)
	loaded, err := LoadRegistry(p)
	if err != nil {
		t.Fatal(err)
	}
	if r := loaded.Resolve("1111"); !r.Valid || r.Open != MainSlot || r.Wipe != NoSlot {
		t.Fatalf("main: %+v", r)
	}
	if r := loaded.Resolve("9999"); !r.Valid || r.Open != NoSlot || r.Wipe != WipeAll {
		t.Fatalf("wipe: %+v", r)
	}
	if r := loaded.Resolve("0000"); r.Valid || r.Open != NoSlot || r.Wipe != NoSlot {
		t.Fatalf("unknown: %+v", r)
	}
	if loaded.Legacy() || loaded.AddProfile("2222", 0) != ErrReadOnly {
		t.Fatal("a loaded registry is not read-only")
	}
}

// A GREG1 blob written by an earlier box is read, resolves the same PINs, says it is legacy, and
// saves as GREG2 with the roles gone from the bytes.
func TestRegistryLegacyBlobConverts(t *testing.T) {
	salt := []byte("box-salt-1234567")
	p := filepath.Join(t.TempDir(), "registry.blob")
	writeLegacy(t, p, salt, []struct {
		pin        string
		open, wipe int
	}{{"1111", MainSlot, NoSlot}, {"9999", NoSlot, WipeAll}})
	reg, err := LoadRegistry(p)
	if err != nil {
		t.Fatal(err)
	}
	if !reg.Legacy() {
		t.Fatal("not marked legacy")
	}
	if r := reg.Resolve("1111"); !r.Valid || r.Open != MainSlot {
		t.Fatalf("main after conversion: %+v", r)
	}
	if r := reg.Resolve("9999"); !r.Valid || r.Wipe != WipeAll || r.Open != NoSlot {
		t.Fatalf("wipe after conversion: %+v", r)
	}
	if r := reg.Resolve("1234"); r.Valid {
		t.Fatal("a wrong PIN resolves")
	}
	if err := reg.Save(p); err != nil {
		t.Fatal(err)
	}
	if reg.Legacy() {
		t.Fatal("still legacy after the save")
	}
	b, _ := os.ReadFile(p)
	if string(b[:5]) != registryMagic || bytes.Contains(b, PinKey("9999", salt)) || bytes.Contains(b, []byte{0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xfe}) {
		t.Fatal("the converted blob still carries the old form")
	}
	again, err := LoadRegistry(p)
	if err != nil || again.Legacy() {
		t.Fatal(err)
	}
	if r := again.Resolve("9999"); !r.Valid || r.Wipe != WipeAll {
		t.Fatalf("wipe after the round trip: %+v", r)
	}
}
