package secd

// The second audit's invariants on the front door in the HEADER shape (nginx terminates TLS and
// passes the certificate as X-Client-Cert over plain loopback HTTP; the edge flag file is
// absent), kept as regression tests (10 Nov 2026). Before the fixes: any non-empty header was let
// in; a PIN posted during a running unlock was compared with the running one outside the
// limiter and handed the run id; one wrong PIN from any device ended the owner's session and
// counted against one box-wide limiter. Everything runs in-process on 127.0.0.1.

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/json"
	"encoding/pem"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/profile"
)

// INVARIANT 1: on the plain (header) path, only a certificate the box CA issued names a device.
// The claim under test is the one in SECURITY.md and the unit brief ("secd verifies the
// certificate against the box CA").
func TestAuditHeaderPathChecksTheIssuer(t *testing.T) {
	e := newTestEdge(t)
	addr := e.serve(t, e.s.Handler())
	try := func(name, hdr string) int {
		code, _ := get(t, http.DefaultClient, "http://"+addr+"/v1/health", map[string]string{"X-Client-Cert": hdr})
		t.Logf("%-46s -> %d", name, code)
		return code
	}
	if c := try("the enrolled phone's certificate", headerFor(e.phoneDER)); c != 200 {
		t.Fatalf("control: the enrolled phone is refused (%d)", c)
	}
	stranger := strangerCert(t)
	if c := try("a certificate another CA issued", headerFor(stranger.Certificate[0])); c != 503 {
		t.Errorf("INVARIANT BROKEN: a certificate the box CA never issued passed the door (%d)", c)
	}
	// self-signed, no CA at all
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	tmpl := &x509.Certificate{SerialNumber: big.NewInt(9), Subject: pkix.Name{CommonName: "nobody"},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour)}
	self, _ := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &k.PublicKey, k)
	if c := try("a self-signed certificate", headerFor(self)); c != 503 {
		t.Errorf("INVARIANT BROKEN: a self-signed certificate passed the door (%d)", c)
	}
	if c := try("a header that is not a certificate", "not-a-certificate"); c != 503 {
		t.Errorf("INVARIANT BROKEN: a header that is no certificate passed the door (%d)", c)
	}
	// the dates ARE checked on this path (devicekey.go admit): the control that does hold
	old := &x509.Certificate{SerialNumber: big.NewInt(10), Subject: pkix.Name{CommonName: "nobody"},
		NotBefore: time.Now().Add(-48 * time.Hour), NotAfter: time.Now().Add(-24 * time.Hour)}
	expired, _ := x509.CreateCertificate(rand.Reader, old, old, &k.PublicKey, k)
	if c := try("an expired self-signed certificate", headerFor(expired)); c != 503 {
		t.Errorf("an expired certificate passed the door (%d)", c)
	}
	// and with the edge flag set, the plain path is shut whatever the header says
	writeEdgeFlag(t, e)
	if c := try("the enrolled phone, after the switch to tls", headerFor(e.phoneDER)); c != 503 {
		t.Errorf("plain HTTP still served after the switch (%d)", c)
	}
}

func writeEdgeFlag(t *testing.T, e *testEdge) {
	t.Helper()
	if err := os.WriteFile(e.s.edgeFile(), []byte("tls\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	e.s.edge.mu.Lock()
	e.s.edge.modeAt = time.Time{}
	e.s.edge.mu.Unlock()
}

// INVARIANT 2: past the door but without a session bearer, every route answers the down page
// except the ones the design names as pre-PIN (health, info, unlock, unlock/poll). The test walks
// the route table with GET and POST and lists what answers anything else.
func TestAuditRoutesWithoutBearer(t *testing.T) {
	e := newTestEdge(t)
	h := e.s.Handler()
	hdr := headerFor(e.phoneDER)
	// pre-PIN by design: health, info, the unlock itself; the model catalogue and bytes (public
	// weights from the unencrypted shared area, which an enrolled phone downloads in the
	// background) and the API's own description. All of them need a verified certificate.
	allowed := map[string]bool{"/v1/health": true, "/v1/info": true, "/v1/unlock": true, "/v1/unlock/poll": true,
		"/v1/models": true, "/v1/models/": true, "/v1/openapi.json": true}
	var open []string
	for _, rt := range e.s.routes() {
		path := rt.Path
		if path == "/v1/unlock" {
			continue // posting here starts a PIN attempt; covered by the next test
		}
		if strings.HasSuffix(path, "/") {
			path += "x"
		}
		for _, m := range []string{"GET", "POST", "HEAD", "OPTIONS", "PUT", "DELETE"} {
			rr := httptest.NewRecorder()
			req := httptest.NewRequest(m, path, strings.NewReader("{}"))
			req.Header.Set("X-Client-Cert", hdr)
			h.ServeHTTP(rr, req)
			down := rr.Code == 503 && (m == "HEAD" || rr.Body.String() == downBody) && rr.Result().Header.Get("Content-Type") == "text/html" && rr.Result().Header.Get("Content-Length") == "83"
			if !down {
				open = append(open, m+" "+rt.Path+" -> "+http.StatusText(rr.Code))
			}
		}
	}
	sort.Strings(open)
	for _, o := range open {
		t.Log("answers without a bearer: " + o)
	}
	for _, o := range open {
		p := strings.Fields(o)[1]
		if !allowed[p] {
			t.Errorf("INVARIANT BROKEN: %s answers something other than the down page with no session", o)
		}
	}
}

// secondDevice issues another certificate from the box's own CA: a second enrolled phone.
func secondDevice(t *testing.T, e *testEdge, name string) []byte {
	t.Helper()
	cb, _ := os.ReadFile(filepath.Join(e.caDir, "box-ca.pem"))
	kb, _ := os.ReadFile(filepath.Join(e.caDir, "box-ca-key.pem"))
	cblk, _ := pem.Decode(cb)
	kblk, _ := pem.Decode(kb)
	ca, err := x509.ParseCertificate(cblk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	caKey, err := x509.ParseECPrivateKey(kblk.Bytes)
	if err != nil {
		t.Fatal(err)
	}
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, err := issueDeviceCert(ca, caKey, name, &k.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	return der
}

// slowOpen is an UnlockBackend that takes a moment and opens for one PIN. It counts Resolve
// calls: Resolve is the only place the limiter runs.
type slowOpen struct {
	pin      string
	resolves *atomic.Int32
}

func (b slowOpen) Resolve(pin string) (int, bool, error) {
	b.resolves.Add(1)
	time.Sleep(300 * time.Millisecond)
	if pin == b.pin {
		return profile.MainSlot, false, nil
	}
	return profile.NoSlot, false, nil
}
func (slowOpen) Unseal(int, string) ([]byte, error)     { return []byte{1}, nil }
func (slowOpen) Mount(int, []byte) error                { return nil }
func (slowOpen) StartDB(int) error                      { return nil }
func (slowOpen) StartCache(int) error                   { return nil }
func (slowOpen) Warm(int) bool                          { return false }
func (slowOpen) Lock(int, func(profile.Progress)) error { return nil }
func (slowOpen) AuthorizesLock(string) bool             { return false }

func waitIdle(e *testEdge) {
	deadline := time.Now().Add(10 * time.Second)
	for {
		e.s.unlock.mu.Lock()
		running := e.s.unlock.running
		e.s.unlock.mu.Unlock()
		if !running || time.Now().After(deadline) {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// A PIN posted while another device's unlock runs is never compared with the running one: it
// waits, then goes through the limiter like any other, and is never handed the running
// unlock's run id (which collects the session token). The same device with the same PIN joins
// its own unlock, as a second tap does.
func TestSecondDeviceDuringARunningUnlock(t *testing.T) {
	old := rejectFloor
	rejectFloor = func() time.Duration { return 10 * time.Millisecond }
	defer func() { rejectFloor = old }()
	e := newTestEdge(t)
	var resolves atomic.Int32
	e.s.unlock.backend = slowOpen{pin: "owner-pin", resolves: &resolves}
	h := e.s.Handler()
	owner := headerFor(e.phoneDER)
	second := headerFor(secondDevice(t, e, "second-phone"))
	post := func(hdr, pin string) (int, map[string]any) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/unlock", strings.NewReader(`{"pin":"`+pin+`"}`))
		req.Header.Set("X-Client-Cert", hdr)
		h.ServeHTTP(rr, req)
		var body map[string]any
		_ = json.Unmarshal(rr.Body.Bytes(), &body)
		return rr.Code, body
	}
	code, body := post(owner, "owner-pin")
	ownerRun, _ := body["run"].(string)
	if code != 200 || ownerRun == "" {
		t.Fatalf("owner's unlock did not start: %d %v", code, body)
	}
	// the same device, the same PIN: joins, with the same run id (a second tap)
	if c, b := post(owner, "owner-pin"); c != 200 || b["run"] != ownerRun {
		t.Fatalf("the device's own second tap did not join: %d %v", c, b)
	}
	// the second device, the SAME PIN while the owner's unlock runs: waits, then runs as its own
	// unlock through the limiter, with a run id of its own
	c2, b2 := post(second, "owner-pin")
	run2, _ := b2["run"].(string)
	if c2 != 200 || run2 == "" || run2 == ownerRun {
		t.Fatalf("second device, same PIN: %d %v (owner's run %s)", c2, b2, ownerRun)
	}
	waitIdle(e)
	if n := resolves.Load(); n != 2 {
		t.Fatalf("the second device's PIN did not go through the limiter: %d resolves", n)
	}
	// a different PIN while a run is on: also waits, also goes through the limiter, and the two
	// answers do not differ in shape (a run id of its own either way)
	_, _ = post(owner, "owner-pin")
	c3, b3 := post(second, "some-other-pin")
	run3, _ := b3["run"].(string)
	if c3 != 200 || run3 == "" {
		t.Fatalf("second device, different PIN: %d %v", c3, b3)
	}
	waitIdle(e)
	if n := resolves.Load(); n != 4 {
		t.Fatalf("resolves: %d", n)
	}
	// a queue that outlasts its wait is answered like a down box
	oldWait := unlockQueueWait
	unlockQueueWait = 50 * time.Millisecond
	defer func() { unlockQueueWait = oldWait }()
	_, _ = post(owner, "owner-pin")
	if c, _ := post(second, "x"); c != 503 {
		t.Fatalf("an outwaited queue answered %d", c)
	}
	waitIdle(e)
}

// A wrong PIN ends the session of the device that typed it, not another device's, and the
// limiter counts per device: one phone's wrong PINs do not throttle the next.
func TestWrongPinIsTheDevicesOwn(t *testing.T) {
	old := rejectFloor
	rejectFloor = func() time.Duration { return 10 * time.Millisecond }
	defer func() { rejectFloor = old }()
	e := newTestEdge(t)
	h := e.s.Handler()
	owner := headerFor(e.phoneDER)
	otherDER := secondDevice(t, e, "second-phone")
	other := headerFor(otherDER)
	devOf := func(hdr string) string {
		req := httptest.NewRequest("GET", "/v1/health", nil)
		req.Header.Set("X-Client-Cert", hdr)
		return deviceOf(req)
	}
	ownerTok, _ := e.s.session.IssueTo(devOf(owner))
	post := func(hdr, pin string) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/unlock", strings.NewReader(`{"pin":"`+pin+`"}`))
		req.Header.Set("X-Client-Cert", hdr)
		h.ServeHTTP(rr, req)
		waitIdle(e)
	}
	post(other, "0000")
	if !e.s.session.Valid(ownerTok) {
		t.Fatal("a wrong PIN from another device ended the owner's session")
	}
	b, ok := e.s.unlock.backend.(*backend)
	if !ok {
		t.Skip("not the default backend")
	}
	for i := 0; i < 4; i++ {
		post(other, "0000")
	}
	// the wrong PINs stand against the device that typed them, and against no other. (Asked
	// of the count, not of "may it try now": that answer turns on how long ago the last
	// attempt was, and on 12 Nov 2026 a loaded machine took long enough for the wait to have
	// run out, which failed this test once without anything being wrong.)
	if n := b.accounts.Failures("dev:" + devOf(other)); n < 1 {
		t.Fatalf("five wrong PINs and %d stand against the device that typed them", n)
	}
	if n := b.accounts.Failures("dev:" + devOf(owner)); n != 0 {
		t.Fatalf("another device's wrong PINs stand against the owner's: %d", n)
	}
	if err := b.accounts.Allowed("dev:" + devOf(owner)); err != nil {
		t.Fatalf("another device's wrong PINs throttle the owner's: %v", err)
	}
	// the owner's own wrong PIN does end the owner's session
	post(owner, "0000")
	if e.s.session.Valid(ownerTok) {
		t.Fatal("a wrong PIN on the device that holds the session left it live")
	}
}
