package pgp

import (
	"bytes"
	"compress/bzip2"
	"compress/flate"
	"compress/zlib"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha1"
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"io"
	"time"
)

// Message is what Open returns: the plaintext and what is known about where it came from.
type Message struct {
	Data      []byte
	Filename  string
	Encrypted bool
	Signed    bool
	SignedBy  [20]byte // the signer's primary fingerprint, when the signature verified
	SignerID  uint64   // the issuer key ID in the signature, verified or not
	Verified  bool
	SigReason string // why the signature did not verify, in plain words
}

// Encrypt makes an OpenPGP message of plain for the recipients, signed by signer when given:
// one session key, a public-key-encrypted copy of it per recipient's encryption key, and the
// literal data (with the one-pass signature around it) in an integrity-protected packet. The
// result is binary; Armor it for mail.
func Encrypt(plain []byte, filename string, now time.Time, to []*PublicKey, signer *Key) ([]byte, error) {
	if len(to) == 0 {
		return nil, ErrNoKey
	}
	sessionKey := make([]byte, 32)
	if _, err := rand.Read(sessionKey); err != nil {
		return nil, err
	}
	var out bytes.Buffer
	for _, r := range to {
		if len(r.encrypters) == 0 {
			return nil, fmt.Errorf("%w: %s has no encryption key", ErrNoKey, first(r.UserIDs))
		}
		body, err := pkesk(r.encrypters[0], sessionKey)
		if err != nil {
			return nil, err
		}
		writePacket(&out, tagPKESK, body)
	}
	inner, err := literalPackets(plain, filename, now, signer)
	if err != nil {
		return nil, err
	}
	writePacket(&out, tagSEIPD, seipd(sessionKey, inner))
	return out.Bytes(), nil
}

// Sign makes a signed-only message: the literal data with a one-pass signature around it.
func Sign(plain []byte, filename string, now time.Time, signer *Key) ([]byte, error) {
	return literalPackets(plain, filename, now, signer)
}

// DetachedSignature signs data and returns the signature packet alone (PGP/MIME's multipart/signed).
func DetachedSignature(data []byte, now time.Time, signer *Key) ([]byte, error) {
	body, err := signer.makeSignature(0x00, docSubs(signer, now), func(s *signature) ([]byte, error) {
		d, _, err := s.digest(nil, nil, nil, data)
		return d, err
	})
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	writePacket(&out, tagSignature, body)
	return out.Bytes(), nil
}

// VerifyDetached checks a detached signature over data against the keys it may be from.
func VerifyDetached(data, sig []byte, now time.Time, from []*PublicKey) (*Message, error) {
	raw, err := unwrap(sig)
	if err != nil {
		return nil, err
	}
	pk, err := readPackets(raw)
	if err != nil {
		return nil, err
	}
	m := &Message{Data: data}
	for _, p := range pk {
		if p.tag != tagSignature {
			continue
		}
		s, err := parseSignature(p.body)
		if err != nil {
			m.Signed, m.SigReason = true, err.Error()
			continue
		}
		m.check(s, data, now, from)
		return m, nil
	}
	return nil, fmt.Errorf("no signature in the block")
}

func docSubs(k *Key, now time.Time) [][]byte {
	return [][]byte{
		sub2(2, putU32(uint32(now.Unix()))),
		sub2(33, append([]byte{4}, k.primary.fingerprint[:]...)),
	}
}

func first(xs []string) string {
	if len(xs) == 0 {
		return "the key"
	}
	return xs[0]
}

// literalPackets wraps plain in a literal data packet, inside a one-pass signature when signed.
func literalPackets(plain []byte, filename string, now time.Time, signer *Key) ([]byte, error) {
	var out bytes.Buffer
	if signer != nil {
		var ops bytes.Buffer
		ops.Write([]byte{3, 0x00, hashSHA256, algoEdDSA})
		var kid [8]byte
		binary.BigEndian.PutUint64(kid[:], signer.primary.keyID)
		ops.Write(kid[:])
		ops.WriteByte(1) // nested: the last one-pass before the data
		writePacket(&out, tagOnePass, ops.Bytes())
	}
	var lit bytes.Buffer
	lit.WriteByte('b')
	if len(filename) > 255 {
		filename = filename[:255]
	}
	lit.WriteByte(byte(len(filename)))
	lit.WriteString(filename)
	lit.Write(putU32(uint32(now.Unix())))
	lit.Write(plain)
	writePacket(&out, tagLiteral, lit.Bytes())
	if signer != nil {
		body, err := signer.makeSignature(0x00, docSubs(signer, now), func(s *signature) ([]byte, error) {
			d, _, err := s.digest(nil, nil, nil, plain)
			return d, err
		})
		if err != nil {
			return nil, err
		}
		writePacket(&out, tagSignature, body)
	}
	return out.Bytes(), nil
}

// pkesk builds a public-key encrypted session key packet for one recipient key.
func pkesk(k *pubKey, sessionKey []byte) ([]byte, error) {
	var body bytes.Buffer
	body.WriteByte(3)
	var kid [8]byte
	binary.BigEndian.PutUint64(kid[:], k.keyID)
	body.Write(kid[:])
	// the session key with its algorithm and checksum, as both RSA and ECDH carry it
	m := make([]byte, 0, 35)
	m = append(m, symAES256)
	m = append(m, sessionKey...)
	m = append(m, checksum(sessionKey)...)
	switch {
	case k.rsa != nil:
		body.WriteByte(algoRSA)
		c, err := rsa.EncryptPKCS1v15(rand.Reader, k.rsa, m)
		if err != nil {
			return nil, err
		}
		writeMPI(&body, c)
	case k.ecdh != nil:
		body.WriteByte(algoECDH)
		eph, err := k.curve.ecdh.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		shared, err := eph.ECDH(k.ecdh)
		if err != nil {
			return nil, err
		}
		kek := ecdhKDF(k, shared)
		// PKCS#5 padding to a multiple of 8 (40 bytes for AES-256 material)
		pad := 8 - len(m)%8
		for i := 0; i < pad; i++ {
			m = append(m, byte(pad))
		}
		wrapped, err := keyWrap(kek, m)
		if err != nil {
			return nil, err
		}
		pub := eph.PublicKey().Bytes()
		if k.curve.x2551 {
			pub = append([]byte{0x40}, pub...)
		}
		writeMPI(&body, pub)
		body.WriteByte(byte(len(wrapped)))
		body.Write(wrapped)
	default:
		return nil, ErrNoKey
	}
	return body.Bytes(), nil
}

func checksum(key []byte) []byte {
	var sum uint16
	for _, b := range key {
		sum += uint16(b)
	}
	return []byte{byte(sum >> 8), byte(sum)}
}

// ecdhKDF derives the key-wrapping key from the shared secret (RFC 6637 section 7).
func ecdhKDF(k *pubKey, shared []byte) []byte {
	h := hashOf(k.kdfHash).New()
	h.Write([]byte{0, 0, 0, 1})
	h.Write(shared)
	h.Write([]byte{byte(len(k.curve.oid))})
	h.Write(k.curve.oid)
	h.Write([]byte{algoECDH, 3, 1, k.kdfHash, k.kdfSym})
	h.Write([]byte("Anonymous Sender    "))
	h.Write(k.fingerprint[:])
	return h.Sum(nil)[:symKeyLen(k.kdfSym)]
}

// keyWrap is AES key wrap (RFC 3394) with the default IV.
func keyWrap(kek, plain []byte) ([]byte, error) {
	if len(plain)%8 != 0 || len(plain) < 16 {
		return nil, fmt.Errorf("key wrap: bad length %d", len(plain))
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	n := len(plain) / 8
	a := []byte{0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6}
	r := make([][]byte, n)
	for i := range r {
		r[i] = append([]byte(nil), plain[i*8:i*8+8]...)
	}
	var b [16]byte
	for j := 0; j < 6; j++ {
		for i := 0; i < n; i++ {
			copy(b[:8], a)
			copy(b[8:], r[i])
			block.Encrypt(b[:], b[:])
			t := uint64(n*j + i + 1)
			copy(a, b[:8])
			for x := 7; x >= 0; x-- {
				a[x] ^= byte(t)
				t >>= 8
			}
			copy(r[i], b[8:])
		}
	}
	out := append([]byte(nil), a...)
	for _, x := range r {
		out = append(out, x...)
	}
	return out, nil
}

func keyUnwrap(kek, wrapped []byte) ([]byte, error) {
	if len(wrapped)%8 != 0 || len(wrapped) < 24 {
		return nil, fmt.Errorf("key unwrap: bad length %d", len(wrapped))
	}
	block, err := aes.NewCipher(kek)
	if err != nil {
		return nil, err
	}
	n := len(wrapped)/8 - 1
	a := append([]byte(nil), wrapped[:8]...)
	r := make([][]byte, n)
	for i := range r {
		r[i] = append([]byte(nil), wrapped[8+i*8:16+i*8]...)
	}
	var b [16]byte
	for j := 5; j >= 0; j-- {
		for i := n - 1; i >= 0; i-- {
			t := uint64(n*j + i + 1)
			for x := 7; x >= 0; x-- {
				a[x] ^= byte(t)
				t >>= 8
			}
			copy(b[:8], a)
			copy(b[8:], r[i])
			block.Decrypt(b[:], b[:])
			copy(a, b[:8])
			copy(r[i], b[8:])
		}
	}
	if subtle.ConstantTimeCompare(a, []byte{0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6, 0xA6}) != 1 {
		return nil, fmt.Errorf("key unwrap: integrity check failed")
	}
	var out []byte
	for _, x := range r {
		out = append(out, x...)
	}
	return out, nil
}

// cfb is OpenPGP's CFB (full-block, zero IV, no resync in the integrity-protected packet).
func cfb(block cipher.Block, data []byte, encrypt bool) []byte {
	bs := block.BlockSize()
	out := make([]byte, len(data))
	prev := make([]byte, bs)
	ks := make([]byte, bs)
	for off := 0; off < len(data); off += bs {
		block.Encrypt(ks, prev)
		end := off + bs
		if end > len(data) {
			end = len(data)
		}
		for i := off; i < end; i++ {
			out[i] = data[i] ^ ks[i-off]
		}
		if encrypt {
			copy(prev, out[off:end])
		} else {
			copy(prev, data[off:end])
		}
	}
	return out
}

// seipd builds the body of a version 1 symmetrically encrypted integrity protected data packet:
// a random prefix with its last two bytes repeated, the inner packets, and the modification
// detection code, all under AES-256 CFB.
func seipd(key, inner []byte) []byte {
	block, _ := aes.NewCipher(key)
	prefix := make([]byte, 18)
	rand.Read(prefix[:16])
	prefix[16], prefix[17] = prefix[14], prefix[15]
	var plain bytes.Buffer
	plain.Write(prefix)
	plain.Write(inner)
	plain.Write([]byte{0xD3, 0x14})
	sum := sha1.Sum(plain.Bytes())
	plain.Write(sum[:])
	return append([]byte{1}, cfb(block, plain.Bytes(), true)...)
}

// Open reads an OpenPGP message, armored or binary: decrypts it with the box's key when it is
// encrypted, decompresses what the sender compressed, and verifies the signature when there is
// one against the keys it may be from (by issuer). A message encrypted to someone else is
// ErrNotForUs; a failed integrity check is ErrIntegrity. A signature that does not verify is
// not an error: the Message says Signed and not Verified, with the reason, and the caller shows
// that.
func Open(msg []byte, k *Key, now time.Time, from []*PublicKey) (*Message, error) {
	raw, err := unwrap(msg)
	if err != nil {
		return nil, err
	}
	pk, err := readPackets(raw)
	if err != nil {
		return nil, err
	}
	m := &Message{}
	var sessionKey []byte
	sawPKESK := false
	for _, p := range pk {
		switch p.tag {
		case tagPKESK:
			sawPKESK = true
			if sessionKey != nil || k == nil {
				continue
			}
			sk, err := k.openPKESK(p.body)
			if err == nil {
				sessionKey = sk
			}
		case tagSKESK:
			// passphrase-encrypted copies: the box has no passphrase to try
		case tagSEIPD, tagSED:
			if sessionKey == nil {
				if sawPKESK {
					return nil, ErrNotForUs
				}
				return nil, fmt.Errorf("encrypted with a passphrase, not to a key")
			}
			if p.tag == tagSED {
				return nil, fmt.Errorf("%w: a message without integrity protection (the old packet)", ErrUnsupported)
			}
			inner, err := openSEIPD(sessionKey, p.body)
			if err != nil {
				return nil, err
			}
			m.Encrypted = true
			return m.readInner(inner, now, from, 0)
		case tagCompressed, tagLiteral, tagOnePass, tagSignature:
			// a signed or plain message, not encrypted
			return m.readInner(raw, now, from, 0)
		case tagMarker, tagPadding:
		default:
			return nil, fmt.Errorf("%w: packet tag %d", ErrUnsupported, p.tag)
		}
	}
	if sawPKESK {
		return nil, fmt.Errorf("encrypted session key without a message")
	}
	return nil, fmt.Errorf("no OpenPGP message")
}

// openPKESK recovers the session key from a packet addressed to the box's subkey.
func (k *Key) openPKESK(body []byte) ([]byte, error) {
	r := bytes.NewReader(body)
	ver, err := r.ReadByte()
	if err != nil || ver != 3 {
		return nil, fmt.Errorf("%w: PKESK version", ErrUnsupported)
	}
	var kid [8]byte
	if _, err := io.ReadFull(r, kid[:]); err != nil {
		return nil, err
	}
	id := binary.BigEndian.Uint64(kid[:])
	if id != 0 && id != k.sub.keyID {
		return nil, ErrNotForUs
	}
	algo, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	if algo != algoECDH {
		return nil, ErrNotForUs
	}
	point, err := readMPI(r)
	if err != nil {
		return nil, err
	}
	if len(point) != 33 || point[0] != 0x40 {
		return nil, fmt.Errorf("bad ephemeral point")
	}
	wl, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	wrapped := make([]byte, wl)
	if _, err := io.ReadFull(r, wrapped); err != nil {
		return nil, err
	}
	eph, err := ecdh.X25519().NewPublicKey(point[1:])
	if err != nil {
		return nil, err
	}
	shared, err := k.enc.ECDH(eph)
	if err != nil {
		return nil, err
	}
	m, err := keyUnwrap(ecdhKDF(k.sub, shared), wrapped)
	if err != nil {
		return nil, ErrNotForUs
	}
	pad := int(m[len(m)-1])
	if pad < 1 || pad > 8 || pad > len(m) {
		return nil, ErrNotForUs
	}
	m = m[:len(m)-pad]
	n := symKeyLen(m[0])
	if n == 0 || len(m) != 1+n+2 {
		return nil, fmt.Errorf("%w: session key cipher %d", ErrUnsupported, m[0])
	}
	if subtle.ConstantTimeCompare(checksum(m[1:1+n]), m[1+n:]) != 1 {
		return nil, ErrNotForUs
	}
	return m[1 : 1+n], nil
}

// openSEIPD decrypts the integrity-protected packet and checks its prefix and its MDC.
func openSEIPD(key, body []byte) ([]byte, error) {
	if len(body) < 1 {
		return nil, ErrIntegrity
	}
	if body[0] == 2 {
		return nil, fmt.Errorf("%w: the AEAD packet of RFC 9580", ErrUnsupported)
	}
	if body[0] != 1 {
		return nil, fmt.Errorf("%w: SEIPD version %d", ErrUnsupported, body[0])
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	bs := block.BlockSize()
	plain := cfb(block, body[1:], false)
	if len(plain) < bs+2+22 {
		return nil, ErrIntegrity
	}
	if plain[bs] != plain[bs-2] || plain[bs+1] != plain[bs-1] {
		return nil, ErrIntegrity
	}
	mdcAt := len(plain) - 22
	if plain[mdcAt] != 0xD3 || plain[mdcAt+1] != 0x14 {
		return nil, ErrIntegrity
	}
	sum := sha1.Sum(plain[:mdcAt+2])
	if subtle.ConstantTimeCompare(sum[:], plain[mdcAt+2:]) != 1 {
		return nil, ErrIntegrity
	}
	return plain[bs+2 : mdcAt], nil
}

// readInner walks the packets inside: compression, one-pass signatures, the literal data, the
// signatures after it.
func (m *Message) readInner(data []byte, now time.Time, from []*PublicKey, depth int) (*Message, error) {
	if depth > 2 {
		return nil, fmt.Errorf("%w: nesting", ErrUnsupported)
	}
	pk, err := readPackets(data)
	if err != nil {
		return nil, err
	}
	gotLiteral := false
	for _, p := range pk {
		switch p.tag {
		case tagCompressed:
			if len(p.body) < 1 {
				return nil, fmt.Errorf("empty compressed packet")
			}
			var zr io.Reader
			switch p.body[0] {
			case compNone:
				zr = bytes.NewReader(p.body[1:])
			case compZIP:
				zr = flate.NewReader(bytes.NewReader(p.body[1:]))
			case compZLIB:
				zr, err = zlib.NewReader(bytes.NewReader(p.body[1:]))
				if err != nil {
					return nil, err
				}
			case compBZip2:
				zr = bzip2.NewReader(bytes.NewReader(p.body[1:]))
			default:
				return nil, fmt.Errorf("%w: compression %d", ErrUnsupported, p.body[0])
			}
			inner, err := io.ReadAll(io.LimitReader(zr, maxPacket+1))
			if err != nil {
				return nil, fmt.Errorf("decompress: %v", err)
			}
			if len(inner) > maxPacket {
				return nil, fmt.Errorf("%w: decompressed over %d bytes", ErrUnsupported, maxPacket)
			}
			return m.readInner(inner, now, from, depth+1)
		case tagOnePass:
			m.Signed = true
		case tagLiteral:
			if gotLiteral {
				return nil, fmt.Errorf("%w: two literal packets", ErrUnsupported)
			}
			gotLiteral = true
			r := bytes.NewReader(p.body)
			if _, err := r.ReadByte(); err != nil {
				return nil, err
			}
			nl, err := r.ReadByte()
			if err != nil {
				return nil, err
			}
			name := make([]byte, nl)
			if _, err := io.ReadFull(r, name); err != nil {
				return nil, err
			}
			var date [4]byte
			if _, err := io.ReadFull(r, date[:]); err != nil {
				return nil, err
			}
			m.Filename = string(name)
			m.Data, _ = io.ReadAll(r)
		case tagSignature:
			s, err := parseSignature(p.body)
			if err != nil {
				m.Signed, m.SigReason = true, err.Error()
				continue
			}
			if !gotLiteral {
				continue // a signature before the data without a one-pass: not a form the box makes
			}
			if m.Verified {
				continue // one good signature is enough
			}
			m.check(s, m.Data, now, from)
		case tagMarker, tagPadding, tagTrust:
		default:
			return nil, fmt.Errorf("%w: packet tag %d inside the message", ErrUnsupported, p.tag)
		}
	}
	if !gotLiteral {
		return nil, fmt.Errorf("no data in the message")
	}
	return m, nil
}

// check verifies one document signature against the candidate keys.
func (m *Message) check(s *signature, data []byte, now time.Time, from []*PublicKey) {
	m.Signed = true
	m.SignerID = s.issuer
	if s.typ != 0x00 && s.typ != 0x01 {
		m.SigReason = fmt.Sprintf("signature type 0x%02x over a message", s.typ)
		return
	}
	if s.created.IsZero() {
		m.SigReason = "the signature carries no time"
		return
	}
	if s.created.After(now.Add(24 * time.Hour)) {
		m.SigReason = "the signature is dated in the future"
		return
	}
	if s.sigExpires > 0 && now.After(s.created.Add(s.sigExpires)) {
		m.SigReason = "the signature has expired"
		return
	}
	data = canonical(s.typ, data)
	var tried bool
	for _, k := range from {
		for _, signer := range k.signers {
			if s.issuer != 0 && s.issuer != signer.keyID {
				continue
			}
			if s.hasIssFP && s.issuerFP != signer.fingerprint {
				continue
			}
			tried = true
			d, h, err := s.digest(nil, nil, nil, data)
			if err != nil {
				m.SigReason = err.Error()
				return
			}
			if err := s.verifyWith(signer, d, h); err == nil {
				m.Verified = true
				m.SignedBy = k.Fingerprint
				m.SigReason = ""
				return
			}
		}
	}
	if !tried {
		m.SigReason = "signed by a key the box does not have"
		return
	}
	m.SigReason = "the signature does not match the message"
}

// canonical gives a text-mode signature (type 0x01) its CRLF line ends.
func canonical(typ byte, data []byte) []byte {
	if typ != 0x01 {
		return data
	}
	data = bytes.ReplaceAll(data, []byte("\r\n"), []byte("\n"))
	return bytes.ReplaceAll(data, []byte("\n"), []byte("\r\n"))
}
