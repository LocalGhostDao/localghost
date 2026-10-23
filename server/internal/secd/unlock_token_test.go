package secd

import (
	"encoding/json"
	"io"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/profile"
)

func pollFor(t *testing.T, s *Server, query string) map[string]any {
	t.Helper()
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/v1/unlock/poll"+query, nil))
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	return body
}

// finished marks the owner's unlock as done with slot 0 open, run "RUN".
func finished(s *Server, run string, ago time.Duration) {
	s.unlock.mu.Lock()
	s.unlock.done, s.unlock.failed, s.unlock.openSlot = true, "", profile.MainSlot
	s.unlock.runID, s.unlock.token, s.unlock.doneAt = run, "", time.Now().Add(-ago)
	s.unlock.mu.Unlock()
}

// Found on 30 Sep 2026: after the owner's unlock finished, every /v1/unlock/poll minted a live
// session token for whoever asked, with no PIN (any enrolled device, or any process on the box
// through secd's loopback port). The token now goes only to the poll naming the run the PIN started.
func TestPollGivesTheTokenOnlyToItsRun(t *testing.T) {
	s := newTestServer(t)
	finished(s, "RUN", time.Second)

	if tok, _ := pollFor(t, s, "")["token"].(string); tok != "" {
		t.Fatal("a poll with no run got a token")
	}
	if tok, _ := pollFor(t, s, "?run=OTHER")["token"].(string); tok != "" {
		t.Fatal("a poll naming another run got a token")
	}
	first, _ := pollFor(t, s, "?run=RUN")["token"].(string)
	if first == "" || !s.session.Valid(first) {
		t.Fatalf("the run did not get a live token: %q", first)
	}
	// asked again (a lost answer): the same token, not a new one that logs the first out
	if again, _ := pollFor(t, s, "?run=RUN")["token"].(string); again != first || !s.session.Valid(first) {
		t.Fatal("a repeat poll rotated the token")
	}
	// the stages stay visible to any poll (the app's bar), just not the token
	if b := pollFor(t, s, ""); b["done"] != true {
		t.Fatalf("progress hidden: %v", b)
	}
}

func TestPollTokenWindowCloses(t *testing.T) {
	s := newTestServer(t)
	finished(s, "RUN", tokenWindow+time.Second)
	if tok, _ := pollFor(t, s, "?run=RUN")["token"].(string); tok != "" {
		t.Fatal("a token handed out after the window")
	}
}

func TestUnlockStartNamesItsRun(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/v1/unlock", jsonBody(`{"pin":"1111"}`)))
	var body map[string]any
	_ = json.Unmarshal(rr.Body.Bytes(), &body)
	run, _ := body["run"].(string)
	if len(run) < 20 {
		t.Fatalf("no run id: %v", body)
	}
}

func jsonBody(s string) *stringsReader { return &stringsReader{s: s} }

type stringsReader struct {
	s string
	i int
}

func (r *stringsReader) Read(p []byte) (int, error) {
	if r.i >= len(r.s) {
		return 0, io.EOF
	}
	n := copy(p, r.s[r.i:])
	r.i += n
	return n, nil
}

// A rejected PIN revokes the live token the moment the run fails, whether or not anyone polls:
// the foreground and the notification poller go dark together (session.go's contract; the audit
// found the only revoke sat under `if u.done`, which a failed unlock never set).
func TestWrongPinRevokesThePriorToken(t *testing.T) {
	s := newTestServer(t) // an empty state dir: the default backend rejects every PIN
	prior, _ := s.session.Issue()
	if !s.session.Valid(prior) || !s.Status().HasSession {
		t.Fatal("no live token to start with")
	}
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/v1/unlock", strings.NewReader(`{"pin":"0000"}`)))
	if rr.Code != 200 {
		t.Fatalf("unlock start: %d", rr.Code)
	}
	deadline := time.Now().Add(15 * time.Second)
	for {
		s.unlock.mu.Lock()
		failed := s.unlock.failed
		s.unlock.mu.Unlock()
		if failed != "" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the rejected unlock never reported failure")
		}
		time.Sleep(20 * time.Millisecond)
	}
	// no poll happened; the token is gone all the same
	if s.session.Valid(prior) || s.Status().HasSession {
		t.Fatal("the prior token survived a wrong PIN")
	}
}
