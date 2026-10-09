package secd

import (
	"bytes"
	"crypto/ecdh"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/base64"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// a box CA on disk, the way setup writes it, and a device certificate "from the QR"
func testCA(t *testing.T, dir string) (*x509.Certificate, *ecdsa.PrivateKey) {
	t.Helper()
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "box CA"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0),
		IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	kder, _ := x509.MarshalECPrivateKey(key)
	os.WriteFile(filepath.Join(dir, "box-ca.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), 0o644)
	os.WriteFile(filepath.Join(dir, "box-ca-key.pem"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: kder}), 0o600)
	ca, _ := x509.ParseCertificate(der)
	return ca, key
}

// the header nginx sets for a verified client certificate
func escapedPEM(der []byte) string {
	return url.QueryEscape(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
}

func TestDeviceKeyRotation(t *testing.T) {
	caDir := t.TempDir()
	ca, caKey := testCA(t, caDir)
	qrKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	qrDER, err := issueDeviceCert(ca, caKey, "vlad-phone", &qrKey.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{StateDir: t.TempDir(), CaDir: caDir})
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.mounted = 0
	s.mu.Unlock()
	mount := filepath.Join(s.cfg.StateDir, "mnt", "slot0")
	os.MkdirAll(mount, 0o755)
	tok, _ := s.session.Issue()
	call := func(path, cert string, body []byte) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", path, bytes.NewReader(body))
		if path == "/v1/health" {
			req = httptest.NewRequest("GET", path, nil)
		}
		req.Header.Set("Authorization", "Bearer "+tok)
		if cert != "" {
			req.Header.Set("X-Client-Cert", cert)
		}
		s.Handler().ServeHTTP(rr, req)
		return rr
	}
	oldHdr := escapedPEM(qrDER)

	// the phone's trail key is filed under its current certificate
	tk, _ := ecdh.X25519().GenerateKey(rand.Reader)
	oldReq := httptest.NewRequest("GET", "/", nil)
	oldReq.Header.Set("X-Client-Cert", oldHdr)
	if err := saveTrailKey(mount, deviceKeyFromRequest(oldReq), tk); err != nil {
		t.Fatal(err)
	}

	// the phone's own key, and the proof it holds it
	phoneKey, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	spki, _ := x509.MarshalPKIXPublicKey(&phoneKey.PublicKey)
	h := sha256.Sum256(append([]byte(rekeyMessage), spki...))
	sig, _ := ecdsa.SignASN1(rand.Reader, phoneKey, h[:])
	enc := base64.StdEncoding.EncodeToString

	// a proof made with another key is refused
	other, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	badSig, _ := ecdsa.SignASN1(rand.Reader, other, h[:])
	bad, _ := json.Marshal(map[string]string{"spki": enc(spki), "sig": enc(badSig)})
	if rr := call("/v1/device/rekey", oldHdr, bad); rr.Code != 400 {
		t.Fatalf("a bad proof: %d", rr.Code)
	}
	// no client certificate, no rotation
	good, _ := json.Marshal(map[string]string{"spki": enc(spki), "sig": enc(sig)})
	if rr := call("/v1/device/rekey", "", good); rr.Code == 200 {
		t.Fatal("rotated without a client certificate")
	}

	rr := call("/v1/device/rekey", oldHdr, good)
	if rr.Code != 200 {
		t.Fatalf("rekey: %d %s", rr.Code, rr.Body)
	}
	var got struct {
		Cert string `json:"cert"`
	}
	json.Unmarshal(rr.Body.Bytes(), &got)
	blk, _ := pem.Decode([]byte(got.Cert))
	if blk == nil {
		t.Fatalf("no certificate: %s", rr.Body)
	}
	nc, err := x509.ParseCertificate(blk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	if err := nc.CheckSignatureFrom(ca); err != nil {
		t.Fatalf("not signed by the box CA: %v", err)
	}
	if !nc.PublicKey.(*ecdsa.PublicKey).Equal(&phoneKey.PublicKey) || nc.Subject.CommonName != "vlad-phone" {
		t.Fatal("the certificate is not for the phone's key, or lost its name")
	}
	// until the phone confirms, the QR's certificate still works
	if rr := call("/v1/health", oldHdr, nil); rr.Code != 200 {
		t.Fatalf("old certificate before confirm: %d", rr.Code)
	}
	newHdr := escapedPEM(blk.Bytes)
	if rr := call("/v1/device/rekey/confirm", newHdr, nil); rr.Code != 200 || strings.Contains(rr.Body.String(), "already") {
		t.Fatalf("confirm: %d %s", rr.Code, rr.Body)
	}
	if rr := call("/v1/health", oldHdr, nil); rr.Code == 200 {
		t.Fatal("the retired certificate still reaches the box")
	}
	if rr := call("/v1/unlock", oldHdr, []byte(`{"pin":"123456"}`)); rr.Code == 200 || rr.Code == 202 {
		t.Fatal("the retired certificate can still try PINs")
	}
	if rr := call("/v1/health", newHdr, nil); rr.Code != 200 {
		t.Fatalf("new certificate: %d", rr.Code)
	}
	// the trail key followed the phone
	newReq := httptest.NewRequest("GET", "/", nil)
	newReq.Header.Set("X-Client-Cert", newHdr)
	if _, err := loadTrailKey(mount, deviceKeyFromRequest(newReq)); err != nil {
		t.Fatalf("trail key did not follow: %v", err)
	}
	// a second confirm (a lost answer) is fine
	if rr := call("/v1/device/rekey/confirm", newHdr, nil); rr.Code != 200 {
		t.Fatalf("second confirm: %d", rr.Code)
	}
	// retired survives a restart of secd, and the file holds fingerprints only
	s2, _ := New(Config{StateDir: s.cfg.StateDir, CaDir: caDir})
	rr2 := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/health", nil)
	req.Header.Set("X-Client-Cert", oldHdr)
	s2.Handler().ServeHTTP(rr2, req)
	if rr2.Code == 200 {
		t.Fatal("retired forgotten across a restart")
	}
	b, _ := os.ReadFile(filepath.Join(s.cfg.StateDir, "devices", "retired"))
	if fi, _ := os.Stat(filepath.Join(s.cfg.StateDir, "devices", "retired")); fi.Mode().Perm() != 0o600 || strings.Contains(string(b), "BEGIN") {
		t.Fatalf("retired file: %v %q", fi.Mode(), b)
	}
	// the retired line carries the old certificate's expiry (two weeks from its issue), and the
	// issued certificate is good for two weeks, not ten years
	if f := strings.Fields(strings.TrimSpace(string(b))); len(f) != 2 || f[1] == "" {
		t.Fatalf("retired line without an expiry: %q", b)
	}
	if c, _ := x509.ParseCertificate(qrDER); c.NotAfter.Sub(time.Now()) > 15*24*time.Hour || c.NotAfter.Sub(time.Now()) < 13*24*time.Hour {
		t.Fatalf("a device certificate lasts two weeks, not until %v", c.NotAfter)
	}

	// THE DAILY RENEWAL: the same key, a new certificate, the proof bound to the certificate
	// presented (v2); the device key stays, so nothing on the box moves, and the trail key is
	// where it was
	devBefore := deviceKeyFromRequest(newReq)
	v2 := sha256.Sum256(append([]byte(rekeyMessageV2+derID(blk.Bytes)+"\n"), spki...))
	sig2, _ := ecdsa.SignASN1(rand.Reader, phoneKey, v2[:])
	renew, _ := json.Marshal(map[string]string{"spki": enc(spki), "sig": enc(sig2)})
	// a v2 proof bound to another certificate (the QR's) is no proof over this one
	wrong := sha256.Sum256(append([]byte(rekeyMessageV2+derID(qrDER)+"\n"), spki...))
	sigW, _ := ecdsa.SignASN1(rand.Reader, phoneKey, wrong[:])
	badBind, _ := json.Marshal(map[string]string{"spki": enc(spki), "sig": enc(sigW)})
	if rr := call("/v1/device/rekey", newHdr, badBind); rr.Code != 400 {
		t.Fatalf("a proof bound to another certificate: %d", rr.Code)
	}
	rr = call("/v1/device/rekey", newHdr, renew)
	if rr.Code != 200 {
		t.Fatalf("renew: %d %s", rr.Code, rr.Body)
	}
	json.Unmarshal(rr.Body.Bytes(), &got)
	blk2, _ := pem.Decode([]byte(got.Cert))
	nc2, _ := x509.ParseCertificate(blk2.Bytes)
	if !nc2.PublicKey.(*ecdsa.PublicKey).Equal(&phoneKey.PublicKey) || nc2.SerialNumber.Cmp(nc.SerialNumber) == 0 {
		t.Fatal("the renewal is not a new certificate for the same key")
	}
	renewedHdr := escapedPEM(blk2.Bytes)
	if rr := call("/v1/device/rekey/confirm", renewedHdr, nil); rr.Code != 200 {
		t.Fatalf("confirm the renewal: %d %s", rr.Code, rr.Body)
	}
	if rr := call("/v1/health", newHdr, nil); rr.Code == 200 {
		t.Fatal("the certificate before the renewal still reaches the box")
	}
	renewedReq := httptest.NewRequest("GET", "/", nil)
	renewedReq.Header.Set("X-Client-Cert", renewedHdr)
	if dev := deviceKeyFromRequest(renewedReq); dev != devBefore {
		t.Fatalf("the device key changed with a renewal: %s → %s", devBefore, dev)
	}
	if _, err := loadTrailKey(mount, devBefore); err != nil {
		t.Fatalf("the trail key moved on a renewal: %v", err)
	}
}

// secd refuses an expired certificate itself, whatever nginx passed (the header is trusted until
// the edge passthrough); a not-yet-valid one too, past the clock slack.
func TestFrontDoorChecksTheDates(t *testing.T) {
	caDir := t.TempDir()
	ca, caKey := testCA(t, caDir)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	s, err := New(Config{StateDir: t.TempDir(), CaDir: caDir})
	if err != nil {
		t.Fatal(err)
	}
	issue := func(nb, na time.Time) string {
		sn, _ := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 64))
		tmpl := &x509.Certificate{SerialNumber: sn, Subject: pkix.Name{CommonName: "p"}, NotBefore: nb, NotAfter: na,
			KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
		der, err := x509.CreateCertificate(rand.Reader, tmpl, ca, &key.PublicKey, caKey)
		if err != nil {
			t.Fatal(err)
		}
		return escapedPEM(der)
	}
	call := func(hdr string) int {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", "/v1/health", nil)
		req.Header.Set("X-Client-Cert", hdr)
		s.Handler().ServeHTTP(rr, req)
		return rr.Code
	}
	now := time.Now()
	if c := call(issue(now.Add(-time.Hour), now.Add(time.Hour))); c != 200 {
		t.Fatalf("a valid certificate: %d", c)
	}
	if c := call(issue(now.Add(-48*time.Hour), now.Add(-time.Minute))); c == 200 {
		t.Fatal("an expired certificate reached the box")
	}
	if c := call(issue(now.Add(time.Hour), now.Add(2*time.Hour))); c == 200 {
		t.Fatal("a certificate from the future reached the box")
	}
	if c := call(issue(now.Add(2*time.Minute), now.Add(2*time.Hour))); c != 200 {
		t.Fatalf("two minutes of clock slack: %d", c)
	}
}

// The retired list forgets a certificate once it has expired (nginx refuses it by then), keeps
// the operator's retirements for good, and reads old one-column files.
func TestRetiredListForgetsTheExpired(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "retired")
	past := time.Now().Add(-time.Hour).Unix()
	future := time.Now().Add(time.Hour).Unix()
	old := strings.Repeat("a", 64)
	live := strings.Repeat("b", 64)
	forever := strings.Repeat("c", 16)
	os.WriteFile(path, []byte(old+" "+strconv.FormatInt(past, 10)+"\n"+live+" "+strconv.FormatInt(future, 10)+"\n"+forever+"\n"), 0o600)
	rc := &retiredCerts{path: path}
	if rc.has(old) {
		t.Fatal("an expired certificate is still retired")
	}
	if !rc.has(live) || !rc.has(forever) || !rc.has(strings.Repeat("c", 64)) {
		t.Fatal("a live retirement was forgotten")
	}
	b, _ := os.ReadFile(path)
	if strings.Contains(string(b), old) || !strings.Contains(string(b), live) || !strings.Contains(string(b), forever) {
		t.Fatalf("the file after the load: %q", b)
	}
	if err := rc.addUntil(strings.Repeat("d", 64), future); err != nil {
		t.Fatal(err)
	}
	rc2 := &retiredCerts{path: path}
	if !rc2.has(strings.Repeat("d", 64)) {
		t.Fatal("an added retirement did not read back")
	}
}

// A phone retired by its device key (the 16-hex name every listing shows) is refused on every
// route, the PIN entry included, while the box is locked; a bad key is refused; the list reads
// back after a restart.
func TestRetireByDeviceKey(t *testing.T) {
	caDir := t.TempDir()
	ca, caKey := testCA(t, caDir)
	key, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := issueDeviceCert(ca, caKey, "lent-phone", &key.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Config{StateDir: t.TempDir(), CaDir: caDir})
	if err != nil {
		t.Fatal(err)
	}
	hdr := escapedPEM(der)
	call := func(path string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("X-Client-Cert", hdr)
		s.Handler().ServeHTTP(rr, req)
		return rr
	}
	if rr := call("/v1/health"); rr.Code != 200 {
		t.Fatalf("before: %d", rr.Code)
	}
	req := httptest.NewRequest("GET", "/", nil)
	req.Header.Set("X-Client-Cert", hdr)
	devKey := deviceKey(req)
	if len(devKey) != 16 {
		t.Fatalf("device key %q", devKey)
	}
	for _, bad := range []string{"", "abc", "zz12345678901234", strings.Repeat("a", 20)} {
		if err := s.Retire(bad); err == nil {
			t.Fatalf("retired %q", bad)
		}
	}
	if err := s.Retire(strings.ToUpper(devKey)); err != nil {
		t.Fatal(err)
	}
	if rr := call("/v1/health"); rr.Code == 200 {
		t.Fatal("still reaches the box")
	}
	if v := s.Devices(); !v.Locked || v.Retired != 1 {
		t.Fatalf("devices view: %+v", v)
	}
	s2, _ := New(Config{StateDir: s.cfg.StateDir, CaDir: caDir})
	rr := httptest.NewRecorder()
	s2.Handler().ServeHTTP(rr, req)
	if rr.Code == 200 {
		t.Fatal("forgotten across a restart")
	}
	// another phone is untouched
	key2, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der2, _ := issueDeviceCert(ca, caKey, "my-phone", &key2.PublicKey)
	rr = httptest.NewRecorder()
	req2 := httptest.NewRequest("GET", "/v1/health", nil)
	req2.Header.Set("X-Client-Cert", escapedPEM(der2))
	s2.Handler().ServeHTTP(rr, req2)
	if rr.Code != 200 {
		t.Fatalf("the other phone: %d", rr.Code)
	}
}
