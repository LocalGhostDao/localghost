package secd

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/models"
	"github.com/LocalGhostDao/localghost/server/internal/profile"
)

// Server is the ghost.secd HTTP surface the phone talks to. It wires the library packages into the
// handlers the app's BoxClient calls: unlock (streamed), info, status, and the model catalogue.
//
// Auth model recap, enforced by the layers around this: nginx terminates TLS and rejects any client
// without a box-issued device cert at the handshake (the access key), so every request that reaches
// here is already from an enrolled device. The PIN (account selection) is then proven at /unlock.
type Server struct {
	// cached run-user credentials for spool-file handoff (see spoolCred in frames_http.go)
	credOnce       sync.Once
	credUID        int
	credGID        int
	enrolFlagField // one-time "a verified device reached us" marker for provisioning's rotation loop
	cfg            Config
	models         *models.Registry
	mu             sync.Mutex
	mounted        int // currently mounted slot, -1 if locked
	unlock         *unlockService
	session        *sessionManager // the one live session token (foreground + poller share it)
	retired        retiredCerts    // device certificates replaced by a rotation: answered as if down
	migrated       sync.Map        // device keys whose records were moved from the certificate's name to the key's (devicekey.go)
	edge           edgeState       // the phone's TLS, served here (edge.go)
	upd            updateState     // a server release put on from the phone (update_http.go)
	fetch          fetchState      // a fetch from the mirror started from the phone (sources_http.go)
	mute           *hw.MuteStore   // notification mute read/write (in-volume Postgres/Redis), per scope
	notif          *hw.NotifStore  // notification produce/read/seen/delete (in-volume Postgres/Redis)
	// closing: a lock, halt or shutdown is tearing the volume down. Uploads are refused from the
	// first moment and the ones already streaming are cut (spoolBody reads through a gate that
	// fails once this is set), so no open file on the volume keeps it from unmounting. On 29 Sep
	// 2026 a phone re-sending its camera roll held a .part open through every restart: umount
	// "target is busy" for 75 s, then secd gave up with the LUKS mapping still open.
	closing atomic.Bool
	// uploads: the request bodies streaming into the volume right now. closeDoors sets each one's
	// read deadline to now, so even a body read that is blocked on a stalled phone returns at once.
	upMu    sync.Mutex
	uploads map[*http.ResponseController]struct{}
	// countries: the whole-country map download's atlas and tile counts (countries_http.go)
	countryMu    sync.Mutex
	countryCache *countryCache
}

// closeDoors is the first step of every teardown: no new session-authenticated call from here
// on, and every upload still streaming into the volume stops, whether it is mid-read (the gate)
// or waiting on the network (the read deadline).
func (s *Server) closeDoors() {
	s.closing.Store(true)
	s.session.Revoke()
	s.upMu.Lock()
	for rc := range s.uploads {
		_ = rc.SetReadDeadline(time.Now())
	}
	s.upMu.Unlock()
}

// streaming registers an upload for closeDoors to cut; the handler defers the release. An upload
// that registers just as the doors close sees the flag here and is cut the same way.
func (s *Server) streaming(w http.ResponseWriter) (release func()) {
	rc := http.NewResponseController(w)
	s.upMu.Lock()
	if s.uploads == nil {
		s.uploads = map[*http.ResponseController]struct{}{}
	}
	s.uploads[rc] = struct{}{}
	s.upMu.Unlock()
	if s.closing.Load() {
		_ = rc.SetReadDeadline(time.Now())
	}
	return func() {
		s.upMu.Lock()
		delete(s.uploads, rc)
		s.upMu.Unlock()
	}
}

type Config struct {
	StateDir string // unencrypted: /var/lib/ghost (certs, models)
	Disk     string // the raw LUKS-formatted data disk, e.g. /dev/nvme1n1 (used by the TPM backend)
	RunUser  string // if set (--user <name>), watchd runs the ghost.*d cohort as this user
	CaDir    string // the box CA for device key rotation (rekey.go) and secd's TLS (edge.go); empty: /etc/ghost/ca
	EdgeFile string // "tls" in it: plain HTTP is refused (edge.go); empty: /etc/ghost/edge
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
// Halt is the `halt` command: the MAINTENANCE stop, from the local control socket, authorized by the
// main PIN. Everything Off tears down comes down , cohort, watchd, Redis, Postgres , but the volume
// STAYS MOUNTED, so the operator can work on it directly (DB runtime bundle, engine swap, Postgres
// upgrade). Resume is a plain PIN unlock, which converges every service back up.
//
// PIN-opaque like Off, and for the same reason: AuthorizesLock is deliberately unthrottled and
// side-effect-free, so any command that reported "wrong PIN" would turn the local socket into a free
// PIN oracle. Wrong PIN, wipe PIN, already-halted , the reply is the same silence; the operator
// confirms with `status` or ps, which is one command away and leaks nothing to anyone else.
//
// The session is revoked so the phone's next deliberate action is a PIN unlock (the resume). Status
// keeps reporting mounted , TRUE, and it is also what stops the app from auto-prompting an unlock
// that would restart services mid-surgery. The remaining footgun is the operator themselves
// unlocking from the phone before the surgery is done; halt cannot protect anyone from that.
func (s *Server) Halt(pin string) {
	if !s.unlock.AuthorizesLock(pin) {
		return // wrong PIN or wipe PIN: nothing happens, indistinguishably. (Halt never erases.)
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		return // cold box: nothing to halt, and nothing to learn from the silence
	}
	s.closeDoors()
	defer s.closing.Store(false) // the volume stays mounted; the next unlock opens the doors again
	if err := s.unlock.Halt(mounted); err != nil {
		secdLog.Warn("halt: teardown reported trouble (volume still mounted)", "fn", "Halt", "err", err)
	}
	s.session.Revoke() // next phone action is a PIN unlock, which is the resume
}

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
	s.closeDoors()
	defer s.closing.Store(false)
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
	s.retired.path = filepath.Join(cfg.StateDir, "devices", "retired")
	defer s.startTrial() // a release on trial: secd's part starts with it
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
	s.unlock.onDone = s.trialOnUnlock // a release on trial is judged by its first unlocks
	// the last cold unlocks' step times live on the encrypted volume, beside the data they open
	s.unlock.timesPath = filepath.Join(cfg.StateDir, "mnt", fmt.Sprintf("slot%d", profile.MainSlot), "secd", "unlock-times.json")

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
	go s.checkinReminderLoop() // evening ask, once daily, silent if already answered
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
	mux.HandleFunc("/v1/frames/latest", s.handleFramesLatest)        // where-was-I for the app's sync cursor
	mux.HandleFunc("/v1/frames/list", s.handleFramesList)            // gallery paging, newest first
	mux.HandleFunc("/v1/frames/thumb", s.handleFrameThumb)           // one thumbnail's bytes
	mux.HandleFunc("/v1/frames/preview", s.handleFramePreview)       // full-size for the pinch-zoom viewer
	mux.HandleFunc("/v1/frames/original", s.handleFrameOriginal)     // untouched archive bytes, mime-typed
	mux.HandleFunc("/v1/frames/exists", s.handleFramesExists)        // pre-upload dedup by content hash
	mux.HandleFunc("/v1/frames/kinds", s.handleFramesKinds)          // photo or video, for a strip of hashes
	mux.HandleFunc("/v1/sync/cursor", s.handleSyncCursor)            // device sync position, survives reinstall
	mux.HandleFunc("/v1/frames/tag", s.handleFrameTag)               // user tag corrections (tombstoned removes)
	mux.HandleFunc("/v1/services/summary", s.handleServicesSummary)  // latest sample + 24h blob per target
	mux.HandleFunc("/v1/services/detail", s.handleServiceDetail)     // ring buffers for one target (sparklines)
	mux.HandleFunc("/v1/chats", s.handleChatsList)                   // persisted conversations: list + search + paging
	mux.HandleFunc("/v1/chats/messages", s.handleChatMessages)       // one conversation's history, paged
	mux.HandleFunc("/v1/chats/rename", s.handleChatRename)           // the person's title outranks the derived one
	mux.HandleFunc("/v1/chats/delete", s.handleChatDelete)           // real deletion: rows gone, not flagged
	mux.HandleFunc("/v1/frames/geo", s.handleFramesGeo)              // GPS frames as dots, for the map
	mux.HandleFunc("/v1/frames/search", s.handleFramesSearch)        // place + name + tags, AND per term
	mux.HandleFunc("/v1/geo/world", s.handleGeoWorld)                // landmass GeoJSON, ?res= picks a cut
	mux.HandleFunc("/v1/geo/world/index", s.handleGeoWorldIndex)     // which cuts exist (open small, refine big)
	mux.HandleFunc("/v1/geo/landtiles/index", s.handleLandTileIndex) // high-res coast: which 1° cells are water, coast, land
	mux.HandleFunc("/v1/geo/landtile", s.handleLandTile)             // one coast cell (?x=&y=), fetched only when zoomed in over it
	mux.HandleFunc("/v1/geo/labels", s.handleGeoLabels)              // the names on the map for a view, best first
	mux.HandleFunc("/v1/geo/roadtiles/index", s.handleRoadTileIndex) // roads: which cells have a tile, both grids
	mux.HandleFunc("/v1/geo/countries", s.handleCountries)           // whole countries: what the box holds for each
	mux.HandleFunc("/v1/fetch/list", s.handleFetchList)              // what the phone fetches for the box (feeds, tickers)
	mux.HandleFunc("/v1/phone/net", s.handlePhoneNet)                // the phone's network: Wi-Fi means it fetches, else the box does
	mux.HandleFunc("/v1/news/fetched", s.handleNewsFetched)          // the feeds' bytes, spooled for synthd
	mux.HandleFunc("/v1/rates/fetched", s.handleRatesFetched)        // the tickers' bodies, spooled for tallyd
	mux.HandleFunc("/v1/news", s.handleNews)                         // the stories, for the NEWS screen
	mux.HandleFunc("/v1/news/brief", s.handleNewsBrief)              // the day's brief written now (home's button)
	mux.HandleFunc("/v1/news/feeds", s.handleNewsFeeds)              // the feeds, listed and changed (add, remove, on/off)
	mux.HandleFunc("/v1/weather", s.handleWeather)                   // the forecast nearest a position, from the box's daily pull
	mux.HandleFunc("/v1/weather/metoffice", s.handleWeatherMetOffice) // the Met Office key and order for the box's own forecast
	mux.HandleFunc("/v1/sources", s.handleSources)                   // what the box draws on, each with its state
	mux.HandleFunc("/v1/sources/fetch", s.handleSourcesFetch)        // update.sh <step> from the mirror, started from the phone
	mux.HandleFunc("/v1/rates", s.handleRates)                       // the ECB table, the index per symbol, the rank list
	mux.HandleFunc("/v1/rates/fast", s.handleRatesFast)              // BTC, ETH, SOL every five seconds, from Redis
	mux.HandleFunc("/v1/rates/sparks", s.handleRatesSparks)          // a week of hourly closes per coin, for CRYPTO
	mux.HandleFunc("/v1/coins/info", s.handleCoinInfo)               // one coin's page
	mux.HandleFunc("/v1/home", s.handleHome)                         // home's numbers, the brief and FOR YOU
	mux.HandleFunc("/v1/about", s.handleAbout)                       // the note about me and my people
	mux.HandleFunc("/v1/rates/history", s.handleRatesHistory)        // a symbol's daily closes or a currency's daily rate
	mux.HandleFunc("/v1/rates/series", s.handleRatesSeries)          // every minute for a week, every hour for thirty days
	mux.HandleFunc("/v1/feeds/status", s.handleFeedsStatus)          // how each feed is doing, for Box Status
	mux.HandleFunc("/v1/geo/country", s.handleCountry)               // one country's tiles, as index keys
	mux.HandleFunc("/v1/geo/at", s.handleAt)                         // the country a point is in, for the phrases
	mux.HandleFunc("/v1/geo/roadtile", s.handleRoadTile)             // one road cell (?l=&x=&y=)
	mux.HandleFunc("/v1/daemon/summary", s.handleDaemonSummary)      // per-daemon drill-in
	mux.HandleFunc("/v1/pipeline", s.handlePipeline)                 // stage-by-stage archive progress + ETA
	mux.HandleFunc("/v1/frames/geo/lod", s.handleFramesGeoLOD)       // 4-level map aggregation
	mux.HandleFunc("/v1/frames/newest", s.handleFramesNewest)        // map's opening view
	mux.HandleFunc("/v1/geo/days", s.handleGeoDays)                  // which day tracks exist
	mux.HandleFunc("/v1/geo/tracks", s.handleGeoTracks)              // newest N day tracks in one answer
	mux.HandleFunc("/v1/geo/route", s.handleGeoRoute)                // one day as stays and moves along the streets (?d=)
	mux.HandleFunc("/v1/geo/day", s.handleGeoDay)                    // one day's track, as framed wrote it          // operator-provided base-map GeoJSON
	mux.HandleFunc("/v1/memories", s.handleMemories)                 // the distilled corpus, live rows
	mux.HandleFunc("/v1/memories/delete", s.handleMemoryDelete)      // tombstone: deletion outranks the model
	mux.HandleFunc("/v1/memories/add", s.handleMemoryAdd)            // user-authored, sovereign from birth
	mux.HandleFunc("/v1/memories/edit", s.handleMemoryEdit)          // the person's version IS the memory
	mux.HandleFunc("/v1/taste", s.handleTaste)                       // what the photos say you like (synthd's outing pass)
	mux.HandleFunc("/v1/nearby", s.handleNearby)                     // places around you that fit the taste, from the box's own geo data
	mux.HandleFunc("/v1/notes", s.handleNoteAdd)                     // app -> noted inbox -> journal
	mux.HandleFunc("/v1/voice", s.handleVoiceUpload)                 // a voice note's WAV -> voiced inbox (idempotent by id)
	mux.HandleFunc("/v1/voice/notes", s.handleVoiceNotes)            // the notes with transcripts and status
	mux.HandleFunc("/v1/voice/audio", s.handleVoiceAudio)            // one note's audio (?id=), Range works
	mux.HandleFunc("/v1/voice/delete", s.handleVoiceDelete)          // audio, row and journal entry gone
	mux.HandleFunc("/v1/voice/ask", s.handleVoiceAsk)                // a question asked aloud: the words back, nothing kept
	mux.HandleFunc("/v1/onthisday", s.handleOnThisDay)               // synthd's retrospective, from the prebuilt days
	mux.HandleFunc("/v1/wiki", s.handleWiki)                         // the box's Wikipedia (in Postgres): state, a search (?q=), an article (?idx=)
	mux.HandleFunc("/v1/days", s.handleDays)                         // the prebuilt day summaries, newest first (?before&limit)
	mux.HandleFunc("/v1/day", s.handleDay)                           // one day's summary (?d=YYYY-MM-DD&build=1 writes it now, after the check-in)
	mux.HandleFunc("/v1/devices/name", s.handleDeviceName)           // a device names itself
	mux.HandleFunc("/v1/devices/retire", s.handleDeviceRetire)       // a sibling phone refused from now on
	mux.HandleFunc("/v1/devices", s.handleDevices)                   // enrolled phones, honest stats
	mux.HandleFunc("/v1/sync/reset", s.handleSyncReset)              // rewind this device's cursors
	mux.HandleFunc("/v1/checkins", s.handleCheckins)                 // past check-ins, newest first
	mux.HandleFunc("/v1/day/summary", s.handleDaySummary)            // one day at a glance, check-in prefill
	mux.HandleFunc("/v1/health/upload", s.handleHealthUpload)        // Health Connect readout -> tallyd inbox
	mux.HandleFunc("/v1/health/diag", s.handleHealthDiag)            // what the phone could read of Health Connect, and why not
	// NOTE: bare /v1/health was ALREADY the box health endpoint , registering the upload there
	// too made mux panic at startup and secd die before binding. Route names are a namespace;
	// grep before you claim one.
	mux.HandleFunc("/v1/health/stats", s.handleHealthStats) // daily series per metric
	mux.HandleFunc("/v1/health/day", s.handleHealthDay)     // one day's metrics and heart rate samples
	mux.HandleFunc("/v1/chat", s.handleChat)                // ask the box's model (via synthd's retrieval seam)
	mux.HandleFunc("/v1/chat/stop", s.handleChatStop)       // STOP: ends the answer being written for a chat (closing the app does not)
	mux.HandleFunc("/v1/chat/plan", s.handleChatPlan)       // what the question needs from the web, from the model, before the phone searches
	mux.HandleFunc("/v1/locations", s.handleLocations)
	mux.HandleFunc("/v1/geo/trail/answer", s.handleTrailAnswer)      // "were you there?" answered: keep, or delete the points
	mux.HandleFunc("/v1/geo/trail/forget", s.handleTrailForget)      // one wrong fix on the map, and its neighbours at the spot
	mux.HandleFunc("/v1/trail/key", s.handleTrailKey)                // the phone's trail key, kept in the vault
	mux.HandleFunc("/v1/update", s.handleUpdate)                     // what runs, and a release on trial
	mux.HandleFunc("/v1/update/file", s.handleUpdateFile)            // one file of the signed server set, from the phone
	mux.HandleFunc("/v1/update/apply", s.handleUpdateApply)          // verify, put on, lock and restart
	mux.HandleFunc("/v1/update/switch", s.handleUpdateSwitch)        // a release from the shelf back on
	mux.HandleFunc("/v1/update/rollback", s.handleUpdateRollback)    // the earlier build back
	mux.HandleFunc("/v1/device/rekey", s.handleRekey)                // the phone's own key, a new certificate for it
	mux.HandleFunc("/v1/device/rekey/confirm", s.handleRekeyConfirm) // over the new one: the QR's retires
	mux.HandleFunc("/v1/model", s.handleModel)                       // the box model: ready, or loading and how far
	mux.HandleFunc("/v1/models", s.handleModels)
	mux.HandleFunc("/v1/models/", s.handleModelBytes) // /v1/models/{id}
	mux.HandleFunc("/v1/openapi.json", s.handleOpenAPI)
	// The front door (edge.go): who is asking, from the phone's own TLS or the old nginx header; a
	// certificate replaced by a rotation (rekey.go) reaches nothing, the unlock included; the first
	// verified request marks enrolment done for provisioning (enrolflag.go).
	return logRequests(s.front(mux))
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

	s.closeDoors()
	defer s.closing.Store(false)
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
	s.closeDoors() // stays closed: the process is ending
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
