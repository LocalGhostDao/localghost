package secd

// /v1/geo/landtiles/index and /v1/geo/landtile?x=&y= , the high-resolution coastline, cut by
// ghost.framed into one-degree tiles under <mount>/landtiles (internal/landtiles). The phone asks
// for the index once (64 KB, ETag-revalidated) and then only for the tiles under its viewport when
// zoomed in past the Natural Earth base's detail. Static files, served like the world cuts: an
// ETag from mtime and size, a 304 when the phone already has them. No tiles yet is a 404-shaped
// appears-down on the tile and an empty index answer , the map simply keeps its 10m coast.

import (
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strconv"

	"github.com/LocalGhostDao/localghost/server/internal/landtiles"
)

func (s *Server) landtilesDir() (string, bool) {
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	if mounted < 0 {
		return "", false
	}
	return filepath.Join(s.cfg.StateDir, "mnt", fmt.Sprintf("slot%d", mounted), "landtiles"), true
}

// serveStatic sends a file with an mtime+size ETag and honours If-None-Match.
func serveStatic(w http.ResponseWriter, r *http.Request, path, contentType string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil || fi.IsDir() {
		return false
	}
	etag := fmt.Sprintf("\"t-%d-%d\"", fi.ModTime().Unix(), fi.Size())
	w.Header().Set("ETag", etag)
	if r.Header.Get("If-None-Match") == etag {
		w.WriteHeader(http.StatusNotModified)
		return true
	}
	w.Header().Set("Content-Type", contentType)
	w.Header().Set("Content-Length", strconv.FormatInt(fi.Size(), 10))
	_, _ = io.Copy(w, f)
	return true
}

func (s *Server) handleLandTileIndex(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	dir, ok := s.landtilesDir()
	if !ok {
		s.appearsDown(w)
		return
	}
	if !serveStatic(w, r, filepath.Join(dir, "index.bin"), "application/octet-stream") {
		// no tiles built yet: an empty answer the phone reads as "keep the base map"
		w.Header().Set("Content-Type", "application/octet-stream")
		w.WriteHeader(http.StatusNoContent)
	}
}

func (s *Server) handleLandTile(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	dir, ok := s.landtilesDir()
	if !ok {
		s.appearsDown(w)
		return
	}
	x, e1 := strconv.Atoi(r.URL.Query().Get("x"))
	y, e2 := strconv.Atoi(r.URL.Query().Get("y"))
	if e1 != nil || e2 != nil || x < 0 || x >= landtiles.Cols || y < 0 || y >= landtiles.Rows {
		s.appearsDown(w)
		return
	}
	if !serveStatic(w, r, filepath.Join(dir, landtiles.TileName(landtiles.Cell{X: x, Y: y})), "application/octet-stream") {
		w.WriteHeader(http.StatusNotFound)
	}
}
