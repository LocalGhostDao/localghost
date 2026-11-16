package hw

import (
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/apparedis"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// A notification carries where a tap takes the phone; the list shows a week and the prune
// deletes what is older.
func TestNotificationLinkAndWeek(t *testing.T) {
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
	_ = admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_notifkeep WITH (FORCE)")
	if err := admin.ExecSimple("CREATE DATABASE lgtest_notifkeep"); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", "lgtest_notifkeep")
	if _, err := ConvergeSchema(db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	// no Redis here: its socket's services.conf is missing, so the list's trim is skipped
	tmp := t.TempDir()
	ns := &NotifStore{rw: map[int]*poltergres.ReadWrite{0: db}, rd: map[int]*apparedis.ReadWrite{},
		pgSocketFor: func(int) string { return tmp + "/postgres" }}
	now := time.Now()
	if _, err := ns.insertPostgres(0, Notification{Service: "ghost.framed", Kind: "highlight", Title: "your week in frames", Body: "b", Link: "map:2026-09-28"}); err != nil {
		t.Fatal(err)
	}
	if _, err := ns.insertPostgres(0, Notification{Service: "ghost.cued", Kind: "reflection", Title: "old", Body: "b", Created: now.Add(-8 * 24 * time.Hour).Unix()}); err != nil {
		t.Fatal(err)
	}
	list, err := ns.List(0, 50)
	if err != nil || len(list) != 1 || list[0].Link != "map:2026-09-28" {
		t.Fatalf("%+v %v", list, err)
	}
	if n, err := ns.PruneNotifications(0, now); err != nil || n != 1 {
		t.Fatalf("pruned %d %v", n, err)
	}
	rows, _ := db.Query("SELECT count(*) FROM notifications")
	if *rows.Vals[0][0] != "1" {
		t.Fatal("the week's one should stay")
	}
}
