package secd

// DEVICE KEYS, ROTATED. The enrolment QR carries a device certificate AND its private key: the box
// made the key, and anyone who photographed the QR holds a copy. After its first PIN unlock the phone
// makes a key of its own inside its secure element (AndroidKeyStore, never exportable), proves it
// holds it, and the box signs a new certificate for it with the box CA. The phone switches to the new
// certificate and confirms over it; the box then RETIRES the QR's certificate: every request that
// presents it is answered as if the box were down, the unlock and the health check included. A
// photographed QR is then worth nothing.
//
//   POST /v1/device/rekey          {"spki": base64 DER, "sig": base64 ECDSA-SHA256 over
//                                   "localghost rekey v2\n" + hex SHA-256 of the presented
//                                   certificate's DER + "\n" + the spki bytes; v1 signed the
//                                   message and the spki alone and is still read}
//                                  , a certificate for that key, same name as the one presented
//   POST /v1/device/rekey/confirm  over the NEW certificate: the old one retired, the phone's data
//                                  (its trail key, sync and notification positions) moved to the new
//
// The pending hand-over and the retired list live on the OS disk (<state>/devices), root's and 0600,
// so a retired certificate is refused even while the box is locked. They hold fingerprints only.
//
// THE SAME DANCE EVERY DAY. A certificate is good for two weeks (debian.DeviceCertLife), and a
// phone that unlocks asks for a new one once a day (its own new key each time): the rekey above,
// again. A phone in use never sees the end of its certificate; a phone left two weeks does, and
// nginx refuses its handshake, and the person scans a fresh QR. The retired list keeps each
// retired certificate's expiry beside its id and forgets it once it has passed, so a year of
// daily renewals leaves no trace but the one certificate in use.

import (
	"bufio"
	"crypto"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/setup/debian"
)

const rekeyMessage = "localghost rekey v1\n"

// rekeyMessageV2 binds the proof to the certificate the phone presents (its DER's SHA-256, hex,
// then a newline, then the spki): a proof captured from one phone is no proof from another.
const rekeyMessageV2 = "localghost rekey v2\n"

// defaultCaDir is where setup keeps the box CA (box-ca.pem, box-ca-key.pem).
const defaultCaDir = "/etc/ghost/ca"

// certID names the certificate a request presents: SHA-256 of the header nginx sets (the escaped
// PEM of the verified client certificate), hex. "" without one. Over secd's own TLS the front door
// (edge.go) names it and puts the name on the request.
func certID(r *http.Request) string {
	if id, ok := r.Context().Value(certIDKey{}).(string); ok {
		return id
	}
	c := r.Header.Get("X-Client-Cert")
	if c == "" {
		return ""
	}
	sum := sha256.Sum256([]byte(c))
	return hex.EncodeToString(sum[:])
}

// clientCert parses the certificate nginx verified and passed on.
func clientCert(r *http.Request) (*x509.Certificate, error) {
	raw := r.Header.Get("X-Client-Cert")
	if raw == "" {
		return nil, errors.New("no client certificate")
	}
	p, err := url.QueryUnescape(raw)
	if err != nil {
		return nil, err
	}
	b, _ := pem.Decode([]byte(p))
	if b == nil {
		return nil, errors.New("client certificate is not PEM")
	}
	return x509.ParseCertificate(b.Bytes)
}

// retiredCerts is the set of certificates the box no longer answers.
type retiredCerts struct {
	mu     sync.Mutex
	path   string
	loaded bool
	ids    map[string]int64 // id → when the certificate expires (0: never forgotten, the operator's retirements)
}

// load reads the list: one id a line, with the certificate's expiry after a space where the
// retirement came from a renewal. An entry whose certificate has expired is dropped (nginx
// refuses the certificate itself by then) and the file written again without it.
func (rc *retiredCerts) load() {
	if rc.loaded {
		return
	}
	rc.loaded = true
	rc.ids = map[string]int64{}
	f, err := os.Open(rc.path)
	if err != nil {
		return
	}
	sc := bufio.NewScanner(f)
	now := time.Now().Unix()
	dropped := false
	for sc.Scan() {
		fields := strings.Fields(sc.Text())
		if len(fields) == 0 {
			continue
		}
		id := fields[0]
		if len(id) != 64 && len(id) != 16 {
			continue
		}
		var exp int64
		if len(fields) > 1 {
			exp, _ = strconv.ParseInt(fields[1], 10, 64)
		}
		if exp > 0 && exp < now {
			dropped = true
			continue
		}
		rc.ids[id] = exp
	}
	f.Close()
	if dropped {
		rc.rewrite()
	}
}

// rewrite writes the list as it stands (after expired entries went).
func (rc *retiredCerts) rewrite() {
	var sb strings.Builder
	for id, exp := range rc.ids {
		sb.WriteString(id)
		if exp > 0 {
			sb.WriteString(" " + strconv.FormatInt(exp, 10))
		}
		sb.WriteString("\n")
	}
	tmp := rc.path + ".tmp"
	if err := os.WriteFile(tmp, []byte(sb.String()), 0o600); err == nil {
		_ = os.Rename(tmp, rc.path)
	}
}

// Retire refuses a phone from now on: its device key (16 hex, as `devices` lists them) or a
// certificate's full id goes on the retired list on the OS disk, so it holds while the box is
// locked too, and everything that certificate presents is answered as if the box were down, the
// PIN entry included. There is no un-retire: a phone that should be back is enrolled again with a
// fresh QR, and gets a new key and a new name. The operator's way (`ghost-cli ghost.secd retire
// id=…`) and the phone's (POST /v1/devices/retire, never itself) both land here.
func (s *Server) Retire(key string) error {
	key = strings.ToLower(strings.TrimSpace(key))
	if len(key) != 16 && len(key) != 64 {
		return errors.New("a device key is 16 hex characters (ghost-cli ghost.secd devices lists them), a certificate id 64")
	}
	for _, ch := range key {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return errors.New("a device key is hex")
		}
	}
	if err := s.retired.add(key); err != nil {
		return err
	}
	secdLog.Warn("device retired: every certificate it presents is refused from now on", "fn", "Retire", "device", key)
	return nil
}

// DevicesView is what `ghost-cli ghost.secd devices` prints: the enrolled phones with what each
// has done (when the volume is open; the cursors live there) and how many are retired.
type DevicesView struct {
	Locked  bool           `json:"locked"`
	Devices []hw.DeviceRow `json:"devices,omitempty"`
	Retired int            `json:"retired"`
	Note    string         `json:"note,omitempty"`
}

// Devices lists the enrolled phones for the control socket.
func (s *Server) Devices() DevicesView {
	s.retired.mu.Lock()
	s.retired.load()
	n := len(s.retired.ids)
	s.retired.mu.Unlock()
	v := DevicesView{Retired: n}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		v.Locked = true
		v.Note = "the volume is locked: the phones' records are on it; retire works regardless"
		return v
	}
	rows, err := s.notif.DevicesInfo(mounted)
	if err != nil {
		v.Note = "devices: " + err.Error()
		return v
	}
	v.Devices = rows
	return v
}

// has: the certificate's full id, or its device key (the first 16 of it, the name every listing
// shows and `retire` takes); a retired device key refuses every certificate that key names.
func (rc *retiredCerts) has(id string) bool {
	if id == "" {
		return false
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.load()
	_, ok := rc.ids[id]
	if !ok && len(id) > 16 {
		_, ok = rc.ids[id[:16]]
	}
	return ok
}

// add retires an id for good; addUntil retires a certificate until it expires anyway.
func (rc *retiredCerts) add(id string) error { return rc.addUntil(id, 0) }

func (rc *retiredCerts) addUntil(id string, expires int64) error {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	rc.load()
	if _, ok := rc.ids[id]; ok {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(rc.path), 0o700); err != nil {
		return err
	}
	f, err := os.OpenFile(rc.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return err
	}
	line := id
	if expires > 0 {
		line += " " + strconv.FormatInt(expires, 10)
	}
	_, werr := f.WriteString(line + "\n")
	serr := f.Sync()
	if cerr := f.Close(); werr == nil {
		werr = cerr
	}
	if werr != nil {
		return werr
	}
	if serr != nil {
		return serr
	}
	rc.ids[id] = expires
	return nil
}

// rekeyPending is one hand-over between the rekey and its confirmation, filed under the new
// certificate's DER hash ([derID]). The old certificate is named the way every request names the
// certificate it presents ([certID]), so the retired check needs no parsing.
type rekeyPending struct {
	OldID  string `json:"oldId"`  // certID of the certificate being replaced
	OldDev string `json:"oldDev"` // its device key (where the phone's data is filed)
	At     int64  `json:"at"`
	OldExp int64  `json:"oldExp,omitempty"` // when the old certificate expires: the retired list forgets it then
}

func (s *Server) devicesDir() string { return filepath.Join(s.cfg.StateDir, "devices") }

func (s *Server) caDir() string {
	if s.cfg.CaDir != "" {
		return s.cfg.CaDir
	}
	return defaultCaDir
}

// loadBoxCA reads the box CA (setup's internal/setup/debian/pki.go writes it).
func loadBoxCA(dir string) (*x509.Certificate, crypto.Signer, error) {
	cb, err := os.ReadFile(filepath.Join(dir, "box-ca.pem"))
	if err != nil {
		return nil, nil, err
	}
	kb, err := os.ReadFile(filepath.Join(dir, "box-ca-key.pem"))
	if err != nil {
		return nil, nil, err
	}
	cblock, _ := pem.Decode(cb)
	kblock, _ := pem.Decode(kb)
	if cblock == nil || kblock == nil {
		return nil, nil, errors.New("CA files are not PEM")
	}
	cert, err := x509.ParseCertificate(cblock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	if k, err := x509.ParseECPrivateKey(kblock.Bytes); err == nil {
		return cert, k, nil
	}
	k, err := x509.ParsePKCS8PrivateKey(kblock.Bytes)
	if err != nil {
		return nil, nil, err
	}
	signer, ok := k.(crypto.Signer)
	if !ok {
		return nil, nil, errors.New("CA key cannot sign")
	}
	return cert, signer, nil
}

// issueDeviceCert signs a client certificate for a key the phone made (the shape setup issues).
func issueDeviceCert(ca *x509.Certificate, caKey crypto.Signer, name string, pub *ecdsa.PublicKey) ([]byte, error) {
	sn, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return nil, err
	}
	tmpl := &x509.Certificate{
		SerialNumber: sn,
		Subject:      pkix.Name{CommonName: name},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(debian.DeviceCertLife),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth},
	}
	return x509.CreateCertificate(rand.Reader, tmpl, ca, pub, caKey)
}

// verifyRekeyProof: the phone signed the message with the key it asks a certificate for, bound
// to the certificate it presents (v2) or not (v1, read for the apps before 10 October 2026).
func verifyRekeyProof(spkiB64, sigB64 string, presented []byte) (*ecdsa.PublicKey, error) {
	spki, err1 := base64.StdEncoding.DecodeString(spkiB64)
	sig, err2 := base64.StdEncoding.DecodeString(sigB64)
	if err1 != nil || err2 != nil || len(spki) == 0 || len(sig) == 0 {
		return nil, errors.New("spki and sig are base64")
	}
	k, err := x509.ParsePKIXPublicKey(spki)
	if err != nil {
		return nil, err
	}
	pub, ok := k.(*ecdsa.PublicKey)
	if !ok || pub.Curve != elliptic.P256() {
		return nil, errors.New("a device key is ECDSA P-256")
	}
	v2 := sha256.Sum256(append([]byte(rekeyMessageV2+derID(presented)+"\n"), spki...))
	if ecdsa.VerifyASN1(pub, v2[:], sig) {
		return pub, nil
	}
	v1 := sha256.Sum256(append([]byte(rekeyMessage), spki...))
	if ecdsa.VerifyASN1(pub, v1[:], sig) {
		return pub, nil
	}
	return nil, errors.New("the proof does not verify")
}

// handleRekey , POST /v1/device/rekey , see the top of this file.
func (s *Server) handleRekey(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	old, err := clientCert(r)
	if err != nil {
		s.appearsDown(w) // only a phone that came through the verified edge rotates
		return
	}
	var req struct {
		SPKI string `json:"spki"`
		Sig  string `json:"sig"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req) != nil {
		s.appearsDown(w)
		return
	}
	pub, err := verifyRekeyProof(req.SPKI, req.Sig, old.Raw)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	ca, caKey, err := loadBoxCA(s.caDir())
	if err != nil {
		secdLog.Warn("device rekey: no box CA", "fn", "handleRekey", "dir", s.caDir(), "err", err)
		s.appearsDown(w)
		return
	}
	der, err := issueDeviceCert(ca, caKey, old.Subject.CommonName, pub)
	if err != nil {
		secdLog.Warn("device rekey: sign failed", "fn", "handleRekey", "err", err)
		s.appearsDown(w)
		return
	}
	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	newID := derID(der)
	p, _ := json.Marshal(rekeyPending{OldID: certID(r), OldDev: deviceKeyFromRequest(r), At: time.Now().Unix(), OldExp: old.NotAfter.Unix()})
	dir := filepath.Join(s.devicesDir(), "pending")
	if err := os.MkdirAll(dir, 0o700); err == nil {
		err = os.WriteFile(filepath.Join(dir, newID+".json"), p, 0o600)
	}
	if err != nil {
		secdLog.Warn("device rekey: pending not kept", "fn", "handleRekey", "err", err)
		s.appearsDown(w)
		return
	}
	prunePending(dir, 7*24*time.Hour)
	secdLog.Info("device key rotated: new certificate issued, the old one retires when the phone confirms", "fn", "handleRekey",
		"device", deviceKeyFromRequest(r), "goodFor", debian.DeviceCertLife.String())
	writeJSON(w, map[string]any{"ok": true, "cert": string(certPEM), "lifeDays": int(debian.DeviceCertLife.Hours() / 24), "renewAfterHours": int(debian.DeviceCertRenewAfter.Hours())})
}

// handleRekeyConfirm , POST /v1/device/rekey/confirm , over the new certificate.
func (s *Server) handleRekeyConfirm(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	cur, err := clientCert(r)
	if err != nil {
		s.appearsDown(w)
		return
	}
	id := certID(r)
	path := filepath.Join(s.devicesDir(), "pending", derID(cur.Raw)+".json")
	b, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		writeJSON(w, map[string]any{"ok": true, "already": true}) // confirmed before (a lost answer)
		return
	}
	var p rekeyPending
	if err != nil || json.Unmarshal(b, &p) != nil || len(p.OldID) != 64 {
		s.appearsDown(w)
		return
	}
	if p.OldID == id {
		s.appearsDown(w)
		return
	}
	// the old certificate is refused from now until it expires anyway, then forgotten
	if err := s.retired.addUntil(p.OldID, p.OldExp); err != nil {
		secdLog.Warn("device rekey: old certificate not retired", "fn", "handleRekeyConfirm", "err", err)
		s.appearsDown(w)
		return
	}
	newDev := deviceKeyFromRequest(r)
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted >= 0 && p.OldDev != "" && newDev != "" {
		mount := filepath.Join(s.cfg.StateDir, "mnt", fmt.Sprintf("slot%d", mounted))
		if err := moveTrailKey(mount, p.OldDev, newDev); err != nil {
			secdLog.Warn("device rekey: trail key not moved", "fn", "handleRekeyConfirm", "err", err)
		}
		if s.notif != nil {
			if err := s.notif.MoveDevice(mounted, p.OldDev, newDev); err != nil {
				secdLog.Warn("device rekey: positions not moved", "fn", "handleRekeyConfirm", "err", err)
			}
		}
	}
	_ = os.Remove(path)
	secdLog.Info("device key rotated: the QR's certificate is retired", "fn", "handleRekeyConfirm", "device", newDev)
	writeJSON(w, map[string]any{"ok": true})
}

// derID names a certificate by its DER (the pending hand-over is filed under the new one's: the
// box issued it, and at the confirmation it arrives parsed from nginx's header).
func derID(der []byte) string {
	sum := sha256.Sum256(der)
	return hex.EncodeToString(sum[:])
}

// prunePending drops hand-overs never confirmed (the phone died between the two calls and made
// another key since).
func prunePending(dir string, older time.Duration) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	for _, e := range ents {
		if fi, err := e.Info(); err == nil && time.Since(fi.ModTime()) > older {
			_ = os.Remove(filepath.Join(dir, e.Name()))
		}
	}
}
