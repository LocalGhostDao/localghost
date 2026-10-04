package zim

import (
	"bytes"
	"strings"
	"testing"

	"github.com/LocalGhostDao/localghost/server/internal/zim/zimtest"
)

func testFile(t *testing.T, minor uint16) *File {
	t.Helper()
	long := strings.Repeat("The ledger is kept by everyone. ", 400)
	b := zimtest.Build([]zimtest.Item{
		{NS: 'C', Path: "Bitcoin", Mime: "text/html", Body: []byte("<p>Bitcoin is a cryptocurrency.</p>" + long)},
		{NS: 'C', Path: "BTC", Redirect: "Bitcoin"},
		{NS: 'C', Path: "Btc_(currency)", Title: "Btc (currency)", Redirect: "BTC"},
		{NS: 'C', Path: "Solana_(blockchain_platform)", Title: "Solana (blockchain platform)", Mime: "text/html", Body: []byte("<p>Solana is a blockchain platform.</p>")},
		{NS: 'C', Path: "Solana_Beach", Title: "Solana Beach", Mime: "text/html", Body: []byte("<p>A city.</p>")},
		{NS: 'C', Path: "Ethereum", Mime: "text/html", Body: []byte("<p>Ethereum.</p>")},
		{NS: 'M', Path: "Title", Mime: "text/plain", Body: []byte("Wikipedia")},
		{NS: 'M', Path: "Language", Mime: "text/plain", Body: []byte("eng")},
	}, minor)
	z, err := NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	return z
}

func TestReadsPathsTitlesRedirectsAndBothClusters(t *testing.T) {
	z := testFile(t, 1)
	if z.Title() != "Wikipedia" || z.Metadata("Language") != "eng" || z.ArticleNamespace() != 'C' {
		t.Fatalf("%q %q %c", z.Title(), z.Metadata("Language"), z.ArticleNamespace())
	}
	e, ok, err := z.FindPath('C', "Bitcoin")
	if err != nil || !ok || e.Mime != "text/html" || e.Redirect {
		t.Fatalf("%+v %v %v", e, ok, err)
	}
	body, err := z.Content(e)
	if err != nil || !bytes.HasPrefix(body, []byte("<p>Bitcoin is a cryptocurrency.</p>")) || len(body) < 12000 {
		t.Fatalf("%d %v", len(body), err)
	}
	// a redirect of a redirect lands on the article
	r, ok, _ := z.FindPath('C', "Btc_(currency)")
	if !ok || !r.Redirect {
		t.Fatalf("%+v", r)
	}
	end, err := z.Resolve(r)
	if err != nil || end.Path != "Bitcoin" {
		t.Fatalf("%+v %v", end, err)
	}
	if b2, err := z.Content(r); err != nil || !bytes.Equal(b2, body) {
		t.Fatal("content through a redirect", err)
	}
	// every content entry reads, in either cluster
	for _, p := range []string{"Solana_(blockchain_platform)", "Solana_Beach", "Ethereum"} {
		e, ok, err := z.FindPath('C', p)
		if err != nil || !ok {
			t.Fatal(p, err)
		}
		if b, err := z.Content(e); err != nil || !bytes.HasPrefix(b, []byte("<p>")) {
			t.Fatalf("%s: %q %v (cluster %d)", p, b, err, e.Cluster)
		}
	}
	if _, ok, _ := z.FindPath('C', "Nothing"); ok {
		t.Fatal("a path that is not there")
	}
	if _, ok, _ := z.FindPath('A', "Bitcoin"); ok {
		t.Fatal("another namespace")
	}
	// titles
	s, ok, err := z.FindTitle('C', "Solana (blockchain platform)")
	if err != nil || !ok || s.Path != "Solana_(blockchain_platform)" {
		t.Fatalf("%+v %v %v", s, ok, err)
	}
	if _, ok, _ := z.FindTitle('C', "Solana"); ok {
		t.Fatal("a prefix is not a title")
	}
	ps, err := z.TitlesWithPrefix('C', "Solana", 10)
	if err != nil || len(ps) != 2 || ps[0].Title != "Solana (blockchain platform)" || ps[1].Title != "Solana Beach" {
		t.Fatalf("%+v %v", ps, err)
	}
}

func TestRefusesWhatIsNotAZim(t *testing.T) {
	if _, err := NewReader(bytes.NewReader(make([]byte, 100)), 100); err == nil {
		t.Fatal("zeros are not a ZIM file")
	}
	b := testFileBytes(t)
	if _, err := NewReader(bytes.NewReader(b[:90]), 90); err == nil {
		t.Fatal("a truncated file")
	}
}

func testFileBytes(t *testing.T) []byte {
	return zimtest.Build([]zimtest.Item{{NS: 'C', Path: "A", Mime: "text/html", Body: []byte("x")}}, 1)
}

func TestBlobOffsets(t *testing.T) {
	// three blobs, four-byte offsets: 16, 17, 19, 22
	data := []byte{16, 0, 0, 0, 17, 0, 0, 0, 19, 0, 0, 0, 22, 0, 0, 0, 'a', 'b', 'c', 'd', 'e', 'f'}
	for i, want := range []string{"a", "bc", "def"} {
		if b, err := blob(data, false, uint32(i)); err != nil || string(b) != want {
			t.Fatalf("%d: %q %v", i, b, err)
		}
	}
	if _, err := blob(data, false, 3); err == nil {
		t.Fatal("past the last blob")
	}
}

// The newer Kiwix files: no title list in the header, the title order an X entry in an
// uncompressed cluster shared with the search index, gigabytes long (the English Wikipedia's
// cluster 178626 spans 1.4 GB, 3 Oct 2026). The listing and any blob of such a cluster are read
// in place; the cluster is never loaded whole, so the bound on whole clusters does not stop it.
func TestListingInAnUncompressedClusterPastTheBound(t *testing.T) {
	oldC, oldB := maxClusterLen, maxBlobLen
	maxClusterLen, maxBlobLen = 1<<20, 3<<20
	t.Cleanup(func() { maxClusterLen, maxBlobLen = oldC, oldB })
	b := zimtest.BuildWith([]zimtest.Item{
		{NS: 'C', Path: "Bitcoin", Mime: "text/html", Body: []byte("<p>Bitcoin is a cryptocurrency.</p>")},
		{NS: 'C', Path: "BTC", Redirect: "Bitcoin"},
		{NS: 'C', Path: "Solana_(blockchain_platform)", Title: "Solana (blockchain platform)", Mime: "text/html", Body: []byte("<p>Solana is a blockchain platform.</p>")},
		{NS: 'C', Path: "Ethereum", Mime: "text/html", Body: []byte("<p>Ethereum.</p>")},
		{NS: 'M', Path: "Title", Mime: "text/plain", Body: []byte("Wikipedia")},
	}, 1, zimtest.Options{TitleListing: true, Filler: 2 << 20})
	z, err := NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	if z.H.TitlePtrPos != ^uint64(0) || z.H.ClusterCount != 3 {
		t.Fatalf("the shape: %+v", z.H)
	}
	if z.Title() != "Wikipedia" {
		t.Fatalf("title %q", z.Title())
	}
	// the title order comes from the listing, read in place
	s, ok, err := z.FindTitle('C', "Solana (blockchain platform)")
	if err != nil || !ok || s.Path != "Solana_(blockchain_platform)" {
		t.Fatalf("%+v %v %v", s, ok, err)
	}
	if ps, err := z.TitlesWithPrefix('C', "B", 10); err != nil || len(ps) != 2 || ps[0].Title != "BTC" || ps[1].Title != "Bitcoin" {
		t.Fatalf("%+v %v", ps, err)
	}
	// the articles in the ordinary clusters read as before
	e, _, _ := z.FindPath('C', "Ethereum")
	if body, err := z.Content(e); err != nil || string(body) != "<p>Ethereum.</p>" {
		t.Fatalf("%q %v", body, err)
	}
	// a blob of the big cluster reads in place, as long as it is within the blob bound
	x, ok, _ := z.FindPath('X', "fulltext/xapian")
	if !ok || x.Cluster != 2 {
		t.Fatalf("%+v", x)
	}
	if body, err := z.Content(x); err != nil || len(body) != 2<<20 {
		t.Fatalf("%d %v", len(body), err)
	}
	maxBlobLen = 1 << 20
	if _, err := z.Content(x); err == nil || !strings.Contains(err.Error(), "more than this reader loads") {
		t.Fatalf("past the blob bound: %v", err)
	}
	// the listing itself is in the same cluster and still answers (nothing was loaded whole)
	if _, ok, err := z.FindTitle('C', "Bitcoin"); err != nil || !ok {
		t.Fatal(ok, err)
	}
	// a compressed cluster past the whole-cluster bound is refused, with its size (a fresh
	// reader: the first one may hold the cluster decompressed already)
	maxClusterLen = 16
	z2, err := NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		t.Fatal(err)
	}
	sol, _, _ := z2.FindPath('C', "Solana_(blockchain_platform)")
	if sol.Cluster != 1 {
		t.Fatalf("solana in cluster %d, the zstd one is 1", sol.Cluster)
	}
	if _, err := z2.Content(sol); err == nil || !strings.Contains(err.Error(), "decompresses whole") {
		t.Fatalf("past the cluster bound: %v", err)
	}
}
