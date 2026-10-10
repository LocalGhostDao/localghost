package hw

import (
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"

	"github.com/LocalGhostDao/localghost/server/internal/apparedis"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// The memories feed keeps every whole memory (a trip from June) ahead of the parts folded under
// trips, so a feed cut at its limit loses old parts first, never a trip; a whole's parts come by
// ref, oldest first; a trip's ref is given to the app.
func TestMemoriesListWholeFirstAndPartsByRef(t *testing.T) {
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
	_ = admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_memlist")
	if err := admin.ExecSimple("CREATE DATABASE lgtest_memlist"); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", "lgtest_memlist")
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := ConvergeSchema(db, lg); err != nil {
		t.Fatal(err)
	}
	ins := func(title, kind, ref, meta string, created int64) {
		if err := db.Exec("INSERT INTO memories (title, body, kind, source_ref, meta, created_at, updated_at) VALUES ($1,'b',$2,$3,$4::jsonb,$5,$5)", title, kind, ref, meta, created); err != nil {
			t.Fatal(err)
		}
	}
	// an old trip, its two parts, and three newer whole days
	ins("Canada, 6 June to 18 July 2026", "trip", "trip:2026-06-06", `{"outings":["outing:2026-06-06"]}`, 1_000)
	ins("Toronto", "outing", "outing:2026-06-06", `{"part_of":"trip:2026-06-06"}`, 900)
	ins("a day in Toronto", "day", "day:2026-06-07", `{"part_of":"trip:2026-06-06"}`, 950)
	ins("a day at home", "day", "day:2026-10-01", `{}`, 2_000)
	ins("another day at home", "day", "day:2026-10-02", `{}`, 3_000)
	ins("a fact", "distilled", "chat:7", `{}`, 4_000)
	tmp := t.TempDir()
	ns := &NotifStore{rw: map[int]*poltergres.ReadWrite{0: db}, rd: map[int]*apparedis.ReadWrite{},
		pgSocketFor: func(int) string { return tmp + "/postgres" }}
	// cut at four: the four wholes, newest first; the trip's parts are past the cut
	rows, err := ns.MemoriesList(0, 4)
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 4 || rows[0].Title != "a fact" || rows[3].Kind != "trip" || rows[3].Ref != "trip:2026-06-06" {
		t.Fatalf("feed: %+v", rows)
	}
	if rows[0].Ref != "" {
		t.Fatalf("a chat's ref given to the app: %q", rows[0].Ref)
	}
	// the whole feed: the parts after the wholes
	rows, err = ns.MemoriesList(0, 0)
	if err != nil || len(rows) != 6 || rows[4].Title != "a day in Toronto" || rows[5].Title != "Toronto" {
		t.Fatalf("whole feed: %v %+v", err, rows)
	}
	// the trip's parts by ref, oldest first
	parts, err := ns.MemoriesPartsOf(0, "trip:2026-06-06")
	if err != nil || len(parts) != 2 || parts[0].Title != "Toronto" || parts[1].Ref != "day:2026-06-07" {
		t.Fatalf("parts: %v %+v", err, parts)
	}
}
