package main

import (
	"strings"
	"testing"
)

func TestParseVerdict(t *testing.T) {
	record := "Vlad: we went to Corfu in October with Cristina, five days.\nghost: noted."
	cases := []struct {
		out   string
		word  string
		found string
	}{
		{"YES", "YES", ""},
		{"yes.", "YES", ""},
		{"NO\nThe record says they went to Corfu in October, five days.", "NO", "The record says they went to Corfu in October, five days."},
		{"PARTLY\nIt was Corfu, not Crete.", "PARTLY", ""}, // Crete is not in the record: the sentence is the model's, dropped
		{"NO: it was five days, not 6.", "NO", ""},         // 6 is not in the record
		{"NO: it was five days.", "NO", "it was five days."},
		{"Maybe", "PARTLY", ""},
		{"", "PARTLY", ""},
	}
	for _, c := range cases {
		v := parseVerdict(c.out, record)
		if v.Word != c.word || v.Found != c.found {
			t.Fatalf("%q: got %+v, want %s %q", c.out, v, c.word, c.found)
		}
	}
	// a long found sentence is clipped
	long := "NO\n" + strings.Repeat("Corfu ", 100)
	if v := parseVerdict(long, record); len([]rune(v.Found)) > foundMax {
		t.Fatalf("not clipped: %d", len(v.Found))
	}
}

func TestReadAgainPromptAndBody(t *testing.T) {
	p := readAgainPrompt("Vlad", "the record", "Corfu | Vlad went to Corfu.")
	for _, want := range []string{"RECORD:\nthe record", "MEMORY: Corfu | Vlad went to Corfu.", "YES", "NO", "PARTLY", "Vlad's"} {
		if !strings.Contains(p, want) {
			t.Fatalf("prompt lacks %q:\n%s", want, p)
		}
	}
	if !strings.Contains(readAgainPrompt("", "r", "m"), "the person's") {
		t.Fatal("no owner, no name")
	}
	// a doubted fact leaves the rendered body and the model's facts; it stays in meta
	m := personMeta{Note: "Cristina is Vlad's wife.", Facts: []personFact{{T: "Cristina paints.", Ref: "chat:1"}, {T: "Cristina is from Crete.", Ref: "chat:2", Doubt: "the record says Corfu"}}}
	if b := personBody(m); strings.Contains(b, "Crete") || !strings.Contains(b, "paints") {
		t.Fatalf("body: %q", b)
	}
	if f := personFacts(personRow{meta: m}); len(f) != 2 || strings.Contains(strings.Join(f, " "), "Crete") {
		t.Fatalf("facts: %v", f)
	}
	if s := m.json(); !strings.Contains(s, `"doubt":"the record says Corfu"`) {
		t.Fatalf("meta: %s", s)
	}
}

// A YES stands only when the memory's own names and numbers are in the record.
func TestSupported(t *testing.T) {
	record := "Vlad: we went to Corfu in October with Cristina, five days.\nghost: noted."
	if !supported("Dan | Dan ran ten kilometres with Vlad on Sunday.", "Ran with Dan on Sunday morning, ten kilometres.", "Vlad") {
		t.Fatal("the owner's name is looked for in a first-person record")
	}
	for memory, want := range map[string]bool{
		"Corfu trip | Vlad went to Corfu in October with Cristina.": true,
		"Corfu trip | Vlad's trip to Corfu was in October.":         true,
		"Corfu trip | Vlad went to Crete in October.":               false, // a name the record never had
		"Corfu trip | Vlad went to Corfu for 6 days.":               false, // a figure the record never had
		"Trip | They went away in the autumn.":                      true,  // nothing to check: the model's word stands
	} {
		if got := supported(memory, record, "Vlad"); got != want {
			t.Errorf("supported(%q) = %v", memory, got)
		}
	}
}
