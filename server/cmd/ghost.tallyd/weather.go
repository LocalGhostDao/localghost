package main

// THE WEATHER PULL. The box asks Open-Meteo for the forecast of the world's larger places, the
// same list whoever and wherever the person is (internal/weather says why: the forecast where
// they are is then looked up on the box, and no weather service learns where that is), a
// hundred places every two minutes, the longest unpulled first, each pulled again once its row
// is sixteen hours old. `ghost-cli ghost.tallyd weather fetch=1` pulls the next batch now, `weather lat=
// lon=` or `weather place=` reads the table the way the chat does.

import (
	"context"
	"encoding/json"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/egress"
	"github.com/LocalGhostDao/localghost/server/internal/feedstat"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"github.com/LocalGhostDao/localghost/server/internal/weather"
)

// weatherState is what the last batch did and how the day's pull goes, for the ctl command.
type weatherState struct {
	mu      sync.Mutex
	at      time.Time
	last    weather.Result
	noGeo   bool   // the geo set is not on the box: nothing to pull
	readErr string // the place list could not be read (the last error, cleared by a read that worked)
	running bool
	batches int // since start
	places  int // pulled since start
	failed  int // batches that failed since start
}

func (w *weatherState) snapshot() map[string]any {
	w.mu.Lock()
	defer w.mu.Unlock()
	out := map[string]any{"running": w.running, "noGeo": w.noGeo, "batchesSinceStart": w.batches, "placesSinceStart": w.places, "failedSinceStart": w.failed,
		"pace": "a batch of " + itoa(weather.Batch) + " every " + weather.BatchEvery.String() + ", a place again after " + weather.Every.String()}
	if !w.at.IsZero() {
		out["lastAt"] = w.at.Unix()
		out["last"] = w.last
	}
	if w.noGeo {
		out["note"] = "no places: the geo set (GeoNames) is not on the box; tools/fetch_geo.sh brings it, then the pull runs"
	}
	if w.readErr != "" {
		out["error"] = "the place list could not be read: " + w.readErr
	}
	return out
}

// weatherLoop runs the pull when it is due: a look at start, then every hour.
func weatherLoop(ctx context.Context, mount string, ws *weatherState, lg *slog.Logger, force <-chan struct{}) {
	client := egress.New()
	var db *poltergres.ReadWrite
	pass := func(forced bool) {
		if db == nil {
			cfg, err := hw.LoadServicesConfig(mount)
			if err != nil {
				return
			}
			db = poltergres.NewReadWrite(hw.SocketForMount(mount), cfg.Postgres.Port, cfg.Postgres.RWUser, cfg.Postgres.RWPass, cfg.Postgres.Name)
		}
		now := time.Now()
		// the box's own forecast (nwp.go) fills the table once it has a run: the API is then
		// left alone, unless GHOST_WEATHER_API=1 keeps it for a comparison
		if nwpActive(mount) && os.Getenv("GHOST_WEATHER_API") != "1" {
			if forced {
				lg.Info("weather: the box's own forecast stands; the API pull is off (GHOST_WEATHER_API=1 keeps it)", "fn", "weatherLoop")
			}
			return
		}
		// the hundred longest unpulled; pulled when the oldest is Every old (or never pulled),
		// or on fetch=1
		batch, due, err := weather.NextBatch(db, now)
		if err != nil {
			lg.Warn("weather: the places could not be read", "fn", "weatherLoop", "err", err)
			ws.mu.Lock()
			ws.readErr = err.Error()
			ws.mu.Unlock()
			return
		}
		ws.mu.Lock()
		ws.noGeo = len(batch) == 0
		ws.readErr = ""
		ws.mu.Unlock()
		if len(batch) == 0 {
			if forced {
				lg.Info("weather: no places to pull (the geo set is not on the box)", "fn", "weatherLoop")
			}
			return
		}
		if !due && !forced {
			return
		}
		ws.mu.Lock()
		ws.running = true
		ws.mu.Unlock()
		r := weather.Pass(ctx, db, client, batch, now, nil)
		entries := make([]feedstat.Entry, 0, len(r.Fetches))
		for _, f := range r.Fetches {
			entries = append(entries, feedstat.Entry{Source: weather.Source, Kind: feedstat.KindWeather, By: "box",
				Status: f.Status, OK: f.OK, TookMs: f.TookMs, Bytes: f.Bytes, Items: f.Items, Error: f.Error})
		}
		if len(entries) > 0 {
			_ = feedstat.Log(db, now, entries)
		}
		ws.mu.Lock()
		ws.at, ws.last, ws.running = time.Now(), r, false
		ws.batches += r.Batches
		ws.places += r.Places
		ws.failed += r.Failed
		ws.mu.Unlock()
		if r.Failed > 0 {
			lg.Warn("weather batch failed", "fn", "weatherLoop", "places", len(batch), "err", r.LastErr, "took", r.Took.Round(time.Second))
		} else {
			lg.Debug("weather batch pulled", "fn", "weatherLoop", "places", r.Places, "took", r.Took.Round(time.Second))
		}
	}
	// a moment after start, so the databases are up and the tickers' first minute is not
	// crowded, then a batch every BatchEvery while there is one due
	select {
	case <-ctx.Done():
		return
	case <-time.After(90 * time.Second):
	}
	pass(false)
	t := time.NewTicker(weather.BatchEvery)
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

// weatherCtl answers `ghost-cli ghost.tallyd weather [lat= lon= | place=] [fetch=1] [nwp=1]`.
func weatherCtl(mount string, ws *weatherState, ns *nwpState, force chan<- struct{}, forceNWP chan<- struct{}, args json.RawMessage) (map[string]any, error) {
	var a struct {
		Lat   float64 `json:"lat"`
		Lon   float64 `json:"lon"`
		Place string  `json:"place"`
		Fetch bool    `json:"fetch"`
		NWP   bool    `json:"nwp"`
		// the Met Office key and order from SETTINGS; a key of "" forgets them
		MetOffice *struct {
			Key   string `json:"key"`
			Order string `json:"order"`
		} `json:"metoffice"`
		Home bool `json:"home"` // say where the box thinks home is
	}
	if len(args) > 0 {
		_ = json.Unmarshal(args, &a)
	}
	out := map[string]any{"pull": ws.snapshot(), "index": ns.snapshot()}
	if a.NWP {
		select {
		case forceNWP <- struct{}{}:
			out["indexing"] = "the box looks for new runs now and computes; the log says what came"
		default:
			out["indexing"] = "a pass is already running"
		}
	}
	cfg, err := hw.LoadServicesConfig(mount)
	if err != nil {
		return nil, err
	}
	db := poltergres.NewReadWrite(hw.SocketForMount(mount), cfg.Postgres.Port, cfg.Postgres.RWUser, cfg.Postgres.RWPass, cfg.Postgres.Name)
	out["table"] = weather.Load(db)
	if a.MetOffice != nil {
		if err := setMetOffice(db, a.MetOffice.Key, a.MetOffice.Order); err != nil {
			return nil, err
		}
		if a.MetOffice.Key != "" {
			select {
			case forceNWP <- struct{}{}:
			default:
			}
		}
	}
	out["metoffice"] = metOfficeState(db, ns)
	if a.Home || a.MetOffice != nil {
		out["home"] = homeHint(db)
	}
	if a.Fetch {
		select {
		case force <- struct{}{}:
			out["fetching"] = "the box pulls the next batch now; the rest follow at the pace"
		default:
			out["fetching"] = "a pull is already running"
		}
	}
	if places, err := weather.Places(db); err == nil {
		out["list"] = len(places)
	}
	now := time.Now()
	switch {
	case a.Place != "":
		if f, km, ok := weather.ByName(db, a.Place); ok {
			out["forecast"] = f
			out["text"] = weather.Describe(f, km, now)
		} else {
			out["text"] = "no place of that name among the pulled places or the box's GeoNames"
		}
	case a.Lat != 0 || a.Lon != 0:
		if f, km, ok := weather.Nearest(db, a.Lat, a.Lon); ok {
			out["forecast"] = f
			out["text"] = weather.Describe(f, km, now)
		} else {
			out["text"] = "no pulled place within " + itoa(int(weather.NearKm)) + " km"
		}
	default:
		// where the trail says the phone is, as the chat answers "what's the weather like"
		if ts, lat, lon := hw.TrailNewest(db); ts > 0 {
			if f, km, ok := weather.Nearest(db, lat, lon); ok {
				out["forecast"] = f
				out["text"] = weather.Describe(f, km, now)
			} else {
				out["text"] = "no pulled place within " + itoa(int(weather.NearKm)) + " km of the trail's newest point"
			}
		} else {
			out["text"] = "no trail yet: weather lat= lon= or place= reads the table"
		}
	}
	return out, nil
}
