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
}

func TestWikiFromTheBox(t *testing.T) {
	dir := t.TempDir()
	b := zimtest.Build([]zimtest.Item{
		{NS: 'C', Path: "Solana", Mime: "text/html", Body: []byte(`<body><p>Solana may refer to: a city, a blockchain.</p></body>`)},
		{NS: 'C', Path: "Solana_(blockchain_platform)", Title: "Solana (blockchain platform)", Mime: "text/html",
			Body: []byte(`<body><p>Solana is a blockchain platform which uses a proof-of-stake mechanism to provide smart contract functionality. Its native cryptocurrency is SOL.</p></body>`)},
		{NS: 'C', Path: "Kassiopi", Mime: "text/html", Body: []byte(`<body><p>Kassiopi is a village in the north-east of Corfu, Greece, on the coast facing Albania.</p></body>`)},
	}, 1)
	if err := os.WriteFile(filepath.Join(dir, "wikipedia_en_all_nopic.zim"), b, 0o600); err != nil {
		t.Fatal(err)
	}
	old := boxWiki
	boxWiki = &wiki.Shared{Dir: dir}
	t.Cleanup(func() { boxWiki = old })
	src, found, have := wikiAboutCoin("Solana", "SOL")
	if !have || !found || src.From != "Wikipedia" || !strings.HasPrefix(src.Text, "Solana is a blockchain platform") {
		t.Fatalf("%+v %v %v", src, found, have)
	}
	if _, found, have := wikiAboutCoin("Dogecoin", "DOGE"); !have || found {
		t.Fatal("a coin the copy has no article on")
	}
	items := wikiSource("/x/run", "Tell me about Kassiopi")
	if len(items) != 1 || items[0].Source != "wikipedia" || !strings.HasPrefix(items[0].Snippet, "Kassiopi: Kassiopi is a village") {
		t.Fatalf("%+v", items)
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
	boxWiki = &wiki.Shared{Dir: t.TempDir()}
	if _, ok := wikiCovers("what is Kassiopi?"); ok {
		t.Fatal("no copy: nothing covered")
	}
	if _, _, have := wikiAboutCoin("Solana", "SOL"); have {
		t.Fatal("no copy: the network is asked")
	}
}
