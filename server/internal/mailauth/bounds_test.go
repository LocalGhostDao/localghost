package mailauth

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

// countingDNS answers "not found" to everything after a tiny delay and counts the calls.
type countingDNS struct {
	calls *int
}

func (c countingDNS) LookupTXT(_ context.Context, n string) ([]string, error) {
	*c.calls++
	return nil, notFound(n)
}
func (c countingDNS) LookupIP(_ context.Context, _ string, n string) ([]net.IP, error) {
	*c.calls++
	return nil, notFound(n)
}
func (c countingDNS) LookupMX(_ context.Context, n string) ([]*net.MX, error) {
	*c.calls++
	return nil, notFound(n)
}
func (c countingDNS) LookupAddr(_ context.Context, a string) ([]string, error) {
	*c.calls++
	return nil, notFound(a)
}

// A Subject folded into very many lines used to cost O(n²) in parseMessage (the audit's
// confirmed finding); it is linear now and the block is clipped at the caps.
func TestParseMessageLinearAndBounded(t *testing.T) {
	fold := func(n int) []byte {
		var b strings.Builder
		b.WriteString("Subject: x\r\n")
		for i := 0; i < n; i++ {
			b.WriteString(" x\r\n")
		}
		b.WriteString("From: a@b.example\r\n\r\nbody\r\n")
		return []byte(b.String())
	}
	t0 := time.Now()
	m := parseMessage(fold(400000)) // 1.6 MB of folded Subject
	d := time.Since(t0)
	if d > 2*time.Second {
		t.Fatalf("parseMessage took %v on a 400k-line fold", d)
	}
	if !m.clipped {
		t.Fatal("a 1.6 MB header block was not clipped")
	}
	if len(m.headers) != 1 || m.headers[0].name != "Subject" {
		t.Fatalf("headers after the clip: %d", len(m.headers))
	}
	if len(m.body) != 6 { // "body\r\n"
		t.Fatalf("body lost: %q", m.body)
	}
	s := ScreenHeaders(fold(400000), nil)
	found := false
	for _, r := range s.Reasons {
		if strings.Contains(r, "header block over") {
			found = true
		}
	}
	if !found {
		t.Fatalf("no clip reason: %v", s.Reasons)
	}
	// a line-count clip too: 3000 one-line headers
	var b strings.Builder
	for i := 0; i < 3000; i++ {
		fmt.Fprintf(&b, "X-H%d: v\r\n", i)
	}
	b.WriteString("\r\nbody")
	m = parseMessage([]byte(b.String()))
	if !m.clipped || len(m.headers) != maxHeaderLines {
		t.Fatalf("line clip: clipped=%v headers=%d", m.clipped, len(m.headers))
	}
	// an ordinary message is untouched
	m = parseMessage([]byte("From: a@b.example\r\nSubject: hi\r\n there\r\n\r\nbody"))
	if m.clipped || len(m.headers) != 2 || m.headers[1].raw != "Subject: hi\r\n there\r\n" {
		t.Fatalf("ordinary: %+v", m.headers)
	}
}

// Many DKIM-Signature headers used to mean one body hash and one DNS lookup each, without end.
func TestVerifyDKIMCapsSignatures(t *testing.T) {
	var b strings.Builder
	b.WriteString("From: a@b.example\r\n")
	for i := 0; i < 100; i++ {
		fmt.Fprintf(&b, "DKIM-Signature: v=1; a=rsa-sha256; c=relaxed/relaxed; d=example.test; s=s%d; h=from; bh=x; b=x\r\n", i)
	}
	b.WriteString("\r\nbody\r\n")
	calls := 0
	res := VerifyDKIM(context.Background(), countingDNS{&calls}, []byte(b.String()), time.Time{})
	if len(res) != maxSignatures+1 {
		t.Fatalf("%d results", len(res))
	}
	if last := res[len(res)-1]; last.Result != "permerror" || !strings.Contains(last.Reason, "92 more signatures") {
		t.Fatalf("last: %+v", last)
	}
	if calls > maxSignatures {
		t.Fatalf("%d DNS calls for %d checked signatures", calls, maxSignatures)
	}
	// an h= list of hundreds of names is refused before any scan
	raw := "From: a@b.example\r\nDKIM-Signature: v=1; a=rsa-sha256; d=example.test; s=s; h=" + strings.Repeat("from:", 200) + "from; bh=x; b=x\r\n\r\nbody\r\n"
	res = VerifyDKIM(context.Background(), countingDNS{&calls}, []byte(raw), time.Time{})
	if len(res) != 1 || res[0].Result != "permerror" || !strings.Contains(res[0].Reason, "h= names") {
		t.Fatalf("%+v", res)
	}
}

// An SPF record of many mx terms used to cost ten lookups per term on top of the ten-term limit.
func TestSPFQueryBudget(t *testing.T) {
	f := fakeDNS{txt: map[string][]string{}, mx: map[string][]*net.MX{}, ip: map[string][]net.IP{}}
	var terms []string
	for i := 0; i < 9; i++ {
		d := fmt.Sprintf("m%d.example", i)
		terms = append(terms, "mx:"+d)
		var mxs []*net.MX
		for j := 0; j < 10; j++ {
			h := fmt.Sprintf("mx%d.%s", j, d)
			mxs = append(mxs, &net.MX{Host: h})
			f.ip[h] = []net.IP{net.ParseIP("198.51.100.1")}
		}
		f.mx[d] = mxs
	}
	f.txt["sender.example"] = []string{"v=spf1 " + strings.Join(terms, " ") + " -all"}
	calls := 0
	counted := countingFake{fakeDNS: f, calls: &calls}
	r := CheckSPF(context.Background(), counted, net.ParseIP("203.0.113.9"), "x@sender.example", "h.example")
	if r.Result != "permerror" || !strings.Contains(r.Reason, "too many DNS queries") {
		t.Fatalf("%+v", r)
	}
	if calls > spfMaxQueries+1 {
		t.Fatalf("%d queries made", calls)
	}
}

type countingFake struct {
	fakeDNS
	calls *int
}

func (c countingFake) LookupTXT(ctx context.Context, n string) ([]string, error) {
	*c.calls++
	return c.fakeDNS.LookupTXT(ctx, n)
}
func (c countingFake) LookupIP(ctx context.Context, nw, n string) ([]net.IP, error) {
	*c.calls++
	return c.fakeDNS.LookupIP(ctx, nw, n)
}
func (c countingFake) LookupMX(ctx context.Context, n string) ([]*net.MX, error) {
	*c.calls++
	return c.fakeDNS.LookupMX(ctx, n)
}
func (c countingFake) LookupAddr(ctx context.Context, a string) ([]string, error) {
	*c.calls++
	return c.fakeDNS.LookupAddr(ctx, a)
}
