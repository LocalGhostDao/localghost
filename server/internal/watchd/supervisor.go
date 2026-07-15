// Package watchd is the box's supervisor and janitor. It runs ON the encrypted volume (started by
// ghost.secd after the DBs come up) and owns the lifecycle of every ghost.*d daemon: spawn, health
// poll, restart with backoff, and , the property the unmount depends on , tear the whole cohort down
// and CONFIRM every process is dead on its own SIGTERM. It also owns the daemon logs (one file each,
// under <mount>/logs) and rotates them.
//
// The split from secd: secd is the only thing on the unencrypted disk and is freely restartable. It
// mounts, starts Postgres + Redis, then starts watchd, then serves. watchd starts the rest. secd
// never touches the ghost.*d lifecycle , it asks watchd over a unix control socket (see control.go).
// So a secd redeploy does not orphan anything: on secd SIGTERM secd stops watchd (which tears the
// cohort down), stops the DBs, and unmounts , one clean shutdown path, always.
//
// pg/redis are NOT supervised here. secd owns them via DataStore, started before watchd. Keeping the
// supervisor downstream of the DBs (and downstream of the mount) means watchd can restart-loop a
// daemon all it likes without ever threatening the mount or the one pg data dir.
package watchd

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ghosthealth"
)

// serviceState is the per-service state machine. Restarting/Backoff exist to stop a mid-restart
// no-response from triggering ANOTHER restart (the thundering loop that, on a stateful process over
// one data dir, wedges things). A service in Restarting/Backoff is not polled for restart.
type serviceState int

const (
	stateDown serviceState = iota
	stateUp
	stateRestarting
	stateBackoff
)

func (s serviceState) String() string {
	switch s {
	case stateUp:
		return "up"
	case stateRestarting:
		return "restarting"
	case stateBackoff:
		return "backoff"
	default:
		return "down"
	}
}

// Service describes one managed ghost.*d process. Postgres and Redis are deliberately NOT here , they
// are part of secd's mount/unmount lifecycle (mount -> pg/redis -> watchd, reversed on lock), because
// bringing the data plane up IS mounting the data, and keeping a supervisor away from pg avoids the
// thundering-restart-over-one-data-dir wedge entirely. watchd supervises only the stateless daemons.
type Service struct {
	Name       string
	Critical   bool
	HealthPort int    // loopback /health port
	BinPath    string // absolute path on the encrypted volume, e.g. <mount>/bin/ghost.noted
}

type serviceRuntime struct {
	svc        Service
	state      serviceState
	proc       *os.Process
	restarts   int
	lastErr    string
	lastCode   ghosthealth.Code
	backoffTil time.Time
}

// Supervisor manages the cohort. logDir is on the encrypted volume; each daemon's stdout/stderr go to
// a per-day file <logDir>/<name>-YYYY-MM-DD.log. The Roller archives completed days and prunes to 7.
type Supervisor struct {
	mu       sync.Mutex
	services map[string]*serviceRuntime
	order    []string
	pollEach time.Duration
	client   *http.Client
	stopPoll context.CancelFunc
	wg       sync.WaitGroup
	logDir   string
	mount    string  // the encrypted volume root; daemons get it as GHOST_MOUNT
	runUser  string  // if set, daemons are spawned as this user
	jlog     *slog.Logger // watchd's own log (through a rotlog.RotWriter)
}

// New builds a supervisor. mount is the encrypted volume root; logDir is <mount>/logs. jlog is
// watchd's own slog logger (writing through a rotlog.RotWriter).
func New(mount, runUser string, jlog *slog.Logger) *Supervisor {
	return &Supervisor{
		services: map[string]*serviceRuntime{},
		pollEach: 5 * time.Second,
		client:   &http.Client{Timeout: 2 * time.Second},
		mount:    mount,
		logDir:   filepath.Join(mount, "logs"),
		runUser:  runUser,
		jlog:     jlog,
	}
}

// Register adds a service before StartAll. Registration order is start order.
func (s *Supervisor) Register(svc Service) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.services[svc.Name]; ok {
		return
	}
	s.services[svc.Name] = &serviceRuntime{svc: svc, state: stateDown}
	s.order = append(s.order, svc.Name)
}

// StartAll launches every registered service in order, then begins the poll loop. A CRITICAL service
// that will not start is returned as an error (secd surfaces it via /v1/status; the box stays mounted
// and serving). Non-critical start failures are logged and supervised for restart.
//
// IDEMPOTENT by rule: start-cohort now means "ensure the cohort is running", because secd re-issues
// it at every unlock (the convergent-unlock rule). A service whose process is alive is skipped, and
// the poll loop is started at most once , so re-issuing against a healthy cohort is a no-op, while a
// half-dead cohort gets exactly its dead members respawned.
func (s *Supervisor) StartAll(ctx context.Context) error {
	s.mu.Lock()
	var criticalErr error
	for _, name := range s.order {
		rt := s.services[name]
		// Signal 0 probes liveness without touching the process. Alive means leave it alone ,
		// spawning a second copy of a running daemon is strictly worse than any alternative.
		if rt.proc != nil && rt.proc.Signal(syscall.Signal(0)) == nil {
			continue
		}
		if err := s.startOne(rt); err != nil {
			rt.lastErr = err.Error()
			if rt.svc.Critical {
				s.jlog.Error("critical service failed to start", "fn", "StartAll", "svc", name, "err", err)
				if criticalErr == nil {
					criticalErr = fmt.Errorf("critical service %s: %w", name, err)
				}
			} else {
				s.jlog.Warn("service failed to start", "fn", "StartAll", "svc", name, "err", err)
			}
		}
	}
	// At most ONE poll loop, however many times start-cohort is issued. stopPoll is the marker: set
	// while a loop runs, cleared by TeardownAll after the loop is confirmed gone.
	if s.stopPoll == nil {
		pctx, cancel := context.WithCancel(ctx)
		s.stopPoll = cancel
		s.wg.Add(1)
		go s.pollLoop(pctx)
	}
	s.mu.Unlock()
	return criticalErr
}

func (s *Supervisor) startOne(rt *serviceRuntime) error {
	proc, err := s.spawn(rt.svc)
	if err != nil {
		rt.state = stateBackoff
		return err
	}
	rt.proc = proc
	rt.state = stateRestarting
	rt.backoffTil = time.Now().Add(3 * time.Second)
	return nil
}

func (s *Supervisor) pollLoop(ctx context.Context) {
	defer s.wg.Done()
	t := time.NewTicker(s.pollEach)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			s.pollOnce()
		}
	}
}

func (s *Supervisor) pollOnce() {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now()
	for _, name := range s.order {
		rt := s.services[name]
		switch rt.state {
		case stateRestarting:
			if now.Before(rt.backoffTil) {
				continue
			}
			rt.state = stateUp
			fallthrough
		case stateUp:
			code, err := s.probe(rt)
			if err != nil || code == ghosthealth.Failed {
				s.markFailedAndRestart(rt, code, err)
			} else {
				rt.lastCode = code
				rt.lastErr = ""
			}
		case stateBackoff:
			if now.After(rt.backoffTil) {
				s.jlog.Info("restarting after backoff", "fn", "pollOnce", "svc", rt.svc.Name, "attempt", rt.restarts+1)
				if err := s.startOne(rt); err != nil {
					s.scheduleBackoff(rt, err.Error())
				}
			}
		}
	}
}

func (s *Supervisor) probe(rt *serviceRuntime) (ghosthealth.Code, error) {
	if rt.svc.HealthPort == 0 {
		return ghosthealth.OK, nil
	}
	url := fmt.Sprintf("http://127.0.0.1:%d/health", rt.svc.HealthPort)
	resp, err := s.client.Get(url)
	if err != nil {
		return ghosthealth.Failed, err
	}
	defer resp.Body.Close()
	var h ghosthealth.Health
	if err := json.NewDecoder(resp.Body).Decode(&h); err != nil {
		return ghosthealth.Failed, err
	}
	if h.Name != "" && h.Name != rt.svc.Name {
		return ghosthealth.Failed, fmt.Errorf("port %d answered as %q, expected %q",
			rt.svc.HealthPort, h.Name, rt.svc.Name)
	}
	return h.Code, nil
}

func (s *Supervisor) markFailedAndRestart(rt *serviceRuntime, code ghosthealth.Code, err error) {
	detail := "health code failed"
	if err != nil {
		detail = err.Error()
	}
	s.jlog.Warn("service failed, scheduling restart", "fn", "markFailedAndRestart", "svc", rt.svc.Name, "detail", detail)
	s.killProc(rt)
	s.scheduleBackoff(rt, detail)
}

func (s *Supervisor) scheduleBackoff(rt *serviceRuntime, detail string) {
	rt.restarts++
	rt.lastErr = detail
	rt.state = stateBackoff
	backoff := time.Duration(1<<min(rt.restarts, 6)) * time.Second
	if backoff > 60*time.Second {
		backoff = 60 * time.Second
	}
	rt.backoffTil = time.Now().Add(backoff)
}

// killProc signals TERM, then after a grace KILL, and reaps. Closes the log file too.
func (s *Supervisor) killProc(rt *serviceRuntime) {
	if rt.proc == nil {
		return
	}
	_ = rt.proc.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _, _ = rt.proc.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = rt.proc.Kill()
		<-done
	}
	rt.proc = nil
}

// TeardownAll stops the poll loop, then stops every service in reverse order and CONFIRMS each is
// dead. Returns only after every daemon process is gone , the property secd's unmount depends on.
func (s *Supervisor) TeardownAll() error {
	s.mu.Lock()
	if s.stopPoll != nil {
		s.stopPoll()
		s.stopPoll = nil // cleared only here, after which StartAll may start a fresh loop
	}
	s.mu.Unlock()
	s.wg.Wait()

	s.mu.Lock()
	defer s.mu.Unlock()
	for i := len(s.order) - 1; i >= 0; i-- {
		rt := s.services[s.order[i]]
		s.killProc(rt)
		rt.state = stateDown
	}
	return nil
}

// RestartOne kills and respawns a single service from its (possibly updated) binary on the volume.
// This is the deploy primitive: drop a new binary at <mount>/bin/<name>, then ask watchd to restart
// it over the control socket. No pkill, no orphan.
func (s *Supervisor) RestartOne(name string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	rt, ok := s.services[name]
	if !ok {
		return fmt.Errorf("unknown service %q", name)
	}
	s.jlog.Info("operator-requested restart", "fn", "RestartOne", "svc", name)
	s.killProc(rt)
	rt.restarts = 0
	if err := s.startOne(rt); err != nil {
		s.scheduleBackoff(rt, err.Error())
		return err
	}
	return nil
}

// Status is the snapshot secd fetches over the socket for /v1/status.
func (s *Supervisor) Status() []ServiceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]ServiceStatus, 0, len(s.order))
	for _, name := range s.order {
		rt := s.services[name]
		out = append(out, ServiceStatus{
			Name:     rt.svc.Name,
			Critical: rt.svc.Critical,
			State:    rt.state.String(),
			Restarts: rt.restarts,
			LastErr:  rt.lastErr,
			Code:     uint8(rt.lastCode),
		})
	}
	return out
}

// ServiceStatus is one service's supervision view, serialised over the socket and into /v1/status.
type ServiceStatus struct {
	Name     string `json:"name"`
	Critical bool   `json:"critical"`
	State    string `json:"state"`
	Restarts int    `json:"restarts"`
	LastErr  string `json:"lastErr,omitempty"`
	Code     uint8  `json:"code"`
}

// AllProcessesDead reports whether no supervised process is still running (teardown assertion).
func (s *Supervisor) AllProcessesDead() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, name := range s.order {
		if s.services[name].proc != nil {
			return false
		}
	}
	return true
}

// spawn execs a daemon from its volume binary, wiring stdout/stderr to its rotated log file, in its
// own process group. If runUser is set, the process is dropped to that uid/gid.
func (s *Supervisor) spawn(svc Service) (*os.Process, error) {
	// watchd does NOT open the daemon's log file. The daemon opens its own rotlog.RotWriter using
	// GHOST_LOG_DIR (set below) and logs structured lines through it , self-rotating at midnight with
	// no restart. watchd only tells it WHERE (the dir); the daemon owns the fd, so a four-year run
	// rolls a file per day on its own. stdout/stderr are left inherited so a raw panic still surfaces
	// (to watchd's journal); the daemon's real logs go through its own writer, not an fd we hold.
	cmd := exec.Command(svc.BinPath, "--health-port", fmt.Sprint(svc.HealthPort))
	cmd.Env = append(os.Environ(),
		fmt.Sprintf("GHOST_HEALTH_PORT=%d", svc.HealthPort),
		fmt.Sprintf("GHOST_LOG_DIR=%s", s.logDir),
		fmt.Sprintf("GHOST_MOUNT=%s", s.mount),
		fmt.Sprintf("GHOST_RUN_DIR=%s", filepath.Join(s.mount, "run")),
		// GHOST_LOG_LEVEL is inherited from watchd's own env (os.Environ), so the whole cohort logs at
		// the level the box was started with. Set explicitly here too in case watchd wants to override
		// it per-cohort later; for now it is a passthrough of watchd's environment.
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if cred := s.userCred(); cred != nil {
		cmd.SysProcAttr.Credential = cred
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Process, nil
}
