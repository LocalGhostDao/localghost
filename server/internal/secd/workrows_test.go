package secd

import (
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/voiced"
	"github.com/LocalGhostDao/localghost/server/internal/workcount"
)

func rowsText(rows []hw.DaemonKV) string {
	var b strings.Builder
	for _, r := range rows {
		b.WriteString(r.K + "=" + r.V + "\n")
	}
	return b.String()
}

func TestWorkRowsNameAndOrderTheKinds(t *testing.T) {
	now := time.Date(2026, 10, 1, 1, 0, 0, 0, time.UTC)
	w := workcount.Snapshot{Since: now.Add(-3*time.Hour - 10*time.Minute).Unix(), Kinds: []workcount.Kind{
		{Kind: "caption", Hour: 42, Day: 1310, Total: 1310, LastAt: now.Add(-20 * time.Second).Unix()},
		{Kind: "tag", Hour: 40, Day: 1290, Total: 1290, LastAt: now.Add(-50 * time.Second).Unix()},
		{Kind: "zzz new kind", Hour: 1, Day: 1, Total: 1},
	}}
	got := rowsText(formatWork("ghost.searchd", w, now, nil))
	for _, want := range []string{
		"counting since=the daemon started, 3 h 10 min ago",
		"photos described=42 last hour · 1,310 since start · last 20 s ago",
		"photos named and tagged=40 last hour · 1,290 since start",
		"zzz new kind=1 last hour",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	if strings.Index(got, "photos described") > strings.Index(got, "photos named") || strings.Index(got, "photos named") > strings.Index(got, "zzz") {
		t.Fatalf("order:\n%s", got)
	}
	// over a day up: the last 24 h is its own figure
	w.Since = now.Add(-30 * time.Hour).Unix()
	if got := rowsText(formatWork("ghost.searchd", w, now, nil)); !strings.Contains(got, "42 last hour · 1,310 last 24 h · 1,310 since start") {
		t.Fatalf("a day up:\n%s", got)
	}
	if got := rowsText(formatWork("ghost.framed", workcount.Snapshot{Since: now.Unix()}, now, nil)); !strings.Contains(got, "done=nothing yet since it started") {
		t.Fatalf("empty:\n%s", got)
	}
}

func TestGPUUseRows(t *testing.T) {
	on, off := true, false
	got := rowsText(formatGPUUse("gemma-4-12b", true, true, &on, "", 9064, 12282, ""))
	for _, want := range []string{"model=gemma-4-12b · on the card · holds 8.9 GB of 12.0 GB", "sees photos=yes", "on the CPU by choice=search embeddings and voice transcription"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	got = rowsText(formatGPUUse("gemma-4-12b", true, true, &off, "the projector is switched off by hand (mmproj-F16.gguf.off)", 8700, 0, ""))
	if !strings.Contains(got, "sees photos=NO , the projector is switched off by hand") || !strings.Contains(got, "holds 8.5 GB\n") {
		t.Fatalf("no vision:\n%s", got)
	}
	if got := rowsText(formatGPUUse("gemma-4-12b", false, false, nil, "", 0, 0, "")); strings.Contains(got, "sees photos") || !strings.Contains(got, "loading") {
		t.Fatalf("older oracled:\n%s", got)
	}
	// a second model for the arbiter is on the CPU by choice too, and the card's page says so
	if got := rowsText(formatGPUUse("gemma-4-12b", true, true, &on, "", 9064, 12282, "Qwen3-4B-Q4_K_M")); !strings.Contains(got,
		"on the CPU by choice=search embeddings and voice transcription, and the arbiter's own model (Qwen3-4B-Q4_K_M) (the card's memory is the chat model's)") {
		t.Fatalf("with a second model:\n%s", got)
	}
}

// The arbiter's own model under ghost.oracled: which it is, whether it is up, what it has
// done; a box with none says which model reads cold and what a second one would change.
func TestTheArbitersModelInTheDrillIn(t *testing.T) {
	if arbiterRows(nil) != nil {
		t.Fatal("an oracled from before it said has no rows")
	}
	none := rowsText(arbiterRows(&oracle.ArbiterInfo{ServedBy: "the main model", Main: "gemma-4-12b"}))
	if !strings.Contains(none, "the arbiter's model=none on this box: ghost.shadowd's cold read is by gemma-4-12b, with nothing about you") || !strings.Contains(none, "ai-models/arbiter-<name>.gguf") {
		t.Fatalf("one model:\n%s", none)
	}
	up := rowsText(arbiterRows(&oracle.ArbiterInfo{Has: true, Model: "Qwen3-4B-Q4_K_M", Up: true, ServedBy: "Qwen3-4B-Q4_K_M", Main: "gemma-4-12b",
		Answers: 12, TokensPerSecond: 9.34, LastAnswerAt: "14:02"}))
	for _, want := range []string{"the arbiter's model=Qwen3-4B-Q4_K_M · up, on the CPU beside gemma-4-12b, in a queue of its own", "it has answered=12 since start · 9.3 tok/s · last 14:02"} {
		if !strings.Contains(up, want) {
			t.Fatalf("missing %q in\n%s", want, up)
		}
	}
	if fresh := rowsText(arbiterRows(&oracle.ArbiterInfo{Has: true, Model: "m", Up: true})); !strings.Contains(fresh, "it has answered=nothing yet since it started") {
		t.Fatalf("just started:\n%s", fresh)
	}
	down := rowsText(arbiterRows(&oracle.ArbiterInfo{Has: true, Model: "Qwen3-4B-Q4_K_M", Why: "died after 3s up (starting again in 30s): out of memory", ServedBy: "nothing yet: waiting for Qwen3-4B-Q4_K_M"}))
	if !strings.Contains(down, "the arbiter's model=Qwen3-4B-Q4_K_M · NOT up: died after 3s up") || !strings.Contains(down, "the cold read is by=nothing yet: waiting for Qwen3-4B-Q4_K_M") {
		t.Fatalf("down:\n%s", down)
	}
	// THE TOOLS READ `models` WITH SED AND GREP for the main model's ready, verdict, speed and
	// tokPerSecLast (update.sh waits on "ready": true; gpu.sh times tokPerSecLast). The
	// arbiter's block is in the same answer and sorts ahead of them: it must not use those names.
	b, _ := json.Marshal(oracle.ArbiterInfo{Has: true, Model: "m", Up: true, Why: "w", Answers: 1, TokensPerSecond: 1, LastAnswerAt: "x", ServedBy: "m", Main: "g"})
	for _, taken := range []string{`"ready"`, `"verdict"`, `"speed"`, `"tokPerSecLast"`, `"tokPerSecAvg"`, `"onGPU"`} {
		if strings.Contains(string(b), taken) {
			t.Fatalf("the arbiter's block uses %s, which the tools read as the main model's: %s", taken, b)
		}
	}
}

// How long a question asked aloud takes, in the voice daemon's drill-in.
func TestQuestionsAskedAloudInTheDrillIn(t *testing.T) {
	now := time.Unix(1_795_000_000, 0)
	if got := rowsText(askRows(voiced.Asks{}, now)); !strings.Contains(got, "a question takes=none asked aloud since the daemon started") {
		t.Fatalf("none yet:\n%s", got)
	}
	last := voiced.Ask{At: now.Add(-12 * time.Minute).Unix(), AudioMS: 3200, TookMS: 4130, Lang: "en", Words: 7}
	got := rowsText(askRows(voiced.Asks{N: 12, Last: &last, AvgTookMS: 4310, Over: 12}, now))
	for _, want := range []string{"the last question=3.2 s of speech · heard in 4.1 s · en · 12 min ago", "a question takes=4.3 s on average over the last 12"} {
		if !strings.Contains(got, want) {
			t.Fatalf("missing %q in\n%s", want, got)
		}
	}
	twice := voiced.Ask{At: now.Unix(), AudioMS: 2000, TookMS: 7000, Lang: "ro", Again: true}
	if got := rowsText(askRows(voiced.Asks{N: 1, Last: &twice, Over: 1}, now)); !strings.Contains(got, "heard twice") || !strings.Contains(got, "no words in it") || strings.Contains(got, "on average") {
		t.Fatalf("heard twice, one only:\n%s", got)
	}
	// and the work rows carry them: the labels a person reads, then the timings
	w := workcount.Snapshot{Since: now.Add(-time.Hour).Unix(), Kinds: []workcount.Kind{{Kind: "question heard", Hour: 3, Day: 3, Total: 3, LastAt: now.Unix()}, {Kind: "note set aside", Hour: 1, Day: 1, Total: 1, LastAt: now.Unix()}}}
	rows := rowsText(formatWork("ghost.voiced", w, now, nil))
	if !strings.Contains(rows, "questions asked aloud, heard=3 last hour") || !strings.Contains(rows, "a voice note set aside for a question=1 last hour") {
		t.Fatalf("labels:\n%s", rows)
	}
}

func TestThousandsAndSpan(t *testing.T) {
	for in, want := range map[int64]string{0: "0", 999: "999", 1000: "1,000", 1234567: "1,234,567"} {
		if thousands(in) != want {
			t.Fatalf("%d: %s", in, thousands(in))
		}
	}
	if span(50*time.Hour) != "2 days 2 h" || span(90*time.Second) != "1 min" || span(125*time.Minute) != "2 h 5 min" {
		t.Fatal("span")
	}
}
