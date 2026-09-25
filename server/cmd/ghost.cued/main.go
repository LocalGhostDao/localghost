// ghost.cued is the cueing daemon: the First Officer that reads the user's context and, quietly and
// rarely, surfaces the right memory for the moment , keep this photo, remember this , as an answerable
// notification. It runs the four mechanisms from the "Before You Ask" Hard Truth (a running context
// description, cheap priming, a high suppression threshold, a per-cue learning curve) for real.
//
// HONEST STATE. cued is a full daemon, but the RETRIEVAL it depends on , ghost.synthd , is not built.
// So the synth client is a stub that returns nothing: cued runs its whole loop, primes on every
// context change, applies the threshold and learning curve, and correctly surfaces nothing, because
// synthd proposes nothing yet. cued is running-but-blind, and it logs that plainly on start rather
// than pretending to think. When synthd lands, its client replaces the stub behind the same interface
// and cued begins surfacing with no change to this daemon.
//
// The shoebox nomination works end to end today, kept as a control command
// (ghost-cli ghost.cued nominate photo="beach, this morning") so the ask/answer loop can be exercised
// against the app now. It produces a real ask through the same NotifStore.Produce path the automatic
// trigger will use.
//
// Runs only while UNLOCKED (the volume is mounted, Postgres + Redis up). Spawned by ghost.watchd from
// <mount>/bin; logs to <mount>/logs/ghost.cued-YYYY-MM-DD.log.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/cued"
	"github.com/LocalGhostDao/localghost/server/internal/ghosthealth"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/rotlog"
	"github.com/LocalGhostDao/localghost/server/internal/svcconf"
	"github.com/LocalGhostDao/localghost/server/internal/synth"
)

const service = "ghost.cued"

var keepOptions = []string{"Keep", "Skip"}

// conf is ghost.cued's config: base keys plus the engine tuning (threshold, cooldown, learning
// half-life), all live-reloadable via ghost.cued.conf and the ctlsock, so the suppression behaviour
// can be tuned on a running box.
type conf struct {
	svcconf.Base
	SurfaceThreshold   float64 `json:"surfaceThreshold"`
	MinDistinctiveness float64 `json:"minDistinctiveness"`
	PrimeLimit         int     `json:"primeLimit"`
	LearningHalfLife   float64 `json:"learningHalfLife"`
	CooldownSeconds    int     `json:"cooldownSeconds"`
	Slot               int     `json:"slot"`
	// SynthService names the daemon answering prime/ready. ghost.synthd is the default; ghost.searchd
	// answers the same contract over the real corpus , flip here when retiring synthd.
	SynthService string `json:"synthService"`
}

func defaultConf() conf {
	d := cued.DefaultConfig()
	return conf{
		Base:               svcconf.DefaultBase(),
		SurfaceThreshold:   d.SurfaceThreshold,
		MinDistinctiveness: d.MinDistinctiveness,
		PrimeLimit:         d.PrimeLimit,
		LearningHalfLife:   d.LearningHalfLife,
		CooldownSeconds:    int(d.Cooldown.Seconds()),
		Slot:               0,
	}
}

func (c conf) engineConfig() cued.Config {
	return cued.Config{
		SurfaceThreshold:   c.SurfaceThreshold,
		MinDistinctiveness: c.MinDistinctiveness,
		PrimeLimit:         c.PrimeLimit,
		LearningHalfLife:   c.LearningHalfLife,
		Cooldown:           time.Duration(c.CooldownSeconds) * time.Second,
	}
}

// notifSurfacer produces a cleared cue as a real answerable notification through NotifStore , the same
// produce path the manual nominate uses.
type notifSurfacer struct {
	store *hw.NotifStore
	slot  int
}

func (n notifSurfacer) Surface(c synth.Candidate) error {
	kind := "note"
	if len(c.Options) > 0 {
		kind = "ask"
	}
	return n.store.Produce(n.slot, hw.Notification{
		Service: service, Kind: kind, Title: c.Title, Body: c.Body, Options: c.Options,
	})
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

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	// -mount IS the volume here (watchd passes /var/lib/ghost/mnt/slot0). secd builds this store from
	// its STATE dir, <state>/mnt/slotN; copying that shape here doubled the path
	// (/var/lib/ghost/mnt/slot0/mnt/slot0/services.conf) and every reflection and cue failed to post.
	store := hw.NewNotifStore(func(int) string { return hw.SocketForMount(*mount) })

	runDir := os.Getenv("GHOST_RUN_DIR")
	if runDir == "" {
		runDir = filepath.Join(*mount, "run")
	}

	// The engine, wired to the REAL synth client talking to ghost.synthd over its control socket. Today
	// synthd's index is empty, so Prime returns nothing and cued surfaces nothing , but the path is
	// real: when synthd's corpus is built, cued starts surfacing with no change here. A short timeout
	// keeps priming a hot path (cued would rather get nothing fast than block on retrieval).
	synthSvc := cfg.SynthService
	if synthSvc == "" {
		synthSvc = "ghost.synthd"
	}
	sc := synth.NewSocketClientFor(synthSvc, runDir, 2*time.Second)
	engine := cued.New(cfg.engineConfig(), sc, notifSurfacer{store: store, slot: cfg.Slot}, lg)
	if !sc.Ready() {
		lg.Info("running; ghost.synthd reports no index yet, so no automatic cue will be raised "+
			"(manual nominate still works)", "fn", "main")
	}

	ctl := ctlsock.NewServer(service, runDir, lg)
	svcconf.BindBase(ctl, service, lvl, func() (svcconf.Base, map[string]string, error) {
		fresh := defaultConf()
		if err := svcconf.Load(confPath, &fresh); err != nil {
			return svcconf.Base{}, nil, err
		}
		svcconf.FillBaseDefaults(&fresh.Base)
		engine.SetConfig(fresh.engineConfig()) // tuning is hot , applied live
		return fresh.Base, map[string]string{
			"surfaceThreshold": "applied", "cooldownSeconds": "applied", "learningHalfLife": "applied",
		}, nil
	})
	// AUTOMATIC DAILY REFLECTION , the loop the architecture pointed at: live a day, the box
	// remembers it (synthd episodes), one morning it hands the day back. Priority: this day one
	// year ago; else a random episode older than 30 days; else silence (a young archive earns
	// quiet mornings, not filler). Once per day, morning hours only, and answering is optional ,
	// a reflection is an offering, not homework.
	go reflectionLoop(ctx, filepath.Dir(runDir), store, cfg.Slot, lg)

	ctl.Handle("nominate", func(args json.RawMessage) (ctlsock.Response, error) {
		var a struct {
			Photo string `json:"photo"`
			Slot  int    `json:"slot"`
		}
		if len(args) > 0 {
			_ = json.Unmarshal(args, &a)
		}
		if a.Photo == "" {
			return ctlsock.Response{}, fmt.Errorf("nominate requires photo=<ref>")
		}
		ask := hw.Notification{
			Service: service, Kind: "ask", Title: "Keep this one?",
			Body: fmt.Sprintf("A favourite from today (%s) , keep it in the plain shoebox so a lost "+
				"code can never erase it?", a.Photo),
			Options: keepOptions,
		}
		if err := store.Produce(a.Slot, ask); err != nil {
			return ctlsock.Response{}, err
		}
		return ctlsock.Response{OK: true, Text: "raised a shoebox nomination for " + a.Photo}, nil
	})
	ctl.Handle("queue", func(json.RawMessage) (ctlsock.Response, error) {
		data, _ := json.Marshal(engine.Snapshot())
		return ctlsock.Response{OK: true, Data: data}, nil
	})
	defer ctl.Cleanup()
	go func() {
		if err := ctl.Serve(ctx); err != nil {
			lg.Error("control server exited", "fn", "main", "err", err)
		}
	}()

	// Health: OK , the daemon is up and doing its job (which is mostly staying silent). Detail notes
	// retrieval is offline so /v1/status can show "cueing online, retrieval offline".
	rep := ghosthealth.ReporterFunc(func() ghosthealth.Health {
		d := ""
		if !sc.Ready() {
			d = "retrieval offline (synthd not built)"
		}
		return ghosthealth.Health{Code: ghosthealth.OK, Name: service, Detail: d}
	})
	hsrv := ghosthealth.NewServer(service, rep)
	go func() {
		if err := hsrv.Serve(*port); err != nil {
			lg.Error("health server stopped", "fn", "main", "err", err)
		}
	}()

	// The context loop. When context-signal sources exist, they feed engine.UpdateContext and the four
	// mechanisms run per change. Today there are no signal sources on the box, so the loop idles ,
	// correct: no context change, no priming, no cue. cued is present and ready, waiting for both the
	// signals and synthd, without faking either.
	lg.Info("up", "fn", "main", "healthPort", *port, "surfaceThreshold", cfg.SurfaceThreshold,
		"cooldownSeconds", cfg.CooldownSeconds)
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

func reflectionLoop(ctx context.Context, mount string, store *hw.NotifStore, slot int, lg *slog.Logger) {
	t := time.NewTicker(30 * time.Minute)
	defer t.Stop()
	var db *poltergres.ReadWrite
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		h := time.Now().Hour()
		if h < 8 || h > 11 {
			continue
		}
		today := time.Now().Format("2006-01-02")
		if v, err := store.GetSetting(slot, "cued_reflected"); err == nil && v == today {
			continue
		}
		if db == nil {
			m := findMount(mount)
			if m == "" {
				continue // no mounted volume in sight; try again next tick
			}
			sc, err := hw.LoadServicesConfig(m)
			if err != nil {
				continue
			}
			db = poltergres.NewReadWrite(hw.SocketForMount(m), sc.Postgres.Port, sc.Postgres.RWUser, sc.Postgres.RWPass, sc.Postgres.Name)
		}
		pick := func(q string, args ...any) (string, string, bool) {
			rows, err := db.Query(q, args...)
			if err != nil || len(rows.Vals) == 0 || len(rows.Vals[0]) < 2 || rows.Vals[0][0] == nil || rows.Vals[0][1] == nil {
				return "", "", false
			}
			return *rows.Vals[0][0], *rows.Vals[0][1], true
		}
		yearAgo := time.Now().AddDate(-1, 0, 0).Format("2006-01-02")
		title, body, ok := pick(
			"SELECT title, body FROM memories WHERE kind = 'episode' AND NOT tombstoned AND source_ref = $1",
			"episode:"+yearAgo)
		head := "one year ago today"
		if !ok {
			title, body, ok = pick(
				"SELECT title, body FROM memories WHERE kind = 'episode' AND NOT tombstoned AND created_at < $1 ORDER BY random() LIMIT 1",
				time.Now().AddDate(0, 0, -30).UnixMilli())
			head = "a day worth revisiting"
		}
		if !ok {
			// A young archive earns quiet mornings. Mark the day so we do not poll until tomorrow.
			_ = store.SetSetting(slot, "cued_reflected", today)
			continue
		}
		if err := store.Produce(slot, hw.Notification{
			Service: "ghost.cued", Kind: "reflection",
			Title: head + " , " + title,
			Body:  body + " It is in your MEMORIES.",
		}); err != nil {
			lg.Warn("reflection produce failed", "fn", "reflectionLoop", "err", err)
			db = nil
			continue
		}
		lg.Info("reflection offered", "fn", "reflectionLoop", "day", title)
		_ = store.SetSetting(slot, "cued_reflected", today)
	}
}

// findMount walks UP from a run dir until it finds the directory that actually contains
// services.conf , the volume mount. Twice this codebase guessed the relationship between runDir
// and the mount and twice the guess was wrong in a different direction; the filesystem knows,
// so ask it. Five levels is generous; empty means no mounted volume in sight.
func findMount(start string) string {
	d := start
	for i := 0; i < 5; i++ {
		if _, err := os.Stat(filepath.Join(d, "services.conf")); err == nil {
			return d
		}
		nd := filepath.Dir(d)
		if nd == d {
			break
		}
		d = nd
	}
	return ""
}
