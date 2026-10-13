// Package mailauth verifies what can be verified about a received mail, on the box, after the
// relay has handed it over sealed: SPF (RFC 7208) for the connecting address the relay noted,
// every DKIM signature (RFC 6376, RSA-SHA256 and Ed25519-SHA256, RFC 8463), DMARC alignment
// (RFC 7489), the relay's reverse-DNS note, a structural screen of the headers for the ways a
// message lies about where it came from, and a spam score from all of that. It also signs the
// box's own mail. Standard library only, plus the resolver the caller hands in, so every verdict
// is testable without a network and the box's DNS use is one switch.
package mailauth

import (
	"context"
	"net"
	"strings"
	"time"
)

// Resolver is the DNS the verifiers use. net.Resolver satisfies it; tests use a map.
type Resolver interface {
	LookupTXT(ctx context.Context, name string) ([]string, error)
	LookupIP(ctx context.Context, network, host string) ([]net.IP, error)
	LookupMX(ctx context.Context, name string) ([]*net.MX, error)
	LookupAddr(ctx context.Context, addr string) ([]string, error)
}

// NetResolver is the system resolver with a per-lookup timeout.
type NetResolver struct {
	R       *net.Resolver
	Timeout time.Duration
}

func (n NetResolver) r() *net.Resolver {
	if n.R != nil {
		return n.R
	}
	return net.DefaultResolver
}

func (n NetResolver) ctx(ctx context.Context) (context.Context, context.CancelFunc) {
	t := n.Timeout
	if t == 0 {
		t = 5 * time.Second
	}
	return context.WithTimeout(ctx, t)
}

func (n NetResolver) LookupTXT(ctx context.Context, name string) ([]string, error) {
	c, cancel := n.ctx(ctx)
	defer cancel()
	return n.r().LookupTXT(c, name)
}

func (n NetResolver) LookupIP(ctx context.Context, network, host string) ([]net.IP, error) {
	c, cancel := n.ctx(ctx)
	defer cancel()
	return n.r().LookupIP(c, network, host)
}

func (n NetResolver) LookupMX(ctx context.Context, name string) ([]*net.MX, error) {
	c, cancel := n.ctx(ctx)
	defer cancel()
	return n.r().LookupMX(c, name)
}

func (n NetResolver) LookupAddr(ctx context.Context, addr string) ([]string, error) {
	c, cancel := n.ctx(ctx)
	defer cancel()
	return n.r().LookupAddr(c, addr)
}

// NoResolver answers nothing: every verdict that needs DNS reads "not checked". For a box whose
// person would rather not have it look up the domains that write to them.
type NoResolver struct{}

// ErrNoDNS is what NoResolver returns.
type errNoDNS struct{}

func (errNoDNS) Error() string   { return "dns lookups are off" }
func (errNoDNS) Timeout() bool   { return false }
func (errNoDNS) Temporary() bool { return true }

func (NoResolver) LookupTXT(context.Context, string) ([]string, error) { return nil, errNoDNS{} }
func (NoResolver) LookupIP(context.Context, string, string) ([]net.IP, error) {
	return nil, errNoDNS{}
}
func (NoResolver) LookupMX(context.Context, string) ([]*net.MX, error) { return nil, errNoDNS{} }
func (NoResolver) LookupAddr(context.Context, string) ([]string, error) {
	return nil, errNoDNS{}
}

// isNotFound tells a name that does not exist (a definitive "no record") from a lookup that
// failed (a temporary error).
func isNotFound(err error) bool {
	if err == nil {
		return false
	}
	var de *net.DNSError
	if asDNS(err, &de) {
		return de.IsNotFound
	}
	return false
}

func asDNS(err error, target **net.DNSError) bool {
	for err != nil {
		if d, ok := err.(*net.DNSError); ok {
			*target = d
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}

// lowerDomain trims a trailing dot and lower-cases.
func lowerDomain(d string) string {
	return strings.ToLower(strings.TrimSuffix(strings.TrimSpace(d), "."))
}

// OrgDomain is the organisational domain DMARC aligns on: the registrable part under the public
// suffix. The standard library has no public suffix list, so this is the common two-label
// suffixes by hand; a rarer one aligns strictly, which errs toward "not aligned".
func OrgDomain(d string) string {
	d = lowerDomain(d)
	labels := strings.Split(d, ".")
	if len(labels) <= 2 {
		return d
	}
	last2 := labels[len(labels)-2] + "." + labels[len(labels)-1]
	if twoLabelSuffix[last2] {
		if len(labels) == 3 {
			return d
		}
		return strings.Join(labels[len(labels)-3:], ".")
	}
	return last2
}

var twoLabelSuffix = map[string]bool{
	"co.uk": true, "org.uk": true, "ac.uk": true, "gov.uk": true, "me.uk": true, "ltd.uk": true, "plc.uk": true, "net.uk": true, "nhs.uk": true, "sch.uk": true,
	"com.au": true, "net.au": true, "org.au": true, "edu.au": true, "gov.au": true, "id.au": true,
	"co.nz": true, "org.nz": true, "net.nz": true, "govt.nz": true, "ac.nz": true,
	"co.jp": true, "ne.jp": true, "or.jp": true, "ac.jp": true, "go.jp": true,
	"co.za": true, "org.za": true, "net.za": true, "gov.za": true, "ac.za": true,
	"com.br": true, "org.br": true, "net.br": true, "gov.br": true,
	"com.mx": true, "org.mx": true, "com.ar": true, "com.co": true, "com.pe": true, "com.ve": true, "com.uy": true, "com.ec": true,
	"co.in": true, "net.in": true, "org.in": true, "ac.in": true, "gov.in": true, "co.il": true, "org.il": true, "ac.il": true,
	"com.sg": true, "com.hk": true, "com.tr": true, "com.cn": true, "net.cn": true, "org.cn": true, "gov.cn": true,
	"co.kr": true, "or.kr": true, "com.tw": true, "com.my": true, "com.ph": true, "com.pk": true, "com.eg": true, "com.ng": true, "co.ke": true,
	"com.ua": true, "com.pl": true, "co.id": true, "com.vn": true, "com.sa": true, "com.ae": true, "co.th": true, "com.bd": true,
	"com.ru": true, "org.ru": true, "net.ru": true, "com.es": true, "com.pt": true, "com.gr": true,
	"github.io": true, "gitlab.io": true, "herokuapp.com": true, "blogspot.com": true, "wordpress.com": true, "cloudfront.net": true, "amazonaws.com": true,
}
