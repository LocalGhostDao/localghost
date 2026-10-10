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

// A geo set imported before the populations (rows, every one at 0) is told apart from an empty
// set and from one with the column filled: that is what sends framed back to the files at start.
func TestGeoWithoutPopulations(t *testing.T) {
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
	_ = admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_geopop")
	if err := admin.ExecSimple("CREATE DATABASE lgtest_geopop"); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", "lgtest_geopop")
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := hw.ConvergeSchema(db, lg); err != nil {
		t.Fatal(err)
	}
	s := NewStoreDB(db)
	if s.GeoWithoutPopulations() {
		t.Fatal("an empty set is not one without populations")
	}
	if err := db.Exec("INSERT INTO geo_points (geonameid,name,lat,lon,kind) VALUES (1,'Neu-Ulm',48.39,10.01,'P'), (2,'Ulm',48.4,9.99,'P')"); err != nil {
		t.Fatal(err)
	}
	if !s.GeoWithoutPopulations() {
		t.Fatal("rows at population 0: the set is from before the populations")
	}
	if err := db.Exec("UPDATE geo_points SET population = 126000 WHERE geonameid = 2"); err != nil {
		t.Fatal(err)
	}
	if s.GeoWithoutPopulations() {
		t.Fatal("one population filled: the set has them")
	}
}
