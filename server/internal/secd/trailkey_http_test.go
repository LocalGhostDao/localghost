package secd

import (
	"bytes"
	"crypto/ecdh"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The phone hands its key over, reads it back, and an upload of sealed points reaches the spool
// opened; before the key is here the upload is refused (409) so the phone keeps it.
func TestTrailKeyAndSealedUpload(t *testing.T) {
	s := newTestServer(t)
	s.mu.Lock()
	s.mounted = 0
	s.mu.Unlock()
	if err := os.MkdirAll(filepath.Join(s.cfg.StateDir, "mnt", "slot0"), 0o755); err != nil {
		t.Fatal(err)
	}
	tok, err := s.session.Issue()
	if err != nil {
		t.Fatal(err)
	}
	do := func(method, path string, body []byte) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewReader(body))
		req.Header.Set("Authorization", "Bearer "+tok)
		req.Header.Set("X-Client-Cert", "-----BEGIN%20CERTIFICATE-----phone-one")
		s.Handler().ServeHTTP(rr, req)
		return rr
	}
	k, _ := ecdh.X25519().GenerateKey(rand.Reader)
	line, _ := sealTrail(k.PublicKey(), "1790000000 38.25 20.625 9")
	batch, _ := json.Marshal(map[string]any{"source": "phone-ab", "points": []any{}, "sealed": []string{line}})

	if rr := do("POST", "/v1/locations", batch); rr.Code != 409 {
		t.Fatalf("sealed upload with no key here: %d %s", rr.Code, rr.Body)
	}
	if rr := do("GET", "/v1/trail/key", nil); !strings.Contains(rr.Body.String(), `"have":false`) {
		t.Fatalf("no key yet: %s", rr.Body)
	}
	enc := base64.StdEncoding.EncodeToString
	reg, _ := json.Marshal(map[string]string{"public": enc(k.PublicKey().Bytes()), "private": enc(k.Bytes())})
	if rr := do("POST", "/v1/trail/key", reg); rr.Code != 200 {
		t.Fatalf("hand over: %d %s", rr.Code, rr.Body)
	}
	rr := do("GET", "/v1/trail/key", nil)
	var got struct {
		Have    bool   `json:"have"`
		Private string `json:"private"`
	}
	json.Unmarshal(rr.Body.Bytes(), &got)
	if !got.Have || got.Private != enc(k.Bytes()) || rr.Header().Get("Cache-Control") != "no-store" {
		t.Fatalf("read back: %s", rr.Body)
	}
	if rr := do("POST", "/v1/locations", batch); rr.Code != 202 {
		t.Fatalf("sealed upload: %d %s", rr.Code, rr.Body)
	}
	spool := filepath.Join(s.cfg.StateDir, "mnt", "slot0", "frames", "incoming-locations")
	ents, _ := os.ReadDir(spool)
	if len(ents) != 1 {
		t.Fatalf("spool: %v", ents)
	}
	b, _ := os.ReadFile(filepath.Join(spool, ents[0].Name()))
	if !strings.Contains(string(b), `"lat":38.25`) || strings.Contains(string(b), "s1:") {
		t.Fatalf("spooled: %s", b)
	}
	// another device cannot read this one's key
	rr2 := httptest.NewRecorder()
	req := httptest.NewRequest("GET", "/v1/trail/key", nil)
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("X-Client-Cert", "-----BEGIN%20CERTIFICATE-----phone-two")
	s.Handler().ServeHTTP(rr2, req)
	if !strings.Contains(rr2.Body.String(), `"have":false`) {
		t.Fatalf("another device read the key: %s", rr2.Body)
	}
	// a mismatched pair is refused
	other, _ := ecdh.X25519().GenerateKey(rand.Reader)
	bad, _ := json.Marshal(map[string]string{"public": enc(k.PublicKey().Bytes()), "private": enc(other.Bytes())})
	if rr := do("POST", "/v1/trail/key", bad); rr.Code != 503 { // the 400 is folded into the down page at the door
		t.Fatalf("mismatched pair: %d", rr.Code)
	}
}
