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
	var told []string
	hearAsk = func(runDir, path string, langs []string) (heardAsk, error) {
		heard, told = path, langs
		if runDir != filepath.Join(mount, "run") {
			return heardAsk{}, errors.New("wrong run dir " + runDir)
		}
		if _, err := os.Stat(path); err != nil {
			return heardAsk{}, err
		}
		return heardAsk{Text: " What is the weather in Corfu? ", Lang: "en", MS: 900}, nil
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
	if len(told) != 0 || !strings.Contains(rr.Body.String(), `"ms":`) {
		t.Fatalf("no languages were sent and %v was passed on; body %s", told, rr.Body)
	}
	// the phone's languages go with it, and only what is a language code does
	rrl := httptest.NewRecorder()
	reql := httptest.NewRequest("POST", "/v1/voice/ask", bytes.NewReader(wav))
	reql.Header.Set("Authorization", "Bearer "+tok)
	reql.Header.Set("X-Ghost-Langs", "en-GB, ro,en,../etc,x, FIL")
	s.Handler().ServeHTTP(rrl, reql)
	if rrl.Code != 200 || strings.Join(told, ",") != "en,ro,fil" {
		t.Fatalf("%d, told %v", rrl.Code, told)
	}
	if rr := post([]byte("not audio at all")); rr.Code != 400 {
		t.Fatalf("not a WAV: %d", rr.Code)
	}
	hearAsk = func(string, string, []string) (heardAsk, error) {
		return heardAsk{}, errors.New("no speech engine: update.sh speech")
	}
	if rr := post(wav); !strings.Contains(rr.Body.String(), `"ok":false`) || !strings.Contains(rr.Body.String(), "no speech engine") {
		t.Fatalf("%s", rr.Body)
	}
}
