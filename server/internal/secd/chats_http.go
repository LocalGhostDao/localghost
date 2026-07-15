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
	"strconv"
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
