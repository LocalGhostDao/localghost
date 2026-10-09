package secd

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/hex"
	"encoding/pem"
	"fmt"
	"io"
	"math/big"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// an edge the way setup leaves /etc/ghost/ca: the box CA, the server certificate the phone pins,
// the device CA bundle, and a phone's certificate from the QR
type testEdge struct {
	caDir, state string
	s            *Server
	phone        tls.Certificate
	phoneDER     []byte
	serverPool   *x509.CertPool
}

func newTestEdge(t *testing.T) *testEdge {
	t.Helper()
	caDir := t.TempDir()
	ca, caKey := testCA(t, caDir)
	caPEM, _ := os.ReadFile(filepath.Join(caDir, "box-ca.pem"))
	os.WriteFile(filepath.Join(caDir, "devices-ca.pem"), caPEM, 0o644)
	// the server certificate
	sk, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	stmpl := &x509.Certificate{SerialNumber: big.NewInt(7), Subject: pkix.Name{CommonName: "box.test"},
		DNSNames: []string{"box.test"}, IPAddresses: []net.IP{net.IPv4(127, 0, 0, 1)},
		NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().AddDate(1, 0, 0),
		KeyUsage: x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}}
	sder, err := x509.CreateCertificate(rand.Reader, stmpl, ca, &sk.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	skd, _ := x509.MarshalECPrivateKey(sk)
	os.WriteFile(filepath.Join(caDir, "box-server.pem"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: sder}), 0o644)
	os.WriteFile(filepath.Join(caDir, "box-server-key.pem"), pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: skd}), 0o600)
	// the phone
	pk, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	pder, err := issueDeviceCert(ca, caKey, "vlad-phone", &pk.PublicKey)
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	s, err := New(Config{StateDir: state, CaDir: caDir, EdgeFile: filepath.Join(state, "edge")})
	if err != nil {
		t.Fatal(err)
	}
	pool := x509.NewCertPool()
	pool.AddCert(ca)
	return &testEdge{caDir: caDir, state: state, s: s,
		phone: tls.Certificate{Certificate: [][]byte{pder}, PrivateKey: pk}, phoneDER: pder, serverPool: pool}
}

// another CA's certificate, valid in every way but the issuer
func strangerCert(t *testing.T) tls.Certificate {
	dir := t.TempDir()
	ca, caKey := testCA(t, dir)
	k, _ := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	der, _ := issueDeviceCert(ca, caKey, "vlad-phone", &k.PublicKey)
	return tls.Certificate{Certificate: [][]byte{der}, PrivateKey: k}
}

func (e *testEdge) serve(t *testing.T, h http.Handler) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	srv := &http.Server{Handler: h, ReadHeaderTimeout: 5 * time.Second}
	go srv.Serve(e.s.Listener(ln))
	t.Cleanup(func() { srv.Close() })
	return ln.Addr().String()
}

func (e *testEdge) client(certs ...tls.Certificate) *http.Client {
	return &http.Client{Timeout: 5 * time.Second, Transport: &http.Transport{TLSClientConfig: &tls.Config{
		RootCAs: e.serverPool, ServerName: "box.test", Certificates: certs}}}
}

func get(t *testing.T, c *http.Client, u string, hdr map[string]string) (int, string) {
	t.Helper()
	req, _ := http.NewRequest("GET", u, nil)
	for k, v := range hdr {
		req.Header.Set(k, v)
	}
	resp, err := c.Do(req)
	if err != nil {
		t.Fatalf("GET %s: %v", u, err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp.StatusCode, string(b)
}

// The phone's TLS reaches secd itself; without the device CA's certificate nothing does, and a
// refusal looks like a box that is down, never like a failed handshake.
func TestEdgeServesThePhoneOverTLS(t *testing.T) {
	e := newTestEdge(t)
	addr := e.serve(t, e.s.Handler())

	code, body := get(t, e.client(e.phone), "https://"+addr+"/v1/health", nil)
	if code != 200 || !strings.Contains(body, `"ghost.secd"`) {
		t.Fatalf("the phone's certificate: %d %q", code, body)
	}
	for name, c := range map[string]*http.Client{
		"no certificate":           e.client(),
		"another CA's":             e.client(strangerCert(t)),
		"a forged header, no cert": e.client(),
	} {
		hdr := map[string]string{}
		if strings.Contains(name, "forged") {
			hdr["X-Client-Cert"] = headerFor(e.phoneDER)
		}
		code, body := get(t, c, "https://"+addr+"/v1/health", hdr)
		if code != 503 || body != downBody {
			t.Fatalf("%s: %d %q, want the 503 page", name, code, body)
		}
	}
	// a verified phone asking for nothing that exists: the 503 page too (nginx's error_page)
	code, body = get(t, e.client(e.phone), "https://"+addr+"/v1/no-such-thing", nil)
	if code != 503 || body != downBody {
		t.Fatalf("a 404 for the phone: %d %q", code, body)
	}
}

// Plain HTTP is the old nginx site: served, trusted and learned from until /etc/ghost/edge says
// tls, then refused like everything else.
func TestEdgePlainHTTPUntilTheSwitch(t *testing.T) {
	e := newTestEdge(t)
	var seen string
	capture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { seen = certID(r); w.Write([]byte("in")) })
	addr := e.serve(t, e.s.front(capture))
	nginxHdr := headerFor(e.phoneDER)

	code, body := get(t, http.DefaultClient, "http://"+addr+"/x", map[string]string{"X-Client-Cert": nginxHdr})
	if code != 200 || body != "in" || seen != sha256Hex(nginxHdr) {
		t.Fatalf("the old path: %d %q id %s", code, body, seen)
	}
	v := e.s.Edge()
	if v.Mode != "nginx-http" || !v.TLS || v.Learned != 1 || v.Agreed != 1 || !v.ReadyToGo {
		t.Fatalf("edge after one phone on the old path: %+v", v)
	}

	os.WriteFile(e.s.edgeFile(), []byte("tls\n"), 0o644)
	e.s.edge.modeAt = time.Time{} // read the file now, not in five seconds
	code, body = get(t, http.DefaultClient, "http://"+addr+"/x", map[string]string{"X-Client-Cert": nginxHdr})
	if code != 503 || body != downBody {
		t.Fatalf("plain HTTP after the switch: %d %q", code, body)
	}
	if e.s.Edge().Mode != "tls" {
		t.Fatal("the edge does not say tls")
	}
	// and the phone over TLS is the same device it was
	seen = ""
	code, _ = get(t, e.client(e.phone), "https://"+addr+"/x", nil)
	if code != 200 || seen != sha256Hex(nginxHdr) {
		t.Fatalf("the phone over TLS: %d id %s, want %s", code, seen, sha256Hex(nginxHdr))
	}
}

// A phone keeps the name nginx gave it even when the rebuilt header would differ, and a retired
// certificate stays retired over TLS.
func TestEdgeKeepsNamesAndRetirements(t *testing.T) {
	e := newTestEdge(t)
	var seen, dev string
	capture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen, dev = certID(r), deviceKeyFromRequest(r)
		w.Write([]byte("in"))
	})
	addr := e.serve(t, e.s.front(capture))
	// a header escaped some other way (query escaping: '+' for a space)
	odd := url.QueryEscape(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: e.phoneDER})))
	if odd == headerFor(e.phoneDER) {
		t.Fatal("the test needs a header that differs")
	}
	get(t, http.DefaultClient, "http://"+addr+"/x", map[string]string{"X-Client-Cert": odd})
	if v := e.s.Edge(); v.Learned != 1 || v.Differed != 1 || v.ReadyToGo {
		t.Fatalf("a differing header: %+v", v)
	}
	seen = ""
	get(t, e.client(e.phone), "https://"+addr+"/x", nil)
	// the certificate keeps the name nginx gave it; the device key is the public key's (devicekey.go)
	pc, _ := x509.ParseCertificate(e.phoneDER)
	spkiSum := sha256.Sum256(pc.RawSubjectPublicKeyInfo)
	if seen != sha256Hex(odd) || dev != hex.EncodeToString(spkiSum[:8]) {
		t.Fatalf("over TLS the phone is %s/%s, want the name nginx gave it %s and the key's device key", seen, dev, sha256Hex(odd))
	}
	// the names survive a restart
	s2, _ := New(Config{StateDir: e.state, CaDir: e.caDir, EdgeFile: e.s.edgeFile()})
	if s2.idFor(e.phoneDER) != sha256Hex(odd) {
		t.Fatal("the learned name did not survive a restart")
	}
	// retired: the same answer as no certificate
	e.s.retired.add(sha256Hex(odd))
	code, body := get(t, e.client(e.phone), "https://"+addr+"/x", nil)
	if code != 503 || body != downBody {
		t.Fatalf("a retired certificate over TLS: %d %q", code, body)
	}
}

// nginx's stream module puts a PROXY line first so the other sites keep their clients' addresses;
// secd takes it off and serves the TLS behind it.
func TestEdgeTakesAProxyLine(t *testing.T) {
	e := newTestEdge(t)
	var remote string
	capture := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { remote = r.RemoteAddr; w.Write([]byte("in")) })
	addr := e.serve(t, e.s.front(capture))
	c, err := net.Dial("tcp", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close()
	fmt.Fprintf(c, "PROXY TCP4 203.0.113.9 127.0.0.1 51000 443\r\n")
	tc := tls.Client(c, &tls.Config{RootCAs: e.serverPool, ServerName: "box.test", Certificates: []tls.Certificate{e.phone}})
	fmt.Fprintf(tc, "GET /x HTTP/1.1\r\nHost: box.test\r\nConnection: close\r\n\r\n")
	resp, err := http.ReadResponse(bufio.NewReader(tc), nil)
	if err != nil {
		t.Fatal(err)
	}
	b, _ := io.ReadAll(resp.Body)
	if resp.StatusCode != 200 || string(b) != "in" || !strings.HasPrefix(remote, "203.0.113.9:") {
		t.Fatalf("behind a PROXY line: %d %q from %s", resp.StatusCode, b, remote)
	}
}

func TestNginxEscape(t *testing.T) {
	in := "-----BEGIN CERTIFICATE-----\nMIIB+/9a=\n-----END CERTIFICATE-----\n"
	want := "-----BEGIN%20CERTIFICATE-----%0AMIIB%2B%2F9a%3D%0A-----END%20CERTIFICATE-----%0A"
	if got := nginxEscape(in); got != want {
		t.Fatalf("got %s", got)
	}
}

func TestFoldWriter(t *testing.T) {
	for code, folded := range map[int]bool{404: true, 500: true, 502: true, 409: false, 202: false, 206: false} {
		rr := httptest.NewRecorder()
		fw := &foldWriter{ResponseWriter: rr}
		fw.Header().Set("Content-Type", "application/json")
		fw.WriteHeader(code)
		fw.Write([]byte(`{"why":"x"}`))
		if folded && (rr.Code != 503 || rr.Body.String() != downBody || rr.Header().Get("Content-Type") != "text/html") {
			t.Fatalf("%d: %d %q", code, rr.Code, rr.Body.String())
		}
		if !folded && (rr.Code != code || rr.Body.String() != `{"why":"x"}`) {
			t.Fatalf("%d passed as %d %q", code, rr.Code, rr.Body.String())
		}
	}
}
