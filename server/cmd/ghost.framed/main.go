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
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"strings"
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
	pipe.OnArchived(func(archivePath string, takenAt int64) {
		_, err := searchCli.Call("ingest", map[string]any{
			"source": "image", "path": archivePath, "capturedAt": takenAt, "daemon": service,
		})
		if err != nil {
			lg.Warn("search ingest notify failed (rebuild will cover it)", "fn", "main",
				"path", archivePath, "err", err)
		}
	})

	// Resume: drain whatever was spooled before the last lock/crash, then poll.
	go func() {
		n := pipe.DrainIncoming() + pipe.DrainLocations()
		if n > 0 {
			lg.Info("resume drain complete", "fn", "main", "processed", n)
		}
		t := time.NewTicker(time.Duration(cfg.PollSeconds) * time.Second)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				pipe.DrainIncoming()
				pipe.DrainLocations()
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
