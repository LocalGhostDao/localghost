package secd

// VOICE NOTES , the phone's recordings in, the list and the audio out, deletion on the person's word.
// secd spools and never parses beyond the RIFF/WAVE magic; ghost.voiced does the rest behind the
// front door (archive, transcription, journal).

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/voiced"
)

// askMaxBytes bounds a question asked aloud: 8 MB is four minutes of the phone's WAV.
const askMaxBytes = 8 << 20

// hearAsk hands a question's WAV to ghost.voiced and brings the words back; tests replace it.
var hearAsk = func(runDir, path string) (string, string, error) {
	resp, err := ctlsock.NewClientTimeout("ghost.voiced", runDir, 3*time.Minute).Call("hear", map[string]any{"path": path})
	if err != nil {
		return "", "", err
	}
	if !resp.OK {
		return "", "", fmt.Errorf("%s", resp.Err)
	}
	var d struct {
		Text string `json:"text"`
		Lang string `json:"lang"`
	}
	_ = json.Unmarshal(resp.Data, &d)
	if d.Text == "" {
		d.Text = resp.Text
	}
	return d.Text, d.Lang, nil
}

// handleVoiceAsk , POST /v1/voice/ask , a question asked aloud: the WAV as the body, the words
// back ({"text", "lang"}), for the chat to send as a typed question. Nothing is kept: the file
// goes to <mount>/voiced/ask, ghost.voiced hears it and removes it, no row, no journal entry. A
// box without a speech engine says so ({"ok": false, "why"}), and the phone falls back to typing.
func (s *Server) handleVoiceAsk(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	mount, ok := s.voiceMount()
	if !ok || s.closing.Load() {
		s.appearsDown(w)
		return
	}
	defer s.streaming(w)()
	dir := voiced.AskDir(mount)
	if err := os.MkdirAll(dir, 0o750); err != nil {
		s.appearsDown(w)
		return
	}
	uid, gid := s.spoolCred()
	if uid > 0 {
		_ = os.Chown(filepath.Join(mount, "voiced"), uid, gid)
		_ = os.Chown(dir, uid, gid)
	}
	var rb [8]byte
	_, _ = rand.Read(rb[:])
	path := filepath.Join(dir, hex.EncodeToString(rb[:])+".wav")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_EXCL, 0o640)
	if err != nil {
		s.appearsDown(w)
		return
	}
	n, err := io.Copy(f, gateReader{r: http.MaxBytesReader(w, r.Body, askMaxBytes), stop: &s.closing})
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = wavMagic(path)
	}
	if err != nil {
		_ = os.Remove(path)
		if s.closing.Load() {
			s.appearsDown(w)
			return
		}
		http.Error(w, "not a WAV", http.StatusBadRequest)
		return
	}
	if uid > 0 {
		_ = os.Chown(path, uid, gid)
	}
	runDir := filepath.Join(mount, "run")
	text, lang, err := hearAsk(runDir, path)
	_ = os.Remove(path) // voiced removes it too; one of the two is enough
	if err != nil {
		secdLog.Warn("a question asked aloud was not heard", "fn", "handleVoiceAsk", "bytes", n, "err", err)
		writeJSON(w, map[string]any{"ok": false, "why": err.Error()})
		return
	}
	text = strings.TrimSpace(text)
	writeJSON(w, map[string]any{"ok": true, "text": text, "lang": lang, "heard": text != ""})
}

// voiceMaxBytes bounds one note: 128 MB is 66 minutes of the phone's 16 kHz mono 16-bit WAV. The
// app stops a recording at 20 minutes (38 MB).
const voiceMaxBytes = 128 << 20

func (s *Server) voiceMount() (string, bool) {
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		return "", false
	}
	return filepath.Join(s.cfg.StateDir, "mnt", fmt.Sprintf("slot%d", mounted)), true
}

// handleVoiceUpload , POST /v1/voice , one note: the WAV as the body, and in headers the phone's
// id (X-Ghost-Voice-Id, 32 hex), kind (X-Ghost-Voice-Kind: checkin | journal), day (X-Ghost-Voice-Day)
// and start time (X-Ghost-Taken, unix ms). Spooled to <mount>/voiced/inbox as <id>.wav, then
// <id>.json; ghost.voiced takes the pair. Idempotent by id: a retry after a lost answer gets 200 and
// nothing is stored twice. 202 when taken, 200 when the box already had it.
func (s *Server) handleVoiceUpload(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	mount, ok := s.voiceMount()
	if !ok {
		s.appearsDown(w)
		return
	}
	if s.closing.Load() {
		s.appearsDown(w) // locking: nothing new goes into the volume
		return
	}
	defer s.streaming(w)()
	id := strings.ToLower(strings.TrimSpace(r.Header.Get("X-Ghost-Voice-Id")))
	if !voiced.IDRE.MatchString(id) {
		secdLog.Warn("voice upload rejected: bad id", "fn", "handleVoiceUpload")
		s.appearsDown(w)
		return
	}
	inbox := filepath.Join(mount, "voiced", "inbox")
	wav := filepath.Join(inbox, id+".wav")
	side := filepath.Join(inbox, id+".json")
	if have, err := s.notif.VoiceHave(mounted, id); err == nil && have {
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, voiceMaxBytes))
		writeJSON(w, map[string]any{"ok": true, "have": true})
		return
	}
	if _, err := os.Stat(side); err == nil {
		_, _ = io.Copy(io.Discard, io.LimitReader(r.Body, voiceMaxBytes))
		writeJSON(w, map[string]any{"ok": true, "have": true}) // spooled already, not yet archived
		return
	}
	m := voiced.Meta{ID: id, Kind: r.Header.Get("X-Ghost-Voice-Kind"), Day: r.Header.Get("X-Ghost-Voice-Day"), Device: deviceKeyFromRequest(r)}
	if m.Kind != "checkin" {
		m.Kind = "journal"
	}
	if _, err := time.Parse("2006-01-02", m.Day); err != nil {
		m.Day = ""
	}
	if t, err := strconv.ParseInt(r.Header.Get("X-Ghost-Taken"), 10, 64); err == nil && t > 0 {
		m.Taken = t
	}
	if err := os.MkdirAll(inbox, 0o750); err != nil {
		secdLog.Warn("voice upload: inbox", "fn", "handleVoiceUpload", "err", err)
		s.appearsDown(w)
		return
	}
	uid, gid := s.spoolCred()
	if uid > 0 {
		_ = os.Chown(filepath.Join(mount, "voiced"), uid, gid)
		_ = os.Chown(inbox, uid, gid)
	}
	part := wav + ".part"
	f, err := os.OpenFile(part, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o640)
	if err != nil {
		secdLog.Warn("voice upload: create", "fn", "handleVoiceUpload", "err", err)
		s.appearsDown(w)
		return
	}
	n, err := io.Copy(f, gateReader{r: http.MaxBytesReader(w, r.Body, voiceMaxBytes), stop: &s.closing})
	if err == nil {
		err = f.Sync()
	}
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = wavMagic(part)
	}
	if err != nil {
		_ = os.Remove(part)
		if s.closing.Load() {
			s.appearsDown(w) // cut by a lock: the phone keeps the note and sends it again
			return
		}
		secdLog.Warn("voice upload failed", "fn", "handleVoiceUpload", "id", id[:8], "bytes", n, "err", err)
		http.Error(w, "upload failed", http.StatusBadRequest)
		return
	}
	mb, _ := json.Marshal(m)
	err = os.Rename(part, wav)
	if err == nil {
		err = os.WriteFile(side+".part", mb, 0o640)
	}
	if err == nil {
		if uid > 0 {
			_ = os.Chown(wav, uid, gid)
			_ = os.Chown(side+".part", uid, gid)
		}
		err = os.Rename(side+".part", side) // the sidecar last: voiced takes a pair only once both are whole
	}
	if err != nil {
		_ = os.Remove(part)
		_ = os.Remove(side + ".part")
		secdLog.Warn("voice upload: commit", "fn", "handleVoiceUpload", "id", id[:8], "err", err)
		s.appearsDown(w)
		return
	}
	secdLog.Debug("voice note spooled", "fn", "handleVoiceUpload", "id", id[:8], "kind", m.Kind, "bytes", n)
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(http.StatusAccepted)
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// wavMagic: the first twelve bytes say RIFF....WAVE. The rest is ghost.voiced's to judge.
func wavMagic(path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	var h [12]byte
	if _, err := io.ReadFull(f, h[:]); err != nil {
		return fmt.Errorf("too short for a WAV")
	}
	if string(h[0:4]) != "RIFF" || string(h[8:12]) != "WAVE" {
		return fmt.Errorf("not a WAV")
	}
	return nil
}

// handleVoiceNotes , GET /v1/voice/notes?n=N , the newest notes with their transcripts and status.
func (s *Server) handleVoiceNotes(w http.ResponseWriter, r *http.Request) {
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
	n, _ := strconv.Atoi(r.URL.Query().Get("n"))
	notes, err := s.notif.VoiceNotes(mounted, n)
	if err != nil {
		secdLog.Warn("voice notes failed", "fn", "handleVoiceNotes", "err", err)
		s.appearsDown(w)
		return
	}
	out := map[string]any{"notes": notes}
	if mount, ok := s.voiceMount(); ok {
		out["queue"] = voiceQueue(mount, time.Now())
	}
	writeJSON(w, out)
}

// voiceQueue is what the phone shows beside a waiting note: being transcribed now, why not (no
// speech engine), or that ghost.voiced is not running (its state file is missing or old).
func voiceQueue(mount string, now time.Time) map[string]any {
	st, ok := voiced.ReadState(mount)
	running := ok && now.Sub(time.UnixMilli(st.At)) < voiced.StateFresh
	q := map[string]any{"running": running}
	if running {
		q["engine"], q["why"], q["working"] = st.Engine, st.Why, st.Working
	}
	return q
}

// handleVoiceAudio , GET /v1/voice/audio?id= , the note's WAV as it was recorded (Range works).
func (s *Server) handleVoiceAudio(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	mount, ok := s.voiceMount()
	id := r.URL.Query().Get("id")
	if !ok || !voiced.IDRE.MatchString(id) {
		s.appearsDown(w)
		return
	}
	rel, err := s.notif.VoicePath(mounted, id)
	path := ""
	if err == nil && rel != "" && safeVoicePath(rel) {
		path = filepath.Join(mount, rel)
	} else if err == nil {
		path = filepath.Join(mount, "voiced", "inbox", id+".wav") // uploaded, not yet archived
	}
	f, ferr := os.Open(path)
	if err != nil || path == "" || ferr != nil {
		http.NotFound(w, r)
		return
	}
	defer f.Close()
	st, _ := f.Stat()
	w.Header().Set("Content-Type", "audio/wav")
	w.Header().Set("Cache-Control", "private, no-store")
	http.ServeContent(w, r, id+".wav", st.ModTime(), f)
}

// safeVoicePath: a stored path stays under voiced/ on the volume.
func safeVoicePath(rel string) bool {
	c := filepath.Clean(rel)
	return !filepath.IsAbs(c) && strings.HasPrefix(c, "voiced"+string(filepath.Separator)) && !strings.Contains(c, "..")
}

// handleVoiceDelete , POST /v1/voice/delete {"id"} , the note gone: audio, row and journal entry.
// A memory synthd already distilled from it stays until the person deletes that memory too (it is
// listed on the MEMORIES screen like every other).
func (s *Server) handleVoiceDelete(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	mount, ok := s.voiceMount()
	var req struct {
		ID string `json:"id"`
	}
	if !ok || json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req) != nil || !voiced.IDRE.MatchString(req.ID) {
		s.appearsDown(w)
		return
	}
	rel, found, err := s.notif.VoiceDelete(mounted, req.ID)
	if err != nil {
		secdLog.Warn("voice delete failed", "fn", "handleVoiceDelete", "err", err)
		s.appearsDown(w)
		return
	}
	if found && safeVoicePath(rel) {
		if err := os.Remove(filepath.Join(mount, rel)); err != nil && !os.IsNotExist(err) {
			secdLog.Warn("voice delete: audio not removed", "fn", "handleVoiceDelete", "id", req.ID[:8], "err", err)
		}
	}
	for _, ext := range []string{".wav", ".json"} { // still in the inbox, not yet archived
		_ = os.Remove(filepath.Join(mount, "voiced", "inbox", req.ID+ext))
	}
	secdLog.Debug("voice note deleted", "fn", "handleVoiceDelete", "id", req.ID[:8], "found", found)
	writeJSON(w, map[string]any{"ok": true, "found": found})
}
