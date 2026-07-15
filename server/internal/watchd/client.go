package watchd

// Client is how ghost.secd talks to watchd over the control socket. It lives in the watchd package so
// the request/response types are defined once and cannot drift between the two sides. secd holds a
// Client pointed at <mount>/run/watchd.sock and calls it after mounting (StartCohort), on lock
// (StopCohort), for the status screen (Status), and on a single-daemon deploy (Restart).

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"time"
)

// Client dials the watchd control socket per call (connections are cheap and short-lived; no pool).
type Client struct {
	sockPath string
	timeout  time.Duration
}

// NewClient points a client at the socket. Timeout bounds each call , start-cohort can take a moment
// (it spawns processes), so this is generous.
func NewClient(sockPath string) *Client {
	return &Client{sockPath: sockPath, timeout: 30 * time.Second}
}

func (c *Client) call(req request) (response, error) {
	d := net.Dialer{Timeout: 3 * time.Second}
	ctx, cancel := context.WithTimeout(context.Background(), c.timeout)
	defer cancel()
	conn, err := d.DialContext(ctx, "unix", c.sockPath)
	if err != nil {
		return response{}, fmt.Errorf("dial watchd: %w", err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(c.timeout))
	if err := json.NewEncoder(conn).Encode(req); err != nil {
		return response{}, err
	}
	var resp response
	if err := json.NewDecoder(conn).Decode(&resp); err != nil {
		return response{}, err
	}
	if !resp.OK {
		return resp, fmt.Errorf("watchd: %s", resp.Err)
	}
	return resp, nil
}

// Ping checks watchd is up (secd calls this before StartCohort, to confirm the daemon it just
// launched is listening).
func (c *Client) Ping() error {
	_, err := c.call(request{Cmd: "ping"})
	return err
}

// StartCohort tells watchd to start and supervise every daemon. Returns the initial status.
func (c *Client) StartCohort() ([]ServiceStatus, error) {
	resp, err := c.call(request{Cmd: "start-cohort"})
	return resp.Services, err
}

// StopCohort tells watchd to tear the whole cohort down and confirm every process dead. secd calls
// this BEFORE stopping the DBs and unmounting , the anti-wedge order.
func (c *Client) StopCohort() error {
	_, err := c.call(request{Cmd: "stop-cohort"})
	return err
}

// Shutdown tells watchd to tear the cohort down and EXIT. The stop channel for an ADOPTED watchd ,
// one that outlived a secd restart (own process group), leaving the new secd with no process handle
// to signal. The caller confirms death by polling Ping until it fails; this call alone is not proof.
func (c *Client) Shutdown() error {
	_, err := c.call(request{Cmd: "shutdown"})
	return err
}

// Restart asks watchd to kill+respawn one daemon from its (updated) volume binary. The deploy path.
func (c *Client) Restart(name string) ([]ServiceStatus, error) {
	resp, err := c.call(request{Cmd: "restart", Name: name})
	return resp.Services, err
}

// Status fetches the supervision snapshot for /v1/status.
func (c *Client) Status() ([]ServiceStatus, error) {
	resp, err := c.call(request{Cmd: "status"})
	return resp.Services, err
}
