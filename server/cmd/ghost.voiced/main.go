// ghost.voiced , the person's own voice notes. The phone records one (at the daily check-in, or any
// time), secd spools the WAV into <mount>/voiced/inbox, and this daemon archives it on the encrypted
// volume, transcribes it with whisper.cpp on the box's CPU when the box has a speech model, and
// writes the transcript to the journal (source ghost.voiced, ref voice:<id>). ghost.synthd reads it
// from there into the day's summary and, when it holds something durable, into memories.
//
// The audio is kept: a voice note is the person's own content, like a photo. Deleting one (the app,
// /v1/voice/delete) removes the file, the row and its journal entry. Without a speech model the
// notes are archived and wait; the next pass after `sudo ./tools/update.sh speech` transcribes them.
// Nothing here reaches a network.
//
// Runs only while the account is UNLOCKED (data lives on the encrypted volume). Exits cleanly on
// SIGTERM so the supervisor's stop-and-confirm-dead teardown never leaves it holding the mount; a
// whisper-cli mid-note is killed with it and the note stays pending.
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
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/ghosthealth"
	"github.com/LocalGhostDao/localghost/server/internal/harden"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"github.com/LocalGhostDao/localghost/server/internal/rotlog"
	"github.com/LocalGhostDao/localghost/server/internal/svcconf"
	"github.com/LocalGhostDao/localghost/server/internal/voiced"
)

const service = "ghost.voiced"

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
	var d *voiced.Daemon
	if runDir != "" {
		mount := filepath.Dir(runDir)
		d = &voiced.Daemon{
			Mount: mount,
			Log:   lg,
			Find:  voiced.FindEngine,
			Open: func() (voiced.DB, error) {
				cfg, err := hw.LoadServicesConfig(mount)
				if err != nil {
					return nil, err
				}
				return poltergres.NewReadWrite(hw.SocketForMount(mount), cfg.Postgres.Port,
					cfg.Postgres.RWUser, cfg.Postgres.RWPass, cfg.Postgres.Name), nil
			},
		}
	}

	// Health: OK while there is nothing to wait for; degraded when notes wait for a speech model the
	// box does not have (the app's status screen then says why) or some failed.
	rep := reporter{d: d}
	srv := ghosthealth.NewServer(service, rep)
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
		// one connection for the control commands, made on first use (the loop keeps its own)
		var ctlDB voiced.DB
		var ctlMu sync.Mutex
		ctlOpen := func() (voiced.DB, error) {
			ctlMu.Lock()
			defer ctlMu.Unlock()
			if ctlDB != nil {
				return ctlDB, nil
			}
			db, err := d.Open()
			if err == nil {
				ctlDB = db
			}
			return db, err
		}
		// voice , the counts, the engine and the newest notes
		ctl.Handle("voice", func(args json.RawMessage) (ctlsock.Response, error) {
			db, err := ctlOpen()
			if err != nil {
				return ctlsock.Response{}, err
			}
			text, err := voiced.Report(db, d.Stat())
			if err != nil {
				return ctlsock.Response{}, err
			}
			return ctlsock.Response{OK: true, Text: text}, nil
		})
		// voice-again id=<id>|failed , back in the queue (a better model arrived, or a note failed)
		ctl.Handle("voice-again", func(args json.RawMessage) (ctlsock.Response, error) {
			var a struct {
				ID string `json:"id"`
			}
			if len(args) > 0 {
				_ = json.Unmarshal(args, &a)
			}
			db, err := ctlOpen()
			if err != nil {
				return ctlsock.Response{}, err
			}
			n, err := voiced.Again(db, strings.TrimSpace(a.ID))
			if err != nil {
				return ctlsock.Response{}, err
			}
			return ctlsock.Response{OK: true, Text: fmt.Sprintf("%d note(s) back in the queue; transcribed on the next pass (15 s)", n)}, nil
		})
		// hear path=<mount>/voiced/ask/<name>.wav , the words of a question asked aloud (secd, for
		// the chat); nothing is kept, the file goes
		ctl.Handle("hear", func(args json.RawMessage) (ctlsock.Response, error) {
			var a struct {
				Path string `json:"path"`
			}
			if len(args) > 0 {
				_ = json.Unmarshal(args, &a)
			}
			hctx, cancel := context.WithTimeout(ctx, 3*time.Minute)
			defer cancel()
			res, err := d.Hear(hctx, strings.TrimSpace(a.Path))
			if err != nil {
				return ctlsock.Response{}, err
			}
			data, _ := json.Marshal(map[string]any{"text": res.Text, "lang": res.Lang, "ms": res.Took.Milliseconds()})
			return ctlsock.Response{OK: true, Text: res.Text, Data: data}, nil
		})
		defer ctl.Cleanup()
		go func() {
			if err := ctl.Serve(ctx); err != nil {
				lg.Error("control server exited", "fn", "main", "err", err)
			}
		}()
		go d.Run(ctx)
		lg.Info("voice notes up", "fn", "main", "inbox", filepath.Join(d.Mount, "voiced", "inbox"))
	} else {
		lg.Warn("no run dir , voice notes disabled (health only)", "fn", "main")
	}

	<-ctx.Done()
	lg.Info("shutting down", "fn", "main")
}

type reporter struct{ d *voiced.Daemon }

func (r reporter) Health() ghosthealth.Health {
	if r.d == nil {
		return ghosthealth.Health{Code: ghosthealth.OK, Name: service, Detail: "no run dir"}
	}
	st := r.d.Stat()
	switch {
	case st.Pending > 0 && st.Engine == "":
		return ghosthealth.Health{Code: ghosthealth.Degraded, Name: service,
			Detail: fmt.Sprintf("%d voice note(s) kept, not transcribed: %s", st.Pending, st.Why)}
	case st.Failed > 0:
		return ghosthealth.Health{Code: ghosthealth.Degraded, Name: service,
			Detail: fmt.Sprintf("%d transcribed, %d failed (ghost-cli ghost.voiced voice)", st.Done, st.Failed)}
	case st.Pending > 0:
		return ghosthealth.Health{Code: ghosthealth.OK, Name: service,
			Detail: fmt.Sprintf("transcribing: %d waiting (%s)", st.Pending, st.Engine)}
	case st.Engine == "":
		return ghosthealth.Health{Code: ghosthealth.OK, Name: service,
			Detail: fmt.Sprintf("%d transcribed; no speech model yet", st.Done)}
	}
	return ghosthealth.Health{Code: ghosthealth.OK, Name: service, Detail: fmt.Sprintf("%d transcribed (%s)", st.Done, st.Engine)}
}

func (r reporter) Metrics() json.RawMessage {
	if r.d == nil {
		return nil
	}
	b, _ := json.Marshal(r.d.Stat())
	return b
}

func envPort(key string) int {
	if v := os.Getenv(key); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 0
}
