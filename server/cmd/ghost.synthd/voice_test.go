package main

import (
	"strings"
	"testing"
)

// Em dashes become commas, with their spacing put right, whole or token by token; a range's en
// dash stays; nothing else is touched.
func TestPlainDashesAndTheStream(t *testing.T) {
	for in, want := range map[string]string{
		"the box — not the cloud — keeps it": "the box, not the cloud, keeps it",
		"word—word":                          "word, word",
		"9–16°C and 10 – 20 km":              "9–16°C and 10, 20 km",
		"no dashes here":                     "no dashes here",
		"ends with a dash —":                 "ends with a dash, ",
	} {
		if got := plainDashes(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	// the same text split the ways a tokenizer splits it
	whole := "the box — not the cloud — keeps it, and 9–16°C"
	for _, cut := range [][]string{
		{"the box", " —", " not the cloud", " —", " keeps it, and 9", "–", "16°C"},
		{"the box ", "— not", " the cloud —", " keeps", " it, and 9–16°C"},
		{"the box —", " not the cloud —", " keeps it, and 9–16°C"},
		{"the", " box ", "—", " not", " the", " cloud", " ", "—", " keeps", " it, and 9–16°C"},
	} {
		vf := &voiceFilter{}
		var b strings.Builder
		for _, tok := range cut {
			b.WriteString(vf.pass(tok))
		}
		b.WriteString(vf.flush())
		if got := b.String(); got != plainDashes(whole) {
			t.Errorf("%q: %q, want %q", cut, got, plainDashes(whole))
		}
	}
	if !strings.Contains(voiceRules, "No em dashes") || strings.ContainsAny(voiceRules, "—:") {
		t.Fatal("the rules break their own rules")
	}
}
