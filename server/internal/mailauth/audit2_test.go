package mailauth

import (
	"context"
	"net"
	"strings"
	"testing"
	"time"
)

// Two strangers under a country's category domain that is not in the list do not share an
// organisational domain (audit run 2).
func TestOrgDomainUnderUnlistedCategorySuffix(t *testing.T) {
	for in, want := range map[string]string{
		"mail.bank.com.kz": "bank.com.kz", "evil.com.kz": "evil.com.kz", "a.b.co.at": "b.co.at", "x.gov.kz": "x.gov.kz",
		"mail.example.co.uk": "example.co.uk", "mail.example.com": "example.com", "example.de": "example.de", "a.example.de": "example.de",
		"news.example.io": "example.io", "x.y.example.org": "example.org",
	} {
		if got := OrgDomain(in); got != want {
			t.Errorf("OrgDomain(%s) = %s, want %s", in, got, want)
		}
	}
	if alignedDomains("evil.com.kz", "bank.com.kz", "r") {
		t.Fatal("two registrants under com.kz align")
	}
	if !alignedDomains("mail.bank.com.kz", "bank.com.kz", "r") {
		t.Fatal("a subdomain no longer aligns with its own domain")
	}
}

// slowDNS answers nothing until the context it was given ends.
type slowDNS struct{ dmarc []string }

func (s slowDNS) LookupTXT(ctx context.Context, n string) ([]string, error) {
	if strings.HasPrefix(n, "_dmarc.") {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return s.dmarc, nil
	}
	<-ctx.Done()
	return nil, ctx.Err()
}
func (s slowDNS) LookupIP(ctx context.Context, _ string, _ string) ([]net.IP, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (s slowDNS) LookupMX(ctx context.Context, _ string) ([]*net.MX, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}
func (s slowDNS) LookupAddr(ctx context.Context, _ string) ([]string, error) {
	<-ctx.Done()
	return nil, ctx.Err()
}

// A sender whose SPF never answers does not starve the policy lookup: DMARC is still read, and
// a reject policy with nothing aligned is a fail, not a shrug.
func TestSlowSPFDoesNotStarveDMARC(t *testing.T) {
	raw := []byte("From: ceo@bank.example\r\nTo: a@lgs.example\r\nSubject: s\r\nDate: Mon, 09 Nov 2026 10:00:00 +0000\r\nMessage-ID: <1@x>\r\n\r\nhi\r\n")
	// the caller's deadline is longer than SPF's share: SPF and DKIM hang for their shares and
	// the policy is still read
	old := [3]time.Duration{stageSPF, stageDKIM, stageDMARC}
	stageSPF, stageDKIM, stageDMARC = 150*time.Millisecond, 150*time.Millisecond, 150*time.Millisecond
	defer func() { stageSPF, stageDKIM, stageDMARC = old[0], old[1], old[2] }()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	v := Judge(ctx, slowDNS{dmarc: []string{"v=DMARC1; p=reject"}}, Input{Raw: raw, IP: net.ParseIP("203.0.113.9"), MailFrom: "x@evil.example", Helo: "mx.evil.example", PTRState: "ok", Now: time.Now()})
	if v.SPF.Result != "temperror" {
		t.Fatalf("SPF %s", v.SPF.Result)
	}
	if v.DMARC.Result != "fail" || v.DMARC.Policy != "reject" {
		t.Fatalf("DMARC after a slow SPF: %+v", v.DMARC)
	}
	if v.Spam != "high" {
		t.Fatalf("judged %s: %v", v.Spam, v.SpamReasons)
	}
	// and when the policy itself cannot be read, that is points, not nothing
	tv := Verdicts{SPF: SPFResult{Result: "pass", Domain: "evil.example"}, DMARC: DMARCResult{Result: "temperror"}, Reverse: "ok"}
	if _, why := score(tv, Input{SeenBefore: 3}); !strings.Contains(strings.Join(why, ";"), "DMARC could not be checked") {
		t.Fatalf("temperror: %v", why)
	}
}

// With no policy and nothing that passed being the From domain's, the message is not "low" on
// the strength of someone else's SPF and DKIM.
func TestNothingAlignedIsNotLow(t *testing.T) {
	v := Verdicts{
		SPF:     SPFResult{Result: "pass", Domain: "sender.example"},
		DKIM:    []DKIMResult{{Domain: "sender.example", Result: "pass"}},
		DMARC:   DMARCResult{Result: "none", Domain: "bank.example", Reason: "no policy published"},
		Reverse: "ok",
	}
	lvl, why := score(v, Input{SeenBefore: 0})
	if lvl == "low" || !strings.Contains(strings.Join(why, ";"), "nothing proves the From domain") {
		t.Fatalf("%s %v", lvl, why)
	}
	// aligned, no policy: low as before
	v.DMARC.Aligned = "dkim"
	if lvl, _ := score(v, Input{SeenBefore: 2}); lvl != "low" {
		t.Fatalf("aligned without a policy: %s", lvl)
	}
	// DNS checks off: nothing to say either way
	v.DMARC = DMARCResult{Result: "none", Reason: "not checked"}
	if _, why := score(v, Input{SeenBefore: 2}); strings.Contains(strings.Join(why, ";"), "nothing proves") {
		t.Fatal("scored a check that was not run")
	}
}

// A signature that covers only part of the body (l=) is not a pass for alignment or the score.
func TestPartialSignatureIsNotAPass(t *testing.T) {
	k := DKIMResult{Domain: "bank.example", Result: "pass", Partial: true}
	if k.Pass() {
		t.Fatal("a partial signature passes")
	}
	d := CheckDMARC(context.Background(), fakeDNS{txt: map[string][]string{"_dmarc.bank.example": {"v=DMARC1; p=reject"}}}, "bank.example", SPFResult{Result: "none"}, []DKIMResult{k})
	if d.Result != "fail" {
		t.Fatalf("DMARC with only a partial signature: %+v", d)
	}
	_, why := score(Verdicts{DKIM: []DKIMResult{k}, DMARC: d, Reverse: "ok"}, Input{})
	if !strings.Contains(strings.Join(why, ";"), "DKIM signs only part of the body") {
		t.Fatalf("%v", why)
	}
}

func TestDuplicateContentTypeIsNamed(t *testing.T) {
	raw := []byte("From: a@x.example\r\nTo: b@y.example\r\nSubject: s\r\nDate: Mon, 09 Nov 2026 10:00:00 +0000\r\nMessage-ID: <1@x>\r\nContent-Type: text/plain\r\nContent-Type: text/html\r\n\r\nhi\r\n")
	v := Judge(context.Background(), NoResolver{}, Input{Raw: raw, SeenBefore: 2})
	if !strings.Contains(strings.Join(v.Screen.Reasons, ";"), "more than one Content-type header") {
		t.Fatalf("%v", v.Screen.Reasons)
	}
}
