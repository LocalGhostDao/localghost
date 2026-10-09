package secd

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The phone asks what to fetch (the defaults until synthd seeds the list), posts the bodies, and
// each batch lands whole in the right daemon's inbox; a batch without the shape is refused.
func TestFetchListAndSpools(t *testing.T) {
	s := newTestServer(t)
	s.mu.Lock()
	s.mounted = 0
	s.mu.Unlock()
	tok, err := s.session.Issue()
	if err != nil {
		t.Fatal(err)
	}
	call := func(method, path, body string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		r := httptest.NewRequest(method, path, strings.NewReader(body))
		r.Header.Set("Authorization", "Bearer "+tok)
		s.Handler().ServeHTTP(rr, r)
		return rr
	}
	rr := call("GET", "/v1/fetch/list", "")
	if rr.Code != 200 {
		t.Fatalf("list: %d", rr.Code)
	}
	var list fetchListDoc
	_ = json.Unmarshal(rr.Body.Bytes(), &list)
	// twelve feeds (the FT is read in its own app); the rates the phone fetches: the ECB twice and Coinbase's rank list (the
	// tickers are the box's own, every minute)
	if len(list.Feeds) != 12 || len(list.Rates) != 3 || list.FeedsEvery != 120 || list.Feeds[0].ID != "bbc" || list.Rates[0].ID != "ecb" || list.Rates[2].ID != "coinbase-ranks" {
		t.Fatalf("list: %+v", list)
	}
	mount := filepath.Join(s.cfg.StateDir, "mnt", "slot0")
	rr = call("POST", "/v1/news/fetched", `{"fetchedAt":1,"feeds":[{"id":"bbc","status":200,"body":"<rss/>"}]}`)
	if rr.Code != 200 {
		t.Fatalf("news fetched: %d %s", rr.Code, rr.Body.String())
	}
	ents, _ := os.ReadDir(filepath.Join(mount, "synthd", "news", "inbox"))
	if len(ents) != 1 || !strings.HasPrefix(ents[0].Name(), "news-") || strings.HasSuffix(ents[0].Name(), ".part") {
		t.Fatalf("news inbox: %v", ents)
	}
	if rr := call("POST", "/v1/news/fetched", `{"fetchedAt":1,"feeds":[]}`); rr.Code != 400 {
		t.Fatalf("empty news batch: %d", rr.Code)
	}
	rr = call("POST", "/v1/rates/fetched", `{"fetchedAt":1,"sources":[{"id":"ecb","status":200,"body":"<x/>"}]}`)
	if rr.Code != 200 {
		t.Fatalf("rates fetched: %d", rr.Code)
	}
	ents, _ = os.ReadDir(filepath.Join(mount, "tallyd", "rates"))
	if len(ents) != 1 || !strings.HasPrefix(ents[0].Name(), "rates-") {
		t.Fatalf("rates inbox: %v", ents)
	}
	if rr := call("POST", "/v1/rates/fetched", `{"nope":true}`); rr.Code != 400 {
		t.Fatalf("bad rates batch: %d", rr.Code)
	}
	// the phone's word on its network: a word the box does not know is refused; a good word with
	// no database to keep it in (this test server has none) appears down rather than lying
	if rr := call("POST", "/v1/phone/net", `{"net":"5g"}`); rr.Code != 400 {
		t.Fatalf("5g: %d", rr.Code)
	}
	if rr := call("POST", "/v1/phone/net", `{"net":"wifi"}`); rr.Code == 200 || rr.Code == 400 {
		t.Fatalf("wifi without a database: %d", rr.Code)
	}
	// the feeds' status reads the database straight: none here, so it appears down rather than
	// saying all is well
	if rr := call("GET", "/v1/feeds/status", ""); rr.Code == 200 {
		t.Fatalf("feeds status without a database: %d", rr.Code)
	}
	if rr := call("POST", "/v1/feeds/status", ""); rr.Code == 200 {
		t.Fatalf("feeds status by POST: %d", rr.Code)
	}
	// without a session: appears-down
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/v1/fetch/list", nil))
	if rr.Code == 200 {
		t.Fatal("no session answered")
	}
}
