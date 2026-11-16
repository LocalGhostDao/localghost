package framed

import (
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// A "no" deletes the points asked about (from every source) and nothing else; a "yes" is
// remembered for the stretch and overlapping ranges find it.
func TestTrailAnswersInTheStore(t *testing.T) {
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
	_ = admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_trailanswers WITH (FORCE)")
	if err := admin.ExecSimple("CREATE DATABASE lgtest_trailanswers"); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", "lgtest_trailanswers")
	if _, err := hw.ConvergeSchema(db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	s := NewStoreDB(db)
	if err := s.InsertPoints("phone-ab", []TrackPoint{{TS: 100, Lat: 1, Lon: 1}, {TS: 200, Lat: 5, Lon: 5}, {TS: 300, Lat: 1, Lon: 1}}); err != nil {
		t.Fatal(err)
	}
	if err := s.InsertPoints("watch", []TrackPoint{{TS: 200, Lat: 5, Lon: 5}}); err != nil {
		t.Fatal(err)
	}
	n, err := s.DeletePoints([]int64{200})
	if err != nil || n != 2 {
		t.Fatalf("deleted %d: %v", n, err)
	}
	left, _ := s.DayPoints(0, 1000)
	if len(left) != 2 || left[0].TS != 100 || left[1].TS != 300 {
		t.Fatalf("left %+v", left)
	}
	if err := s.KeepStretch(150, 250); err != nil {
		t.Fatal(err)
	}
	if err := s.KeepStretch(150, 250); err != nil {
		t.Fatalf("a second yes: %v", err)
	}
	if k, _ := s.KeptStretches(0, 1000); len(k) != 1 || k[0] != [2]int64{150, 250} {
		t.Fatalf("kept %v", k)
	}
	if k, _ := s.KeptStretches(260, 1000); len(k) != 0 {
		t.Fatalf("a range after it found it: %v", k)
	}
}
