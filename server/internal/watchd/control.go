package watchd

// The control socket: how ghost.secd drives watchd. A unix socket on the ENCRYPTED VOLUME
// (<mount>/run/watchd.sock), 0600, owned by the run-user. It lives on the volume because watchd only
// exists once the volume is mounted , there is no "before mount" caller. Filesystem permissions are
// the whole auth story: only a process that can read the mounted volume as the run-user (secd runs as
// root, so it can) can talk to it, and it is crypto-erased with everything else on lock.
//
// Protocol: one JSON request per connection, one JSON response, connection closes. Line-oriented,
// no framing games. Commands:
//   {"cmd":"start-cohort"}          -> start every registered daemon, begin supervising
//   {"cmd":"stop-cohort"}           -> tear the whole cohort down, confirm every process dead
//   {"cmd":"restart","name":"..."}  -> kill+respawn one daemon from its (updated) volume binary
//   {"cmd":"status"}                -> the supervision snapshot for /v1/status
//   {"cmd":"ping"}                  -> liveness (secd checks watchd is up before start-cohort)

import (
	"log/slog"
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
)

type request struct {
	Cmd  string `json:"cmd"`
	Name string `json:"name,omitempty"`
}

type response struct {
	OK       bool            `json:"ok"`
	Err      string          `json:"err,omitempty"`
	Services []ServiceStatus `json:"services,omitempty"`
}

// ControlServer listens on the unix socket and dispatches to the supervisor.
type ControlServer struct {
	sup      *Supervisor
	sockPath string
	ln       net.Listener
	jlog     *slog.Logger
	shutdown func()
}

// WithShutdown installs the process-exit hook the `shutdown` command triggers , the same teardown
// path SIGTERM takes (cohort down, confirmed dead, then exit). It exists for the ORPHAN case: watchd
// runs in its own process group so a secd restart does not kill it, which means a restarted secd can
// find a live watchd it has no process handle for. The socket is then the only stop channel , without
// this, lock cannot bring an adopted watchd down and the unmount wedges on its open volume files.
func (c *ControlServer) WithShutdown(fn func()) { c.shutdown = fn }

// NewControlServer prepares the server; call Serve to run it. sockPath is <mount>/run/watchd.sock.
func NewControlServer(sup *Supervisor, sockPath string, jlog *slog.Logger) *ControlServer {
	return &ControlServer{sup: sup, sockPath: sockPath, jlog: jlog}
}

// Serve binds the socket and handles connections until ctx is cancelled. A stale socket file from an
// unclean previous exit is removed first (safe: if an old watchd were still bound, secd would not
// have started us).
func (c *ControlServer) Serve(ctx context.Context) error {
	if err := os.MkdirAll(filepath.Dir(c.sockPath), 0o750); err != nil {
		return err
	}
	_ = os.Remove(c.sockPath) // clear a stale socket from an unclean exit
	ln, err := net.Listen("unix", c.sockPath)
	if err != nil {
		return err
	}
	if err := os.Chmod(c.sockPath, 0o600); err != nil {
		_ = ln.Close()
		return err
	}
	c.ln = ln

	go func() {
		<-ctx.Done()
		_ = ln.Close()
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if errors.Is(err, net.ErrClosed) {
				return nil // clean shutdown
			}
			c.jlog.Error("control accept error", "fn", "Serve", "err", err)
			continue
		}
		go c.handle(conn)
	}
}

func (c *ControlServer) handle(conn net.Conn) {
	defer conn.Close()
	var req request
	if err := json.NewDecoder(conn).Decode(&req); err != nil {
		_ = json.NewEncoder(conn).Encode(response{OK: false, Err: "bad request: " + err.Error()})
		return
	}
	resp := c.dispatch(req)
	_ = json.NewEncoder(conn).Encode(resp)
}

func (c *ControlServer) dispatch(req request) response {
	switch req.Cmd {
	case "ping":
		return response{OK: true}
	case "start-cohort":
		// StartAll returns a critical-start error, but that must NOT fail the call: the box stays
		// mounted and serving-degraded, and secd surfaces the failure via status. So report OK and
		// let status carry the detail , matching the unlock-never-aborts rule.
		if err := c.sup.StartAll(context.Background()); err != nil {
			c.jlog.Warn("start-cohort reported trouble (box stays up, degraded)", "fn", "dispatch", "err", err)
		}
		return response{OK: true, Services: c.sup.Status()}
	case "stop-cohort":
		if err := c.sup.TeardownAll(); err != nil {
			return response{OK: false, Err: err.Error()}
		}
		return response{OK: true}
	case "shutdown":
		// Full stop: cohort down, then watchd itself exits. The hook runs in a goroutine and its
		// teardown takes seconds, so the response encode all but always wins the race , but the
		// caller must NOT trust this round trip: it polls ping-until-dead to confirm.
		if c.shutdown == nil {
			return response{OK: false, Err: "shutdown not wired"}
		}
		go c.shutdown()
		return response{OK: true}
	case "restart":
		if req.Name == "" {
			return response{OK: false, Err: "restart requires a service name"}
		}
		if err := c.sup.RestartOne(req.Name); err != nil {
			return response{OK: false, Err: err.Error()}
		}
		return response{OK: true, Services: c.sup.Status()}
	case "status":
		return response{OK: true, Services: c.sup.Status()}
	default:
		return response{OK: false, Err: "unknown command: " + req.Cmd}
	}
}

// Cleanup removes the socket file on shutdown.
func (c *ControlServer) Cleanup() {
	if c.ln != nil {
		_ = c.ln.Close()
	}
	_ = os.Remove(c.sockPath)
}
