package main

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

func answersDB(t *testing.T) *poltergres.ReadWrite {
	t.Helper()
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
	if err := admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_answers WITH (FORCE)"); err != nil {
		t.Fatal(err)
	}
	if err := admin.ExecSimple("CREATE DATABASE lgtest_answers"); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", "lgtest_answers")
	if _, err := hw.ConvergeSchema(db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("schema: %v", err)
	}
	chatDBOnce.Do(func() {})
	chatDB = db
	return db
}

func row(t *testing.T, db *poltergres.ReadWrite, id int64) (content, reasoning, sources, state string) {
	t.Helper()
	r, err := db.Query(`SELECT content, reasoning, sources, state FROM chat_messages WHERE id = $1`, strconv.FormatInt(id, 10))
	if err != nil || len(r.Vals) != 1 {
		t.Fatalf("row %d: %v", id, err)
	}
	return *r.Vals[0][0], *r.Vals[0][1], *r.Vals[0][2], *r.Vals[0][3]
}

// The answer's row exists from the first moment, fills in while the model writes, and ends "done"
// with its thinking and sources; the next question's history leaves out an answer still being
// written; a synthd restart settles answers left mid-sentence.
func TestAnswerIsSavedWhileItIsWritten(t *testing.T) {
	db := answersDB(t)
	chatID := chatPersist("", 0, "user", "what is the btc to usd rate?")
	if chatID == 0 {
		t.Fatal("no chat")
	}
	src := sourcesJSON([]webHit{{Title: "BTC price", URL: "https://example.org/btc", Kind: "page", Fetched: "2026-09-30 00:59 UTC"}})
	msgID := chatStartAnswer("", chatID, src)
	if msgID == 0 {
		t.Fatal("no answer row")
	}
	if c, _, s, st := row(t, db, msgID); c != "" || st != "writing" || s != src {
		t.Fatalf("started: %q %q %q", c, st, s)
	}
	if h := chatHistory("", chatID); len(h) != 1 || h[0].Role != "user" {
		t.Fatalf("an answer being written is not history yet: %+v", h)
	}

	sv := newAnswerSaver("", chatID, msgID)
	sv.update("Bitcoin is at", "the rate changes by the minute", "")
	deadline := time.Now().Add(5 * time.Second)
	for {
		if c, _, _, _ := row(t, db, msgID); c == "Bitcoin is at" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the partial answer was not saved within 5 s")
		}
		time.Sleep(200 * time.Millisecond)
	}
	sv.update("Bitcoin is at $83,611 [1].", "the rate changes by the minute", "done")
	sv.wait(3 * time.Second)
	if c, r, _, st := row(t, db, msgID); c != "Bitcoin is at $83,611 [1]." || r != "the rate changes by the minute" || st != "done" {
		t.Fatalf("done: %q %q %q", c, r, st)
	}
	if h := chatHistory("", chatID); len(h) != 2 || h[1].Content != "Bitcoin is at $83,611 [1]." {
		t.Fatalf("history after done: %+v", h)
	}

	// a synthd that died mid-answer leaves "writing"; the next one settles it
	orphan := chatStartAnswer("", chatID, "")
	settleOrphans("")
	if _, _, _, st := row(t, db, orphan); st != "stopped" {
		t.Fatalf("orphan left %q", st)
	}
}

// A saved chat's answer runs on the box's clock: the phone's connection ending does not stop
// it, STOP does. An incognito one still ends with the connection.
func TestAnswerOutlivesThePhoneButNotStop(t *testing.T) {
	phone, hangUp := context.WithCancel(context.Background())
	ctx, end := answerContext(phone, 42, true)
	defer end()
	hangUp()
	select {
	case <-ctx.Done():
		t.Fatal("the phone going away stopped a saved chat's answer")
	case <-time.After(100 * time.Millisecond):
	}
	if !stopAnswer(42) {
		t.Fatal("STOP found nothing running")
	}
	select {
	case <-ctx.Done():
	case <-time.After(time.Second):
		t.Fatal("STOP did not end the answer")
	}
	if stopAnswer(42) {
		t.Fatal("stopped twice")
	}

	phone2, hangUp2 := context.WithCancel(context.Background())
	ctx2, end2 := answerContext(phone2, 0, false)
	defer end2()
	hangUp2()
	select {
	case <-ctx2.Done():
	case <-time.After(time.Second):
		t.Fatal("an incognito answer outlived its connection")
	}

	// a newer question in the same chat supersedes the older answer
	a, endA := answerContext(context.Background(), 7, true)
	_, endB := answerContext(context.Background(), 7, true)
	select {
	case <-a.Done():
	case <-time.After(time.Second):
		t.Fatal("the older answer was not superseded")
	}
	endA() // the old one's end must not unregister the new one
	if !stopAnswer(7) {
		t.Fatal("the newer answer lost its STOP")
	}
	endB()
}

// What ghost.shadowd named about a chat is said at the top of the next answer's trail, once:
// what was named since the person last wrote there, not what was muted, not another chat's.
func TestTheNextAnswerSaysWhatTheArbiterNamed(t *testing.T) {
	db := answersDB(t)
	chatID := chatPersist("", 0, "user", "should I send the email?")
	if got := shadowSaid(db, chatID); len(got) != 0 {
		t.Fatalf("nothing named yet: %v", got)
	}
	named := func(subject, title string, muted bool, at time.Time) {
		t.Helper()
		if err := db.Exec(`INSERT INTO shadow_observations (detector, subject, ts, title, muted) VALUES ('cold-read', $1, $2, $3, $4)`,
			subject, at.Unix(), title, muted); err != nil {
			t.Fatal(err)
		}
	}
	soon := time.Now().Add(time.Minute)
	named(fmt.Sprintf("chat:%d/msg:2", chatID), "read cold, the box answered differently", false, soon)
	named(fmt.Sprintf("chat:%d/caved/4", chatID), "the box changed its answer when you pushed", true, soon)
	named(fmt.Sprintf("chat:%d9/msg:7", chatID), "another chat's", false, soon)
	named("mail:3", "this mail leans on pressure", false, soon)
	got := shadowSaid(db, chatID)
	if len(got) != 1 || got[0] != "ghost.shadowd, about an earlier answer in this chat: read cold, the box answered differently (its notification has both sides)" {
		t.Fatalf("said: %q", got)
	}
	// the person writes again, later than the observation: it is not said a second time
	if err := db.Exec(`INSERT INTO chat_messages (chat_id, role, content, ts) VALUES ($1, 'user', 'ok', $2)`,
		strconv.FormatInt(chatID, 10), strconv.FormatInt(soon.Add(time.Minute).UnixMilli(), 10)); err != nil {
		t.Fatal(err)
	}
	if got := shadowSaid(db, chatID); len(got) != 0 {
		t.Fatalf("said twice: %v", got)
	}
	if got := shadowSaid(db, 0); got != nil {
		t.Fatal("an unsaved chat has nothing named")
	}
}
