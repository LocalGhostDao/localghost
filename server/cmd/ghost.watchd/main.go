// ghost.watchd is the box's supervisor and janitor. It runs ON the encrypted volume, started by
// ghost.secd after Postgres + Redis are up. It reads the cohort from services.conf, spawns each
// ghost.*d daemon from <mount>/bin, health-polls them, restarts with backoff, owns their logs under
// <mount>/logs (with rotation), and exposes a control socket at <mount>/run/watchd.sock that secd
// drives (start-cohort / stop-cohort / restart <name> / status).
//
// Lifecycle:
//   - secd mounts the volume, starts pg+redis, then execs ghost.watchd --mount <path> [--user <name>]
//   - watchd opens its log + socket, registers the cohort, waits for secd's start-cohort
//   - on SIGTERM (secd stopping it, as part of a clean lock), watchd tears the WHOLE cohort down and
//     confirms every process dead before exiting , the property secd's unmount depends on.
//
//	ghost.watchd --mount /var/lib/ghost/mnt/slot0 --user ghost
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/rotlog"
	"github.com/LocalGhostDao/localghost/server/internal/svcconf"
	"github.com/LocalGhostDao/localghost/server/internal/watchd"
)

func main() {
	mount := flag.String("mount", "", "path to the mounted encrypted volume (required)")
	runUser := flag.String("user", "", "run daemons as this user (empty = inherit watchd's user)")
	flag.Parse()
	if *mount == "" {
		fmt.Fprintln(os.Stderr, "ghost.watchd: --mount is required")
		os.Exit(2)
	}

	logDir := filepath.Join(*mount, "logs")
	// watchd's own log goes through a self-rotating rotlog writer (new file at midnight, no restart),
	// same as every daemon. jlog is the structured slog logger over it.
	lw, err := rotlog.New(logDir, "watchd")
	if err != nil {
		fmt.Fprintln(os.Stderr, "ghost.watchd: open log:", err)
		os.Exit(1)
	}
	defer lw.Close()
	jlog, lvl := rotlog.Logger(lw)
	jlog.Info("starting", "fn", "main", "mount", *mount, "user", *runUser)

	cfg, err := hw.LoadServicesConfig(*mount)
	if err != nil {
		jlog.Error("read services.conf", "fn", "main", "err", err)
		os.Exit(1)
	}
	critical := map[string]bool{}
	for _, name := range cfg.Critical {
		critical[name] = true
	}

	// watchd's own conf carries the log disk-guard caps (soft/hard) and its own log level.
	wconf := svcconf.DefaultBase()
	_ = svcconf.Load(svcconf.Path(*mount, "ghost.watchd"), &wconf)
	svcconf.FillBaseDefaults(&wconf)
	_ = svcconf.ApplyLevel(lvl, wconf.LogLevel)

	// cohort names for the disk-guard (which service to quieten if over-logging).
	cohortNames := make([]string, 0, len(cfg.Daemons))
	for name := range cfg.Daemons {
		cohortNames = append(cohortNames, name)
	}
	runDir := filepath.Join(*mount, "run")

	// SYSTEM HEALTH's stats sampler , 10s ring buffers into the slot's Redis for every tracked
	// target (cohort health, datastores, host vitals). Lives as long as watchd, which is as long
	// as the volume is unlocked , exactly the window in which its Redis exists. secd only reads.
	if sampler, serr := watchd.NewStatsSampler(*mount, jlog); serr != nil {
		jlog.Warn("stats sampler unavailable", "fn", "main", "err", serr)
	} else {
		go sampler.Loop(context.Background())
	}

	// The Roller is the JANITOR: rotation happens in each writer itself; the roller gzips completed
	// days into logs/archive, prunes, and runs the disk-guard (soft cap -> ask ghost.oracle; hard cap
	// -> drop oldest archives). Runs for watchd's life.
	guard := watchd.NewDiskGuard(logDir, runDir, wconf.LogSoftCapMB, wconf.LogHardCapMB, cohortNames, jlog)
	roller := watchd.NewRoller(logDir).WithGuard(guard)
	rollerStop := make(chan struct{})
	go roller.Run(rollerStop)
	defer close(rollerStop)

	sup := watchd.New(*mount, *runUser, jlog)
	binDir := filepath.Join(*mount, "bin")
	// Start order comes from the single daemon registry (hw.StartOrder), so it cannot drift from the
	// supervised set or the seed list. Registry order is the start order (shadowd, critical, first).
	// Register in that order, then any extra Daemons a hand-edited services.conf added that the
	// registry does not know about (forward-compat).
	order := hw.StartOrder()
	seen := map[string]bool{}
	for _, name := range order {
		if port, ok := cfg.Daemons[name]; ok {
			sup.Register(watchd.Service{
				Name: name, Critical: critical[name], HealthPort: port,
				BinPath: filepath.Join(binDir, name),
			})
			seen[name] = true
		}
	}
	for name, port := range cfg.Daemons {
		if seen[name] {
			continue
		}
		sup.Register(watchd.Service{
			Name: name, Critical: critical[name], HealthPort: port,
			BinPath: filepath.Join(binDir, name),
		})
	}

	sockPath := filepath.Join(*mount, "run", "watchd.sock")
	ctrl := watchd.NewControlServer(sup, sockPath, jlog)
	defer ctrl.Cleanup()

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// ghost-cli-facing control socket (SEPARATE from the secd-facing control socket above): the base
	// command set plus watchd's own commands, so `ghost-cli ghost.watchd status|reload|cohort` works.
	// reload re-reads watchd.conf and applies the log level live; the disk-guard reads its caps fresh
	// on each 15-min tick, so a reloaded cap takes effect on the next pass without a restart.
	cli := ctlsock.NewServer("ghost.watchd", runDir, jlog)
	svcconf.BindBase(cli, "ghost.watchd", lvl, func() (svcconf.Base, map[string]string, error) {
		fresh := svcconf.DefaultBase()
		if err := svcconf.Load(svcconf.Path(*mount, "ghost.watchd"), &fresh); err != nil {
			return svcconf.Base{}, nil, err
		}
		svcconf.FillBaseDefaults(&fresh)
		// The guard reads caps live each tick, so cap changes are hot; report them as applied.
		return fresh, map[string]string{"logSoftCapMB": "applied", "logHardCapMB": "applied"}, nil
	})
	cli.Handle("cohort", func(json.RawMessage) (ctlsock.Response, error) {
		data, _ := json.Marshal(sup.Status())
		return ctlsock.Response{OK: true, Data: data}, nil
	})
	defer cli.Cleanup()
	go func() {
		if err := cli.Serve(ctx); err != nil {
			jlog.Error("cli control server exited", "fn", "main", "err", err)
		}
	}()

	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		s := <-sigc
		jlog.Info("signal received, tearing down cohort", "fn", "main", "signal", s.String())
		if err := sup.TeardownAll(); err != nil {
			jlog.Error("teardown error", "fn", "main", "err", err)
		}
		cancel()
	}()

	jlog.Info("control socket ready, waiting for secd", "fn", "main", "socket", sockPath)
	if err := ctrl.Serve(ctx); err != nil {
		jlog.Error("control server exited", "fn", "main", "err", err)
	}
	jlog.Info("stopped", "fn", "main")
}
