package hw

import (
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// FrameKinds names each known frame photo or video and leaves unknown hashes out.
func TestFrameKinds(t *testing.T) {
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
	_ = admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_framekinds WITH (FORCE)")
	if err := admin.ExecSimple("CREATE DATABASE lgtest_framekinds"); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", "lgtest_framekinds")
	if _, err := ConvergeSchema(db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	p, v := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := db.Exec(`INSERT INTO frames (hash, archive_path, kind) VALUES ($1, '/p', 'photo'), ($2, '/v', 'video')`, p, v); err != nil {
		t.Fatal(err)
	}
	ns := &NotifStore{rw: map[int]*poltergres.ReadWrite{0: db}}
	got, err := ns.FrameKinds(0, []string{p, v, "cccccccccccccccccccccccccccccccc"})
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 2 || got[p] != "photo" || got[v] != "video" {
		t.Fatalf("%v", got)
	}
	if got, err := ns.FrameKinds(0, nil); err != nil || len(got) != 0 {
		t.Fatalf("empty: %v %v", got, err)
	}
}
