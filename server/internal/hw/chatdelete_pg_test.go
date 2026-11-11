package hw

import (
	"io"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"testing"

	"github.com/LocalGhostDao/localghost/server/internal/apparedis"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// Deleting a chat takes its copy out of the journal and the sentences the read-again pass
// quoted from it; what was made from it stays, marked, for the person to keep or delete.
func TestChatDeleteAgainstPostgres(t *testing.T) {
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
	_ = admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_chatdelete")
	if err := admin.ExecSimple("CREATE DATABASE lgtest_chatdelete"); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", "lgtest_chatdelete")
	if _, err := ConvergeSchema(db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	ns := &NotifStore{rw: map[int]*poltergres.ReadWrite{0: db}, rd: map[int]*apparedis.ReadWrite{}, pgSocketFor: func(int) string { return t.TempDir() }}
	must := func(err error) {
		t.Helper()
		if err != nil {
			t.Fatal(err)
		}
	}
	rows, err := db.Query("INSERT INTO chats (title, created_at, updated_at) VALUES ('the diagnosis', 1, 1) RETURNING id")
	must(err)
	id, _ := strconv.ParseInt(*rows.Vals[0][0], 10, 64)
	ref := "chat:" + *rows.Vals[0][0]
	must(db.Exec("INSERT INTO chat_messages (chat_id, role, content, ts) VALUES ($1, 'user', 'the consultant said it is early', 1)", id))
	must(db.Exec("INSERT INTO journal_entries (source, ref, ts, title, body, created_at) VALUES ('ghost.noted', $1, 1, 'the diagnosis', 'Vlad: the consultant said it is early', 1)", ref))
	must(db.Exec("INSERT INTO journal_entries (source, ref, ts, title, body, created_at) VALUES ('ghost.noted', 'chat:999999', 1, 'another', 'kept', 1)"))
	must(db.Exec(`INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at, meta) VALUES ('Health', 'It is late.', 'distilled', $1, 1, 1, $2::jsonb)`,
		ref, `{"verified_at":5,"doubt":{"at":5,"found":"The record says the consultant said it is early.","ref":"`+ref+`"}}`))
	must(db.Exec(`INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at, meta) VALUES ('Ana', 'x', 'person', 'person:ana', 1, 1, $1::jsonb)`,
		`{"facts":[{"t":"Ana came to the appointment.","ref":"`+ref+`","v":5,"doubt":"The record says the consultant said it is early."},{"t":"Ana paints.","ref":"chat:999999","v":5,"doubt":"kept as it is"}]}`))

	ok, err := ns.ChatDelete(0, id)
	if err != nil || !ok {
		t.Fatalf("delete: %v %v", ok, err)
	}
	if rows, _ := db.Query("SELECT 1 FROM journal_entries WHERE ref = $1", ref); len(rows.Vals) != 0 {
		t.Fatal("the transcript is still in the journal")
	}
	if rows, _ := db.Query("SELECT 1 FROM journal_entries WHERE ref = 'chat:999999'"); len(rows.Vals) != 1 {
		t.Fatal("another chat's entry went too")
	}
	rows, err = db.Query("SELECT title, meta::text FROM memories ORDER BY id")
	must(err)
	if len(rows.Vals) != 2 {
		t.Fatalf("memories: %d", len(rows.Vals))
	}
	for _, v := range rows.Vals {
		if strings.Contains(*v[1], "consultant") {
			t.Fatalf("%s still quotes the deleted chat: %s", *v[0], *v[1])
		}
	}
	if !strings.Contains(*rows.Vals[0][1], `"doubt"`) || !strings.Contains(*rows.Vals[1][1], factDoubtPlain) || !strings.Contains(*rows.Vals[1][1], "kept as it is") {
		t.Fatalf("the doubts: %s | %s", *rows.Vals[0][1], *rows.Vals[1][1])
	}
}
