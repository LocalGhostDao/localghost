package pgtest

import (
	"strings"
	"testing"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/voiced"
)

// ghost.voiced in Box Status: it had no drill-in at all ("no drill-in for this daemon yet")
// until 21 November 2026. The rows here are the database's side of it, the notes; the
// questions asked aloud leave no row and are counted by the daemon (secd puts them first).
func TestTheVoiceDrillIn(t *testing.T) {
	db := fresh(t)
	got := func() map[string]string {
		m := map[string]string{}
		for _, kv := range hw.DaemonSummaryFrom(db, "ghost.voiced") {
			m[kv.K] = kv.V
		}
		return m
	}
	// a box that has heard nothing yet says so, in words, not in zeros that read as a fault
	empty := got()
	if empty["voice notes"] != "0" || empty["languages heard"] != "none yet" || empty["speech model"] != "none used yet" || empty["audio kept"] != "0.0 hours" {
		t.Fatalf("an empty box: %v", empty)
	}
	note := func(id, status, lang string, ms, at int64) {
		t.Helper()
		if err := db.Exec(`INSERT INTO voice_notes (id, kind, day, taken_at, duration_ms, status, lang, model, transcribed_at)
			VALUES ($1, 'journal', '2026-11-21', $2, $3, $4, $5, $6, $7)`, id, at, ms, status, lang,
			map[bool]string{true: "ggml-large-v3-turbo-q5_0", false: ""}[status == "done"], at*1000+5000); err != nil {
			t.Fatal(err)
		}
	}
	note("a", "done", "en", 60_000, 100)
	note("b", "done", "en", 120_000, 200)
	note("c", "done", "ro", 1_800_000, 300)
	note("d", "pending", "", 30_000, 400)
	note("e", "failed", "", 30_000, 500)
	full := got()
	for k, want := range map[string]string{
		"voice notes": "5", "transcribed": "3", "waiting": "1", "failed": "1",
		"audio kept":      "0.6 hours", // 2,040 seconds
		"languages heard": "en 2 · ro 1",
		"speech model":    "ggml-large-v3-turbo-q5_0",
	} {
		if full[k] != want {
			t.Fatalf("%s: %q, want %q (all: %v)", k, full[k], want, full)
		}
	}
	if !strings.Contains(full["where it runs"], "a question asked aloud goes first") {
		t.Fatalf("where it runs: %q", full["where it runs"])
	}
	// and what the daemon tells a question's language by is the same list, most spoken first
	if langs := voiced.HeardLangs(db); strings.Join(langs, ",") != "en,ro" {
		t.Fatalf("heard languages: %v", langs)
	}
}
