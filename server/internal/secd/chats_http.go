package secd

// Chat READ endpoints , secd turned out to be the whole API, so the conversations ghost.synthd
// persists get their list/load/search surface here. Writes still happen only in synthd on the
// stream path; these are pure reads over the same tables.
//
//   GET /v1/chats?limit=20&before=<updated_at ms>&q=<text>   , newest first, keyset paginated,
//        q matches the title or any message body (case-insensitive)
//   GET /v1/chats/messages?id=N&limit=100&before=<message id> , one conversation's history,
//        newest first; the app reverses for display and pages with the smallest id it has

import (
	"encoding/json"
	"net/http"

	"fmt"
	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

func (s *Server) handleChatsList(w http.ResponseWriter, r *http.Request) {
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
	limit, _ := strconv.Atoi(qp.Get("limit"))
	before, _ := strconv.ParseInt(qp.Get("before"), 10, 64)
	q := qp.Get("q")
	if len(q) > 120 {
		q = q[:120] // a search box, not an essay slot
	}
	chats, err := s.notif.ChatsList(mounted, limit, before, q)
	if err != nil {
		secdLog.Warn("chats list failed", "fn", "handleChatsList", "err", err)
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"chats": chats})
}

func (s *Server) handleChatMessages(w http.ResponseWriter, r *http.Request) {
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
	id, _ := strconv.ParseInt(qp.Get("id"), 10, 64)
	if id <= 0 {
		s.appearsDown(w)
		return
	}
	limit, _ := strconv.Atoi(qp.Get("limit"))
	before, _ := strconv.ParseInt(qp.Get("before"), 10, 64)
	msgs, err := s.notif.ChatMessages(mounted, id, limit, before)
	if err != nil {
		secdLog.Warn("chat messages failed", "fn", "handleChatMessages", "err", err)
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"messages": msgs})
}

// handleChatRename , POST /v1/chats/rename {"id": N, "title": "..."} , the one WRITE in this file.
// The person's title outranks the derived one permanently (synthd only auto-titles empty titles).
// Same appears-down discipline as everything else: bad session, wrong method, cold box, bogus id,
// oversized title , all the same 503, no distinguishable errors for anyone probing.
func (s *Server) handleChatRename(w http.ResponseWriter, r *http.Request) {
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
		ID    int64  `json:"id"`
		Title string `json:"title"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4096)).Decode(&req); err != nil ||
		req.ID <= 0 || len(req.Title) == 0 || len(req.Title) > 200 {
		s.appearsDown(w)
		return
	}
	ok, err := s.notif.ChatRename(mounted, req.ID, req.Title)
	if err != nil || !ok {
		if err != nil {
			secdLog.Warn("chat rename failed", "fn", "handleChatRename", "err", err)
		}
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// handleChatDelete , POST /v1/chats/delete {"id": N}. Real deletion, same appears-down discipline.
func (s *Server) handleChatDelete(w http.ResponseWriter, r *http.Request) {
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
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil || req.ID <= 0 {
		s.appearsDown(w)
		return
	}
	ok, err := s.notif.ChatDelete(mounted, req.ID)
	if err != nil || !ok {
		if err != nil {
			secdLog.Warn("chat delete failed", "fn", "handleChatDelete", "err", err)
		}
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// handleMemories , GET /v1/memories , the distilled corpus, live rows only, for the MEMORIES screen.
func (s *Server) handleMemories(w http.ResponseWriter, r *http.Request) {
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
	rowsOut, err := s.notif.MemoriesList(mounted, limit)
	if err != nil {
		secdLog.Warn("memories list failed", "fn", "handleMemories", "err", err)
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"memories": rowsOut})
}

// handleDays , GET /v1/days?before=YYYY-MM-DD&limit=N , the prebuilt day summaries, newest first
// (the sheet included: covers, places, the route line), for a DAYS feed and "on this day"-style
// screens. GET /v1/day?d=YYYY-MM-DD is one of them. Empty list on a box that has built none yet.
func (s *Server) handleDays(w http.ResponseWriter, r *http.Request) {
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
	before := r.URL.Query().Get("before")
	if before != "" {
		if _, err := time.Parse("2006-01-02", before); err != nil {
			http.Error(w, "before=YYYY-MM-DD", http.StatusBadRequest)
			return
		}
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	rowsOut, err := s.notif.DaysList(mounted, before, limit)
	if err != nil {
		secdLog.Warn("days list failed", "fn", "handleDays", "err", err)
		rowsOut = nil // a box whose schema predates the table: an empty feed, not appears-down
	}
	if rowsOut == nil {
		rowsOut = []hw.DayRow{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"days": rowsOut})
}

// handleDay , GET /v1/day?d=YYYY-MM-DD[&build=1] , one day's summary: what synthd wrote from the
// photos, the trail, the health sync, the voice notes and the check-in (days.go). With build=1
// the box writes it NOW: the check-in just landed and the person is waiting to read their day.
// That waits for noted to take the check-in from its inbox (a 30 s tick), then asks synthd for
// the day, which takes the model a minute or two; the phone's read timeout covers it.
func (s *Server) handleDay(w http.ResponseWriter, r *http.Request) {
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
	day := r.URL.Query().Get("d")
	if _, err := time.Parse("2006-01-02", day); err != nil {
		http.Error(w, "d=YYYY-MM-DD", http.StatusBadRequest)
		return
	}
	built := false
	if r.URL.Query().Get("build") == "1" {
		// the check-in first: it goes through noted's inbox, and the day told without it is
		// not the day the person just described
		checked := false
		for i := 0; i < 15; i++ {
			if ok, _ := s.notif.CheckedIn(mounted, day); ok {
				checked = true
				break
			}
			select {
			case <-r.Context().Done():
				return
			case <-time.After(3 * time.Second):
			}
		}
		runDir := fmt.Sprintf("%s/mnt/slot%d/run", s.cfg.StateDir, mounted)
		c := ctlsock.NewClientTimeout("ghost.synthd", runDir, 4*time.Minute)
		if resp, err := c.Call("days", map[string]any{"day": day, "pass": true}); err != nil || !resp.OK {
			why := ""
			if err != nil {
				why = err.Error()
			} else {
				why = resp.Err
			}
			secdLog.Warn("day build failed", "fn", "handleDay", "day", day, "checkedIn", checked, "err", why)
		} else {
			built = true
		}
	}
	row, err := s.notif.DayOne(mounted, day)
	if err != nil {
		secdLog.Warn("day read failed", "fn", "handleDay", "day", day, "err", err)
		s.appearsDown(w)
		return
	}
	checked, _ := s.notif.CheckedIn(mounted, day)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"day": row, "checkedIn": checked, "built": built})
}

// handleMemoryDelete , POST /v1/memories/delete {"id":N} , tombstone, never resurrectable.
func (s *Server) handleMemoryDelete(w http.ResponseWriter, r *http.Request) {
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
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&req); err != nil || req.ID <= 0 {
		s.appearsDown(w)
		return
	}
	ok, err := s.notif.MemoryTombstone(mounted, req.ID)
	if err != nil || !ok {
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// handleMemoryAdd , POST /v1/memories/add {"title","body"} , a user-authored memory, sovereign from birth.
func (s *Server) handleMemoryAdd(w http.ResponseWriter, r *http.Request) {
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
	var req struct{ Title, Body string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil ||
		strings.TrimSpace(req.Title) == "" || len(req.Title) > 160 || len(req.Body) > 2000 {
		s.appearsDown(w)
		return
	}
	id, err := s.notif.MemoryAdd(mounted, strings.TrimSpace(req.Title), strings.TrimSpace(req.Body))
	if err != nil || id == 0 {
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "id": id})
}

// handleMemoryEdit , POST /v1/memories/edit {"id","title","body"} , the person's version IS the memory.
func (s *Server) handleMemoryEdit(w http.ResponseWriter, r *http.Request) {
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
		ID          int64
		Title, Body string
	}
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 8192)).Decode(&req); err != nil ||
		req.ID <= 0 || strings.TrimSpace(req.Title) == "" || len(req.Title) > 160 || len(req.Body) > 2000 {
		s.appearsDown(w)
		return
	}
	ok, err := s.notif.MemoryEdit(mounted, req.ID, strings.TrimSpace(req.Title), strings.TrimSpace(req.Body))
	if err != nil || !ok {
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true})
}

// handleNoteAdd , POST /v1/notes {"text"} , the app's way INTO the journal. secd writes the text
// into noted's inbox on the volume (owned by the run user so noted can read it); noted ingests it
// on its next tick exactly like anything dropped there by hand , one path, no special cases. The
// share sheet and the jot box both land here.
func (s *Server) handleNoteAdd(w http.ResponseWriter, r *http.Request) {
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
	var req struct{ Text string }
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64*1024)).Decode(&req); err != nil ||
		strings.TrimSpace(req.Text) == "" {
		s.appearsDown(w)
		return
	}
	inbox := filepath.Join(s.cfg.StateDir, "mnt", fmt.Sprintf("slot%d", mounted), "noted", "inbox")
	if err := os.MkdirAll(inbox, 0o750); err != nil {
		s.appearsDown(w)
		return
	}
	name := fmt.Sprintf("app-%d.txt", time.Now().UnixNano())
	path := filepath.Join(inbox, name)
	if err := os.WriteFile(path, []byte(strings.TrimSpace(req.Text)+"\n"), 0o640); err != nil {
		s.appearsDown(w)
		return
	}
	// secd runs as root; noted runs as the run user , chown so the ingest can read its own inbox.
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

// handleWiki , GET /v1/wiki?q=<phrase>&n=8 or ?idx=<n>: the box's Wikipedia, in its database once
// the mirror's file is imported (ghost.synthd wikipedia.go). The state and counts always; with q,
// the articles the phrase names, surest first (the title in any case, a redirect, a qualified
// place, a prefix, a likeness, the words of a lead); with idx, one article whole. Nothing leaves the
// box: the question is a few SQL reads. Asked through synthd's control socket, which owns the store.
func (s *Server) handleWiki(w http.ResponseWriter, r *http.Request) {
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
	args := map[string]any{}
	if q := strings.TrimSpace(r.URL.Query().Get("q")); q != "" {
		args["q"] = q
	}
	if n, err := strconv.Atoi(r.URL.Query().Get("n")); err == nil && n > 0 {
		args["n"] = n
	}
	if idx, err := strconv.ParseUint(r.URL.Query().Get("idx"), 10, 32); err == nil && idx > 0 {
		args["idx"] = idx
	}
	runDir := fmt.Sprintf("%s/mnt/slot%d/run", s.cfg.StateDir, mounted)
	resp, err := ctlsock.NewClientTimeout("ghost.synthd", runDir, 30*time.Second).Call("wiki", args)
	if err != nil || !resp.OK {
		if err != nil {
			secdLog.Warn("wiki failed", "fn", "handleWiki", "err", err)
		}
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(resp.Text))
}

// handleOnThisDay , GET /v1/onthisday?day=MM-DD (empty = today) , synthd's retrospective, proxied.
func (s *Server) handleOnThisDay(w http.ResponseWriter, r *http.Request) {
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
	runDir := fmt.Sprintf("%s/mnt/slot%d/run", s.cfg.StateDir, mounted)
	c := ctlsock.NewClientTimeout("ghost.synthd", runDir, 5*time.Minute) // first build narrates via the model
	resp, err := c.Call("onthisday", map[string]any{"day": r.URL.Query().Get("day")})
	if err != nil || !resp.OK {
		if err != nil {
			secdLog.Warn("onthisday failed", "fn", "handleOnThisDay", "err", err)
		}
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_, _ = w.Write([]byte(resp.Text))
}
