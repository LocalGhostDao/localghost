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

// The person's mark and the company a memory keeps, at the store: an anchor is set and lifted
// by the person and comes first in the feed, past any limit; opening a memory touches it and
// returns its neighbours with why; deleting it drops its links at once and takes its mark; an
// anchor, an edit and a delete each empty the front page so it is chosen again; the feed marks
// the memories on today's page.
func TestMemoryAnchorOpenAndLinks(t *testing.T) {
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
	_ = admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_memanchor")
	if err := admin.ExecSimple("CREATE DATABASE lgtest_memanchor"); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", "lgtest_memanchor")
	if _, err := ConvergeSchema(db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	ins := func(title, kind, ref string, created int64) int64 {
		rows, err := db.Query("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ($1,'b',$2,$3,$4,$4) RETURNING id", title, kind, ref, created)
		if err != nil || len(rows.Vals) != 1 {
			t.Fatal(err)
		}
		id, _ := strconv.ParseInt(*rows.Vals[0][0], 10, 64)
		return id
	}
	wake := ins("Bogdan's wake", "user", "", 1_000) // the oldest row on the box
	mircea := ins("Mircea", "person", "person:mircea", 2_000)
	day := ins("Friday 3 October 2026", "day", "day:2026-10-03", 3_000)
	fact := ins("a fact", "distilled", "chat:7", 4_000)
	link := func(a, b int64, why string) {
		if a > b {
			a, b = b, a
		}
		if err := db.Exec("INSERT INTO memory_links (a, b, why, made_at) VALUES ($1,$2,$3,1)", a, b, why); err != nil {
			t.Fatal(err)
		}
	}
	link(wake, mircea, "names")
	link(mircea, day, "names")
	tmp := t.TempDir()
	ns := &NotifStore{rw: map[int]*poltergres.ReadWrite{0: db}, rd: map[int]*apparedis.ReadWrite{},
		pgSocketFor: func(int) string { return tmp + "/postgres" }}
	one := func(q string, args ...any) string {
		rows, err := db.Query(q, args...)
		if err != nil || len(rows.Vals) != 1 || rows.Vals[0][0] == nil {
			t.Fatalf("%s: %v %+v", q, err, rows)
		}
		return *rows.Vals[0][0]
	}

	// no pass has run and nobody has marked anything: no anchors
	rows, err := ns.MemoriesList(0, 0)
	if err != nil || len(rows) != 4 || rows[0].Title != "a fact" || rows[0].Anchor || rows[3].Anchor {
		t.Fatalf("before: %v %+v", err, rows)
	}
	// the person anchors the oldest memory: it is first, and a feed cut at one still has it
	if err := ns.SetSetting(0, FrontPageKey, `{"day":"d","sig":"s","ids":[`+strconv.FormatInt(fact, 10)+`],"text":"t"}`); err != nil {
		t.Fatal(err)
	}
	if rows, _ = ns.MemoriesList(0, 0); !rows[0].Front || rows[1].Front {
		t.Fatalf("the front page's memory is not marked: %+v", rows)
	}
	if ok, err := ns.MemoryAnchor(0, wake, true); err != nil || !ok {
		t.Fatalf("anchor: %v %v", ok, err)
	}
	if v, _ := ns.GetSetting(0, FrontPageKey); v != "" {
		t.Fatalf("an anchor did not empty the front page: %q", v)
	}
	rows, err = ns.MemoriesList(0, 1)
	if err != nil || len(rows) != 1 || rows[0].ID != wake || !rows[0].Anchor {
		t.Fatalf("the anchor is not first past the limit: %v %+v", err, rows)
	}
	// marking it again keeps the first moment; a memory that is not there is not marked
	at := one("SELECT anchored_at FROM memories WHERE id = $1", wake)
	if ok, _ := ns.MemoryAnchor(0, wake, true); !ok || one("SELECT anchored_at FROM memories WHERE id = $1", wake) != at {
		t.Fatal("marking an anchor twice moved its moment")
	}
	if ok, _ := ns.MemoryAnchor(0, 99999, true); ok {
		t.Fatal("anchored a memory that does not exist")
	}

	// opening a memory touches it and gives its company, the anchor first
	links, ok, err := ns.MemoryOpen(0, mircea)
	if err != nil || !ok || len(links) != 2 || links[0].ID != wake || !links[0].Anchor || links[0].Why != "names" ||
		links[1].ID != day || links[1].Ref != "day:2026-10-03" {
		t.Fatalf("open: %v %v %+v", ok, err, links)
	}
	if one("SELECT touched_at FROM memories WHERE id = $1", mircea) == "0" || one("SELECT touched_at FROM memories WHERE id = $1", day) != "0" {
		t.Fatal("opening touched the wrong memory, or none")
	}
	if _, ok, _ := ns.MemoryOpen(0, 99999); ok {
		t.Fatal("opened a memory that does not exist")
	}

	// an edit empties the front page too
	_ = ns.SetSetting(0, FrontPageKey, `{"ids":[1]}`)
	if ok, _ := ns.MemoryEdit(0, fact, "a fact", "in my words"); !ok {
		t.Fatal("edit")
	}
	if v, _ := ns.GetSetting(0, FrontPageKey); v != "" {
		t.Fatal("an edit did not empty the front page")
	}

	// deleting the anchored memory: its links go now, its mark with it, the page is emptied
	_ = ns.SetSetting(0, FrontPageKey, `{"ids":[1]}`)
	if ok, err := ns.MemoryTombstone(0, wake); err != nil || !ok {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if n := one("SELECT count(*) FROM memory_links WHERE a = $1 OR b = $1", wake); n != "0" {
		t.Fatalf("a deleted memory kept %s links", n)
	}
	if one("SELECT count(*) FROM memory_links") != "1" {
		t.Fatal("the delete took a link that was not its own")
	}
	if one("SELECT (anchored_at IS NULL)::text FROM memories WHERE id = $1", wake) != "true" {
		t.Fatal("a deleted memory kept its anchor")
	}
	if v, _ := ns.GetSetting(0, FrontPageKey); v != "" {
		t.Fatal("a delete did not empty the front page")
	}
	if links, _, _ = ns.MemoryOpen(0, mircea); len(links) != 1 || links[0].ID != day {
		t.Fatalf("a deleted memory is still in a neighbourhood: %+v", links)
	}
	// and a deleted memory cannot be anchored or opened
	if ok, _ := ns.MemoryAnchor(0, wake, true); ok {
		t.Fatal("anchored a deleted memory")
	}
	// ONE MEMORY BY ID, and a whole by its ref: the page reads its own row, not the feed
	whole := ins("August 2026", "month", "month:2026-08", 500)
	if err := db.Exec("UPDATE memories SET meta = '{\"part_of\":\"month:2026-08\"}'::jsonb WHERE id = $1", day); err != nil {
		t.Fatal(err)
	}
	_ = ns.SetSetting(0, FrontPageKey, `{"ids":[`+strconv.FormatInt(day, 10)+`]}`)
	got, err := ns.MemoryByID(0, day)
	if err != nil || len(got) != 1 || got[0].ID != day || got[0].Ref != "day:2026-10-03" || !got[0].Front {
		t.Fatalf("by id: %v %+v", err, got)
	}
	if feed, _ := ns.MemoriesList(0, 2); len(feed) != 2 || feed[0].ID == day || feed[1].ID == day {
		t.Fatalf("the part should be past a feed cut at two: %+v", feed)
	}
	if got, _ = ns.MemoryByID(0, wake); len(got) != 0 {
		t.Fatal("a deleted memory was read by id")
	}
	if got, err = ns.MemoryByRef(0, "month:2026-08"); err != nil || len(got) != 1 || got[0].ID != whole || got[0].Ref != "month:2026-08" {
		t.Fatalf("by ref: %v %+v", err, got)
	}
	if got, _ = ns.MemoryByRef(0, "chat:7"); len(got) != 0 {
		t.Fatal("a ref that is not a whole's was answered")
	}
	// lifting a mark
	if ok, _ := ns.MemoryAnchor(0, mircea, true); !ok {
		t.Fatal("anchor mircea")
	}
	if ok, _ := ns.MemoryAnchor(0, mircea, false); !ok || one("SELECT (anchored_at IS NULL)::text FROM memories WHERE id = $1", mircea) != "true" {
		t.Fatal("the mark was not lifted")
	}
}
