package main

import (
	"strings"
	"testing"
	"unicode/utf8"
)

func TestMemoryTermsSkipTheWordsEveryMemoryHas(t *testing.T) {
	got := memoryTerms("What did I do on the boat trip to Antipaxos, and where did we eat?")
	want := []string{"boat", "trip", "antipaxos", "eat"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Fatalf("terms = %v, want %v", got, want)
	}
	if len(memoryTerms("what is the thing that we did")) != 1 { // "thing"
		t.Fatalf("%v", memoryTerms("what is the thing that we did"))
	}
	if n := len(memoryTerms("a b c d e f g h i j k l m n o p q r s t u v w x y z alpha bravo charlie delta echo foxtrot golf hotel")); n != 6 {
		t.Fatalf("capped at six, got %d", n)
	}
}

func TestWantsTaste(t *testing.T) {
	for _, q := range []string{"what should I do today?", "anywhere good for a swim near here", "recommend a beach", "plan a day out on Paxos", "where can we eat tonight", "what do I like to photograph"} {
		if !wantsTaste(q) {
			t.Errorf("%q should ask for the taste", q)
		}
	}
	for _, q := range []string{"what is the capital of Greece", "convert 20 euros to pounds", "summarise my notes from Tuesday"} {
		if wantsTaste(q) {
			t.Errorf("%q should not", q)
		}
	}
}

func TestBudgetFromMeasuredSpeed(t *testing.T) {
	cpu := engineSpeed{}
	if cpu.promptTPS() != cpuPromptTPS || cpu.contextBudget() != 3200 {
		t.Fatalf("cpu default: %v tps, %d chars", cpu.promptTPS(), cpu.contextBudget())
	}
	gpu := engineSpeed{OnGPU: true}
	if gpu.contextBudget() != 120000 {
		t.Fatalf("gpu default budget = %d", gpu.contextBudget())
	}
	measured := engineSpeed{PromptTPS: 85}
	if measured.contextBudget() != 6800 {
		t.Fatalf("measured budget = %d", measured.contextBudget())
	}
	if s := cpu.readSeconds(8000); s != 50 {
		t.Fatalf("8000 chars at 40 t/s = %v s", s)
	}
	n := readingNote(cpu, 8000, true)
	if !strings.Contains(n, "on the CPU") || !strings.Contains(n, "about 50s") || !strings.Contains(n, "shortened") {
		t.Fatalf("note = %q", n)
	}
	if readingNote(gpu, 8000, false) != "" {
		t.Fatal("a quick read needs no note")
	}
}

func TestFitWebCutsFromTheBottomAndKeepsItCitable(t *testing.T) {
	long := strings.Repeat("Paxos is small and green. ", 60) // ~1500 chars
	var hits []webHit
	for i := 0; i < 6; i++ {
		hits = append(hits, webHit{Title: "Page", URL: "https://example.org/p", Snippet: "a snippet about it", Excerpt: long})
	}
	out, cut := fitWeb(hits, 100000)
	if cut || len(out) != 6 {
		t.Fatal("a generous budget cuts nothing")
	}
	out, cut = fitWeb(hits, 3200)
	if !cut {
		t.Fatal("a CPU budget must cut")
	}
	if out[0].Excerpt == "" || out[1].Excerpt == "" {
		t.Fatal("the top two keep page text")
	}
	for i := 2; i < len(out); i++ {
		if out[i].Excerpt != "" {
			t.Fatalf("hit %d kept its excerpt", i)
		}
		if out[i].URL == "" || out[i].Title == "" {
			t.Fatal("a trimmed hit must stay citable")
		}
	}
	total := 0
	for _, h := range out {
		total += len(h.Title) + len(h.URL) + len(h.Snippet) + len(h.Excerpt) + 24
		if !utf8.ValidString(h.Excerpt) {
			t.Fatal("cut inside a rune")
		}
	}
	if total > 3400 {
		t.Fatalf("still %d chars", total)
	}
	// never below the floor: a hopeless budget still sends something worth sending
	out, _ = fitWeb(hits, 10)
	if len(out) < 3 {
		t.Fatalf("hits = %d, never below three", len(out))
	}
	// multibyte text is cut on a rune boundary
	greek := strings.Repeat("Καλημέρα, τι κάνεις; ", 120)
	out, _ = fitWeb([]webHit{{Title: "Ελληνικά", URL: "https://el.example/x", Excerpt: greek}, {Title: "b", URL: "u", Excerpt: greek}}, 2000)
	for _, h := range out {
		if !utf8.ValidString(h.Excerpt) {
			t.Fatal("greek cut inside a rune")
		}
	}
}

func TestHereValid(t *testing.T) {
	var nilHere *hereT
	if nilHere.valid() || (&hereT{0, 0}).valid() || (&hereT{91, 0}).valid() || !(&hereT{39.15, 20.22}).valid() {
		t.Fatal("here validity")
	}
}
