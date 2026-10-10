package hw

import (
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// Home is where the nights are: a week of nights in Neu-Ulm outweighs the days in Munich and
// two nights in a hotel in Berlin.
func TestHomeGuessIsWhereTheNightsAre(t *testing.T) {
	dir := os.Getenv("GHOST_PG_SOCKET_DIR")
	if dir == "" {
		t.Skip("GHOST_PG_SOCKET_DIR not set; no Postgres to test against")
	}
	port := 5432
	if p, err := strconv.Atoi(os.Getenv("GHOST_PG_PORT")); err == nil {
		port = p
	}
	user := os.Getenv("GHOST_PG_USER")
	if user == "" {
		user = "postgres"
	}
	admin := poltergres.NewReadWrite(dir, port, user, "", "postgres")
	_ = admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_home")
	if err := admin.ExecSimple("CREATE DATABASE lgtest_home"); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", "lgtest_home")
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := ConvergeSchema(db, lg); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 10, 12, 0, 0, 0, time.UTC)
	if _, _, n, ok := HomeGuess(db, now); ok || n != 0 {
		t.Fatal("a home from no trail")
	}
	// seven nights at 48.39, 10.01 (points at 02:00 local, 01:20 UTC), each day's noon in
	// Munich, two nights in Berlin
	for d := 1; d <= 7; d++ {
		night := time.Date(2026, 10, d, 1, 20, 0, 0, time.UTC)
		for k := 0; k < 4; k++ {
			if err := db.Exec("INSERT INTO location_points (ts, lat, lon, source) VALUES ($1, 48.3901, 10.0123, $2)", night.Add(time.Duration(k)*15*time.Minute).Unix(), "w"+strconv.Itoa(k)); err != nil {
				t.Fatal(err)
			}
		}
		noon := time.Date(2026, 10, d, 11, 0, 0, 0, time.UTC)
		for k := 0; k < 8; k++ {
			_ = db.Exec("INSERT INTO location_points (ts, lat, lon, source) VALUES ($1, 48.137, 11.575, $2)", noon.Add(time.Duration(k)*15*time.Minute).Unix(), "w"+strconv.Itoa(k))
		}
	}
	for d := 8; d <= 9; d++ {
		night := time.Date(2026, 10, d, 1, 20, 0, 0, time.UTC)
		_ = db.Exec("INSERT INTO location_points (ts, lat, lon, source) VALUES ($1, 52.52, 13.405, 'w')", night.Unix())
	}
	lat, lon, nights, ok := HomeGuess(db, now)
	if !ok || nights != 7 || lat < 48.38 || lat > 48.40 || lon < 10.0 || lon > 10.02 {
		t.Fatalf("%v %v %d %v", lat, lon, nights, ok)
	}
	// nights older than sixty days do not count
	if _, _, _, ok := HomeGuess(db, now.Add(70*24*time.Hour)); ok {
		t.Fatal("old nights counted")
	}
}
