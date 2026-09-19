package secd

// Photo and location intake. secd stays THIN here on purpose: it authenticates the session, streams
// bytes to ghost.framed's intake folder, and never decodes an image or parses a coordinate. All
// interpretation happens in ghost.framed, on the volume, behind the front door. That keeps secd (the
// one root, network-facing component) free of image parsers , historically one of the most
// exploit-rich code families you can put in front of untrusted input , and keeps the linear trust
// story: the network reaches exactly one small program, and that program only moves bytes.
//
// Write protocol shared with framed: stream to <name>.part, fsync, rename. framed skips *.part, so a
// half-written upload is never processed; the rename is the commit.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"sort"
	"strings"
	"time"
)

// uploadMaxBytes bounds one camera upload. Photos are tens of MB; VIDEOS are the sizing case , a 4K
// phone clip runs ~400MB a minute, so 4GiB gives multi-minute headroom. The body is STREAMED to the
// encrypted volume (io.Copy, never held in RAM), so a big cap costs nothing in memory; the real bound
// is volume disk, and only enrolled, session-authenticated devices reach this handler at all.
const uploadMaxBytes = 4 << 30

// locationsMaxBytes bounds one location batch. A day of 1Hz samples is ~5MB of JSON; 16MB is generous.
const locationsMaxBytes = 16 << 20

// handleFrameUpload accepts one raw image per POST and spools it for ghost.framed. The body is the
// image bytes, exactly as shot , no multipart, no re-encode, no inspection. framed archives the same
// bytes it receives here.
func (s *Server) handleFrameUpload(w http.ResponseWriter, r *http.Request) {
	// Every rejection logs its reason server-side. The WIRE response stays the uniform appears-down
	// 503 (no information leaks to the caller), but the journal must say why an upload bounced ,
	// a silent 503 here cost a debugging session: the app just sees "failed" and the box said nothing.
	if !s.session.Valid(bearer(r)) {
		hasBearer := bearer(r) != ""
		secdLog.Warn("frame upload rejected: invalid session", "fn", "handleFrameUpload", "bearerPresent", hasBearer, "remote", r.RemoteAddr)
		s.appearsDown(w)
		return
	}
	if r.Method != http.MethodPost {
		secdLog.Warn("frame upload rejected: wrong method", "fn", "handleFrameUpload", "method", r.Method)
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		secdLog.Warn("frame upload rejected: box locked", "fn", "handleFrameUpload")
		s.appearsDown(w) // locked box takes nothing; the app queues and retries after unlock
		return
	}
	dir := filepath.Join(s.cfg.StateDir, "mnt", fmt.Sprintf("slot%d", mounted), "frames", "incoming")
	t0 := time.Now()
	uid, gid := s.spoolCred()
	n, err := spoolBody(dir, r, uploadMaxBytes, uid, gid)
	if err != nil {
		secdLog.Warn("frame upload spool failed", "fn", "handleFrameUpload", "dir", dir, "err", err)
		http.Error(w, "upload failed", http.StatusInsufficientStorage)
		return
	}
	secdLog.Info("frame spooled", "fn", "handleFrameUpload", "bytes", n, "took", time.Since(t0).String())
	w.WriteHeader(http.StatusAccepted) // accepted for processing; framed does the rest asynchronously
}

// spoolCred resolves the run user's uid/gid once (cached) for spool-file handoff. (0,0) when no run
// user is configured , spool files then stay with secd's owner, correct for single-user dev setups.
func (s *Server) spoolCred() (int, int) {
	s.credOnce.Do(func() {
		if s.cfg.RunUser == "" {
			return
		}
		u, err := user.Lookup(s.cfg.RunUser)
		if err != nil {
			secdLog.Warn("run user lookup failed , spool files will stay root-owned", "fn", "spoolCred", "user", s.cfg.RunUser, "err", err)
			return
		}
		s.credUID, _ = strconv.Atoi(u.Uid)
		s.credGID, _ = strconv.Atoi(u.Gid)
	})
	return s.credUID, s.credGID
}

// handleFramesLatest reports the newest taken_at per kind on the box , the app's "where was I". The
// phone seeds its sync cursor from this, so a killed or reinstalled app resumes from what the box
// actually has instead of re-offering the whole roll.
func (s *Server) handleFramesLatest(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) {
		secdLog.Warn("frames/latest rejected: invalid session", "fn", "handleFramesLatest", "bearerPresent", bearer(r) != "")
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	photoTs, videoTs, err := s.notif.FramesLatest(mounted)
	if err != nil {
		secdLog.Warn("frames/latest query failed", "fn", "handleFramesLatest", "err", err)
		s.appearsDown(w)
		return
	}
	// Merge the stored device cursor: content-derived trust (exif/hint rows) OR the position the
	// phone last reported , whichever is further. This is what makes an app reinstall RESUME instead
	// of re-walking the roll: the phone's local cursor died with its prefs, the box's copy did not.
	// NOTE units: frames store SECONDS, the device cursor stores the phone's MILLISECONDS , convert
	// the frame side up rather than truncating the cursor down.
	photoMs, videoMs := photoTs*1000, videoTs*1000
	if cur, cerr := s.notif.CursorGet(mounted, "default"); cerr == nil {
		if ts := cur["photo"]; ts > photoMs {
			photoMs = ts
		}
		if ts := cur["video"]; ts > videoMs {
			videoMs = ts
		}
	}
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"photoTakenAt":%d,"videoTakenAt":%d}`, photoMs/1000, videoMs/1000)
}

// handleSyncCursor , the phone reports its confirmed position after each run; the box remembers it
// per device so a reinstall resumes instead of restarting. POST {"kind":"photo","ts":ms,"id":n}.
func (s *Server) handleSyncCursor(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) {
		s.appearsDown(w)
		return
	}
	if r.Method == http.MethodGet {
		s.handleSyncCursorGet(w, r)
		return
	}
	if r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	var req struct {
		Kind string `json:"kind"`
		TS   int64  `json:"ts"`
		ID   int64  `json:"id"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil || (req.Kind != "photo" && req.Kind != "video") {
		s.appearsDown(w)
		return
	}
	if err := s.notif.CursorSet(mounted, deviceKey(r), req.Kind, req.TS, req.ID); err != nil {
		secdLog.Warn("cursor store failed", "fn", "handleSyncCursor", "err", err)
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// handleSyncCursorGet , THE cursor authority. The phone keeps NO persistent cursor: it asks here at
// the start of every run, gets {kind:{ts,id}}, skips everything at-or-before, and reports progress
// back via POST. One source of truth ends the split-brain class of sync bugs (reinstalls, cleared
// prefs, diverged local state). ts is MILLISECONDS (the phone's MediaStore unit); the content-trust
// side (exif/hint frames, stored in seconds) is scaled up and merged , id 0 on that side, which the
// tuple comparison treats as "everything with this exact ts re-offers once", and the hash dedup
// absorbs that overlap for free.
// handleSyncCursorGet , THE cursor authority, PER DEVICE. The phone keeps no persistent cursor:
// it asks here every run and gets ITS OWN stored position , a fresh device (a partner's phone)
// gets (0,0) and offers its whole library; a reinstall on the same device keeps the same client
// cert, so the same key, so it resumes. The old global merge against the ARCHIVE's newest frame
// was the single-device era talking: it handed a second phone the FIRST phone's position, and her
// library , older than his newest photo , offered exactly one recent video. Account-global state
// answering a device-scoped question is the bug class; the stored per-device row is the whole
// answer. ts stays MILLISECONDS (MediaStore's unit); (0,0) on any miss, and hash dedup makes
// over-offering free.
func (s *Server) handleSyncCursorGet(w http.ResponseWriter, r *http.Request) {
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	full, err := s.notif.CursorGetFull(mounted, deviceKey(r))
	if err != nil {
		secdLog.Warn("cursor get failed", "fn", "handleSyncCursorGet", "err", err)
		s.appearsDown(w)
		return
	}
	type kcur struct {
		TS  int64  `json:"ts"`
		ID  int64  `json:"id"`
		Src string `json:"src"`
	}
	out := map[string]kcur{}
	for _, kind := range []string{"photo", "video"} {
		c := kcur{Src: "none"}
		if v, ok := full[kind]; ok {
			c.TS, c.ID, c.Src = v.TS, v.ID, v.Src
		}
		out[kind] = c
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(out)
}
func (s *Server) handleFrameTag(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	var req struct {
		Hash   string `json:"hash"`
		Tag    string `json:"tag"`
		Action string `json:"action"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.appearsDown(w)
		return
	}
	if len(req.Hash) != 32 || !isHex32(req.Hash) || (req.Action != "add" && req.Action != "remove") {
		s.appearsDown(w)
		return
	}
	tag := strings.ToLower(strings.TrimSpace(req.Tag))
	if len(tag) < 2 || len(tag) > 24 || strings.ContainsAny(tag, "\n\t'\"") {
		s.appearsDown(w)
		return
	}
	if err := s.notif.TagSet(mounted, req.Hash, tag, req.Action); err != nil {
		secdLog.Warn("tag set failed", "fn", "handleFrameTag", "err", err)
		s.appearsDown(w)
		return
	}
	secdLog.Info("tag corrected", "fn", "handleFrameTag", "action", req.Action)
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(`{"ok":true}`))
}

// isHex32 , exactly 32 lowercase hex chars, the frame identity format everywhere.
func isHex32(h string) bool {
	for _, ch := range h {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return false
		}
	}
	return len(h) == 32
}

// handleFramesExists , the pre-upload existence check. POST {"hashes":[...]} -> {"have":[...]}. The
// phone skips streaming any file whose content hash the box confirms , turning a re-offered 400MB
// video into a few bytes of request. Hashes are STRICT lowercase hex (the only user input reaching a
// SQL IN list, validated here to the character); anything malformed is dropped, not queried.
func (s *Server) handleFramesExists(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) {
		secdLog.Warn("frames/exists rejected: invalid session", "fn", "handleFramesExists", "bearerPresent", bearer(r) != "")
		s.appearsDown(w)
		return
	}
	if r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	var req struct {
		Hashes []string `json:"hashes"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		s.appearsDown(w)
		return
	}
	clean := make([]string, 0, len(req.Hashes))
	for _, h := range req.Hashes {
		if len(h) != 32 {
			continue
		}
		ok := true
		for _, ch := range h {
			if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
				ok = false
				break
			}
		}
		if ok {
			clean = append(clean, h)
		}
	}
	have, err := s.notif.FramesHave(mounted, clean)
	if err != nil {
		secdLog.Warn("frames/exists query failed", "fn", "handleFramesExists", "err", err)
		s.appearsDown(w)
		return
	}
	out := make([]string, 0, len(have))
	for h := range have {
		out = append(out, h)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string][]string{"have": out})
}

// handleFramesList pages the archived frames newest-first for the app's gallery grid.
// GET /v1/frames/list?before=<takenAt>&limit=<n> -> {"frames":[{hash,takenAt,kind,bytes}]}
func (s *Server) handleFramesList(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	before, _ := strconv.ParseInt(r.URL.Query().Get("before"), 10, 64)
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rowsOut, err := s.notif.FramesList(mounted, before, limit)
	if err != nil {
		secdLog.Warn("frames/list query failed", "fn", "handleFramesList", "err", err)
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"frames": rowsOut})
}

// handleFrameThumb streams one thumbnail's bytes. GET /v1/frames/thumb?hash=<hash>.
// The hash is validated hex , it is the only user input that touches a filesystem path.
func (s *Server) handleFrameThumb(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	hash := r.URL.Query().Get("hash")
	if len(hash) < 16 || len(hash) > 128 {
		s.appearsDown(w)
		return
	}
	for _, ch := range hash {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			s.appearsDown(w) // not lowercase hex -> not one of our hashes; no path characters ever
			return
		}
	}
	path, err := s.notif.FrameThumbPath(mounted, hash)
	if err != nil || path == "" {
		s.appearsDown(w)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		secdLog.Warn("thumb open failed", "fn", "handleFrameThumb", "path", path, "err", err)
		s.appearsDown(w)
		return
	}
	defer f.Close()
	if strings.HasSuffix(path, ".webp") {
		w.Header().Set("Content-Type", "image/webp")
	} else {
		w.Header().Set("Content-Type", "image/jpeg")
	}
	_, _ = io.Copy(w, f)
}

// handleLocations accepts a JSON batch of location points ({"source":..,"points":[{ts,lat,lon}..]})
// and spools it. secd does not parse it; framed validates and drops malformed batches.
// handleFramePreview , GET /v1/frames/preview?hash= , the large derived JPEG for full-screen
// viewing and pinch-zoom. Same hash validation and appears-down discipline as the thumb.
func (s *Server) handleFramePreview(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	hash := r.URL.Query().Get("hash")
	if len(hash) < 16 || len(hash) > 128 {
		s.appearsDown(w)
		return
	}
	for _, ch := range hash {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			s.appearsDown(w) // not lowercase hex -> not one of our hashes; no path characters ever
			return
		}
	}
	path, err := s.notif.FramePreviewPath(mounted, hash)
	if err != nil || path == "" {
		s.appearsDown(w)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		secdLog.Warn("preview open failed", "fn", "handleFramePreview", "path", path, "err", err)
		s.appearsDown(w)
		return
	}
	defer f.Close()
	if strings.HasSuffix(path, ".webp") {
		w.Header().Set("Content-Type", "image/webp")
	} else {
		w.Header().Set("Content-Type", "image/jpeg")
	}
	_, _ = io.Copy(w, f)
}

// handleLocations accepts a JSON batch of location points ({"source":..,"points":[{ts,lat,lon}..]})
// and spools it. secd does not parse it; framed validates and drops malformed batches.
// handleFrameOriginal , GET /v1/frames/original?hash= , the UNTOUCHED archived bytes, mime-typed.
// The viewer asks for this first; preview and thumb are the fallbacks, not the ceiling.
func (s *Server) handleFrameOriginal(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	hash := r.URL.Query().Get("hash")
	if len(hash) < 16 || len(hash) > 128 {
		s.appearsDown(w)
		return
	}
	for _, ch := range hash {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			s.appearsDown(w) // not lowercase hex -> not one of our hashes; no path characters ever
			return
		}
	}
	path, mime, err := s.notif.FrameOriginalPath(mounted, hash)
	if err != nil || path == "" {
		s.appearsDown(w)
		return
	}
	f, err := os.Open(path)
	if err != nil {
		secdLog.Warn("original open failed", "fn", "handleFrameOriginal", "path", path, "err", err)
		s.appearsDown(w)
		return
	}
	defer f.Close()
	if mime != "" {
		w.Header().Set("Content-Type", mime)
	} else {
		w.Header().Set("Content-Type", "application/octet-stream")
	}
	// ServeContent, not Copy: RANGE requests are how video players seek , probe the moov atom at
	// the tail, jump to minute 7, resume , and ServeContent speaks 206/Content-Range/If-Range
	// natively given a ReadSeeker. The whole streaming-with-seek feature server-side is this one
	// call. Empty name: the mime header above already decided the type, no sniffing wanted.
	st, serr := f.Stat()
	if serr != nil {
		s.appearsDown(w)
		return
	}
	http.ServeContent(w, r, "", st.ModTime(), f)
}

// handleLocations accepts a JSON batch of location points ({"source":..,"points":[{ts,lat,lon}..]})
// and spools it. secd does not parse it; framed validates and drops malformed batches.
func (s *Server) handleLocations(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) {
		secdLog.Warn("location upload rejected: invalid session", "fn", "handleLocations", "bearerPresent", bearer(r) != "", "remote", r.RemoteAddr)
		s.appearsDown(w)
		return
	}
	if r.Method != http.MethodPost {
		secdLog.Warn("location upload rejected: wrong method", "fn", "handleLocations", "method", r.Method)
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		secdLog.Warn("location upload rejected: box locked", "fn", "handleLocations")
		s.appearsDown(w)
		return
	}
	dir := filepath.Join(s.cfg.StateDir, "mnt", fmt.Sprintf("slot%d", mounted), "frames", "incoming-locations")
	uid, gid := s.spoolCred()
	n, err := spoolBody(dir, r, locationsMaxBytes, uid, gid)
	if err != nil {
		secdLog.Warn("location spool failed", "fn", "handleLocations", "dir", dir, "err", err)
		http.Error(w, "upload failed", http.StatusInsufficientStorage)
		return
	}
	secdLog.Info("locations spooled", "fn", "handleLocations", "bytes", n)
	w.WriteHeader(http.StatusAccepted)
}

// spoolBody streams the request body to a fresh .part file in dir, fsyncs, and renames it live. The
// name is arrival-ordered (nanosecond timestamp) plus random hex so concurrent uploads never collide.
func spoolBody(dir string, r *http.Request, maxBytes int64, uid, gid int) (int64, error) {
	if err := os.MkdirAll(dir, 0o750); err != nil {
		return 0, fmt.Errorf("create spool dir: %w", err)
	}
	var rb [6]byte
	_, _ = rand.Read(rb[:])
	name := fmt.Sprintf("%d-%s", time.Now().UnixNano(), hex.EncodeToString(rb[:]))
	// Taken-timestamp HINT from the phone (X-Ghost-Taken, epoch millis). Folded into the spool
	// filename as a -t suffix , secd still never parses content, and framed treats it as the
	// FALLBACK taken time when the bytes carry none (videos have no EXIF; their container metadata
	// parser is future work). Digits-only sanitisation: a hostile header cannot inject a path.
	if taken := r.Header.Get("X-Ghost-Taken"); taken != "" {
		clean := true
		for _, ch := range taken {
			if ch < '0' || ch > '9' {
				clean = false
				break
			}
		}
		if clean && len(taken) <= 20 {
			name += "-t" + taken
		}
	}
	// PROVENANCE , which phone sent this. Folded in as -d<hex> the same way the taken hint is:
	// secd still never parses content, framed reads the suffix and stores it on the frame. This
	// matters more every month: the plan is for photos to leave the phones entirely, and an
	// archive that cannot say WHERE a memory came from has already lost half of it.
	if dev := deviceKeyFromRequest(r); dev != "" {
		name += "-d" + dev
	}
	part := filepath.Join(dir, name+".part")
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o640)
	if err != nil {
		return 0, fmt.Errorf("create .part: %w", err)
	}
	body := http.MaxBytesReader(nil, r.Body, maxBytes)
	n, err := io.Copy(f, body)
	if err != nil {
		_ = f.Close()
		_ = os.Remove(part)
		return 0, fmt.Errorf("stream body (after %d bytes): %w", n, err)
	}
	if err := f.Sync(); err != nil { // the bytes are the photo; make sure they hit the disk
		_ = f.Close()
		_ = os.Remove(part)
		return 0, fmt.Errorf("fsync: %w", err)
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(part)
		return 0, fmt.Errorf("close: %w", err)
	}
	final := filepath.Join(dir, name)
	if err := os.Rename(part, final); err != nil { // the commit
		return 0, fmt.Errorf("commit rename: %w", err)
	}
	// Hand the file to the run user AT COMMIT. secd runs as ROOT; without this every spool file lands
	// root:root 0640 and framed (the run user) cannot read a single one , the drain then retries the
	// whole backlog every tick, flooding its log while archiving nothing (observed: 35GB stuck, 7.3GB
	// of permission-denied lines). Root writes it, the cohort consumes it: ownership crosses here.
	if uid > 0 {
		if err := os.Chown(final, uid, gid); err != nil {
			secdLog.Warn("spool chown failed , framed cannot read this file", "fn", "spoolBody", "path", final, "err", err)
		}
		_ = os.Chown(dir, uid, gid)
	}
	return n, nil
}

// handleFramesGeo , GET /v1/frames/geo?minlat&maxlat&minlon&maxlon&limit , GPS-bearing frames as
// dots for the MAP. All parameters optional: no bbox means the whole world (capped). Same appears-
// down discipline as everything else.
func (s *Server) handleFramesGeo(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	qp := r.URL.Query()
	pf := func(k string, def float64) float64 {
		v, err := strconv.ParseFloat(qp.Get(k), 64)
		if err != nil {
			return def
		}
		return v
	}
	limit, _ := strconv.Atoi(qp.Get("limit"))
	pts, err := s.notif.FramesGeo(mounted,
		pf("minlat", -90), pf("maxlat", 90), pf("minlon", -180), pf("maxlon", 180), limit)
	if err != nil {
		secdLog.Warn("frames geo failed", "fn", "handleFramesGeo", "err", err)
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"points": pts})
}

// handleFramesSearch , GET /v1/frames/search?q=&limit , place + display-name + tag search, AND per
// term. "waterfall vancouver island" works the way a person means it.
func (s *Server) handleFramesSearch(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	q := r.URL.Query().Get("q")
	if len(q) > 160 {
		q = q[:160]
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rowsOut, err := s.notif.FramesSearch(mounted, q, limit)
	if err != nil {
		secdLog.Warn("frames search failed", "fn", "handleFramesSearch", "err", err)
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"frames": rowsOut})
}

// worldFileName maps the optional ?res= to a file under <mount>/geo: "" is world.geojson (the file
// every box has had), anything else is world-<res>.geojson (world-110m.geojson, world-50m.geojson ,
// the coarser Natural Earth cuts fetch_geo.sh now drops beside the 10m one). res is a short
// lowercase token or the request is refused , it becomes a filename.
func worldFileName(res string) (string, bool) {
	if res == "" {
		return "world.geojson", true
	}
	if len(res) > 12 {
		return "", false
	}
	for _, ch := range res {
		if (ch < 'a' || ch > 'z') && (ch < '0' || ch > '9') {
			return "", false
		}
	}
	return "world-" + res + ".geojson", true
}

func worldETag(fi os.FileInfo) string {
	return fmt.Sprintf("\"w-%d-%d\"", fi.ModTime().Unix(), fi.Size())
}

// handleGeoWorld , GET /v1/geo/world[?res=110m] , serves an operator-provided Natural Earth GeoJSON
// from the volume (<mount>/geo/world.geojson, or world-<res>.geojson) for the app's self-drawn base
// map. Absent file appears down , the map renders without landmass (graticule + tracks + dots), by
// design. /v1/geo/world/index says which cuts exist so the app can open on the small one and
// refine with the big one.
func (s *Server) handleGeoWorld(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	name, ok := worldFileName(r.URL.Query().Get("res"))
	if !ok {
		s.appearsDown(w)
		return
	}
	path := filepath.Join(s.cfg.StateDir, "mnt", fmt.Sprintf("slot%d", mounted), "geo", name)
	f, err := os.Open(path)
	if err != nil {
		s.appearsDown(w)
		return
	}
	defer f.Close()
	// ETag = mtime+size (cheap, changes exactly when the operator swaps the file). The app caches
	// the multi-MB world locally and revalidates with If-None-Match; a match costs a 304 and zero
	// bytes , the map opens instantly offline-first and updates only when the world actually does.
	fi, _ := f.Stat()
	if fi != nil {
		etag := worldETag(fi)
		w.Header().Set("ETag", etag)
		if r.Header.Get("If-None-Match") == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.Copy(w, f)
}

// handleGeoWorldIndex , GET /v1/geo/world/index , the landmass cuts on the volume: every
// world*.geojson under <mount>/geo with its res token, size and ETag. The app opens on the smallest
// (a 110m world is under a megabyte and draws in a blink) and refines with the largest once it is
// in; one file only means one entry and the old behaviour. No geo dir at all is an empty list, not
// appears-down , a young box with no landmass is a normal state the map already handles.
func (s *Server) handleGeoWorldIndex(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	type cut struct {
		Res   string `json:"res"`
		Name  string `json:"name"`
		Bytes int64  `json:"bytes"`
		ETag  string `json:"etag"`
	}
	cuts := []cut{}
	dir := filepath.Join(s.cfg.StateDir, "mnt", fmt.Sprintf("slot%d", mounted), "geo")
	if ents, err := os.ReadDir(dir); err == nil {
		for _, e := range ents {
			n := e.Name()
			if e.IsDir() || !strings.HasPrefix(n, "world") || !strings.HasSuffix(n, ".geojson") {
				continue
			}
			res := strings.TrimSuffix(strings.TrimPrefix(n, "world"), ".geojson")
			res = strings.TrimPrefix(res, "-")
			if _, ok := worldFileName(res); !ok {
				continue // a name the world endpoint would refuse is not offered
			}
			fi, ferr := e.Info()
			if ferr != nil {
				continue
			}
			cuts = append(cuts, cut{Res: res, Name: n, Bytes: fi.Size(), ETag: worldETag(fi)})
		}
	}
	sort.Slice(cuts, func(i, j int) bool { return cuts[i].Bytes < cuts[j].Bytes })
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"cuts": cuts})
}

// handleGeoDays , GET /v1/geo/days?limit=N , which day tracks exist (framed's RebuildDay output),
// newest first. The MAP uses this to know what it can draw.
func (s *Server) handleGeoDays(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 400 {
		limit = 60
	}
	dir := filepath.Join(s.cfg.StateDir, "mnt", fmt.Sprintf("slot%d", mounted), "paths")
	entries, err := os.ReadDir(dir)
	if err != nil {
		// No paths dir yet is a normal young-box state , empty list, not appears-down.
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"days": []string{}})
		return
	}
	days := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".geojson") {
			days = append(days, strings.TrimSuffix(e.Name(), ".geojson"))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days))) // YYYY-MM-DD sorts lexically
	if len(days) > limit {
		days = days[:limit]
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"days": days})
}

// handleGeoTracks , GET /v1/geo/tracks?limit=N , the newest N day tracks in ONE answer, as compact
// [lat,lon] polylines. The map used to open with /v1/geo/days and then one /v1/geo/day per day ,
// fifteen sequential mTLS round trips before the first track could draw. framed's day files already
// carry the Douglas-Peucker-simplified LineString; this reads them once and hands back only the
// line, not the photo Points the LOD feed already covers. Days with no track (photos only) are
// omitted. No paths dir is an empty list, a young box's normal state.
func (s *Server) handleGeoTracks(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	if limit <= 0 || limit > 120 {
		limit = 14
	}
	type track struct {
		Day    string       `json:"day"`
		Coords [][2]float64 `json:"coords"` // [lat, lon], the order the map speaks
	}
	out := []track{}
	dir := filepath.Join(s.cfg.StateDir, "mnt", fmt.Sprintf("slot%d", mounted), "paths")
	entries, err := os.ReadDir(dir)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"tracks": out})
		return
	}
	days := make([]string, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".geojson") {
			days = append(days, strings.TrimSuffix(e.Name(), ".geojson"))
		}
	}
	sort.Sort(sort.Reverse(sort.StringSlice(days)))
	for _, d := range days {
		if len(out) >= limit {
			break
		}
		if _, perr := time.Parse("2006-01-02", d); perr != nil {
			continue // only the files framed names; nothing else in this dir is a day
		}
		b, rerr := os.ReadFile(filepath.Join(dir, d+".geojson"))
		if rerr != nil {
			continue
		}
		var doc struct {
			Features []struct {
				Geometry struct {
					Type   string          `json:"type"`
					Coords json.RawMessage `json:"coordinates"`
				} `json:"geometry"`
			} `json:"features"`
		}
		if json.Unmarshal(b, &doc) != nil {
			continue
		}
		for _, f := range doc.Features {
			if f.Geometry.Type != "LineString" {
				continue
			}
			var lonlat [][2]float64
			if json.Unmarshal(f.Geometry.Coords, &lonlat) != nil || len(lonlat) < 2 {
				continue
			}
			t := track{Day: d, Coords: make([][2]float64, len(lonlat))}
			for i, c := range lonlat {
				t.Coords[i] = [2]float64{c[1], c[0]} // GeoJSON is lon,lat; the map wants lat,lon
			}
			out = append(out, t)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"tracks": out})
}

// handleGeoDay , GET /v1/geo/day?d=YYYY-MM-DD , one day's track GeoJSON, exactly as framed wrote it.
func (s *Server) handleGeoDay(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	d := r.URL.Query().Get("d")
	if _, err := time.Parse("2006-01-02", d); err != nil { // strict date = the path-traversal guard
		s.appearsDown(w)
		return
	}
	path := filepath.Join(s.cfg.StateDir, "mnt", fmt.Sprintf("slot%d", mounted), "paths", d+".geojson")
	f, err := os.Open(path)
	if err != nil {
		s.appearsDown(w)
		return
	}
	defer f.Close()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.Copy(w, f)
}

// handleDaySummary , GET /v1/day/summary?start=&end= (unix seconds, the CALLER's day bounds , the
// phone knows its timezone, the box does not guess). The check-in prefill and any "what did today
// look like" consumer.
func (s *Server) handleDaySummary(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	start, _ := strconv.ParseInt(r.URL.Query().Get("start"), 10, 64)
	end, _ := strconv.ParseInt(r.URL.Query().Get("end"), 10, 64)
	if start <= 0 || end <= start || end-start > 172800 {
		s.appearsDown(w)
		return
	}
	sum, err := s.notif.DayContext(mounted, start, end)
	if err != nil {
		secdLog.Warn("day summary failed", "fn", "handleDaySummary", "err", err)
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(sum)
}

// handleHealthUpload , POST /v1/health {"days":[{"day","metrics":{...}}]} , the phone's Health
// Connect readout, dropped as a batch into tallyd's inbox. Same one-path rule as notes.
func (s *Server) handleHealthUpload(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1024*1024)) // app chunks history by month; 1MB covers the densest month
	if err != nil || len(body) == 0 || !json.Valid(body) {
		s.appearsDown(w)
		return
	}
	inbox := filepath.Join(s.cfg.StateDir, "mnt", fmt.Sprintf("slot%d", mounted), "tallyd", "inbox")
	if err := os.MkdirAll(inbox, 0o750); err != nil {
		s.appearsDown(w)
		return
	}
	path := filepath.Join(inbox, fmt.Sprintf("health-%d.json", time.Now().UnixNano()))
	if err := os.WriteFile(path, body, 0o640); err != nil {
		s.appearsDown(w)
		return
	}
	if s.cfg.RunUser != "" {
		if u, uerr := user.Lookup(s.cfg.RunUser); uerr == nil {
			uid, _ := strconv.Atoi(u.Uid)
			gid, _ := strconv.Atoi(u.Gid)
			_ = os.Chown(path, uid, gid)
			_ = os.Chown(inbox, uid, gid)
		}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// handleHealthStats , GET /v1/health/stats?days=N , daily series per metric for the HEALTH screen.
func (s *Server) handleHealthStats(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("days"))
	series, err := s.notif.HealthStats(mounted, n)
	if err != nil {
		secdLog.Warn("health stats failed", "fn", "handleHealthStats", "err", err)
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"series": series})
}

// handleFramesGeoLOD , GET /v1/frames/geo/lod?level=0..3&minlat&maxlat&minlon&maxlon , the map's
// level-of-detail feed. Postgres aggregates; the app ships a viewport and a level and gets back a
// few hundred cells instead of every point on the planet.
func (s *Server) handleFramesGeoLOD(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	qp := r.URL.Query()
	pf := func(k string, def float64) float64 {
		v, err := strconv.ParseFloat(qp.Get(k), 64)
		if err != nil {
			return def
		}
		return v
	}
	level, _ := strconv.Atoi(qp.Get("level"))
	if level < 0 || level > 3 {
		level = 0
	}
	minLat, maxLat := pf("minlat", -90), pf("maxlat", 90)
	minLon, maxLon := pf("minlon", -180), pf("maxlon", 180)
	pts, err := s.notif.FramesGeoLOD(mounted, level, minLat, maxLat, minLon, maxLon, 0)
	if err != nil {
		secdLog.Warn("geo lod failed", "fn", "handleFramesGeoLOD", "err", err)
		s.appearsDown(w)
		return
	}
	// INSTRUMENTED , four rounds of map debugging died on silence: a successful handler logged
	// nothing, so "request never arrived" and "request answered empty" looked identical from
	// the outside. Now one map-open tells the whole story in one line.
	secdLog.Info("geo lod", "fn", "handleFramesGeoLOD", "level", level,
		"minlat", minLat, "maxlat", maxLat, "minlon", minLon, "maxlon", maxLon, "points", len(pts))
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"level": level, "points": pts})
}

// handleFramesNewest , GET /v1/frames/newest , the newest geotagged frame; the map opens here.
func (s *Server) handleFramesNewest(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	g, err := s.notif.NewestGeoFrame(mounted)
	if err != nil {
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(g)
}

// handleDaemonSummary , GET /v1/daemon/summary?name=ghost.framed , the per-daemon drill-in feed.
func (s *Server) handleDaemonSummary(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	name := r.URL.Query().Get("name")
	if len(name) > 40 {
		s.appearsDown(w)
		return
	}
	kv, err := s.notif.DaemonSummary(mounted, name)
	if err != nil {
		secdLog.Warn("daemon summary failed", "fn", "handleDaemonSummary", "name", name, "err", err)
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"name": name, "rows": kv})
}

// handlePipeline , GET /v1/pipeline , the archive's stage-by-stage progress, searchd's queue,
// the description rate and ETA, and framed's live stock-take. The Box Status screen polls it.
func (s *Server) handlePipeline(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	p, err := s.notif.PipelineProgress(mounted)
	if err != nil {
		secdLog.Warn("pipeline progress failed", "fn", "handlePipeline", "err", err)
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(p)
}

// handleCheckins , GET /v1/checkins?days=N , past daily check-ins for the MEMORIES strip.
func (s *Server) handleCheckins(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	n, _ := strconv.Atoi(r.URL.Query().Get("days"))
	rows, err := s.notif.CheckinHistory(mounted, n)
	if err != nil {
		secdLog.Warn("checkins failed", "fn", "handleCheckins", "err", err)
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"checkins": rows})
}

// handleSyncReset , POST /v1/sync/reset , rewind THIS device's cursors to zero. The next sync run
// re-offers everything; dedup absorbs what the box already holds, so the cost is time, never
// duplicates. Per-device: a second phone (a partner's, say) rewinding does not disturb the first.
func (s *Server) handleSyncReset(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	device := deviceKey(r)
	if err := s.notif.ResetSyncCursors(mounted, device); err != nil {
		secdLog.Warn("sync reset failed", "fn", "handleSyncReset", "err", err)
		s.appearsDown(w)
		return
	}
	secdLog.Info("sync cursors reset", "fn", "handleSyncReset", "device", device)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"reset": true})
}

// handleDevices , GET /v1/devices , the enrolled phones and what each has actually done. Replaces
// a screen that displayed INVENTED devices with counts nobody measured: the cursor rows are the
// only honest source, and they answer last-sync, last-photo and last-video exactly.
func (s *Server) handleDevices(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	rows, err := s.notif.DevicesInfo(mounted)
	if err != nil {
		secdLog.Warn("devices failed", "fn", "handleDevices", "err", err)
		s.appearsDown(w)
		return
	}
	me := deviceKey(r)
	for i := range rows {
		rows[i].This = rows[i].Device == me
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"devices": rows})
}

// handleDeviceName , POST /v1/devices/name {"name":"...","model":"..."} , the calling device
// names ITSELF (identity comes from its certificate, so no device can rename another).
func (s *Server) handleDeviceName(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		s.appearsDown(w)
		return
	}
	var req struct {
		Name     string `json:"name"`
		Model    string `json:"model"`
		StableID string `json:"stableId"`
		Device   string `json:"device"` // optional: name ANOTHER enrolled phone (same archive)
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil {
		s.appearsDown(w)
		return
	}
	if len(req.Name) > 40 {
		req.Name = req.Name[:40]
	}
	if len(req.Model) > 40 {
		req.Model = req.Model[:40]
	}
	target := deviceKey(r)
	if req.Device != "" {
		// Naming a sibling phone is allowed , every enrolled device already shares this archive,
		// so a friendly label is bookkeeping, not privilege. The STABLE ID, though, is only ever
		// claimed for the caller: identity adoption stays tied to the certificate presenting it.
		target = req.Device
		req.StableID = ""
	}
	if err := s.notif.DeviceNameSet(mounted, target, req.Name, req.Model, req.StableID); err != nil {
		secdLog.Warn("device name failed", "fn", "handleDeviceName", "err", err)
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// deviceKeyFromRequest , the calling phone's stable key, hex only (a spool name must never be
// able to carry a path separator or anything else surprising).
func deviceKeyFromRequest(r *http.Request) string {
	k := deviceKey(r)
	for _, ch := range k {
		if (ch < '0' || ch > '9') && (ch < 'a' || ch > 'f') {
			return ""
		}
	}
	if len(k) > 32 {
		return ""
	}
	return k
}
