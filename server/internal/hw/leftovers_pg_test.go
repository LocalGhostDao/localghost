package hw

import (
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// A box that had the paper sign-ins, the unreadable mark and the resting calorie estimate loses
// them, once, at its next convergence; frames and the measured health days keep their rows.
func TestRemovedFeaturesDropped(t *testing.T) {
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
	_ = admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_leftovers WITH (FORCE)")
	if err := admin.ExecSimple("CREATE DATABASE lgtest_leftovers"); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", "lgtest_leftovers")
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := ConvergeSchema(db, lg); err != nil {
		t.Fatal(err)
	}
	// the box as an earlier build left it: the table, the column, migration 2 not yet run
	for _, q := range []string{
		"CREATE TABLE news_logins (domain TEXT PRIMARY KEY, cookie TEXT NOT NULL DEFAULT '')",
		"ALTER TABLE frames ADD COLUMN unreadable TEXT NOT NULL DEFAULT ''",
		"INSERT INTO frames (hash, archive_path, kind) VALUES ('aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa', '/p', 'photo')",
		"DELETE FROM schema_migrations WHERE version IN (2, 3, 4)",
		"INSERT INTO notifications (service, kind, title, body) VALUES ('ghost.framed','highlight','your week in frames','Monday was the big one'), ('ghost.framed','highlight','your week in frames','Monday was the big one'), ('ghost.cued','reflection','a day','x'), ('ghost.cued','reflection','a day','x')",
		"INSERT INTO health_metrics (day, metric, value) VALUES ('2026-09-01','calories',1564.5), ('2026-09-01','steps',23213)",
		"INSERT INTO journal_entries (source, ref, ts, title, body, created_at) VALUES ('ghost.tallyd','health:2026-09-01',1,'health , 2026-09-01','23213 steps. 1564 kcal. Avg heart rate 70.',1)",
	} {
		if err := db.Exec(q); err != nil {
			t.Fatal(q, err)
		}
	}
	if _, err := ConvergeSchema(db, lg); err != nil {
		t.Fatal(err)
	}
	one := func(q string) string {
		rows, err := db.Query(q)
		if err != nil || len(rows.Vals) != 1 || rows.Vals[0][0] == nil {
			t.Fatal(q, err)
		}
		return *rows.Vals[0][0]
	}
	if n := one("SELECT count(*) FROM information_schema.tables WHERE table_name = 'news_logins'"); n != "0" {
		t.Fatal("news_logins is still there")
	}
	if n := one("SELECT count(*) FROM information_schema.columns WHERE table_name = 'frames' AND column_name = 'unreadable'"); n != "0" {
		t.Fatal("frames.unreadable is still there")
	}
	if n := one("SELECT count(*) FROM frames"); n != "1" {
		t.Fatal("frames lost its rows")
	}
	if n := one("SELECT count(*) FROM schema_migrations WHERE version IN (2, 3, 4)"); n != "3" {
		t.Fatal("migrations 2 to 4 not recorded")
	}
	// the repeated weekly highlight is one now; two of anything else stay two
	if n := one("SELECT count(*) FROM notifications WHERE kind = 'highlight'"); n != "1" {
		t.Fatal("highlights", n)
	}
	if n := one("SELECT count(*) FROM notifications WHERE kind = 'reflection'"); n != "2" {
		t.Fatal("reflections", n)
	}
	// the resting estimate is gone, the steps and the rest of the day's line stay
	if n := one("SELECT string_agg(metric, ',') FROM health_metrics"); n != "steps" {
		t.Fatal(n)
	}
	if b := one("SELECT body FROM journal_entries WHERE ref = 'health:2026-09-01'"); b != "23213 steps. Avg heart rate 70." {
		t.Fatalf("%q", b)
	}
}
