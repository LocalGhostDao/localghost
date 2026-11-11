package pgp

// What the second audit found in this package (10 Nov 2026), each as a test that fails on the
// code as it was.

import (
	"bytes"
	"compress/zlib"
	"errors"
	"strings"
	"testing"
	"time"
)

// literal is a literal data packet body: binary, no name, no date.
func literal(data []byte) []byte {
	return append([]byte{'b', 0, 0, 0, 0, 0}, data...)
}

func compressed(t *testing.T, inner []byte) []byte {
	t.Helper()
	var z bytes.Buffer
	zw := zlib.NewWriter(&z)
	if _, err := zw.Write(inner); err != nil {
		t.Fatal(err)
	}
	zw.Close()
	return append([]byte{compZLIB}, z.Bytes()...)
}

// A genuine signed message with other data attached, in any of the places a packet can stand,
// never comes back as that other data with the signer's name on it.
func TestSignatureDoesNotCoverAttachedData(t *testing.T) {
	a, _ := Generate("A <a@example>", t0)
	signed, err := Sign([]byte("pay the electrician\n"), "", t0, a)
	if err != nil {
		t.Fatal(err)
	}
	from := []*PublicKey{a.Public()}
	if m, err := Open(signed, nil, t0, from); err != nil || !m.Verified || string(m.Data) != "pay the electrician\n" {
		t.Fatalf("the genuine message: %+v %v", m, err)
	}
	var forged bytes.Buffer
	writePacket(&forged, tagLiteral, literal([]byte("pay the stranger\n")))
	other := forged.Bytes()
	var wrapped bytes.Buffer
	writePacket(&wrapped, tagCompressed, compressed(t, other))

	cases := map[string][]byte{
		"compressed packet after the message":  append(append([]byte{}, signed...), wrapped.Bytes()...),
		"compressed packet before the message": append(append([]byte{}, wrapped.Bytes()...), signed...),
		"second literal after the message":     append(append([]byte{}, signed...), other...),
		"second literal before the message":    append(append([]byte{}, other...), signed...),
	}
	for name, msg := range cases {
		m, err := Open(msg, nil, t0, from)
		if err == nil && m.Verified && string(m.Data) != "pay the electrician\n" {
			t.Fatalf("%s: verified as A's with data %q", name, m.Data)
		}
		if err == nil {
			t.Fatalf("%s: accepted (%+v)", name, m)
		}
		if !errors.Is(err, ErrUnsupported) {
			t.Fatalf("%s: %v", name, err)
		}
	}
	// compression inside compression is not a message the box reads
	var twice bytes.Buffer
	writePacket(&twice, tagCompressed, compressed(t, wrapped.Bytes()))
	if _, err := Open(twice.Bytes(), nil, t0, from); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("nested compression: %v", err)
	}
	// one compressed layer around a whole signed message is what GnuPG writes, and still reads
	var whole bytes.Buffer
	writePacket(&whole, tagCompressed, compressed(t, signed))
	if m, err := Open(whole.Bytes(), nil, t0, from); err != nil || !m.Verified {
		t.Fatalf("a compressed signed message: %+v %v", m, err)
	}
}

// A few hundred bytes do not become gigabytes, and a claimed length is not believed.
func TestMessageCannotInflateOrClaim(t *testing.T) {
	var lit bytes.Buffer
	writePacket(&lit, tagLiteral, literal(bytes.Repeat([]byte{0}, 48<<20)))
	var bomb bytes.Buffer
	writePacket(&bomb, tagCompressed, compressed(t, lit.Bytes()))
	if len(bomb.Bytes()) > 100<<10 {
		t.Fatalf("the test's own message is %d bytes", len(bomb.Bytes()))
	}
	if _, err := Open(bomb.Bytes(), nil, t0, nil); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("48 MB out of %d bytes: %v", len(bomb.Bytes()), err)
	}
	// a packet that says it is 64 MB long and is six bytes
	claim := []byte{0xC0 | tagLiteral, 0xFF, 0x03, 0xFF, 0xFF, 0xFF}
	if _, err := readPackets(claim); err == nil {
		t.Fatal("a packet longer than its stream was read")
	}
	// empty packets by the thousand
	if _, err := readPackets(bytes.Repeat([]byte{0xC0 | tagMarker, 0}, maxPackets+1)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("packet flood: %v", err)
	}
	// more signatures than the box will check: said, and the message still read
	a, _ := Generate("A <a@example>", t0)
	signed, _ := Sign([]byte("x"), "", t0, a)
	pk, _ := readPackets(signed)
	var many bytes.Buffer
	for _, p := range pk {
		if p.tag == tagSignature {
			bad := append([]byte{}, p.body...)
			bad[len(bad)-1] ^= 1
			for i := 0; i < maxDocSigs+5; i++ {
				writePacket(&many, tagSignature, bad)
			}
		}
		writePacket(&many, p.tag, p.body)
	}
	m, err := Open(many.Bytes(), nil, t0, []*PublicKey{a.Public()})
	if err != nil || m.Verified || !strings.Contains(m.SigReason, "more than") {
		t.Fatalf("a flood of signatures: %+v %v", m, err)
	}
}

// rebuild writes a key block from a's packets with extra signature packets placed by the test.
func keyParts(t *testing.T, k *Key) (primary, uid, selfSig, sub, bindSig packet) {
	t.Helper()
	pk, err := readPackets(k.public)
	if err != nil || len(pk) != 5 {
		t.Fatalf("own key: %d packets, %v", len(pk), err)
	}
	return pk[0], pk[1], pk[2], pk[3], pk[4]
}

func block(ps ...packet) []byte {
	var b bytes.Buffer
	for _, p := range ps {
		writePacket(&b, p.tag, p.body)
	}
	return b.Bytes()
}

func (k *Key) testSig(t *testing.T, typ byte, at time.Time, extra [][]byte, uid []byte, withSub bool) packet {
	t.Helper()
	subs := append([][]byte{sub2(2, putU32(uint32(at.Unix())))}, extra...)
	body, err := k.makeSignature(typ, subs, func(s *signature) ([]byte, error) {
		var sk *pubKey
		if withSub {
			sk = k.sub
		}
		d, _, err := s.digest(k.primary, sk, uid, nil)
		return d, err
	})
	if err != nil {
		t.Fatal(err)
	}
	return packet{tag: tagSignature, body: body}
}

// A revocation counts wherever it stands. GnuPG writes it before the signature it revokes.
func TestRevocationsCountInAnyOrder(t *testing.T) {
	k, _ := Generate("A <a@example>", t0)
	primary, uid, selfSig, sub, bindSig := keyParts(t, k)
	later := t0.Add(48 * time.Hour)
	now := t0.Add(96 * time.Hour)

	subRev := k.testSig(t, 0x28, later, nil, nil, true)
	for name, blk := range map[string][]byte{
		"revocation before the binding (GnuPG's order)": block(primary, uid, selfSig, sub, subRev, bindSig),
		"revocation after the binding":                  block(primary, uid, selfSig, sub, bindSig, subRev),
	} {
		pub, err := ParsePublicKey(blk, now)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if pub.CanEncrypt() {
			t.Fatalf("%s: the revoked subkey is still encrypted to", name)
		}
	}
	uidRev := k.testSig(t, 0x30, later, nil, uid.body, false)
	for name, blk := range map[string][]byte{
		"before": block(primary, uid, uidRev, selfSig, sub, bindSig),
		"after":  block(primary, uid, selfSig, uidRev, sub, bindSig),
	} {
		if _, err := ParsePublicKey(blk, now); err == nil || !strings.Contains(err.Error(), "no user ID") {
			t.Fatalf("user ID revocation %s its certification: %v", name, err)
		}
	}
	keyRev := k.testSig(t, 0x20, later, nil, nil, false)
	for name, blk := range map[string][]byte{
		"at the top":           block(primary, keyRev, uid, selfSig, sub, bindSig),
		"after the user ID":    block(primary, uid, selfSig, keyRev, sub, bindSig),
		"at the end, on a sub": block(primary, uid, selfSig, sub, bindSig, keyRev),
	} {
		if _, err := ParsePublicKey(blk, now); err == nil || !strings.Contains(err.Error(), "revoked") {
			t.Fatalf("key revocation %s: %v", name, err)
		}
	}
}

// The newest binding decides: a subkey given an expiry that has passed is not kept alive by an
// older binding without one.
func TestNewestBindingDecides(t *testing.T) {
	k, _ := Generate("A <a@example>", t0)
	primary, uid, selfSig, sub, bindSig := keyParts(t, k)
	expiring := k.testSig(t, 0x18, t0.Add(24*time.Hour), [][]byte{sub2(27, []byte{0x0C}), sub2(9, putU32(3*86400))}, nil, true)
	for name, blk := range map[string][]byte{
		"old first": block(primary, uid, selfSig, sub, bindSig, expiring),
		"new first": block(primary, uid, selfSig, sub, expiring, bindSig),
	} {
		pub, err := ParsePublicKey(blk, t0.Add(48*time.Hour))
		if err != nil || !pub.CanEncrypt() {
			t.Fatalf("%s, before the expiry: %v", name, err)
		}
		pub, err = ParsePublicKey(blk, t0.Add(10*24*time.Hour))
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if pub.CanEncrypt() {
			t.Fatalf("%s: the expired subkey is still encrypted to", name)
		}
		// and a key read while it was good is not encrypted to once it has run out
		good, _ := ParsePublicKey(blk, t0.Add(48*time.Hour))
		if _, err := Encrypt([]byte("x"), "", t0.Add(10*24*time.Hour), []*PublicKey{good}, nil); !errors.Is(err, ErrNoKey) {
			t.Fatalf("%s: encrypted to an expired subkey: %v", name, err)
		}
	}
}

// A signature that carries a critical subpacket the box does not know is not relied on, and a
// key block is bounded.
func TestCriticalSubpacketsAndKeyBounds(t *testing.T) {
	k, _ := Generate("A <a@example>", t0)
	primary, uid, _, sub, bindSig := keyParts(t, k)
	crit := k.testSig(t, 0x13, t0, [][]byte{sub2(27, []byte{0x03}), sub2(0x80|101, []byte{1})}, uid.body, false)
	if _, err := parseSignature(crit.body); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("a critical unknown subpacket: %v", err)
	}
	if _, err := ParsePublicKey(block(primary, uid, crit, sub, bindSig), t0); err == nil {
		t.Fatal("a user ID certified only by a signature the box may not rely on stands")
	}
	known := k.testSig(t, 0x13, t0, [][]byte{sub2(0x80|27, []byte{0x03})}, uid.body, false)
	if _, err := ParsePublicKey(block(primary, uid, known, sub, bindSig), t0); err != nil {
		t.Fatalf("a critical subpacket the box knows: %v", err)
	}
	// too many self-signatures on one user ID: refused whole, not read in part
	ps := []packet{primary, uid}
	for i := 0; i <= maxSigsPer; i++ {
		ps = append(ps, k.testSig(t, 0x13, t0.Add(time.Duration(i)*time.Second), [][]byte{sub2(27, []byte{0x03})}, uid.body, false))
	}
	if _, err := ParsePublicKey(block(append(ps, sub, bindSig)...), t0.Add(time.Hour)); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("a flood of self-signatures: %v", err)
	}
	if _, err := ParsePublicKey(bytes.Repeat([]byte{0xC0 | tagMarker, 0}, maxKeyBlock), t0); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("an oversized block: %v", err)
	}
	// an RSA modulus past 8192 bits is not a key the box spends time on
	var rb bytes.Buffer
	rb.WriteByte(4)
	rb.Write(putU32(uint32(t0.Unix())))
	rb.WriteByte(algoRSA)
	n := bytes.Repeat([]byte{0xFF}, 2048) // 16384 bits
	writeMPI(&rb, n)
	writeMPI(&rb, []byte{1, 0, 1})
	if _, err := parsePubKey(rb.Bytes()); !errors.Is(err, ErrUnsupported) {
		t.Fatalf("a 16384-bit RSA key: %v", err)
	}
}

// A signature is judged against the key as it stands at the time of use.
func TestSignerJudgedAtUse(t *testing.T) {
	k, _ := Generate("A <a@example>", t0)
	primary, uid, _, sub, bindSig := keyParts(t, k)
	expiring := k.testSig(t, 0x13, t0, [][]byte{sub2(27, []byte{0x03}), sub2(9, putU32(3*86400))}, uid.body, false)
	pub, err := ParsePublicKey(block(primary, uid, expiring, sub, bindSig), t0.Add(time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	signed, _ := Sign([]byte("x"), "", t0.Add(time.Hour), k)
	if m, err := Open(signed, nil, t0.Add(2*time.Hour), []*PublicKey{pub}); err != nil || !m.Verified {
		t.Fatalf("while the key is good: %+v %v", m, err)
	}
	m, err := Open(signed, nil, t0.Add(10*24*time.Hour), []*PublicKey{pub})
	if err != nil || m.Verified || !strings.Contains(m.SigReason, "expired") {
		t.Fatalf("after the key ran out: %+v %v", m, err)
	}
}
