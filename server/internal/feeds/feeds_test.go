package feeds

import (
	"testing"
	"time"
)

const rssFixture = `<?xml version="1.0" encoding="UTF-8"?>
<rss version="2.0" xmlns:content="http://purl.org/rss/1.0/modules/content/">
<channel><title>Example News</title>
<item><title>Minister resigns over leaked memo</title><link>https://ex.org/a</link><guid isPermaLink="false">a-1</guid>
<description><![CDATA[<p>The minister <b>resigned</b> on Tuesday &amp; said nothing more.</p>]]></description>
<pubDate>Tue, 30 Sep 2026 18:20:00 GMT</pubDate></item>
<item><title>Rates held at 4%</title><link>https://ex.org/b</link>
<content:encoded><![CDATA[Long form <i>text</i>]]></content:encoded><pubDate>Wed, 01 Oct 2026 07:00:00 +0100</pubDate></item>
<item><title></title><link>https://ex.org/empty</link></item>
</channel></rss>`

const atomFixture = `<?xml version="1.0"?>
<feed xmlns="http://www.w3.org/2005/Atom"><title>Atom Site</title>
<entry><title>Leaked memo: the minister's resignation</title><id>urn:1</id>
<link rel="alternate" href="https://atom.example/1"/><link rel="enclosure" href="https://atom.example/1.mp3"/>
<summary type="html">&lt;p&gt;Short&lt;/p&gt;</summary><updated>2026-10-01T06:30:00Z</updated></entry>
</feed>`

const rdfFixture = `<?xml version="1.0" encoding="utf-8"?>
<rdf:RDF xmlns:rdf="http://www.w3.org/1999/02/22-rdf-syntax-ns#" xmlns="http://purl.org/rss/1.0/" xmlns:dc="http://purl.org/dc/elements/1.1/">
<channel rdf:about="https://rss.dw.com/rdf/rss-en-all"><title>DW</title></channel>
<item rdf:about="https://dw.com/x"><title>Something in Berlin</title><link>https://dw.com/x</link><description>Desc</description><dc:date>2026-10-01T05:00:00Z</dc:date></item>
</rdf:RDF>`

func TestParseThreeDialects(t *testing.T) {
	f, err := Parse([]byte(rssFixture))
	if err != nil {
		t.Fatal(err)
	}
	if f.Title != "Example News" || len(f.Items) != 2 {
		t.Fatalf("rss: %+v", f)
	}
	a := f.Items[0]
	if a.GUID != "a-1" || a.Link != "https://ex.org/a" || a.Summary != "The minister resigned on Tuesday & said nothing more." {
		t.Fatalf("item: %+v", a)
	}
	if !a.Published.Equal(time.Date(2026, 9, 30, 18, 20, 0, 0, time.UTC)) {
		t.Fatalf("pubDate: %v", a.Published)
	}
	b := f.Items[1]
	if b.GUID != "https://ex.org/b" || b.Summary != "Long form text" || !b.Published.Equal(time.Date(2026, 10, 1, 6, 0, 0, 0, time.UTC)) {
		t.Fatalf("second: %+v", b)
	}
	g, err := Parse([]byte(atomFixture))
	if err != nil || g.Title != "Atom Site" || len(g.Items) != 1 {
		t.Fatalf("atom: %+v %v", g, err)
	}
	if g.Items[0].Link != "https://atom.example/1" || g.Items[0].Summary != "Short" || g.Items[0].GUID != "urn:1" {
		t.Fatalf("atom item: %+v", g.Items[0])
	}
	r, err := Parse([]byte(rdfFixture))
	if err != nil || r.Title != "DW" || len(r.Items) != 1 || r.Items[0].Published.Hour() != 5 {
		t.Fatalf("rdf: %+v %v", r, err)
	}
}

func TestParseRefusesWhatIsNotAFeed(t *testing.T) {
	if _, err := Parse([]byte("<html><body>Please accept cookies</body></html>")); err == nil {
		t.Fatal("an HTML page parsed as a feed")
	}
	if _, err := Parse([]byte("")); err == nil {
		t.Fatal("empty parsed")
	}
	if _, err := Parse([]byte(`<rss><channel><title>x</title></channel></rss>`)); err == nil {
		t.Fatal("a feed with no entries passed")
	}
}

func TestSimilarTitles(t *testing.T) {
	a := Tokens("Minister resigns over leaked memo")
	b := Tokens("Leaked memo: the minister's resignation")
	if !Similar(a, b) {
		t.Fatalf("same story not matched: %v %v", a, b)
	}
	c := Tokens("Rates held at 4% as inflation cools")
	if Similar(a, c) {
		t.Fatal("different stories matched")
	}
	if Similar(Tokens("Live: the day"), Tokens("Live updates")) {
		t.Fatal("stop words alone matched")
	}
}

func TestDefaultSourcesAreWellFormed(t *testing.T) {
	seen := map[string]bool{}
	for _, s := range DefaultSources() {
		if s.ID == "" || s.Name == "" || len(s.URL) < 12 || s.URL[:8] != "https://" || seen[s.ID] {
			t.Fatalf("source: %+v", s)
		}
		seen[s.ID] = true
	}
	if len(seen) != 12 { // the FT is read in its own app
		t.Fatalf("%d sources", len(seen))
	}
	for _, a := range Added {
		if !seen[a.ID] {
			t.Fatalf("%s is added later but not in the list", a.ID)
		}
	}
}
