package secd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/profile"
	"github.com/LocalGhostDao/localghost/server/internal/models"
)

// Server is the ghost.secd HTTP surface the phone talks to. It wires the library packages into the
// handlers the app's BoxClient calls: unlock (streamed), info, status, and the model catalogue.
//
// Auth model recap, enforced by the layers around this: nginx terminates TLS and rejects any client
// without a box-issued device cert at the handshake (the access key), so every request that reaches
// here is already from an enrolled device. The PIN (account selection) is then proven at /unlock.
type Server struct {
	// cached run-user credentials for spool-file handoff (see spoolCred in frames_http.go)
	credOnce sync.Once
	credUID  int
	credGID  int
	enrolFlagField // one-time "a verified device reached us" marker for provisioning's rotation loop
	cfg      Config
	models   *models.Registry
	mu       sync.Mutex
	mounted  int // currently mounted slot, -1 if locked
	unlock   *unlockService
	session  *sessionManager // the one live session token (foreground + poller share it)
	mute     *hw.MuteStore   // notification mute read/write (in-volume Postgres/Redis), per scope
	notif    *hw.NotifStore  // notification produce/read/seen/delete (in-volume Postgres/Redis)
}

type Config struct {
	StateDir string // unencrypted: /var/lib/ghost (certs, models)
	Disk     string // the raw LUKS-formatted data disk, e.g. /dev/nvme1n1 (used by the TPM backend)
	RunUser  string // if set (--user <name>), watchd runs the ghost.*d cohort as this user
}

// StatusView is the front-door state ghost-cli reads over secd's control socket. It works even when
// LOCKED (secd is the always-on process), so `ghost-cli ghost.secd status` tells you the box is
// locked and appears-down without needing to unlock it first.
type StatusView struct {
	Locked      bool `json:"locked"`
	MountedSlot int  `json:"mountedSlot"` // -1 when locked
	HasSession  bool `json:"hasSession"`
}

// Status returns the current front-door state for the control socket.
func (s *Server) Status() StatusView {
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	return StatusView{
		Locked:      mounted < 0,
		MountedSlot: mounted,
		HasSession:  s.session.ExpiresAt().After(time.Now()),
	}
}

// Off is the `off` command: lock the box NOW from the local control socket, authorized by the main
// PIN rather than an app session. It is the border-crossing "make appears-down true" action , the same
// teardown as /v1/lock (stop cohort, stop DBs, unmount, luksClose), reachable without opening the app.
//
// Deliberately Option A , a LOCK, never a wipe. off can only tear the box down to the cold state the
// main PIN reverses; it cannot destroy data. That separation is a property you can state plainly: off
// cannot erase anything, so it cannot be coerced into erasing anything.
//
// Indistinguishability: whatever the input, Off returns nothing an observer can read , a wrong PIN, a
// wipe PIN, an already-locked box, and a successful lock all return with no error and no signal. The
// only observable is the box being (or staying) down, which is the whole point.
func (s *Server) Off(pin string) {
	if !s.unlock.AuthorizesLock(pin) {
		return // wrong PIN or wipe PIN: off does nothing, indistinguishably. (No wipe: off never erases.)
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.session.Revoke() // already cold; kill any stray token too
		return
	}
	if _, err := s.unlock.Lock(mounted); err != nil {
		// The volume did not fully close. Stay honest about mounted state (it is still up); the caller
		// gets no detail either way. Log locally for the operator; the socket reply is opaque.
		secdLog.Warn("off: lock did not fully complete", "fn", "Off", "err", err)
		return
	}
	s.mu.Lock()
	s.mounted = -1
	s.mu.Unlock()
	s.session.Revoke() // foreground + poller down until the next unlock
}

func New(cfg Config) (*Server, error) {
	if cfg.StateDir == "" {
		cfg.StateDir = "/var/lib/ghost"
	}
	if err := os.MkdirAll(filepath.Join(cfg.StateDir, "models"), 0o755); err != nil {
		return nil, fmt.Errorf("state dir: %w", err)
	}
	s := &Server{
		cfg:     cfg,
		models:  models.NewRegistry(filepath.Join(cfg.StateDir, "models")),
		mounted: -1,
	}
	s.session = newSessionManager(SessionTTL)
	// Wire the notification mute store. The mute lives in the in-volume Postgres/Redis, per scope
	// (global "*" + per-service). The mount path for a slot is <stateDir>/mnt/slot<N> (matching
	// DMCryptMounter) and the pg socket is its "postgres" subdir. The handlers read it on the
	// notification poll and the settings control. Built in both sim and tpm builds (hw is not
	// tpm-tagged except tpm.go); harmless in sim (no DBs -> the poller is "down" via the
	// locked/mounted check before the mute is consulted).
	s.mute = hw.NewMuteStore(func(slot int) string {
		mnt := filepath.Join(cfg.StateDir, "mnt", fmt.Sprintf("slot%d", slot))
		return hw.SocketForMount(mnt)
	})
	s.notif = hw.NewNotifStore(func(slot int) string {
		mnt := filepath.Join(cfg.StateDir, "mnt", fmt.Sprintf("slot%d", slot))
		return hw.SocketForMount(mnt)
	})
	// newDefaultBackend is build-tag-selected: the simulation in the default build, the real TPM +
	// dm-crypt + Postgres/Redis backend with -tags tpm. This is the seam where unlock meets hardware.
	s.unlock = newUnlockService(newDefaultBackend(cfg))

	// Reconcile mount state at startup. The dm-crypt mount is KERNEL state: it survives a secd process
	// restart (secd has no unmount-on-exit). If slot 0 is already mounted , e.g. secd crashed, or was
	// restarted while unlocked , adopt it rather than defaulting to locked, so we do not split-brain
	// (report locked while the volume is up and refuse authenticated calls to mounted data).
	//
	// HONEST LIMIT: adopting the mount does NOT re-adopt the ghost.*d daemons. They were spawned by
	// the OLD secd in their own process group (Setpgid), so a secd restart orphans them , still
	// running, but with no handle in the new supervisor. So after an unclean secd restart: reads work,
	// but /v1/status shows no daemons and a later Lock cannot signal them. The correct operational fix
	// is to LOCK before updating secd (the release script does this); this reconcile only keeps the
	// crash case from looking like data loss. A future improvement is re-adopting daemons by PID file.
	if s.unlock.Warm(profile.MainSlot) {
		s.mounted = profile.MainSlot
	}
	return s, nil
}

// Handler returns the routed mux. Routes match what the app's BoxClient calls.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/v1/health", s.handleHealth)
	mux.HandleFunc("/v1/unlock", s.handleUnlockStart)
	mux.HandleFunc("/v1/unlock/poll", s.handleUnlockPoll)
	mux.HandleFunc("/v1/lock", s.handleLock)
	mux.HandleFunc("/v1/info", s.handleInfo)
	mux.HandleFunc("/v1/status", s.handleStatus) // supervised-daemon roster for the app's Box Status screen
	mux.HandleFunc("/v1/notifications", s.handleNotifications)
	mux.HandleFunc("/v1/notifications/mute", s.handleMute)
	mux.HandleFunc("/v1/notifications/list", s.handleNotificationList)
	mux.HandleFunc("/v1/notifications/seen", s.handleNotificationSeen)
	mux.HandleFunc("/v1/notifications/delete", s.handleNotificationDelete)
	mux.HandleFunc("/v1/notifications/answer", s.handleNotificationAnswer)
	mux.HandleFunc("/v1/frames/upload", s.handleFrameUpload)
	mux.HandleFunc("/v1/frames/latest", s.handleFramesLatest) // where-was-I for the app's sync cursor
	mux.HandleFunc("/v1/frames/list", s.handleFramesList)     // gallery paging, newest first
	mux.HandleFunc("/v1/frames/thumb", s.handleFrameThumb)    // one thumbnail's bytes
	mux.HandleFunc("/v1/frames/exists", s.handleFramesExists) // pre-upload dedup by content hash
	mux.HandleFunc("/v1/sync/cursor", s.handleSyncCursor)     // device sync position, survives reinstall
	mux.HandleFunc("/v1/frames/tag", s.handleFrameTag)        // user tag corrections (tombstoned removes)
	mux.HandleFunc("/v1/services/summary", s.handleServicesSummary) // latest sample + 24h blob per target
	mux.HandleFunc("/v1/services/detail", s.handleServiceDetail)    // ring buffers for one target (sparklines)
	mux.HandleFunc("/v1/chat", s.handleChat)                  // ask the box's model (via synthd's retrieval seam)
	mux.HandleFunc("/v1/locations", s.handleLocations)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/models/", s.handleModelBytes) // /v1/models/{id}
	mux.HandleFunc("/v1/openapi.json", s.handleOpenAPI)
	// Observe verified clients: the first request nginx forwards with a verified client cert means a
	// phone finished enrolment. Mark it (best-effort) so provisioning can stop rotating the QR. This
	// wraps every route and changes no response , purely a side signal.
	observed := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if verifiedClient(r) {
			s.noteVerifiedDevice()
		}
		mux.ServeHTTP(w, r)
	})
	return logRequests(observed)
}

// handleHealth is the cheap reachability check the app's reachable() calls. It needs no account.
func (s *Server) handleHealth(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, map[string]any{"ok": true, "service": "ghost.secd"})
}

// handleLock spins the box down on demand: the settings "lock now" button. A deliberate foreground
// action by an AUTHENTICATED app , it requires a valid session (you cannot lock a box you cannot
// already talk to), then stops the account's DBs, unmounts + luksCloses the volume (evicting the key
// from the kernel), marks the box locked, and revokes the session so every subsequent request , the
// app's own poll included , collapses to the appears-down 502 until a PIN unlocks it again.
//
// Idempotent: locking an already-locked box succeeds without doing anything. The response is a normal
// 200 (the app asked for this and wants confirmation); only an authenticated caller can reach it, so
// it adds no oracle. A failed unmount is a real error , the drive did NOT close , and is reported so
// the app does not falsely tell the user the box is locked.
func (s *Server) handleLock(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) {
		s.appearsDown(w) // no session -> looks down, same as everything else
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()

	if mounted < 0 {
		s.session.Revoke() // already cold; make sure the caller's token is dead too
		writeJSON(w, map[string]any{"locked": true, "steps": []any{}})
		return
	}

	steps, err := s.unlock.Lock(mounted)
	if err != nil {
		// The volume did not fully close. Do NOT claim locked, but DO return the steps so the app can
		// show which one failed, and keep the mounted state honest (still mounted).
		writeJSON(w, map[string]any{"locked": false, "steps": steps, "error": "lock failed"})
		return
	}

	s.mu.Lock()
	s.mounted = -1
	s.mu.Unlock()
	s.session.Revoke() // kill the token: foreground + poller both go down until the next unlock
	writeJSON(w, map[string]any{"locked": true, "steps": steps})
}

// Shutdown performs a clean lock for process shutdown (SIGTERM). ghost.secd has ONE shutdown
// behaviour: if the box is mounted, tear the whole stack down , stop watchd (which stops the cohort
// and confirms every daemon dead), stop the DBs, unmount, close LUKS , then return. This is why secd
// is freely restartable: a systemd restart always brings everything down cleanly, never leaving the
// volume mounted with no front door. The cost is a re-unlock after every secd deploy, which is the
// correct trade for a security appliance (a stopped gatekeeper must not leave data open). Returns any
// teardown error so the caller can log it, but shutdown proceeds regardless.
func (s *Server) Shutdown() error {
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		return nil // already locked; nothing to tear down
	}
	_, err := s.unlock.Lock(mounted)
	s.mu.Lock()
	s.mounted = -1
	s.mu.Unlock()
	s.session.Revoke()
	return err
}

// handleInfo returns box + mounted-account summary for the app's home screen. Returns locked state
// if no account is mounted.
func (s *Server) handleInfo(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		writeJSON(w, map[string]any{"locked": true})
		return
	}
	writeJSON(w, map[string]any{
		"locked":      false,
		"mountedSlot": mounted,
		"daemons":     1, // placeholder until the backing daemons report in
	})
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.WriteHeader(status)
	writeJSON(w, map[string]any{"error": msg})
}
