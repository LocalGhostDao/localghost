// Package pgp is a small OpenPGP (RFC 4880, with RFC 6637's curves) for the box's mail: a key of
// its own, the public keys of the people it writes to, and the three operations a message needs
// (encrypt to them and sign, decrypt and verify, sign alone). Built on the standard library's
// primitives (ed25519, ecdh, rsa, ecdsa, aes, cipher, sha1, sha256, sha512, the compressors) and
// nothing else: no OpenPGP library, frozen or otherwise. The subset is the one every client in
// use speaks (GnuPG, Thunderbird, Proton, Apple Mail with GPG Suite): version 4 keys, Ed25519 and
// Curve25519 for the box's own, RSA and the NIST curves accepted from others, AES-256 in the
// integrity-protected packet, SHA-256 signatures. The relay that carries the mail sees ciphertext.
//
// Version 6 keys and the AEAD packet of RFC 9580 are recognised and refused with a plain reason
// (the box's key does not advertise them, so no client sends them to it).
package pgp

import (
	"bytes"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math/big"
)

// packet tags (RFC 4880 section 4.3)
const (
	tagPKESK      = 1
	tagSignature  = 2
	tagSKESK      = 3
	tagOnePass    = 4
	tagSecretKey  = 5
	tagPublicKey  = 6
	tagSecretSub  = 7
	tagCompressed = 8
	tagSED        = 9
	tagMarker     = 10
	tagLiteral    = 11
	tagTrust      = 12
	tagUserID     = 13
	tagPublicSub  = 14
	tagUserAttr   = 17
	tagSEIPD      = 18
	tagMDC        = 19
	tagPadding    = 21
)

// public key algorithms
const (
	algoRSA        = 1
	algoRSAEncrypt = 2
	algoRSASign    = 3
	algoElGamal    = 16
	algoDSA        = 17
	algoECDH       = 18
	algoECDSA      = 19
	algoEdDSA      = 22
	algoX25519     = 25 // RFC 9580, refused
	algoEd25519    = 27 // RFC 9580, refused
)

// symmetric, hash and compression algorithms
const (
	symAES128 = 7
	symAES192 = 8
	symAES256 = 9

	hashSHA1   = 2
	hashSHA256 = 8
	hashSHA384 = 9
	hashSHA512 = 10
	hashSHA224 = 11

	compNone  = 0
	compZIP   = 1
	compZLIB  = 2
	compBZip2 = 3
)

// maxPacket bounds any one packet the parser will hold; mail bodies and attachments are capped
// well below it by the caller.
const maxPacket = 64 << 20

var (
	ErrNoKey       = errors.New("no usable key")
	ErrNotForUs    = errors.New("the message is not encrypted to this key")
	ErrIntegrity   = errors.New("the message failed its integrity check")
	ErrUnsupported = errors.New("unsupported OpenPGP construct")
)

type packet struct {
	tag  byte
	body []byte
}

// readPackets splits a binary OpenPGP stream into packets, old and new header formats, partial
// body lengths joined.
func readPackets(data []byte) ([]packet, error) {
	var out []packet
	r := bytes.NewReader(data)
	for r.Len() > 0 {
		p, err := readPacket(r)
		if err != nil {
			return nil, err
		}
		out = append(out, p)
	}
	return out, nil
}

func readPacket(r *bytes.Reader) (packet, error) {
	c, err := r.ReadByte()
	if err != nil {
		return packet{}, err
	}
	if c&0x80 == 0 {
		return packet{}, fmt.Errorf("%w: bad packet header 0x%02x", ErrUnsupported, c)
	}
	var tag byte
	var body []byte
	if c&0x40 != 0 {
		// new format
		tag = c & 0x3f
		var buf bytes.Buffer
		for {
			n, partial, err := readNewLength(r)
			if err != nil {
				return packet{}, err
			}
			if n > maxPacket || buf.Len()+int(n) > maxPacket {
				return packet{}, fmt.Errorf("%w: packet over %d bytes", ErrUnsupported, maxPacket)
			}
			chunk := make([]byte, n)
			if _, err := io.ReadFull(r, chunk); err != nil {
				return packet{}, fmt.Errorf("truncated packet (tag %d)", tag)
			}
			buf.Write(chunk)
			if !partial {
				break
			}
		}
		body = buf.Bytes()
	} else {
		tag = (c >> 2) & 0x0f
		var n int64
		switch c & 0x03 {
		case 0:
			b, err := r.ReadByte()
			if err != nil {
				return packet{}, err
			}
			n = int64(b)
		case 1:
			var b [2]byte
			if _, err := io.ReadFull(r, b[:]); err != nil {
				return packet{}, err
			}
			n = int64(binary.BigEndian.Uint16(b[:]))
		case 2:
			var b [4]byte
			if _, err := io.ReadFull(r, b[:]); err != nil {
				return packet{}, err
			}
			n = int64(binary.BigEndian.Uint32(b[:]))
		case 3:
			n = int64(r.Len()) // indeterminate: to the end
		}
		if n > maxPacket {
			return packet{}, fmt.Errorf("%w: packet over %d bytes", ErrUnsupported, maxPacket)
		}
		body = make([]byte, n)
		if _, err := io.ReadFull(r, body); err != nil {
			return packet{}, fmt.Errorf("truncated packet (tag %d)", tag)
		}
	}
	return packet{tag: tag, body: body}, nil
}

func readNewLength(r *bytes.Reader) (n int64, partial bool, err error) {
	b, err := r.ReadByte()
	if err != nil {
		return 0, false, err
	}
	switch {
	case b < 192:
		return int64(b), false, nil
	case b < 224:
		b2, err := r.ReadByte()
		if err != nil {
			return 0, false, err
		}
		return (int64(b)-192)<<8 + int64(b2) + 192, false, nil
	case b < 255:
		return 1 << (b & 0x1f), true, nil
	default:
		var buf [4]byte
		if _, err := io.ReadFull(r, buf[:]); err != nil {
			return 0, false, err
		}
		return int64(binary.BigEndian.Uint32(buf[:])), false, nil
	}
}

// writePacket appends a new-format packet with a definite length.
func writePacket(w *bytes.Buffer, tag byte, body []byte) {
	w.WriteByte(0xc0 | tag)
	n := len(body)
	switch {
	case n < 192:
		w.WriteByte(byte(n))
	case n < 8384:
		n -= 192
		w.WriteByte(byte(n>>8) + 192)
		w.WriteByte(byte(n))
	default:
		w.WriteByte(255)
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], uint32(n))
		w.Write(b[:])
	}
	w.Write(body)
}

// mpi reads a multi-precision integer: a two-byte bit count and the big-endian bytes.
func readMPI(r *bytes.Reader) ([]byte, error) {
	var b [2]byte
	if _, err := io.ReadFull(r, b[:]); err != nil {
		return nil, err
	}
	bits := int(binary.BigEndian.Uint16(b[:]))
	n := (bits + 7) / 8
	out := make([]byte, n)
	if _, err := io.ReadFull(r, out); err != nil {
		return nil, err
	}
	return out, nil
}

func writeMPI(w *bytes.Buffer, b []byte) {
	b = bytes.TrimLeft(b, "\x00")
	bits := 0
	if len(b) > 0 {
		bits = (len(b)-1)*8 + big.NewInt(int64(b[0])).BitLen()
	}
	var h [2]byte
	binary.BigEndian.PutUint16(h[:], uint16(bits))
	w.Write(h[:])
	w.Write(b)
}

// fixedMPI pads a value to a fixed length (a signature or scalar that may have leading zeros).
func fixedMPI(b []byte, n int) ([]byte, error) {
	if len(b) > n {
		return nil, fmt.Errorf("%w: value of %d bytes where %d fit", ErrUnsupported, len(b), n)
	}
	out := make([]byte, n)
	copy(out[n-len(b):], b)
	return out, nil
}

func u32(b []byte) uint32 { return binary.BigEndian.Uint32(b) }

func putU32(v uint32) []byte {
	var b [4]byte
	binary.BigEndian.PutUint32(b[:], v)
	return b[:]
}
