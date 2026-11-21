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
// bounded windows, never disableable as a fleet.
//
// WHAT RUNS (internal/shadow, where all of it lives; docs/SHADOW.md says it whole):
//
//   - THE ARBITER, afterwards. An answer to a question of judgement, once it has been done ten
//     minutes, is asked again cold (no memories, no history, nothing about the person) and the
//     two are compared. Where they part, the person is told what the cold read said. The
//     conversation is read for the box giving way to a bare push, pressing past a reason the
//     person gave, and asking its way to an answer; each finding must quote the box's own
//     sentence from the chat or it is dropped. Lowest priority: a person using the model takes
//     its place and the answer waits.
//   - THE DETECTORS THAT COUNT, hourly, from what the box already holds: messages week on
//     week, long and late sittings, the person's own sentences (what is spent, a label, how
//     they feel), pressure in the mail that arrives, the age of the memories the box brings,
//     how often it sides with the person, how often it opens with praise.
//   - THE SETTINGS' OWN GUARD: every threshold and mute is logged, three changes in a row
//     toward quiet are named, thresholds go back to the baseline every ninety days.
//
// Seventeen detectors over fourteen of the catalogue's thirty entries. The other sixteen are
// listed as not built, each with its reason (they need the person's conversations with other
// people, or a classifier nobody has). The arbiter reads with a second model when the box has
// one (ai-models/arbiter-<name>.gguf, run by ghost.oracled on the CPU) and with the box's main
// model when it has not, and its record says which it was.
//
// Runs only while the account is UNLOCKED (data lives on the encrypted volume). Spawned by ghost.watchd
// from <mount>/bin/ghost.shadowd; its stdout/stderr go to <mount>/logs/ghost.shadowd.log.
package main

import (
	"context"
	"encoding/json"
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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/ghosthealth"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/rotlog"
	"github.com/LocalGhostDao/localghost/server/internal/shadow"
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
	// THE HEALTH LINE: how much of the catalogue runs, what it has said, how many answers the
	// arbiter has read cold.
	fleet := fmt.Sprintf("%d detectors over the catalogue's %d patterns", len(shadow.Detectors), len(shadow.Catalogue))
	srv := ghosthealth.NewServer(service, ghosthealth.Cached(service, time.Minute, func() string {
		mount := ""
		if runDir != "" {
			mount = findMount(filepath.Dir(runDir))
		}
		if mount == "" {
			return fleet + ": no volume yet"
		}
		sc, err := hw.LoadServicesConfig(mount)
		if err != nil {
			return "the volume's database is not up"
		}
		db := poltergres.NewReadWrite(hw.SocketForMount(mount), sc.Postgres.Port, sc.Postgres.RWUser, sc.Postgres.RWPass, sc.Postgres.Name)
		n := func(q string) string {
			if rows, qerr := db.Query(q); qerr == nil && len(rows.Vals) == 1 && rows.Vals[0][0] != nil {
				return *rows.Vals[0][0]
			}
			return "0"
		}
		return fleet + " · observations made " + n("SELECT count(*) FROM shadow_observations") +
			" · answers read cold " + n("SELECT count(*) FROM shadow_reads WHERE cold <> ''")
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
		mount := findMount(filepath.Dir(runDir))
		if mount != "" {
			sc, err := hw.LoadServicesConfig(mount)
			if err == nil {
				db := poltergres.NewReadWrite(hw.SocketForMount(mount), sc.Postgres.Port, sc.Postgres.RWUser, sc.Postgres.RWPass, sc.Postgres.Name)
				oc := oracle.NewClient(runDir, 3*time.Minute)
				bindCommands(ctl, db, oc, lg)
				go detectorLoop(ctx, db, oc, lg)
			} else {
				lg.Warn("no services config , detectors idle", "fn", "main", "err", err)
			}
		} else {
			lg.Warn("no mounted volume found from run dir , detectors idle", "fn", "main")
		}
	}

	lg.Info("up: "+fleet+"; the arbiter reads saved answers cold, afterwards", "fn", "main", "healthPort", *port)

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

// one pass at a time: the loop's and one a person asked for do not run through each other
var passMu sync.Mutex

// bindCommands is the daemon by hand (ghost-cli ghost.shadowd <command>), and what secd calls
// when the phone asks for a chat to be read now:
//
//	fleet          every detector: its threshold, its mute, how often it has spoken
//	said           the last things named (detector=<id> for one detector's)
//	detect         run the detectors that count now, not at the next hour
//	read           run the arbiter's pass now; read chat=<id> reads that chat now, however fresh
//	try            the self-test: nine written cases through the arbiter's model, with what it
//	               said where a case did not pass. Run it after the model is changed.
//
// namedLately is how much the fleet has named in thirty days, and how many answers the arbiter
// has looked at, in a line.
func namedLately(db *poltergres.ReadWrite, now time.Time) string {
	n := func(q string, args ...any) string {
		rows, err := db.Query(q, args...)
		if err != nil || len(rows.Vals) != 1 || rows.Vals[0][0] == nil {
			return "0"
		}
		return *rows.Vals[0][0]
	}
	since := now.Add(-30 * 24 * time.Hour).Unix()
	return fmt.Sprintf("named in thirty days: %s (%s while muted) · answers looked at: %s, read cold: %s",
		n("SELECT count(*) FROM shadow_observations WHERE ts >= $1", since),
		n("SELECT count(*) FROM shadow_observations WHERE muted AND ts >= $1", since),
		n("SELECT count(*) FROM shadow_reads"),
		n("SELECT count(*) FROM shadow_reads WHERE cold <> ''"))
}

// readBy says which model the cold read is read by, for a terminal.
func readBy(oc *oracle.Client) string {
	a, err := oc.Arbiter()
	switch {
	case err != nil:
		return "the cold read is by: unknown, ghost.oracled does not answer (" + err.Error() + ")"
	case a.Has && a.Up:
		return "the cold read is by: " + a.Model + ", a second model beside " + a.Main
	case a.Has:
		return "the cold read is by: " + a.ServedBy + " (" + a.Model + " is not up: " + a.Why + ")"
	}
	return "the cold read is by: " + a.Main + ", the box's one model, with nothing about you. A second model at ai-models/arbiter-<name>.gguf makes it a second opinion (docs/SHADOW.md)."
}

func bindCommands(ctl *ctlsock.Server, db *poltergres.ReadWrite, oc *oracle.Client, lg *slog.Logger) {
	data := func(v any, text string) (ctlsock.Response, error) {
		b, err := json.Marshal(v)
		if err != nil {
			return ctlsock.Response{}, err
		}
		return ctlsock.Response{OK: true, Text: text, Data: b}, nil
	}
	// for a terminal: ghost-cli prints the data after the text, so a command a person reads
	// gives text alone
	words := func(text string) (ctlsock.Response, error) { return ctlsock.Response{OK: true, Text: text}, nil }
	said := func() int64 {
		rows, err := db.Query("SELECT count(*) FROM shadow_observations")
		if err != nil || len(rows.Vals) != 1 || rows.Vals[0][0] == nil {
			return 0
		}
		n, _ := strconv.ParseInt(*rows.Vals[0][0], 10, 64)
		return n
	}
	ctl.Handle("fleet", func(json.RawMessage) (ctlsock.Response, error) {
		now := time.Now()
		st := shadow.State(db, now)
		var b strings.Builder
		for _, d := range st {
			line := fmt.Sprintf("%-17s entry %2d  threshold %-5s (%s to %s)  spoken %d", d.ID, d.Entry,
				strconv.FormatFloat(d.Threshold, 'f', -1, 64), strconv.FormatFloat(d.Min, 'f', -1, 64), strconv.FormatFloat(d.Max, 'f', -1, 64), d.Fired)
			if d.MutedUntil > 0 {
				line += "  muted until " + time.Unix(d.MutedUntil, 0).Format("2 Jan 15:04")
			}
			b.WriteString(line + "\n")
		}
		// the last three lines are what tools/health.sh prints: named lately, the reset, who reads
		b.WriteString(namedLately(db, now) + "\n")
		b.WriteString("thresholds go back to their baseline on " + time.Unix(shadow.NextReset(db, now), 0).Format("2 January 2006") + "\n")
		b.WriteString(readBy(oc) + "\n")
		return words(b.String())
	})
	ctl.Handle("said", func(args json.RawMessage) (ctlsock.Response, error) {
		var q struct {
			Detector string `json:"detector"`
			Limit    int    `json:"limit"`
		}
		_ = json.Unmarshal(args, &q)
		obs, err := shadow.Observations(db, q.Detector, q.Limit)
		if err != nil {
			return ctlsock.Response{}, err
		}
		var b strings.Builder
		for _, o := range obs {
			muted := ""
			if o.Muted {
				muted = " (while muted)"
			}
			b.WriteString(fmt.Sprintf("%s  %s%s\n  %s\n  %s\n", time.Unix(o.At, 0).Format("2 Jan 15:04"), o.Detector, muted, o.Title, o.Body))
		}
		if len(obs) == 0 {
			b.WriteString("nothing named yet\n")
		}
		return words(b.String())
	})
	ctl.Handle("detect", func(json.RawMessage) (ctlsock.Response, error) {
		passMu.Lock()
		defer passMu.Unlock()
		before := said()
		err := shadow.Detect(db, time.Now(), lg)
		n := said() - before
		text := fmt.Sprintf("the detectors that count ran: %d new\n", n)
		if err != nil {
			text += "one of them failed: " + err.Error() + "\n"
		}
		return words(text)
	})
	ctl.Handle("read", func(args json.RawMessage) (ctlsock.Response, error) {
		var q struct {
			Chat int64 `json:"chat"`
			// later: started and not waited for (the phone, through secd: a request held open for
			// minutes does not survive a phone). Where it has got to is shadow.AskedFor.
			Later bool `json:"later"`
		}
		_ = json.Unmarshal(args, &q)
		if q.Chat > 0 && q.Later {
			if !shadow.MarkAsked(db, q.Chat, time.Now()) {
				return data(map[string]any{"started": false, "reading": true}, "")
			}
			go func() {
				passMu.Lock()
				defer passMu.Unlock()
				n, err := shadow.ReadChat(db, oc, q.Chat, time.Now(), lg)
				shadow.MarkRead(db, q.Chat, n, err, time.Now())
				if err != nil {
					lg.Info("a read asked for did not finish", "fn", "read", "chat", q.Chat, "read", n, "err", err)
				}
			}()
			return data(map[string]any{"started": true, "reading": true}, "")
		}
		passMu.Lock()
		defer passMu.Unlock()
		before := said()
		var n int
		var err error
		if q.Chat > 0 {
			n, err = shadow.ReadChat(db, oc, q.Chat, time.Now(), lg)
		} else {
			n, err = shadow.ReadAnswers(db, oc, time.Now(), lg)
		}
		if err != nil {
			return ctlsock.Response{}, fmt.Errorf("read %d, then: %w", n, err)
		}
		text := fmt.Sprintf("read %d, named %d\n", n, said()-before)
		if q.Chat > 0 {
			obs, _ := shadow.ObservationsAbout(db, fmt.Sprintf("chat:%d", q.Chat))
			for _, o := range obs {
				text += fmt.Sprintf("%s  %s\n  %s\n", o.Detector, o.Title, o.Body)
			}
		}
		return words(text)
	})
	ctl.Handle("try", func(json.RawMessage) (ctlsock.Response, error) {
		passMu.Lock()
		defer passMu.Unlock()
		ts, err := shadow.Trials(oc)
		text := shadow.TrialsText(ts)
		if err != nil {
			text += "stopped: the model could not be asked: " + err.Error() + "\n"
		}
		return words(text)
	})
}

// detectorLoop is the daemon's work, all of it in internal/shadow: the detectors that count,
// once an hour (and two minutes after the start, so a box unlocked for ten minutes still gets
// a pass); the arbiter's pass over saved answers, every ten minutes. Both are bounded and both
// are safe to stop at any point: what has been said is said once, what has not is found again.
func detectorLoop(ctx context.Context, db *poltergres.ReadWrite, oc *oracle.Client, lg *slog.Logger) {
	count := time.NewTimer(2 * time.Minute)
	read := time.NewTicker(10 * time.Minute)
	defer count.Stop()
	defer read.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-count.C:
			count.Reset(time.Hour)
			passMu.Lock()
			err := shadow.Detect(db, time.Now(), lg)
			passMu.Unlock()
			if err != nil {
				lg.Warn("detectors", "fn", "detectorLoop", "err", err)
			}
		case <-read.C:
			// one answer at a time, the lock let go between them: a read a person asked for
			// (the phone's "read it cold now") gets in after the answer in hand, not after
			// the pass, which is minutes with a second model on the CPU
			n := 0
			for i := 0; i < 16 && n < 4 && ctx.Err() == nil; i++ {
				passMu.Lock()
				done, more, err := shadow.ReadNext(db, oc, time.Now(), lg)
				passMu.Unlock()
				n += done
				if err != nil {
					lg.Warn("the arbiter's pass", "fn", "detectorLoop", "err", err)
					break
				}
				if !more {
					break
				}
			}
			if n > 0 {
				lg.Info("answers looked at", "fn", "detectorLoop", "n", n)
			}
		}
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
