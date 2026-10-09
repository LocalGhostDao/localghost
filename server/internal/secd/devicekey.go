package secd

// THE PHONE'S NAME IS ITS PUBLIC KEY. Until 10 October 2026 a phone was named by the hash of the
// certificate it presented (the first sixteen hex of certID), and everything on the box (its
// trail key, its sync positions, its name) was filed under that. A certificate renewed for the
// same key then made a new phone, and the renewal had to carry the data over, every day; the
// operator's `retire` by device key named a certificate that was gone by the next unlock. Now the
// name is the SHA-256 of the certificate's public key (SubjectPublicKeyInfo), first sixteen hex:
// the same across renewals, different only when the key itself is rotated (the QR's key for the
// phone's own, once). A phone filed under the old name is moved over once, the first time it is
// seen with the volume up (migrateDevice).
//
// The same parse gives the certificate's dates, and the front door checks them itself on every
// request rather than lean on nginx alone: the header nginx passes over 127.0.0.1 is trusted
// until the edge passthrough is set up, and a forged header must not carry an expired or not yet
// valid certificate past this point either.

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"path/filepath"
	"sync"
	"time"
)

// certFacts is what the front door needs from a certificate, parsed once per certificate.
type certFacts struct {
	dev       string // the device key: SHA-256 of the SPKI, first 16 hex
	notBefore int64
	notAfter  int64
}

var certFactsCache sync.Map // certID → certFacts

// factsFor parses the certificate a request presents, once per certificate id.
func factsFor(r *http.Request) (certFacts, bool) {
	id := certID(r)
	if id == "" {
		return certFacts{}, false
	}
	if v, ok := certFactsCache.Load(id); ok {
		return v.(certFacts), true
	}
	c, err := clientCert(r)
	if err != nil {
		return certFacts{}, false
	}
	sum := sha256.Sum256(c.RawSubjectPublicKeyInfo)
	f := certFacts{dev: hex.EncodeToString(sum[:8]), notBefore: c.NotBefore.Unix(), notAfter: c.NotAfter.Unix()}
	certFactsCache.Store(id, f)
	return f, true
}

// certClockSlack is how far a certificate's NotBefore may sit in the future and still pass: the
// box backdates its own by an hour, so only a wrong clock on another box trips this.
const certClockSlack = 5 * time.Minute

// admit is the front door's own look at the certificate: within its dates, not a retired device
// key, and filed under its public key (moved from the certificate's hash once). False means the
// request was answered as if the box were down.
func (s *Server) admit(w http.ResponseWriter, r *http.Request) bool {
	f, ok := factsFor(r)
	if !ok {
		return true // no certificate (the edge takes care of that), or one that will not parse: nothing to judge here
	}
	now := time.Now().Unix()
	if now > f.notAfter || now+int64(certClockSlack/time.Second) < f.notBefore {
		s.appearsDown(w)
		return false
	}
	if s.retired.has(f.dev) {
		s.appearsDown(w)
		return false
	}
	s.migrateDevice(certID(r)[:16], f.dev)
	return true
}

// migrateDevice moves what the box filed under a phone's old name (the certificate's hash) to
// its new one (the public key's), once per process with the volume up. Nothing filed under the
// old name makes it a no-op; the volume locked leaves it for a later request.
func (s *Server) migrateDevice(legacy, dev string) {
	if legacy == "" || dev == "" || legacy == dev {
		return
	}
	if _, done := s.migrated.Load(dev); done {
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		return
	}
	s.migrated.Store(dev, true)
	mount := filepath.Join(s.cfg.StateDir, "mnt", fmt.Sprintf("slot%d", mounted))
	if err := moveTrailKey(mount, legacy, dev); err != nil {
		secdLog.Warn("device name: trail key not moved to the key's name", "fn", "migrateDevice", "err", err)
	}
	if s.notif != nil {
		if err := s.notif.MoveDevice(mounted, legacy, dev); err != nil {
			secdLog.Warn("device name: records not moved to the key's name", "fn", "migrateDevice", "err", err)
		}
	}
}
