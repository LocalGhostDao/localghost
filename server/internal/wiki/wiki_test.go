package wiki

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"github.com/LocalGhostDao/localghost/server/internal/zim/zimtest"
)

const bitcoinPage = `<!DOCTYPE html><html><head><title>Bitcoin</title><style>p{color:red}</style></head>
<body class="mw-body"><div id="mw-content-text"><section data-mw-section-id="0">
<div class="shortdescription">First decentralized cryptocurrency</div>
<table class="infobox"><tr><td><p>Infobox paragraph, never in the lead.</p><table><tr><td>nested</td></tr></table></td></tr></table>
<p class="mw-empty-elt"></p>
<p><b>Bitcoin</b> (abbreviation: <b>BTC</b>) is the first <a href="Decentralization">decentralized</a> cryptocurrency.<sup class="mw-ref reference" id="cite_ref-1"><a href="#cite_note-1"><span class="mw-reflink-text">[1]</span></a></sup> It was invented in 2008 by an unknown person.[2][citation needed]</p>
<p>Bitcoin &amp; its ledger are kept by a network of nodes, without a central bank.</p>
</section><section data-mw-section-id="1"><h2>History</h2><p>Later history, never in the lead: the first block was mined in January 2009.</p></section>
<section data-mw-section-id="2"><h2>References</h2><p>Reference one, two and three, long enough to count as a paragraph.</p></section></div></body></html>`

const towerPage = `<body><section data-mw-section-id="0"><p>The <b>Eiffel Tower</b> is a wrought-iron lattice tower in Paris, France. It is named after the engineer Gustave Eiffel.</p></section>
<details data-mw-section-id="1"><summary><h2>History</h2></summary><p>The tower was built between 1887 and 1889 as the entrance to the 1889 World's Fair. It was criticised by artists at first.</p></details>
<details data-mw-section-id="2"><summary><h2>Design</h2></summary><p>The tower is 330 metres tall, about the same height as an 81-storey building. Its base is square, 125 metres on each side.</p>
<details data-mw-section-id="3"><summary><h3>Materials</h3></summary><p>The puddle iron came from the Pompey forges.</p></details></details>
<details data-mw-section-id="4"><summary><h2>See also</h2></summary><p>A list of tall towers, never a section worth reading.</p></details>
<details data-mw-section-id="5"><summary><h2>References</h2></summary><p>Reference one, two and three, all long enough to count.</p></details></body>`

func testWiki(t *testing.T) *Wiki {
	t.Helper()
	b := zimtest.Build([]zimtest.Item{
		{NS: 'C', Path: "Bitcoin", Mime: "text/html", Body: []byte(bitcoinPage)},
		{NS: 'C', Path: "BTC", Redirect: "Bitcoin"},
		{NS: 'C', Path: "Solana", Mime: "text/html", Body: []byte(`<body><p><b>Solana</b> may refer to: a city in California, a blockchain platform.</p></body>`)},
		{NS: 'C', Path: "Solana_(blockchain_platform)", Title: "Solana (blockchain platform)", Mime: "text/html", Body: []byte(`<body><p>Solana is a blockchain platform which uses a proof-of-stake mechanism. It launched in 2020.</p></body>`)},
		{NS: 'C', Path: "style.css", Mime: "text/css", Body: []byte("p{}")},
		{NS: 'C', Path: "Greenwich", Mime: "text/html", Body: []byte(`<body><p>Greenwich is an area in south-east London, England, on the Thames, home of the Royal Observatory.</p></body>`)},
		{NS: 'C', Path: "Kassiopi,_Corfu", Title: "Kassiopi, Corfu", Mime: "text/html", Body: []byte(`<body><p>Kassiopi is a village on the north-east coast of Corfu, Greece.</p></body>`)},
		{NS: 'C', Path: "Eiffel_Tower", Title: "Eiffel Tower", Mime: "text/html", Body: []byte(towerPage)},
		{NS: 'C', Path: "Tour_Eiffel", Title: "Tour Eiffel", Redirect: "Eiffel_Tower"},
		// a page with a stray Latin-1 byte in its title and its text (the database takes UTF-8 only)
		{NS: 'C', Path: "Lodz", Title: "\xc5\x61ód\xc5\xba", Mime: "text/html", Body: []byte("<body><p>\xc5\x61ód\xc5\xba is a city in central Poland, the third largest in the country, with a textile past.</p></body>")},
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

func TestLeadAndBody(t *testing.T) {
	w := testWiki(t)
	if w.Name != "Wikipedia, 2026-06" {
		t.Fatal(w.Name)
	}
	want := "Bitcoin (abbreviation: BTC) is the first decentralized cryptocurrency. It was invented in 2008 by an unknown person.\nBitcoin & its ledger are kept by a network of nodes, without a central bank."
	if got := Lead(bitcoinPage, 2000); got != want {
		t.Fatalf("%q", got)
	}
	if short := Lead(bitcoinPage, 60); len(short) > 64 || !strings.HasSuffix(short, "…") {
		t.Fatalf("%q", short)
	}
	body := Body(bitcoinPage, BodyMax)
	if !strings.HasPrefix(body, "== History ==\nLater history") || strings.Contains(body, "Reference one") {
		t.Fatalf("body %q", body)
	}
	if !disamb("Solana may refer to: a city") || disamb("Solana is a blockchain platform") {
		t.Fatal("disamb")
	}
}

func TestSectionsAndTheBestOne(t *testing.T) {
	secs := Sections(towerPage, 400)
	if len(secs) != 3 || secs[0].Heading != "History" || secs[1].Heading != "Design" || secs[2].Heading != "Materials" {
		t.Fatalf("sections %+v", secs)
	}
	if !strings.HasPrefix(secs[0].Text, "The tower was built between 1887 and 1889") {
		t.Fatalf("history %q", secs[0].Text)
	}
	if s := BestSection(secs, "How tall is the Eiffel Tower?"); s == nil || s.Heading != "Design" {
		t.Fatalf("tall → %+v", s)
	}
	if s := BestSection(secs, "when was the Eiffel Tower built?"); s == nil || s.Heading != "History" {
		t.Fatalf("built → %+v", s)
	}
	if s := BestSection(secs, "what is the Eiffel Tower"); s != nil {
		t.Fatalf("a what-is has no section: %+v", s)
	}
	if s := BestSection(secs, "which iron forges supplied it?"); s == nil || s.Heading != "Materials" {
		t.Fatalf("forges → %+v", s)
	}
	if got := cutAt("One sentence. Another sentence that runs on and on.", 22); got != "One sentence." {
		t.Fatalf("cut %q", got)
	}
	// the body as stored reads back into the same sections
	body := Body(towerPage, BodyMax)
	back := parseBody(body)
	if len(back) != 3 || back[1].Heading != "Design" || !strings.HasPrefix(back[1].Text, "The tower is 330 metres tall") {
		t.Fatalf("parsed back %+v", back)
	}
	if h, txt := SectionFor(body, "how tall is it", 300); h != "Design" || !strings.Contains(txt, "330 metres") {
		t.Fatalf("section for: %q %q", h, txt)
	}
}

func TestNamesInAQuestion(t *testing.T) {
	for q, want := range map[string][]string{
		"How tall is the Eiffel Tower?":                         {"Eiffel Tower"},
		"Tell me about Greenwich":                               {"Greenwich"},
		"is Kassiopi worth visiting, and what about Corfu Town": {"Corfu Town", "Kassiopi"},
		"what time is it":                                       nil,
		"Paris":                                                 {"Paris"},
		"Who was Gustave Eiffel, the engineer?":                 {"Gustave Eiffel"},
		"did Vlad go to Toronto in September":                   {"September", "Toronto", "Vlad"},
	} {
		got := Names(q)
		if strings.Join(got, "|") != strings.Join(want, "|") {
			t.Fatalf("%q: %v (want %v)", q, got, want)
		}
	}
}

func TestDropElement(t *testing.T) {
	in := `a<table x><tr><td><table><tr><td>b</td></tr></table></td></tr></table>c<tablex>d</tablex>`
	if got := dropElement(in, "table"); got != "ac<tablex>d</tablex>" {
		t.Fatal(got)
	}
}

func pgFresh(t *testing.T, name string) *poltergres.ReadWrite {
	t.Helper()
	dir := os.Getenv("GHOST_PG_SOCKET_DIR")
	if dir == "" {
		t.Skip("GHOST_PG_SOCKET_DIR not set; no Postgres to test against")
	}
	port := 5432
	if p, err := strconv.Atoi(os.Getenv("GHOST_PG_PORT")); err == nil {
		port = p
	}
	user := os.Getenv("GHOST_PG_USER")
	if user == "" {
		user = "postgres"
	}
	admin := poltergres.NewReadWrite(dir, port, user, "", "postgres")
	if err := admin.Ping(); err != nil {
		t.Fatalf("postgres unreachable: %v", err)
	}
	if err := admin.ExecSimple("DROP DATABASE IF EXISTS " + name); err != nil {
		t.Fatal(err)
	}
	if err := admin.ExecSimple("CREATE DATABASE " + name); err != nil {
		t.Fatal(err)
	}
	db := poltergres.NewReadWrite(dir, port, user, "", name)
	_ = db.Exec("CREATE EXTENSION IF NOT EXISTS pg_trgm")
	if _, err := hw.ConvergeSchema(db, slog.New(slog.NewTextHandler(io.Discard, nil))); err != nil {
		t.Fatalf("schema: %v", err)
	}
	return db
}

// The store against Postgres: the file imported in slices (the state carried between them), then
// the lookups: the title in any case, a redirect, a qualified place without its comma, a prefix,
// a typo (with pg_trgm), the words of a lead; an article read whole; a new file starts over.
func TestStorePGImportsAndFinds(t *testing.T) {
	db := pgFresh(t, "lg_wiki_store")
	w := testWiki(t)
	s := &Store{DB: db}
	if _, ok := s.Ready(); ok {
		t.Fatal("ready before the import")
	}
	st, err := s.Import(w, 0) // a budget of nothing still reads the first slice, which is all of this file
	if err != nil {
		t.Fatal(err)
	}
	if !st.Done || st.Articles != 7 || st.Redirects != 2 || st.Edition != "Wikipedia, 2026-06" || !s.Current(w) {
		t.Fatalf("state %+v", st)
	}
	if a, r := s.Counts(); a != 7 || r != 2 {
		t.Fatalf("counts %d %d", a, r)
	}
	// the stray byte became the replacement character, the page is in and found by its clean part
	if h, ok, err := s.Best("\uFFFDaód\u017A", 400); err != nil || !ok || !strings.Contains(h.Lead, "central Poland") {
		t.Fatalf("a page with a bad byte: %+v %v %v", h, ok, err)
	}
	if _, ok := s.Ready(); !ok {
		t.Fatal("not ready after the import")
	}
	// the lookup indexes were made at the end (the pattern and text ones at least; the trigram
	// ones only with pg_trgm), and a new import takes them off until it is done
	indexes := func() int {
		rows, _ := db.Query("SELECT count(*) FROM pg_indexes WHERE indexname LIKE 'wiki_%_lc' OR indexname LIKE 'wiki_%_fts' OR indexname LIKE 'wiki_%_trgm'")
		n, _ := strconv.Atoi(*rows.Vals[0][0])
		return n
	}
	if n := indexes(); n < 3 {
		t.Fatalf("%d lookup indexes after the import", n)
	}
	// an import drops the heavy ones and keeps the two title indexes, so a search works meanwhile
	if err := s.DropIndexes(); err != nil || indexes() != 2 {
		t.Fatalf("drop: %v, %d left", err, indexes())
	}
	if h, ok, err := s.Best("bitcoin", 400); err != nil || !ok || h.How != "exact" {
		t.Fatalf("a lookup by title while importing: %+v %v %v", h, ok, err)
	}
	if hits, _ := s.Lookup("royal observatory", 4, 400); len(hits) != 0 {
		t.Fatalf("the words of a lead before the index: %+v", hits)
	}
	if err := s.EnsureIndexes(); err != nil || indexes() < 3 {
		t.Fatalf("ensure: %v, %d", err, indexes())
	}
	for q, want := range map[string][2]string{
		"bitcoin": {"Bitcoin", "exact"}, "BTC": {"Bitcoin", "redirect"}, "tour eiffel": {"Eiffel Tower", "redirect"},
		"kassiopi corfu": {"Kassiopi, Corfu", "qualified"}, "greenwich london": {"Greenwich", "like|text"},
		"eiffel": {"Eiffel Tower", "prefix"}, "royal observatory": {"Greenwich", "text"},
	} {
		h, ok, err := s.Best(q, 400)
		if err != nil || !ok || h.Title != want[0] || !strings.Contains(want[1], h.How) {
			t.Fatalf("%q: %+v %v %v (want %v)", q, h, ok, err, want)
		}
	}
	if s.hasTrgm() {
		if h, ok, _ := s.Best("grenwich", 400); !ok || h.Title != "Greenwich" || h.How != "like" {
			t.Fatalf("typo: %+v %v", h, ok)
		}
	} else {
		t.Log("pg_trgm not installed here; the likeness lookup was not exercised")
	}
	// the exact title is a page of meanings: the lookup lists it first and says so, the article by
	// prefix after it, and Best takes the article
	hits, _ := s.Lookup("solana", 4, 400)
	if len(hits) != 2 || !hits[0].Disamb || hits[0].How != "exact" || hits[1].Title != "Solana (blockchain platform)" {
		t.Fatalf("solana: %+v", hits)
	}
	if b, ok, _ := s.Best("solana", 400); !ok || b.Title != "Solana (blockchain platform)" {
		t.Fatalf("best solana: %+v", b)
	}
	if _, ok, _ := s.Best("dogecoin", 400); ok {
		t.Fatal("not there")
	}
	a, ok, err := s.Article(hits[1].Idx)
	if err != nil || !ok || a.Title != "Solana (blockchain platform)" || !strings.HasPrefix(a.Lead, "Solana is a blockchain") {
		t.Fatalf("article %+v %v %v", a, ok, err)
	}
	tower, _, _ := s.Best("eiffel tower", 400)
	full, _, _ := s.Article(tower.Idx)
	if h, txt := SectionFor(full.Body, "when was the Eiffel Tower built", 300); h != "History" || !strings.Contains(txt, "1887") {
		t.Fatalf("section %q %q", h, txt)
	}
	// the same file: nothing to do; a changed size: everything again
	if st2, err := s.Import(w, time.Second); err != nil || !st2.Done || st2.Articles != 7 {
		t.Fatalf("second import %+v %v", st2, err)
	}
	_ = db.Exec("UPDATE settings SET value = replace(value, '\"size\":', '\"size\":1') WHERE key = $1", ImportKey)
	if s.Current(w) {
		t.Fatal("current for another size")
	}
	st3, err := s.Import(w, time.Second)
	if err != nil || !st3.Done || st3.Articles != 7 || st3.Redirects != 2 {
		t.Fatalf("import again %+v %v", st3, err)
	}
}

// Three readers over their own ranges of the file give the same tables as one; a state from a
// single-reader import goes on as one shard from where it was.
func TestStorePGImportsInParallel(t *testing.T) {
	db := pgFresh(t, "lg_wiki_parallel")
	w := testWiki(t)
	s := &Store{DB: db}
	st, err := s.ImportWith(w, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Done || len(st.Shards) != 3 || st.Read() != st.Total || st.Articles != 7 || st.Redirects != 2 {
		t.Fatalf("state %+v", st)
	}
	if a, r := s.Counts(); a != 7 || r != 2 {
		t.Fatalf("counts %d %d", a, r)
	}
	// the shards cut the entries whole and in order
	var seen uint32
	for i, sh := range st.Shards {
		if sh.From != seen || sh.Next != sh.To || (i == 2 && sh.To != st.Total) {
			t.Fatalf("shard %d %+v", i, sh)
		}
		seen = sh.To
	}
	// a single-reader state, half way: one shard from there, the rest of the file read
	_ = db.Exec("DELETE FROM wiki_articles")
	_ = db.Exec("DELETE FROM wiki_redirects")
	legacy := ImportState{File: st.File, Size: st.Size, Edition: st.Edition, Total: st.Total, Next: st.Total / 2, StartedAt: 1}
	if err := s.Save(legacy); err != nil {
		t.Fatal(err)
	}
	st, err = s.ImportWith(w, 3, 0)
	if err != nil {
		t.Fatal(err)
	}
	if !st.Done || len(st.Shards) != 1 || st.Shards[0].Next != st.Total || st.Articles >= 7 || st.Articles == 0 {
		t.Fatalf("resumed %+v", st)
	}
	// a state wiped after a finished import, the file gone: the state comes back from the tables
	// and the marker, so the box says ready and the chat reads it
	if err := s.Save(ImportState{}); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.Ready(); ok {
		t.Fatal("ready with a wiped state")
	}
	dir := t.TempDir()
	_ = os.WriteFile(filepath.Join(dir, ".imported"), []byte("wikipedia_en_all_nopic_2026-06.zim abc Wikipedia, 2026-06\n"), 0o640)
	st, did := s.Recover(dir)
	if !did || !st.Done || !st.Removed || st.Articles == 0 || st.Edition != "Wikipedia, 2026-06" || st.File != "wikipedia_en_all_nopic_2026-06.zim" {
		t.Fatalf("recovered %+v %v", st, did)
	}
	if _, ok := s.Ready(); !ok {
		t.Fatal("not ready after the recovery")
	}
	// nothing in the tables, or a state already there: nothing to recover
	if _, did := s.Recover(dir); did {
		t.Fatal("recovered over a state")
	}
	_ = db.Exec("DELETE FROM wiki_articles")
	_ = s.Save(ImportState{})
	if _, did := s.Recover(dir); did {
		t.Fatal("recovered from empty tables")
	}
	if n := Shards(10, 4); len(n) != 4 || n[0].To != 2 || n[3].From != 6 || n[3].To != 10 {
		t.Fatalf("%+v", n)
	}
	if n := Shards(3, 8); len(n) != 3 {
		t.Fatalf("%+v", n)
	}
}
