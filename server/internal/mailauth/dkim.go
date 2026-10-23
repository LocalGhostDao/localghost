package mailauth

import (
	"bytes"
	"context"
	"crypto"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// DKIMResult is one signature's verdict.
type DKIMResult struct {
	Domain   string `json:"domain"`
	Selector string `json:"selector"`
	Algo     string `json:"algo"`
	// Result: pass, fail, permerror (the signature or key is malformed), temperror (DNS failed),
	// neutral (a key record says it is for testing only).
	Result string `json:"result"`
	Reason string `json:"reason,omitempty"`
	// Headers lists the signed header names, so a reader can see Subject or Date were not covered.
	Headers []string `json:"headers,omitempty"`
}

// Pass is true for a passing signature.
func (r DKIMResult) Pass() bool { return r.Result == "pass" }

// header is one raw header field, name and the original text (with its CRLF folding).
type header struct {
	name string // as written
	raw  string // "Name: value\r\n ...continued\r\n"
}

// message splits a raw RFC 5322 message into header fields and body, normalising bare LF to
// CRLF, which is what canonicalisation and hashing need. The header block is bounded (see
// maxHeaderBytes and maxHeaderLines): a sender who folds one field into millions of lines
// gets a clipped block and a screen reason, not the box's mail goroutine for an hour.
type message struct {
	headers []header
	body    []byte
	// bounded is the CRLF message with the header block clipped to the caps; clipped says
	// whether anything was cut.
	bounded []byte
	clipped bool
}

// The header block a box will read: 128 KB and 2000 lines is far above any real message
// (Postfix stops at 100 KB) and far below what makes the parse slow.
const (
	maxHeaderBytes = 128 << 10
	maxHeaderLines = 2000
)

// clipHeaderBlock cuts a CRLF header block at the caps, on a line boundary, so what remains
// still parses. The second value says whether it cut.
func clipHeaderBlock(hdr []byte) ([]byte, bool) {
	clipped := false
	if len(hdr) > maxHeaderBytes {
		cut := bytes.LastIndex(hdr[:maxHeaderBytes], []byte("\r\n"))
		if cut < 0 {
			cut = 0
		} else {
			cut += 2
		}
		hdr = hdr[:cut]
		clipped = true
	}
	lines := 0
	for i := 0; i < len(hdr); i++ {
		if hdr[i] == '\n' {
			lines++
			if lines == maxHeaderLines && i+1 < len(hdr) {
				hdr = hdr[:i+1]
				clipped = true
				break
			}
		}
	}
	return hdr, clipped
}

func parseMessage(raw []byte) message {
	raw = toCRLF(raw)
	var m message
	i := bytes.Index(raw, []byte("\r\n\r\n"))
	var hdr []byte
	if i < 0 {
		hdr = raw
		m.body = nil
	} else {
		hdr = raw[:i+2]
		m.body = raw[i+4:]
	}
	hdr, m.clipped = clipHeaderBlock(hdr)
	if m.clipped {
		b := make([]byte, 0, len(hdr)+2+len(m.body))
		b = append(b, hdr...)
		b = append(b, '\r', '\n')
		m.bounded = append(b, m.body...)
	} else {
		m.bounded = raw
	}
	lines := bytes.SplitAfter(hdr, []byte("\r\n"))
	// each field is the slices of its lines, joined once at the end: linear in the block's
	// size however many continuation lines a field has
	type acc struct {
		name  string
		parts [][]byte
	}
	var accs []acc
	for _, ln := range lines {
		if len(ln) == 0 {
			continue
		}
		if (ln[0] == ' ' || ln[0] == '\t') && len(accs) > 0 {
			accs[len(accs)-1].parts = append(accs[len(accs)-1].parts, ln)
			continue
		}
		c := bytes.IndexByte(ln, ':')
		if c < 0 {
			// a line that is no header and no continuation: keep it attached to the last field
			// (a verifier must see what the signer saw) or drop it when there is none
			if len(accs) > 0 {
				accs[len(accs)-1].parts = append(accs[len(accs)-1].parts, ln)
			}
			continue
		}
		accs = append(accs, acc{name: string(ln[:c]), parts: [][]byte{ln}})
	}
	m.headers = make([]header, len(accs))
	for i, a := range accs {
		m.headers[i] = header{name: a.name, raw: string(bytes.Join(a.parts, nil))}
	}
	return m
}

func toCRLF(b []byte) []byte {
	if !bytes.Contains(b, []byte("\n")) {
		return b
	}
	out := make([]byte, 0, len(b)+len(b)/40)
	for i := 0; i < len(b); i++ {
		if b[i] == '\n' && (i == 0 || b[i-1] != '\r') {
			out = append(out, '\r', '\n')
			continue
		}
		out = append(out, b[i])
	}
	return out
}

// Canonicalisation (RFC 6376 section 3.4).

var wspRun = regexp.MustCompile(`[ \t]+`)

func relaxedHeader(h header) string {
	raw := strings.TrimSuffix(h.raw, "\r\n")
	c := strings.IndexByte(raw, ':')
	name := strings.ToLower(strings.TrimRight(raw[:c], " \t"))
	val := raw[c+1:]
	val = strings.ReplaceAll(val, "\r\n", "") // unfold
	val = wspRun.ReplaceAllString(val, " ")
	val = strings.Trim(val, " \t")
	return name + ":" + val + "\r\n"
}

func simpleHeader(h header) string {
	if strings.HasSuffix(h.raw, "\r\n") {
		return h.raw
	}
	return h.raw + "\r\n"
}

func canonHeader(c string, h header) string {
	if c == "relaxed" {
		return relaxedHeader(h)
	}
	return simpleHeader(h)
}

func canonBody(c string, body []byte) []byte {
	if c == "relaxed" {
		lines := bytes.Split(body, []byte("\r\n"))
		for i, ln := range lines {
			ln = bytes.TrimRight(ln, " \t")
			ln = wspRun.ReplaceAll(ln, []byte(" "))
			lines[i] = ln
		}
		out := bytes.Join(lines, []byte("\r\n"))
		if len(out) > 0 && !bytes.HasSuffix(out, []byte("\r\n")) {
			out = append(out, '\r', '\n')
		}
		for bytes.HasSuffix(out, []byte("\r\n\r\n")) {
			out = out[:len(out)-2]
		}
		if bytes.Equal(out, []byte("\r\n")) {
			return nil
		}
		return out
	}
	out := body
	if len(out) > 0 && !bytes.HasSuffix(out, []byte("\r\n")) {
		out = append(append([]byte{}, out...), '\r', '\n')
	}
	for bytes.HasSuffix(out, []byte("\r\n\r\n")) {
		out = out[:len(out)-2]
	}
	if len(out) == 0 {
		return []byte("\r\n")
	}
	return out
}

// tags parses a tag=value list (DKIM-Signature values and key records).
func tags(s string) map[string]string {
	m := map[string]string{}
	for _, part := range strings.Split(s, ";") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		eq := strings.IndexByte(part, '=')
		if eq < 0 {
			continue
		}
		k := strings.TrimSpace(part[:eq])
		v := strings.TrimSpace(part[eq+1:])
		if _, dup := m[k]; dup {
			continue
		}
		m[k] = v
	}
	return m
}

// stripB removes the value of the b= tag from a raw DKIM-Signature field, leaving "b=".
func stripB(raw string) string {
	c := strings.IndexByte(raw, ':')
	if c < 0 {
		return raw
	}
	val := raw[c+1:]
	// find "b=" as a tag: at the start (after FWS) or after ';' and FWS, not "bh="
	i := 0
	for {
		j := strings.Index(val[i:], "b=")
		if j < 0 {
			return raw
		}
		p := i + j
		before := strings.TrimRight(val[:p], " \t\r\n")
		if before == "" || strings.HasSuffix(before, ";") {
			end := strings.IndexByte(val[p:], ';')
			if end < 0 {
				return raw[:c+1] + val[:p+2]
			}
			return raw[:c+1] + val[:p+2] + val[p+end:]
		}
		i = p + 2
	}
}

func unfoldTagValue(v string) string {
	v = strings.ReplaceAll(v, "\r\n", "")
	return strings.Map(func(r rune) rune {
		if r == ' ' || r == '\t' {
			return -1
		}
		return r
	}, v)
}

// A message is signed by one or two domains; each signature costs a body hash and a DNS
// lookup, so the verifier stops after maxSignatures and names the rest in one permerror
// (RFC 6376 section 6.1 lets a verifier limit the signatures it checks). A signature's h= list
// is bounded too: each name is a scan of the headers.
const (
	maxSignatures  = 8
	maxSignedNames = 64
)

// VerifyDKIM checks the DKIM-Signature headers on the message, the first maxSignatures of
// them. now is when the message is judged (for x= expiry); a zero time means "do not check
// expiry".
func VerifyDKIM(ctx context.Context, res Resolver, raw []byte, now time.Time) []DKIMResult {
	m := parseMessage(raw)
	var out []DKIMResult
	extra := 0
	for idx, h := range m.headers {
		if !strings.EqualFold(strings.TrimSpace(h.name), "DKIM-Signature") {
			continue
		}
		if len(out) >= maxSignatures {
			extra++
			continue
		}
		if ctx.Err() != nil {
			out = append(out, DKIMResult{Result: "temperror", Reason: "judging ran out of time"})
			break
		}
		out = append(out, verifyOne(ctx, res, m, idx, now))
	}
	if extra > 0 {
		out = append(out, DKIMResult{Result: "permerror", Reason: fmt.Sprintf("%d more signatures than the %d a box checks", extra, maxSignatures)})
	}
	return out
}

func verifyOne(ctx context.Context, res Resolver, m message, idx int, now time.Time) DKIMResult {
	h := m.headers[idx]
	c := strings.IndexByte(h.raw, ':')
	t := tags(strings.ReplaceAll(h.raw[c+1:], "\r\n", ""))
	r := DKIMResult{Domain: lowerDomain(t["d"]), Selector: strings.ToLower(strings.TrimSpace(t["s"])), Algo: strings.ToLower(t["a"])}
	perm := func(reason string) DKIMResult {
		r.Result, r.Reason = "permerror", reason
		return r
	}
	if t["v"] != "1" {
		return perm("not a v=1 signature")
	}
	for _, k := range []string{"a", "b", "bh", "d", "h", "s"} {
		if t[k] == "" {
			return perm("missing " + k + "= tag")
		}
	}
	switch r.Algo {
	case "rsa-sha256", "ed25519-sha256":
	case "rsa-sha1":
		return perm("rsa-sha1 is no longer accepted")
	default:
		return perm("unknown algorithm " + r.Algo)
	}
	hc, bc := "simple", "simple"
	if cv := t["c"]; cv != "" {
		parts := strings.SplitN(cv, "/", 2)
		hc = parts[0]
		if len(parts) == 2 {
			bc = parts[1]
		}
	}
	if (hc != "simple" && hc != "relaxed") || (bc != "simple" && bc != "relaxed") {
		return perm("unknown canonicalisation " + t["c"])
	}
	hnames := strings.Split(unfoldTagValue(t["h"]), ":")
	if len(hnames) > maxSignedNames {
		return perm(fmt.Sprintf("h= names %d headers, more than the %d a box reads", len(hnames), maxSignedNames))
	}
	r.Headers = hnames
	hasFrom := false
	for _, n := range hnames {
		if strings.EqualFold(n, "from") {
			hasFrom = true
		}
	}
	if !hasFrom {
		return perm("From is not signed")
	}
	if i := t["i"]; i != "" {
		at := strings.LastIndexByte(i, '@')
		if at < 0 {
			return perm("i= is not an address")
		}
		id := lowerDomain(i[at+1:])
		if id != r.Domain && !strings.HasSuffix(id, "."+r.Domain) {
			return perm("i= domain is not under d=")
		}
	}
	if !now.IsZero() {
		if x := t["x"]; x != "" {
			if xs, err := strconv.ParseInt(x, 10, 64); err == nil && now.Unix() > xs {
				r.Result, r.Reason = "fail", "signature expired"
				return r
			}
		}
	}
	// body hash
	body := canonBody(bc, m.body)
	if l := t["l"]; l != "" {
		n, err := strconv.Atoi(l)
		if err != nil || n < 0 {
			return perm("bad l= tag")
		}
		if n < len(body) {
			body = body[:n]
		}
	}
	bh := sha256.Sum256(body)
	want, err := base64.StdEncoding.DecodeString(unfoldTagValue(t["bh"]))
	if err != nil {
		return perm("bh= is not base64")
	}
	if !bytes.Equal(bh[:], want) {
		r.Result, r.Reason = "fail", "body hash does not match (the body was changed in transit)"
		return r
	}
	// header hash input
	used := make([]bool, len(m.headers))
	used[idx] = true
	var in bytes.Buffer
	for _, n := range hnames {
		n = strings.TrimSpace(n)
		for j := len(m.headers) - 1; j >= 0; j-- {
			if used[j] || !strings.EqualFold(strings.TrimSpace(m.headers[j].name), n) {
				continue
			}
			used[j] = true
			in.WriteString(canonHeader(hc, m.headers[j]))
			break
		}
	}
	sigField := header{name: h.name, raw: stripB(h.raw)}
	in.WriteString(strings.TrimSuffix(canonHeader(hc, sigField), "\r\n"))
	sig, err := base64.StdEncoding.DecodeString(unfoldTagValue(t["b"]))
	if err != nil {
		return perm("b= is not base64")
	}
	// the key
	txts, err := res.LookupTXT(ctx, r.Selector+"._domainkey."+r.Domain)
	if err != nil {
		if isNotFound(err) {
			return perm("no key at " + r.Selector + "._domainkey." + r.Domain)
		}
		r.Result, r.Reason = "temperror", "key lookup failed"
		return r
	}
	var rec map[string]string
	for _, txt := range txts {
		kt := tags(txt)
		if v, ok := kt["v"]; ok && v != "DKIM1" {
			continue
		}
		if _, ok := kt["p"]; ok {
			rec = kt
			break
		}
	}
	if rec == nil {
		return perm("no DKIM key record found")
	}
	if rec["p"] == "" {
		r.Result, r.Reason = "fail", "key revoked"
		return r
	}
	if hs := rec["h"]; hs != "" {
		ok := false
		for _, a := range strings.Split(hs, ":") {
			if strings.TrimSpace(a) == "sha256" {
				ok = true
			}
		}
		if !ok {
			return perm("key does not allow sha256")
		}
	}
	pub, err := base64.StdEncoding.DecodeString(strings.Map(func(c rune) rune {
		if c == ' ' || c == '\t' {
			return -1
		}
		return c
	}, rec["p"]))
	if err != nil {
		return perm("key is not base64")
	}
	digest := sha256.Sum256(in.Bytes())
	keyType := strings.ToLower(rec["k"])
	if keyType == "" {
		keyType = "rsa"
	}
	switch {
	case r.Algo == "rsa-sha256" && keyType == "rsa":
		pk, err := x509.ParsePKIXPublicKey(pub)
		if err != nil {
			// some records carry a bare PKCS#1 key
			if pk1, e2 := x509.ParsePKCS1PublicKey(pub); e2 == nil {
				pk = pk1
			} else {
				return perm("key does not parse")
			}
		}
		rk, ok := pk.(*rsa.PublicKey)
		if !ok {
			return perm("key is not RSA")
		}
		if rk.N.BitLen() < 1024 {
			return perm("RSA key shorter than 1024 bits")
		}
		if err := rsa.VerifyPKCS1v15(rk, crypto.SHA256, digest[:], sig); err != nil {
			r.Result, r.Reason = "fail", "signature does not verify (headers changed or not the signer's key)"
			return r
		}
	case r.Algo == "ed25519-sha256" && keyType == "ed25519":
		if len(pub) != ed25519.PublicKeySize {
			return perm("Ed25519 key is not 32 bytes")
		}
		if !ed25519.Verify(ed25519.PublicKey(pub), digest[:], sig) {
			r.Result, r.Reason = "fail", "signature does not verify (headers changed or not the signer's key)"
			return r
		}
	default:
		return perm("algorithm " + r.Algo + " does not match key type " + keyType)
	}
	if strings.Contains(rec["t"], "y") {
		r.Result, r.Reason = "neutral", "the signer's key is marked testing"
		return r
	}
	r.Result = "pass"
	return r
}

// Signer signs the box's outgoing mail.
type Signer struct {
	Domain   string
	Selector string
	// One of the two. Both set means sign twice (one DKIM-Signature each): RSA for the receivers
	// that do not verify Ed25519 yet, which includes the largest ones.
	RSA     *rsa.PrivateKey
	Ed25519 ed25519.PrivateKey
	// EdSelector is the selector for the Ed25519 signature when both keys are set.
	EdSelector string
	Now        func() time.Time
}

// SignedHeaders is what the box signs: everything a reader sees, and the structure.
var SignedHeaders = []string{"From", "To", "Cc", "Subject", "Date", "Message-ID", "In-Reply-To", "References", "Reply-To", "MIME-Version", "Content-Type", "Content-Transfer-Encoding"}

// Sign returns the message with one or two DKIM-Signature fields prepended. relaxed/relaxed.
func (s Signer) Sign(raw []byte) ([]byte, error) {
	m := parseMessage(raw)
	if s.RSA == nil && s.Ed25519 == nil {
		return nil, errors.New("no signing key")
	}
	var sigs []string
	if s.RSA != nil {
		sig, err := s.sign(m, "rsa-sha256", s.Selector)
		if err != nil {
			return nil, err
		}
		sigs = append(sigs, sig)
	}
	if s.Ed25519 != nil {
		sel := s.EdSelector
		if sel == "" {
			sel = s.Selector
		}
		sig, err := s.sign(m, "ed25519-sha256", sel)
		if err != nil {
			return nil, err
		}
		sigs = append(sigs, sig)
	}
	var out bytes.Buffer
	for _, sg := range sigs {
		out.WriteString(sg)
	}
	out.Write(toCRLF(raw))
	return out.Bytes(), nil
}

func (s Signer) sign(m message, algo, selector string) (string, error) {
	now := time.Now
	if s.Now != nil {
		now = s.Now
	}
	body := canonBody("relaxed", m.body)
	bh := sha256.Sum256(body)
	var hlist []string
	used := make([]bool, len(m.headers))
	var in bytes.Buffer
	for _, n := range SignedHeaders {
		for j := len(m.headers) - 1; j >= 0; j-- {
			if used[j] || !strings.EqualFold(strings.TrimSpace(m.headers[j].name), n) {
				continue
			}
			used[j] = true
			hlist = append(hlist, strings.ToLower(n))
			in.WriteString(relaxedHeader(m.headers[j]))
			break
		}
	}
	if len(hlist) == 0 || hlist[0] != "from" {
		return "", errors.New("message has no From")
	}
	// fold the h= list and the long tags across lines so no line passes 78 chars
	field := fmt.Sprintf("DKIM-Signature: v=1; a=%s; c=relaxed/relaxed; d=%s; s=%s;\r\n\tt=%d; h=%s;\r\n\tbh=%s;\r\n\tb=",
		algo, s.Domain, selector, now().Unix(), strings.Join(hlist, ":"), base64.StdEncoding.EncodeToString(bh[:]))
	in.WriteString(strings.TrimSuffix(relaxedHeader(header{name: "DKIM-Signature", raw: field + "\r\n"}), "\r\n"))
	digest := sha256.Sum256(in.Bytes())
	var sig []byte
	var err error
	switch algo {
	case "rsa-sha256":
		sig, err = rsa.SignPKCS1v15(rand.Reader, s.RSA, crypto.SHA256, digest[:])
	case "ed25519-sha256":
		sig = ed25519.Sign(s.Ed25519, digest[:])
	}
	if err != nil {
		return "", err
	}
	b := base64.StdEncoding.EncodeToString(sig)
	var folded strings.Builder
	for len(b) > 70 {
		folded.WriteString(b[:70] + "\r\n\t")
		b = b[70:]
	}
	folded.WriteString(b)
	return field + folded.String() + "\r\n", nil
}

// DNSRecord is the TXT a public key is published as: name and value, the value split into
// 255-byte strings as a zone file wants them.
func DNSRecord(domain, selector string, rsaPub *rsa.PublicKey, edPub ed25519.PublicKey) (name, value string, err error) {
	name = selector + "._domainkey." + domain
	var p string
	switch {
	case rsaPub != nil:
		der, e := x509.MarshalPKIXPublicKey(rsaPub)
		if e != nil {
			return "", "", e
		}
		p = "v=DKIM1; k=rsa; p=" + base64.StdEncoding.EncodeToString(der)
	case edPub != nil:
		p = "v=DKIM1; k=ed25519; p=" + base64.StdEncoding.EncodeToString(edPub)
	default:
		return "", "", errors.New("no key")
	}
	var parts []string
	for len(p) > 250 {
		parts = append(parts, `"`+p[:250]+`"`)
		p = p[250:]
	}
	parts = append(parts, `"`+p+`"`)
	return name, strings.Join(parts, " "), nil
}
