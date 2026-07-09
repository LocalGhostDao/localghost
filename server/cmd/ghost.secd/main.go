// ghost.secd is the box daemon: the single front door the phone connects to. It terminates the
// authenticated channel, runs unlock, serves info + status + the model catalogue, and , crucially ,
// is the ONLY LocalGhost process on the unencrypted system disk. Everything else (the ghost.*d
// daemons, their data, logs, and binaries) lives on the encrypted volume and is owned by ghost.watchd.
//
// secd owns exactly one child: ghost.watchd, which it starts after mounting + bringing up pg/redis.
// watchd supervises the rest. So secd is freely restartable: on SIGTERM it does a clean lock (stop
// watchd -> cohort torn down + confirmed dead -> stop DBs -> unmount -> close LUKS), and on restart it
// adopts any existing mount. A deploy of secd is just `systemctl restart ghost.secd` followed by a
// re-unlock from the app , nothing is ever orphaned.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/secd"
	"github.com/LocalGhostDao/localghost/server/internal/svcconf"
)

func main() {
	addr := flag.String("addr", "127.0.0.1:8443", "listen address (behind nginx, which terminates public TLS)")
	stateDir := flag.String("state", "/var/lib/ghost", "unencrypted state dir (certs, models)")
	disk := flag.String("disk", os.Getenv("GHOST_DISK"), "the raw LUKS data disk to mount on unlock (e.g. /dev/nvme1n1); defaults to $GHOST_DISK")
	runUser := flag.String("user", os.Getenv("GHOST_RUN_USER"), "run the ghost.*d cohort as this user (default: the ghost user); defaults to $GHOST_RUN_USER")
	flag.Parse()

	srv, err := secd.New(secd.Config{StateDir: *stateDir, Disk: *disk, RunUser: *runUser})
	if err != nil {
		fmt.Fprintln(os.Stderr, "ghost.secd: init failed:", err)
		os.Exit(1)
	}

	httpSrv := &http.Server{Addr: *addr, Handler: srv.Handler()}

	// Control socket for ghost-cli, on the UNENCRYPTED state dir (not the volume): secd is the one
	// process that exists when the box is locked, so its CLI must work when locked too , `ghost-cli
	// ghost.secd status` reports locked/appears-down without needing an unlock. runDir defaults under
	// the state dir; GHOST_RUN_DIR is not used here (that points at the volume, which may be absent).
	ctlCtx, ctlCancel := context.WithCancel(context.Background())
	defer ctlCancel()
	secdRunDir := filepath.Join(*stateDir, "run")
	cli := ctlsock.NewServer("ghost.secd", secdRunDir, secd.SecdLog())
	svcconf.BindBase(cli, "ghost.secd", secd.SecdLevel(), func() (svcconf.Base, map[string]string, error) {
		// secd is not conf-file driven (its config is flags/systemd); reload just re-applies the log
		// level from GHOST_LOG_LEVEL and reports the rest as needs-restart.
		b := svcconf.DefaultBase()
		b.LogLevel = secd.SecdLevel().Level().String()
		return b, map[string]string{"addr": "needs-restart", "disk": "needs-restart"}, nil
	})
	cli.Handle("status", func(json.RawMessage) (ctlsock.Response, error) {
		data, _ := json.Marshal(srv.Status())
		return ctlsock.Response{OK: true, Data: data}, nil
	})
	// off: lock the box NOW, authorized by the main PIN (not an app session). The border-crossing
	// "make appears-down true" command. Option A , a lock, never a wipe. The reply is deliberately
	// opaque: right PIN, wrong PIN, wipe PIN, already-locked all return the same "ok", because the only
	// thing an observer should be able to learn is that the box is down.
	cli.Handle("off", func(args json.RawMessage) (ctlsock.Response, error) {
		var a struct {
			PIN string `json:"pin"`
		}
		if len(args) > 0 {
			_ = json.Unmarshal(args, &a)
		}
		srv.Off(a.PIN)
		return ctlsock.Response{OK: true, Text: "ok"}, nil
	})
	defer cli.Cleanup()
	go func() {
		if err := cli.Serve(ctlCtx); err != nil {
			log.Printf("ghost.secd: cli control server exited: %v", err)
		}
	}()

	// SIGTERM/SIGINT: clean lock, then stop serving. systemd sends SIGTERM on stop/restart; this is
	// the one shutdown path (see Server.Shutdown). We lock FIRST (tear the stack down) so the volume
	// is never left mounted behind a dead front door, then shut the HTTP server.
	sigc := make(chan os.Signal, 1)
	signal.Notify(sigc, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		s := <-sigc
		log.Printf("ghost.secd: signal %v, locking the box before shutdown", s)
		if err := srv.Shutdown(); err != nil {
			log.Printf("ghost.secd: clean lock on shutdown reported: %v", err)
		}
		ctlCancel()
		ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpSrv.Shutdown(ctx)
	}()

	log.Printf("ghost.secd listening on %s (state %s, run-user %q)", *addr, *stateDir, *runUser)
	// Listen FIRST, notify systemd READY, then serve. Type=notify blocks systemctl start until this
	// datagram arrives , sent exactly when the socket is accepting, which is what "started" means.
	ln, err := net.Listen("tcp", *addr)
	if err != nil {
		log.Fatal(err)
	}
	secd.NotifyReady()
	if err := httpSrv.Serve(ln); err != nil && err != http.ErrServerClosed {
		log.Fatal(err)
	}
	log.Printf("ghost.secd stopped")
}
