package secd

import (
	"encoding/json"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"testing"
)

// TestGeoWorldIndexAndCuts pins the landmass contract: the index lists every world*.geojson on the
// volume smallest first with a res token the world endpoint accepts, ?res= serves that file with the
// same ETag, and a res that would not be a filename is refused. The app opens on the smallest cut
// and refines with the largest, so ordering and tokens are the whole feature.
func TestGeoWorldIndexAndCuts(t *testing.T) {
	s := newTestServer(t)
	geo := filepath.Join(s.cfg.StateDir, "mnt", "slot0", "geo")
	if err := os.MkdirAll(geo, 0o755); err != nil {
		t.Fatal(err)
	}
	// Three cuts, sizes deliberately out of name order: index must sort by bytes.
	must := func(name, body string) {
		if err := os.WriteFile(filepath.Join(geo, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	must("world.geojson", `{"type":"FeatureCollection","features":[]}`+string(make([]byte, 3000)))
	must("world-110m.geojson", `{"type":"FeatureCollection","features":[]}`)
	must("world-50m.geojson", `{"type":"FeatureCollection","features":[]}`+string(make([]byte, 1000)))
	must("world-bad name.geojson", `{}`) // not offerable: the world endpoint would refuse the token
	must("notes.txt", `not a world`)
	s.mu.Lock()
	s.mounted = 0
	s.mu.Unlock()
	tok, err := s.session.Issue()
	if err != nil {
		t.Fatal(err)
	}
	get := func(path string, etag string) *httptest.ResponseRecorder {
		rr := httptest.NewRecorder()
		req := httptest.NewRequest("GET", path, nil)
		req.Header.Set("Authorization", "Bearer "+tok)
		if etag != "" {
			req.Header.Set("If-None-Match", etag)
		}
		s.Handler().ServeHTTP(rr, req)
		return rr
	}
	rr := get("/v1/geo/world/index", "")
	if rr.Code != 200 {
		t.Fatalf("index: %d %s", rr.Code, rr.Body.String())
	}
	var idx struct {
		Cuts []struct {
			Res   string `json:"res"`
			Name  string `json:"name"`
			Bytes int64  `json:"bytes"`
			ETag  string `json:"etag"`
		} `json:"cuts"`
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &idx); err != nil {
		t.Fatal(err)
	}
	if len(idx.Cuts) != 3 {
		t.Fatalf("cuts = %+v, want the three offerable files", idx.Cuts)
	}
	if idx.Cuts[0].Res != "110m" || idx.Cuts[1].Res != "50m" || idx.Cuts[2].Res != "" {
		t.Fatalf("order/res wrong: %+v", idx.Cuts)
	}
	for _, c := range idx.Cuts {
		path := "/v1/geo/world"
		if c.Res != "" {
			path += "?res=" + c.Res
		}
		got := get(path, "")
		if got.Code != 200 || got.Header().Get("ETag") != c.ETag || int64(got.Body.Len()) != c.Bytes {
			t.Fatalf("%s: code=%d etag=%q(want %q) bytes=%d(want %d)", path, got.Code, got.Header().Get("ETag"), c.ETag, got.Body.Len(), c.Bytes)
		}
		if again := get(path, c.ETag); again.Code != 304 {
			t.Fatalf("%s with matching If-None-Match: %d, want 304", path, again.Code)
		}
	}
	for _, bad := range []string{"../x", "bad name", "10M", "aVeryLongTokenIndeed"} {
		if got := get("/v1/geo/world?res="+url.QueryEscape(bad), ""); got.Code == 200 {
			t.Fatalf("res=%q must not be served", bad)
		}
	}
	if got := get("/v1/geo/world?res=missing", ""); got.Code == 200 {
		t.Fatal("an absent cut must appear down, not 200")
	}
	// No session: everything appears down, index included.
	rr = httptest.NewRecorder()
	s.Handler().ServeHTTP(rr, httptest.NewRequest("GET", "/v1/geo/world/index", nil))
	if rr.Code == 200 {
		t.Fatal("index must not answer without a session")
	}
}

// TestGeoTracksBatch pins the one-answer track feed: newest days first, the LineString only (photo
// Points are the LOD feed's business), coordinates flipped to [lat,lon], photo-only days omitted,
// the limit honoured, and no paths dir an empty list rather than appears-down.
func TestGeoTracksBatch(t *testing.T) {
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
	var got struct {
		Tracks []struct {
			Day       string       `json:"day"`
			Coords    [][2]float64 `json:"coords"`
			Times     []int64      `json:"times"`
			DistanceM float64      `json:"distanceM"`
		} `json:"tracks"`
	}
	rr := get("/v1/geo/tracks")
	if rr.Code != 200 {
		t.Fatalf("no paths dir must still answer: %d", rr.Code)
	}
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil || len(got.Tracks) != 0 {
		t.Fatalf("young box must answer an empty list: %v %s", err, rr.Body.String())
	}
	dir := filepath.Join(s.cfg.StateDir, "mnt", "slot0", "paths")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	line := func(day string, coords string, extra string) {
		doc := `{"type":"FeatureCollection","features":[
		  {"type":"Feature","geometry":{"type":"Point","coordinates":[0.1,51.5]},"properties":{"kind":"photo"}},
		  {"type":"Feature","geometry":{"type":"LineString","coordinates":[` + coords + `]},"properties":{"kind":"track","day":"` + day + `"` + extra + `}}]}`
		if err := os.WriteFile(filepath.Join(dir, day+".geojson"), []byte(doc), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	line("2026-09-16", "[-0.1,51.5],[-0.2,51.6]", "")                                                     // a day file from before times existed
	line("2026-09-18", "[10.0,45.0],[10.1,45.1],[10.2,45.2]", `,"times":[100,200,300],"distanceM":27300`) // a current one
	line("2026-09-17", "[2.0,48.0],[2.1,48.1]", `,"times":[1,2,3]`)                                       // times that do not match the line: dropped
	// photo-only day: no LineString
	if err := os.WriteFile(filepath.Join(dir, "2026-09-15.geojson"),
		[]byte(`{"type":"FeatureCollection","features":[{"type":"Feature","geometry":{"type":"Point","coordinates":[0,0]},"properties":{}}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	_ = os.WriteFile(filepath.Join(dir, "notes.geojson"), []byte(`{}`), 0o644) // not a day
	rr = get("/v1/geo/tracks?limit=2")
	if rr.Code != 200 {
		t.Fatalf("tracks: %d", rr.Code)
	}
	got.Tracks = nil
	if err := json.Unmarshal(rr.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	if len(got.Tracks) != 2 || got.Tracks[0].Day != "2026-09-18" || got.Tracks[1].Day != "2026-09-17" {
		t.Fatalf("newest two days expected, got %+v", got.Tracks)
	}
	if c := got.Tracks[0].Coords; len(c) != 3 || c[0][0] != 45.0 || c[0][1] != 10.0 {
		t.Fatalf("coords must be [lat,lon]: %+v", c)
	}
	if tm := got.Tracks[0].Times; len(tm) != 3 || tm[2] != 300 || got.Tracks[0].DistanceM != 27300 {
		t.Fatalf("times and distance must pass through: %+v", got.Tracks[0])
	}
	if got.Tracks[1].Times != nil {
		t.Fatalf("mismatched times must be dropped, not served: %+v", got.Tracks[1])
	}
	rr = get("/v1/geo/tracks")
	got.Tracks = nil
	_ = json.Unmarshal(rr.Body.Bytes(), &got)
	if len(got.Tracks) != 3 {
		t.Fatalf("default limit must return every tracked day (3), photo-only day omitted: %+v", got.Tracks)
	}
	if got.Tracks[2].Times != nil || got.Tracks[2].DistanceM != 0 {
		t.Fatalf("an old day file has no times and no distance: %+v", got.Tracks[2])
	}
}
