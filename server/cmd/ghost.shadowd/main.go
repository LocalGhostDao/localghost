// ghost.shadowd , the ANTI-POSSESSION DAEMON (charter: hard-truths/should-not-possess,
// /dictator-brain, /critic-worth-listening-to). NOT the memory layer , that is ghost.synthd. This
// daemon is a fleet of manipulation-pattern DETECTORS (the 28-entry catalogue: gaslighting, DARVO,
// coercive control, addiction-by-design, engagement-driven loneliness, filter-bubble-of-one,
// sunk-cost amplification, memory laundering, self-narrative calcification, arbiter capture, and
// the rest) plus the COLD-READ ARBITER , a separate model that was never shaped by the user, so it
// can disagree (you cannot prompt your way out of a reward function; you cannot configure your way
// out of one either, hence the scheduled arbiter reset to a published baseline).
//
// The contract, from the charter: NAMING IS THE ACTION. Detectors name the pattern, point at the
// data, and stop. No blocking, no refusing, no lockouts unless the user wrote the rule themselves
// (Ulysses contracts, parental controls). Detectors are individually tunable and mutable for
// bounded windows, never disableable as a fleet. The first tractable detectors are the
// [LOCALGHOST]-origin ones that read data the box already holds: total interaction time and
// session-past-task-completion (addiction by design), share of emotional processing done with the
// ghost vs humans (engagement-driven loneliness), sunk-cost retrieval framing, topic-surface
// narrowing (filter bubble of one). This binary is the honest stub of all that: health + ctl only,
// detectors pending, and the box will not ship v1 without it running.
//
// Runs only while the account is UNLOCKED (data lives on the encrypted volume). Spawned by ghost.watchd
// from <mount>/bin/ghost.shadowd; its stdout/stderr go to <mount>/logs/ghost.shadowd.log.
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/LocalGhostDao/localghost/server/internal/harden"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/ghosthealth"
	"github.com/LocalGhostDao/localghost/server/internal/rotlog"
	"github.com/LocalGhostDao/localghost/server/internal/svcconf"
)

const service = "ghost.shadowd"

func main() {
	harden.NoDump() // same-user processes cannot read this one through /proc; no core file
	port := flag.Int("health-port", envPort("GHOST_HEALTH_PORT"), "loopback health/status port (required)")
	flag.Parse()
	if *port <= 0 {
		log.Fatalf("%s: no health port (set --health-port or GHOST_HEALTH_PORT)", service)
	}

	// Logs go through a self-rotating writer: <GHOST_LOG_DIR>/<service>-YYYY-MM-DD.log, a new file at
	// midnight with no restart (watchd sets GHOST_LOG_DIR when it spawns us). If GHOST_LOG_DIR is
	// unset (run by hand), fall back to stderr so nothing is lost.
	var lg *slog.Logger
	var lvl *slog.LevelVar
	if dir := os.Getenv("GHOST_LOG_DIR"); dir != "" {
		w, err := rotlog.New(dir, service)
		if err != nil {
			log.Fatalf("%s: open log: %v", service, err)
		}
		defer w.Close()
		lg, lvl = rotlog.Logger(w)
	} else {
		lvl = new(slog.LevelVar)
		lvl.Set(rotlog.LevelFromEnv())
		lg = slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: lvl}))
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer stop()

	runDir := os.Getenv("GHOST_RUN_DIR")
	if runDir == "" {
		if ld := os.Getenv("GHOST_LOG_DIR"); ld != "" {
			runDir = filepath.Join(filepath.Dir(ld), "run")
		}
	}
	// THE HEALTH LINE: what the detector watches and the observations it has made; "stub ok"
	// was the first week's line.
	srv := ghosthealth.NewServer(service, ghosthealth.Cached(service, time.Minute, func() string {
		mount := ""
		if runDir != "" {
			mount = findMount(filepath.Dir(runDir))
		}
		if mount == "" {
			return "watches how much you talk to the ghost, week against week: no volume yet"
		}
		sc, err := hw.LoadServicesConfig(mount)
		if err != nil {
			return "the volume's database is not up"
		}
		db := poltergres.NewReadWrite(hw.SocketForMount(mount), sc.Postgres.Port, sc.Postgres.RWUser, sc.Postgres.RWPass, sc.Postgres.Name)
		n := "0"
		if rows, qerr := db.Query("SELECT count(*) FROM notifications WHERE service = 'ghost.shadowd'"); qerr == nil && len(rows.Vals) == 1 && rows.Vals[0][0] != nil {
			n = *rows.Vals[0][0]
		}
		return "watches how much you talk to the ghost, week against week · observations made " + n
	}))
	go func() {
		if err := srv.Serve(*port); err != nil {
			lg.Error("health server stopped", "fn", "main", "err", err)
		}
	}()
	if runDir != "" {
		ctl := ctlsock.NewServer(service, runDir, lg)
		svcconf.BindBase(ctl, service, lvl, func() (svcconf.Base, map[string]string, error) {
			mount := filepath.Dir(runDir)
			base := svcconf.DefaultBase()
			_ = svcconf.Load(svcconf.Path(mount, service), &base)
			svcconf.FillBaseDefaults(&base)
			return base, nil, nil
		})
		defer ctl.Cleanup()
		go func() {
			if err := ctl.Serve(ctx); err != nil {
				lg.Error("control server exited", "fn", "main", "err", err)
			}
		}()
		// FIRST REAL DETECTOR , interaction time. The charter in practice: shadowd reads how much
		// the person talked to the ghost (their OWN messages, counted, never content-analysed
		// here) and, when a week doubles the prior week past a floor, says so ONCE, factually.
		// No streaks, no nudges to talk MORE , the only thing this daemon will ever sell you is
		// your own reflection.
		mount := findMount(filepath.Dir(runDir))
		if mount != "" {
			sc, err := hw.LoadServicesConfig(mount)
			if err == nil {
				db := poltergres.NewReadWrite(hw.SocketForMount(mount), sc.Postgres.Port, sc.Postgres.RWUser, sc.Postgres.RWPass, sc.Postgres.Name)
				go detectorLoop(ctx, db, lg)
			} else {
				lg.Warn("no services config , detectors idle", "fn", "main", "err", err)
			}
		} else {
			lg.Warn("no mounted volume found from run dir , detectors idle", "fn", "main")
		}
	}

	lg.Info("stub up (charter recorded, detectors pending)", "fn", "main", "healthPort", *port)

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

// detectorLoop runs the usage-pattern detectors hourly. v1: interaction-time trend.
func detectorLoop(ctx context.Context, db *poltergres.ReadWrite, lg *slog.Logger) {
	t := time.NewTicker(1 * time.Hour)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			if err := interactionTrend(db, lg); err != nil {
				lg.Warn("interaction trend", "fn", "detectorLoop", "err", err)
			}
		}
	}
}

// interactionTrend , this week's user-message count vs the prior week's. Doubling past a floor of
// 60 messages earns ONE factual observation per ISO week, and quiet weeks earn silence.
func interactionTrend(db *poltergres.ReadWrite, lg *slog.Logger) error {
	one := func(q string, args ...any) int64 {
		rows, err := db.Query(q, args...)
		if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
			return 0
		}
		n, _ := strconv.ParseInt(*rows.Vals[0][0], 10, 64)
		return n
	}
	now := time.Now().Unix()
	this7 := one("SELECT count(*) FROM chat_messages WHERE role = 'user' AND ts >= $1", now-7*86400)
	prior7 := one("SELECT count(*) FROM chat_messages WHERE role = 'user' AND ts >= $1 AND ts < $2", now-14*86400, now-7*86400)
	if prior7 == 0 || this7 < 60 || this7 < 2*prior7 {
		return nil
	}
	y, w := time.Now().UTC().ISOWeek() // "2006-W02" was the day of the month (a Go layout)
	wk := fmt.Sprintf("%d-W%02d", y, w)
	rows, err := db.Query("SELECT value FROM settings WHERE key = 'shadow_interaction_note'")
	if err == nil && len(rows.Vals) == 1 && rows.Vals[0][0] != nil && *rows.Vals[0][0] == wk {
		return nil // this week already observed
	}
	if err := db.Exec(
		"INSERT INTO notifications (service, kind, title, body, seen, options, created, link) VALUES ('ghost.shadowd','observation',$1,$2,FALSE,'',now(),'status')",
		"an observation about this week",
		fmt.Sprintf("you sent the ghost %d messages this week, up from %d the week before. Not a problem , just a fact you own. The graphs are on Box Status.", this7, prior7)); err != nil {
		return err
	}
	lg.Info("interaction observation posted", "fn", "interactionTrend", "this7", this7, "prior7", prior7)
	return db.Exec(
		"INSERT INTO settings (key, value) VALUES ('shadow_interaction_note',$1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", wk)
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
