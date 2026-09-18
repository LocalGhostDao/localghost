package main

import (
	"strings"
	"testing"
)

func sp(s string) *string { return &s }

// TestTrimHistoryOrderAndBudget: rows arrive newest first (the query's ORDER BY id DESC LIMIT),
// leave oldest first, and the character budget drops the OLDEST turns, never the newest.
func TestTrimHistoryOrderAndBudget(t *testing.T) {
	rows := [][]*string{
		{sp("assistant"), sp("A3")},
		{sp("user"), sp("Q3")},
		{sp("assistant"), sp("A2")},
		{sp("user"), sp("Q2")},
		{sp("assistant"), sp("A1")},
		{sp("user"), sp("Q1")},
	}
	got := trimHistory(rows)
	want := []string{"Q1", "A1", "Q2", "A2", "Q3", "A3"}
	if len(got) != len(want) {
		t.Fatalf("len = %d", len(got))
	}
	for i := range want {
		if got[i].Content != want[i] {
			t.Fatalf("turn %d = %q, want %q", i, got[i].Content, want[i])
		}
	}
	// Budget: a huge old turn must fall off while the recent ones survive.
	big := strings.Repeat("x", historyMaxChars)
	rows = [][]*string{
		{sp("assistant"), sp("recent answer")},
		{sp("user"), sp("recent question")},
		{sp("assistant"), sp(big)},
		{sp("user"), sp("ancient")},
	}
	got = trimHistory(rows)
	if len(got) != 2 || got[0].Content != "recent question" || got[1].Content != "recent answer" {
		t.Fatalf("budget trim wrong: %+v", got)
	}
	// Nil cells and empty content are skipped, not forwarded.
	rows = [][]*string{{sp("user"), nil}, {nil, sp("x")}, {sp("user"), sp("")}, {sp("user"), sp("ok")}}
	if got = trimHistory(rows); len(got) != 1 || got[0].Content != "ok" {
		t.Fatalf("nil/empty handling: %+v", got)
	}
}
