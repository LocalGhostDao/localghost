// ghost.tallyd takes the phone's Health Connect readout (steps, sleep, heart rate and the rest,
// a day-batch at a time, spooled by secd into <mount>/tallyd/inbox) into health_metrics and
// health_samples, with a journal line per day for the distiller (internal/tally). Its health line
// says whether that is working: a batch that keeps failing, or an inbox nobody drains, shows on
// Box Status instead of "stub ok".
//
// Runs only while the account is UNLOCKED (data lives on the encrypted volume). Exits cleanly on
// SIGTERM so the supervisor's stop-and-confirm-dead teardown never leaves it holding the mount.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log"
	"log/slog"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/ghosthealth"
	"github.com/LocalGhostDao/localghost/server/internal/harden"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/monitor"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"github.com/LocalGhostDao/localghost/server/internal/rates"
	"github.com/LocalGhostDao/localghost/server/internal/rotlog"
	"github.com/LocalGhostDao/localghost/server/internal/svcconf"
	"github.com/LocalGhostDao/localghost/server/internal/tally"
)

const service = "ghost.tallyd"

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

	ing := &ingestState{}
	rs := &ratesState{}
	fs := &fetchState{}
	fast := &fastState{}
	ws := &weatherState{}
	forceFetch := make(chan struct{}, 1)
	forceWeather := make(chan struct{}, 1)
	srv := ghosthealth.NewServer(service, ghosthealth.ReporterFunc(func() ghosthealth.Health {
		h := ing.health()
		if bad, why := fs.stalled(time.Now()); bad && h.Code == ghosthealth.OK {
			return ghosthealth.Health{Code: ghosthealth.Degraded, Name: service, Detail: why}
		}
		if line := rs.line(); line != "" && h.Code == ghosthealth.OK {
			h.Detail += " · rates: " + line
		}
		return h
	}))
	go func() {
		if err := srv.Serve(*port); err != nil {
			lg.Error("health server stopped", "fn", "main", "err", err)
		}
	}()
	// Control socket: base commands (ping/status/reload/log-level/commands) so ghost-cli and watchd
	// can talk to this daemon. A stub has no service-specific commands yet; real logic adds its own.
	runDir := os.Getenv("GHOST_RUN_DIR")
	if runDir == "" {
		if ld := os.Getenv("GHOST_LOG_DIR"); ld != "" {
			runDir = filepath.Join(filepath.Dir(ld), "run")
		}
	}
	hot := &hotRedis{mount: filepath.Dir(runDir)}
	if runDir != "" {
		ctl := ctlsock.NewServer(service, runDir, lg)
		svcconf.BindBase(ctl, service, lvl, func() (svcconf.Base, map[string]string, error) {
			mount := filepath.Dir(runDir)
			base := svcconf.DefaultBase()
			_ = svcconf.Load(svcconf.Path(mount, service), &base)
			svcconf.FillBaseDefaults(&base)
			return base, nil, nil
		})
		// health: what the box holds (days, samples, each metric's newest day and value), what
		// waits in the inbox, and how the last ingest went. `ghost-cli ghost.tallyd health`.
		ctl.Handle("health", func(json.RawMessage) (ctlsock.Response, error) {
			mount := filepath.Dir(runDir)
			out := map[string]any{"ingest": ing.snapshot(), "inbox": inboxDepth(filepath.Join(mount, "tallyd", "inbox"))}
			if cfg, cerr := hw.LoadServicesConfig(mount); cerr == nil {
				db := poltergres.NewReadWrite(hw.SocketForMount(mount), cfg.Postgres.Port, cfg.Postgres.RWUser, cfg.Postgres.RWPass, cfg.Postgres.Name)
				if st, qerr := tally.Query(db); qerr == nil {
					out["stored"] = st
				} else {
					out["storedErr"] = qerr.Error()
				}
			}
			data, _ := json.Marshal(out)
			return ctlsock.Response{OK: true, Data: data}, nil
		})
		// rates: the box's market numbers (the ECB table, the BTC index, the rank list) and how
		// the last batch went; amount= from= to= converts; index=1 lays CRYPTO50 out term by term.
		// `ghost-cli ghost.tallyd rates amount=100 from=EUR to=GBP`.
		ctl.Handle("rates", func(args json.RawMessage) (ctlsock.Response, error) {
			var a struct {
				Amount float64 `json:"amount"`
				From   string  `json:"from"`
				To     string  `json:"to"`
				Add    string  `json:"add"`    // a symbol to follow (SOL)
				Remove string  `json:"remove"` // one to stop following
				Fetch  bool    `json:"fetch"`  // the box fetches now, whatever the phone is on
				Index  bool    `json:"index"`  // CRYPTO50 term by term: each constituent's part of the value now and of the last day's
			}
			if len(args) > 0 {
				_ = json.Unmarshal(args, &a)
			}
			mount := filepath.Dir(runDir)
			out := map[string]any{"ingest": rs.snapshot(), "inbox": inboxDepth(filepath.Join(mount, "tallyd", "rates")), "fetch": fs.snapshot(), "fast": fast.snapshot()}
			cfg, cerr := hw.LoadServicesConfig(mount)
			if cerr != nil {
				return ctlsock.Response{OK: false, Err: cerr.Error()}, nil
			}
			db := poltergres.NewReadWrite(hw.SocketForMount(mount), cfg.Postgres.Port, cfg.Postgres.RWUser, cfg.Postgres.RWPass, cfg.Postgres.Name)
			if a.Add != "" || a.Remove != "" {
				syms := tally.Symbols(db)
				if a.Add != "" {
					syms = append(syms, strings.ToUpper(strings.TrimSpace(a.Add)))
				}
				if a.Remove != "" {
					kept := syms[:0]
					for _, s := range syms {
						if s != strings.ToUpper(strings.TrimSpace(a.Remove)) {
							kept = append(kept, s)
						}
					}
					syms = kept
				}
				seen := map[string]bool{}
				uniq := syms[:0]
				for _, s := range syms {
					if s != "" && !seen[s] {
						seen[s] = true
						uniq = append(uniq, s)
					}
				}
				if err := tally.SetSymbols(db, uniq); err != nil {
					return ctlsock.Response{OK: false, Err: err.Error()}, nil
				}
			}
			out["symbols"] = tally.Symbols(db)
			out["marketsSeen"] = len(tally.MarketsSeen(db, time.Now()))
			if who, at := tally.LastBy(db, "rates_last_by"); who != "" {
				out["lastBatchBy"], out["lastBatchAt"] = who, at
			}
			if st, merr := tally.MarketNow(db, time.Now()); merr == nil {
				out["market"] = st
			} else {
				out["marketErr"] = merr.Error()
			}
			if a.Index {
				if ex, xerr := tally.ExplainMarket(db, time.Now()); xerr == nil {
					out["index"] = ex
				} else {
					out["indexErr"] = xerr.Error()
				}
			}
			if p, ok := tally.LoadProgress(db); ok {
				out["progress"] = p
			}
			if a.Fetch {
				select {
				case forceFetch <- struct{}{}:
					out["fetching"] = "the box fetches now; ask again in a minute"
				default:
					out["fetching"] = "a fetch is already running"
				}
			}
			snap, err := hw.RatesNow(db)
			if err != nil {
				return ctlsock.Response{OK: false, Err: err.Error()}, nil
			}
			if len(snap.Ranks) > 10 {
				snap.Ranks = snap.Ranks[:10]
			}
			out["rates"] = snap
			if a.Amount != 0 && a.From != "" && a.To != "" {
				v, err := rates.Convert(a.Amount, a.From, a.To, snap.FX, snap.USD())
				if err != nil {
					out["convertErr"] = err.Error()
				} else {
					out["converted"] = v
				}
			}
			data, _ := json.Marshal(out)
			return ctlsock.Response{OK: true, Data: data}, nil
		})
		// feeds: how the data the box pulls in is doing, the same report Box Status shows
		// (internal/monitor). `ghost-cli ghost.tallyd feeds`.
		ctl.Handle("feeds", func(json.RawMessage) (ctlsock.Response, error) {
			mount := filepath.Dir(runDir)
			cfg, cerr := hw.LoadServicesConfig(mount)
			if cerr != nil {
				return ctlsock.Response{OK: false, Err: cerr.Error()}, nil
			}
			db := poltergres.NewReadWrite(hw.SocketForMount(mount), cfg.Postgres.Port, cfg.Postgres.RWUser, cfg.Postgres.RWPass, cfg.Postgres.Name)
			data, _ := json.Marshal(monitor.Make(db, time.Now()))
			return ctlsock.Response{OK: true, Data: data}, nil
		})
		// weather: the pull of the world's larger places (each again after sixteen hours) and the forecast where the trail
		// says the phone is. `ghost-cli ghost.tallyd weather [lat= lon= | place=] [fetch=1]`.
		ctl.Handle("weather", func(args json.RawMessage) (ctlsock.Response, error) {
			out, err := weatherCtl(filepath.Dir(runDir), ws, forceWeather, args)
			if err != nil {
				return ctlsock.Response{OK: false, Err: err.Error()}, nil
			}
			data, _ := json.Marshal(out)
			return ctlsock.Response{OK: true, Data: data}, nil
		})
		defer ctl.Cleanup()
		go func() {
			if err := ctl.Serve(ctx); err != nil {
				lg.Error("control server exited", "fn", "main", "err", err)
			}
		}()
	}

	// FIRST REAL SLICE: health ingestion. The app reads the phone's Health Connect store (where
	// Samsung Health writes) and posts day-batches; secd drops them as JSON in <mount>/tallyd/
	// inbox; this loop upserts health_metrics and journals each day once , which is how sleep and
	// movement reach synthd's distillation and the check-in's suggestions. Structured data in,
	// time-series + diary out , exactly the charter.
	if runDir != "" {
		go healthLoop(ctx, filepath.Dir(runDir), ing, lg)
		go ratesLoop(ctx, filepath.Dir(runDir), rs, hot, lg)
		go ratesFetchLoop(ctx, filepath.Dir(runDir), rs, fs, hot, lg, forceFetch)
		go fastLoop(ctx, hot, fast, lg)
		go weatherLoop(ctx, filepath.Dir(runDir), ws, lg, forceWeather)
		lg.Info("health and rates ingestion up; the box fetches rates itself when the phone is not on Wi-Fi, and the weather of the larger places, a hundred every two minutes", "fn", "main")
	} else {
		ing.note("", errors.New("no run dir: ingestion is off (started by hand without GHOST_RUN_DIR)"), tally.Result{})
	}

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

// ingestState is what the last ingests did, for the health line and the `health` command.
type ingestState struct {
	mu        sync.Mutex
	lastAt    time.Time
	lastFile  string
	lastRes   tally.Result
	lastErr   string
	lastErrAt time.Time
	failing   string // the file that keeps failing, "" when none
	ingested  int    // batches taken since start
}

func (s *ingestState) note(file string, err error, res tally.Result) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.lastErr, s.lastErrAt, s.failing = err.Error(), time.Now(), file
		return
	}
	s.lastAt, s.lastFile, s.lastRes, s.failing = time.Now(), file, res, ""
	s.ingested++
}

func (s *ingestState) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]any{"batchesSinceStart": s.ingested}
	if !s.lastAt.IsZero() {
		out["lastAt"] = s.lastAt.Unix()
		out["lastFile"] = s.lastFile
		out["last"] = s.lastRes
	}
	if s.lastErr != "" {
		out["lastError"] = s.lastErr
		out["lastErrorAt"] = s.lastErrAt.Unix()
	}
	if s.failing != "" {
		out["failing"] = s.failing
	}
	return out
}

// health: degraded while a batch keeps failing (the phone's uploads are landing and going
// nowhere), OK otherwise, with the last ingest in the detail.
func (s *ingestState) health() ghosthealth.Health {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failing != "" {
		return ghosthealth.Health{Code: ghosthealth.Degraded, Name: service, Detail: "a health batch keeps failing (" + s.failing + "): " + s.lastErr}
	}
	if s.lastErr != "" && s.lastAt.IsZero() {
		return ghosthealth.Health{Code: ghosthealth.Degraded, Name: service, Detail: s.lastErr}
	}
	d := "no health batch taken since start"
	if !s.lastAt.IsZero() {
		d = fmt.Sprintf("last batch %s ago: %d day(s), %d sample(s), newest %s", time.Since(s.lastAt).Round(time.Minute), s.lastRes.Days, s.lastRes.Samples, s.lastRes.NewestDay)
	}
	return ghosthealth.Health{Code: ghosthealth.OK, Name: service, Detail: d}
}

func inboxDepth(dir string) int {
	es, err := os.ReadDir(dir)
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range es {
		if !e.IsDir() && !strings.HasSuffix(e.Name(), ".part") {
			n++
		}
	}
	return n
}

// healthLoop drains tallyd's inbox: once at start (a batch that landed while tallyd was down
// used to wait for the first tick), then every 30 s. A batch that fails stays and is tried
// again; the health line says so. A file that is not a batch at all is moved aside, once.
func healthLoop(ctx context.Context, mount string, ing *ingestState, lg *slog.Logger) {
	inbox := filepath.Join(mount, "tallyd", "inbox")
	done := filepath.Join(mount, "tallyd", "done")
	for _, d := range []string{inbox, done} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			lg.Error("inbox dirs", "fn", "healthLoop", "err", err)
			ing.note("", fmt.Errorf("cannot make %s: %v", d, err), tally.Result{})
			return
		}
	}
	var db *poltergres.ReadWrite
	drain := func() {
		entries, err := os.ReadDir(inbox)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() || strings.HasSuffix(e.Name(), ".part") {
				continue
			}
			if db == nil {
				cfg, cerr := hw.LoadServicesConfig(mount)
				if cerr != nil {
					ing.note(e.Name(), fmt.Errorf("services.conf: %v", cerr), tally.Result{})
					break
				}
				db = poltergres.NewReadWrite(hw.SocketForMount(mount), cfg.Postgres.Port,
					cfg.Postgres.RWUser, cfg.Postgres.RWPass, cfg.Postgres.Name)
			}
			path := filepath.Join(inbox, e.Name())
			raw, rerr := os.ReadFile(path)
			if rerr != nil {
				ing.note(e.Name(), rerr, tally.Result{})
				continue
			}
			res, ierr := tally.Ingest(db, raw)
			if ierr != nil {
				lg.Warn("health ingest failed, will retry next tick", "fn", "healthLoop", "file", e.Name(), "err", ierr)
				ing.note(e.Name(), ierr, tally.Result{})
				db = nil
				continue
			}
			if res.Unparsable {
				lg.Warn("health batch unparseable, moved aside", "fn", "healthLoop", "file", e.Name())
			} else {
				lg.Info("health batch taken", "fn", "healthLoop", "file", e.Name(), "days", res.Days, "metrics", res.Metrics, "samples", res.Samples, "dropped", res.Dropped, "newest", res.NewestDay)
			}
			ing.note(e.Name(), nil, res)
			_ = os.Rename(path, filepath.Join(done, e.Name()))
		}
		// the raw batches are not kept for ever: the tables hold them now
		pruneDone(done, 200)
	}
	drain()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			drain()
		}
	}
}

// pruneDone keeps the newest keep files under dir.
func pruneDone(dir string, keep int) {
	es, err := os.ReadDir(dir)
	if err != nil || len(es) <= keep {
		return
	}
	names := make([]string, 0, len(es))
	for _, e := range es {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names[:len(names)-keep] {
		_ = os.Remove(filepath.Join(dir, n))
	}
}

// ratesState is how the rates batches have been going, for the `rates` command and the health line.
type ratesState struct {
	mu      sync.Mutex
	lastAt  time.Time
	last    tally.RatesResult
	lastErr string
	taken   int
}

func (s *ratesState) note(err error, res tally.RatesResult) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err != nil {
		s.lastErr = err.Error()
		return
	}
	s.lastAt, s.last, s.lastErr = time.Now(), res, ""
	s.taken++
}

func (s *ratesState) snapshot() map[string]any {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := map[string]any{"batchesSinceStart": s.taken}
	if !s.lastAt.IsZero() {
		out["lastAt"] = s.lastAt.Unix()
		out["last"] = s.last
	}
	if s.lastErr != "" {
		out["lastError"] = s.lastErr
	}
	return out
}

func (s *ratesState) line() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.lastErr != "" {
		return "a batch failed: " + s.lastErr
	}
	if s.lastAt.IsZero() {
		return ""
	}
	return fmt.Sprintf("%s ago, %s", time.Since(s.lastAt).Round(time.Minute), s.last.String())
}

// ratesLoop drains <mount>/tallyd/rates: once at start, then every 30 s. A batch that fails
// stays and is tried again next tick (the database away); a batch that is not a batch is moved
// aside once.
func ratesLoop(ctx context.Context, mount string, rs *ratesState, hot *hotRedis, lg *slog.Logger) {
	inbox := filepath.Join(mount, "tallyd", "rates")
	done := filepath.Join(mount, "tallyd", "rates-done")
	for _, d := range []string{inbox, done} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			lg.Error("rates dirs", "fn", "ratesLoop", "err", err)
			return
		}
	}
	var db *poltergres.ReadWrite
	drain := func() {
		entries, err := os.ReadDir(inbox)
		if err != nil {
			return
		}
		for _, e := range entries {
			if e.IsDir() || strings.HasSuffix(e.Name(), ".part") {
				continue
			}
			if db == nil {
				cfg, cerr := hw.LoadServicesConfig(mount)
				if cerr != nil {
					rs.note(fmt.Errorf("services.conf: %v", cerr), tally.RatesResult{})
					break
				}
				db = poltergres.NewReadWrite(hw.SocketForMount(mount), cfg.Postgres.Port, cfg.Postgres.RWUser, cfg.Postgres.RWPass, cfg.Postgres.Name)
			}
			path := filepath.Join(inbox, e.Name())
			raw, rerr := os.ReadFile(path)
			if rerr != nil {
				continue
			}
			if _, ok := tally.ParseRates(raw); !ok {
				lg.Warn("rates batch unparseable, moved aside", "fn", "ratesLoop", "file", e.Name())
				_ = os.Rename(path, filepath.Join(done, e.Name()))
				continue
			}
			res, ierr := tally.IngestRates(db, raw, time.Now())
			if ierr != nil {
				lg.Warn("rates ingest failed, will retry next tick", "fn", "ratesLoop", "file", e.Name(), "err", ierr)
				rs.note(ierr, tally.RatesResult{})
				db = nil
				continue
			}
			lg.Info("rates batch taken", "fn", "ratesLoop", "file", e.Name(), "what", res.String())
			if len(res.Failed) > 0 {
				lg.Debug("rates sources that did not come", "fn", "ratesLoop", "which", res.Failed)
			}
			rs.note(nil, res)
			_ = os.Rename(path, filepath.Join(done, e.Name()))
			if n, merr := tally.RebuildMarketIndex(db, time.Now()); merr != nil {
				lg.Warn("market index rebuild failed", "fn", "ratesLoop", "err", merr)
			} else if n > 0 {
				lg.Debug("market index rebuilt", "fn", "ratesLoop", "days", n)
			}
			hot.putRates(db, time.Now(), lg) // the phone's batch is in: its copy too
		}
		pruneDone(done, 50)
	}
	drain()
	t := time.NewTicker(30 * time.Second)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			drain()
		}
	}
}
