package egress

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestProxyRule(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	if ok, _ := (PhoneNet{}).Proxy(now); ok {
		t.Fatal("a phone never heard is not a proxy")
	}
	if ok, _ := (PhoneNet{Net: "wifi", Seen: now.Add(-5 * time.Minute)}).Proxy(now); !ok {
		t.Fatal("wifi five minutes ago is a proxy")
	}
	if ok, why := (PhoneNet{Net: "wifi", Seen: now.Add(-45 * time.Minute)}).Proxy(now); ok || !strings.Contains(why, "last heard") {
		t.Fatalf("wifi 45 min ago: %v %s", ok, why)
	}
	if ok, why := (PhoneNet{Net: "mobile", Seen: now}).Proxy(now); ok || why != "the phone is on mobile" {
		t.Fatalf("mobile: %v %s", ok, why)
	}
	if !Due(0, time.Hour, now) || !Due(now.Add(-61*time.Minute).Unix(), time.Hour, now) || Due(now.Add(-30*time.Minute).Unix(), time.Hour, now) {
		t.Fatal("Due")
	}
	if !Due(now.Add(-59*time.Minute).Unix(), time.Hour, now) {
		t.Fatal("a minute's slack")
	}
	if _, ok := ParseNet("5g"); ok {
		t.Fatal("5g is not a word the box knows")
	}
}

func TestClientFetchesAndCaps(t *testing.T) {
	big := strings.Repeat("x", 4<<20)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("User-Agent") != UA || r.Header.Get("Cookie") != "" {
			w.WriteHeader(500)
			return
		}
		switch r.URL.Path {
		case "/ok":
			w.Write([]byte(`{"price":"1"}`))
		case "/big":
			w.Write([]byte(big))
		case "/gone":
			w.WriteHeader(429)
			w.Write([]byte("slow down"))
		}
	}))
	defer srv.Close()
	c := NewUnguarded() // the test serves from loopback over plain http
	f, err := c.Get(context.Background(), "a", srv.URL+"/ok")
	if err != nil || f.Status != 200 || f.Body != `{"price":"1"}` || f.ID != "a" {
		t.Fatalf("%+v %v", f, err)
	}
	f, _ = c.Get(context.Background(), "b", srv.URL+"/big")
	if len(f.Body) != maxBody {
		t.Fatalf("cap: %d", len(f.Body))
	}
	f, _ = c.Get(context.Background(), "c", srv.URL+"/gone")
	if f.Status != 429 || f.Body != "slow down" {
		t.Fatalf("%+v", f)
	}
	f, _ = c.Get(context.Background(), "d", "http://127.0.0.1:1/nothing")
	if f.Status != 0 || f.Error == "" {
		t.Fatalf("a refused connection is a failed fetch: %+v", f)
	}
}
