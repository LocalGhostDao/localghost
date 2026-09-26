package secd

// /v1/geo/roadtiles/index and /v1/geo/roadtile?l=&x=&y= , the roads, cut by ghost.framed into
// one-degree cells (major roads) and tenth-of-a-degree cells (every road, with its name) under
// <mount>/roadtiles (internal/roadtiles). Static files with an mtime+size ETag, like the coast
// tiles. No tiles yet: an empty index answer and a 404 on the tile.

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/LocalGhostDao/localghost/server/internal/roadtiles"
)

func (s *Server) roadtilesDir() (string, bool) {
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		return "", false
	}
	return filepath.Join(s.cfg.StateDir, "mnt", fmt.Sprintf("slot%d", mounted), "roadtiles"), true
}

func (s *Server) handleRoadTileIndex(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	dir, ok := s.roadtilesDir()
	if !ok {
		s.appearsDown(w)
		return
	}
	if !serveStatic(w, r, filepath.Join(dir, "index.bin"), "application/octet-stream") {
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleRoadTile(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	dir, ok := s.roadtilesDir()
	if !ok {
		s.appearsDown(w)
		return
	}
	q := r.URL.Query()
	l, e0 := strconv.Atoi(q.Get("l"))
	x, e1 := strconv.Atoi(q.Get("x"))
	y, e2 := strconv.Atoi(q.Get("y"))
	if e0 != nil || e1 != nil || e2 != nil || l < 0 || l >= roadtiles.Levels || x < 0 || x >= roadtiles.Cols(l) || y < 0 || y >= roadtiles.Rows(l) {
		s.appearsDown(w)
		return
	}
	p := filepath.Join(dir, roadtiles.TileName(roadtiles.Cell{Level: l, X: x, Y: y}))
	if _, err := os.Stat(p); err != nil || !serveStatic(w, r, p, "application/octet-stream") {
		w.WriteHeader(http.StatusNotFound)
	}
}
