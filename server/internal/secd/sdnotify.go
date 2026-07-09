package secd

// Minimal sd_notify , stdlib only, no systemd library. The ghost.secd unit is Type=notify, which
// makes `systemctl start` block until the daemon reports READY over $NOTIFY_SOCKET. That is the
// point: for the box's single root component, "active" should mean LISTENING, not merely forked.
// Without this call, start hangs to systemd's timeout and the unit is killed , the failure mode is
// a box that provisions and then mysteriously never comes up.

import (
	"net"
	"os"
)

// NotifyReady tells systemd the daemon is serving. A no-op outside systemd (empty NOTIFY_SOCKET) and
// on any error , readiness reporting must never take the daemon down.
func NotifyReady() {
	addr := os.Getenv("NOTIFY_SOCKET")
	if addr == "" {
		return
	}
	if addr[0] == '@' { // abstract socket namespace
		addr = "\x00" + addr[1:]
	}
	conn, err := net.Dial("unixgram", addr)
	if err != nil {
		return
	}
	defer conn.Close()
	_, _ = conn.Write([]byte("READY=1"))
}
