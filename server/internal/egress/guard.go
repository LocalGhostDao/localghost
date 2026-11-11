package egress

// WHERE THE BOX MAY CONNECT.
//
// The client fetches addresses that came from outside: a feed entry's link, a coin's website
// from a public list, wherever a page redirects. Until the second audit (10 Nov 2026) nothing
// said where those could lead, so a link in any subscribed feed could make the box ask its own
// loopback services or the router's admin page, and the answer went into the news tables. The
// rule now, applied in three places so that no one of them has to be perfect:
//
//   - the address asked for (first request and every redirect): https only, a real name or a
//     public IP, never this machine's or the LAN's names;
//   - a redirect: five hops at most, and a key header never follows to another host;
//   - the connection itself, after the name has been resolved: the peer must be a public
//     address and not one of this machine's own. This is the one that holds against a name
//     that resolves to 127.0.0.1 or changes its answer between two lookups.
//
// A proxy from the environment is not honoured: the rule is applied where the connection is
// made, and through a proxy the box would not be the one making it.

import (
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/netip"
	"net/url"
	"strings"
	"sync"
	"syscall"
	"time"
)

// ErrRefused is wrapped by every refusal of the destination rule.
var ErrRefused = errors.New("egress: destination refused")

const maxRedirects = 5

var notPublic = func() []netip.Prefix {
	var out []netip.Prefix
	for _, s := range []string{
		"0.0.0.0/8",       // this network
		"100.64.0.0/10",   // carrier-grade NAT
		"192.0.0.0/24",    // protocol assignments
		"192.0.2.0/24",    // documentation
		"198.18.0.0/15",   // benchmarking
		"198.51.100.0/24", // documentation
		"203.0.113.0/24",  // documentation
		"240.0.0.0/4",     // reserved, and broadcast
		"64:ff9b::/96",    // NAT64: an IPv4 address in disguise
		"64:ff9b:1::/48",  // local NAT64
		"2002::/16",       // 6to4: likewise
		"2001::/32",       // Teredo: likewise
		"2001:db8::/32",   // documentation
		"100::/64",        // discard
		"fec0::/10",       // site-local, retired
	} {
		out = append(out, netip.MustParsePrefix(s))
	}
	return out
}()

// PublicAddr reports whether ip is an address on the public internet: not loopback, private,
// link-local, multicast, unspecified, shared, reserved, or IPv4 carried inside IPv6.
func PublicAddr(ip netip.Addr) bool {
	ip = ip.Unmap().WithZone("")
	if !ip.IsValid() || ip.IsUnspecified() || ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	for _, p := range notPublic {
		if p.Contains(ip) {
			return false
		}
	}
	return true
}

// own is this machine's interface addresses, read once a minute (a box with a public address
// on its interface must not be sent back to itself either).
var own struct {
	mu   sync.Mutex
	at   time.Time
	addr map[netip.Addr]bool
}

func isOwn(ip netip.Addr) bool {
	own.mu.Lock()
	defer own.mu.Unlock()
	if own.addr == nil || time.Since(own.at) > time.Minute {
		m := map[netip.Addr]bool{}
		if as, err := net.InterfaceAddrs(); err == nil {
			for _, a := range as {
				if n, ok := a.(*net.IPNet); ok {
					if ip, ok := netip.AddrFromSlice(n.IP); ok {
						m[ip.Unmap()] = true
					}
				}
			}
		}
		own.addr, own.at = m, time.Now()
	}
	return own.addr[ip.Unmap().WithZone("")]
}

// guardDial is the Dialer's Control: it sees the address actually being connected to.
func guardDial(_, address string, _ syscall.RawConn) error {
	ap, err := netip.ParseAddrPort(address)
	if err != nil {
		return fmt.Errorf("%w: %q is not an address", ErrRefused, address)
	}
	if !PublicAddr(ap.Addr()) {
		return fmt.Errorf("%w: %s is not a public address", ErrRefused, ap.Addr())
	}
	if isOwn(ap.Addr()) {
		return fmt.Errorf("%w: %s is this machine", ErrRefused, ap.Addr())
	}
	return nil
}

// localNames are suffixes that never name a place on the internet.
var localNames = []string{".localhost", ".local", ".internal", ".lan", ".home", ".home.arpa", ".intranet", ".corp", ".test", ".invalid", ".onion"}

// CheckURL is the rule for an address before it is asked for.
func CheckURL(u *url.URL) error {
	if u == nil {
		return fmt.Errorf("%w: no address", ErrRefused)
	}
	if u.Scheme != "https" {
		return fmt.Errorf("%w: only https is fetched", ErrRefused)
	}
	if u.User != nil {
		return fmt.Errorf("%w: an address with a login in it", ErrRefused)
	}
	host := strings.ToLower(strings.TrimSuffix(u.Hostname(), "."))
	if host == "" {
		return fmt.Errorf("%w: no host", ErrRefused)
	}
	if ip, err := netip.ParseAddr(host); err == nil {
		if !PublicAddr(ip) || isOwn(ip) {
			return fmt.Errorf("%w: %s is not a public address", ErrRefused, ip)
		}
		return nil
	}
	if host == "localhost" || !strings.Contains(host, ".") {
		return fmt.Errorf("%w: %s is not a name on the internet", ErrRefused, host)
	}
	for _, s := range localNames {
		if strings.HasSuffix(host, s) {
			return fmt.Errorf("%w: %s is not a name on the internet", ErrRefused, host)
		}
	}
	return nil
}

// keyHeaders never follow a redirect to another host (net/http already drops Authorization and
// Cookie; the weather services' key travels in a header of its own).
var keyHeaders = []string{"apikey", "x-api-key", "x-ibm-client-id", "x-ibm-client-secret", "Authorization", "Cookie"}

func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return fmt.Errorf("%w: more than %d redirects", ErrRefused, maxRedirects)
	}
	if err := CheckURL(req.URL); err != nil {
		return err
	}
	if len(via) > 0 && !strings.EqualFold(req.URL.Host, via[0].URL.Host) {
		for _, h := range keyHeaders {
			req.Header.Del(h)
		}
	}
	return nil
}

// guarded applies CheckURL to every request a client makes.
type guarded struct{ rt http.RoundTripper }

func (g guarded) RoundTrip(r *http.Request) (*http.Response, error) {
	if err := CheckURL(r.URL); err != nil {
		return nil, err
	}
	return g.rt.RoundTrip(r)
}

// Transport is a transport whose every connection passes the destination rule. The caller may
// set its timeouts and pool sizes, and hands it to Wrap.
func Transport() *http.Transport {
	return &http.Transport{
		Proxy:                 nil,
		DialContext:           (&net.Dialer{Timeout: 10 * time.Second, Control: guardDial}).DialContext,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 15 * time.Second,
		MaxIdleConns:          4,
		IdleConnTimeout:       30 * time.Second,
	}
}

// Wrap makes the http.Client for a Transport from this package: the address rule on every
// request and redirect, on top of the connection rule the transport already has.
func Wrap(tr *http.Transport, timeout time.Duration) *http.Client {
	return &http.Client{Transport: guarded{rt: tr}, Timeout: timeout, CheckRedirect: checkRedirect}
}
