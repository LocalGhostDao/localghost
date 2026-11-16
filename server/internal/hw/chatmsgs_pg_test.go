package hw

import (
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// A chat's messages carry an answer's thinking, sources and state to the app; a finished answer
// reports no state (it is the normal case).
func TestChatMessagesCarryThinkingSourcesAndState(t *testing.T) {
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
	_ = admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_chatmsgs WITH (FORCE)")
	if err := admin.ExecSimple("CREATE DATABASE lgtest_chatmsgs"); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", "lgtest_chatmsgs")
	if _, err := ConvergeSchema(db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatal(err)
	}
	must := func(q string, a ...any) {
		if err := db.Exec(q, a...); err != nil {
			t.Fatal(err)
		}
	}
	must(`INSERT INTO chat_messages (chat_id, role, content, ts) VALUES (1, 'user', 'how about now?', 1)`)
	must(`INSERT INTO chat_messages (chat_id, role, content, ts, reasoning, sources, state) VALUES (1, 'assistant', 'At $83,611 [1].', 2, 'they mean the price', $1, 'done')`,
		`[{"n":1,"title":"BTC","url":"https://example.org"}]`)
	must(`INSERT INTO chat_messages (chat_id, role, content, ts, state) VALUES (1, 'assistant', 'Half an ans', 3, 'writing')`)
	ns := &NotifStore{rw: map[int]*poltergres.ReadWrite{0: db}}
	msgs, err := ns.ChatMessages(0, 1, 10, 0)
	if err != nil || len(msgs) != 3 {
		t.Fatalf("%v %v", msgs, err)
	}
	if msgs[0].State != "writing" || msgs[0].Content != "Half an ans" {
		t.Fatalf("newest: %+v", msgs[0])
	}
	if msgs[1].State != "" || msgs[1].Reasoning != "they mean the price" || string(msgs[1].Sources) != `[{"n":1,"title":"BTC","url":"https://example.org"}]` {
		t.Fatalf("done answer: %+v", msgs[1])
	}
	if msgs[2].Role != "user" || msgs[2].Sources != nil {
		t.Fatalf("question: %+v", msgs[2])
	}
}
