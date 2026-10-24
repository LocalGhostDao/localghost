package pgp

import (
	"bytes"
	"crypto"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rsa"
	"crypto/sha1"
	_ "crypto/sha256" // registers SHA-224/256 for crypto.Hash
	_ "crypto/sha512" // SHA-384/512
	"crypto/subtle"
	"encoding/binary"
	"fmt"
	"io"
	"math/big"
	"time"
)

// curves the box accepts in other people's keys, by OID (RFC 6637 and the Curve25519 draft)
type curveInfo struct {
	name  string
	oid   []byte
	nist  elliptic.Curve // ECDSA
	ecdh  ecdh.Curve     // ECDH
	plen  int            // point length on the wire, with its prefix byte
	flen  int            // field element length
	ed    bool           // Ed25519 (EdDSA)
	x2551 bool           // Curve25519 (ECDH)
}

var (
	oidP256       = []byte{0x2A, 0x86, 0x48, 0xCE, 0x3D, 0x03, 0x01, 0x07}
	oidP384       = []byte{0x2B, 0x81, 0x04, 0x00, 0x22}
	oidP521       = []byte{0x2B, 0x81, 0x04, 0x00, 0x23}
	oidCurve25519 = []byte{0x2B, 0x06, 0x01, 0x04, 0x01, 0x97, 0x55, 0x01, 0x05, 0x01}
	oidEd25519    = []byte{0x2B, 0x06, 0x01, 0x04, 0x01, 0xDA, 0x47, 0x0F, 0x01}
)

var curves = []curveInfo{
	{name: "P-256", oid: oidP256, nist: elliptic.P256(), ecdh: ecdh.P256(), plen: 65, flen: 32},
	{name: "P-384", oid: oidP384, nist: elliptic.P384(), ecdh: ecdh.P384(), plen: 97, flen: 48},
	{name: "P-521", oid: oidP521, nist: elliptic.P521(), ecdh: ecdh.P521(), plen: 133, flen: 66},
	{name: "Curve25519", oid: oidCurve25519, ecdh: ecdh.X25519(), plen: 33, flen: 32, x2551: true},
	{name: "Ed25519", oid: oidEd25519, plen: 33, flen: 32, ed: true},
}

func curveByOID(oid []byte) *curveInfo {
	for i := range curves {
		if bytes.Equal(curves[i].oid, oid) {
			return &curves[i]
		}
	}
	return nil
}

// pubKey is one public key packet, primary or subkey, parsed.
type pubKey struct {
	created     time.Time
	algo        byte
	rsa         *rsa.PublicKey
	ed          ed25519.PublicKey
	ecdsa       *ecdsa.PublicKey
	ecdh        *ecdh.PublicKey
	curve       *curveInfo
	kdfHash     byte // ECDH: the KDF's hash
	kdfSym      byte // ECDH: the key-wrap cipher
	body        []byte
	fingerprint [20]byte
	keyID       uint64
	// set from the self-signature that binds it
	flags   byte
	expires time.Time
}

func parsePubKey(body []byte) (*pubKey, error) {
	r := bytes.NewReader(body)
	ver, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	switch ver {
	case 4:
	case 5, 6:
		return nil, fmt.Errorf("%w: a version %d key (RFC 9580); the box speaks version 4 keys, which every client still makes", ErrUnsupported, ver)
	default:
		return nil, fmt.Errorf("%w: key packet version %d", ErrUnsupported, ver)
	}
	var cr [4]byte
	if _, err := io.ReadFull(r, cr[:]); err != nil {
		return nil, err
	}
	algo, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	k := &pubKey{created: time.Unix(int64(u32(cr[:])), 0).UTC(), algo: algo, body: body}
	switch algo {
	case algoRSA, algoRSAEncrypt, algoRSASign:
		n, err := readMPI(r)
		if err != nil {
			return nil, err
		}
		e, err := readMPI(r)
		if err != nil {
			return nil, err
		}
		if len(n) < 256 { // 2048 bits
			return nil, fmt.Errorf("%w: an RSA key under 2048 bits", ErrUnsupported)
		}
		eb := new(big.Int).SetBytes(e)
		if !eb.IsInt64() || eb.Int64() < 3 || eb.Int64() > 1<<31 {
			return nil, fmt.Errorf("%w: RSA exponent", ErrUnsupported)
		}
		k.rsa = &rsa.PublicKey{N: new(big.Int).SetBytes(n), E: int(eb.Int64())}
	case algoECDSA, algoEdDSA, algoECDH:
		ol, err := r.ReadByte()
		if err != nil {
			return nil, err
		}
		oid := make([]byte, ol)
		if _, err := io.ReadFull(r, oid); err != nil {
			return nil, err
		}
		c := curveByOID(oid)
		if c == nil {
			return nil, fmt.Errorf("%w: curve with OID %x", ErrUnsupported, oid)
		}
		point, err := readMPI(r)
		if err != nil {
			return nil, err
		}
		k.curve = c
		switch algo {
		case algoEdDSA:
			if !c.ed || len(point) != 33 || point[0] != 0x40 {
				return nil, fmt.Errorf("%w: EdDSA key on %s", ErrUnsupported, c.name)
			}
			k.ed = ed25519.PublicKey(point[1:])
		case algoECDSA:
			if c.nist == nil {
				return nil, fmt.Errorf("%w: ECDSA key on %s", ErrUnsupported, c.name)
			}
			x, y := elliptic.Unmarshal(c.nist, point)
			if x == nil {
				return nil, fmt.Errorf("bad ECDSA point")
			}
			k.ecdsa = &ecdsa.PublicKey{Curve: c.nist, X: x, Y: y}
		case algoECDH:
			if c.ecdh == nil {
				return nil, fmt.Errorf("%w: ECDH key on %s", ErrUnsupported, c.name)
			}
			if c.x2551 {
				if len(point) != 33 || point[0] != 0x40 {
					return nil, fmt.Errorf("bad Curve25519 point")
				}
				point = point[1:]
			}
			pub, err := c.ecdh.NewPublicKey(point)
			if err != nil {
				return nil, fmt.Errorf("bad ECDH point: %v", err)
			}
			k.ecdh = pub
			kl, err := r.ReadByte()
			if err != nil {
				return nil, err
			}
			kdf := make([]byte, kl)
			if _, err := io.ReadFull(r, kdf); err != nil {
				return nil, err
			}
			if len(kdf) != 3 || kdf[0] != 1 {
				return nil, fmt.Errorf("%w: ECDH KDF parameters", ErrUnsupported)
			}
			k.kdfHash, k.kdfSym = kdf[1], kdf[2]
			if hashOf(k.kdfHash) == 0 || symKeyLen(k.kdfSym) == 0 {
				return nil, fmt.Errorf("%w: ECDH KDF hash %d / cipher %d", ErrUnsupported, kdf[1], kdf[2])
			}
		}
	case algoDSA, algoElGamal:
		return nil, fmt.Errorf("%w: a DSA/ElGamal key", ErrUnsupported)
	case algoX25519, algoEd25519:
		return nil, fmt.Errorf("%w: an RFC 9580 key algorithm", ErrUnsupported)
	default:
		return nil, fmt.Errorf("%w: public key algorithm %d", ErrUnsupported, algo)
	}
	k.fingerprint = fingerprintV4(body)
	k.keyID = binary.BigEndian.Uint64(k.fingerprint[12:])
	return k, nil
}

func fingerprintV4(body []byte) [20]byte {
	h := sha1.New()
	h.Write([]byte{0x99, byte(len(body) >> 8), byte(len(body))})
	h.Write(body)
	var fp [20]byte
	copy(fp[:], h.Sum(nil))
	return fp
}

func hashOf(id byte) crypto.Hash {
	switch id {
	case hashSHA256:
		return crypto.SHA256
	case hashSHA384:
		return crypto.SHA384
	case hashSHA512:
		return crypto.SHA512
	case hashSHA224:
		return crypto.SHA224
	}
	return 0
}

func symKeyLen(id byte) int {
	switch id {
	case symAES128:
		return 16
	case symAES192:
		return 24
	case symAES256:
		return 32
	}
	return 0
}

// signature is a version 4 signature packet, parsed.
type signature struct {
	typ        byte
	pubAlgo    byte
	hashAlgo   byte
	hashed     []byte // the hashed section as on the wire (version through hashed subpackets)
	subs       []subpacket
	unhashed   []subpacket
	left16     [2]byte
	rsaSig     []byte
	r, s       []byte
	created    time.Time
	sigExpires time.Duration
	keyExpires time.Duration
	issuer     uint64
	issuerFP   [20]byte
	hasIssFP   bool
	flags      byte
	hasFlags   bool
	prefSym    []byte
	prefHash   []byte
	prefComp   []byte
	features   byte
	primaryUID bool
	embedded   []byte
	revoked    bool
}

type subpacket struct {
	typ      byte
	critical bool
	data     []byte
}

func parseSignature(body []byte) (*signature, error) {
	r := bytes.NewReader(body)
	ver, err := r.ReadByte()
	if err != nil {
		return nil, err
	}
	switch ver {
	case 4:
	case 3:
		return nil, fmt.Errorf("%w: a version 3 signature", ErrUnsupported)
	case 5, 6:
		return nil, fmt.Errorf("%w: a version %d signature (RFC 9580)", ErrUnsupported, ver)
	default:
		return nil, fmt.Errorf("%w: signature version %d", ErrUnsupported, ver)
	}
	var hdr [3]byte
	if _, err := io.ReadFull(r, hdr[:]); err != nil {
		return nil, err
	}
	s := &signature{typ: hdr[0], pubAlgo: hdr[1], hashAlgo: hdr[2]}
	hl, err := readU16(r)
	if err != nil {
		return nil, err
	}
	hashedSubs := make([]byte, hl)
	if _, err := io.ReadFull(r, hashedSubs); err != nil {
		return nil, err
	}
	s.hashed = body[:6+int(hl)]
	ul, err := readU16(r)
	if err != nil {
		return nil, err
	}
	unhashedSubs := make([]byte, ul)
	if _, err := io.ReadFull(r, unhashedSubs); err != nil {
		return nil, err
	}
	if _, err := io.ReadFull(r, s.left16[:]); err != nil {
		return nil, err
	}
	if s.subs, err = parseSubpackets(hashedSubs); err != nil {
		return nil, err
	}
	if s.unhashed, err = parseSubpackets(unhashedSubs); err != nil {
		return nil, err
	}
	for _, sp := range s.subs {
		s.apply(sp, true)
	}
	for _, sp := range s.unhashed {
		s.apply(sp, false)
	}
	switch s.pubAlgo {
	case algoRSA, algoRSASign:
		if s.rsaSig, err = readMPI(r); err != nil {
			return nil, err
		}
	case algoEdDSA, algoECDSA:
		if s.r, err = readMPI(r); err != nil {
			return nil, err
		}
		if s.s, err = readMPI(r); err != nil {
			return nil, err
		}
	default:
		return nil, fmt.Errorf("%w: signature algorithm %d", ErrUnsupported, s.pubAlgo)
	}
	return s, nil
}

func readU16(r *bytes.Reader) (uint16, error) {
	var b [2]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return 0, err
	}
	return binary.BigEndian.Uint16(b[:]), nil
}

func parseSubpackets(b []byte) ([]subpacket, error) {
	var out []subpacket
	r := bytes.NewReader(b)
	for r.Len() > 0 {
		n, partial, err := readNewLength(r)
		if err != nil || partial || n < 1 || n > int64(r.Len()) {
			return nil, fmt.Errorf("bad signature subpacket")
		}
		t, _ := r.ReadByte()
		data := make([]byte, n-1)
		if _, err := io.ReadFull(r, data); err != nil {
			return nil, err
		}
		out = append(out, subpacket{typ: t & 0x7f, critical: t&0x80 != 0, data: data})
	}
	return out, nil
}

// apply reads the subpackets the box cares about. Only the issuer and the embedded back-signature
// may come from the unhashed area; everything else is believed only when signed.
func (s *signature) apply(sp subpacket, hashed bool) {
	switch sp.typ {
	case 16:
		if len(sp.data) == 8 && s.issuer == 0 {
			s.issuer = binary.BigEndian.Uint64(sp.data)
		}
	case 32:
		// the back-signature proves itself (it is checked with the subkey), and GnuPG puts it
		// in the unhashed area
		if s.embedded == nil {
			s.embedded = sp.data
		}
	}
	if !hashed {
		return
	}
	switch sp.typ {
	case 2:
		if len(sp.data) == 4 {
			s.created = time.Unix(int64(u32(sp.data)), 0).UTC()
		}
	case 3:
		if len(sp.data) == 4 {
			s.sigExpires = time.Duration(u32(sp.data)) * time.Second
		}
	case 9:
		if len(sp.data) == 4 {
			s.keyExpires = time.Duration(u32(sp.data)) * time.Second
		}
	case 11:
		s.prefSym = sp.data
	case 21:
		s.prefHash = sp.data
	case 22:
		s.prefComp = sp.data
	case 25:
		s.primaryUID = len(sp.data) == 1 && sp.data[0] != 0
	case 27:
		if len(sp.data) >= 1 {
			s.flags, s.hasFlags = sp.data[0], true
		}
	case 30:
		if len(sp.data) >= 1 {
			s.features = sp.data[0]
		}
	case 33:
		if len(sp.data) == 21 && sp.data[0] == 4 {
			copy(s.issuerFP[:], sp.data[1:])
			s.hasIssFP = true
		}
	}
}

// digest hashes what a signature of this type covers, then the signature's own trailer.
func (s *signature) digest(primary, sub *pubKey, uid []byte, data []byte) ([]byte, crypto.Hash, error) {
	h := hashOf(s.hashAlgo)
	if h == 0 {
		return nil, 0, fmt.Errorf("%w: signature hash %d (SHA-1 and MD5 are not accepted)", ErrUnsupported, s.hashAlgo)
	}
	w := h.New()
	keyBody := func(k *pubKey) {
		w.Write([]byte{0x99, byte(len(k.body) >> 8), byte(len(k.body))})
		w.Write(k.body)
	}
	switch s.typ {
	case 0x00, 0x01:
		w.Write(data)
	case 0x10, 0x11, 0x12, 0x13, 0x30:
		keyBody(primary)
		w.Write([]byte{0xB4})
		w.Write(putU32(uint32(len(uid))))
		w.Write(uid)
	case 0x18, 0x19, 0x28:
		keyBody(primary)
		keyBody(sub)
	case 0x1F, 0x20:
		keyBody(primary)
	default:
		return nil, 0, fmt.Errorf("%w: signature type 0x%02x", ErrUnsupported, s.typ)
	}
	w.Write(s.hashed)
	w.Write([]byte{0x04, 0xFF})
	w.Write(putU32(uint32(len(s.hashed))))
	return w.Sum(nil), h, nil
}

// verifyWith checks the signature's MPIs against a key over a digest.
func (s *signature) verifyWith(k *pubKey, digest []byte, h crypto.Hash) error {
	if subtle.ConstantTimeCompare(digest[:2], s.left16[:]) != 1 {
		return fmt.Errorf("signature does not match")
	}
	switch {
	case k.rsa != nil && (s.pubAlgo == algoRSA || s.pubAlgo == algoRSASign):
		sig, err := fixedMPI(s.rsaSig, (k.rsa.N.BitLen()+7)/8)
		if err != nil {
			return err
		}
		if err := rsa.VerifyPKCS1v15(k.rsa, h, digest, sig); err != nil {
			return fmt.Errorf("signature does not match")
		}
		return nil
	case k.ed != nil && s.pubAlgo == algoEdDSA:
		rr, err := fixedMPI(s.r, 32)
		if err != nil {
			return err
		}
		ss, err := fixedMPI(s.s, 32)
		if err != nil {
			return err
		}
		if !ed25519.Verify(k.ed, digest, append(rr, ss...)) {
			return fmt.Errorf("signature does not match")
		}
		return nil
	case k.ecdsa != nil && s.pubAlgo == algoECDSA:
		if !ecdsa.Verify(k.ecdsa, digest, new(big.Int).SetBytes(s.r), new(big.Int).SetBytes(s.s)) {
			return fmt.Errorf("signature does not match")
		}
		return nil
	}
	return fmt.Errorf("signature algorithm %d does not fit the key", s.pubAlgo)
}

// PublicKey is a transferable public key (RFC 4880 section 11.1): someone's primary key, the user
// IDs its self-signatures certify, and the subkeys it binds, each checked, with the expired and
// the revoked dropped. The packets are kept as received so the key can be exported again.
type PublicKey struct {
	Fingerprint [20]byte
	KeyID       uint64
	UserIDs     []string
	Created     time.Time
	Expires     time.Time // zero: none
	Revoked     bool
	primary     *pubKey
	signers     []*pubKey // the primary (if it may sign) and signing subkeys
	encrypters  []*pubKey // subkeys (or the primary) that may encrypt, newest first
	packets     []byte
}

// ParsePublicKey reads one transferable public key, armored or binary, and validates its
// self-signatures at time now.
func ParsePublicKey(data []byte, now time.Time) (*PublicKey, error) {
	raw, err := unwrap(data)
	if err != nil {
		return nil, err
	}
	pk, err := readPackets(raw)
	if err != nil {
		return nil, err
	}
	if len(pk) == 0 || pk[0].tag != tagPublicKey {
		return nil, fmt.Errorf("not a public key")
	}
	primary, err := parsePubKey(pk[0].body)
	if err != nil {
		return nil, err
	}
	k := &PublicKey{Fingerprint: primary.fingerprint, KeyID: primary.keyID, Created: primary.created, primary: primary, packets: raw}
	var (
		uid       []byte
		sub       *pubKey
		primFlags byte
		gotFlags  bool
		primExp   time.Duration
		latestUID time.Time
	)
	for _, p := range pk[1:] {
		switch p.tag {
		case tagUserID:
			uid, sub = p.body, nil
		case tagUserAttr:
			uid, sub = nil, nil // photos and such: ignored, with their signatures
		case tagPublicSub:
			uid = nil
			sub, err = parsePubKey(p.body)
			if err != nil {
				sub = nil // an unsupported subkey is skipped, not fatal
				continue
			}
		case tagSignature:
			sig, err := parseSignature(p.body)
			if err != nil {
				continue // a signature the box cannot read is skipped
			}
			if sig.issuer != 0 && sig.issuer != primary.keyID && sig.typ != 0x19 {
				continue // a third party's certification: not ours to judge
			}
			switch {
			case uid != nil && (sig.typ >= 0x10 && sig.typ <= 0x13):
				d, h, err := sig.digest(primary, nil, uid, nil)
				if err != nil || sig.verifyWith(primary, d, h) != nil {
					continue
				}
				if sig.sigExpires > 0 && now.After(sig.created.Add(sig.sigExpires)) {
					continue
				}
				if sig.created.After(latestUID) {
					latestUID = sig.created
					if sig.hasFlags {
						primFlags, gotFlags = sig.flags, true
					}
					primExp = sig.keyExpires
				}
				if !contains(k.UserIDs, string(uid)) {
					k.UserIDs = append(k.UserIDs, string(uid))
				}
			case uid != nil && sig.typ == 0x30:
				d, h, err := sig.digest(primary, nil, uid, nil)
				if err == nil && sig.verifyWith(primary, d, h) == nil {
					k.UserIDs = remove(k.UserIDs, string(uid))
				}
			case sub != nil && sig.typ == 0x18:
				d, h, err := sig.digest(primary, sub, nil, nil)
				if err != nil || sig.verifyWith(primary, d, h) != nil {
					continue
				}
				if sig.sigExpires > 0 && now.After(sig.created.Add(sig.sigExpires)) {
					continue
				}
				sub.flags = sig.flags
				if sig.keyExpires > 0 {
					sub.expires = sub.created.Add(sig.keyExpires)
					if now.After(sub.expires) {
						continue
					}
				}
				if sig.flags&0x0C != 0 && sub.canEncrypt() {
					k.encrypters = append([]*pubKey{sub}, k.encrypters...)
				}
				if sig.flags&0x02 != 0 && sub.canSign() && sig.embedded != nil {
					// a signing subkey must sign back (the primary's word alone would let anyone
					// attach a signing key they hold to someone else's certificate)
					back, err := parseSignature(sig.embedded)
					if err == nil && back.typ == 0x19 {
						bd, bh, err := back.digest(primary, sub, nil, nil)
						if err == nil && back.verifyWith(sub, bd, bh) == nil {
							k.signers = append(k.signers, sub)
						}
					}
				}
			case sub != nil && sig.typ == 0x28:
				d, h, err := sig.digest(primary, sub, nil, nil)
				if err == nil && sig.verifyWith(primary, d, h) == nil {
					k.encrypters = removeKey(k.encrypters, sub)
					k.signers = removeKey(k.signers, sub)
				}
			case uid == nil && sub == nil && sig.typ == 0x20:
				d, h, err := sig.digest(primary, nil, nil, nil)
				if err == nil && sig.verifyWith(primary, d, h) == nil {
					k.Revoked = true
				}
			case uid == nil && sub == nil && sig.typ == 0x1F:
				d, h, err := sig.digest(primary, nil, nil, nil)
				if err == nil && sig.verifyWith(primary, d, h) == nil && sig.hasFlags && !gotFlags {
					primFlags, gotFlags = sig.flags, true
				}
			}
		}
	}
	if len(k.UserIDs) == 0 {
		return nil, fmt.Errorf("the key certifies no user ID")
	}
	if primExp > 0 {
		k.Expires = primary.created.Add(primExp)
		if now.After(k.Expires) {
			return nil, fmt.Errorf("the key expired on %s", k.Expires.Format("2 January 2006"))
		}
	}
	if k.Revoked {
		return nil, fmt.Errorf("the key is revoked")
	}
	primary.flags = primFlags
	if (!gotFlags || primFlags&0x02 != 0) && primary.canSign() {
		k.signers = append([]*pubKey{primary}, k.signers...)
	}
	if (!gotFlags || primFlags&0x0C != 0) && primary.canEncrypt() {
		k.encrypters = append(k.encrypters, primary)
	}
	return k, nil
}

func (k *pubKey) canSign() bool {
	return k.ed != nil || k.ecdsa != nil || (k.rsa != nil && k.algo != algoRSAEncrypt)
}
func (k *pubKey) canEncrypt() bool { return k.ecdh != nil || (k.rsa != nil && k.algo != algoRSASign) }

// CanEncrypt says whether the key has something to encrypt to.
func (k *PublicKey) CanEncrypt() bool { return len(k.encrypters) > 0 }

// Armor exports the key as it was received.
func (k *PublicKey) Armor() string { return Armor(BlockPublicKey, k.packets) }

// Bytes exports the key's packets.
func (k *PublicKey) Bytes() []byte { return k.packets }

func contains(xs []string, s string) bool {
	for _, x := range xs {
		if x == s {
			return true
		}
	}
	return false
}

func remove(xs []string, s string) []string {
	var out []string
	for _, x := range xs {
		if x != s {
			out = append(out, x)
		}
	}
	return out
}

func removeKey(ks []*pubKey, k *pubKey) []*pubKey {
	var out []*pubKey
	for _, x := range ks {
		if x != k {
			out = append(out, x)
		}
	}
	return out
}
