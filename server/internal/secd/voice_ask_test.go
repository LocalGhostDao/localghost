package secd

import (
	"bytes"
	"errors"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LocalGhostDao/localghost/server/internal/voiced"
)

// A question asked aloud: the WAV lands under voiced/ask for ghost.voiced, the words come back, the
// file is gone; a body that is not a WAV is refused; what voiced says when it cannot hear comes
// back as why, not as an error.
func TestVoiceAsk(t *testing.T) {
	s := newTestServer(t)
	s.mu.Lock()
	s.mounted = 0
	s.mu.Unlock()
	mount := filepath.Join(s.cfg.StateDir, "mnt", "slot0")
	tok, _ := s.session.Issue()
	post := func(body []byte) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("POST", "/v1/voice/ask", bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		s.Handler().ServeHTTP(rr, req)
		return rr
	}
	var heard string
	old := hearAsk
	hearAsk = func(runDir, path string) (string, string, error) {
		heard = path
		if runDir != filepath.Join(mount, "run") {
			return "", "", errors.New("wrong run dir " + runDir)
		}
		if _, err := os.Stat(path); err != nil {
			return "", "", err
		}
		return " What is the weather in Corfu? ", "en", nil
	}
	t.Cleanup(func() { hearAsk = old })
	wav := append(voiced.WavHeader(16000, 1, 16, 32000), make([]byte, 32000)...)
	rr := post(wav)
	if rr.Code != 200 || !strings.Contains(rr.Body.String(), `"text":"What is the weather in Corfu?"`) || !strings.Contains(rr.Body.String(), `"heard":true`) {
		t.Fatalf("%d %s", rr.Code, rr.Body)
	}
	if !strings.HasPrefix(heard, voiced.AskDir(mount)+string(filepath.Separator)) {
		t.Fatalf("heard from %s", heard)
	}
	if _, err := os.Stat(heard); err == nil {
		t.Fatal("the question's file was kept")
	}
	if rr := post([]byte("not audio at all")); rr.Code != 400 {
		t.Fatalf("not a WAV: %d", rr.Code)
	}
	hearAsk = func(string, string) (string, string, error) {
		return "", "", errors.New("no speech engine: update.sh speech")
	}
	if rr := post(wav); !strings.Contains(rr.Body.String(), `"ok":false`) || !strings.Contains(rr.Body.String(), "no speech engine") {
		t.Fatalf("%s", rr.Body)
	}
}
