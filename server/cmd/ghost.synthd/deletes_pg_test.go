package main

// What a delete reaches (audit run 2): the day's telling does not keep the words of a deleted
// voice note or chat, a day with nothing left is not told at all, and a memory the person
// deleted is not written again when the about note is read.
//
//	GHOST_PG_SOCKET_DIR=/tmp GHOST_PG_PORT=55432 GHOST_PG_USER=claude go test -run DeletesPG ./cmd/ghost.synthd/

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/oracle"
)

func TestDeletesPGReachTheDayStory(t *testing.T) {
	db := pgFresh(t, "lgtest_synthd_deletes")
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	day := time.Now().UTC().AddDate(0, 0, -3)
	dayStr := day.Format("2006-01-02")
	t0 := time.Date(day.Year(), day.Month(), day.Day(), 9, 0, 0, 0, time.UTC).Unix()
	exec := func(q string, args ...any) {
		t.Helper()
		if err := db.Exec(q, args...); err != nil {
			t.Fatalf("%v\n  %s", err, q)
		}
	}
	exec(`INSERT INTO journal_entries (source, ref, ts, title, body, created_at) VALUES ('ghost.voiced','voice:aa',$1,'Voice note: The results came back','Said on `+dayStr+`:'||chr(10)||'The results came back and it is not good news.',$1)`, t0+3600)
	exec(`INSERT INTO chats (title, created_at, updated_at) VALUES ('what the consultant said',$1,$1)`, (t0+5400)*1000)
	oc := oracle.NewClient(t.TempDir(), time.Second) // no oracled: the template path
	mount := t.TempDir()
	if _, _, err := daySummaryPass(db, oc, mount, lg, dayStr); err != nil {
		t.Fatal(err)
	}
	// as if the model had written the day from that sheet
	told := "A hard day. The results came back and it is not good news; there was a chat about what the consultant said."
	exec(`UPDATE day_summaries SET summary = $2, written_by = 'model', model_at = $3 WHERE day = $1`, dayStr, told, time.Now().UnixMilli())
	exec(`UPDATE memories SET body = $2 WHERE kind = 'day' AND source_ref = $1`, "day:"+dayStr, told)

	// the chat is deleted: the day is told again without the model's old text, at once
	exec(`DELETE FROM chats`)
	if _, _, err := daySummaryPass(db, oc, mount, lg, dayStr); err != nil {
		t.Fatal(err)
	}
	rows, err := db.Query(`SELECT summary, written_by, facts::text FROM day_summaries WHERE day = $1`, dayStr)
	if err != nil || len(rows.Vals) != 1 {
		t.Fatalf("row: %v", err)
	}
	if got := *rows.Vals[0][0]; strings.Contains(got, "consultant") || *rows.Vals[0][1] == "model" {
		t.Fatalf("the telling kept the deleted chat: %q (by %s)", got, *rows.Vals[0][1])
	}
	if strings.Contains(*rows.Vals[0][2], "consultant") {
		t.Fatalf("the facts kept the deleted chat: %s", *rows.Vals[0][2])
	}

	// the voice note is deleted too: nothing is left, and the day is not told at all
	exec(`DELETE FROM journal_entries`)
	if _, _, err := daySummaryPass(db, oc, mount, lg, dayStr); err != nil {
		t.Fatal(err)
	}
	if rows, _ := db.Query(`SELECT 1 FROM day_summaries WHERE day = $1`, dayStr); len(rows.Vals) != 0 {
		t.Fatal("a day with nothing left is still told")
	}
	if rows, _ := db.Query(`SELECT body FROM memories WHERE kind = 'day' AND source_ref = $1`, "day:"+dayStr); len(rows.Vals) != 0 {
		t.Fatalf("the day's memory stayed: %q", *rows.Vals[0][0])
	}
}

func TestWordsTakenBack(t *testing.T) {
	old := `{"day":"2026-11-01","notes":["Swam"],"spoken":["The water was cold."],"chats":["ferry times"],"events":["Dentist"],"why":"tired"}`
	same := &dayFacts{Notes: []string{"Swam"}, Spoken: []string{"The water was cold."}, Chats: []string{"ferry times"}, Events: []string{"Dentist"}, Why: "tired"}
	if wordsTakenBack(old, same) {
		t.Fatal("nothing was taken back")
	}
	more := *same
	more.Notes = append([]string{"A second note"}, same.Notes...)
	if wordsTakenBack(old, &more) {
		t.Fatal("an added note is not a removal")
	}
	for name, f := range map[string]*dayFacts{
		"voice": {Notes: same.Notes, Chats: same.Chats, Events: same.Events, Why: "tired"},
		"chat":  {Notes: same.Notes, Spoken: same.Spoken, Events: same.Events, Why: "tired"},
		"event": {Notes: same.Notes, Spoken: same.Spoken, Chats: same.Chats, Why: "tired"},
		"why":   {Notes: same.Notes, Spoken: same.Spoken, Chats: same.Chats, Events: same.Events},
	} {
		if !wordsTakenBack(old, f) {
			t.Fatalf("%s removed and not seen", name)
		}
	}
	if wordsTakenBack("", same) || wordsTakenBack("{", same) {
		t.Fatal("no old facts is not a removal")
	}
}
