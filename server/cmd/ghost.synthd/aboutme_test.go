package main

import (
	"strings"
	"testing"
)

func TestParseAbout(t *testing.T) {
	out := `NAME | Vlad
ME | Where Vlad lives | Vlad lives in London and was born in Romania.
- ME | Work | The user's company is LocalGhost.
PERSON | Cristina | My partner, who travels with me.
PERSON | cristina | She loves the sea.
PERSON | James | A co-founder of mine at Overclock.
just a stray line
ME | | nothing`
	f := parseAbout(out)
	if f.Name != "Vlad" || len(f.Me) != 2 || len(f.People) != 2 {
		t.Fatalf("%+v", f)
	}
	if f.Me[0][1] != "Vlad lives in London and was born in Romania." || f.Me[1][1] != "Vlad's company is LocalGhost." {
		t.Fatalf("by name: %q", f.Me)
	}
	if f.People[0][0] != "Cristina" || f.People[0][1] != "Vlad's partner, who travels with Vlad. She loves the sea." {
		t.Fatalf("one memory per person: %+v", f.People[0])
	}
	// without a name the note's lines stay in the first person
	g := parseAbout("ME | Work | The user's company is LocalGhost.")
	if g.Name != "" || g.Me[0][1] != "My company is LocalGhost." {
		t.Fatalf("%+v", g)
	}
}

func TestNamed(t *testing.T) {
	for in, want := range map[string]string{
		"I am a runner and my knee hurts.":  "Vlad is a runner and Vlad's knee hurts.",
		"I've been to Corfu with my sister": "Vlad has been to Corfu with Vlad's sister",
		"Tell me about the user.":           "Tell Vlad about Vlad.",
		"Vlad prefers tea.":                 "Vlad prefers tea.",
		"Mexico City meme":                  "Mexico City meme",
	} {
		if got := named(in, "Vlad"); got != want {
			t.Errorf("%q: %q", in, got)
		}
	}
}

func TestNamePromptAndParse(t *testing.T) {
	rows := [][3]string{
		{"11", "Coffee", "I take my coffee black, 2 cups a day."},
		{"12", "Cristina", "My partner, who loves the sea."},
		{"13", "Run", "I ran 10 km in Hyde Park."},
	}
	p := namePrompt("Vlad", rows)
	if !strings.Contains(p, "My name is Vlad") || !strings.Contains(p, "2 | Cristina | My partner") {
		t.Fatal(p)
	}
	out := `1 | Coffee | Vlad takes his coffee black, 2 cups a day.
2 | Cristina | Cristina is Vlad's partner, who loves the sea.
3 | Run | Vlad ran 12 km in Hyde Park.
7 | Stray | Vlad.`
	got := parseNamed(out, "Vlad", rows)
	if len(got) != 2 || got[0][1] != "Vlad takes his coffee black, 2 cups a day." || got[1][1] != "Cristina is Vlad's partner, who loves the sea." {
		t.Fatalf("%+v", got)
	}
	if _, ok := got[2]; ok {
		t.Fatal("a number the old note did not say is not kept")
	}
}

func TestIdentityAndDistillPrompt(t *testing.T) {
	front, _ := chooseFront(strings.Repeat("I like the sea. ", 200), nil, frontBudget)
	id := identityText("Vlad", front)
	if !strings.HasPrefix(id, "You are LocalGhost") || !strings.Contains(id, "When I say LocalGhost") || !strings.Contains(id, "I am Vlad;") || len(id) > aboutClip+600 {
		t.Fatalf("%d %q", len(id), id[:200])
	}
	if bare := identityText("", ""); strings.Contains(bare, "I am") || strings.Contains(bare, "told you") {
		t.Fatal(bare)
	}
	p := distillPrompt("Vlad", []string{"Cristina", "James"}, "chat", "we went sailing")
	if !strings.Contains(p, "(I am Vlad)") || !strings.Contains(p, "\"Vlad prefers…\"") || !strings.Contains(p, "never \"I\" or \"the user\"") ||
		!strings.Contains(p, "PERSON | their name") || !strings.Contains(p, "Cristina, James") || strings.Contains(p, "about the USER") {
		t.Fatal(p)
	}
	if q := distillPrompt("", nil, "chat", "x"); !strings.Contains(q, "in the first person") || strings.Contains(q, "third person") {
		t.Fatal(q)
	}
}

func TestCheckinWordsAndPrompt(t *testing.T) {
	voice := "Said at the daily check-in of 2026-10-02 (1 min 20 s):\nI'm Vlad, I live in London with Cristina and I run LocalGhost."
	if got := checkinWords(voice); got != "I'm Vlad, I live in London with Cristina and I run LocalGhost." {
		t.Fatalf("%q", got)
	}
	written := "Daily check-in 2026-10-02\nFeeling: calm, tired\nWhy: shipped the box's coin pages\nVoice: 0123456789abcdef0123456789abcdef"
	if got := checkinWords(written); got != "shipped the box's coin pages" {
		t.Fatalf("%q", got)
	}
	p := checkinAboutPrompt("Vlad", "x")
	if !strings.Contains(p, "\"Vlad lives …\"") || !strings.Contains(p, "How the day went") || !strings.HasSuffix(p, "CHECK-IN:\nx") {
		t.Fatal(p)
	}
	if q := checkinAboutPrompt("", "x"); !strings.Contains(q, "first person") {
		t.Fatal(q)
	}
}
