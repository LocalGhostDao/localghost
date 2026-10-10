// Package ghosthealth is the uniform health/status contract every ghost.*d daemon exposes and
// ghost.watchd polls. One implementation, so the shape cannot drift across daemons.
//
// The split, by design:
//   - Health is the NARROW waist the supervisor reads every 5s. One byte of meaning (Code) plus a
//     detail line for the local log. The supervisor's restart decision uses ONLY Code (or a timeout
//     = no response). Uniform across every daemon.
//   - Status is RICHER and per-daemon: name, uptime, and a Metrics blob the daemon fills with
//     whatever it has (queue depth, archive size, ...). ghost.watchd samples it; ghost.secd passes
//     the blob through into /v1/status without parsing it, staying decoupled from daemon internals.
//
// Transport is loopback HTTP on a per-daemon port (from services.conf on the encrypted volume). It
// MUST bind 127.0.0.1, never 0.0.0.0: these ports live behind the appears-down wall and nothing
// external may reach them. Serve() enforces the loopback bind.
package ghosthealth

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"sync"
	"time"
)

// Code is the uniform health status. The supervisor maps: 0 -> Up, 1 -> degraded (keep serving,
// capability may error), 2 -> failed (restart), no response/timeout -> restart.
type Code uint8

const (
	OK       Code = 0
	Degraded Code = 1
	Failed   Code = 2
)

// Health is what /health returns , uniform, tiny, read every poll.
type Health struct {
	Code   Code   `json:"code"`
	Detail string `json:"detail"`
	Name   string `json:"name"` // identity, so a port collision reads as "wrong service" not "healthy"
}

// Status is what /status returns , richer, per-daemon, sampled by watchd and surfaced in /v1/status.
type Status struct {
	Name      string          `json:"name"`
	Health    Health          `json:"health"`
	UptimeSec int64           `json:"uptimeSec"`
	Metrics   json.RawMessage `json:"metrics"` // daemon-specific; secd passes through unparsed
}

// Reporter is what a daemon implements to describe itself. A stub returns OK with nil metrics.
type Reporter interface {
	Health() Health
	Metrics() json.RawMessage
}

// ReporterFunc adapts a plain func into a Reporter for daemons whose health changes at runtime (e.g.
// ghost.oracled is degraded until its model finishes loading). Metrics are nil; use a full Reporter
// if you need metrics too.
type ReporterFunc func() Health

func (f ReporterFunc) Health() Health           { return f() }
func (f ReporterFunc) Metrics() json.RawMessage { return nil }

// Server binds the loopback health/status endpoints for one daemon.
type Server struct {
	name    string
	rep     Reporter
	started time.Time
	extra   []route
}

func NewServer(name string, rep Reporter) *Server {
	currentMu.Lock()
	current = rep
	currentMu.Unlock()
	return &Server{name: name, rep: rep, started: time.Now()}
}

// current is this process's reporter (a daemon makes one health server), so the control socket's
// status command can say what /health says: tools/health.sh reads it there, through the namespace
// door, without knowing each daemon's port.
var (
	currentMu sync.Mutex
	current   Reporter
)

// Current is this process's health as its /health endpoint would answer; ok is false before a
// health server was made.
func Current() (h Health, ok bool) {
	currentMu.Lock()
	rep := current
	currentMu.Unlock()
	if rep == nil {
		return Health{}, false
	}
	return rep.Health(), true
}

// Handle registers an extra route on the daemon's loopback listener , the transport for daemon-to-
// daemon STREAMING, which the one-shot ctlsock protocol cannot carry. Loopback-only binding (below)
// keeps this inside the box; anything security-sensitive still goes through secd's edge. Must be
// called before Serve.
func (s *Server) Handle(pattern string, h http.HandlerFunc) {
	s.extra = append(s.extra, route{pattern, h})
}

type route struct {
	pattern string
	h       http.HandlerFunc
}

// Serve binds 127.0.0.1:port ONLY and serves /health and /status until the process exits. Refuses a
// non-loopback bind: a daemon exposing its health on a public interface would bypass appears-down.
func (s *Server) Serve(port int) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) {
		h := s.rep.Health()
		if h.Name == "" {
			h.Name = s.name
		}
		writeJSON(w, h)
	})
	for _, r := range s.extra {
		mux.HandleFunc(r.pattern, r.h)
	}
	mux.HandleFunc("/status", func(w http.ResponseWriter, _ *http.Request) {
		h := s.rep.Health()
		if h.Name == "" {
			h.Name = s.name
		}
		writeJSON(w, Status{
			Name:      s.name,
			Health:    h,
			UptimeSec: int64(time.Since(s.started).Seconds()),
			Metrics:   s.rep.Metrics(),
		})
	})
	ln, err := net.Listen("tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return fmt.Errorf("bind loopback health port %d: %w", port, err)
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 5 * time.Second}
	return srv.Serve(ln)
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

// OKReporter is the trivial reporter a stub daemon uses: always healthy, no metrics. Real daemons
// implement their own Reporter reflecting actual state.
type OKReporter struct{ Service string }

func (r OKReporter) Health() Health           { return Health{Code: OK, Detail: "stub ok", Name: r.Service} }
func (r OKReporter) Metrics() json.RawMessage { return nil }

// Cached is a reporter whose detail line costs something (a count from the database): the
// line is made at most once in [every], and the health server, polled every few seconds by
// watchd and the phone, serves the last one. The code is always OK: the daemon is up; what it
// has done is the detail.
func Cached(service string, every time.Duration, detail func() string) Reporter {
	var mu sync.Mutex
	var at time.Time
	var last string
	return ReporterFunc(func() Health {
		mu.Lock()
		defer mu.Unlock()
		if last == "" || time.Since(at) >= every {
			last, at = detail(), time.Now()
		}
		return Health{Code: OK, Name: service, Detail: last}
	})
}
