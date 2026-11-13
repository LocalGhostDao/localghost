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

// The gallery's pages across a burst. Five photos in one second, pages of two: by the time alone
// ("strictly before the last one's second") the first page took two of them and the next page
// began before that second, so three photos were never listed. With the last row's hash as well,
// every frame is listed, once, in one order.
func TestFramesListPagesThroughABurst(t *testing.T) {
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
	_ = admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_frameslist")
	if err := admin.ExecSimple("CREATE DATABASE lgtest_frameslist"); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", "lgtest_frameslist")
	if _, err := ConvergeSchema(db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	hash := func(i int) string { return "00000000000000000000000000000a" + strconv.FormatInt(int64(16+i), 16) }
	ins := func(i int, takenAt int64) {
		if err := db.Exec("INSERT INTO frames (hash, archive_path, taken_at, kind) VALUES ($1, '/x', $2, 'photo')", hash(i), takenAt); err != nil {
			t.Fatal(err)
		}
	}
	ins(0, 2000)              // a newer photo
	for i := 1; i <= 5; i++ { // the burst: one second
		ins(i, 1000)
	}
	ins(6, 500) // an older one
	tmp := t.TempDir()
	ns := &NotifStore{rw: map[int]*poltergres.ReadWrite{0: db}, rd: map[int]*apparedis.ReadWrite{},
		pgSocketFor: func(int) string { return tmp + "/postgres" }}

	var got []string
	before, beforeHash := int64(0), ""
	for page := 0; page < 10; page++ {
		rows, err := ns.FramesList(0, before, beforeHash, 2)
		if err != nil {
			t.Fatal(err)
		}
		if len(rows) == 0 {
			break
		}
		for _, r := range rows {
			got = append(got, r.Hash)
		}
		last := rows[len(rows)-1]
		before, beforeHash = last.TakenAt, last.Hash
	}
	want := []string{hash(0), hash(5), hash(4), hash(3), hash(2), hash(1), hash(6)}
	if len(got) != len(want) {
		t.Fatalf("listed %d of %d frames: %v", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("order at %d: %v", i, got)
		}
	}
	// an app from before the hash was sent: the old rule, which loses the burst's rest but never
	// repeats a frame
	rows, err := ns.FramesList(0, 1000, "", 10)
	if err != nil || len(rows) != 1 || rows[0].Hash != hash(6) {
		t.Fatalf("the old cursor: %v %+v", err, rows)
	}
}
