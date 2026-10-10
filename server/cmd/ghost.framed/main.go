// ghost.framed is the photo pipeline daemon. The phone uploads raw images and location batches over
// the mTLS session channel; secd streams the bytes into <mount>/frames/incoming*; framed drains those
// folders one file at a time:
//
//	hash -> dedupe -> EXIF (time + GPS) -> MOVE the untouched original to archive/YYYY/MM/DD/<hash>
//	-> derive a 1600px preview and 320px thumb -> record in Postgres -> rebuild the day's GeoJSON path
//
// The original is never re-encoded or modified , the archive holds byte-identical raw files, moved
// with an atomic rename. Previews are derived copies. Location points from the watch plus photo GPS
// become one GeoJSON per day (<mount>/frames/paths/YYYY-MM-DD.geojson): a LineString of where you
// went with Point markers where you photographed. The box stores the DATA only and never contacts a
// map or tile service , fetching tiles would send your coordinate history to a third party, the one
// outbound call this box must never make. The phone renders the GeoJSON over OpenStreetMap client-side.
//
// Startup/resume: framed rescans incoming on start, so photos spooled before a lock or crash are
// processed on the next unlock. Everything is idempotent (hash identity, ON CONFLICT DO NOTHING,
// full-rewrite day paths), so a crash mid-photo loses work, never a photo.
//
// Runs only while UNLOCKED. Spawned by ghost.watchd from <mount>/bin; logs to
// <mount>/logs/ghost.framed-YYYY-MM-DD.log.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/LocalGhostDao/localghost/server/internal/dem"
	"github.com/LocalGhostDao/localghost/server/internal/harden"
	"github.com/LocalGhostDao/localghost/server/internal/landtiles"
	"github.com/LocalGhostDao/localghost/server/internal/roadgraph"
	"github.com/LocalGhostDao/localghost/server/internal/roadtiles"
	"github.com/LocalGhostDao/localghost/server/internal/tzgrid"
	"log"
	"log/slog"
	"math"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/framed"
	"github.com/LocalGhostDao/localghost/server/internal/ghosthealth"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/rotlog"
	"github.com/LocalGhostDao/localghost/server/internal/svcconf"
)

const service = "ghost.framed"

// conf is ghost.framed's config: base keys plus the poll cadence and slot.
type conf struct {
	svcconf.Base
	PollSeconds int `json:"pollSeconds"`
	Slot        int `json:"slot"`
}

func defaultConf() conf {
	return conf{Base: svcconf.DefaultBase(), PollSeconds: 10, Slot: 0}
}

func main() {
	harden.NoDump() // same-user processes cannot read this one through /proc; no core file
	port := flag.Int("health-port", envPort("GHOST_HEALTH_PORT"), "loopback health port")
	mount := flag.String("mount", os.Getenv("GHOST_MOUNT"), "encrypted volume mount path")
	flag.Parse()
	if *mount == "" {
		if ld := os.Getenv("GHOST_LOG_DIR"); ld != "" {
			*mount = filepath.Dir(ld)
		}
	}
	if *mount == "" {
		log.Fatalf("%s: --mount (or GHOST_MOUNT) is required", service)
	}

	logDir := filepath.Join(*mount, "logs")
	var lg *slog.Logger
	var lvl *slog.LevelVar
	if w, err := rotlog.New(logDir, service); err == nil {
		defer w.Close()
		lg, lvl = rotlog.Logger(w)
	} else {
		log.Fatalf("%s: open log: %v", service, err)
	}

	cfg := defaultConf()
	confPath := svcconf.Path(*mount, service)
	if err := svcconf.Load(confPath, &cfg); err != nil {
		lg.Warn("read conf, using defaults", "fn", "main", "err", err)
	}
	svcconf.FillBaseDefaults(&cfg.Base)
	_ = svcconf.ApplyLevel(lvl, cfg.LogLevel)
	if cfg.PollSeconds <= 0 {
		cfg.PollSeconds = 10
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// The mount path secd writes into is <stateDir>/mnt/slot<N>; GHOST_MOUNT is that same path, so the
	// frames layout lives directly under it. The store shells psql over the in-volume socket.
	dirs := framed.DefaultDirs(*mount)
	if err := dirs.EnsureDirs(); err != nil {
		log.Fatalf("%s: create frames layout: %v", service, err)
	}
	// Credentials from services.conf on the mount; framed writes, so it connects as ghost_rw.
	sc, err := hw.LoadServicesConfig(*mount)
	if err != nil {
		log.Fatalf("%s: read services.conf: %v", service, err)
	}
	store := framed.NewStore(hw.SocketForMount(*mount), sc.Postgres.Port, sc.Postgres.RWUser, sc.Postgres.RWPass, sc.Postgres.Name)
	pipe := framed.NewPipeline(dirs, store, lg)
	// Reverse geocoder, DB-BACKED , the dataset (allCountries is millions of rows) lives in
	// postgres on the encrypted volume, not in RAM. Wired when geo_points has data; import with
	// `ghost-cli ghost.framed geo-import` after dropping GeoNames TSVs under <mount>/geo, then
	// reprocess to backfill places.
	if store.GeoReady() {
		pipe.WithPlaceResolver(store.ResolvePlace)
		lg.Info("reverse geocoder wired (geo_points populated)", "fn", "main")
		// a set imported before the populations (every row at 0) is imported again from the
		// same files, once, in the background: the weather's place list and the map's label
		// order need the column filled
		if store.GeoWithoutPopulations() {
			lg.Info("geo set without populations: importing the GeoNames files again to fill them", "fn", "main")
			go func() {
				pts, names, err := store.ImportGeo(filepath.Join(*mount, "geo"), lg)
				if err != nil {
					lg.Error("geo import failed", "fn", "main", "points", pts, "err", err)
					return
				}
				lg.Info("geo import done", "fn", "main", "points", pts, "names", names)
			}()
		}
	} else {
		lg.Info("geo_points empty , frames get empty place strings (drop GeoNames TSVs in <mount>/geo, run geo-import)", "fn", "main")
	}

	runDir := os.Getenv("GHOST_RUN_DIR")
	if runDir == "" {
		runDir = filepath.Join(*mount, "run")
	}

	// Hand every archived photo to the search layer. Best-effort: the archive is the source of truth
	// and searchd's rebuild re-covers anything missed; a failure here is a warn, never a drop.
	searchCli := ctlsock.NewClientTimeout("ghost.searchd", runDir, 30*time.Second)
	pipe.OnArchived(func(archivePath, renderPath string, takenAt int64, ensure bool) {
		_, err := searchCli.Call("ingest", map[string]any{
			"source": "image", "path": archivePath, "render": renderPath, "capturedAt": takenAt,
			"daemon": service, "ensure": ensure,
		})
		if err != nil {
			lg.Warn("search ingest notify failed (converge at next start will cover it)", "fn", "main",
				"path", archivePath, "err", err)
		}
	})

	// Resume: drain whatever was spooled before the last lock/crash, then THE STOCK-TAKE: every
	// frame checked against the running pipeline, the ones behind repaired, the ones missing a
	// description, title or tags handed to searchd. On a healthy box this is one query and one
	// line in the log; after a pipeline bump it is the migration, run by the daemon, not by hand.
	go func() {
		n := pipe.DrainIncoming() + pipe.DrainLocations()
		if n > 0 {
			lg.Info("resume drain complete", "fn", "main", "processed", n)
		}
		// The daemons start together; if searchd's socket is not up yet every ensure notify of
		// the pass would fail, and "the next start will cover it" would be true at every start.
		// Wait for it, bounded: a box without searchd still gets framed's half of the pass.
		waitForSearch(ctx, searchCli, 2*time.Minute, lg)
		rep := pipe.Converge()
		categorize(searchCli, rep, lg)
		t := time.NewTicker(time.Duration(cfg.PollSeconds) * time.Second)
		defer t.Stop()
		// THE STOCK-TAKE, AGAIN, EVERY SIX HOURS. It ran at start only, so a frame that fell
		// through (a caption job that completed without describing, a searchd that was not up
		// when the notify went out) waited for the next reboot , and the box's status said
		// "1051 left" for days with nothing queued. A box that runs for weeks finishes its work
		// by itself now; the operator's `converge` is still there for right-now.
		again := time.NewTicker(6 * time.Hour)
		defer again.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pipe.DrainIncoming()
				pipe.DrainLocations()
			case <-again.C:
				lg.Info("stock-take again (every six hours)", "fn", "main")
				categorize(searchCli, pipe.Converge(), lg)
			}
		}
	}()

	ctl := ctlsock.NewServer(service, runDir, lg)
	svcconf.BindBase(ctl, service, lvl, func() (svcconf.Base, map[string]string, error) {
		fresh := defaultConf()
		if err := svcconf.Load(confPath, &fresh); err != nil {
			return svcconf.Base{}, nil, err
		}
		svcconf.FillBaseDefaults(&fresh.Base)
		return fresh.Base, map[string]string{"pollSeconds": "needs-restart"}, nil
	})
	// queue: the intake backlog (pending photos + location batches).
	ctl.Handle("queue", func(json.RawMessage) (ctlsock.Response, error) {
		f, l := pipe.PendingCounts()
		data, _ := json.Marshal(map[string]int{"pendingFrames": f, "pendingLocationBatches": l})
		return ctlsock.Response{OK: true, Data: data}, nil
	})
	// work: what this framed has done, by kind (archived, previews, duplicates, points ...), over
	// the last hour and day and since it started, and what waits in the spool. The Box Status
	// drill-in for ghost.framed reads it.
	ctl.Handle("work", func(json.RawMessage) (ctlsock.Response, error) {
		f, l := pipe.PendingCounts()
		data, _ := json.Marshal(map[string]any{"work": pipe.Work(), "waiting": map[string]int{"uploads": f, "locationBatches": l}})
		return ctlsock.Response{OK: true, Data: data}, nil
	})
	// drain: force a pass now instead of waiting for the tick (operator convenience after a bulk sync).
	ctl.Handle("drain", func(json.RawMessage) (ctlsock.Response, error) {
		n := pipe.DrainIncoming() + pipe.DrainLocations()
		return ctlsock.Response{OK: true, Text: fmt.Sprintf("processed %d", n)}, nil
	})
	// rebuild-day: reassemble one day's GeoJSON (day=YYYY-MM-DD), e.g. after a manual DB fix.
	// gps-import: ingest Google Timeline / location-history exports dropped in
	// <mount>/framed/gps-inbox , years of GPS become track points, day paths, and map history.
	// Also polled automatically every 5 minutes; the ctl command just skips the wait.
	ctl.Handle("gps-import", func(json.RawMessage) (ctlsock.Response, error) {
		go func() {
			n := pipe.IngestTimelineDir(*mount, lg)
			lg.Info("gps import pass done", "fn", "main", "points", n)
		}()
		return ctlsock.Response{OK: true, Text: "gps import started (watch the log)"}, nil
	})
	go func() {
		t := time.NewTicker(5 * time.Minute)
		defer t.Stop()
		hl := time.NewTicker(6 * time.Hour)
		defer hl.Stop()
		if err := store.WeeklyHighlight(); err != nil {
			lg.Warn("weekly highlight", "fn", "main", "err", err)
		}
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pipe.IngestTimelineDir(*mount, lg)
			case <-hl.C:
				if err := store.WeeklyHighlight(); err != nil {
					lg.Warn("weekly highlight", "fn", "main", "err", err)
				}
			}
		}
	}()
	// geo-import: stream GeoNames TSVs from <mount>/geo into geo_points/geo_names. Background ,
	// allCountries is minutes of batched inserts; watch the log. Idempotent by geonameid PK, so
	// re-running after a newer dump only adds. Wires the resolver on completion , no restart.
	ctl.Handle("geo-import", func(json.RawMessage) (ctlsock.Response, error) {
		go func() {
			pts, names, err := store.ImportGeo(filepath.Join(*mount, "geo"), lg)
			if err != nil {
				lg.Error("geo import failed", "fn", "main", "points", pts, "err", err)
				return
			}
			lg.Info("geo import done", "fn", "main", "points", pts, "names", names)
			if store.GeoReady() {
				pipe.WithPlaceResolver(store.ResolvePlace)
				lg.Info("reverse geocoder wired , run reprocess to backfill places", "fn", "main")
			}
		}()
		return ctlsock.Response{OK: true, Text: "geo import started (watch the log; then run reprocess)"}, nil
	})
	// geo-tiles: cut OpenStreetMap's land polygons (land_polygons.shp under <mount>/geo, from
	// tools/fetch_geo.sh) into one-degree tiles under <mount>/landtiles, which secd serves to the
	// map as it zooms in (internal/landtiles). Minutes and a couple of GB of RAM for the whole
	// world, once; background, idempotent, the old tiles stay served until the new set is whole.
	// Also runs by itself at start when the shapefile is newer than the tiles.
	tilesOut := filepath.Join(*mount, "landtiles")
	// the same tiles tell the trail questions where the sea is ("that hop crosses the sea")
	land := landtiles.NewLookup(tilesOut)
	pipe.SetLandCheck(land.OnLand)
	var tilesBusy sync.Mutex
	buildTiles := func(why string) string {
		shp := landtiles.FindShapefile(filepath.Join(*mount, "geo"))
		if shp == "" {
			return "no land_polygons.shp under " + filepath.Join(*mount, "geo") + " , fetch it with tools/fetch_geo.sh (OpenStreetMap land polygons, complete, 4326) and copy it in"
		}
		if !tilesBusy.TryLock() {
			return "a tile build is already running (watch the log)"
		}
		go func() {
			defer tilesBusy.Unlock()
			lg.Info("land tiles: building", "fn", "geo-tiles", "why", why, "from", shp, "to", tilesOut)
			_ = store.SetState("landtiles", []byte(`{"state":"building"}`))
			st, err := landtiles.Build(shp, tilesOut, func(p string) { lg.Info("land tiles: "+p, "fn", "geo-tiles") })
			if err != nil {
				lg.Error("land tiles: build failed", "fn", "geo-tiles", "err", err)
				_ = store.SetState("landtiles", []byte(`{"state":"failed"}`))
				return
			}
			lg.Info("land tiles: done , "+st.String(), "fn", "geo-tiles")
			land.Reset()
			b, _ := json.Marshal(map[string]any{"state": "ready", "coastTiles": st.CoastCell, "landCells": st.LandCells, "points": st.Points, "bytes": st.Bytes})
			_ = store.SetState("landtiles", b)
		}()
		return "land tile build started from " + shp + " (watch the log)"
	}
	ctl.Handle("geo-tiles", func(json.RawMessage) (ctlsock.Response, error) {
		return ctlsock.Response{OK: true, Text: buildTiles("asked")}, nil
	})
	if shp := landtiles.FindShapefile(filepath.Join(*mount, "geo")); shp != "" && landtiles.Stale(shp, tilesOut) {
		buildTiles("the shapefile is newer than the tiles")
	}
	// road-tiles: cut OpenStreetMap's roads (the .osm.pbf files under <mount>/geo/roads, from
	// tools/fetch_geo.sh: the planet or Geofabrik's continents) into the map's road tiles under
	// <mount>/roadtiles (internal/roadtiles). Hours for the world, in the background, one build at
	// a time; the old tiles stay served until the new set is whole. Also by itself at start when a
	// PBF is newer than the tiles.
	roadsIn := filepath.Join(*mount, "geo", "roads")
	roadsOut := filepath.Join(*mount, "roadtiles")
	// the day route walks the streets on the graph the road-tiles build writes beside the tiles;
	// without tiles it draws chords, and it forgets its loaded cells after every rebuild
	roads := roadgraph.Open(roadsOut)
	pipe.SetRouter(roads)
	pipe.SetRoadCheck(roads.NearRoad) // "no road goes there", for the trail questions
	var roadsBusy sync.Mutex
	buildRoads := func(why string) string {
		pbfs := roadtiles.FindPBFs(roadsIn)
		if len(pbfs) == 0 {
			return "no .osm.pbf under " + roadsIn + " , tools/fetch_geo.sh fetches them (set roads on the mirror)"
		}
		if !roadsBusy.TryLock() {
			return "a road tile build is already running (watch the log)"
		}
		go func() {
			defer roadsBusy.Unlock()
			lg.Info("road tiles: building", "fn", "road-tiles", "why", why, "files", len(pbfs), "to", roadsOut)
			_ = store.SetState("roadtiles", []byte(`{"state":"building"}`))
			st, err := roadtiles.Build(pbfs, roadsOut, roadtiles.Options{
				Work:     filepath.Join(*mount, "roadtiles.work"),
				Progress: func(p string) { lg.Info("road tiles: "+p, "fn", "road-tiles") },
			})
			if err != nil {
				lg.Error("road tiles: build failed", "fn", "road-tiles", "err", err)
				_ = store.SetState("roadtiles", []byte(`{"state":"failed"}`))
				return
			}
			lg.Info("road tiles: done , "+st.String(), "fn", "road-tiles")
			roads.Reset()
			// the days drawn as chords before there were streets get their routes again
			n := pipe.RebuildRecentDays(60)
			lg.Info("day routes rebuilt on the new streets", "fn", "road-tiles", "days", n)
			b, _ := json.Marshal(map[string]any{"state": "ready", "majorTiles": st.MajorTiles, "fineTiles": st.FineTiles, "points": st.Points, "bytes": st.Bytes})
			_ = store.SetState("roadtiles", b)
		}()
		return fmt.Sprintf("road tile build started from %d file(s) under %s (hours for the world; watch the log)", len(pbfs), roadsIn)
	}
	ctl.Handle("road-tiles", func(json.RawMessage) (ctlsock.Response, error) {
		return ctlsock.Response{OK: true, Text: buildRoads("asked")}, nil
	})
	if why := roadtiles.StaleWhy(roadtiles.FindPBFs(roadsIn), roadsOut); why != "" {
		buildRoads(why)
	}
	// tz-grid: the time zone boundaries (the mirror's tz set under <mount>/geo/tz) rasterised to
	// <mount>/geo/tz/grid.bin (internal/tzgrid), once, and again when the file is newer. With the
	// grid the trail's newest point names the person's zone (settings local_tz) and the box's
	// "today" and "19:00" stop being UTC's.
	tzDir := filepath.Join(*mount, "geo", "tz")
	tzGrid := filepath.Join(tzDir, "grid.bin")
	var tzMu sync.Mutex
	var tzLookup *tzgrid.Lookup
	useTZ := func() {
		l, err := tzgrid.Open(tzGrid)
		if err != nil {
			return
		}
		tzMu.Lock()
		old := tzLookup
		tzLookup = l
		tzMu.Unlock()
		if old != nil {
			_ = old.Close()
		}
	}
	pipe.SetZoneLookup(func(lat, lon float64) string {
		tzMu.Lock()
		l := tzLookup
		tzMu.Unlock()
		return l.Zone(lat, lon)
	})
	var tzBusy sync.Mutex
	buildTZ := func(why string) string {
		src := tzgrid.FindGeoJSON(tzDir)
		if src == "" {
			return "no time zone file under " + tzDir + " , tools/fetch_geo.sh fetches it (set tz on the mirror)"
		}
		if !tzBusy.TryLock() {
			return "a time zone grid build is already running"
		}
		go func() {
			defer tzBusy.Unlock()
			lg.Info("time zone grid: building", "fn", "tz-grid", "why", why, "from", filepath.Base(src))
			n, err := tzgrid.Build(src, tzGrid, func(p string) { lg.Info("time zone grid: "+p, "fn", "tz-grid") })
			if err != nil {
				lg.Error("time zone grid: build failed", "fn", "tz-grid", "err", err)
				return
			}
			lg.Info("time zone grid: done", "fn", "tz-grid", "zones", n)
			useTZ()
		}()
		return "time zone grid build started from " + filepath.Base(src)
	}
	ctl.Handle("tz-grid", func(json.RawMessage) (ctlsock.Response, error) {
		return ctlsock.Response{OK: true, Text: buildTZ("asked")}, nil
	})
	// ELEVATION: the ground's height from the Copernicus tiles under <mount>/geo/elevation (the
	// mirror's set elevation, internal/dem: packs of a 30-degree block each, or loose tiles).
	// Indexed at start and again when the folder changes
	// (an update brought tiles); when the tiles are new to the days drawn, every day is drawn
	// again in the background so each line carries its heights and its climb. Never asked of a
	// service: a height comes from the disk.
	elevDir := filepath.Join(*mount, "geo", "elevation")
	var elevMu sync.Mutex
	var elev *dem.Set
	var elevMod time.Time
	var elevChecked time.Time
	elevation := func() *dem.Set {
		elevMu.Lock()
		defer elevMu.Unlock()
		if time.Since(elevChecked) < time.Minute && elev != nil {
			return elev
		}
		elevChecked = time.Now()
		st, err := os.Stat(elevDir)
		if err != nil {
			elev = nil
			return nil
		}
		if elev == nil || !st.ModTime().Equal(elevMod) {
			elev, elevMod = dem.Open(elevDir), st.ModTime()
		}
		return elev
	}
	pipe.SetHeightLookup(func(lat, lon float64) (float64, bool) { return elevation().Height(lat, lon) })
	// the days drawn before these tiles: drawn again once, the count of tiles kept beside them
	redrawForHeights := func(why string) {
		set := elevation()
		if set.Tiles() == 0 {
			return
		}
		mark := filepath.Join(elevDir, ".days-drawn")
		if b, _ := os.ReadFile(mark); strings.TrimSpace(string(b)) == strconv.Itoa(set.Tiles()) && why == "" {
			return
		}
		go func() {
			n := pipe.RebuildRecentDays(100000)
			_ = os.WriteFile(mark, []byte(strconv.Itoa(set.Tiles())), 0o640)
			lg.Info("days drawn again with the ground's height", "fn", "elevation", "days", n, "tiles", set.Tiles())
		}()
	}
	redrawForHeights("")
	go func() {
		t := time.NewTicker(10 * time.Minute)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				redrawForHeights("")
			}
		}
	}()
	// elevation: how many tiles, and the height at a point (lat=, lon=); redraw=true draws every
	// day again with the heights
	ctl.Handle("elevation", func(args json.RawMessage) (ctlsock.Response, error) {
		var a struct {
			Lat    *float64 `json:"lat"`
			Lon    *float64 `json:"lon"`
			Redraw bool     `json:"redraw"`
		}
		if len(args) > 0 {
			_ = json.Unmarshal(args, &a)
		}
		set := elevation()
		out := map[string]any{"dir": elevDir, "tiles": set.Tiles(), "packs": set.Packs()}
		if a.Lat != nil && a.Lon != nil {
			if h, ok := set.Height(*a.Lat, *a.Lon); ok {
				out["heightM"] = math.Round(h*10) / 10
			} else {
				out["heightM"] = nil
				out["why"] = "no tile for that point (the sea, or a part of the world not fetched)"
			}
		}
		if a.Redraw {
			redrawForHeights("asked")
			out["redrawing"] = "every day is drawn again in the background (watch the log)"
		}
		b, _ := json.Marshal(out)
		return ctlsock.Response{OK: true, Data: b}, nil
	})
	// setting: one shared settings row (local_tz is the one health.sh asks for), and the zone the
	// grid names for a point (lat=, lon=) to check the grid by hand.
	ctl.Handle("setting", func(args json.RawMessage) (ctlsock.Response, error) {
		var a struct {
			Key string  `json:"key"`
			Lat float64 `json:"lat"`
			Lon float64 `json:"lon"`
		}
		if len(args) > 0 {
			_ = json.Unmarshal(args, &a)
		}
		out := map[string]any{}
		if a.Key != "" {
			v, err := store.Setting(a.Key)
			if err != nil {
				return ctlsock.Response{OK: false, Err: err.Error()}, nil
			}
			out["key"], out["value"] = a.Key, v
		}
		if a.Lat != 0 || a.Lon != 0 {
			tzMu.Lock()
			l := tzLookup
			tzMu.Unlock()
			out["zone"] = l.Zone(a.Lat, a.Lon)
			out["grid"] = l != nil
		}
		data, _ := json.Marshal(out)
		return ctlsock.Response{OK: true, Data: data}, nil
	})
	useTZ()
	if src := tzgrid.FindGeoJSON(tzDir); src != "" && tzgrid.Stale(src, tzGrid) {
		buildTZ("the zone file is newer than the grid")
	}
	// reprocess: converge the archive's derived state , frame records, previews (force=true also
	// re-derives EXISTING previews, the orientation-fix case), search notifies, day paths. Runs in
	// the background: a full archive pass takes minutes and the socket should answer now.
	// NIGHTLY BACKUPS , sealed to the operator's public key (opt-in: no /var/lib/ghost/backup.pub,
	// no backups, one log line, silence). Sunday full, other nights incremental since the last run.
	backupCfg := framed.BackupConfig{Dir: "/var/lib/ghost/backup", PubFile: "/var/lib/ghost/backup.pub"}
	// Bundled ffmpeg outranks the OS copy , the volume carries its own media runtime.
	if ffb := filepath.Join(*mount, "runtime", "ffmpeg", "bin", "ffmpeg"); fileOK(ffb) {
		pipe.SetFFmpeg(ffb, filepath.Join(*mount, "runtime", "ffmpeg", "lib"))
		lg.Info("using bundled ffmpeg from the volume", "fn", "main")
	}
	framedRoot := filepath.Join(*mount, "framed")
	runBackup := func(force bool) string {
		full := force || time.Now().Weekday() == time.Sunday
		var since time.Time
		if b, err := os.ReadFile(filepath.Join(backupCfg.Dir, ".watermark")); err == nil {
			if t, perr := time.Parse(time.RFC3339, strings.TrimSpace(string(b))); perr == nil {
				since = t
			} else {
				full = true
			}
		} else {
			full = true // no watermark = first run = full
		}
		start := time.Now()
		out, files, bytes, err := framed.RunBackup(backupCfg, framedRoot, full, since, lg)
		if err != nil {
			lg.Warn("backup failed", "fn", "main", "err", err)
			return "backup failed: " + err.Error()
		}
		_ = os.WriteFile(filepath.Join(backupCfg.Dir, ".watermark"), []byte(start.UTC().Format(time.RFC3339)), 0o600)
		kind := map[bool]string{true: "full", false: "incremental"}[full]
		lg.Info("backup written", "fn", "main", "kind", kind, "file", out, "files", files, "bytes", bytes)
		return fmt.Sprintf("%s backup: %d file(s), %d bytes -> %s", kind, files, bytes, out)
	}
	if _, err := os.Stat(backupCfg.PubFile); err != nil {
		lg.Info("backups idle , no recipient key at /var/lib/ghost/backup.pub (run ghost.restore keygen, copy the pub over)", "fn", "main")
	}
	go func() {
		bt := time.NewTicker(1 * time.Hour)
		defer bt.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-bt.C:
				h := time.Now().Hour()
				if h != 3 { // the 03:00 hour, box time , the machine's quietest
					continue
				}
				if _, err := os.Stat(backupCfg.PubFile); err != nil {
					continue // opt-in by key, checked live so placing the key needs no restart
				}
				day := time.Now().Format("2006-01-02")
				markFile := filepath.Join(backupCfg.Dir, ".lastday")
				if b, err := os.ReadFile(markFile); err == nil && strings.TrimSpace(string(b)) == day {
					continue // tonight already ran
				}
				_ = runBackup(false)
				_ = os.MkdirAll(backupCfg.Dir, 0o700)
				_ = os.WriteFile(markFile, []byte(day), 0o600)
			}
		}
	}()
	ctl.Handle("backup", func(args json.RawMessage) (ctlsock.Response, error) {
		var a struct {
			Full bool `json:"full"`
		}
		if len(args) > 0 {
			_ = json.Unmarshal(args, &a)
		}
		return ctlsock.Response{OK: true, Text: runBackup(a.Full)}, nil
	})
	ctl.Handle("reprocess", func(args json.RawMessage) (ctlsock.Response, error) {
		var a struct {
			Force bool `json:"force"`
		}
		if len(args) > 0 {
			_ = json.Unmarshal(args, &a)
		}
		go func() {
			lg.Info("reprocess pass starting (may first queue behind a sync drain)", "fn", "main", "force", a.Force)
			scanned, recorded, previewed, notified := pipe.Reprocess(a.Force)
			lg.Info("reprocess done", "fn", "main", "scanned", scanned, "recorded", recorded,
				"previewed", previewed, "notified", notified)
		}()
		return ctlsock.Response{OK: true, Text: "reprocess started (watch the log; force=" +
			map[bool]string{true: "true", false: "false"}[a.Force] + ")"}, nil
	})
	// stages: the stock-take, read-only , X photos and Y videos, how many at the latest stage, and
	// what the rest are missing. converge: the same, plus the repairs, in the background.
	ctl.Handle("stages", func(json.RawMessage) (ctlsock.Response, error) {
		r, err := pipe.Stages()
		if err != nil {
			return ctlsock.Response{}, err
		}
		data, _ := json.Marshal(r)
		return ctlsock.Response{OK: true, Text: r.String(), Data: data}, nil
	})
	ctl.Handle("converge", func(json.RawMessage) (ctlsock.Response, error) {
		go func() {
			lg.Info("converge pass starting (may first queue behind a drain)", "fn", "main")
			categorize(searchCli, pipe.Converge(), lg)
		}()
		return ctlsock.Response{OK: true, Text: "converge started (watch the log for the summary line)"}, nil
	})
	// day-routes [days=N]: rebuild the last N days' paths and routes (default 60), e.g. after the
	// road tiles arrived or the geo data changed
	ctl.Handle("day-routes", func(args json.RawMessage) (ctlsock.Response, error) {
		var a struct {
			Days int `json:"days"`
		}
		if len(args) > 0 {
			_ = json.Unmarshal(args, &a)
		}
		if a.Days <= 0 {
			a.Days = 60
		}
		go func() {
			n := pipe.RebuildRecentDays(a.Days)
			lg.Info("day routes rebuilt", "fn", "day-routes", "days", n)
		}()
		return ctlsock.Response{OK: true, Text: fmt.Sprintf("rebuilding the last %d days' paths and routes (watch the log)", a.Days)}, nil
	})
	// trail: which stored points draw a day's long lines, where each came from, and what the glitch
	// rules made of it , `ghost-cli ghost.framed trail day=2026-09-27 [km=2]`
	ctl.Handle("trail", func(args json.RawMessage) (ctlsock.Response, error) {
		var a struct {
			Day string  `json:"day"`
			Km  float64 `json:"km"`
		}
		if len(args) > 0 {
			_ = json.Unmarshal(args, &a)
		}
		t, err := time.Parse("2006-01-02", a.Day)
		if err != nil {
			return ctlsock.Response{}, fmt.Errorf("trail requires day=YYYY-MM-DD")
		}
		if a.Km <= 0 {
			a.Km = 2
		}
		start := t.UTC().Unix()
		rows, err := store.TrailRows(start-2*3600, start+86400+2*3600)
		if err != nil {
			return ctlsock.Response{}, err
		}
		return ctlsock.Response{OK: true, Text: framed.TrailReport(t, rows, a.Km*1000)}, nil
	})
	// trail-answer: the person's answer to a trail question (framed/questions.go), from the phone
	// through secd: keep=true never asks again; keep=false deletes the points
	ctl.Handle("trail-answer", func(args json.RawMessage) (ctlsock.Response, error) {
		var a struct {
			From int64   `json:"from"`
			To   int64   `json:"to"`
			TS   []int64 `json:"ts"`
			Keep bool    `json:"keep"`
		}
		if len(args) == 0 || json.Unmarshal(args, &a) != nil {
			return ctlsock.Response{}, fmt.Errorf("trail-answer requires from, to, ts, keep")
		}
		n, err := pipe.AnswerTrail(a.From, a.To, a.TS, a.Keep)
		if err != nil {
			return ctlsock.Response{}, err
		}
		data, _ := json.Marshal(map[string]any{"deleted": n})
		return ctlsock.Response{OK: true, Data: data}, nil
	})
	// trail-forget: the person zoomed in on a fix they know is wrong (framed/forget.go): the fix
	// and its neighbours at the same spot; dry says which without deleting
	ctl.Handle("trail-forget", func(args json.RawMessage) (ctlsock.Response, error) {
		var a struct {
			TS      int64   `json:"ts"`
			RadiusM float64 `json:"radiusM"`
			Dry     bool    `json:"dry"`
		}
		if len(args) == 0 || json.Unmarshal(args, &a) != nil || a.TS <= 0 {
			return ctlsock.Response{}, fmt.Errorf("trail-forget requires ts (and radiusM, dry)")
		}
		run, n, err := pipe.ForgetSpot(a.TS, a.RadiusM, a.Dry)
		if err != nil {
			return ctlsock.Response{}, err
		}
		data, _ := json.Marshal(map[string]any{"ts": run, "deleted": n})
		return ctlsock.Response{OK: true, Data: data}, nil
	})
	ctl.Handle("rebuild-day", func(args json.RawMessage) (ctlsock.Response, error) {
		var a struct {
			Day string `json:"day"`
		}
		if len(args) > 0 {
			_ = json.Unmarshal(args, &a)
		}
		if a.Day == "" {
			return ctlsock.Response{}, fmt.Errorf("rebuild-day requires day=YYYY-MM-DD")
		}
		pipe.RebuildDay(a.Day)
		return ctlsock.Response{OK: true, Text: "rebuilt " + a.Day}, nil
	})
	defer ctl.Cleanup()
	go func() {
		if err := ctl.Serve(ctx); err != nil {
			lg.Error("control server exited", "fn", "main", "err", err)
		}
	}()

	rep := ghosthealth.ReporterFunc(func() ghosthealth.Health {
		f, l := pipe.PendingCounts()
		d := ""
		if f+l > 0 {
			d = fmt.Sprintf("backlog: %d frames, %d location batches", f, l)
		}
		return ghosthealth.Health{Code: ghosthealth.OK, Name: service, Detail: d}
	})
	hsrv := ghosthealth.NewServer(service, rep)
	go func() {
		if err := hsrv.Serve(*port); err != nil {
			lg.Error("health server stopped", "fn", "main", "err", err)
		}
	}()

	lg.Info("up", "fn", "main", "healthPort", *port, "pollSeconds", cfg.PollSeconds)
	<-ctx.Done()
	lg.Info("shutting down", "fn", "main")
}

func envPort(key string) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}

func fileOK(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// categorize asks searchd to queue its tag-category backfill when the pass found tags without
// one. Bulk, not per row: a few thousand frames is one command and one queue, not thousands of
// notifies.
func categorize(cli *ctlsock.Client, rep framed.ConvergeReport, lg *slog.Logger) {
	if rep.NoCategory == 0 {
		return
	}
	// the whole backlog at once: searchd works it a few jobs per tick behind captions and tags,
	// so a big queue delays nothing new, and a small one left most of an archive waiting days
	resp, err := cli.Call("categorize", map[string]any{"limit": 40000})
	if err != nil {
		lg.Warn("categorize request failed (next pass retries)", "fn", "categorize", "err", err)
		return
	}
	lg.Info("tag categories: "+resp.Text, "fn", "categorize", "framesWithout", rep.NoCategory)
}

// waitForSearch blocks until searchd answers a ping, the budget runs out, or ctx ends. Returns
// whether it answered; the caller runs the pass either way and logs which it was.
func waitForSearch(ctx context.Context, cli *ctlsock.Client, budget time.Duration, lg *slog.Logger) bool {
	deadline := time.Now().Add(budget)
	for {
		if _, err := cli.Call("ping", nil); err == nil {
			return true
		}
		if time.Now().After(deadline) || ctx.Err() != nil {
			lg.Warn("searchd not answering; converge runs without it (descriptions wait for the next pass)",
				"fn", "waitForSearch", "waited", budget)
			return false
		}
		select {
		case <-ctx.Done():
			return false
		case <-time.After(2 * time.Second):
		}
	}
}
