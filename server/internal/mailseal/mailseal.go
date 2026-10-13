// Package mailseal is the envelope a message travels in between the relay and the box: X25519
// to the recipient's public key, HKDF-SHA256, AES-256-GCM in 64 KB authenticated chunks. It is
// the backup's shape (internal/framed/backup.go) with its own magic and info string and the
// standard library's cipher instead of x/crypto's, so the relay, which lives in the web
// repository, needs no dependency at all. A sealed mail can never be opened as a backup or the
// other way round. The relay seals as the bytes arrive and holds no key that opens anything;
// the box opens with the private half that never leaves the volume. The same shape carries the
// box's outgoing mail to the relay, sealed to the relay's key, so the public API never sees a
// message in the clear.
//
// Format: "GHSTML1" || ephemeral X25519 public key (32) || chunks. Chunk: uint32 BE ciphertext
// length || AES-256-GCM ciphertext (with its 16-byte tag) of up to 64 KB plaintext. Key:
// HKDF-SHA256(shared = X25519(ephemeral, recipient), salt = ephPub || recipientPub,
// info = "ghost-mail-v1", 32 bytes). Nonce (12 bytes): bytes 0..7 BE64 chunk index, byte 11 = 1
// on the final chunk, so a truncated file fails to open instead of opening short. The relay in
// the web repository (deploy/mail/internal/seal) is this file; the two must stay the same.
package mailseal

import (
	"bufio"
	"bytes"
	"crypto/aes"
	"crypto/cipher"
	"crypto/ecdh"
	"crypto/hkdf"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
)

const (
	Magic = "GHSTML1"
	Chunk = 64 << 10
	info  = "ghost-mail-v1"
)

// ErrNotSealed is a stream whose first bytes are not the mail seal's.
var ErrNotSealed = errors.New("not a sealed mail")

// Writer seals a stream to a recipient's public key.
type Writer struct {
	w    io.Writer
	aead interface {
		Seal(dst, nonce, plaintext, additionalData []byte) []byte
		NonceSize() int
	}
	buf   []byte
	n     int
	chunk uint64
	done  bool
}

func derive(shared, ephPub, recipientPub []byte) (cipher.AEAD, error) {
	salt := append(append([]byte{}, ephPub...), recipientPub...)
	key, err := hkdf.Key(sha256.New, shared, salt, info, 32)
	if err != nil {
		return nil, fmt.Errorf("hkdf: %w", err)
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	return cipher.NewGCM(block)
}

// NewWriter starts a sealed stream to recipientPub (32 bytes). Close writes the final chunk.
func NewWriter(w io.Writer, recipientPub []byte) (*Writer, error) {
	if len(recipientPub) != 32 {
		return nil, fmt.Errorf("recipient public key must be 32 bytes, got %d", len(recipientPub))
	}
	curve := ecdh.X25519()
	eph, err := curve.GenerateKey(rand.Reader)
	if err != nil {
		return nil, fmt.Errorf("ephemeral key: %w", err)
	}
	rp, err := curve.NewPublicKey(recipientPub)
	if err != nil {
		return nil, fmt.Errorf("recipient key: %w", err)
	}
	shared, err := eph.ECDH(rp)
	if err != nil {
		return nil, fmt.Errorf("ecdh: %w", err)
	}
	ephPub := eph.PublicKey().Bytes()
	aead, err := derive(shared, ephPub, recipientPub)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write([]byte(Magic)); err != nil {
		return nil, err
	}
	if _, err := w.Write(ephPub); err != nil {
		return nil, err
	}
	return &Writer{w: w, aead: aead, buf: make([]byte, Chunk)}, nil
}

func (s *Writer) Write(p []byte) (int, error) {
	if s.done {
		return 0, errors.New("sealed writer closed")
	}
	total := 0
	for len(p) > 0 {
		c := copy(s.buf[s.n:], p)
		s.n += c
		p = p[c:]
		total += c
		if s.n == len(s.buf) {
			if err := s.flush(false); err != nil {
				return total, err
			}
		}
	}
	return total, nil
}

func (s *Writer) flush(final bool) error {
	nonce := make([]byte, s.aead.NonceSize())
	binary.BigEndian.PutUint64(nonce[:8], s.chunk)
	if final {
		nonce[11] = 1
	}
	ct := s.aead.Seal(nil, nonce, s.buf[:s.n], nil)
	var lenBuf [4]byte
	binary.BigEndian.PutUint32(lenBuf[:], uint32(len(ct)))
	if _, err := s.w.Write(lenBuf[:]); err != nil {
		return err
	}
	if _, err := s.w.Write(ct); err != nil {
		return err
	}
	s.chunk++
	s.n = 0
	return nil
}

// Close seals the final (possibly empty) chunk, the end-of-stream mark.
func (s *Writer) Close() error {
	if s.done {
		return nil
	}
	s.done = true
	return s.flush(true)
}

// Open reads a sealed stream with the recipient's private key (32 bytes) and returns the
// plaintext, refusing more than max bytes.
func Open(r io.Reader, recipientPriv []byte, max int) ([]byte, error) {
	priv, err := ecdh.X25519().NewPrivateKey(recipientPriv)
	if err != nil {
		return nil, fmt.Errorf("private key: %w", err)
	}
	br := bufio.NewReaderSize(r, 1<<16)
	head := make([]byte, len(Magic)+32)
	if _, err := io.ReadFull(br, head); err != nil {
		return nil, ErrNotSealed
	}
	if string(head[:len(Magic)]) != Magic {
		return nil, ErrNotSealed
	}
	ephPub, err := ecdh.X25519().NewPublicKey(head[len(Magic):])
	if err != nil {
		return nil, fmt.Errorf("ephemeral key: %w", err)
	}
	shared, err := priv.ECDH(ephPub)
	if err != nil {
		return nil, fmt.Errorf("ecdh: %w", err)
	}
	aead, err := derive(shared, ephPub.Bytes(), priv.PublicKey().Bytes())
	if err != nil {
		return nil, err
	}
	var out bytes.Buffer
	var chunk uint64
	nonce := make([]byte, aead.NonceSize())
	for {
		var lenBuf [4]byte
		if _, err := io.ReadFull(br, lenBuf[:]); err != nil {
			return nil, errors.New("sealed mail ends before its final chunk")
		}
		n := binary.BigEndian.Uint32(lenBuf[:])
		if n > Chunk+64 {
			return nil, errors.New("sealed chunk too large")
		}
		ct := make([]byte, n)
		if _, err := io.ReadFull(br, ct); err != nil {
			return nil, errors.New("sealed mail truncated")
		}
		for i := range nonce {
			nonce[i] = 0
		}
		binary.BigEndian.PutUint64(nonce[:8], chunk)
		pt, err := aead.Open(nil, nonce, ct, nil)
		final := false
		if err != nil {
			nonce[11] = 1
			pt, err = aead.Open(nil, nonce, ct, nil)
			if err != nil {
				return nil, errors.New("sealed mail does not open with this key")
			}
			final = true
		}
		out.Write(pt)
		if out.Len() > max {
			return nil, fmt.Errorf("sealed mail larger than %d bytes", max)
		}
		chunk++
		if final {
			return out.Bytes(), nil
		}
	}
}

// SealBytes is NewWriter + Write + Close for a message already in memory.
func SealBytes(plain, recipientPub []byte) ([]byte, error) {
	var b bytes.Buffer
	w, err := NewWriter(&b, recipientPub)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(plain); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return b.Bytes(), nil
}

// GenerateKey makes an X25519 keypair; both halves are 32 bytes.
func GenerateKey() (priv, pub []byte, err error) {
	k, err := ecdh.X25519().GenerateKey(rand.Reader)
	if err != nil {
		return nil, nil, err
	}
	return k.Bytes(), k.PublicKey().Bytes(), nil
}

// PublicOf is the public half of a 32-byte X25519 private key.
func PublicOf(priv []byte) ([]byte, error) {
	k, err := ecdh.X25519().NewPrivateKey(priv)
	if err != nil {
		return nil, err
	}
	return k.PublicKey().Bytes(), nil
}

// ReadKeyFile reads a 64-hex-char key file (either half), trimming whitespace.
func ReadKeyFile(path string) ([]byte, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	return ParseKey(string(b))
}

// ParseKey decodes 64 hex chars into 32 bytes.
func ParseKey(s string) ([]byte, error) {
	k, err := hex.DecodeString(strings.TrimSpace(s))
	if err != nil || len(k) != 32 {
		return nil, errors.New("key must be 64 hex chars")
	}
	return k, nil
}

// WriteKeyFile writes a key as 64 hex chars and a newline, mode 0600.
func WriteKeyFile(path string, key []byte) error {
	return os.WriteFile(path, []byte(hex.EncodeToString(key)+"\n"), 0o600)
}
