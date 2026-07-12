package secd

import (
	"time"
	"encoding/json"
	"net/http"
	"sync"

	"github.com/LocalGhostDao/localghost/server/internal/profile"
)

// unlockService runs a PIN unlock and exposes its progress for polling, mirroring the app's
// UnlockStage stream. submitPin starts an unlock; the app polls /unlock/poll once a second and gets
// the stages completed so far (all at once if the account is hot, accumulating if cold).
//
// The stage sequence is identical for every unlock, so a wipe looks the same as a real
// one. This wires profile.StreamUnlock (the validated stage logic) to a poll-able state.
type unlockService struct {
	backend  UnlockBackend
	mu       sync.Mutex
	progress map[profile.Stage]profile.StepState
	order    []profile.Stage
	done     bool
	failed   string
	openSlot int
}

// statusReporter is optionally implemented by a backend that supervises daemons (the real one does;
// a test double may not). Kept separate from UnlockBackend so that interface stays about unlock only.
type statusReporter interface {
	SupervisorStatus() []ServiceStatus
}

// Warm reports whether the slot's volume is already mounted (kernel state that survives a secd
// restart). Used at startup to adopt an existing mount instead of falsely reporting locked.
func (u *unlockService) Warm(slot int) bool { return u.backend.Warm(slot) }

// AuthorizesLock passes through to the backend's side-effect-free main-PIN check for the off command.
func (u *unlockService) AuthorizesLock(pin string) bool { return u.backend.AuthorizesLock(pin) }

// SupervisorStatus returns the supervised-service snapshot if the backend provides one, else nil.
func (u *unlockService) SupervisorStatus() []ServiceStatus {
	if sr, ok := u.backend.(statusReporter); ok {
		return sr.SupervisorStatus()
	}
	return nil
}

func newUnlockService(backend UnlockBackend) *unlockService {
	return &unlockService{
		backend:  backend,
		progress: map[profile.Stage]profile.StepState{},
		order: []profile.Stage{
			profile.StageResolve, profile.StageUnseal, profile.StageMount,
			profile.StageStartDB, profile.StageStartCache, profile.StageDaemons, profile.StageReady,
		},
		openSlot: profile.NoSlot,
	}
}

// Lock spins the slot down via the backend, collecting the teardown steps it emits (so the app can
// show the spin-down), then resets the unlock service to its cold, pre-unlock state so the next open
// re-runs every stage from scratch. Idempotent at the backend level.
func (u *unlockService) Lock(slot int) ([]map[string]any, error) {
	steps := make([]map[string]any, 0, len(profile.LockStages))
	err := u.backend.Lock(slot, func(p profile.Progress) {
		steps = append(steps, map[string]any{
			"stage": stageName(p.Stage),
			"label": p.Stage.Label(),
			"state": stepStateName(p.State),
		})
	})
	u.mu.Lock()
	u.progress = map[profile.Stage]profile.StepState{}
	u.done = false
	u.failed = ""
	u.openSlot = profile.NoSlot
	u.mu.Unlock()
	return steps, err
}

type unlockRequest struct {
	Pin string `json:"pin"`
}

// handleUnlockStart begins an unlock. It resolves the PIN (the security decision) immediately, then
// runs the stages in the background so the app can poll progress. Resolve happens here so a wrong
// PIN is rejected before any work.
func (s *Server) handleUnlockStart(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "POST required")
		return
	}
	var req unlockRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}

	u := s.unlock
	u.mu.Lock()
	// reset for a fresh unlock
	u.progress = map[profile.Stage]profile.StepState{}
	u.done = false
	u.failed = ""
	u.openSlot = profile.NoSlot
	u.mu.Unlock()

	// Drive the unlock through the backend: resolve the PIN (main / wipe / reject),
	// unseal the slot key from the TPM, map + mount the container, start the per-account DB + cache.
	// The default build wires a simulation; the `tpm` build wires the real hardware path.
	go u.run(req.Pin)

	writeJSON(w, map[string]any{"started": true})
}

// run walks the stages, marking each running then complete, with a short delay so a cold unlock
// shows a real progression. The hot path (already mounted) would mark them Skipped instantly.
func (u *unlockService) run(pin string) {
	emit := func(p profile.Progress) {
		u.mu.Lock()
		u.progress[p.Stage] = p.State
		u.mu.Unlock()
	}
	slot, err := runUnlock(u.backend, pin, emit)
	u.mu.Lock()
	if err != nil || slot == profile.NoSlot {
		u.failed = "unlock failed"
		if err != nil && err != errReject {
			u.failed = err.Error()
		}
	} else {
		u.done = true
		u.openSlot = slot
		// The MODEL stage is informational , done is already true, the gate opens, the box archives.
		// This keeps reporting so the unlock screen (and anything polling) can show the 12B warming
		// up instead of the model appearing ready-by-silence. oracled owns the actual load; we watch
		// its health port and mirror the state into the stage map.
		go u.watchModel(emit)
	}
	u.mu.Unlock()
}

// watchModel mirrors oracled's model-load state into the MODEL unlock stage. Running while loading,
// Complete when oracled reports healthy, Failed with the detail if oracled never gets there. Bounded:
// a 12B with mlock takes a couple of minutes; ten is a hard ceiling, not an expectation.
func (u *unlockService) watchModel(emit func(profile.Progress)) {
	emit(profile.Progress{Stage: profile.StageModel, State: profile.Running})
	deadline := time.Now().Add(10 * time.Minute)
	client := &http.Client{Timeout: 2 * time.Second}
	lastDetail := "no response from ghost.oracled"
	for time.Now().Before(deadline) {
		resp, err := client.Get("http://127.0.0.1:9118/health")
		if err == nil {
			var body struct {
				Code   int    `json:"code"`
				Detail string `json:"detail"`
			}
			derr := json.NewDecoder(resp.Body).Decode(&body)
			_ = resp.Body.Close()
			if derr == nil {
				if body.Code == 0 {
					emit(profile.Progress{Stage: profile.StageModel, State: profile.Complete})
					secdLog.Info("model ready", "fn", "watchModel")
					return
				}
				if body.Detail != "" {
					lastDetail = body.Detail
				}
			}
		}
		// A lock during the load ends the watch , no stage spam over a volume that is gone.
		u.mu.Lock()
		abandoned := u.openSlot == profile.NoSlot
		u.mu.Unlock()
		if abandoned {
			return
		}
		time.Sleep(3 * time.Second)
	}
	secdLog.Warn("model did not become ready", "fn", "watchModel", "within", "10m", "last", lastDetail)
	emit(profile.Progress{Stage: profile.StageModel, State: profile.Errored})
}

// handleUnlockPoll returns the current stage states, the shape the app's UnlockSnapshot.from expects.
func (s *Server) handleUnlockPoll(w http.ResponseWriter, r *http.Request) {
	u := s.unlock
	u.mu.Lock()
	defer u.mu.Unlock()
	stages := make([]map[string]any, 0, len(u.order))
	for _, st := range u.order {
		state, ok := u.progress[st]
		stageState := "pending"
		if ok {
			stageState = stepStateName(state)
		}
		stages = append(stages, map[string]any{"stage": stageName(st), "state": stageState})
	}
	resp := map[string]any{"stages": stages, "done": u.done}
	if u.failed != "" {
		resp["failed"] = u.failed
	}
	if u.done {
		if u.failed == "" && u.openSlot >= 0 {
			// correct PIN: reflect the mount and issue a FRESH session token for the app to carry.
			s.mu.Lock()
			s.mounted = u.openSlot
			s.mu.Unlock()
			if tok, err := s.session.Issue(); err == nil {
				resp["token"] = tok
				// Hand the app the expiry so it can persist the token and, as the 2-day window
				// closes, show a "reopen to check notifications" state , the box cannot poll or
				// notify a session it can no longer authenticate, so the app must prompt a re-unlock.
				resp["expiresAt"] = s.session.ExpiresAt().UTC().Format(time.RFC3339)
				resp["ttlSeconds"] = int(SessionTTL.Seconds())
			}
		} else {
			// wrong PIN (or failed unlock): revoke any live token, so the foreground AND the poller
			// go dark together (shared fate). The mount is NOT touched.
			s.session.Revoke()
		}
	}
	writeJSON(w, resp)
}

func stageName(st profile.Stage) string {
	switch st {
	case profile.StageResolve:
		return "RESOLVE"
	case profile.StageUnseal:
		return "UNSEAL"
	case profile.StageMount:
		return "MOUNT"
	case profile.StageStartDB:
		return "START_DB"
	case profile.StageStartCache:
		return "START_CACHE"
	case profile.StageDaemons:
		return "DAEMONS"
	case profile.StageReady:
		return "READY"
	case profile.StageStopServices:
		return "STOP_SERVICES"
	case profile.StageStopCache:
		return "STOP_CACHE"
	case profile.StageStopDB:
		return "STOP_DB"
	case profile.StageUnmount:
		return "UNMOUNT"
	case profile.StageLocked:
		return "LOCKED"
	default:
		return "UNKNOWN"
	}
}

func stepStateName(s profile.StepState) string {
	switch s {
	case profile.Running:
		return "RUNNING"
	case profile.Skipped:
		return "SKIPPED"
	case profile.Complete:
		return "COMPLETE"
	case profile.Errored:
		return "ERRORED"
	default:
		return "PENDING"
	}
}
