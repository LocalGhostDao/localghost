package pgp

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ed25519"
	"crypto/rand"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"time"
)

// Key is the box's own key: an Ed25519 primary that certifies and signs, a Curve25519 subkey
// that receives, one user ID. The secret halves are two 32-byte seeds kept in the vault in the
// box's own form (Marshal); the OpenPGP secret-key packet forms, with their string-to-key
// passphrase machinery, are not needed for a key that never leaves an encrypted volume.
type Key struct {
	Created time.Time
	UserID  string // "Name <address>"
	sign    ed25519.PrivateKey
	enc     *ecdh.PrivateKey
	primary *pubKey
	sub     *pubKey
	public  []byte // the transferable public key, packets
}

type keyFile struct {
	Created int64  `json:"created"`
	UserID  string `json:"userId"`
	Sign    []byte `json:"sign"` // ed25519 seed
	Enc     []byte `json:"enc"`  // x25519 scalar
}

// Generate makes a new key for the user ID at time now (the creation time is part of the key's
// identity: fingerprints hash it, so a key is reproducible from its seeds and this time only).
func Generate(userID string, now time.Time) (*Key, error) {
	_, sk, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	ek, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, err
	}
	return assemble(userID, now.Truncate(time.Second).UTC(), sk, ek)
}

// Unmarshal reads a key written by Marshal.
func Unmarshal(b []byte) (*Key, error) {
	var f keyFile
	if err := json.Unmarshal(b, &f); err != nil {
		return nil, err
	}
	if len(f.Sign) != ed25519.SeedSize || len(f.Enc) != 32 {
		return nil, fmt.Errorf("key file: bad seed lengths")
	}
	ek, err := ecdh.X25519().NewPrivateKey(f.Enc)
	if err != nil {
		return nil, err
	}
	return assemble(f.UserID, time.Unix(f.Created, 0).UTC(), ed25519.NewKeyFromSeed(f.Sign), ek)
}

// Marshal writes the key for the vault.
func (k *Key) Marshal() []byte {
	b, _ := json.Marshal(keyFile{Created: k.Created.Unix(), UserID: k.UserID, Sign: k.sign.Seed(), Enc: k.enc.Bytes()})
	return b
}

func assemble(userID string, created time.Time, sk ed25519.PrivateKey, ek *ecdh.PrivateKey) (*Key, error) {
	k := &Key{Created: created, UserID: userID, sign: sk, enc: ek}
	// the primary key packet body
	var pb bytes.Buffer
	pb.WriteByte(4)
	pb.Write(putU32(uint32(created.Unix())))
	pb.WriteByte(algoEdDSA)
	pb.WriteByte(byte(len(oidEd25519)))
	pb.Write(oidEd25519)
	writeMPI(&pb, append([]byte{0x40}, sk.Public().(ed25519.PublicKey)...))
	primary, err := parsePubKey(pb.Bytes())
	if err != nil {
		return nil, err
	}
	// the encryption subkey packet body
	var sb bytes.Buffer
	sb.WriteByte(4)
	sb.Write(putU32(uint32(created.Unix())))
	sb.WriteByte(algoECDH)
	sb.WriteByte(byte(len(oidCurve25519)))
	sb.Write(oidCurve25519)
	writeMPI(&sb, append([]byte{0x40}, ek.PublicKey().Bytes()...))
	sb.Write([]byte{3, 1, hashSHA256, symAES256})
	sub, err := parsePubKey(sb.Bytes())
	if err != nil {
		return nil, err
	}
	k.primary, k.sub = primary, sub

	// the self-signature on the user ID: what the key is for and what it prefers
	uid := []byte(userID)
	selfSubs := [][]byte{
		sub2(2, putU32(uint32(created.Unix()))),
		sub2(27, []byte{0x03}), // certify + sign
		sub2(11, []byte{symAES256, symAES128}),
		sub2(21, []byte{hashSHA256, hashSHA512}),
		sub2(22, []byte{compNone, compZLIB, compZIP}), // the box would rather not compress; it reads all three
		sub2(30, []byte{0x01}),                        // modification detection (the SEIPD packet)
		sub2(23, []byte{0x80}),                        // key server: no-modify
		sub2(33, append([]byte{4}, primary.fingerprint[:]...)),
	}
	selfSig, err := k.makeSignature(0x13, selfSubs, func(s *signature) ([]byte, error) {
		d, _, err := s.digest(primary, nil, uid, nil)
		return d, err
	})
	if err != nil {
		return nil, err
	}
	bindSubs := [][]byte{
		sub2(2, putU32(uint32(created.Unix()))),
		sub2(27, []byte{0x0C}), // encrypt communications + storage
		sub2(33, append([]byte{4}, primary.fingerprint[:]...)),
	}
	bindSig, err := k.makeSignature(0x18, bindSubs, func(s *signature) ([]byte, error) {
		d, _, err := s.digest(primary, sub, nil, nil)
		return d, err
	})
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	writePacket(&out, tagPublicKey, pb.Bytes())
	writePacket(&out, tagUserID, uid)
	writePacket(&out, tagSignature, selfSig)
	writePacket(&out, tagPublicSub, sb.Bytes())
	writePacket(&out, tagSignature, bindSig)
	k.public = out.Bytes()
	// the exported key must read back through the same validation others' keys get
	if _, err := ParsePublicKey(k.public, created); err != nil {
		return nil, fmt.Errorf("own key does not validate: %v", err)
	}
	return k, nil
}

// sub2 frames one signature subpacket (one-byte length form; all of ours are short).
func sub2(typ byte, data []byte) []byte {
	if len(data)+1 >= 192 {
		panic("subpacket too long for the short form")
	}
	return append([]byte{byte(len(data) + 1), typ}, data...)
}

// makeSignature builds a version 4 EdDSA/SHA-256 signature packet body of the given type: the
// hashed section, then the digest over what digestOf covers plus that section, then the MPIs.
func (k *Key) makeSignature(typ byte, hashedSubs [][]byte, digestOf func(*signature) ([]byte, error)) ([]byte, error) {
	var hs bytes.Buffer
	for _, s := range hashedSubs {
		hs.Write(s)
	}
	var body bytes.Buffer
	body.Write([]byte{4, typ, algoEdDSA, hashSHA256})
	var hl [2]byte
	binary.BigEndian.PutUint16(hl[:], uint16(hs.Len()))
	body.Write(hl[:])
	body.Write(hs.Bytes())
	sig := &signature{typ: typ, pubAlgo: algoEdDSA, hashAlgo: hashSHA256, hashed: append([]byte(nil), body.Bytes()...)}
	digest, err := digestOf(sig)
	if err != nil {
		return nil, err
	}
	// unhashed: the issuer key ID
	var kid [8]byte
	binary.BigEndian.PutUint64(kid[:], k.primary.keyID)
	unh := sub2(16, kid[:])
	var ul [2]byte
	binary.BigEndian.PutUint16(ul[:], uint16(len(unh)))
	body.Write(ul[:])
	body.Write(unh)
	body.Write(digest[:2])
	raw := ed25519.Sign(k.sign, digest)
	writeMPI(&body, raw[:32])
	writeMPI(&body, raw[32:])
	return body.Bytes(), nil
}

// Public returns the box's transferable public key, as others should see it.
func (k *Key) Public() *PublicKey {
	p, _ := ParsePublicKey(k.public, k.Created)
	return p
}

// Fingerprint is the primary key's.
func (k *Key) Fingerprint() [20]byte { return k.primary.fingerprint }

// Armor exports the public key.
func (k *Key) Armor() string { return Armor(BlockPublicKey, k.public) }
