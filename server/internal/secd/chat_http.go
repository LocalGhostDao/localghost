package secd

// /v1/chat , the app's question, answered by the box's own model. secd forwards to ghost.synthd's
// chat command (the retrieval seam: today a pure passthrough to ghost.oracled, tomorrow the place
// where the memory index injects context), and returns the answer. Session-authenticated, appears-
// down on rejection like every other route. Non-streaming v1: the model runs to completion, then one
// JSON reply , llama-server streaming can be plumbed later without changing this route's shape.

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"

	"github.com/LocalGhostDao/localghost/server/internal/streamsock"
	"time"
)

func (s *Server) handleChat(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) {
		secdLog.Warn("chat rejected: invalid session", "fn", "handleChat", "bearerPresent", bearer(r) != "")
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
		Prompt string `json:"prompt"`
		Think  string `json:"think"`
		// Optional base64 image (jpeg/png). Capped: a data URI of a full camera frame is ~12MB of
		// base64, and anything larger is someone probing, not chatting.
		ImageB64 string `json:"imageB64,omitempty"`
		// Persistence controls. FOUND MISSING in review: the app sent these, this struct did not
		// parse them, and the forward silently dropped them , incognito was decorative and every
		// message opened a NEW chat because synthd always saw chatId 0. The edge must forward the
		// whole conversation contract, not just the words.
		Incognito bool  `json:"incognito"`
		ChatID    int64 `json:"chatId"`
		// The phone's copy of the conversation so far (incognito chats have no box copy). Opaque
		// here: forwarded as received, bounded downstream. Same lesson as the two fields above ,
		// the edge forwards the whole contract, or the feature is silently decorative.
		History json.RawMessage `json:"history,omitempty"`
		// Web results the phone fetched for this question. The box never reaches the internet;
		// the phone did, on the person's say-so, and hands the findings in. Opaque here, bounded
		// downstream.
		Web json.RawMessage `json:"web,omitempty"`
		// The phone's last fix when it is recent, so "anywhere good near here?" means somewhere.
		// Checked for range here; synthd uses it against the box's own map data only.
		Here *struct {
			Lat float64 `json:"lat"`
			Lon float64 `json:"lon"`
		} `json:"here,omitempty"`
		// The smarter search's fields (synthd websmart.go): what the model said the question
		// needs, which round this is, the plan's searches not run yet. Bounded, forwarded.
		Need  string   `json:"need,omitempty"`
		Round int      `json:"round,omitempty"`
		Spare []string `json:"spare,omitempty"`
		// How much answer: short, medium or long (synthd length.go). Forwarded when it is one of
		// the three; anything else is dropped, and synthd answers medium.
		Length string `json:"length,omitempty"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&req); err != nil || req.Prompt == "" {
		s.appearsDown(w)
		return
	}
	// STREAMING: pipe synthd's event stream (context first, then tokens, then done) straight to the
	// app. secd adds authentication and appears-down; it does not touch the events. Cancellation
	// flows: app hangs up -> this request context cancels -> synthd -> oracled -> llama stops
	// generating, so an abandoned question stops burning CPU.
	fwd := map[string]any{
		"prompt": req.Prompt, "think": req.Think, "image": req.ImageB64,
		"incognito": req.Incognito, "chatId": req.ChatID,
	}
	if len(req.History) > 0 && len(req.History) <= 64<<10 {
		fwd["history"] = req.History
	}
	// the phone sends each read page's paragraphs now, not one excerpt: a few pages of prose
	if len(req.Web) > 0 && len(req.Web) <= 256<<10 {
		fwd["web"] = req.Web
	}
	if len(req.Need) > 0 && len(req.Need) <= 400 {
		fwd["need"] = req.Need
	}
	if req.Round > 0 && req.Round <= 3 {
		fwd["round"] = req.Round
	}
	if n := len(req.Spare); n > 0 && n <= 3 {
		ok := true
		for _, q := range req.Spare {
			if len(q) > 160 {
				ok = false
			}
		}
		if ok {
			fwd["spare"] = req.Spare
		}
	}
	switch req.Length {
	case "short", "medium", "long":
		fwd["length"] = req.Length
	}
	if h := req.Here; h != nil && h.Lat >= -90 && h.Lat <= 90 && h.Lon >= -180 && h.Lon <= 180 {
		fwd["here"] = map[string]float64{"lat": h.Lat, "lon": h.Lon}
	}
	body, _ := json.Marshal(fwd)
	runDir := fmt.Sprintf("%s/mnt/slot%d/run", s.cfg.StateDir, mounted)
	up, err := http.NewRequestWithContext(r.Context(), http.MethodPost,
		"http://ghost/chat", bytes.NewReader(body))
	if err != nil {
		s.appearsDown(w)
		return
	}
	up.Header.Set("Content-Type", "application/json")
	t0 := time.Now()
	resp, err := streamsock.Client("ghost.synthd", runDir).Do(up)
	if err != nil {
		secdLog.Warn("chat failed: synthd unreachable", "fn", "handleChat", "err", err)
		s.appearsDown(w) // model down / loading / synthd absent: indistinguishable from box-down, by design
		return
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		secdLog.Warn("chat failed", "fn", "handleChat", "code", resp.StatusCode)
		s.appearsDown(w)
		return
	}
	fl, _ := w.(http.Flusher)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	// NGINX FRONTS THIS. nginx buffers proxied responses by default, which turns a token stream
	// into wait-forever-then-everything-at-once , the model was streaming fine, the tokens were
	// sitting in nginx's buffer. This header disables buffering for THIS response only; no nginx
	// conf change, nothing loosened for any other route.
	w.Header().Set("X-Accel-Buffering", "no")
	buf := make([]byte, 4096)
	for {
		n, rerr := resp.Body.Read(buf)
		if n > 0 {
			if _, werr := w.Write(buf[:n]); werr != nil {
				return // app hung up , context cancellation stops the chain
			}
			if fl != nil {
				fl.Flush()
			}
		}
		if rerr != nil {
			break
		}
	}
	secdLog.Debug("chat streamed", "fn", "handleChat", "took", time.Since(t0).Round(time.Millisecond).String())
}

// handleChatPlan , POST /v1/chat/plan {prompt, history} → {ok, search, need, shape, fresh,
// queries}: the model's statement of what a question needs from the web, so the phone searches
// for the right thing. A plain JSON answer, not a stream; {"ok":false} tells the phone to plan by
// itself. Same session rule and appears-down as /v1/chat.
func (s *Server) handleChatPlan(w http.ResponseWriter, r *http.Request) {
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
		Prompt  string          `json:"prompt"`
		History json.RawMessage `json:"history,omitempty"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 256<<10)).Decode(&req); err != nil || req.Prompt == "" || len(req.Prompt) > 4000 {
		s.appearsDown(w)
		return
	}
	fwd := map[string]any{"prompt": req.Prompt}
	if len(req.History) > 0 && len(req.History) <= 64<<10 {
		fwd["history"] = req.History
	}
	body, _ := json.Marshal(fwd)
	runDir := fmt.Sprintf("%s/mnt/slot%d/run", s.cfg.StateDir, mounted)
	up, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "http://ghost/plan", bytes.NewReader(body))
	if err != nil {
		s.appearsDown(w)
		return
	}
	up.Header.Set("Content-Type", "application/json")
	resp, err := streamsock.Client("ghost.synthd", runDir).Do(up)
	if err != nil || resp.StatusCode != http.StatusOK {
		// no plan is not "down": the phone plans by itself
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"ok":false}`))
		if resp != nil {
			resp.Body.Close()
		}
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 64<<10))
}

// handleChatStop , POST /v1/chat/stop {chatId} → {ok, stopped}: STOP in the app. The box writes an
// answer to the end even when the phone goes away (so closing the app loses nothing); this is
// the one way to end one early. What was written stays in the chat, marked stopped.
func (s *Server) handleChatStop(w http.ResponseWriter, r *http.Request) {
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
		ChatID int64 `json:"chatId"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || req.ChatID <= 0 {
		s.appearsDown(w)
		return
	}
	body, _ := json.Marshal(map[string]any{"chatId": req.ChatID})
	runDir := fmt.Sprintf("%s/mnt/slot%d/run", s.cfg.StateDir, mounted)
	up, err := http.NewRequestWithContext(r.Context(), http.MethodPost, "http://ghost/chat/stop", bytes.NewReader(body))
	if err != nil {
		s.appearsDown(w)
		return
	}
	up.Header.Set("Content-Type", "application/json")
	resp, err := streamsock.Client("ghost.synthd", runDir).Do(up)
	if err != nil {
		s.appearsDown(w)
		return
	}
	defer resp.Body.Close()
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.Copy(w, io.LimitReader(resp.Body, 4096))
}
