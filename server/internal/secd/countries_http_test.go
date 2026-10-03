package secd

import (
	"encoding/json"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/landtiles"
	"github.com/LocalGhostDao/localghost/server/internal/roadtiles"
)

// A two-country world; the box holds a street tile and a main-road tile inside the first and a
// coast tile on its one-degree cell, nothing for the second. The list says so, with sizes; the
// country answer carries the keys; the list follows a new tile without a restart.
func TestCountriesFromTheWorldAndTheIndexes(t *testing.T) {
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
	mount := filepath.Join(s.cfg.StateDir, "mnt", "slot0")
	// no world file yet: an empty list, not appears-down
	rr := get("/v1/geo/countries")
	if rr.Code != 200 {
		t.Fatalf("no world: %d", rr.Code)
	}
	var empty countriesDoc
	_ = json.Unmarshal(rr.Body.Bytes(), &empty)
	if len(empty.Countries) != 0 {
		t.Fatalf("no world: %+v", empty)
	}
	os.MkdirAll(filepath.Join(mount, "geo"), 0o755)
	world := `{"type":"FeatureCollection","features":[
	{"type":"Feature","properties":{"NAME":"Squareland","ISO_A2":"SQ"},"geometry":{"type":"Polygon","coordinates":[[[10.02,40.02],[12.02,40.02],[12.02,42.02],[10.02,42.02],[10.02,40.02]]]}},
	{"type":"Feature","properties":{"NAME":"Elsewhere","ISO_A2":"EW"},"geometry":{"type":"Polygon","coordinates":[[[-50.02,-20.02],[-48.02,-20.02],[-48.02,-18.02],[-50.02,-18.02],[-50.02,-20.02]]]}}]}`
	os.WriteFile(filepath.Join(mount, "geo", "world.geojson"), []byte(world), 0o644)

	// tiles: a street cell at (11.05, 41.05), its one-degree cell, and the coast tile there
	rdir := filepath.Join(mount, "roadtiles")
	os.MkdirAll(filepath.Join(rdir, "0"), 0o755)
	os.MkdirAll(filepath.Join(rdir, "1"), 0o755)
	fine := roadtiles.CellAt(0, 11.05, 41.05)
	major := roadtiles.CellAt(1, 11.05, 41.05)
	tile := roadtiles.Tile{Cell: fine, Pieces: []roadtiles.Piece{{Class: roadtiles.ClassPrimary, Name: "Main", Points: []uint16{0, 0, 100, 100}}}}
	os.WriteFile(filepath.Join(rdir, roadtiles.TileName(fine)), tile.Encode(), 0o644)
	os.WriteFile(filepath.Join(rdir, roadtiles.TileName(major)), tile.Encode(), 0o644)
	ix := roadtiles.NewIndex()
	ix.Set(fine)
	ix.Set(major)
	os.WriteFile(filepath.Join(rdir, "index.bin"), ix.Encode(), 0o644)
	ldir := filepath.Join(mount, "landtiles")
	os.MkdirAll(ldir, 0o755)
	states := make([]byte, landtiles.Cols*landtiles.Rows)
	lc := landtiles.Cell{X: 11 + 180, Y: 41 + 90}
	states[lc.Key()] = landtiles.Coast
	os.WriteFile(filepath.Join(ldir, "index.bin"), landtiles.EncodeIndex(states), 0o644)
	os.WriteFile(filepath.Join(ldir, landtiles.TileName(lc)), make([]byte, 1000), 0o644)

	rr = get("/v1/geo/countries")
	if rr.Code != 200 {
		t.Fatalf("countries: %d", rr.Code)
	}
	var list countriesDoc
	if err := json.Unmarshal(rr.Body.Bytes(), &list); err != nil {
		t.Fatal(err)
	}
	if !list.Roads || !list.Land || len(list.Countries) != 2 {
		t.Fatalf("list: %+v", list)
	}
	ew, sq := list.Countries[0], list.Countries[1] // name order
	if ew.Code != "EW" || ew.Streets != 0 || ew.Coast != 0 || ew.Bytes != 0 {
		t.Fatalf("elsewhere has nothing: %+v", ew)
	}
	tileLen := int64(len(tile.Encode()))
	if sq.Code != "SQ" || sq.Streets != 1 || sq.Major != 1 || sq.Coast != 1 || sq.Bytes != 2*tileLen+1000 {
		t.Fatalf("squareland: %+v (tile %d B)", sq, tileLen)
	}

	rr = get("/v1/geo/country?code=sq")
	if rr.Code != 200 {
		t.Fatalf("country: %d", rr.Code)
	}
	var c countryDoc
	if err := json.Unmarshal(rr.Body.Bytes(), &c); err != nil {
		t.Fatal(err)
	}
	if len(c.Fine) != 1 || int(c.Fine[0]) != fine.Key() || len(c.MajorKeys) != 1 || int(c.MajorKeys[0]) != major.Key() ||
		len(c.CoastKeys) != 1 || int(c.CoastKeys[0]) != lc.Key() {
		t.Fatalf("keys: %+v (want %d %d %d)", c, fine.Key(), major.Key(), lc.Key())
	}
	if rr := get("/v1/geo/country?code=XX"); rr.Code != 404 {
		t.Fatalf("unknown country: %d", rr.Code)
	}
	if rr := get("/v1/geo/country?code=TOOLONG"); rr.Code == 200 || rr.Code == 404 {
		t.Fatalf("bad code: %d, want appears-down", rr.Code)
	}
	// the country a point is in: inside Squareland, inside Elsewhere, at sea, a bad point
	rr = get("/v1/geo/at?lat=41.05&lon=11.05")
	var at whereDoc
	if rr.Code != 200 || json.Unmarshal(rr.Body.Bytes(), &at) != nil || at.Country != "SQ" || at.Name != "Squareland" {
		t.Fatalf("at: %d %s", rr.Code, rr.Body.String())
	}
	if rr = get("/v1/geo/at?lat=-19&lon=-49"); rr.Code != 200 || !strings.Contains(rr.Body.String(), `"EW"`) {
		t.Fatalf("at elsewhere: %d %s", rr.Code, rr.Body.String())
	}
	if rr := get("/v1/geo/at?lat=0&lon=0"); rr.Code != 404 {
		t.Fatalf("at sea: %d", rr.Code)
	}
	if rr := get("/v1/geo/at?lat=91&lon=0"); rr.Code == 200 || rr.Code == 404 {
		t.Fatalf("bad point: %d, want appears-down", rr.Code)
	}

	// a second street tile lands and the index is rewritten: the list follows
	fine2 := roadtiles.CellAt(0, 11.55, 41.55)
	os.WriteFile(filepath.Join(rdir, roadtiles.TileName(fine2)), tile.Encode(), 0o644)
	ix.Set(fine2)
	os.WriteFile(filepath.Join(rdir, "index.bin"), ix.Encode(), 0o644)
	// the key is mtime+size; the size is the same, so nudge the mtime past the second
	past := time.Now().Add(-time.Hour)
	os.Chtimes(filepath.Join(rdir, "index.bin"), past, past)
	rr = get("/v1/geo/countries")
	_ = json.Unmarshal(rr.Body.Bytes(), &list)
	if list.Countries[1].Streets != 2 {
		t.Fatalf("after a new tile: %+v", list.Countries[1])
	}
}
