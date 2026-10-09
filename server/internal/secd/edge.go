package secd

// THE FRONT DOOR'S TLS, IN SECD. Until 30 Sep 2026 nginx terminated the phone's TLS, checked its
// device certificate, and passed it to secd as a header (X-Client-Cert) over plain HTTP on
// 127.0.0.1:8443. secd believed that header, so any process on the box (a website, a shell, an SSRF
// in either) could reach the PIN prompt without a phone and claim to be any device, retired ones
// included. Now nginx forwards the raw TLS stream by name (ssl_preread) and secd does the TLS itself:
//
//   - the certificate the phone pins (box-server.pem) is served here;
//   - a client certificate is ASKED for and checked in the handler, not in the handshake, so no
//     certificate, a forged one and a retired one all get the same 503 as a box that is down (a
//     handshake refusal would be a tell, which is why nginx had "ssl_verify_client optional");
//   - every error status a verified phone could see is folded into that one 503 page, the way
//     nginx's error_page did.
//
// ONE PORT, BOTH WAYS, FOR THE MOVE. The listener looks at each connection's first byte: 0x16 is a
// TLS handshake, anything else is plain HTTP from the old nginx site. An optional PROXY v1 line
// (nginx stream "proxy_protocol on", so the other sites on the box keep their clients' addresses)
// comes off first. Plain HTTP is served the old way until /etc/ghost/edge says "tls"; from then it
// gets the 503 and nothing else. So a redeploy changes nothing on its own, and the switch
// (ghost-ctl edge-passthrough) is one nginx reload plus one file, with no restart and no lock.
//
// THE DEVICE'S NAME DOES NOT CHANGE. Everything a phone owns on the box (its trail key, sync
// positions, notification cursor, the retired list) is filed under the SHA-256 of the header nginx
// sent: the certificate's PEM, escaped by nginx. secd rebuilds that string from the certificate the
// phone presents (nginxEscape), and while the old path still runs it records, for every certificate
// it sees, the name nginx gave it (devices/ids), so a phone keeps its name across the switch even if
// the rebuilt string were ever to differ. `ghost-cli ghost.secd edge` says whether they agreed.

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"encoding/hex"
	"encoding/pem"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

// downBody is the one page anything that is not an enrolled phone ever sees (nginx's @down, byte for
// byte).
const downBody = "<!doctype html><title>503 Service Unavailable</title>\n<h1>Service Unavailable</h1>\n"

// foldStatus is nginx's error_page list: each of these became the 503 page.
var foldStatus = map[int]bool{400: true, 401: true, 403: true, 404: true, 405: true, 408: true, 413: true,
	414: true, 431: true, 495: true, 496: true, 497: true, 500: true, 501: true, 502: true, 503: true,
	504: true, 507: true}

// writeDown answers with the 503 page, whatever the handler had set before.
func writeDown(w http.ResponseWriter) {
	h := w.Header()
	for k := range h {
		delete(h, k)
	}
	h.Set("Content-Type", "text/html")
	h.Set("Content-Length", fmt.Sprint(len(downBody)))
	w.WriteHeader(http.StatusServiceUnavailable)
	_, _ = w.Write([]byte(downBody))
}

// foldWriter turns every error status in foldStatus into the 503 page (proxy_intercept_errors).
type foldWriter struct {
	http.ResponseWriter
	wrote, folded bool
}

func (f *foldWriter) WriteHeader(code int) {
	if f.wrote {
		return
	}
	f.wrote = true
	if foldStatus[code] {
		f.folded = true
		writeDown(f.ResponseWriter)
		return
	}
	f.ResponseWriter.WriteHeader(code)
}

func (f *foldWriter) Write(b []byte) (int, error) {
	if !f.wrote {
		f.WriteHeader(http.StatusOK)
	}
	if f.folded {
		return len(b), nil
	}
	return f.ResponseWriter.Write(b)
}

func (f *foldWriter) Flush() {
	if f.folded {
		return
	}
	if fl, ok := f.ResponseWriter.(http.Flusher); ok {
		fl.Flush()
	}
}

// Unwrap lets http.ResponseController reach the connection (upload read deadlines).
func (f *foldWriter) Unwrap() http.ResponseWriter { return f.ResponseWriter }

// nginxEscape is nginx's $ssl_client_escaped_cert escaping (ngx_escape_uri, URI component): every
// byte but A-Z a-z 0-9 - . _ ~ as %XX, upper-case hex.
func nginxEscape(s string) string {
	const hexd = "0123456789ABCDEF"
	var b strings.Builder
	b.Grow(len(s) * 3 / 2)
	for i := 0; i < len(s); i++ {
		c := s[i]
		if 'A' <= c && c <= 'Z' || 'a' <= c && c <= 'z' || '0' <= c && c <= '9' || c == '-' || c == '.' || c == '_' || c == '~' {
			b.WriteByte(c)
			continue
		}
		b.WriteByte('%')
		b.WriteByte(hexd[c>>4])
		b.WriteByte(hexd[c&15])
	}
	return b.String()
}

// headerFor is the header nginx would have sent for this certificate (OpenSSL's PEM: 64-character
// lines, a newline after each, the same as encoding/pem).
func headerFor(der []byte) string {
	return nginxEscape(string(pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})))
}

func sha256Hex(s string) string {
	sum := sha256.Sum256([]byte(s))
	return hex.EncodeToString(sum[:])
}

type certIDKey struct{}

// --- the names nginx gave each certificate (devices/ids) ---

type edgeState struct {
	mu        sync.Mutex
	loaded    bool
	ids       map[string]string // DER SHA-256 -> the certID nginx's header gave it
	agreed    int               // learned certificates whose rebuilt header matched nginx's
	differed  int               // ... and those whose did not
	seen      map[string]bool   // headers already learned this run
	modeAt    time.Time
	strict    bool
	tlsCfg    *tls.Config
	pool      *x509.CertPool
	tlsErr    error
	tlsLoaded bool
}

func (s *Server) idsPath() string { return filepath.Join(s.cfg.StateDir, "devices", "ids") }

func (s *Server) edgeFile() string {
	if s.cfg.EdgeFile != "" {
		return s.cfg.EdgeFile
	}
	return "/etc/ghost/edge"
}

// edgeStrict: /etc/ghost/edge says "tls", so plain HTTP is no longer the old nginx path. Read at
// most every five seconds.
func (s *Server) edgeStrict() bool {
	e := &s.edge
	e.mu.Lock()
	defer e.mu.Unlock()
	if time.Since(e.modeAt) < 5*time.Second && !e.modeAt.IsZero() {
		return e.strict
	}
	b, _ := os.ReadFile(s.edgeFile())
	e.strict = strings.TrimSpace(string(b)) == "tls"
	e.modeAt = time.Now()
	return e.strict
}

func (e *edgeState) loadIDs(path string) {
	if e.loaded {
		return
	}
	e.loaded = true
	e.ids = map[string]string{}
	e.seen = map[string]bool{}
	f, err := os.Open(path)
	if err != nil {
		return
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		p := strings.Fields(sc.Text())
		if len(p) >= 2 && len(p[0]) == 64 && len(p[1]) == 64 {
			e.ids[p[0]] = p[1]
			if len(p) >= 3 && p[2] == "agreed" {
				e.agreed++
			} else if len(p) >= 3 {
				e.differed++
			}
		}
	}
}

// learn records the name nginx gave a certificate (plain HTTP from the old site only).
func (s *Server) learn(hdr string) {
	e := &s.edge
	e.mu.Lock()
	defer e.mu.Unlock()
	e.loadIDs(s.idsPath())
	if e.seen[hdr] {
		return
	}
	e.seen[hdr] = true
	unesc, err := url.QueryUnescape(hdr)
	if err != nil {
		return
	}
	blk, _ := pem.Decode([]byte(unesc))
	if blk == nil {
		return
	}
	d := derID(blk.Bytes)
	if _, ok := e.ids[d]; ok {
		return
	}
	id := sha256Hex(hdr)
	verdict := "agreed"
	if headerFor(blk.Bytes) != hdr {
		verdict = "differed"
		secdLog.Warn("the header rebuilt from a device certificate differs from nginx's; its name is kept from nginx's", "fn", "learn")
	}
	if err := os.MkdirAll(filepath.Dir(s.idsPath()), 0o700); err != nil {
		return
	}
	f, err := os.OpenFile(s.idsPath(), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return
	}
	_, werr := fmt.Fprintf(f, "%s %s %s\n", d, id, verdict)
	if cerr := f.Close(); werr == nil && cerr == nil {
		e.ids[d] = id
		if verdict == "agreed" {
			e.agreed++
		} else {
			e.differed++
		}
	}
}

// idFor names a certificate presented over TLS: the name nginx gave it if secd saw it on the old
// path, else the name nginx would have given it.
func (s *Server) idFor(der []byte) string {
	e := &s.edge
	e.mu.Lock()
	e.loadIDs(s.idsPath())
	id, ok := e.ids[derID(der)]
	e.mu.Unlock()
	if ok {
		return id
	}
	return sha256Hex(headerFor(der))
}

// EdgeView is what `ghost-cli ghost.secd edge` prints.
type EdgeView struct {
	Mode      string `json:"mode"` // "tls" (plain HTTP refused) or "nginx-http" (the old path still served)
	TLS       bool   `json:"tls"`  // secd can serve TLS (the certificates are readable)
	TLSError  string `json:"tlsError,omitempty"`
	Learned   int    `json:"learned"`   // certificates seen on the old path
	Agreed    int    `json:"agreed"`    // ... whose rebuilt header matched nginx's
	Differed  int    `json:"differed"`  // ... whose did not (their names are kept from nginx's)
	ReadyToGo bool   `json:"readyToGo"` // TLS works and at least one phone was seen with a matching header
}

// Edge reports the front door's state for the control socket.
func (s *Server) Edge() EdgeView {
	_, _, terr := s.edgeTLS()
	strict := s.edgeStrict()
	e := &s.edge
	e.mu.Lock()
	e.loadIDs(s.idsPath())
	v := EdgeView{Mode: "nginx-http", TLS: terr == nil, Learned: len(e.ids), Agreed: e.agreed, Differed: e.differed}
	e.mu.Unlock()
	if strict {
		v.Mode = "tls"
	}
	if terr != nil {
		v.TLSError = terr.Error()
	}
	v.ReadyToGo = v.TLS && v.Agreed > 0
	return v
}

// --- TLS ---

// edgeTLS loads the box's server certificate and the device CA once.
func (s *Server) edgeTLS() (*tls.Config, *x509.CertPool, error) {
	e := &s.edge
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.tlsLoaded {
		return e.tlsCfg, e.pool, e.tlsErr
	}
	e.tlsLoaded = true
	dir := s.caDir()
	cert, err := tls.LoadX509KeyPair(filepath.Join(dir, "box-server.pem"), filepath.Join(dir, "box-server-key.pem"))
	if err != nil {
		e.tlsErr = fmt.Errorf("server certificate: %w", err)
		return nil, nil, e.tlsErr
	}
	caPEM, err := os.ReadFile(filepath.Join(dir, "devices-ca.pem"))
	if err != nil {
		e.tlsErr = fmt.Errorf("device CA: %w", err)
		return nil, nil, e.tlsErr
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caPEM) {
		e.tlsErr = errors.New("device CA: no certificate in devices-ca.pem")
		return nil, nil, e.tlsErr
	}
	e.pool = pool
	e.tlsCfg = &tls.Config{
		Certificates: []tls.Certificate{cert},
		// asked for, never required and never checked here: the handler checks it, so a missing or
		// bad certificate is answered like a down box instead of failing the handshake
		ClientAuth: tls.RequestClientCert,
		ClientCAs:  pool, // names the CA in the request, as nginx's ssl_client_certificate did
		MinVersion: tls.VersionTLS12,
		NextProtos: []string{"http/1.1"},
	}
	return e.tlsCfg, e.pool, nil
}

// verifiedPeer checks the certificate a TLS client presented against the device CA.
func (s *Server) verifiedPeer(st *tls.ConnectionState) ([]byte, bool) {
	if st == nil || len(st.PeerCertificates) == 0 {
		return nil, false
	}
	_, pool, err := s.edgeTLS()
	if err != nil {
		return nil, false
	}
	leaf := st.PeerCertificates[0]
	inter := x509.NewCertPool()
	for _, c := range st.PeerCertificates[1:] {
		inter.AddCert(c)
	}
	if _, err := leaf.Verify(x509.VerifyOptions{Roots: pool, Intermediates: inter,
		KeyUsages: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}); err != nil {
		return nil, false
	}
	return leaf.Raw, true
}

// front is the door every request goes through before the routes.
func (s *Server) front(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.TLS != nil {
			// the phone's own TLS: only the certificate it proved it holds names it
			r.Header.Del("X-Client-Cert")
			der, ok := s.verifiedPeer(r.TLS)
			if !ok {
				writeDown(w)
				return
			}
			id := s.idFor(der)
			if s.retired.has(id) {
				writeDown(w)
				return
			}
			r.Header.Set("X-Client-Cert", headerFor(der))
			r = r.WithContext(context.WithValue(r.Context(), certIDKey{}, id))
			if !s.admit(w, r) {
				return
			}
			s.noteVerifiedDevice()
			next.ServeHTTP(&foldWriter{ResponseWriter: w}, r)
			return
		}
		// plain HTTP: the old nginx site, until the switch
		if s.edgeStrict() {
			writeDown(w)
			return
		}
		if s.retired.has(certID(r)) {
			s.appearsDown(w)
			return
		}
		if hdr := r.Header.Get("X-Client-Cert"); hdr != "" {
			// the dates, the device key's retirement, the move to the key's name: secd's own look,
			// not nginx's alone (devicekey.go)
			if !s.admit(w, r) {
				return
			}
			s.learn(hdr)
			s.noteVerifiedDevice()
		}
		next.ServeHTTP(w, r)
	})
}

// --- the listener: TLS or plain by the first byte, a PROXY line first when there is one ---

// Listener wraps the loopback listener so each connection is served as TLS or plain HTTP by its
// first byte. Without readable certificates it is the listener as given (plain only, as before).
func (s *Server) Listener(ln net.Listener) net.Listener {
	cfg, _, err := s.edgeTLS()
	if err != nil {
		secdLog.Warn("TLS not served (plain HTTP behind nginx only)", "fn", "Listener", "err", err)
		return ln
	}
	el := &edgeListener{inner: ln, cfg: cfg, conns: make(chan net.Conn), done: make(chan struct{})}
	go el.loop()
	return el
}

type edgeListener struct {
	inner net.Listener
	cfg   *tls.Config
	conns chan net.Conn
	done  chan struct{}
	once  sync.Once
	err   error
}

// sniffTimeout bounds how long a connection may take to say what it is (and to finish its TLS
// handshake: the read deadline set here stays until the server sets its own after the handshake).
var sniffTimeout = 15 * time.Second

func (l *edgeListener) loop() {
	for {
		c, err := l.inner.Accept()
		if err != nil {
			var ne net.Error
			if errors.As(err, &ne) && ne.Timeout() {
				continue
			}
			l.err = err
			l.Close()
			return
		}
		go l.classify(c)
	}
}

func (l *edgeListener) classify(c net.Conn) {
	_ = c.SetReadDeadline(time.Now().Add(sniffTimeout))
	br := bufio.NewReaderSize(c, 512)
	pc := &peekConn{Conn: c, r: br}
	if head, err := br.Peek(6); err == nil && string(head) == "PROXY " {
		line, err := br.ReadSlice('\n')
		if err != nil || len(line) > 108 {
			c.Close()
			return
		}
		pc.remote = proxySource(line)
	}
	first, err := br.Peek(1)
	if err != nil {
		c.Close()
		return
	}
	var out net.Conn = pc
	if first[0] == 0x16 {
		out = tls.Server(pc, l.cfg)
	}
	select {
	case l.conns <- out:
	case <-l.done:
		c.Close()
	}
}

func (l *edgeListener) Accept() (net.Conn, error) {
	select {
	case c := <-l.conns:
		return c, nil
	case <-l.done:
		if l.err != nil {
			return nil, l.err
		}
		return nil, net.ErrClosed
	}
}

func (l *edgeListener) Close() error {
	var err error
	l.once.Do(func() {
		close(l.done)
		err = l.inner.Close()
	})
	return err
}

func (l *edgeListener) Addr() net.Addr { return l.inner.Addr() }

// peekConn replays what the classifier read.
type peekConn struct {
	net.Conn
	r      *bufio.Reader
	remote net.Addr
}

func (p *peekConn) Read(b []byte) (int, error) { return p.r.Read(b) }

func (p *peekConn) RemoteAddr() net.Addr {
	if p.remote != nil {
		return p.remote
	}
	return p.Conn.RemoteAddr()
}

// proxySource reads the client's address from a PROXY v1 line ("PROXY TCP4 src dst sport dport").
func proxySource(line []byte) net.Addr {
	f := bytes.Fields(bytes.TrimSpace(line))
	if len(f) != 6 || (string(f[1]) != "TCP4" && string(f[1]) != "TCP6") {
		return nil
	}
	ip := net.ParseIP(string(f[2]))
	if ip == nil {
		return nil
	}
	var port int
	fmt.Sscan(string(f[4]), &port)
	return &net.TCPAddr{IP: ip, Port: port}
}
