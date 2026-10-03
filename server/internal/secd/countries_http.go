package secd

// /v1/geo/countries and /v1/geo/country?code= , whole countries for the phone's map download.
// SETTINGS › MAPS lets a person pick countries; the phone then fetches every tile the box holds
// for them (streets, main roads, coast) ahead of time. The list says, for each country, how many
// tiles the box has and how big they are, so the choice is made knowing the cost; the per-country
// answer is the cells themselves. The countries are Natural Earth's (<mount>/geo/world.geojson,
// internal/countrycells), rasterised once per secd run; the tile counts and sizes follow the
// indexes and are recomputed when either index changes.

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"

	"github.com/LocalGhostDao/localghost/server/internal/countrycells"
	"github.com/LocalGhostDao/localghost/server/internal/landtiles"
	"github.com/LocalGhostDao/localghost/server/internal/roadtiles"
)

// countryRowDoc is one country in the list.
type countryRowDoc struct {
	Code    string `json:"code"`
	Name    string `json:"name"`
	Streets int    `json:"streets"` // level-0 road tiles the box holds for it
	Major   int    `json:"major"`   // level-1 road tiles
	Coast   int    `json:"coast"`   // coast tiles
	Bytes   int64  `json:"bytes"`   // all of those on disk
}

// countriesDoc is the list answer.
type countriesDoc struct {
	Countries []countryRowDoc `json:"countries"`
	Roads     bool            `json:"roads"` // the box has road tiles at all
	Land      bool            `json:"land"`  // and coast tiles
}

// countryDoc is one country's cells: index keys (y*cols+x) on each grid, only cells with a tile.
type countryDoc struct {
	countryRowDoc
	Fine      []int32 `json:"fine"`      // level-0 road tiles
	MajorKeys []int32 `json:"majorKeys"` // level-1 road tiles
	CoastKeys []int32 `json:"coastKeys"` // coast tiles (the land grid, same keys as level 1)
}

// countryCache is the atlas and the per-index answers for one secd run.
type countryCache struct {
	mu        sync.Mutex
	atlasPath string
	atlasKey  string // mtime+size of the GeoJSON the atlas was read from
	atlas     *countrycells.Atlas
	atlasErr  error
	indexKey  string // the two indexes' mtime+size
	ts        tileSet
	rows      []countryRowDoc
	docs      map[string]*countryDoc
}

// tileSet is what the box holds: which cells have a tile and how big each is.
type tileSet struct {
	roads *roadtiles.Index
	land  []byte // the 64,800 states, nil when no coast tiles
	size  map[string]int64
	key   string
}

func fileKey(p string) string {
	fi, err := os.Stat(p)
	if err != nil {
		return "none"
	}
	return fmt.Sprintf("%d-%d", fi.ModTime().Unix(), fi.Size())
}

// tileSetKey is what the tile set is cached by: the two indexes' mtime and size (a rebuild
// swaps the index; the phone's own revalidation uses the same signal).
func tileSetKey(roadDir, landDir string) string {
	return fileKey(filepath.Join(roadDir, "index.bin")) + "|" + fileKey(filepath.Join(landDir, "index.bin"))
}

// readTileSet reads both indexes and the sizes of every tile file (one directory walk per
// level; a few hundred thousand entries for a continent, well under a second, and done once
// per index change).
func readTileSet(roadDir, landDir string) tileSet {
	ts := tileSet{size: map[string]int64{}}
	ts.key = tileSetKey(roadDir, landDir)
	if b, err := os.ReadFile(filepath.Join(roadDir, "index.bin")); err == nil {
		if ix, err := roadtiles.DecodeIndex(b); err == nil {
			ts.roads = ix
			for _, lvl := range []string{"0", "1"} {
				if ents, err := os.ReadDir(filepath.Join(roadDir, lvl)); err == nil {
					for _, e := range ents {
						if fi, err := e.Info(); err == nil && !fi.IsDir() {
							ts.size[lvl+"/"+e.Name()] = fi.Size()
						}
					}
				}
			}
		}
	}
	if b, err := os.ReadFile(filepath.Join(landDir, "index.bin")); err == nil {
		if states, err := landtiles.DecodeIndex(b); err == nil {
			ts.land = states
		}
	}
	if ts.land != nil {
		if ents, err := os.ReadDir(landDir); err == nil {
			for _, e := range ents {
				if fi, err := e.Info(); err == nil && !fi.IsDir() && strings.HasSuffix(e.Name(), ".lgt") {
					ts.size["land/"+e.Name()] = fi.Size()
				}
			}
		}
	}
	return ts
}

// countryTiles is a country's tiles against what the box holds (pure; the tests feed a tileSet).
func countryTiles(c *countrycells.Country, ts tileSet) *countryDoc {
	d := &countryDoc{Fine: []int32{}, MajorKeys: []int32{}, CoastKeys: []int32{}}
	d.Code, d.Name = c.Code, c.Name
	if ts.roads != nil {
		for _, k := range c.Fine {
			cell := roadtiles.Cell{Level: 0, X: int(k) % roadtiles.Cols(0), Y: int(k) / roadtiles.Cols(0)}
			if ts.roads.Has(cell) {
				d.Fine = append(d.Fine, k)
				d.Bytes += ts.size[roadtiles.TileName(cell)]
			}
		}
		for _, k := range c.Major {
			cell := roadtiles.Cell{Level: 1, X: int(k) % roadtiles.Cols(1), Y: int(k) / roadtiles.Cols(1)}
			if ts.roads.Has(cell) {
				d.MajorKeys = append(d.MajorKeys, k)
				d.Bytes += ts.size[roadtiles.TileName(cell)]
			}
		}
	}
	if ts.land != nil {
		for _, k := range c.Major {
			if int(k) < len(ts.land) && ts.land[k] == landtiles.Coast {
				cell := landtiles.Cell{X: int(k) % landtiles.Cols, Y: int(k) / landtiles.Cols}
				d.CoastKeys = append(d.CoastKeys, k)
				d.Bytes += ts.size["land/"+landtiles.TileName(cell)]
			}
		}
	}
	d.Streets, d.Major, d.Coast = len(d.Fine), len(d.MajorKeys), len(d.CoastKeys)
	return d
}

// countries is the cache for the mounted slot, filled on first use and when a file changes.
func (s *Server) countries() (*countryCache, tileSet, error) {
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		return nil, tileSet{}, fmt.Errorf("locked")
	}
	mount := filepath.Join(s.cfg.StateDir, "mnt", fmt.Sprintf("slot%d", mounted))
	geo := filepath.Join(mount, "geo", "world.geojson")
	s.countryMu.Lock()
	if s.countryCache == nil {
		s.countryCache = &countryCache{docs: map[string]*countryDoc{}}
	}
	cc := s.countryCache
	s.countryMu.Unlock()

	cc.mu.Lock()
	defer cc.mu.Unlock()
	if k := fileKey(geo); cc.atlas == nil || cc.atlasPath != geo || cc.atlasKey != k {
		cc.atlasPath, cc.atlasKey = geo, k
		cc.atlas, cc.atlasErr = countrycells.Load(geo)
		cc.indexKey, cc.rows, cc.docs = "", nil, map[string]*countryDoc{}
	}
	if cc.atlasErr != nil {
		return cc, tileSet{}, cc.atlasErr
	}
	roadDir, landDir := filepath.Join(mount, "roadtiles"), filepath.Join(mount, "landtiles")
	if key := tileSetKey(roadDir, landDir); key != cc.indexKey {
		ts := readTileSet(roadDir, landDir)
		cc.indexKey, cc.ts, cc.rows, cc.docs = ts.key, ts, nil, map[string]*countryDoc{}
		for i := range cc.atlas.Countries {
			d := countryTiles(&cc.atlas.Countries[i], ts)
			cc.docs[d.Code] = d
			cc.rows = append(cc.rows, d.countryRowDoc)
		}
		sort.SliceStable(cc.rows, func(i, j int) bool { return cc.rows[i].Name < cc.rows[j].Name })
	}
	return cc, cc.ts, nil
}

// handleCountries , GET /v1/geo/countries , every country with what the box holds for it.
func (s *Server) handleCountries(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	cc, ts, err := s.countries()
	if err != nil {
		if cc != nil && cc.atlasErr != nil {
			// no world file: an honest empty list, the map's base is missing too
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(countriesDoc{Countries: []countryRowDoc{}})
			return
		}
		s.appearsDown(w)
		return
	}
	cc.mu.Lock()
	rows := append([]countryRowDoc(nil), cc.rows...)
	cc.mu.Unlock()
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(countriesDoc{Countries: rows, Roads: ts.roads != nil, Land: ts.land != nil})
}

// whereDoc is /v1/geo/at's answer: the country a point is in.
type whereDoc struct {
	Country string `json:"country"` // ISO 3166-1 alpha-2 (Natural Earth's ADM0_A3 for a unit without one)
	Name    string `json:"name"`
}

// handleAt , GET /v1/geo/at?lat=&lon= , the country a point is in, from the box's own Natural
// Earth polygons. The phone asks this for the country the lock-screen phrases follow (it asked the
// OS geocoder before, which on most phones is a network call to Google with the position; the
// box's answer leaves nothing anywhere). 404 at sea or outside every polygon.
func (s *Server) handleAt(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	lat, err1 := strconv.ParseFloat(r.URL.Query().Get("lat"), 64)
	lon, err2 := strconv.ParseFloat(r.URL.Query().Get("lon"), 64)
	if err1 != nil || err2 != nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		s.appearsDown(w)
		return
	}
	cc, _, err := s.countries()
	if err != nil {
		s.appearsDown(w)
		return
	}
	cc.mu.Lock()
	code, name, ok := cc.atlas.At(lat, lon)
	cc.mu.Unlock()
	if !ok {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(whereDoc{Country: code, Name: name})
}

// handleCountry , GET /v1/geo/country?code=GR , one country's tiles as index keys.
func (s *Server) handleCountry(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	code := strings.ToUpper(r.URL.Query().Get("code"))
	if code == "" || len(code) > 3 {
		s.appearsDown(w)
		return
	}
	cc, _, err := s.countries()
	if err != nil {
		s.appearsDown(w)
		return
	}
	cc.mu.Lock()
	d := cc.docs[code]
	cc.mu.Unlock()
	if d == nil {
		w.WriteHeader(http.StatusNotFound)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(d)
}
