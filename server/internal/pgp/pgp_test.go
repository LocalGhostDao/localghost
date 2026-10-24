package pgp

import (
	"bytes"
	"strings"
	"testing"
	"time"
)

var t0 = time.Date(2026, 10, 24, 1, 0, 0, 0, time.UTC)

func TestOwnKeyRoundTrip(t *testing.T) {
	k, err := Generate("Vlad <vlad@ghost.example>", t0)
	if err != nil {
		t.Fatal(err)
	}
	k2, err := Unmarshal(k.Marshal())
	if err != nil {
		t.Fatal(err)
	}
	if k2.Fingerprint() != k.Fingerprint() || !bytes.Equal(k2.public, k.public) {
		t.Fatal("a key read back is not the same key")
	}
	pub, err := ParsePublicKey([]byte(k.Armor()), t0)
	if err != nil {
		t.Fatal(err)
	}
	if pub.Fingerprint != k.Fingerprint() || pub.UserIDs[0] != "Vlad <vlad@ghost.example>" || !pub.CanEncrypt() || len(pub.signers) != 1 {
		t.Fatalf("%+v", pub)
	}
	bt, _, err := Dearmor(k.Armor())
	if err != nil || bt != BlockPublicKey {
		t.Fatal(bt, err)
	}
}

func TestEncryptSignOpen(t *testing.T) {
	a, _ := Generate("A <a@example>", t0)
	b, _ := Generate("B <b@example>", t0)
	c, _ := Generate("C <c@example>", t0)
	plain := []byte("the relay cannot read this\n")
	msg, err := Encrypt(plain, "msg.txt", t0, []*PublicKey{b.Public(), c.Public()}, a)
	if err != nil {
		t.Fatal(err)
	}
	arm := Armor(BlockMessage, msg)
	for _, who := range []*Key{b, c} {
		m, err := Open([]byte(arm), who, t0, []*PublicKey{a.Public()})
		if err != nil {
			t.Fatal(err)
		}
		if !m.Encrypted || !m.Signed || !m.Verified || m.SignedBy != a.Fingerprint() || !bytes.Equal(m.Data, plain) || m.Filename != "msg.txt" {
			t.Fatalf("%+v", m)
		}
	}
	// not for A
	if _, err := Open(msg, a, t0, nil); err != ErrNotForUs {
		t.Fatalf("A opened it: %v", err)
	}
	// signer unknown: signed, not verified, with the reason
	m, err := Open(msg, b, t0, nil)
	if err != nil || m.Verified || !m.Signed || !strings.Contains(m.SigReason, "does not have") {
		t.Fatalf("%+v %v", m, err)
	}
	// wrong signer offered: does not match
	m, _ = Open(msg, b, t0, []*PublicKey{b.Public()})
	if m.Verified || m.SigReason == "" {
		t.Fatalf("%+v", m)
	}
	// a flipped byte in the ciphertext fails the integrity check
	bad := append([]byte(nil), msg...)
	bad[len(bad)-30] ^= 1
	if _, err := Open(bad, b, t0, nil); err != ErrIntegrity {
		t.Fatalf("tampered message opened: %v", err)
	}
	// unsigned
	msg, _ = Encrypt(plain, "", t0, []*PublicKey{b.Public()}, nil)
	m, err = Open(msg, b, t0, nil)
	if err != nil || m.Signed || !bytes.Equal(m.Data, plain) {
		t.Fatalf("%+v %v", m, err)
	}
	// signed only
	signed, _ := Sign(plain, "", t0, a)
	m, err = Open(signed, nil, t0, []*PublicKey{a.Public()})
	if err != nil || m.Encrypted || !m.Verified {
		t.Fatalf("%+v %v", m, err)
	}
	// detached
	sig, _ := DetachedSignature(plain, t0, a)
	m, err = VerifyDetached(plain, []byte(Armor(BlockSignature, sig)), t0, []*PublicKey{a.Public()})
	if err != nil || !m.Verified {
		t.Fatalf("%+v %v", m, err)
	}
	m, _ = VerifyDetached([]byte("other"), sig, t0, []*PublicKey{a.Public()})
	if m.Verified {
		t.Fatal("a detached signature verified over other data")
	}
}

func TestKeyWrapVectors(t *testing.T) {
	// RFC 3394 section 4.6: 256-bit KEK, 256-bit key
	kek := mustHex("000102030405060708090A0B0C0D0E0F101112131415161718191A1B1C1D1E1F")
	key := mustHex("00112233445566778899AABBCCDDEEFF000102030405060708090A0B0C0D0E0F")
	want := mustHex("28C9F404C4B810F4CBCCB35CFB87F8263F5786E2D80ED326CBC7F0E71A99F43BFB988B9B7A02DD21")
	got, err := keyWrap(kek, key)
	if err != nil || !bytes.Equal(got, want) {
		t.Fatalf("%x %v", got, err)
	}
	back, err := keyUnwrap(kek, got)
	if err != nil || !bytes.Equal(back, key) {
		t.Fatalf("%x %v", back, err)
	}
	got[9] ^= 1
	if _, err := keyUnwrap(kek, got); err == nil {
		t.Fatal("a corrupted wrap unwrapped")
	}
}

func TestArmorCRC(t *testing.T) {
	data := []byte("hello armor")
	a := Armor(BlockMessage, data)
	bt, back, err := Dearmor(a)
	if err != nil || bt != BlockMessage || !bytes.Equal(back, data) {
		t.Fatal(bt, err)
	}
	broken := strings.Replace(a, "=", "=A", 1)
	if _, _, err := Dearmor(broken); err == nil {
		t.Fatal("bad checksum accepted")
	}
	if crc24([]byte("")) != 0xB704CE {
		t.Fatal("crc24 init")
	}
}

func mustHex(s string) []byte {
	out := make([]byte, len(s)/2)
	for i := range out {
		var v byte
		for _, c := range s[i*2 : i*2+2] {
			v <<= 4
			switch {
			case c >= '0' && c <= '9':
				v |= byte(c - '0')
			case c >= 'A' && c <= 'F':
				v |= byte(c-'A') + 10
			case c >= 'a' && c <= 'f':
				v |= byte(c-'a') + 10
			}
		}
		out[i] = v
	}
	return out
}
