package main

import (
	"encoding/json"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestParsePlanTakesWhatTheModelWrappedAndKeepsOnlyWhatIsValid(t *testing.T) {
	out := "Sure, here is the plan:\n```json\n{\"search\": true, \"need\": \"the price of a freddo espresso in Athens in euros, this year\", \"shape\": \"number\", \"fresh\": true, \"queries\": [\"freddo espresso price Athens 2026\", \" freddo price athens 2026\", \"FREDDO ESPRESSO PRICE ATHENS 2026\", \"coffee prices greece\", \"a fifth query\"]}\n```"
	p, ok := parsePlan(out)
	if !ok || !p.Search || p.Shape != "number" || !p.Fresh || !strings.HasPrefix(p.Need, "the price of a freddo") {
		t.Fatalf("plan: %+v %v", p, ok)
	}
	// duplicates (case and space) folded, at most three kept, in the model's order
	if len(p.Queries) != 3 || p.Queries[0] != "freddo espresso price Athens 2026" || p.Queries[1] != "freddo price athens 2026" || p.Queries[2] != "coffee prices greece" {
		t.Fatalf("queries: %v", p.Queries)
	}
	// an unknown shape is prose; search=false needs no queries
	p, ok = parsePlan(`{"search": false, "need": "", "shape": "poem", "queries": []}`)
	if !ok || p.Search || p.Shape != "prose" {
		t.Fatalf("no-search plan: %+v %v", p, ok)
	}
	// search without a query or without a need is no plan
	if _, ok := parsePlan(`{"search": true, "need": "x", "queries": []}`); ok {
		t.Fatal("search with no query accepted")
	}
	if _, ok := parsePlan(`{"search": true, "need": "", "queries": ["x"]}`); ok {
		t.Fatal("search with no need accepted")
	}
	if _, ok := parsePlan("I cannot help with that."); ok {
		t.Fatal("prose accepted as a plan")
	}
	if _, ok := parsePlan(`{"search": tru`); ok {
		t.Fatal("broken JSON accepted")
	}
	// a query the model padded to an essay is dropped, not sent to a search engine
	p, _ = parsePlan(`{"search": true, "need": "n", "queries": ["` + strings.Repeat("long ", 40) + `", "short one"]}`)
	if len(p.Queries) != 1 || p.Queries[0] != "short one" {
		t.Fatalf("overlong query kept: %v", p.Queries)
	}
}

func TestPlanPromptCarriesTheLastTurnsAndTheDate(t *testing.T) {
	h := []chatTurn{{"user", "how much is a freddo in athens"}, {"assistant", "About 3 to 4 euros."}, {"user", "and in pounds?"}}
	s := planPrompt("and in pounds?", h, time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC))
	for _, must := range []string{"2026-09-26", "user: how much is a freddo", "assistant: About 3 to 4 euros.", "The question: and in pounds?", `"queries"`,
		"Never put the person's own details"} {
		if !strings.Contains(s, must) {
			t.Fatalf("prompt lacks %q:\n%s", must, s)
		}
	}
	// long turns are clipped, many turns cut to the last four
	many := make([]chatTurn, 10)
	for i := range many {
		many[i] = chatTurn{"user", strings.Repeat("x", 1000) + string(rune('a'+i))}
	}
	s = planPrompt("q", many, time.Now())
	if strings.Count(s, "user: ") != 4 || strings.Contains(s, strings.Repeat("x", 400)) {
		t.Fatalf("history not bounded: %d turns", strings.Count(s, "user: "))
	}
}

func TestRankParagraphsWithoutAnEmbedderPicksByTheWordsAndWritesTheExcerpt(t *testing.T) {
	runDir := t.TempDir() // no searchd socket here: the keyword pick stands in
	hits := []webHit{
		{Title: "Coffee in Athens", URL: "https://example.org/coffee", Paragraphs: []string{
			"The Athens coffee scene has grown enormously over the last decade, with roasters on every corner and a culture of long afternoons.",
			"A freddo espresso costs between 3 and 4.50 euros in central Athens in 2026, with the price higher on the islands and at the airport.",
			"Our newsletter brings you the best of the city every week; subscribe below and never miss a story again from our editors.",
			"Short.",
		}},
		{Title: "Weather", URL: "https://example.org/weather", Paragraphs: []string{
			"Athens is hot in July and August with temperatures well above thirty degrees on most afternoons and warm nights.",
		}},
		{Title: "A tool", URL: "", Kind: "rate", Excerpt: "1 EUR = 0.86 GBP", Paragraphs: nil},
	}
	best, embedded := rankParagraphs(runDir, "the price of a freddo espresso in Athens in euros in 2026", hits)
	if embedded {
		t.Fatal("no embedder here, yet ranked as embedded")
	}
	if best < 0.5 {
		t.Fatalf("the price paragraph should score well: %.2f", best)
	}
	if !strings.Contains(hits[0].Excerpt, "freddo espresso costs") || hits[0].Paragraphs != nil {
		t.Fatalf("excerpt: %q (paragraphs left: %v)", hits[0].Excerpt, hits[0].Paragraphs)
	}
	// the top three kept in page order: the scene paragraph (athens) may come along, the
	// newsletter and the short line never lead
	if strings.Index(hits[0].Excerpt, "coffee scene") > strings.Index(hits[0].Excerpt, "freddo espresso costs") && strings.Contains(hits[0].Excerpt, "coffee scene") {
		t.Fatalf("page order lost: %q", hits[0].Excerpt)
	}
	if hits[2].Excerpt != "1 EUR = 0.86 GBP" {
		t.Fatalf("a hit without paragraphs keeps its excerpt: %q", hits[2].Excerpt)
	}
	// nothing to rank: zero, not embedded
	if b, e := rankParagraphs(runDir, "x", []webHit{{Excerpt: "e"}}); b != 0 || e {
		t.Fatalf("empty: %v %v", b, e)
	}
	if hasParagraphs(hits) {
		t.Fatal("paragraphs should be consumed")
	}
}

func TestBoundWebKeepsParagraphsBounded(t *testing.T) {
	var ps []string
	for i := 0; i < 30; i++ {
		ps = append(ps, strings.Repeat("p", 3000)+"\nnext")
	}
	out := boundWeb([]webHit{{Title: "t", URL: "u", Paragraphs: ps}})
	if len(out) != 1 || len(out[0].Paragraphs) != 16 || len(out[0].Paragraphs[0]) > rankParaMaxLen+len("…") || strings.Contains(out[0].Paragraphs[0], "\n") {
		t.Fatalf("bound: %d paragraphs, first %d chars", len(out[0].Paragraphs), len(out[0].Paragraphs[0]))
	}
}

func TestMoreWebIsAStreamThePhoneAlreadyReads(t *testing.T) {
	rr := httptest.NewRecorder()
	moreWeb(rr, []string{"q1", "q2", "q3", "q4"}, "thin")
	if ct := rr.Header().Get("Content-Type"); ct != "text/event-stream" {
		t.Fatalf("content type %q", ct)
	}
	lines := strings.Split(strings.TrimSpace(rr.Body.String()), "\n\n")
	if len(lines) != 3 {
		t.Fatalf("events: %q", rr.Body.String())
	}
	var ctxEv struct {
		Context []any  `json:"context"`
		Note    string `json:"note"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[0], "data: ")), &ctxEv); err != nil || ctxEv.Context == nil || ctxEv.Note != "thin" {
		t.Fatalf("context event: %v %s", err, lines[0])
	}
	var more struct {
		More struct {
			Queries []string `json:"queries"`
		} `json:"more"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[1], "data: ")), &more); err != nil || len(more.More.Queries) != 3 {
		t.Fatalf("more event: %v %s", err, lines[1])
	}
	var done struct {
		Done bool `json:"done"`
		More bool `json:"more"`
	}
	if err := json.Unmarshal([]byte(strings.TrimPrefix(lines[2], "data: ")), &done); err != nil || !done.Done || !done.More {
		t.Fatalf("done event: %v %s", err, lines[2])
	}
}

func TestPhoneNotesArePresentedAsNotesWithTheirQuote(t *testing.T) {
	hits := boundWeb([]webHit{
		{Title: "Freddo prices", URL: "https://example.gr/freddo", Kind: "note", Excerpt: "- A freddo espresso costs 3 to 4.50 euros in central Athens\n- Islands are dearer",
			Quote: "A freddo espresso costs between 3 and 4.50 euros in central Athens in 2026.", Fetched: "2026-09-27 10:00 UTC"},
		{Title: "Weather", URL: "https://open-meteo.com", Kind: "weather", Source: "open-meteo", Excerpt: "Sunny, 27°C"},
		{Title: "Odd", URL: "https://x.org", Kind: "gossip", Excerpt: "e"},
	})
	if hits[0].Kind != "note" || hits[2].Kind != "page" {
		t.Fatalf("kinds: %s %s", hits[0].Kind, hits[2].Kind)
	}
	out := formatWeb(hits)
	for _, must := range []string{
		"read by the phone's own small model",
		"trust the quote over the notes",
		"[1] Freddo prices (example.gr) , page, read by the phone's model , https://example.gr/freddo",
		"NOTES: - A freddo espresso costs 3 to 4.50 euros in central Athens / - Islands are dearer",
		`QUOTE: "A freddo espresso costs between 3 and 4.50 euros in central Athens in 2026."`,
		"[2] Weather (open-meteo) , weather forecast",
	} {
		if !strings.Contains(out, must) {
			t.Fatalf("formatWeb lacks %q:\n%s", must, out)
		}
	}
	// without notes the caution is not there
	if strings.Contains(formatWeb(hits[1:]), "small model") {
		t.Fatal("caution printed without notes")
	}
	// the box's speed as the phone reads it
	b := planBox(engineSpeed{Known: true, OnGPU: false})
	if b["onGPU"] != false || b["promptTPS"] != 40.0 || b["known"] != true {
		t.Fatalf("planBox CPU: %v", b)
	}
	if b := planBox(engineSpeed{Known: true, OnGPU: true, PromptTPS: 2310.4}); b["promptTPS"] != 2310.0 {
		t.Fatalf("planBox GPU: %v", b)
	}
}

// The planned searches are held to a rule before the phone hears them.
func TestGuardQueries(t *testing.T) {
	history := []chatTurn{
		{Role: "user", Content: "who should I ask about the boat survey?"},
		{Role: "assistant", Content: "Cristina arranged the last survey with Nikos Papadakis in Lefkada, on 0030 2645 022 911."},
	}
	private := []string{"Cristina", "Nikos Papadakis", "Vlad"}
	kept, withheld := guardQueries([]string{
		"boat survey cost Greece",          // public: kept
		"Nikos Papadakis surveyor Lefkada", // a person of theirs, named by the box and not typed by the person
		"marine surveyors Lefkada",         // Lefkada is a place, not one of their people: kept
		"my boat survey Lefkada",           // first person
		"call 0030 2645 022 911",           // a phone number
		"email nikos@example.gr surveyor",  // an address
		"how much did I pay for a survey",  // first person
	}, "what would a survey cost now?", history, private)
	if len(kept) != 2 || kept[0] != "boat survey cost Greece" || kept[1] != "marine surveyors Lefkada" || withheld != 5 {
		t.Fatalf("kept %q, withheld %d", kept, withheld)
	}
	// a name the person typed themselves is theirs to search for, friend of the same name or not
	kept, withheld = guardQueries([]string{"Cristina Scuccia latest album", "Maria Callas biography"}, "what is cristina scuccia's latest album?", nil, []string{"Cristina", "Maria"})
	if len(kept) != 1 || kept[0] != "Cristina Scuccia latest album" || withheld != 1 {
		t.Fatalf("typed names: kept %q, withheld %d", kept, withheld)
	}
	// ordinary searches pass: years, short numbers, "US", the first world war
	kept, withheld = guardQueries([]string{"US election results 2024", "World War I causes", "iPhone 16 price", "flight BA 2490 status", "we are the champions lyrics"}, "x", nil, private)
	if len(kept) != 4 || withheld != 1 { // the last reads as first person: the cost of the rule
		t.Fatalf("ordinary searches: kept %q, withheld %d", kept, withheld)
	}
}

func TestPlanPromptAsksForAnEagerPlan(t *testing.T) {
	p := planPrompt("what's that greek island with the venetian fortress", nil, time.Date(2026, 11, 11, 0, 0, 0, 0, time.UTC))
	for _, want := range []string{"also when you believe you know the answer", "unclear", "exactly three", "the subject's own name alone", "Never put the person's own details"} {
		if !strings.Contains(p, want) {
			t.Fatalf("the plan prompt lacks %q", want)
		}
	}
}
