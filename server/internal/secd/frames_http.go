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
	w.Header().Set("Content-Type", "application/json")
	fmt.Fprintf(w, `{"photoTakenAt":%d,"videoTakenAt":%d}`, photoTs, videoTs)
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
