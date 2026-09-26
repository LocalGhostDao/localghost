package secd

import (
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"github.com/LocalGhostDao/localghost/server/internal/roadtiles"
)

func TestRoadTilesServed(t *testing.T) {
	s := newTestServer(t)
	s.mu.Lock()
	s.mounted = 0
	s.mu.Unlock()
	tok, err := s.session.Issue()
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		s.Handler().ServeHTTP(rr, req)
		return rr
	}
	// no tiles yet: an empty index, a 404 tile
	if rr := get("/v1/geo/roadtiles/index"); rr.Code != 204 {
		t.Fatalf("no index: %d", rr.Code)
	}
	if rr := get("/v1/geo/roadtile?l=0&x=2061&y=1344"); rr.Code != 404 {
		t.Fatalf("no tile: %d", rr.Code)
	}
	dir := filepath.Join(s.cfg.StateDir, "mnt", "slot0", "roadtiles")
	os.MkdirAll(filepath.Join(dir, "0"), 0o755)
	os.MkdirAll(filepath.Join(dir, "1"), 0o755)
	c := roadtiles.Cell{Level: 0, X: 2061, Y: 1344}
	tile := roadtiles.Tile{Cell: c, Pieces: []roadtiles.Piece{{Class: roadtiles.ClassPrimary, Name: "Unirii", Points: []uint16{0, 0, 100, 100}}}}
	os.WriteFile(filepath.Join(dir, roadtiles.TileName(c)), tile.Encode(), 0o644)
	ix := roadtiles.NewIndex()
	ix.Set(c)
	os.WriteFile(filepath.Join(dir, "index.bin"), ix.Encode(), 0o644)
	rr := get("/v1/geo/roadtiles/index")
	if rr.Code != 200 {
		t.Fatalf("index: %d", rr.Code)
	}
	got, err := roadtiles.DecodeIndex(rr.Body.Bytes())
	if err != nil || !got.Has(c) {
		t.Fatalf("index body: %v", err)
	}
	rr = get("/v1/geo/roadtile?l=0&x=2061&y=1344")
	if rr.Code != 200 {
		t.Fatalf("tile: %d", rr.Code)
	}
	tl, err := roadtiles.Decode(rr.Body.Bytes())
	if err != nil || tl.Pieces[0].Name != "Unirii" {
		t.Fatalf("tile body: %v", err)
	}
	for _, bad := range []string{"/v1/geo/roadtile?l=2&x=1&y=1", "/v1/geo/roadtile?l=0&x=3600&y=1", "/v1/geo/roadtile?l=1&x=-1&y=1", "/v1/geo/roadtile?l=0&x=a&y=1"} {
		if rr := get(bad); rr.Code == 200 || rr.Code == 404 {
			t.Errorf("%s: %d, want appears-down", bad, rr.Code)
		}
	}
	// without a session: appears-down
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/v1/geo/roadtiles/index", nil))
	if rr.Code == 200 || rr.Code == 204 {
		t.Fatalf("no session: %d", rr.Code)
	}
}
