package secd

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestServeStaticETag(t *testing.T) {
	p := filepath.Join(t.TempDir(), "index.bin")
	os.WriteFile(p, []byte("LGI1...."), 0o644)
	rec := httptest.NewRecorder()
	if !serveStatic(rec, httptest.NewRequest(http.MethodGet, "/", nil), p, "application/octet-stream") || rec.Code != 200 || rec.Body.String() != "LGI1...." {
		t.Fatalf("first: %d %q", rec.Code, rec.Body.String())
	}
	etag := rec.Header().Get("ETag")
	req := httptest.NewRequest(http.MethodGet, "/", nil)
	req.Header.Set("If-None-Match", etag)
	rec = httptest.NewRecorder()
	if !serveStatic(rec, req, p, "application/octet-stream") || rec.Code != http.StatusNotModified || rec.Body.Len() != 0 {
		t.Fatalf("revalidate: %d", rec.Code)
	}
	if serveStatic(httptest.NewRecorder(), req, p+".missing", "x") {
		t.Fatal("a missing file served")
	}
}
