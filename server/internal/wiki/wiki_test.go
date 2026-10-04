package wiki

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LocalGhostDao/localghost/server/internal/zim/zimtest"
)

const bitcoinPage = `<!DOCTYPE html><html><head><title>Bitcoin</title><style>p{color:red}</style></head>
<body class="mw-body"><div id="mw-content-text"><section data-mw-section-id="0">
<div class="shortdescription">First decentralized cryptocurrency</div>
<table class="infobox"><tr><td><p>Infobox paragraph, never in the lead.</p><table><tr><td>nested</td></tr></table></td></tr></table>
<p class="mw-empty-elt"></p>
<p><b>Bitcoin</b> (abbreviation: <b>BTC</b>) is the first <a href="Decentralization">decentralized</a> cryptocurrency.<sup class="mw-ref reference" id="cite_ref-1"><a href="#cite_note-1"><span class="mw-reflink-text">[1]</span></a></sup> It was invented in 2008 by an unknown person.[2][citation needed]</p>
<p>Bitcoin &amp; its ledger are kept by a network of nodes, without a central bank.</p>
</section><section data-mw-section-id="1"><h2>History</h2><p>Later history, never in the lead.</p></section></div></body></html>`

func testWiki(t *testing.T) *Wiki {
	t.Helper()
	b := zimtest.Build([]zimtest.Item{
		{NS: 'C', Path: "Bitcoin", Mime: "text/html", Body: []byte(bitcoinPage)},
		{NS: 'C', Path: "BTC", Redirect: "Bitcoin"},
		{NS: 'C', Path: "Solana", Mime: "text/html", Body: []byte(`<body><p><b>Solana</b> may refer to: a city in California, a blockchain platform.</p></body>`)},
		{NS: 'C', Path: "Solana_(blockchain_platform)", Title: "Solana (blockchain platform)", Mime: "text/html", Body: []byte(`<body><p>Solana is a blockchain platform which uses a proof-of-stake mechanism. It launched in 2020.</p></body>`)},
		{NS: 'C', Path: "style.css", Mime: "text/css", Body: []byte("p{}")},
		{NS: 'C', Path: "Greenwich", Mime: "text/html", Body: []byte(`<body><p>Greenwich is an area in south-east London, England, on the Thames.</p></body>`)},
		{NS: 'C', Path: "Kassiopi,_Corfu", Title: "Kassiopi, Corfu", Mime: "text/html", Body: []byte(`<body><p>Kassiopi is a village on Corfu.</p></body>`)},
		{NS: 'M', Path: "Title", Mime: "text/plain", Body: []byte("Wikipedia")},
		{NS: 'M', Path: "Date", Mime: "text/plain", Body: []byte("2026-06-14")},
	}, 1)
	dir := t.TempDir()
	path := filepath.Join(dir, "wikipedia_en_all_nopic.zim")
	if err := os.WriteFile(path, b, 0o600); err != nil {
		t.Fatal(err)
	}
	w, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	return w
}

func TestArticleLead(t *testing.T) {
	w := testWiki(t)
	if w.Name != "Wikipedia, 2026-06" {
		t.Fatal(w.Name)
	}
	for _, q := range []string{"Bitcoin", "bitcoin", "BTC", "btc"} {
		a, ok, err := w.Article(q, 2000)
		if err != nil || !ok || a.Title != "Bitcoin" || a.Disamb {
			t.Fatalf("%q: %+v %v %v", q, a, ok, err)
		}
	}
	a, _, _ := w.Article("Bitcoin", 2000)
	want := "Bitcoin (abbreviation: BTC) is the first decentralized cryptocurrency. It was invented in 2008 by an unknown person.\nBitcoin & its ledger are kept by a network of nodes, without a central bank."
	if a.Lead != want {
		t.Fatalf("%q", a.Lead)
	}
	if strings.Contains(a.Lead, "Infobox") || strings.Contains(a.Lead, "Later") || strings.Contains(a.Lead, "[") {
		t.Fatal(a.Lead)
	}
	if short, _, _ := w.Article("Bitcoin", 60); len(short.Lead) > 64 || !strings.HasSuffix(short.Lead, "…") {
		t.Fatalf("%q", short.Lead)
	}
	if s, ok, _ := w.Article("solana", 500); !ok || !s.Disamb {
		t.Fatalf("a disambiguation page says so: %+v", s)
	}
	if s, ok, _ := w.Article("Solana (blockchain platform)", 500); !ok || s.Disamb || !strings.HasPrefix(s.Lead, "Solana is a blockchain") {
		t.Fatalf("%+v", s)
	}
	if _, ok, _ := w.Article("style.css", 100); ok {
		t.Fatal("not an article")
	}
	if _, ok, _ := w.Article("Dogecoin", 100); ok {
		t.Fatal("not there")
	}
	// a place said with its city or country: "Greenwich london" is Greenwich, "Kassiopi Corfu"
	// is "Kassiopi, Corfu"; "Dogecoin London" is still nothing
	if g, ok, _ := w.Article("Greenwich london", 500); !ok || g.Title != "Greenwich" {
		t.Fatalf("%+v %v", g, ok)
	}
	if k, ok, _ := w.Article("kassiopi corfu", 500); !ok || k.Title != "Kassiopi, Corfu" {
		t.Fatalf("%+v %v", k, ok)
	}
	if _, ok, _ := w.Article("Dogecoin London", 100); ok {
		t.Fatal("a qualifier does not make an article")
	}
	ts, err := w.Titles("solana", 5)
	if err != nil || len(ts) != 2 || ts[1] != "Solana (blockchain platform)" {
		t.Fatalf("%v %v", ts, err)
	}
}

func TestVariantsAndShared(t *testing.T) {
	got := variants("solana beach")
	if strings.Join(got, "|") != "solana beach|Solana beach|Solana Beach" {
		t.Fatal(got)
	}
	if v := variants("btc"); v[len(v)-1] != "BTC" {
		t.Fatal(v)
	}
	s := &Shared{Dir: t.TempDir()}
	if _, err := s.Get(); err != ErrNone {
		t.Fatal(err)
	}
	w := testWiki(t)
	s2 := &Shared{Dir: filepath.Dir(w.Path)}
	got2, err := s2.Get()
	if err != nil || got2.Name != "Wikipedia, 2026-06" {
		t.Fatal(err)
	}
}

func TestDropElement(t *testing.T) {
	in := `a<table x><tr><td><table><tr><td>b</td></tr></table></td></tr></table>c<tablex>d</tablex>`
	if got := dropElement(in, "table"); got != "ac<tablex>d</tablex>" {
		t.Fatal(got)
	}
}
