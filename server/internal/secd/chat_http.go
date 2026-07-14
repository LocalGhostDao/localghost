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
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 16<<20)).Decode(&req); err != nil || req.Prompt == "" {
		s.appearsDown(w)
		return
	}
	// STREAMING: pipe synthd's event stream (context first, then tokens, then done) straight to the
	// app. secd adds authentication and appears-down; it does not touch the events. Cancellation
	// flows: app hangs up -> this request context cancels -> synthd -> oracled -> llama stops
	// generating, so an abandoned question stops burning CPU.
	body, _ := json.Marshal(map[string]any{
		"prompt": req.Prompt, "think": req.Think, "image": req.ImageB64,
		"incognito": req.Incognito, "chatId": req.ChatID,
	})
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
	secdLog.Info("chat streamed", "fn", "handleChat", "took", time.Since(t0).Round(time.Millisecond).String())
}

