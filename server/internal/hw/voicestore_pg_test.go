package hw

// Voice notes end to end against a real Postgres, with a stand-in whisper-cli: a WAV and its
// sidecar in the inbox (as secd leaves them) are archived and recorded by ghost.voiced's pass,
// transcribed, journaled; the check-in that names the note gets its transcript through
// CheckinHistory; the list shows it; deleting it takes the row, the journal entry and the file.
//
//	GHOST_PG_SOCKET_DIR=/run/lgpg GHOST_PG_PORT=55432 GHOST_PG_USER=claude go test -run VoicePG ./internal/hw/

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"github.com/LocalGhostDao/localghost/server/internal/voiced"
)

const fakeWhisperCLI = `#!/bin/sh
of=""
while [ $# -gt 0 ]; do case "$1" in -of) of="$2"; shift 2 ;; *) shift ;; esac; done
printf '{"result":{"language":"en"},"transcription":[{"text":" Swam to the lighthouse and back."},{"text":" Feeling calm."}]}' > "$of.json"
`

func TestVoicePGNoteToCheckin(t *testing.T) {
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
	if err := admin.ExecSimple("DROP DATABASE IF EXISTS lgtest_voice"); err != nil {
		t.Fatal(err)
	}
	if err := admin.ExecSimple("CREATE DATABASE lgtest_voice"); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", "lgtest_voice")
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	if _, err := ConvergeSchema(db, lg); err != nil {
		t.Fatalf("schema: %v", err)
	}

	// the volume: bin/whisper-cli, ai-models/ggml-*.bin, and an upload in voiced/inbox
	mount := t.TempDir()
	_ = os.MkdirAll(filepath.Join(mount, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(mount, "bin", "whisper-cli"), []byte(fakeWhisperCLI), 0o755)
	_ = os.MkdirAll(filepath.Join(mount, "ai-models"), 0o755)
	_ = os.WriteFile(filepath.Join(mount, "ai-models", "ggml-large-v3-turbo-q5_0.bin"), make([]byte, 2<<20), 0o644)
	inbox := filepath.Join(mount, "voiced", "inbox")
	_ = os.MkdirAll(inbox, 0o755)
	id := "0123456789abcdef0123456789abcdef"
	day := time.Now().Format("2006-01-02")
	taken := time.Now().Add(-time.Minute).UnixMilli()
	pcm := make([]byte, 16000*2*3) // three seconds of silence, as the phone writes it
	wav := append(voiced.WavHeader(16000, 1, 16, uint32(len(pcm))), pcm...)
	binary.LittleEndian.PutUint32(wav[40:44], 0) // and an unfinished header, for good measure
	_ = os.WriteFile(filepath.Join(inbox, id+".wav"), wav, 0o640)
	meta, _ := json.Marshal(voiced.Meta{ID: id, Kind: "checkin", Day: day, Taken: taken, Device: "abc"})
	_ = os.WriteFile(filepath.Join(inbox, id+".json"), meta, 0o640)
	// a sidecar that is not a WAV goes to rejected, never deleted
	bad := "fedcba9876543210fedcba9876543210"
	_ = os.WriteFile(filepath.Join(inbox, bad+".wav"), []byte("ID3 not a wav at all"), 0o640)
	_ = os.WriteFile(filepath.Join(inbox, bad+".json"), meta, 0o640)

	d := &voiced.Daemon{Mount: mount, Log: lg, Find: voiced.FindEngine, Open: func() (voiced.DB, error) { return db, nil }}
	if err := d.Pass(context.Background(), db); err != nil {
		t.Fatalf("pass: %v", err)
	}
	st := d.Stat()
	if st.Done != 1 || st.Pending != 0 || st.Engine != "ggml-large-v3-turbo-q5_0" {
		t.Fatalf("stat %+v", st)
	}
	if _, err := os.Stat(filepath.Join(inbox, "rejected", bad+".wav")); err != nil {
		t.Fatalf("the bad upload was not kept in rejected: %v", err)
	}
	rows, err := db.Query("SELECT path, duration_ms, transcript, lang, kind, day FROM voice_notes WHERE id = $1", id)
	if err != nil || len(rows.Vals) != 1 {
		t.Fatalf("row: %v", err)
	}
	v := rows.Vals[0]
	if !strings.HasPrefix(*v[0], "voiced/archive/") || *v[1] != "3000" || *v[2] != "Swam to the lighthouse and back. Feeling calm." || *v[3] != "en" || *v[4] != "checkin" || *v[5] != day {
		t.Fatalf("row %v %v %v %v %v %v", *v[0], *v[1], *v[2], *v[3], *v[4], *v[5])
	}
	archived := filepath.Join(mount, *v[0])
	if _, err := os.Stat(archived); err != nil {
		t.Fatalf("archive: %v", err)
	}
	j, err := db.Query("SELECT title, body, ts FROM journal_entries WHERE source = 'ghost.voiced' AND ref = $1", "voice:"+id)
	if err != nil || len(j.Vals) != 1 {
		t.Fatalf("journal: %v", err)
	}
	if !strings.HasPrefix(*j.Vals[0][0], "Voice note: Swam to the lighthouse") || !strings.Contains(*j.Vals[0][1], "daily check-in of "+day+" (3 s)") ||
		*j.Vals[0][2] != strconv.FormatInt(taken/1000, 10) {
		t.Fatalf("journal row %q %q %q", *j.Vals[0][0], *j.Vals[0][1], *j.Vals[0][2])
	}

	// the check-in text names the note; the history carries its transcript
	body := "Daily check-in " + day + "\nFeeling: calm, tired\nPreselected: tired\nWhy: long swim\nVoice: " + id + " 0:03"
	if err := db.Exec(`INSERT INTO journal_entries (source, ref, ts, title, body, created_at) VALUES ('ghost.noted','c1',$1,$2,$3,$1)`,
		time.Now().Unix(), "Daily check-in "+day, body); err != nil {
		t.Fatal(err)
	}
	ns := &NotifStore{rw: map[int]*poltergres.ReadWrite{0: db}}
	hist, err := ns.CheckinHistory(0, 10)
	if err != nil || len(hist) != 1 {
		t.Fatalf("history %v %v", hist, err)
	}
	h := hist[0]
	if h.Feelings != "calm, tired" || h.Preselected != "tired" || h.Voice == nil || h.Voice.Status != "done" ||
		h.Voice.Transcript != "Swam to the lighthouse and back. Feeling calm." || h.Voice.DurationMS != 3000 {
		t.Fatalf("history row %+v voice %+v", h, h.Voice)
	}
	if len(h.Voices) != 1 || h.Voices[0].ID != id {
		t.Fatalf("voices %+v", h.Voices)
	}
	// a second note said to the same check-in later in the day: listed after the first; a journal
	// note of the day, and a check-in note of another day, are not
	later := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	for _, n := range []struct{ id, kind, day string }{{later, "checkin", day}, {"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "journal", day}, {"cccccccccccccccccccccccccccccccc", "checkin", "2001-01-01"}} {
		if err := db.Exec(`INSERT INTO voice_notes (id, kind, day, taken_at, duration_ms, status, transcript, received_at) VALUES ($1,$2,$3,$4,4000,'done','More, later.',$4)`,
			n.id, n.kind, n.day, time.Now().Unix()); err != nil {
			t.Fatal(err)
		}
	}
	hist, _ = ns.CheckinHistory(0, 10)
	if len(hist) != 1 || len(hist[0].Voices) != 2 || hist[0].Voices[0].ID != id || hist[0].Voices[1].ID != later || hist[0].Voice.ID != id {
		t.Fatalf("with a note added %+v", hist[0].Voices)
	}
	for _, n := range []string{later, "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", "cccccccccccccccccccccccccccccccc"} {
		_ = db.Exec("DELETE FROM voice_notes WHERE id = $1", n)
	}
	notes, err := ns.VoiceNotes(0, 10)
	if err != nil || len(notes) != 1 || notes[0].ID != id {
		t.Fatalf("list %v %v", notes, err)
	}
	if done, err := ns.CheckinDoneToday(0, day); err != nil || !done {
		t.Fatalf("check-in not seen as done: %v", err)
	}

	// deleted: row and journal entry gone, the path handed back for the file
	rel, found, err := ns.VoiceDelete(0, id)
	if err != nil || !found || rel != *v[0] {
		t.Fatalf("delete %q %v %v", rel, found, err)
	}
	if j, _ := db.Query("SELECT 1 FROM journal_entries WHERE source = 'ghost.voiced'"); len(j.Vals) != 0 {
		t.Fatal("journal entry outlived the delete")
	}
	hist, _ = ns.CheckinHistory(0, 10)
	if len(hist) != 1 || hist[0].Voice == nil || hist[0].Voice.Status != "missing" || len(hist[0].Voices) != 1 {
		t.Fatalf("after delete %+v", hist)
	}
}
