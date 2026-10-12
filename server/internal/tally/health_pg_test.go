package tally_test

import (
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"github.com/LocalGhostDao/localghost/server/internal/tally"
)

// A counter never goes down: a lower step count for a day the box already holds leaves the
// box's (Health Connect's partial view after the Samsung download's full one); a measurement
// takes the latest.
func TestIngestKeepsTheHigherCounter(t *testing.T) {
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
	_ = admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_tally")
	if err := admin.ExecSimple("CREATE DATABASE lgtest_tally"); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", "lgtest_tally")
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := hw.ConvergeSchema(db, lg); err != nil {
		t.Fatal(err)
	}
	if _, err := tally.Ingest(db, []byte(`{"days":[{"day":"2026-10-10","metrics":{"steps":18776,"weight_kg":80.4}}]}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := tally.Ingest(db, []byte(`{"days":[{"day":"2026-10-10","metrics":{"steps":15909,"weight_kg":79.9}}]}`)); err != nil {
		t.Fatal(err)
	}
	st, err := tally.Query(db)
	if err != nil {
		t.Fatal(err)
	}
	if st.LastValues["steps"] != 18776 {
		t.Errorf("steps went down: %v", st.LastValues["steps"])
	}
	if st.LastValues["weight_kg"] != 79.9 {
		t.Errorf("weight did not take the latest: %v", st.LastValues["weight_kg"])
	}
	// and a higher count still replaces a lower one
	if _, err := tally.Ingest(db, []byte(`{"days":[{"day":"2026-10-10","metrics":{"steps":19000}}]}`)); err != nil {
		t.Fatal(err)
	}
	st, _ = tally.Query(db)
	if st.LastValues["steps"] != 19000 {
		t.Errorf("a higher count was refused: %v", st.LastValues["steps"])
	}
}
