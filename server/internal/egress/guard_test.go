package egress

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublicAddr(t *testing.T) {
	for _, s := range []string{"127.0.0.1", "127.8.9.1", "::1", "0.0.0.0", "::", "10.1.2.3", "172.16.0.1", "172.31.255.254", "192.168.1.100",
		"169.254.169.254", "fe80::1", "fe80::1%eth0", "100.64.0.1", "100.127.255.255", "224.0.0.1", "ff02::1", "255.255.255.255", "240.0.0.1",
		"fc00::1", "fd12:3456::1", "::ffff:127.0.0.1", "::ffff:192.168.1.1", "64:ff9b::7f00:1", "2002:7f00:1::1", "2001::1", "192.0.2.5", "198.18.0.1", "0.1.2.3", "fec0::1"} {
		if PublicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s passed as public", s)
		}
	}
	for _, s := range []string{"1.1.1.1", "140.228.69.225", "8.8.8.8", "2606:4700:4700::1111", "172.32.0.1", "100.128.0.1", "::ffff:8.8.8.8"} {
		if !PublicAddr(netip.MustParseAddr(s)) {
			t.Errorf("%s refused", s)
		}
	}
}

func TestCheckURL(t *testing.T) {
	for _, s := range []string{"http://example.com/a", "ftp://example.com/", "file:///etc/passwd", "https://localhost/", "https://LOCALHOST./", "https://box/",
		"https://printer.local/", "https://router.lan/x", "https://db.internal/", "https://127.0.0.1/", "https://[::1]:8443/", "https://192.168.1.1/admin",
		"https://169.254.169.254/latest/meta-data/", "https://user:pw@example.com/", "https:///nohost", "https://2130706433/", "https://x.onion/", "gopher://example.com"} {
		u, err := url.Parse(s)
		if err == nil && CheckURL(u) == nil {
			t.Errorf("%s passed", s)
		}
	}
	for _, s := range []string{"https://example.com/a", "https://www.bbc.co.uk/news/1", "https://1.1.1.1/", "https://example.com:8443/x", "https://data.ecmwf.int/forecasts"} {
		u, _ := url.Parse(s)
		if err := CheckURL(u); err != nil {
			t.Errorf("%s: %v", s, err)
		}
	}
}

// The connection rule: a guarded client does not reach a loopback server, whatever the address
// says, and the refusal is a Fetched with an error, not a fault.
func TestGuardedClientDoesNotReachLoopback(t *testing.T) {
	hit := 0
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hit++ }))
	defer srv.Close()
	c := New()
	for _, addr := range []string{srv.URL, strings.Replace(srv.URL, "127.0.0.1", "localhost", 1), strings.Replace(srv.URL, "https://", "http://", 1)} {
		f, err := c.Get(context.Background(), "x", addr)
		if err != nil || f.Status != 0 || f.Error == "" {
			t.Fatalf("%s: %+v %v", addr, f, err)
		}
		if c.Allows(addr) == nil {
			t.Fatalf("%s: Allows said yes", addr)
		}
	}
	// the dial itself, as it would be for a public name that resolves to this machine
	if err := guardDial("tcp4", "127.0.0.1:443", nil); !errors.Is(err, ErrRefused) {
		t.Fatalf("dial to loopback: %v", err)
	}
	if err := guardDial("tcp6", "[fe80::1%lo]:443", nil); !errors.Is(err, ErrRefused) {
		t.Fatalf("dial to link-local: %v", err)
	}
	if err := guardDial("tcp4", "1.1.1.1:443", nil); err != nil {
		t.Fatalf("dial to a public address: %v", err)
	}
	if hit != 0 {
		t.Fatalf("the loopback server was reached %d times", hit)
	}
	if NewUnguarded().Allows("http://127.0.0.1:1/") != nil {
		t.Fatal("the tests' client refuses loopback")
	}
}

func TestRedirectRule(t *testing.T) {
	first, _ := http.NewRequest("GET", "https://datahub.example.org/orders/1/latest", nil)
	first.Header.Set("apikey", "secret")
	next := func(to string) *http.Request {
		r, _ := http.NewRequest("GET", to, nil)
		r.Header.Set("apikey", "secret")
		r.Header.Set("Accept", "application/json")
		return r
	}
	same := next("https://datahub.example.org/orders/1/files")
	if err := checkRedirect(same, []*http.Request{first}); err != nil || same.Header.Get("apikey") != "secret" {
		t.Fatalf("same host: %v, key %q", err, same.Header.Get("apikey"))
	}
	other := next("https://cdn.example.net/file.grib")
	if err := checkRedirect(other, []*http.Request{first}); err != nil || other.Header.Get("apikey") != "" || other.Header.Get("Accept") == "" {
		t.Fatalf("another host: %v, key %q", err, other.Header.Get("apikey"))
	}
	for _, to := range []string{"http://datahub.example.org/x", "https://127.0.0.1/x", "https://router.lan/"} {
		if err := checkRedirect(next(to), []*http.Request{first}); !errors.Is(err, ErrRefused) {
			t.Fatalf("redirect to %s: %v", to, err)
		}
	}
	if err := checkRedirect(same, make([]*http.Request, maxRedirects)); !errors.Is(err, ErrRefused) {
		t.Fatal("no limit on redirects")
	}
}

// NewUnguarded exists for tests. Nothing that is built into a daemon may call it.
func TestNothingShipsUnguarded(t *testing.T) {
	root := filepath.Join("..", "..")
	err := filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil || d.IsDir() || !strings.HasSuffix(p, ".go") || strings.HasSuffix(p, "_test.go") {
			return nil
		}
		b, rerr := os.ReadFile(p)
		if rerr != nil {
			return nil
		}
		if strings.Contains(string(b), "NewUnguarded(") && filepath.Base(p) != "egress.go" {
			t.Errorf("%s calls egress.NewUnguarded", p)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
