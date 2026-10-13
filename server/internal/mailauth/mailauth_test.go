package mailauth

import (
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeDNS answers from maps; a missing name is "not found".
type fakeDNS struct {
	txt map[string][]string
	ip  map[string][]net.IP
	mx  map[string][]*net.MX
	ptr map[string][]string
}

func notFound(name string) error {
	return &net.DNSError{Err: "no such host", Name: name, IsNotFound: true}
}

func (f fakeDNS) LookupTXT(_ context.Context, n string) ([]string, error) {
	if v, ok := f.txt[strings.ToLower(n)]; ok {
		return v, nil
	}
	return nil, notFound(n)
}
func (f fakeDNS) LookupIP(_ context.Context, _ string, n string) ([]net.IP, error) {
	if v, ok := f.ip[strings.ToLower(n)]; ok {
		return v, nil
	}
	return nil, notFound(n)
}
func (f fakeDNS) LookupMX(_ context.Context, n string) ([]*net.MX, error) {
	if v, ok := f.mx[strings.ToLower(n)]; ok {
		return v, nil
	}
	return nil, notFound(n)
}
func (f fakeDNS) LookupAddr(_ context.Context, a string) ([]string, error) {
	if v, ok := f.ptr[a]; ok {
		return v, nil
	}
	return nil, notFound(a)
}

func TestCanonicalisationVectors(t *testing.T) {
	// RFC 6376 section 3.4.5
	raw := []byte("A: X\r\nB : Y\t\r\n\tZ  \r\n\r\n C \r\nD \t E\r\n\r\n\r\n")
	m := parseMessage(raw)
	var rh, sh strings.Builder
	for _, h := range m.headers {
		rh.WriteString(relaxedHeader(h))
		sh.WriteString(simpleHeader(h))
	}
	if rh.String() != "a:X\r\nb:Y Z\r\n" {
		t.Fatalf("relaxed header: %q", rh.String())
	}
	if sh.String() != "A: X\r\nB : Y\t\r\n\tZ  \r\n" {
		t.Fatalf("simple header: %q", sh.String())
	}
	if got := string(canonBody("relaxed", m.body)); got != " C\r\nD E\r\n" {
		t.Fatalf("relaxed body: %q", got)
	}
	if got := string(canonBody("simple", m.body)); got != " C \r\nD \t E\r\n" {
		t.Fatalf("simple body: %q", got)
	}
	if got := string(canonBody("simple", nil)); got != "\r\n" {
		t.Fatalf("simple empty body: %q", got)
	}
	if got := canonBody("relaxed", []byte("\r\n\r\n")); len(got) != 0 {
		t.Fatalf("relaxed empty body: %q", got)
	}
}

func TestStripB(t *testing.T) {
	raw := "DKIM-Signature: v=1; bh=abc; b=xyz\r\n ; d=x\r\n"
	if got := stripB(raw); got != "DKIM-Signature: v=1; bh=abc; b=; d=x\r\n" {
		t.Fatalf("%q", got)
	}
	raw = "DKIM-Signature: v=1; bh=abc;\r\n\tb=xyz\r\n\tmore\r\n"
	if got := stripB(raw); got != "DKIM-Signature: v=1; bh=abc;\r\n\tb=" {
		t.Fatalf("%q", got)
	}
}

const msgText = "From: Paul <paul@example.org>\r\nTo: agent@lgs.example\r\nSubject: the flight times\r\nDate: Mon, 12 Oct 2026 10:00:00 +0100\r\nMessage-ID: <abc@example.org>\r\nMIME-Version: 1.0\r\nContent-Type: text/plain; charset=utf-8\r\n\r\nThursday works.  \r\n\r\nPaul\r\n"

func TestSignAndVerifyBothAlgorithms(t *testing.T) {
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	edPub, edPriv, _ := ed25519.GenerateKey(rand.Reader)
	s := Signer{Domain: "example.org", Selector: "ghost", EdSelector: "ghosted", RSA: rk, Ed25519: edPriv, Now: func() time.Time { return time.Unix(1760000000, 0) }}
	signed, err := s.Sign([]byte(msgText))
	if err != nil {
		t.Fatal(err)
	}
	_, rsaRec, _ := DNSRecord("example.org", "ghost", &rk.PublicKey, nil)
	_, edRec, _ := DNSRecord("example.org", "ghosted", nil, edPub)
	unquote := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, `" "`, ""), `"`, "") }
	dns := fakeDNS{txt: map[string][]string{
		"ghost._domainkey.example.org":   {unquote(rsaRec)},
		"ghosted._domainkey.example.org": {unquote(edRec)},
	}}
	res := VerifyDKIM(context.Background(), dns, signed, time.Unix(1760000100, 0))
	if len(res) != 2 {
		t.Fatalf("want 2 signatures, got %+v", res)
	}
	for _, r := range res {
		if !r.Pass() {
			t.Fatalf("%s %s: %s %s", r.Algo, r.Selector, r.Result, r.Reason)
		}
	}
	// a relay's Received line on top changes nothing
	withRecv := append([]byte("Received: from mx.example (mx.example [10.0.0.1]) by relay; Mon, 12 Oct 2026 09:00:00 +0000\r\n"), signed...)
	for _, r := range VerifyDKIM(context.Background(), dns, withRecv, time.Time{}) {
		if !r.Pass() {
			t.Fatalf("after Received: %s %s", r.Result, r.Reason)
		}
	}
	// LF line endings from a file on disk verify the same
	lf := strings.ReplaceAll(string(signed), "\r\n", "\n")
	for _, r := range VerifyDKIM(context.Background(), dns, []byte(lf), time.Time{}) {
		if !r.Pass() {
			t.Fatalf("LF: %s %s", r.Result, r.Reason)
		}
	}
	// a changed body fails on the body hash; a changed subject on the signature
	tampered := []byte(strings.Replace(string(signed), "Thursday works", "Friday works", 1))
	for _, r := range VerifyDKIM(context.Background(), dns, tampered, time.Time{}) {
		if r.Result != "fail" || !strings.Contains(r.Reason, "body hash") {
			t.Fatalf("tampered body: %s %s", r.Result, r.Reason)
		}
	}
	tampered = []byte(strings.Replace(string(signed), "Subject: the flight times", "Subject: send money", 1))
	for _, r := range VerifyDKIM(context.Background(), dns, tampered, time.Time{}) {
		if r.Result != "fail" || !strings.Contains(r.Reason, "signature does not verify") {
			t.Fatalf("tampered subject: %s %s", r.Result, r.Reason)
		}
	}
	// no key in DNS
	none := VerifyDKIM(context.Background(), fakeDNS{}, signed, time.Time{})
	if none[0].Result != "permerror" {
		t.Fatalf("no key: %+v", none[0])
	}
	// DNS off
	off := VerifyDKIM(context.Background(), NoResolver{}, signed, time.Time{})
	if off[0].Result != "temperror" {
		t.Fatalf("dns off: %+v", off[0])
	}
}

func TestDNSRecordSplitsLongKeys(t *testing.T) {
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	name, val, err := DNSRecord("lgs.example", "ghost", &rk.PublicKey, nil)
	if err != nil || name != "ghost._domainkey.lgs.example" {
		t.Fatal(name, err)
	}
	if !strings.Contains(val, `" "`) {
		t.Fatalf("a 2048-bit key needs two strings: %s", val)
	}
	joined := strings.ReplaceAll(strings.ReplaceAll(val, `" "`, ""), `"`, "")
	p := tags(joined)["p"]
	der, err := base64.StdEncoding.DecodeString(p)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := x509.ParsePKIXPublicKey(der); err != nil {
		t.Fatal(err)
	}
}

func TestSPF(t *testing.T) {
	dns := fakeDNS{
		txt: map[string][]string{
			"example.org":         {"v=spf1 ip4:192.0.2.0/24 include:_spf.mailer.example -all"},
			"_spf.mailer.example": {"v=spf1 ip4:198.51.100.7 a:out.mailer.example ~all"},
			"soft.example":        {"v=spf1 mx ~all"},
			"loop.example":        {"v=spf1 include:loop.example -all"},
			"redir.example":       {"v=spf1 redirect=example.org"},
			"macro.example":       {"v=spf1 exists:%{i}.rbl.example -all"},
			"two.example":         {"v=spf1 -all", "v=spf1 +all"},
		},
		ip: map[string][]net.IP{
			"out.mailer.example":     {net.ParseIP("203.0.113.9")},
			"mx1.soft.example":       {net.ParseIP("203.0.113.50")},
			"192.0.2.77.rbl.example": {net.ParseIP("127.0.0.2")},
		},
		mx: map[string][]*net.MX{"soft.example": {{Host: "mx1.soft.example", Pref: 10}}},
	}
	cases := []struct {
		ip, from, want string
	}{
		{"192.0.2.10", "paul@example.org", "pass"},
		{"198.51.100.7", "paul@example.org", "pass"},
		{"203.0.113.9", "paul@example.org", "pass"},
		{"203.0.113.10", "paul@example.org", "fail"},
		{"203.0.113.50", "x@soft.example", "pass"},
		{"203.0.113.51", "x@soft.example", "softfail"},
		{"203.0.113.51", "x@none.example", "none"},
		{"203.0.113.51", "x@loop.example", "permerror"},
		{"192.0.2.10", "x@redir.example", "pass"},
		{"192.0.2.77", "x@macro.example", "pass"},
		{"192.0.2.78", "x@macro.example", "fail"},
		{"192.0.2.78", "x@two.example", "permerror"},
	}
	for _, c := range cases {
		r := CheckSPF(context.Background(), dns, net.ParseIP(c.ip), c.from, "helo.example")
		if r.Result != c.want {
			t.Errorf("%s from %s: want %s got %s (%s)", c.ip, c.from, c.want, r.Result, r.Reason)
		}
	}
	// a bounce is judged on the HELO name
	r := CheckSPF(context.Background(), dns, net.ParseIP("192.0.2.10"), "", "example.org")
	if r.Result != "pass" || r.Domain != "example.org" {
		t.Fatalf("bounce: %+v", r)
	}
	if r := CheckSPF(context.Background(), NoResolver{}, net.ParseIP("192.0.2.10"), "p@example.org", ""); r.Result != "none" || r.Reason != "not checked" {
		t.Fatalf("dns off: %+v", r)
	}
}

func TestDMARCAlignment(t *testing.T) {
	dns := fakeDNS{txt: map[string][]string{
		"_dmarc.example.org": {"v=DMARC1; p=reject; adkim=s"},
		"_dmarc.bank.co.uk":  {"v=DMARC1; p=quarantine"},
	}}
	ctx := context.Background()
	pass := []DKIMResult{{Domain: "example.org", Result: "pass"}}
	r := CheckDMARC(ctx, dns, "example.org", SPFResult{Result: "fail"}, pass)
	if r.Result != "pass" || r.Aligned != "dkim" || r.Policy != "reject" {
		t.Fatalf("%+v", r)
	}
	// strict adkim: a subdomain signature is not aligned; a relaxed SPF on the org domain is
	r = CheckDMARC(ctx, dns, "example.org", SPFResult{Result: "pass", Domain: "mail.example.org"}, []DKIMResult{{Domain: "mail.example.org", Result: "pass"}})
	if r.Result != "pass" || r.Aligned != "spf" {
		t.Fatalf("%+v", r)
	}
	r = CheckDMARC(ctx, dns, "example.org", SPFResult{Result: "pass", Domain: "evil.example"}, nil)
	if r.Result != "fail" {
		t.Fatalf("%+v", r)
	}
	// the org domain's policy covers a subdomain
	r = CheckDMARC(ctx, dns, "alerts.bank.co.uk", SPFResult{Result: "pass", Domain: "bank.co.uk"}, nil)
	if r.Result != "pass" || r.Policy != "quarantine" {
		t.Fatalf("%+v", r)
	}
	r = CheckDMARC(ctx, dns, "nopolicy.example", SPFResult{Result: "pass", Domain: "nopolicy.example"}, nil)
	if r.Result != "none" || r.Aligned != "spf" {
		t.Fatalf("%+v", r)
	}
	if OrgDomain("a.b.example.co.uk") != "example.co.uk" || OrgDomain("mail.example.org") != "example.org" || OrgDomain("example.org") != "example.org" {
		t.Fatal("org domain")
	}
}

func TestScreen(t *testing.T) {
	s := ScreenHeaders([]byte(msgText), []string{"lgs.example"})
	if !s.Clean() || s.From != "paul@example.org" || s.FromName != "Paul" || s.Subject != "the flight times" {
		t.Fatalf("%+v", s)
	}
	bad := "From: \"me@lgs.example\" <x@evil.example>\r\nFrom: y@evil.example\r\nReply-To: z@other.example\r\nSubject: =?utf-8?q?URGENT_WIRE_TRANSFER_NOW?=\r\nTo: agent@lgs.example\r\nContent-Type: text/html\r\n\r\n<b>hi</b>\r\n"
	s = ScreenHeaders([]byte(bad), []string{"lgs.example"})
	want := []string{"more than one From header", "no Message-ID", "no Date", "the display name is an address on another domain (me@lgs.example)", "the display name pretends to be this box", "Reply-To goes to another domain (other.example)", "subject all in capitals", "the body is HTML only, no text part"}
	if strings.Join(s.Reasons, "|") != strings.Join(want, "|") {
		t.Fatalf("got %q", s.Reasons)
	}
	inj := "From: a@b.example\rBcc: victim@c.example\r\nSubject: x\r\nDate: Mon, 12 Oct 2026 10:00:00 +0100\r\nMessage-ID: <1@b.example>\r\n\r\nbody\r\n"
	s = ScreenHeaders([]byte(inj), nil)
	if len(s.Reasons) == 0 || !strings.Contains(s.Reasons[0], "bare CR") {
		t.Fatalf("%+v", s.Reasons)
	}
}

func TestJudgeAndLine(t *testing.T) {
	v := Judge(context.Background(), NoResolver{}, Input{Raw: []byte(msgText), IP: net.ParseIP("192.0.2.1"), Helo: "mx.example.org", MailFrom: "paul@example.org", PTRState: "ok", PTR: "mx.example.org", SeenBefore: 3})
	if !v.HeloMatch || v.Spam != "low" {
		t.Fatalf("%+v", v)
	}
	line := v.Line()
	if !strings.HasPrefix(line, "SPF none · DKIM none · no DMARC policy · reverse ok · spam low") {
		t.Fatalf("%q", line)
	}
	v.Screen.Reasons = []string{"the display name pretends to be this box"}
	v.SPF.Result = "fail"
	v.Spam, v.SpamReasons = score(v, Input{})
	if v.Spam != "high" {
		t.Fatalf("%s %v", v.Spam, v.SpamReasons)
	}
}
