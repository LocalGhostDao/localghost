package main

// THE BOX'S OWN FORECAST. tallyd pulls the forecast models' grids from the weather centres
// (internal/nwp: ECMWF's IFS, DWD's ICON-EU, NOAA's GFS), reduces each new run to the box's
// place list, and blends the models into one forecast per place, written into the same table
// the Open-Meteo pull filled (weather_places), so the phone reads it unchanged. Once a run has
// been read, the API pull stands down (GHOST_WEATHER_API=1 keeps it); docs/WEATHER.md says why.
// `ghost-cli ghost.tallyd weather nwp=1` pulls and computes now.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"math"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/dem"
	"github.com/LocalGhostDao/localghost/server/internal/feedstat"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/nwp"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"github.com/LocalGhostDao/localghost/server/internal/tzgrid"
	"github.com/LocalGhostDao/localghost/server/internal/weather"
)

const (
	nwpEvery     = 30 * time.Minute // how often the loop looks for a new run
	nwpKeepRuns  = 2                // runs kept on the volume per model
	nwpStaleAt   = 36 * time.Hour   // an index older than this no longer stands down the API pull
	nwpRecompute = time.Hour        // the table is computed again this often, for the hour "now"
)

// nwpModelState is one model's last pull.
type nwpModelState struct {
	Skipped string `json:"skipped,omitempty"` // a regional model the phone is not in the region of
	Run     string `json:"run,omitempty"`     // "2026-10-10 06Z"
	At      int64  `json:"at,omitempty"`      // when it was pulled
	Fields  int    `json:"fields,omitempty"`
	Missing int    `json:"missing,omitempty"`
	Bytes   int64  `json:"bytes,omitempty"`
	TookS   int    `json:"tookS,omitempty"`
	Error   string `json:"error,omitempty"`
	Pulling bool   `json:"pulling,omitempty"`
}

// nwpState is what the ctl command and INTEGRATIONS see.
type nwpState struct {
	mu        sync.Mutex
	models    map[string]*nwpModelState
	computed  int64 // the last index, unix
	places    int
	runs      []string
	here      string // the forecast at the fix: its runs, or why there is none
	tookS     int
	err       string
	computing bool
}

func (n *nwpState) snapshot() map[string]any {
	n.mu.Lock()
	defer n.mu.Unlock()
	models := map[string]any{}
	for id, m := range n.models {
		models[id] = *m
	}
	out := map[string]any{"models": models, "active": n.computed > 0 && time.Since(time.Unix(n.computed, 0)) < nwpStaleAt,
		"pace": "a look for a new run every " + nwpEvery.String() + "; the models: " + modelLine()}
	if n.computed > 0 {
		out["computedAt"] = n.computed
		out["places"] = n.places
		out["runs"] = n.runs
		out["tookS"] = n.tookS
		out["here"] = n.here
	}
	if n.err != "" {
		out["error"] = n.err
	}
	if n.computing {
		out["computing"] = true
	}
	return out
}

func modelLine() string {
	s := ""
	for i, m := range nwp.Models {
		if i > 0 {
			s += ", "
		}
		s += m.Name + " (" + m.Centre + ")"
	}
	return s
}

// nwpMarker is the file that says the index stands: <mount>/weather/index.json.
func nwpMarker(mount string) string { return filepath.Join(mount, "weather", "index.json") }

type nwpMarkerFile struct {
	At     int64    `json:"at"`
	Places int      `json:"places"`
	Runs   []string `json:"runs"`
}

// nwpActive says whether the box's own forecast is in the table and fresh: the API pull
// stands down while it is.
func nwpActive(mount string) bool {
	b, err := os.ReadFile(nwpMarker(mount))
	if err != nil {
		return false
	}
	var m nwpMarkerFile
	if json.Unmarshal(b, &m) != nil || m.At == 0 {
		return false
	}
	return time.Since(time.Unix(m.At, 0)) < nwpStaleAt
}

// nwpLoop pulls new runs as the centres publish them and computes the index.
func nwpLoop(ctx context.Context, mount string, st *nwpState, lg *slog.Logger, force <-chan struct{}) {
	st.mu.Lock()
	if st.models == nil {
		st.models = map[string]*nwpModelState{}
	}
	st.mu.Unlock()
	if b, err := os.ReadFile(nwpMarker(mount)); err == nil {
		var m nwpMarkerFile
		if json.Unmarshal(b, &m) == nil {
			st.mu.Lock()
			st.computed, st.places, st.runs = m.At, m.Places, m.Runs
			st.mu.Unlock()
		}
	}
	if os.Getenv("GHOST_WEATHER_RUNS") == "2" {
		nwp.RunsADay = 2 // a metered line: the 00 and 12 runs only
	}
	fetcher := nwp.NewFetcher()
	var db *poltergres.ReadWrite
	open := func() bool {
		if db != nil {
			return true
		}
		cfg, err := hw.LoadServicesConfig(mount)
		if err != nil {
			return false
		}
		db = poltergres.NewReadWrite(hw.SocketForMount(mount), cfg.Postgres.Port, cfg.Postgres.RWUser, cfg.Postgres.RWPass, cfg.Postgres.Name)
		return true
	}
	pass := func(forced bool) {
		if !open() {
			return
		}
		places, err := weather.Places(db)
		if err != nil || len(places) == 0 {
			if forced {
				lg.Info("weather index: no places (the geo set is not on the box)", "fn", "nwpLoop", "err", err)
			}
			return
		}
		cells := make([]nwp.Cell, len(places))
		for i, p := range places {
			cells[i] = nwp.Cell{ID: p.ID, Lat: float32(p.Lat), Lon: float32(p.Lon)}
		}
		now := time.Now()
		// the phone's last fix, when it is recent: each run keeps the model's grid around it
		// for the forecast where the person is, and the regional models are pulled for it
		fix := hereFix(db, now)
		newRuns := 0
		for _, m := range nwp.Models {
			if ctx.Err() != nil {
				return
			}
			ms := st.model(m.ID)
			if m.Keyed {
				fetcher.MetOffice = metOfficeSettings(db)
				if fetcher.MetOffice == nil {
					st.mu.Lock()
					ms.Error = ""
					ms.Skipped = "no Met Office key and order in SETTINGS"
					st.mu.Unlock()
					continue
				}
			}
			if m.Regional && (fix == nil || !m.Domain.Has(fix.Lat, fix.Lon)) {
				st.mu.Lock()
				ms.Error = ""
				ms.Skipped = "the phone is not in its region"
				st.mu.Unlock()
				continue
			}
			st.mu.Lock()
			ms.Skipped = ""
			st.mu.Unlock()
			for _, run := range nwp.Candidates(m, now) {
				path := nwp.Path(mount, m.ID, run)
				if _, err := os.Stat(path); err == nil {
					break // the newest published run is on the box already
				}
				if !fetcher.Complete(ctx, m, run) {
					continue // not all published yet: the run before, if it is newer than ours
				}
				st.mu.Lock()
				ms.Pulling = true
				st.mu.Unlock()
				lg.Info("weather index: pulling a run", "fn", "nwpLoop", "model", m.ID, "run", run.Format("2006-01-02 15Z"))
				r, p, err := fetcher.Pull(ctx, m, run, cells, fix)
				st.mu.Lock()
				ms.Pulling = false
				ms.At, ms.Fields, ms.Missing, ms.Bytes, ms.TookS, ms.Error = time.Now().Unix(), p.Fields, p.Missing, p.Bytes, int(p.Took.Seconds()), p.LastErr
				st.mu.Unlock()
				_ = feedstat.Log(db, now, []feedstat.Entry{{Source: "nwp:" + m.ID, Kind: feedstat.KindWeather, By: "box", Status: 200,
					OK: err == nil && nwp.Enough(r, m), TookMs: int(p.Took.Milliseconds()), Bytes: int(p.Bytes), Items: p.Fields, Error: p.LastErr}})
				if err != nil {
					lg.Warn("weather index: the pull stopped", "fn", "nwpLoop", "model", m.ID, "err", err)
					return
				}
				if !nwp.Enough(r, m) {
					lg.Warn("weather index: too little of the run came", "fn", "nwpLoop", "model", m.ID, "run", run.Format("2006-01-02 15Z"), "fields", p.Fields, "missing", p.Missing, "lastErr", p.LastErr)
					st.mu.Lock()
					ms.Error = "too little of the run came: " + p.LastErr
					st.mu.Unlock()
					break
				}
				if err := nwp.Save(path, r); err != nil {
					lg.Warn("weather index: the run could not be kept", "fn", "nwpLoop", "err", err)
					break
				}
				st.mu.Lock()
				ms.Run = run.Format("2006-01-02 15Z")
				st.mu.Unlock()
				lg.Info("weather index: run kept", "fn", "nwpLoop", "model", m.ID, "run", run.Format("2006-01-02 15Z"), "fields", p.Fields, "missing", p.Missing,
					"mb", p.Bytes>>20, "requests", p.Requests, "took", p.Took.Round(time.Second))
				newRuns++
				break
			}
		}
		st.mu.Lock()
		computedAt := st.computed
		st.mu.Unlock()
		if newRuns > 0 || forced || time.Since(time.Unix(computedAt, 0)) >= nwpRecompute {
			nwpCompute(mount, db, st, places, fix, lg)
		}
		if removed := nwp.Prune(mount, nwpKeepRuns); len(removed) > 0 {
			lg.Debug("weather index: old runs removed", "fn", "nwpLoop", "removed", removed)
		}
	}
	select {
	case <-ctx.Done():
		return
	case <-time.After(2 * time.Minute):
	}
	pass(false)
	t := time.NewTicker(nwpEvery)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			pass(false)
		case <-force:
			pass(true)
		}
	}
}

func (n *nwpState) model(id string) *nwpModelState {
	n.mu.Lock()
	defer n.mu.Unlock()
	m := n.models[id]
	if m == nil {
		m = &nwpModelState{}
		n.models[id] = m
	}
	return m
}

// hereFreshFor is how old the phone's last fix may be and still be where the person is.
const hereFreshFor = 48 * time.Hour

// hereFix is the phone's last fix when it is recent, nil otherwise.
func hereFix(db *poltergres.ReadWrite, now time.Time) *nwp.Fix {
	ts, lat, lon := hw.TrailNewest(db)
	if ts == 0 || now.Unix()-ts > int64(hereFreshFor/time.Second) {
		return nil
	}
	return &nwp.Fix{Lat: lat, Lon: lon}
}

// nwpCompute blends the runs on the volume into the table: every place of the list, and the
// point the phone is at (the HereID row, from the runs' windows around the fix).
func nwpCompute(mount string, db *poltergres.ReadWrite, st *nwpState, places []weather.Place, fix *nwp.Fix, lg *slog.Logger) {
	runs, err := nwp.Available(mount, nwp.Models, nwpKeepRuns)
	if err != nil || len(runs) == 0 {
		return
	}
	st.mu.Lock()
	st.computing = true
	st.mu.Unlock()
	defer func() {
		st.mu.Lock()
		st.computing = false
		st.mu.Unlock()
	}()
	t0 := time.Now()
	var zones nwp.ZoneFinder
	if l, err := tzgrid.Open(filepath.Join(mount, "geo", "tz", "grid.bin")); err == nil {
		zones = l
		defer l.Close()
	}
	var heights nwp.Heights
	if d := dem.Open(filepath.Join(mount, "geo", "elevation")); d != nil && d.Packs()+d.Tiles() > 0 {
		heights = d
	}
	fs := nwp.Compute(runs, places, t0, zones, heights)
	if len(fs) == 0 {
		st.mu.Lock()
		st.err = "the runs on the box cover none of the places"
		st.mu.Unlock()
		return
	}
	// where the person is: the exact point, named after the nearest listed town
	here := "no recent fix from the phone"
	if fix != nil {
		p := weather.Place{ID: weather.HereID, Lat: fix.Lat, Lon: fix.Lon}
		if near, _, ok := weather.NearestListed(db, fix.Lat, fix.Lon); ok {
			p.Name, p.Country, p.Population = near.Place.Name, near.Place.Country, near.Place.Population
		}
		if hf, ok := nwp.ComputeAt(runs, p, t0, zones, heights); ok {
			fs = append(fs, hf)
			here = hf.Source
		} else {
			here = "no run's window covers the fix yet (the next pull keeps one)"
			_ = db.Exec("DELETE FROM weather_places WHERE geonameid = $1", weather.HereID)
		}
	} else {
		_ = db.Exec("DELETE FROM weather_places WHERE geonameid = $1", weather.HereID)
	}
	if err := weather.Save(db, fs); err != nil {
		lg.Warn("weather index: the table could not be written", "fn", "nwpCompute", "err", err)
		st.mu.Lock()
		st.err = err.Error()
		st.mu.Unlock()
		return
	}
	var names []string
	seen := map[string]bool{}
	for _, r := range runs {
		if !seen[r.Model] {
			seen[r.Model] = true
			names = append(names, nwp.RunName(r))
		}
	}
	listed := 0
	for _, f := range fs {
		if !f.Here {
			listed++
		}
	}
	m := nwpMarkerFile{At: t0.Unix(), Places: listed, Runs: names}
	if b, err := json.Marshal(m); err == nil {
		_ = os.MkdirAll(filepath.Dir(nwpMarker(mount)), 0o700)
		_ = os.WriteFile(nwpMarker(mount), b, 0o600)
	}
	st.mu.Lock()
	st.computed, st.places, st.runs, st.tookS, st.err, st.here = t0.Unix(), listed, names, int(time.Since(t0).Seconds()), "", here
	st.mu.Unlock()
	lg.Info("weather index computed", "fn", "nwpCompute", "places", listed, "runs", names, "here", here, "heights", heights != nil, "zones", zones != nil, "took", time.Since(t0).Round(time.Second))
}

// The Met Office key and order live in the settings table, put there from SETTINGS › SERVER
// on the phone (secd's /v1/weather/metoffice, the ctl's metoffice argument). The key is read
// back only as "set"; it never goes out again.
const (
	metOfficeKeySetting   = "weather_metoffice_key"
	metOfficeOrderSetting = "weather_metoffice_order"
)

func metOfficeSettings(db *poltergres.ReadWrite) *nwp.MetOffice {
	rows, err := db.Query("SELECT key, value FROM settings WHERE key IN ($1, $2)", metOfficeKeySetting, metOfficeOrderSetting)
	if err != nil {
		return nil
	}
	var mo nwp.MetOffice
	for _, v := range rows.Vals {
		if len(v) < 2 || v[0] == nil || v[1] == nil {
			continue
		}
		switch *v[0] {
		case metOfficeKeySetting:
			mo.Key = *v[1]
		case metOfficeOrderSetting:
			mo.Order = *v[1]
		}
	}
	if mo.Key == "" || mo.Order == "" {
		return nil
	}
	return &mo
}

// setMetOffice keeps a key and an order (an empty key forgets both). The order's name is
// taken as the API wants it: lower case, spaces as hyphens.
func setMetOffice(db *poltergres.ReadWrite, key, order string) error {
	key = strings.TrimSpace(key)
	order = strings.ToLower(strings.Join(strings.Fields(strings.TrimSpace(order)), "-"))
	if key == "" {
		if err := db.Exec("DELETE FROM settings WHERE key IN ($1, $2)", metOfficeKeySetting, metOfficeOrderSetting); err != nil {
			return err
		}
		return nil
	}
	for k, v := range map[string]string{metOfficeKeySetting: key, metOfficeOrderSetting: order} {
		if err := db.Exec("INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", k, v); err != nil {
			return err
		}
	}
	return nil
}

// metOfficeState is what the phone sees: whether a key is set, the order, and the model's
// last pull; never the key.
func metOfficeState(db *poltergres.ReadWrite, ns *nwpState) map[string]any {
	out := map[string]any{"set": false}
	if mo := metOfficeSettings(db); mo != nil {
		out["set"] = true
		out["order"] = mo.Order
	}
	ns.mu.Lock()
	if m := ns.models["ukv"]; m != nil {
		out["model"] = *m
	}
	ns.mu.Unlock()
	return out
}

// homeHint is where the box thinks home is, for the Met Office order's region: the trail's
// nights, the nearest listed town, and a region a degree either side.
func homeHint(db *poltergres.ReadWrite) map[string]any {
	lat, lon, nights, ok := hw.HomeGuess(db, time.Now())
	if !ok {
		return map[string]any{"known": false, "nights": nights, "note": "the box needs a few nights of the trail to say where home is"}
	}
	out := map[string]any{"known": true, "lat": math.Round(lat*100) / 100, "lon": math.Round(lon*100) / 100, "nights": nights,
		"region": fmt.Sprintf("%.1f to %.1f N, %.1f to %.1f E", lat-1, lat+1, lon-1, lon+1)}
	if near, km, ok := weather.NearestListed(db, lat, lon); ok {
		out["near"] = near.Place.Name
		out["nearKm"] = math.Round(km)
	}
	return out
}
