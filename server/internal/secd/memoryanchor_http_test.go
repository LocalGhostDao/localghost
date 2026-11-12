package secd

import (
	"net/http/httptest"
	"strings"
	"testing"
)

// The anchor and the open of a memory are the person's acts: without their session, with the
// wrong method, with no memory named, or with no volume to act on, the box answers exactly as
// it does for any path it will not serve (it appears down), never with a reason.
func TestMemoryAnchorAndOpenGuards(t *testing.T) {
	s := newTestServer(t)
	tok, err := s.session.Issue()
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path, body, token string) (int, string) {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, strings.NewReader(body))
		if token != "" {
			req.Header.Set("Authorization", "Bearer "+token)
		}
		s.Handler().ServeHTTP(rr, req)
		return rr.Code, rr.Body.String()
	}
	downCode, downBody := do("GET", "/v1/memories", "", "") // no session: the reference answer
	for _, path := range []string{"/v1/memories/anchor", "/v1/memories/open"} {
		for name, c := range map[string][3]string{
			"no session":   {"POST", `{"id":1,"on":true}`, ""},
			"wrong method": {"GET", ``, tok},
			"locked":       {"POST", `{"id":1,"on":true}`, tok}, // a session, no volume mounted
		} {
			if code, body := do(c[0], path, c[1], c[2]); code != downCode || body != downBody {
				t.Errorf("%s, %s: %d %q, want the box to appear down (%d)", path, name, code, body, downCode)
			}
		}
	}
	// mounted, but nothing named or not JSON: still down, and nothing was asked of the store
	s.mu.Lock()
	s.mounted = 0
	s.mu.Unlock()
	for _, path := range []string{"/v1/memories/anchor", "/v1/memories/open"} {
		for _, body := range []string{`{"id":0}`, `{"id":-4}`, `not json`, ``, `{"id":"1; DROP TABLE memories"}`} {
			if code, got := do("POST", path, body, tok); code != downCode || got != downBody {
				t.Errorf("%s %q: %d %q", path, body, code, got)
			}
		}
		// a body past the cap is refused before it is read whole
		if code, _ := do("POST", path, `{"id":1,"pad":"`+strings.Repeat("x", 4096)+`"}`, tok); code != downCode {
			t.Errorf("%s: an oversized body got %d", path, code)
		}
	}
	// one memory by id, a whole by ref: an id that is not a positive number and a ref no real
	// ref could be are refused before the store is asked
	for _, path := range []string{"/v1/memories?id=abc", "/v1/memories?id=0", "/v1/memories?id=-1", "/v1/memories?id=1%3B+DROP",
		"/v1/memories?ref=" + strings.Repeat("m", 65)} {
		if code, got := do("GET", path, "", tok); code != downCode || got != downBody {
			t.Errorf("%s: %d %q", path, code, got)
		}
	}
}
