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
	"github.com/LocalGhostDao/localghost/server/internal/models"
)

// Server is the ghost.secd HTTP surface the phone talks to. It wires the library packages into the
// handlers the app's BoxClient calls: unlock (streamed), info, status, and the model catalogue.
//
// Auth model recap, enforced by the layers around this: nginx terminates TLS and rejects any client
// without a box-issued device cert at the handshake (the access key), so every request that reaches
// here is already from an enrolled device. The PIN (account selection) is then proven at /unlock.
type Server struct {
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
	s.session = newSessionManager(12 * time.Hour)
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
	mux.HandleFunc("/v1/notifications", s.handleNotifications)
	mux.HandleFunc("/v1/notifications/mute", s.handleMute)
	mux.HandleFunc("/v1/notifications/list", s.handleNotificationList)
	mux.HandleFunc("/v1/notifications/seen", s.handleNotificationSeen)
	mux.HandleFunc("/v1/notifications/delete", s.handleNotificationDelete)
	mux.HandleFunc("/v1/notifications/answer", s.handleNotificationAnswer)
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/models/", s.handleModelBytes) // /v1/models/{id}
	return logRequests(mux)
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
