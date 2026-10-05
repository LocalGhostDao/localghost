package secd

// A NEW SERVER RELEASE, FROM THE PHONE, WITHOUT ROOT (internal/update says how it goes on and
// comes off). The box never fetches it: the phone checks the mirror once a day, and when the
// person taps DEPLOY it downloads the signed "server" set and hands the files here. secd checks
// them with the mirror's own verifier and the site key the box already holds (never one that came
// with the files), unpacks, keeps what runs now, puts the new build in place, locks, and restarts
// onto it. The release is then on trial until its first unlock has run ten minutes with the
// daemons up; a failed first unlock, a daemon that keeps dying, a secd that will not stay up, or
// the person asking, puts the earlier build back.
//
//   GET  /v1/update                  , what runs (version, name, commit, builtAt, go), the trial's
//                                      state, and the shelf: the releases the box keeps
//   POST /v1/update/file?name=...    , one file of the set: MANIFEST.txt (first; it starts a new
//                                      upload), MANIFEST.txt.asc, <build>/server/<file>
//   POST /v1/update/apply            , verify, put on, lock and restart; {"ok", "version"}
//   POST /v1/update/switch           , {"version"}: a release from the shelf back on (verified
//                                      again, unpacked again, tried again), lock and restart
//   POST /v1/update/rollback         , the earlier build back, lock and restart
//
// THE SHELF (internal/update/shelf.go). The mirror offers one release, the newest; the box keeps
// the signed set of every release it took, so an earlier one can go back on without the mirror.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/profile"
	"github.com/LocalGhostDao/localghost/server/internal/update"
	"github.com/LocalGhostDao/localghost/server/internal/watchd"
)

// Version is the build secd reports (Makefile: git describe; tools/release_build.sh: the tag).
var Version = "dev"

// ReleaseName is the release's name (tools/release.names: 0.0.1 is "wisp"), "" for a build from
// source.
var ReleaseName = ""

// Commit is the commit the build came from (Makefile: git rev-parse; tools/release_build.sh: the
// tag's), "" when the build did not say.
var Commit = ""

// BuiltAt is when the build was made, RFC 3339 UTC. For a release it is the commit's time
// (release_build.sh: the build is reproducible, and a clock reading would make it not), for a build
// from source the moment make ran.
var BuiltAt = ""

// updateMaxBytes bounds one uploaded file of the set (the bundle is the big one).
const updateMaxBytes = 512 << 20

// trialWatch is how long the first unlock onto a new release runs before it is confirmed.
var trialWatch = 10 * time.Minute

// restartSelf ends secd so systemd starts it again (Restart=on-failure) onto the files now in
// place. Tests replace it.
var restartSelf = func() { os.Exit(75) }

var updateFileRE = regexp.MustCompile(`^(MANIFEST\.txt|MANIFEST\.txt\.asc|[A-Za-z0-9._-]{1,64}/server/[A-Za-z0-9._+-]{1,100})$`)

type updateState struct {
	mu       sync.Mutex
	busy     bool
	watching bool
	paths    *update.Paths // nil: the box's
	// verify runs the mirror's verifier over the set at from into dir; tests replace it. shelf
	// says the set is one the box kept (an older manifest than the newest it has used).
	verify func(ctx context.Context, p update.Paths, from, dir string, shelf bool) error
}

func (s *Server) updPaths() update.Paths {
	if s.upd.paths != nil {
		return *s.upd.paths
	}
	return update.BoxPaths()
}

// mirrorVerify is the mirror's own check over a copy of the mirror on disk: the manifest's
// signature against the site key pinned in tools (installed beside the verifier, never taken from
// the upload), every file's SHA-256, and no manifest older than the newest the box has used. A set
// from the shelf is older by nature (the box took it when it was the newest, and verified it then),
// so for that run the verifier reads and writes its "newest build used" marker in the scratch
// directory: the real marker neither refuses the set nor moves back to it.
func mirrorVerify(ctx context.Context, p update.Paths, from, dir string, shelf bool) error {
	script := filepath.Join(p.Tools, "mirror_fetch.sh")
	key := filepath.Join(p.Tools, "mirror-key.asc")
	for _, f := range []string{script, key} {
		if _, err := os.Stat(f); err != nil {
			return fmt.Errorf("%s is not installed (redeploy.sh puts it there once)", f)
		}
	}
	cmd := exec.CommandContext(ctx, "/bin/sh", script, "server", dir)
	cmd.Env = append(os.Environ(), "GHOST_MIRROR=file://"+from, "GHOST_MIRROR_KEY="+key)
	if shelf {
		cmd.Env = append(cmd.Env, "GHOST_MIRROR_STATE="+filepath.Join(p.State, "shelf-build"))
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("the release did not verify: %v: %s", err, lastLine(string(out)))
	}
	return nil
}

func lastLine(s string) string {
	l := strings.Split(strings.TrimSpace(s), "\n")
	return strings.TrimSpace(l[len(l)-1])
}

// handleUpdate , GET /v1/update.
func (s *Server) handleUpdate(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	p := s.updPaths()
	writeJSON(w, map[string]any{"version": Version, "name": ReleaseName, "commit": Commit, "builtAt": BuiltAt, "go": runtime.Version(),
		"trial": update.LoadTrial(p), "shelf": update.Shelf(p)})
}

// handleUpdateFile , POST /v1/update/file?name=... , one file of the set, streamed to incoming/.
func (s *Server) handleUpdateFile(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	name := r.URL.Query().Get("name")
	if !updateFileRE.MatchString(name) || strings.Contains(name, "..") {
		http.Error(w, "not a file of the server set", http.StatusBadRequest)
		return
	}
	p := s.updPaths()
	incoming := filepath.Join(p.State, "incoming")
	if name == "MANIFEST.txt" {
		_ = os.RemoveAll(incoming) // a new upload
	}
	dst := filepath.Join(incoming, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(dst), 0o700); err != nil {
		s.appearsDown(w)
		return
	}
	f, err := os.OpenFile(dst+".part", os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o600)
	if err != nil {
		s.appearsDown(w)
		return
	}
	n, err := io.Copy(f, http.MaxBytesReader(w, r.Body, updateMaxBytes))
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = os.Rename(dst+".part", dst)
	}
	if err != nil {
		_ = os.Remove(dst + ".part")
		http.Error(w, "upload failed", http.StatusBadRequest)
		return
	}
	writeJSON(w, map[string]any{"ok": true, "bytes": n})
}

// handleUpdateApply , POST /v1/update/apply: the set the phone uploaded.
func (s *Server) handleUpdateApply(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	p := s.updPaths()
	s.putOn(w, r, filepath.Join(p.State, "incoming"), false)
}

// handleUpdateSwitch , POST /v1/update/switch {"version"}: a release from the shelf.
func (s *Server) handleUpdateSwitch(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	var in struct {
		Version string `json:"version"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&in); err != nil || !updateVersionRE.MatchString(in.Version) {
		http.Error(w, "which version?", http.StatusBadRequest)
		return
	}
	p := s.updPaths()
	set := update.SetDir(p, in.Version)
	if _, err := os.Stat(filepath.Join(set, "MANIFEST.txt.asc")); err != nil {
		writeJSON(w, map[string]any{"ok": false, "why": "the box keeps no signed set of " + in.Version})
		return
	}
	s.putOn(w, r, set, true)
}

var updateVersionRE = regexp.MustCompile(`^[A-Za-z0-9][A-Za-z0-9._+-]{0,63}$`)

// putOn verifies the set at from (the upload, or a set from the shelf), unpacks its bundle, puts the
// release on and restarts onto it; the set goes to (or stays on) the shelf. One at a time.
func (s *Server) putOn(w http.ResponseWriter, r *http.Request, from string, shelf bool) {
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	s.upd.mu.Lock()
	if s.upd.busy {
		s.upd.mu.Unlock()
		http.Error(w, "an update is already being put on", http.StatusConflict)
		return
	}
	s.upd.busy = true
	s.upd.mu.Unlock()
	defer func() { s.upd.mu.Lock(); s.upd.busy = false; s.upd.mu.Unlock() }()

	p := s.updPaths()
	verified := filepath.Join(p.State, "verified")
	_ = os.RemoveAll(verified)
	verify := s.upd.verify
	if verify == nil {
		verify = mirrorVerify
	}
	ctx, cancel := context.WithTimeout(r.Context(), 5*time.Minute)
	defer cancel()
	if err := verify(ctx, p, from, verified, shelf); err != nil {
		secdLog.Warn("update refused", "fn", "putOn", "shelf", shelf, "err", err)
		writeJSON(w, map[string]any{"ok": false, "why": err.Error()})
		return
	}
	bundles, _ := filepath.Glob(filepath.Join(verified, "localghost-server-*.tar.gz"))
	if len(bundles) != 1 {
		writeJSON(w, map[string]any{"ok": false, "why": fmt.Sprintf("the set holds %d release bundles, not one", len(bundles))})
		return
	}
	rel, err := update.Unpack(bundles[0], filepath.Join(p.State, "releases", "unpacking"))
	if err == nil {
		final := filepath.Join(p.State, "releases", rel.Version)
		if shelf {
			// the set being put on lives under final: it moves with the fresh unpack
			if err = os.Rename(from, filepath.Join(rel.Dir, "set")); err == nil {
				from = filepath.Join(rel.Dir, "set")
			}
		}
		if err == nil {
			_ = os.RemoveAll(final)
			if err = os.Rename(rel.Dir, final); err == nil {
				rel.Dir = final
				if shelf {
					from = update.SetDir(p, rel.Version)
				}
			}
		}
	}
	if err != nil {
		writeJSON(w, map[string]any{"ok": false, "why": err.Error()})
		return
	}
	if rel.Version == Version {
		writeJSON(w, map[string]any{"ok": false, "why": "the box already runs " + Version})
		return
	}
	cohort := filepath.Join(s.cfg.StateDir, "mnt", fmt.Sprintf("slot%d", mounted), "bin")
	if err := update.Apply(p, rel, cohort, Version); err != nil {
		secdLog.Error("update not put on (the running build is untouched where the error came first)", "fn", "putOn", "err", err)
		if rerr := update.Rollback(p, "putting it on failed: "+err.Error()); rerr != nil {
			secdLog.Error("and the earlier build could not be put back", "fn", "putOn", "err", rerr)
		}
		writeJSON(w, map[string]any{"ok": false, "why": err.Error()})
		return
	}
	// the set onto the shelf (a switch's is there already), the oldest off it
	if err := update.Keep(p, rel.Version, from); err != nil {
		secdLog.Warn("the set was not kept on the shelf", "fn", "putOn", "version", rel.Version, "err", err)
	}
	if dropped := update.Prune(p, rel.Version); len(dropped) > 0 {
		secdLog.Info("shelf pruned", "fn", "putOn", "dropped", strings.Join(dropped, " "))
	}
	secdLog.Info("release put on, restarting onto it", "fn", "putOn", "from", Version, "to", rel.Version, "shelf", shelf)
	writeJSON(w, map[string]any{"ok": true, "version": rel.Version, "changes": rel.Changes})
	s.restartOnto("the release " + rel.Version)
}

// handleUpdateRollback , POST /v1/update/rollback.
func (s *Server) handleUpdateRollback(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	if err := update.Rollback(s.updPaths(), "asked from the phone"); err != nil {
		writeJSON(w, map[string]any{"ok": false, "why": err.Error()})
		return
	}
	writeJSON(w, map[string]any{"ok": true})
	s.restartOnto("the earlier build")
}

// restartOnto locks the box and ends secd a moment after the answer has gone, so systemd starts
// it again onto the files now in place. The next unlock is cold: the cohort starts from the new
// binaries (ingested from staging before the daemons start).
func (s *Server) restartOnto(what string) {
	go func() {
		time.Sleep(1500 * time.Millisecond)
		secdLog.Info("locking and restarting", "fn", "restartOnto", "onto", what)
		if err := s.Shutdown(); err != nil {
			secdLog.Warn("lock before the restart reported", "fn", "restartOnto", "err", err)
		}
		restartSelf()
	}()
}

// startTrial is secd's part of a trial, from its start: after a minute up, the guard's count of
// quick starts begins again.
func (s *Server) startTrial() {
	if update.LoadTrial(s.updPaths()).State != "trial" {
		return
	}
	go func() {
		time.Sleep(time.Minute)
		update.ServedAMinute(s.updPaths())
	}()
}

// trialOnUnlock follows the first unlocks onto a release on trial.
func (s *Server) trialOnUnlock(ok bool, failedAt profile.Stage) {
	p := s.updPaths()
	t := update.LoadTrial(p)
	if t.State != "trial" {
		return
	}
	if !ok {
		if failedAt == profile.StageResolve {
			return // a wrong PIN says nothing about the build
		}
		s.rollBackNow(fmt.Sprintf("the first unlock onto %s failed at %s", t.Version, failedAt.Label()))
		return
	}
	if t.UnlockedAt == 0 {
		t.UnlockedAt = time.Now().Unix()
		_ = update.SaveTrial(p, t)
	}
	s.upd.mu.Lock()
	if s.upd.watching {
		s.upd.mu.Unlock()
		return
	}
	s.upd.watching = true
	s.upd.mu.Unlock()
	go func() {
		defer func() { s.upd.mu.Lock(); s.upd.watching = false; s.upd.mu.Unlock() }()
		time.Sleep(trialWatch)
		s.mu.Lock()
		mounted := s.mounted
		s.mu.Unlock()
		if mounted < 0 {
			return // locked before the watch ended: the next unlock watches again
		}
		sock := filepath.Join(s.cfg.StateDir, "mnt", fmt.Sprintf("slot%d", mounted), "run", "watchd.sock")
		st, err := watchd.NewClient(sock).Status()
		if err != nil {
			return
		}
		if why := unhealthy(st); why != "" {
			s.rollBackNow(fmt.Sprintf("after the first unlock onto %s, %s", t.Version, why))
			return
		}
		cur := update.LoadTrial(p)
		if cur.State == "trial" {
			cur.State = "confirmed"
			_ = update.SaveTrial(p, cur)
			secdLog.Info("release confirmed: ten minutes up with the daemons healthy", "fn", "trialOnUnlock", "version", cur.Version)
		}
	}()
}

// unhealthy names a critical daemon that is down or keeps restarting; "" when all is well.
func unhealthy(st []watchd.ServiceStatus) string {
	for _, d := range st {
		if !d.Critical {
			continue
		}
		if d.Restarts >= 3 {
			return fmt.Sprintf("%s restarted %d times", d.Name, d.Restarts)
		}
		if d.State == "down" || d.State == "backoff" {
			return fmt.Sprintf("%s is %s", d.Name, d.State)
		}
	}
	return ""
}

func (s *Server) rollBackNow(reason string) {
	if err := update.Rollback(s.updPaths(), reason); err != nil {
		if !errors.Is(err, os.ErrNotExist) {
			secdLog.Error("a release on trial failed and could not be rolled back", "fn", "rollBackNow", "why", reason, "err", err)
		}
		return
	}
	secdLog.Warn("release rolled back", "fn", "rollBackNow", "why", reason)
	s.restartOnto("the earlier build")
}
