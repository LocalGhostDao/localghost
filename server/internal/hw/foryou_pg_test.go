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

// FOR YOU against Postgres: this date in earlier years from the day stories, outings and photos;
// no places without a taste, and says why; the stories that touch the 'me' memories.
func TestForYouNow(t *testing.T) {
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
	_ = admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_foryou WITH (FORCE)")
	if err := admin.ExecSimple("CREATE DATABASE lgtest_foryou"); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", "lgtest_foryou")
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := ConvergeSchema(db, lg); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 2, 12, 0, 0, 0, time.UTC)
	ms := now.UnixMilli()
	for _, q := range [][]any{
		{"INSERT INTO settings (key, value) VALUES ('local_tz', 'Europe/London')"},
		{"INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ('Corfu, the north', 'A day by the sea at Kassiopi. Then dinner.', 'day', 'day:2024-10-02', $1, $1)", ms},
		{"INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ('Antipaxos', 'An outing.', 'outing', 'outing:2024-10-02', $1, $1)", ms},
		{"INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ('A walk', 'Hyde Park.', 'outing', 'outing:2025-10-02', $1, $1)", ms},
		{"INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ('Not today', 'x', 'day', 'day:2025-10-03', $1, $1)", ms},
		{"INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ('Work', 'Vlad builds blockchain data products.', 'me', 'about:me:0', $1, $1)", ms},
		{"INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ('Sailing', 'Vlad sails.', 'distilled', 'chat:1', $1, $1)", ms},
		{"INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ('Sailing plans', 'Vlad plans a trip.', 'distilled', 'chat:2', $1, $1)", ms},
		{"INSERT INTO frames (hash, archive_path, taken_at, kind, place) VALUES ('a', '/x', $1, 'photo', 'Europe / Greece / Corfu / Kassiopi')", time.Date(2023, 10, 2, 22, 30, 0, 0, time.UTC).Unix()},
		{"INSERT INTO frames (hash, archive_path, taken_at, kind, place) VALUES ('b', '/x', $1, 'photo', 'Europe / Greece / Corfu / Kassiopi')", time.Date(2023, 10, 2, 10, 0, 0, 0, time.UTC).Unix()},
		{"INSERT INTO frames (hash, archive_path, taken_at, kind, place) VALUES ('c', '/x', $1, 'photo', '')", time.Date(2023, 10, 2, 23, 30, 0, 0, time.UTC).Unix()},
		{"INSERT INTO location_points (ts, lat, lon) VALUES ($1, 51.5, -0.12)", now.Unix() - 600},
	} {
		if err := db.Exec(q[0].(string), q[1:]...); err != nil {
			t.Fatal(q[0], err)
		}
	}
	nd := &NewsDoc{Stories: []NewsStory{{ID: 9, Title: "Blockchain bill passes", Sources: 4, LastSeen: now.Unix() - 60}}}
	f := ForYouNow(db, nd, now)
	if len(f.Places) != 0 || f.From == 0 || f.Note == "" {
		t.Fatalf("places without a taste: %+v", f)
	}
	// 2025 (the outing), 2024 (the day story, not the outing), 2023 (two photos on the 2nd in
	// London; 23:30 UTC is the 3rd there)
	if len(f.Days) != 3 || f.Days[0].Day != "2025-10-02" || f.Days[0].YearsAgo != 1 || f.Days[0].Title != "A walk" ||
		f.Days[1].Title != "Corfu, the north" || f.Days[1].Lead != "A day by the sea at Kassiopi." ||
		f.Days[2].Day != "2023-10-02" || f.Days[2].Photos != 2 || f.Days[2].Place != "Kassiopi" || f.Days[2].MemoryID != 0 {
		t.Fatalf("days %+v", f.Days)
	}
	if len(f.Stories) != 1 || f.Stories[0].ID != 9 || f.Stories[0].Why != "you mention blockchain" {
		t.Fatalf("stories %+v", f.Stories)
	}
	// a distilled memory comes back, the same one all day
	if f.Remember == nil || f.Remember.Kind != "distilled" {
		t.Fatalf("remember %+v", f.Remember)
	}
	if g := ForYouNow(db, nd, now.Add(time.Hour)); g.Remember == nil || g.Remember.ID != f.Remember.ID {
		t.Fatal("the same memory all day")
	}
	// what the box noticed lately comes first
	if err := db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ('Further on foot','Vlad walked 84 km.','insight','insight:x',$1,$1)", now.UnixMilli()); err != nil {
		t.Fatal(err)
	}
	if g := ForYouNow(db, nd, now); g.Remember == nil || g.Remember.Kind != "insight" {
		t.Fatalf("%+v", g.Remember)
	}
}
