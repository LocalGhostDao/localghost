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
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"os/user"
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
	// watchDone closes when the reaper goroutine (spawnWatchd) observes watchd exit. The lock path
	// waits on THIS, never on Wait() directly , two concurrent Wait()s on one process race, and the
	// loser gets ECHILD and can wrongly conclude the process died while it is still running.
	watchDone chan struct{}
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
	store := hw.NewDataStore(func(slot int) string { return mounter.MountPath(slot) }, cfg.RunUser)

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

	// Heal volume permissions for the run user BEFORE spawning watchd. Provisioning wrote
	// services.conf as root 0600, so watchd (dropped to the run user) died in 2ms with
	// "read services.conf: permission denied" , the exit-status-1 zombie. watchd also needs logs/
	// (its rotlog) and run/ (its control socket) to exist and be writable. Doing this here, at the
	// point of use, heals EXISTING volumes too, not only fresh provisions.
	if cred := userCredential(b.runUser); cred != nil {
		uid, gid := int(cred.Uid), int(cred.Gid)
		if err := os.Chown(filepath.Join(mount, "services.conf"), uid, gid); err != nil {
			secdLog.Warn("chown services.conf for run user", "fn", "StartCache", "err", err)
		}
		for _, d := range []string{"logs", "run", "bin", "conf"} {
			dir := filepath.Join(mount, d)
			if err := os.MkdirAll(dir, 0o700); err != nil {
				secdLog.Warn("ensure volume dir", "fn", "StartCache", "dir", dir, "err", err)
				continue
			}
			if err := os.Chown(dir, uid, gid); err != nil {
				secdLog.Warn("chown volume dir for run user", "fn", "StartCache", "dir", dir, "err", err)
			}
		}
	}

	// Ingest provisioning-staged model weights onto the encrypted volume. tools/stage_models.sh
	// places ggufs at <state>/staging/ai-models (the volume does not exist pre-unlock, so staging on
	// the OS disk is the only provision-time option); this moves them into <mount>/ai-models BEFORE
	// watchd spawns the cohort, so oracled's eager llama-server start finds its weights on the very
	// first unlock. Runs only when staging is non-empty , a no-op forever after.
	b.ingestStagedModels(mount)
	b.ingestStagedBinaries(mount)

	// ADOPT before spawn. watchd runs in its own process group precisely so a secd restart does not
	// kill it , so finding one alive here is the EXPECTED post-deploy state, not an anomaly. If its
	// socket answers, adopt the client (no process handle exists; the lock path stops an adopted
	// watchd over the socket instead) and re-issue start-cohort, which is idempotent: daemons whose
	// processes are alive are left alone, dead ones are respawned. This also covers the half-open
	// case where a previous secd died between watchd-ready and start-cohort.
	if err := b.watch.Ping(); err == nil {
		secdLog.Info("unlock: adopted live ghost.watchd from a previous secd", "fn", "StartCache")
		if _, err := b.watch.StartCohort(); err != nil {
			b.log("unlock: ghost.watchd start-cohort reported trouble (box stays mounted, degraded)", err)
		}
		return nil
	}

	// Spawn watchd from the volume's bin dir. It runs as the run-user (or inherits secd's root if
	// unset , but provisioning sets it). watchd opens its own log + socket.
	proc, err := b.spawnWatchd(mount)
	if err != nil {
		b.log("unlock: could not start ghost.watchd (box stays mounted, daemons unavailable)", err)
		return nil
	}
	b.watchProc = proc

	// Wait briefly for watchd's socket to come up, then start the cohort. A timeout here is not fatal.
	// 15s, not 2s: on a COLD first unlock watchd is exec'd fresh from the volume, sets up its control
	// socket, and connects to the just-started Postgres/Redis , 2s was too tight and left the whole
	// cohort down while the box reported "unlocked". If it still misses, log enough to tell WHY (is the
	// process alive? did exec fail?) instead of a bare timeout.
	if err := b.waitWatchdReady(15 * time.Second); err != nil {
		alive := "unknown"
		if b.watchProc != nil {
			// Signal 0 probes liveness without affecting the process.
			if perr := b.watchProc.Signal(syscall.Signal(0)); perr == nil {
				alive = "process alive but socket never ready , watchd started then wedged (check its log on the volume)"
			} else {
				alive = "process NOT alive , watchd exec'd then died (missing binary, bad perms, or crash)"
			}
		} else {
			alive = "no watchd process handle , spawn itself failed"
		}
		secdLog.Error("unlock: ghost.watchd did not become ready", "fn", "unlock", "within", "15s", "diag", alive, "err", err)
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
// ingestStagedModels moves gguf files staged by tools/stage_models.sh onto the encrypted volume.
// Copy + remove (staging is on a different filesystem), chown to the run user, per-file logging. The
// staged copies were PLAINTEXT on the OS disk , that exposure is inherent to staging before the
// volume exists, which is why this runs at the first opportunity and removes them.
func (b *backend) ingestStagedModels(mount string) {
	staging := filepath.Join(filepath.Dir(filepath.Dir(mount)), "staging", "ai-models")
	entries, err := os.ReadDir(staging)
	if err != nil || len(entries) == 0 {
		return // nothing staged , the usual case
	}
	dst := filepath.Join(mount, "ai-models")
	if err := os.MkdirAll(dst, 0o750); err != nil {
		secdLog.Error("model ingest: cannot create ai-models on volume", "fn", "ingestStagedModels", "err", err)
		return
	}
	cred := userCredential(b.runUser)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		srcPath := filepath.Join(staging, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		t0 := time.Now()
		if err := copyFileSync(srcPath, dstPath); err != nil {
			secdLog.Error("model ingest failed , staged copy kept for retry next unlock",
				"fn", "ingestStagedModels", "file", e.Name(), "err", err)
			continue
		}
		if cred != nil {
			if err := os.Chown(dstPath, int(cred.Uid), int(cred.Gid)); err != nil {
				secdLog.Warn("model ingest: chown", "fn", "ingestStagedModels", "file", e.Name(), "err", err)
			}
		}
		if err := os.Remove(srcPath); err != nil {
			secdLog.Warn("model ingest: staged copy not removed", "fn", "ingestStagedModels", "file", e.Name(), "err", err)
		}
		secdLog.Info("model ingested onto encrypted volume", "fn", "ingestStagedModels",
			"file", e.Name(), "took", time.Since(t0).Round(time.Millisecond).String())
	}
	if cred != nil {
		_ = os.Chown(dst, int(cred.Uid), int(cred.Gid))
	}
}

// ingestStagedBinaries moves redeploy-staged cohort binaries (ghost.*d, llama-server) from
// <state>/staging/bin onto <mount>/bin. Runs BEFORE watchd spawns the cohort, so nothing being
// replaced is running , no ETXTBSY, no partial cohorts. This closes the gap where a redeploy
// updated secd but the volume's daemons stayed at provision-day builds forever: fixes compiled,
// deployed nowhere, and silently never ran.
func (b *backend) ingestStagedBinaries(mount string) {
	staging := filepath.Join(filepath.Dir(filepath.Dir(mount)), "staging", "bin")
	entries, err := os.ReadDir(staging)
	if err != nil || len(entries) == 0 {
		return // nothing staged , no redeploy since last unlock
	}
	dst := filepath.Join(mount, "bin")
	cred := userCredential(b.runUser)
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		srcPath := filepath.Join(staging, e.Name())
		dstPath := filepath.Join(dst, e.Name())
		// Copy to a temp name, then rename over the destination. Writing the destination directly
		// (O_TRUNC) is ETXTBSY against a binary that is RUNNING , which is now the normal case, since
		// ingest also runs on warm unlocks where the cohort was adopted alive. The rename swaps the
		// inode: running processes keep executing their old image, the next (re)start execs the new
		// one, and there is never a half-written binary at the real path.
		tmpPath := dstPath + ".ingest"
		if err := copyFileSync(srcPath, tmpPath); err != nil {
			secdLog.Error("binary ingest failed , volume keeps its previous build of this daemon",
				"fn", "ingestStagedBinaries", "file", e.Name(), "err", err)
			_ = os.Remove(tmpPath)
			continue
		}
		if err := os.Chmod(tmpPath, 0o755); err != nil {
			secdLog.Warn("binary ingest: chmod", "fn", "ingestStagedBinaries", "file", e.Name(), "err", err)
		}
		if cred != nil {
			_ = os.Chown(tmpPath, int(cred.Uid), int(cred.Gid))
		}
		if err := os.Rename(tmpPath, dstPath); err != nil {
			secdLog.Error("binary ingest: rename into place failed , volume keeps its previous build",
				"fn", "ingestStagedBinaries", "file", e.Name(), "err", err)
			_ = os.Remove(tmpPath)
			continue
		}
		_ = os.Remove(srcPath)
		secdLog.Info("cohort binary refreshed on volume", "fn", "ingestStagedBinaries", "file", e.Name())
	}
}

// copyFileSync copies src to dst with an fsync , model weights are multi-GB and the whole point is
// that they survive; a torn copy that "succeeded" would fail oracled's size pre-flight confusingly.
func copyFileSync(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	if err := out.Sync(); err != nil {
		_ = out.Close()
		_ = os.Remove(dst)
		return err
	}
	return out.Close()
}

func (b *backend) spawnWatchd(mount string) (*os.Process, error) {
	bin := filepath.Join(mount, "bin", "ghost.watchd")
	// Fail loudly if the binary is not there/executable , "exec then die silently" wastes a debugging
	// round; "the volume has no ghost.watchd at <path>" is a one-glance diagnosis.
	if fi, err := os.Stat(bin); err != nil {
		return nil, fmt.Errorf("ghost.watchd binary not on the volume at %s: %w (was the volume bin/ staged?)", bin, err)
	} else if fi.Mode().Perm()&0o111 == 0 {
		return nil, fmt.Errorf("ghost.watchd at %s is not executable (mode %v)", bin, fi.Mode())
	}
	cmd := exec.Command(bin, "--mount", mount)
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if cred := userCredential(b.runUser); cred != nil {
		cmd.SysProcAttr.Credential = cred
	}
	// The child's own output goes to secd's stderr -> journald. Before this, watchd's dying words
	// (missing conf, unwritable run dir, panic) went to /dev/null and it died invisibly.
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("spawn ghost.watchd: %w", err)
	}
	// Reap. Without a Wait the child becomes a zombie on death (observed: [ghost.watchd] <defunct>)
	// and its exit status , the WHY of the death , is thrown away. Log it instead.
	proc := cmd.Process
	done := make(chan struct{})
	b.watchDone = done
	go func() {
		defer close(done)
		state, werr := proc.Wait()
		if werr != nil {
			secdLog.Warn("ghost.watchd wait failed", "fn", "spawnWatchd", "err", werr)
			return
		}
		if state.Success() {
			secdLog.Info("ghost.watchd exited cleanly", "fn", "spawnWatchd")
		} else {
			secdLog.Error("ghost.watchd DIED", "fn", "spawnWatchd", "state", state.String())
		}
	}()
	return proc, nil
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
		// No process handle , either the box is cold (nothing to do) or this watchd was ADOPTED
		// after a secd restart, in which case the control socket is the only stop channel. Send
		// shutdown, then poll ping-until-dead: the shutdown response is written before the teardown
		// finishes, so the round trip alone proves nothing. Bounded at 15s, matching the signal
		// path's teardown allowance; a watchd still answering after that is logged loudly, because
		// its open files on the volume are exactly what wedges the coming unmount.
		if b.watch != nil {
			if err := b.watch.Shutdown(); err != nil {
				secdLog.Warn("adopted watchd shutdown command failed", "fn", "stopWatchd", "err", err)
			}
			deadline := time.Now().Add(15 * time.Second)
			dead := false
			for time.Now().Before(deadline) {
				if b.watch.Ping() != nil {
					dead = true
					break
				}
				time.Sleep(100 * time.Millisecond)
			}
			if !dead {
				secdLog.Error("adopted watchd still alive after shutdown , unmount may wedge on its open volume files", "fn", "stopWatchd")
			}
			b.watch = nil
		}
		return
	}
	if err := b.watchProc.Signal(syscall.SIGTERM); err != nil {
		secdLog.Warn("signal watchd for stop", "fn", "stopWatchd", "err", err)
	}
	// Wait on the reaper's channel (spawnWatchd owns the one-and-only Wait). A second concurrent
	// Wait here would race the reaper and could conclude "exited" while watchd still runs.
	done := b.watchDone
	if done == nil { // defensive: no reaper (should not happen) , fall back to a bounded sleep
		done = make(chan struct{})
		go func() { time.Sleep(15 * time.Second); close(done) }()
	}
	select {
	case <-done:
	case <-time.After(15 * time.Second): // generous: watchd is tearing down the whole cohort
		if err := b.watchProc.Kill(); err != nil {
			secdLog.Warn("kill watchd after stop timeout", "fn", "stopWatchd", "err", err)
		}
		<-done
	}
	b.watchProc = nil
	b.watchDone = nil
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
