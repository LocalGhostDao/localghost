package main

// The read-again pass against a real Postgres and a stub oracled on the GPU: a distilled memory
// the record supports is confirmed; one it contradicts is put in doubt with the record's words
// beside it and leaves the chat's grounding; a memory whose record is gone is in doubt with no
// words; a person's fact in doubt leaves the rendered body and stays in meta; a hand-edited row
// is never read; a second pass finds nothing and marks the day.
//
//	GHOST_PG_SOCKET_DIR=/tmp GHOST_PG_PORT=55432 GHOST_PG_USER=claude go test -run ReadAgainPG ./cmd/ghost.synthd/

import (
	"context"
	"encoding/json"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
)

func TestReadAgainPG(t *testing.T) {
	db := pgFresh(t, "lg_read_again")
	now := time.Now().UnixMilli()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.Exec("INSERT INTO settings (key, value) VALUES ('owner_name', 'Vlad') ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value"))
	// the records
	must(db.Exec("INSERT INTO journal_entries (source, ref, ts, title, body, created_at, distilled) VALUES ('ghost.noted', 'chat:1', $1, 'Corfu', 'Vlad: we went to Corfu in October with Cristina, five days.\nghost: noted.', $1, TRUE)", now/1000))
	must(db.Exec("INSERT INTO journal_entries (source, ref, ts, title, body, created_at, distilled) VALUES ('ghost.noted', 'chat:2', $1, 'Boat', 'Vlad: the boat is called Wisp and sleeps four.\nghost: noted.', $1, TRUE)", now/1000))
	// the memories: one true, one wrong, one whose record is gone, one hand-edited
	must(db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ('Corfu trip', 'Vlad went to Corfu in October for five days.', 'distilled', 'chat:1', $1, $1)", now-4))
	must(db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ('The boat', 'Vlad''s boat sleeps six.', 'distilled', 'chat:2', $1, $1)", now-3))
	must(db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ('Old note', 'Vlad likes early starts.', 'distilled', 'note:gone', $1, $1)", now-2))
	must(db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at, user_edited) VALUES ('Mine', 'As I wrote it.', 'distilled', 'chat:2', $1, $1, TRUE)", now-1))
	// a person with two facts, one of them wrong
	pm := personMeta{Note: "Cristina is Vlad's wife.", Facts: []personFact{{T: "Cristina was in Corfu with Vlad in October.", Ref: "chat:1"}, {T: "Cristina owns the boat.", Ref: "chat:2"}}}
	must(db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at, meta) VALUES ('Cristina', $2, 'person', 'person:cristina', $1, $1, $3::jsonb)", now, personBody(pm), pm.json()))

	// a stub oracled on the GPU that reads the record like a careful clerk
	dir, err := os.MkdirTemp("/tmp", "lgor")
	if err != nil {
		t.Skip("no short temp dir")
	}
	defer os.RemoveAll(dir)
	srv := ctlsock.NewServer("ghost.oracled", dir, quietLog())
	srv.Handle("models", func(json.RawMessage) (ctlsock.Response, error) {
		return ctlsock.Response{OK: true, Data: json.RawMessage(`{"onGPU":true,"ready":true}`)}, nil
	})
	asked := 0
	srv.Handle("infer", func(args json.RawMessage) (ctlsock.Response, error) {
		var req oracle.Request
		_ = json.Unmarshal(args, &req)
		asked++
		out := "YES"
		switch {
		case strings.Contains(req.Input, "MEMORY: The boat | Vlad's boat sleeps six."):
			out = "NO\nThe record says the boat is called Wisp and sleeps four."
		case strings.Contains(req.Input, "MEMORY: Cristina | Cristina owns the boat."):
			out = "NO\nThe record says the boat is called Wisp; it does not say whose it is."
		case strings.Contains(req.Input, "MEMORY: Mine |"):
			t.Error("a hand-edited memory was read again")
		}
		data, _ := json.Marshal(oracle.Response{Output: out})
		return ctlsock.Response{OK: true, Data: data}, nil
	})
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = srv.Serve(ctx) }()
	defer srv.Cleanup()
	time.Sleep(100 * time.Millisecond)
	oc := oracle.NewClient(dir, 5*time.Second)

	ok, nope, err := readAgainPass(db, oc, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if ok != 2 || nope != 3 {
		t.Fatalf("confirmed %d, doubted %d (asked %d)", ok, nope, asked)
	}
	rows := memRows(t, db, "kind = 'distilled'")
	byTitle := map[string]string{}
	for _, r := range rows {
		byTitle[r[1]] = r[4]
	}
	if !strings.Contains(byTitle["Corfu trip"], `"verified_at"`) || strings.Contains(byTitle["Corfu trip"], "doubt") {
		t.Fatalf("Corfu: %s", byTitle["Corfu trip"])
	}
	if !strings.Contains(byTitle["The boat"], `The record says the boat is called Wisp and sleeps four.`) || !strings.Contains(byTitle["The boat"], `"doubt"`) {
		t.Fatalf("boat: %s", byTitle["The boat"])
	}
	if !strings.Contains(byTitle["Old note"], `"doubt"`) || strings.Contains(byTitle["Old note"], `"found"`) {
		t.Fatalf("gone record: %s", byTitle["Old note"])
	}
	if byTitle["Mine"] != "" {
		t.Fatalf("hand-edited touched: %s", byTitle["Mine"])
	}
	// the person: the wrong fact out of the body, in the meta with its doubt
	prow := memRows(t, db, "kind = 'person'")
	if len(prow) != 1 || strings.Contains(prow[0][3], "owns the boat") || !strings.Contains(prow[0][3], "in Corfu") {
		t.Fatalf("person body: %q", prow[0][3])
	}
	if !strings.Contains(prow[0][4], `The record says the boat is called Wisp; it does not say whose it is.`) || !strings.Contains(prow[0][4], `"doubt"`) {
		t.Fatalf("person meta: %s", prow[0][4])
	}
	// the doubted memory is out of the chat's grounding query
	g, err := db.Query("SELECT title FROM memories WHERE NOT tombstoned AND (meta IS NULL OR NOT jsonb_exists(meta, 'doubt')) AND (title ILIKE '%boat%' OR body ILIKE '%boat%')")
	must(err)
	for _, v := range g.Vals {
		if v[0] != nil && *v[0] == "The boat" {
			t.Fatal("a doubted memory would still reach the chat")
		}
	}
	// a second pass: nothing left to read, the day is marked
	before := asked
	ok, nope, err = readAgainPass(db, oc, quietLog())
	if err != nil || ok != 0 || nope != 0 || asked != before {
		t.Fatalf("second pass: %d %d %v, asked %d → %d", ok, nope, err, before, asked)
	}
	if setting(db, verifyDayKey) != time.Now().Format("2006-01-02") {
		t.Fatal("the day was not marked")
	}
}
