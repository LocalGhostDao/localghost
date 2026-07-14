package secd

// READ-ONLY stats endpoints. ghost.watchd is SYSTEM HEALTH , it samples every daemon's /health,
// the datastores, and the host vitals, and writes the ring buffers (see internal/watchd/stats.go).
// secd is the app-facing edge: it reads those Redis keys and serves them, nothing more. A first
// version had the sampler HERE, which violated the layering ("the only daemon with read access to
// every other daemon's metrics" is watchd) , moved, and this comment is the fence against drift.

import (
	"encoding/json"
	"net/http"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
)

const (
	stats10sMax = 600
	stats1mMax  = 1440
)

type statEntry struct {
	T int64   `json:"t"`
	C uint8   `json:"c"`
	V float64 `json:"v"`
	D string  `json:"d,omitempty"`
}

// handleServicesSummary , GET /v1/services/summary. One row per tracked target: the latest 10s
// sample plus the stored 24h blob. This is the list the Box Status screen paints.
func (s *Server) handleServicesSummary(w http.ResponseWriter, r *http.Request) {
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
	type row struct {
		Name  string          `json:"name"`
		Now   *statEntry      `json:"now,omitempty"`
		Day   json.RawMessage `json:"day,omitempty"`
	}
	var rows []row
	for name := range hw.StatsTargets() {
		rw := row{Name: name}
		if latest, err := s.notif.StatsRange(mounted, "stats:10s:"+name, 1); err == nil && len(latest) == 1 {
			var e statEntry
			if json.Unmarshal([]byte(latest[0]), &e) == nil {
				rw.Now = &e
			}
		}
		if blob, ok, err := s.notif.StatsGet(mounted, "stats:24h:"+name); err == nil && ok {
			rw.Day = json.RawMessage(blob)
		}
		rows = append(rows, rw)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"targets": rows})
}

// handleServiceDetail , GET /v1/services/detail?name=X. The full picture for one target: both ring
// buffers (newest first, as stored) plus the 24h blob. The app draws its sparklines from these.
func (s *Server) handleServiceDetail(w http.ResponseWriter, r *http.Request) {
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
	if !hw.StatsTargets()[name] {
		s.appearsDown(w) // unknown target indistinguishable from anything else, by house rule
		return
	}
	parse := func(key string, n int) []statEntry {
		rows, err := s.notif.StatsRange(mounted, key, n)
		if err != nil {
			return nil
		}
		out := make([]statEntry, 0, len(rows))
		for _, raw := range rows {
			var e statEntry
			if json.Unmarshal([]byte(raw), &e) == nil {
				out = append(out, e)
			}
		}
		return out
	}
	resp := map[string]any{
		"name": name,
		"s10":  parse("stats:10s:"+name, stats10sMax),
		"s1m":  parse("stats:1m:"+name, stats1mMax),
	}
	if blob, ok, err := s.notif.StatsGet(mounted, "stats:24h:"+name); err == nil && ok {
		resp["day"] = json.RawMessage(blob)
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}
