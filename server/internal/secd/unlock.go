package secd

import (
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/base64"
	"encoding/json"
	"net/http"
	"sync"
	"time"

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
	// onDone, when set, hears how each unlock ended: ok, or the step it failed at (a release on
	// trial is rolled back when the first unlock onto it fails past checking the PIN, update_http.go)
	onDone   func(ok bool, failedAt profile.Stage)
	mu       sync.Mutex
	progress map[profile.Stage]profile.StepState
	order    []profile.Stage
	done     bool
	failed   string
	openSlot int
	// model is the model's load progress while MODEL runs (oracled's /load), for the app's bar.
	// Numbers only (phase, percent, time): nothing that names the model or the account, so the
	// poll keeps the same shape whichever account opened.
	model *modelLoad
	// running: an unlock is in progress; runSum is its PIN's hash, so a second tap of the same PIN
	// joins it instead of starting another
	running bool
	runSum  [32]byte
	// runID is a random name for this unlock, handed ONLY to the POST that carried the PIN. The
	// session token goes only to a poll that presents it, once (repeated to that run for
	// tokenWindow, so a lost answer can be asked again), never to anyone else. Before this, every
	// poll after a finished unlock minted a fresh token for whoever asked, with no PIN: any enrolled
	// device, or any process on the box itself through secd's loopback port.
	runID  string
	token  string
	doneAt time.Time
	// timesPath is where the last cold unlocks' step times are kept, on the encrypted volume
	// (replay.go); "" keeps nothing and a warm unlock replays the built-in profile.
	timesPath string
}

// tokenWindow: how long after a finished unlock its run may collect the session token.
const tokenWindow = 2 * time.Minute

func newRunID() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return base64.RawURLEncoding.EncodeToString(b)
}

// modelLoad is oracled's load progress as the unlock poll carries it.
type modelLoad struct {
	Phase     string `json:"phase"`
	Pct       int    `json:"pct"`
	EtaMs     int64  `json:"etaMs"`
	ElapsedMs int64  `json:"elapsedMs"`
}

// statusReporter is optionally implemented by a backend that supervises daemons (the real one does;
// a test double may not). Kept separate from UnlockBackend so that interface stays about unlock only.
type statusReporter interface {
	SupervisorStatus() []ServiceStatus
}

// halter is optionally implemented by a backend that can stop every service while keeping the volume
// mounted (the maintenance stop). Same seam pattern as statusReporter , UnlockBackend stays about
// unlock, and a test double that never halts compiles unchanged.
type halter interface {
	Halt(slot int) error
}

// Halt passes the maintenance stop through to the backend. Unsupported backends report so plainly.
func (u *unlockService) Halt(slot int) error {
	h, ok := u.backend.(halter)
	if !ok {
		return errHaltUnsupported
	}
	return h.Halt(slot)
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
			profile.StageStartDB, profile.StageStartCache, profile.StageDaemons,
			profile.StageModel, profile.StageReady,
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
	u.model = nil
	u.runID, u.token, u.doneAt = "", "", time.Time{}
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
	// pre-auth: the body is a PIN and nothing else; a body past a few KB is not a PIN
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&req); err != nil {
		writeErr(w, http.StatusBadRequest, "bad request")
		return
	}

	u := s.unlock
	sum := sha256.Sum256([]byte(req.Pin))
	u.mu.Lock()
	// ONE unlock at a time. A second POST while one runs (a second tap on OK while "starting
	// database" sat behind a busy table) used to start a second run over the same state: two
	// schema converges at once, which deadlocked each other on 29 Sep 2026 and failed the unlock.
	// The same PIN again joins the running unlock (the app just polls it); a different one is told
	// to wait, and is not tried.
	if u.running {
		same := subtle.ConstantTimeCompare(sum[:], u.runSum[:]) == 1
		run := u.runID
		u.mu.Unlock()
		if same {
			writeJSON(w, map[string]any{"started": true, "run": run})
			return
		}
		writeErr(w, http.StatusConflict, "an unlock is already running")
		return
	}
	u.running, u.runSum = true, sum
	run := newRunID()
	u.runID, u.token, u.doneAt = run, "", time.Time{}
	// reset for a fresh unlock
	u.progress = map[profile.Stage]profile.StepState{}
	u.done = false
	u.failed = ""
	u.openSlot = profile.NoSlot
	u.model = nil
	u.mu.Unlock()

	// Drive the unlock through the backend: resolve the PIN (main / wipe / reject),
	// unseal the slot key from the TPM, map + mount the container, start the per-account DB + cache.
	// The default build wires a simulation; the `tpm` build wires the real hardware path.
	go func() {
		u.run(req.Pin)
		u.mu.Lock()
		u.running, u.runSum = false, [32]byte{}
		u.mu.Unlock()
	}()

	writeJSON(w, map[string]any{"started": true, "run": run})
}

// run walks the stages, marking each running then complete, with a short delay so a cold unlock
// shows a real progression. The hot path (already mounted) would mark them Skipped instantly.
func (u *unlockService) run(pin string) {
	t0 := time.Now()
	publish := func(p profile.Progress) {
		u.mu.Lock()
		u.progress[p.Stage] = p.State
		u.mu.Unlock()
	}
	// A WARM box (already mounted) answers in about a second, and a second says someone opened it
	// recently. So a warm unlock does its real work out of sight and then shows one of this box's
	// last eight cold unlocks, step by step, at its own pace (replay.go): the same steps completing
	// (never "skipped"), the same length within 8%, the same polls on the wire. A cold unlock shows
	// itself as it happens and is recorded for the next warm one. Whether the box is warm is a fact
	// about the box, not the PIN, so it is read before the PIN is.
	warm := u.backend != nil && u.backend.Warm(profile.MainSlot)
	var shadow []profile.Progress
	ends := map[profile.Stage]time.Duration{} // when each step finished, from t0 (cold: to record)
	emit := func(p profile.Progress) {
		if p.State == profile.Complete || p.State == profile.Skipped {
			ends[p.Stage] = time.Since(t0)
		}
		if warm {
			shadow = append(shadow, p)
			return
		}
		publish(p)
	}
	if warm {
		publish(profile.Progress{Stage: profile.StageResolve, State: profile.Running})
	}
	slot, err := runUnlock(u.backend, pin, emit)
	if err != nil || slot == profile.NoSlot {
		// a failure shows as it is, when it happens (a reject has already waited out the common
		// reject time in runUnlock, warm or cold)
		for _, p := range shadow {
			publish(p)
		}
		u.mu.Lock()
		u.failed = "unlock failed"
		if err != nil && err != errReject {
			u.failed = err.Error()
		}
		failedAt := profile.StageResolve
		for st, state := range u.progress {
			if state == profile.Errored {
				failedAt = st
			}
		}
		u.mu.Unlock()
		if u.onDone != nil {
			u.onDone(false, failedAt)
		}
		return
	}
	if warm {
		replayUnlock(t0, pickUnlockTimes(loadUnlockTimes(u.timesPath)), publish, time.Sleep)
	} else if cold, ok := coldTimes(ends); ok {
		saveUnlockTimes(u.timesPath, appendUnlockTimes(loadUnlockTimes(u.timesPath), cold))
	}
	// THE MODEL IS OFF THE UNLOCK (1 Oct 2026). It used to hold the unlock on "loading model" until
	// oracled said ready, 10 to 30 s of a cold unlock. oracled starts loading the moment the cohort
	// is up, and the app now asks /v1/model before a chat and says "loading the model" while it
	// loads. MODEL is reported skipped, warm or cold, so the stream is the same either way.
	publish(profile.Progress{Stage: profile.StageModel, State: profile.Skipped})
	publish(profile.Progress{Stage: profile.StageReady, State: profile.Complete})
	u.mu.Lock()
	u.done = true
	u.openSlot = slot
	u.doneAt = time.Now()
	u.mu.Unlock()
	if u.onDone != nil {
		u.onDone(true, profile.StageReady)
	}
}

// modelState is the box model's state for the app: ready, or loading with oracled's own progress
// (phase, percent, time left), or not up yet. GET /v1/model; the app asks before a chat.
type modelState struct {
	Ready     bool   `json:"ready"`
	Phase     string `json:"phase"`
	Pct       int    `json:"pct"`
	EtaMs     int64  `json:"etaMs"`
	ElapsedMs int64  `json:"elapsedMs"`
	Detail    string `json:"detail,omitempty"`
}

// modelStatus asks oracled's health port (code 0 = the model answers) and its /load.
func modelStatus(c *http.Client) modelState {
	st := modelState{Phase: "starting", Detail: "the model service is starting"}
	resp, err := c.Get(oracledHealthURL + "/health")
	if err != nil {
		return st
	}
	var body struct {
		Code   int    `json:"code"`
		Detail string `json:"detail"`
	}
	derr := json.NewDecoder(resp.Body).Decode(&body)
	_ = resp.Body.Close()
	if derr == nil && body.Code == 0 {
		return modelState{Ready: true, Phase: "ready", Pct: 100}
	}
	if derr == nil && body.Detail != "" {
		st.Detail = body.Detail
	}
	if lp, ok := fetchModelLoad(c); ok {
		st.Phase, st.Pct, st.EtaMs, st.ElapsedMs = lp.Phase, lp.Pct, lp.EtaMs, lp.ElapsedMs
	}
	return st
}

// handleModel , GET /v1/model , the box model's state (session required).
func (s *Server) handleModel(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	writeJSON(w, modelStatus(&http.Client{Timeout: 2 * time.Second}))
}

// oracledHealthURL is ghost.oracled's loopback health listener (its fixed health port).
var oracledHealthURL = "http://127.0.0.1:9118"

// fetchModelLoad reads oracled's /load. False when oracled is not up yet or is an older build
// without it (the bar then runs on the app's own timings).
func fetchModelLoad(c *http.Client) (modelLoad, bool) {
	var lp modelLoad
	resp, err := c.Get(oracledHealthURL + "/load")
	if err != nil {
		return lp, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return lp, false
	}
	if err := json.NewDecoder(resp.Body).Decode(&lp); err != nil || lp.Phase == "" {
		return lp, false
	}
	return lp, true
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
	if u.model != nil {
		resp["model"] = u.model // the model's load: phase, percent, time left
	}
	if u.failed != "" {
		resp["failed"] = u.failed
	}
	if u.done {
		if u.failed == "" && u.openSlot >= 0 {
			// correct PIN: reflect the mount, and hand the session token to THIS run's poller only
			s.mu.Lock()
			s.mounted = u.openSlot
			s.mu.Unlock()
			run := r.URL.Query().Get("run")
			mine := run != "" && u.runID != "" && subtle.ConstantTimeCompare([]byte(run), []byte(u.runID)) == 1
			if mine && time.Since(u.doneAt) < tokenWindow {
				if u.token == "" {
					if tok, err := s.session.Issue(); err == nil {
						u.token = tok
					}
				}
				if u.token != "" && s.session.Valid(u.token) {
					resp["token"] = u.token
					// Hand the app the expiry so it can persist the token and, as the 2-day window
					// closes, show a "reopen to check notifications" state , the box cannot poll or
					// notify a session it can no longer authenticate, so the app must prompt a re-unlock.
					resp["expiresAt"] = s.session.ExpiresAt().UTC().Format(time.RFC3339)
					resp["ttlSeconds"] = int(SessionTTL.Seconds())
				}
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
	case profile.StageModel:
		return "MODEL"
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
