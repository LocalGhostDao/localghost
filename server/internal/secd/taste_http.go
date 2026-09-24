package secd

// /v1/taste and /v1/nearby , what the photos say the person likes, and what is around them that
// fits. Both are read-only views over what synthd's outing pass wrote (settings 'synthd_taste')
// and the GeoNames points the box already holds for geocoding; the ranking is internal/outings.
// The phone sends its own position in the query; the box answers from its own tables; nothing
// leaves the box. A box without geo data, or without a taste yet, answers with an empty list and
// a note, never an error the app would read as "down".

import (
	"encoding/json"
	"net/http"
	"strconv"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/outings"
)

// handleTaste , GET /v1/taste , the taste JSON as synthd wrote it ({} with a note before the first pass).
func (s *Server) handleTaste(w http.ResponseWriter, r *http.Request) {
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
	raw, err := s.notif.TasteJSON(mounted)
	if err != nil {
		secdLog.Warn("taste read failed", "fn", "handleTaste", "err", err)
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	if raw == "" || !json.Valid([]byte(raw)) {
		_ = json.NewEncoder(w).Encode(map[string]any{"note": "no taste yet: synthd builds it from the photos' tags within half an hour of the first tagged photos"})
		return
	}
	_, _ = w.Write([]byte(raw))
}

// handleNearby , GET /v1/nearby?lat=&lon=&km= , spots around the position that fit the taste,
// ranked, with the reason and whether the person has photographed there before.
func (s *Server) handleNearby(w http.ResponseWriter, r *http.Request) {
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
	q := r.URL.Query()
	lat, e1 := strconv.ParseFloat(q.Get("lat"), 64)
	lon, e2 := strconv.ParseFloat(q.Get("lon"), 64)
	if e1 != nil || e2 != nil || lat < -90 || lat > 90 || lon < -180 || lon > 180 {
		s.appearsDown(w)
		return
	}
	km, _ := strconv.ParseFloat(q.Get("km"), 64)
	if km <= 0 || km > 60 {
		km = 15
	}
	w.Header().Set("Content-Type", "application/json")
	raw, err := s.notif.TasteJSON(mounted)
	if err != nil {
		secdLog.Warn("taste read failed", "fn", "handleNearby", "err", err)
		s.appearsDown(w)
		return
	}
	var taste outings.Taste
	if raw == "" || json.Unmarshal([]byte(raw), &taste) != nil || len(taste.Interests) == 0 {
		_ = json.NewEncoder(w).Encode(map[string]any{"suggestions": []outings.Suggestion{}, "note": "no taste yet: nothing to match places against until the photos are tagged"})
		return
	}
	spots, err := s.notif.SpotsNear(mounted, lat, lon, km)
	if err != nil {
		secdLog.Warn("spots query failed", "fn", "handleNearby", "err", err)
		s.appearsDown(w)
		return
	}
	if len(spots) == 0 {
		_ = json.NewEncoder(w).Encode(map[string]any{"suggestions": []outings.Suggestion{}, "note": "no places known within " + strconv.Itoa(int(km)) + " km: no geo data on the box yet, or a box that predates the spot import (ghost-cli ghost.framed geo-import)"})
		return
	}
	cells, err := s.notif.PhotoCellsNear(mounted, lat, lon, km)
	if err != nil {
		secdLog.Warn("photo cells query failed (suggestions without been-there)", "fn", "handleNearby", "err", err)
		cells = nil
	}
	out := outings.Rank(spots, taste, lat, lon, km, hw.PhotosNearFunc(cells))
	_ = json.NewEncoder(w).Encode(map[string]any{"suggestions": out, "km": km, "candidates": len(spots)})
}
