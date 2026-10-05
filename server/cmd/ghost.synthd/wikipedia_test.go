package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LocalGhostDao/localghost/server/internal/wiki"
	"github.com/LocalGhostDao/localghost/server/internal/zim/zimtest"
)

func TestWikiSubject(t *testing.T) {
	for in, want := range map[string]string{
		"What is Solana?":                  "Solana",
		"who was Ada Lovelace":             "Ada Lovelace",
		"so what is the Lightning Network": "Lightning Network",
		"Tell me about Kassiopi.":          "Kassiopi",
		"where is Antipaxos?":              "Antipaxos",
		"how tall is the Eiffel Tower?":    "Eiffel Tower",
		"what is my plan for tomorrow?":    "",
		"tell me about our trip to Corfu":  "",
		"what is the time":                 "",
		"how are you":                      "",
		"What did I do on Thursday?":       "",
	} {
		if got := wikiSubject(in); got != want {
			t.Errorf("%q: %q, want %q", in, got, want)
		}
	}
	names := wikiNames("Is Kassiopi far from Corfu Town, and did Cristina like it", "Vlad", []string{"Cristina Cealicu"})
	if strings.Join(names, "|") != "Corfu Town|Kassiopi" {
		t.Fatalf("names %v", names)
	}
	if n := wikiNames("what did Vlad and I do in Toronto", "Vlad", nil); n != nil {
		t.Fatalf("a question about my own things: %v", n)
	}
}

// The chat's Wikipedia against the store: the test file imported into a fresh database, then a
// coin's article, a "tell me about", a question by name with the section it points at, what the
// box covers and what it leaves to the web, and nothing at all without an import.
func TestWikiFromTheBoxPG(t *testing.T) {
	db := pgFresh(t, "lg_synthd_wiki")
	dir := t.TempDir()
	b := zimtest.Build([]zimtest.Item{
		{NS: 'C', Path: "Solana", Mime: "text/html", Body: []byte(`<body><p>Solana may refer to: a city, a blockchain.</p></body>`)},
		{NS: 'C', Path: "Solana_(blockchain_platform)", Title: "Solana (blockchain platform)", Mime: "text/html",
			Body: []byte(`<body><p>Solana is a blockchain platform which uses a proof-of-stake mechanism to provide smart contract functionality. Its native cryptocurrency is SOL.</p></body>`)},
		{NS: 'C', Path: "Kassiopi", Mime: "text/html", Body: []byte(`<body><p>Kassiopi is a village in the north-east of Corfu, Greece, on the coast facing Albania.</p></body>`)},
		{NS: 'C', Path: "Eiffel_Tower", Title: "Eiffel Tower", Mime: "text/html", Body: []byte(`<body><section data-mw-section-id="0"><p>The Eiffel Tower is a wrought-iron lattice tower in Paris, France, named after the engineer Gustave Eiffel.</p></section>
<details data-mw-section-id="1"><summary><h2>History</h2></summary><p>The tower was built between 1887 and 1889 as the entrance to the 1889 World's Fair, and criticised at first.</p></details>
<details data-mw-section-id="2"><summary><h2>Design</h2></summary><p>The tower is 330 metres tall, about the height of an 81-storey building, with a square base 125 metres on each side.</p></details></body>`)},
		{NS: 'M', Path: "Title", Mime: "text/plain", Body: []byte("Wikipedia")},
		{NS: 'M', Path: "Date", Mime: "text/plain", Body: []byte("2026-06-14")},
	}, 1)
	path := filepath.Join(dir, "wikipedia_en_all_nopic.zim")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	wikiMu.Lock()
	old := wikiStoreV
	wikiStoreV = &wiki.Store{DB: db}
	wikiMu.Unlock()
	t.Cleanup(func() { wikiMu.Lock(); wikiStoreV = old; wikiMu.Unlock() })

	// nothing imported: nothing covered, the network is asked about a coin
	if _, ok := wikiCovers("what is Kassiopi?"); ok {
		t.Fatal("no import: nothing covered")
	}
	if _, _, have := wikiAboutCoin("Solana", "SOL"); have {
		t.Fatal("no import: the network is asked")
	}
	w, err := wiki.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if st, err := wikiStore().Import(w, 0); err != nil || !st.Done {
		t.Fatalf("import %+v %v", st, err)
	}

	src, found, have := wikiAboutCoin("Solana", "SOL")
	if !have || !found || src.From != "Wikipedia" || !strings.HasPrefix(src.Text, "Solana is a blockchain platform") {
		t.Fatalf("%+v %v %v", src, found, have)
	}
	if _, found, have := wikiAboutCoin("Dogecoin", "DOGE"); !have || found {
		t.Fatal("a coin the copy has no article on")
	}
	items := wikiSource("/x/run", "Tell me about Kassiopi")
	if len(items) != 1 || items[0].Source != "wikipedia" || !strings.HasPrefix(items[0].Snippet, "Kassiopi: Kassiopi is a village") || !strings.Contains(items[0].Why, "Wikipedia, 2026-06") {
		t.Fatalf("%+v", items)
	}
	// by name, with the section the question points at
	items = wikiSource("/x/run", "How tall is the Eiffel Tower, and who designed it?")
	if len(items) != 1 || !strings.HasPrefix(items[0].Snippet, "Eiffel Tower: The Eiffel Tower is a wrought-iron") || !strings.Contains(items[0].Snippet, "\nDesign: The tower is 330 metres tall") {
		t.Fatalf("%+v", items)
	}
	if items := wikiSource("/x/run", "tell me about my trip to Kassiopi"); items != nil {
		t.Fatalf("my own things: %+v", items)
	}
	// the box has it: the phone searches nothing. A page of meanings, a subject the copy lacks,
	// a relation ("of") and a question about now all leave the web its turn.
	if why, ok := wikiCovers("what is Kassiopi?"); !ok || !strings.Contains(why, "Kassiopi") {
		t.Fatalf("covers: %q %v", why, ok)
	}
	for _, q := range []string{"what is Solana", "who is Nikos Kazantzakis", "what is the mayor of Kassiopi", "what is Kassiopi like now", "what's the weather in Kassiopi"} {
		if why, ok := wikiCovers(q); ok {
			t.Fatalf("%q: the web's turn, not %q", q, why)
		}
	}
	st := wikiStatus()
	if st.State != "ready" || st.Articles != 4 || st.Name != "Wikipedia, 2026-06" || st.Answers < 2 {
		t.Fatalf("status %+v", st)
	}
}
