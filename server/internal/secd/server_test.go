package secd

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func newTestServer(t *testing.T) *Server {
	t.Helper()
	s, err := New(Config{StateDir: t.TempDir()})
	if err != nil {
		t.Fatal(err)
	}
	return s
}

func TestHealthIsReachableWithoutAccount(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/v1/health", nil))
	if rr.Code != 200 {
		t.Fatalf("health should be 200, got %d", rr.Code)
	}
	var body map[string]any
	json.Unmarshal(rr.Body.Bytes(), &body)
	if body["ok"] != true {
		t.Fatal("health should report ok")
	}
}

func TestInfoLockedBeforeUnlock(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/v1/info", nil))
	var body map[string]any
	json.Unmarshal(rr.Body.Bytes(), &body)
	if body["locked"] != true {
		t.Fatal("info should report locked before any unlock")
	}
}

func TestModelsEmptyCatalogReturnsList(t *testing.T) {
	s := newTestServer(t)
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/v1/models", nil))
	if rr.Code != 200 {
		t.Fatalf("models endpoint should be 200, got %d", rr.Code)
	}
	if !strings.Contains(rr.Body.String(), "models") {
		t.Fatal("models response should contain a models field")
	}
}

func TestUnlockStartThenPollProgresses(t *testing.T) {
	s := newTestServer(t)
	// start
	rr := httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("POST", "/v1/unlock", strings.NewReader(`{"pin":"1111"}`)))
	if rr.Code != 200 {
		t.Fatalf("unlock start should be 200, got %d", rr.Code)
	}
	// poll a few times; should report stages (we cannot easily wait for done in a unit test, but the
	// poll must return the expected shape with a stages array)
	pr := httptest.NewRecorder()
	s.Handler().ServeHTTP(pr, httptest.NewRequest("GET", "/v1/unlock/poll", nil))
	var body map[string]any
	json.Unmarshal(pr.Body.Bytes(), &body)
	if _, ok := body["stages"]; !ok {
		t.Fatal("poll must return a stages array")
	}
}

var _ = http.MethodGet
