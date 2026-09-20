package main

import (
	"strings"
	"testing"
)

func TestBoundWebKindsAndCaps(t *testing.T) {
	in := []webHit{
		{Title: "A", URL: "https://www.example.com/a", Kind: "weather", Source: "open-meteo", Published: "2026-09-20", Excerpt: strings.Repeat("x", 3000)},
		{Title: "B", URL: "https://b.example", Kind: "nonsense"},
		{Title: "", URL: ""},
	}
	out := boundWeb(in)
	if len(out) != 2 {
		t.Fatalf("bound = %d, want 2 (the empty one dropped)", len(out))
	}
	if out[0].Kind != "weather" || out[1].Kind != "page" {
		t.Fatalf("kinds %q %q: unknown kinds fall back to page", out[0].Kind, out[1].Kind)
	}
	if len(out[0].Excerpt) > webMaxExcerpt+3 {
		t.Fatalf("excerpt not clipped: %d", len(out[0].Excerpt))
	}
	if webSite("https://www.example.com/a?x=1") != "example.com" || webSite("::bad") != "" {
		t.Fatalf("webSite: %q %q", webSite("https://www.example.com/a?x=1"), webSite("::bad"))
	}
}

func TestFormatWebNumbersAndLabels(t *testing.T) {
	hits := boundWeb([]webHit{
		{Title: "Weather · Athens, Greece", URL: "https://api.open-meteo.com/v1/forecast?x", Kind: "weather", Source: "open-meteo", Excerpt: "Now in Athens: clear, 28°C.", Fetched: "2026-09-20 10:41 UTC"},
		{Title: "Athens - BBC Weather", URL: "https://www.bbc.co.uk/weather/264371", Snippet: "14-day forecast", Excerpt: "Sunny spells.", Published: "2026-09-19", Fetched: "2026-09-20 10:41 UTC"},
		{Title: "Nikos Kazantzakis", URL: "https://en.wikipedia.org/wiki/Nikos_Kazantzakis", Kind: "summary", Source: "wikipedia", Excerpt: "Greek writer.", Published: "2026-08-30"},
	})
	got := formatWeb(hits)
	for _, want := range []string{
		"Cite by number, e.g. [2]",
		"Fetched 2026-09-20 10:41 UTC.",
		"\n[1] Weather · Athens, Greece (open-meteo) , weather forecast , https://api.open-meteo.com/v1/forecast?x\n    Now in Athens: clear, 28°C.",
		"\n[2] Athens - BBC Weather (bbc.co.uk, 2026-09-19) , page , https://www.bbc.co.uk/weather/264371\n    14-day forecast\n    Sunny spells.",
		"\n[3] Nikos Kazantzakis (wikipedia, 2026-08-30) , encyclopedia summary , https://en.wikipedia.org/wiki/Nikos_Kazantzakis\n    Greek writer.",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("formatWeb missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "\n    14-day forecast\n    Now") {
		t.Fatalf("a figure's snippet must not be printed as prose")
	}
	if formatWeb(nil) != "" {
		t.Fatalf("no hits, no block")
	}
}
