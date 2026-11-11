package main

import (
	"io"
	"log/slog"
	"strings"
	"testing"
	"unicode/utf8"
)

// A page in another encoding, or with letters that change length when lowered, is read; what is
// kept is valid UTF-8 cut on a character (audit run 2: such a page took synthd down every pass).
func TestArticleTextOnPagesThatAreNotUTF8(t *testing.T) {
	para := "<p>The council met on Tuesday and voted nine to four to keep the library open through the winter months.</p>"
	for name, page := range map[string]string{
		"latin-1":  "<html><body>" + strings.Repeat("\xe9\xe8\xff", 200) + "<ARTICLE>" + strings.Repeat(para, 3) + "</ARTICLE></body></html>",
		"longer":   "<html><body>" + strings.Repeat("ȺȾ", 200) + "<article>" + strings.Repeat(para, 3) + "</article></body></html>",
		"shorter":  "<html><body>" + strings.Repeat("K", 200) + "<main>" + strings.Repeat(para, 3) + "</main></body></html>",
		"cut rune": "<article><p>" + strings.Repeat("é", articleKeep) + " and some more words to make it prose enough.</p></article>",
		"nul":      "<article><p>The council met on Tuesday \x00 and voted nine to four to keep the library open.</p></article>",
	} {
		text, _, ok := readArticle(page)
		if !ok {
			t.Fatalf("%s: unreadable", name)
		}
		if !utf8.ValidString(text) || strings.ContainsRune(text, 0) || len(text) > articleKeep {
			t.Fatalf("%s: kept text the database would refuse (%d bytes)", name, len(text))
		}
		if name != "cut rune" && name != "nul" && !strings.Contains(text, "council met") {
			t.Fatalf("%s: the article's words are missing: %q", name, text)
		}
		_ = siteText(page)
	}
	if got := clipText(strings.Repeat("é", 100), 51); !utf8.ValidString(got) {
		t.Fatal("clipText cut inside a character")
	}
}

// A pass that faults is abandoned and the loop goes on.
func TestGuardedPassSurvivesAFault(t *testing.T) {
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	ran := 0
	pass := guarded("slow", lg, func() {
		ran++
		var s []int
		_ = s[3]
	})
	pass()
	pass()
	if ran != 2 {
		t.Fatalf("ran %d times", ran)
	}
	got := false
	guardedArg("fetch", lg, func(b bool) { got = b; panic("x") })(true)
	if !got {
		t.Fatal("the argument was lost")
	}
}
