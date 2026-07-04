package secd

// The single unlock backend. One binary compiles both seal tiers (see internal/hw), and the tier is
// chosen at RUNTIME from the mode recorded in seal.env , not a build tag. A user can add or clear a
// TPM, or run migrate-to-tpm, without recompiling. The mounter/DataStore/Accounts/wiper wiring is
// identical for both tiers; the ONLY per-tier differences are how a slot's key is unsealed (the
// Sealer chosen by hw.SelectSealer) and what crypto-erase destroys (the sealer's Destroy).
//
// Not validated in CI (no TPM, no root, no encrypted volumes in the build env). Built against the
// documented go-tpm + cryptsetup interfaces and exercised on the box.

import (
	"context"
	"log"
	"path/filepath"
	"strconv"
	"syscall"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/auth"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/profile"
	"github.com/LocalGhostDao/localghost/server/internal/rotlog"
	"github.com/LocalGhostDao/localghost/server/internal/watchd"
	"github.com/LocalGhostDao/localghost/server/internal/wipe"
)

const tpmDevice = "/dev/tpmrm0"

type backend struct {
	accounts *profile.Accounts
	mounter  *hw.DMCryptMounter
	store    *hw.DataStore
	sealerAt func(slot int) (hw.Sealer, error) // resolves the slot's Sealer per the provisioned tier
	stateDir string
	runUser  string // if set (--user <name>), watchd runs the cohort as this user

	// secd no longer supervises the ghost.*d daemons , ghost.watchd does, from ON the volume. secd
	// owns exactly ONE child: watchd. It starts watchd after the DBs are up, then drives it over the
	// control socket (start-cohort / stop-cohort / restart / status). watchProc is the watchd process
	// secd spawned; watch is the socket client. Both nil until StartCache; both cleared on Lock.
	watchProc *os.Process
	watch     *watchd.Client
}

func newDefaultBackend(cfg Config) UnlockBackend {
	sealStore := hw.NewEnvSealStore(filepath.Join(cfg.StateDir, "seal.env"))

	// mode is read fresh for each sealer construction, so a migrate (which rewrites seal.env) is
	// picked up without restarting the daemon. On an unprovisioned box mode is "" and SelectSealer
	// errors , resolution then rejects every PIN, which is the correct pre-provision behaviour.
	sealerAt := func(slot int) (hw.Sealer, error) {
		mode, _ := sealStore.Mode()
		return hw.SelectSealer(mode, tpmDevice, sealStore, slot)
	}

	// keyFor bridges the mounter to whichever tier is active. A tier/probe error (e.g. tpm mode but
	// no usable TPM) surfaces here as an unseal failure , the unlock fails cleanly and the reason is
	// logged, rather than silently downgrading.
	keyFor := func(slot int, pin string) ([]byte, error) {
		s, err := sealerAt(slot)
		if err != nil {
			return nil, err
		}
		return s.Unseal(pin)
	}
	mounter := hw.NewDMCryptMounter(cfg.StateDir, cfg.Disk, keyFor)
	store := hw.NewDataStore(func(slot int) string { return mounter.MountPath(slot) })

	reg, err := loadRegistry(cfg.StateDir)
	if err != nil {
		reg, _ = profile.NewRegistry(nil)
	}
	gate := auth.NewGate(auth.DefaultPolicy(), auth.NewMemoryStore())
	wiper := wipe.NewWiper(wipe.NewKeyVault(), sealerEraser{sealerAt}, func(slot int) error {
		return mounter.Unmount(slot)
	})
	accounts := profile.NewAccounts(reg, gate, wiper)

	return &backend{
		accounts: accounts, mounter: mounter, store: store,
		sealerAt: sealerAt, stateDir: cfg.StateDir, runUser: cfg.RunUser,
	}
}

func (b *backend) Resolve(pin string) (int, bool, error) {
	d := b.accounts.Unlock("box", pin)
	switch d.Outcome {
	case profile.Open:
		return d.OpenSlot, d.Wiped, nil
	default:
		return profile.NoSlot, d.Wiped, nil
	}
}

// AuthorizesLock reports whether pin is a valid main PIN, with no side effects , used by `off` to
// authorize a lock without ever being able to wipe or disturb an armed wipe.
func (b *backend) AuthorizesLock(pin string) bool { return b.accounts.AuthorizesLock(pin) }

func (b *backend) Unseal(slot int, pin string) ([]byte, error) {
	s, err := b.sealerAt(slot)
	if err != nil {
		return nil, err
	}
	return s.Unseal(pin)
}

func (b *backend) Mount(slot int, key []byte) error {
	if _, err := b.mounter.MapWithKey(slot, key); err != nil {
		return err
	}
	return b.mounter.ResizeToFill(slot)
}

func (b *backend) StartDB(slot int) error {
	_, err := b.store.Start(slot)
	return err
}

// StartCache is the last cold-unlock stage before DAEMONS. Postgres + Redis are already up (StartDB).
// Here secd starts its ONE child , ghost.watchd , from the volume, waits for watchd's control socket,
// then tells watchd to start-cohort (spawn + supervise every ghost.*d daemon). On the warm path this
// is Skipped by runUnlock, so a running watchd + cohort from the original cold unlock keep going.
//
// A watchd or cohort failure must NOT abort the unlock: the box stays mounted and serves, degraded,
// with the detail on /v1/status. So this logs and returns nil on any watchd trouble , the unlock
// completes regardless, matching the never-abort rule.
func (b *backend) StartCache(slot int) error {
	mount := b.mounter.MountPath(slot)
	sockPath := filepath.Join(mount, "run", "watchd.sock")
	b.watch = watchd.NewClient(sockPath)

	// Spawn watchd from the volume's bin dir. It runs as the run-user (or inherits secd's root if
	// unset , but provisioning sets it). watchd opens its own log + socket.
	proc, err := b.spawnWatchd(mount)
	if err != nil {
		b.log("unlock: could not start ghost.watchd (box stays mounted, daemons unavailable)", err)
		return nil
	}
	b.watchProc = proc

	// Wait briefly for watchd's socket to come up, then start the cohort. A timeout here is not fatal.
	if err := b.waitWatchdReady(2 * time.Second); err != nil {
		b.log("unlock: ghost.watchd did not become ready (daemons unavailable)", err)
		return nil
	}
	if _, err := b.watch.StartCohort(); err != nil {
		b.log("unlock: ghost.watchd start-cohort reported trouble (box stays mounted, degraded)", err)
	}
	return nil
}

// spawnWatchd execs ghost.watchd from the volume bin dir, pointed at the mount, DROPPED to the run
// user. secd is root (it mounts); watchd and everything downstream of it , the daemons, the logs, the
// control socket , run as the service user, so the whole on-volume tree has one owner. watchd runs in
// its own process group so a secd restart does not kill it (secd stops it explicitly on lock). Because
// watchd already runs as the user, it does NOT need to drop the daemons itself , they inherit its uid.
func (b *backend) spawnWatchd(mount string) (*os.Process, error) {
	bin := filepath.Join(mount, "bin", "ghost.watchd")
	cmd := exec.Command(bin, "--mount", mount)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if cred := userCredential(b.runUser); cred != nil {
		cmd.SysProcAttr.Credential = cred
	}
	if err := cmd.Start(); err != nil {
		return nil, err
	}
	return cmd.Process, nil
}

// userCredential resolves a username to a syscall.Credential so secd (root) can spawn watchd as the
// service user. Empty user, or a lookup failure, returns nil (watchd inherits secd's uid) , the
// default "ghost" user is created at provision, so a well-provisioned box always resolves.
func userCredential(name string) *syscall.Credential {
	if name == "" {
		return nil
	}
	u, err := user.Lookup(name)
	if err != nil {
		return nil
	}
	uid, e1 := strconv.Atoi(u.Uid)
	gid, e2 := strconv.Atoi(u.Gid)
	if e1 != nil || e2 != nil {
		return nil
	}
	return &syscall.Credential{Uid: uint32(uid), Gid: uint32(gid)}
}

// waitWatchdReady polls watchd's socket until Ping succeeds or the deadline passes.
func (b *backend) waitWatchdReady(within time.Duration) error {
	deadline := time.Now().Add(within)
	for time.Now().Before(deadline) {
		if err := b.watch.Ping(); err == nil {
			return nil
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("watchd socket not ready within %s", within)
}

// secdLevel is ghost.secd's live log level, so `ghost-cli ghost.secd log-level level=debug` can change
// it at runtime without a restart , the same knob the cohort daemons have.
var secdLevel = func() *slog.LevelVar {
	lv := new(slog.LevelVar)
	lv.Set(rotlog.LevelFromEnv())
	return lv
}()

// SecdLevel exposes the live level so main can wire it to the control socket.
func SecdLevel() *slog.LevelVar { return secdLevel }

// SecdLog exposes secd's structured logger for the control socket wiring in main.
func SecdLog() *slog.Logger { return secdLog }

// secdLog is ghost.secd's structured logger. secd is the systemd unit on the unencrypted disk, so it
// logs to STDERR (journald captures it) rather than a rotlog file , but through slog at the same
// GHOST_LOG_LEVEL and the same key=value shape as the daemons, so `grep 'level=ERROR'` / `fn=...`
// works uniformly across the whole box. Built once at package init.
var secdLog = func() *slog.Logger {
	h := slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{
		Level: secdLevel,
		ReplaceAttr: func(_ []string, a slog.Attr) slog.Attr {
			if a.Key == slog.TimeKey {
				a.Value = slog.StringValue(time.Now().UTC().Format("15:04:05.000000000"))
			}
			return a
		},
	})
	return slog.New(h).With("svc", "ghost.secd")
}()

// log notes a degraded state. These are all box-stays-up warnings (watchd trouble, etc.), so they log
// at Warn. The message is the human sentence; callers keep passing the error as the last arg and it
// lands in an err= field. Kept printf-free at the call site by threading the error through directly.
func (b *backend) log(msg string, a ...any) {
	// a is historically [err]; attach it as a structured field when present.
	if len(a) == 1 {
		secdLog.Warn(msg, "err", a[0])
		return
	}
	secdLog.Warn(msg, a...)
}

// stopWatchd signals watchd (SIGTERM) and reaps it. watchd's own SIGTERM handler tears down the
// cohort and confirms every daemon dead before it exits, so when this returns, the whole cohort AND
// watchd are gone , the volume is safe to unmount. Falls back to KILL after a grace.
func (b *backend) stopWatchd() {
	if b.watchProc == nil {
		return
	}
	_ = b.watchProc.Signal(syscall.SIGTERM)
	done := make(chan struct{})
	go func() { _, _ = b.watchProc.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(15 * time.Second): // generous: watchd is tearing down the whole cohort
		_ = b.watchProc.Kill()
		<-done
	}
	b.watchProc = nil
	b.watch = nil
}

// SupervisorStatus fetches the cohort snapshot from watchd over the control socket for /v1/status.
// nil when cold (no watchd) or if watchd is unreachable , the status handler treats nil as "no
// daemon info", which the app renders as unavailable rather than as all-healthy.
func (b *backend) SupervisorStatus() []ServiceStatus {
	if b.watch == nil {
		return nil
	}
	ws, err := b.watch.Status()
	if err != nil {
		b.log("status: could not reach ghost.watchd", err)
		return nil
	}
	out := make([]ServiceStatus, 0, len(ws))
	for _, w := range ws {
		out = append(out, ServiceStatus{
			Name: w.Name, Critical: w.Critical, State: w.State,
			Restarts: w.Restarts, LastErr: w.LastErr, Code: w.Code,
		})
	}
	return out
}

// RestartDaemon asks watchd to restart one daemon from its (updated) volume binary , the deploy
// primitive exposed so an operator tool can trigger a single-daemon redeploy without pkill. Returns
// an error if the box is locked (no watchd) or watchd rejects the name.
func (b *backend) RestartDaemon(name string) error {
	if b.watch == nil {
		return fmt.Errorf("box is locked; no watchd to restart %q", name)
	}
	_, err := b.watch.Restart(name)
	return err
}

func (b *backend) Warm(slot int) bool { return b.mounter.IsMounted(slot) }

func (b *backend) Lock(slot int, emit func(profile.Progress)) error {
	step := func(st profile.Stage, do func() error) error {
		emit(profile.Progress{Stage: st, State: profile.Running})
		err := do()
		if err != nil {
			emit(profile.Progress{Stage: st, State: profile.Errored})
		} else {
			emit(profile.Progress{Stage: st, State: profile.Complete})
		}
		return err
	}
	// Stop the ghost.*d cohort FIRST and confirm every one is dead before we touch the volume. This
	// is the anti-wedge ordering: a daemon holding the mount open would block Unmount. We ask watchd
	// to stop-cohort (it tears down and confirms every daemon dead), THEN stop watchd itself. Both
	// must be gone before the DBs stop and the volume unmounts.
	_ = step(profile.StageStopServices, func() error {
		if b.watch != nil {
			if err := b.watch.StopCohort(); err != nil {
				b.log("lock: watchd stop-cohort error (continuing teardown)", err)
			}
		}
		b.stopWatchd()
		return nil
	})
	_ = step(profile.StageStopCache, func() error { return b.store.StopCache(slot) })
	_ = step(profile.StageStopDB, func() error { return b.store.StopDB(slot) })
	if err := step(profile.StageUnmount, func() error { return b.mounter.Unmount(slot) }); err != nil {
		return err
	}
	emit(profile.Progress{Stage: profile.StageLocked, State: profile.Complete})
	return nil
}

// sealerEraser adapts whichever tier's Sealer to wipe.HardwareEraser: crypto-erase destroys a slot's
// sealed key (TPM object evicted, or software wrapping deleted from seal.env) so the volume's key can
// no longer be recovered.
type sealerEraser struct {
	sealerAt func(slot int) (hw.Sealer, error)
}

func (e sealerEraser) EraseAccount(slot int) error {
	s, err := e.sealerAt(slot)
	if err != nil {
		return err
	}
	return s.Destroy()
}
func (e sealerEraser) EraseAll() error {
	for _, slot := range []int{profile.MainSlot} {
		s, err := e.sealerAt(slot)
		if err != nil {
			return err
		}
		if err := s.Destroy(); err != nil {
			return err
		}
	}
	return nil
}
