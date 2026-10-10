package main

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/egress"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
)

// Two feeds tell the same story, a third answers with an HTML page, a fourth is not fetched: the
// entries land once, the story counts two outlets, the feeds' health says what happened, the
// digest goes out once an hour-day, and the chat finds the story by its words.
func TestNewsIngestStoriesAndDigest(t *testing.T) {
	db := pgFresh(t, "lgtest_synthd_news")
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	if err := seedFeeds(db); err != nil {
		t.Fatal(err)
	}
	if err := seedFeeds(db); err != nil { // twice is fine
		t.Fatal(err)
	}
	now := time.Date(2026, 10, 1, 8, 30, 0, 0, time.UTC)
	rss := func(title, link, desc string) string {
		return `<rss version="2.0"><channel><title>x</title><item><title>` + title + `</title><link>` + link + `</link><description>` + desc + `</description><pubDate>` + now.Add(-time.Hour).Format(time.RFC1123Z) + `</pubDate></item></channel></rss>`
	}
	batch := map[string]any{"fetchedAt": now.Unix(), "feeds": []map[string]any{
		{"id": "bbc", "status": 200, "body": rss("Minister resigns over leaked memo", "https://bbc/a", "The minister resigned on Tuesday after a memo leaked.")},
		{"id": "guardian", "status": 200, "body": rss("Leaked memo: the minister's resignation", "https://guardian/b", "A leaked memo ended the minister's week.")},
		{"id": "telegraph", "status": 200, "body": "<html><body>please accept cookies</body></html>"},
		{"id": "npr", "status": 0, "error": "timeout"},
	}}
	raw, _ := json.Marshal(batch)
	res, err := ingestFetched(db, raw, now)
	if err != nil {
		t.Fatal(err)
	}
	if res.Feeds != 4 || res.OK != 2 || res.NewItems != 2 || res.NewStories != 1 || len(res.Failed) != 2 {
		t.Fatalf("%+v", res)
	}
	// the same batch again: nothing new
	res, err = ingestFetched(db, raw, now.Add(time.Minute))
	if err != nil || res.NewItems != 0 || res.NewStories != 0 {
		t.Fatalf("again: %+v %v", res, err)
	}
	st, err := newsStatus(db, now.Add(time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if st.FeedsOKLast3h != 2 || st.Items7d != 2 || st.Stories24h != 1 {
		t.Fatalf("status: %+v", st)
	}
	byID := map[string]newsFeed{}
	for _, f := range st.Feeds {
		byID[f.ID] = f
	}
	if byID["bbc"].Status != "ok" || byID["bbc"].Items != 1 || !strings.HasPrefix(byID["telegraph"].Status, "not a feed") || byID["telegraph"].Failures != 2 || byID["npr"].Status != "fetch failed: timeout" {
		t.Fatalf("feeds: %+v", byID)
	}
	rows, _ := db.Query("SELECT sources, title FROM news_stories")
	if len(rows.Vals) != 1 || *rows.Vals[0][0] != "2" {
		t.Fatalf("story: %v", rows.Vals)
	}
	// the chat finds it by its words
	items := newsItemsFor(db, []string{"minister", "memo"}, now)
	if len(items) != 2 || items[0].Source != "news" || !strings.Contains(items[0].Snippet+items[1].Snippet, "Minister resigns") {
		t.Fatalf("news source: %+v", items)
	}
	if items := newsItemsFor(db, []string{"ferries", "corfu"}, now); len(items) != 0 {
		t.Fatalf("an unrelated question matched: %+v", items)
	}
	// the digest, in the person's zone: 07:00 in Athens is 04:00 UTC
	_ = db.Exec("INSERT INTO settings (key, value) VALUES ('local_tz','Europe/Athens')")
	if _, _, due := digestDue(db, time.Date(2026, 10, 1, 7, 0, 0, 0, time.UTC)); due {
		t.Fatal("07:00 UTC is 10:00 in Athens: no digest")
	}
	kind, day, due := digestDue(db, time.Date(2026, 10, 1, 4, 10, 0, 0, time.UTC))
	if !due || kind != "07:00" || day != "2026-10-01" {
		t.Fatalf("due: %v %s %s", due, kind, day)
	}
	var posted []hw.Notification
	produce := func(n hw.Notification) error { posted = append(posted, n); return nil }
	n, err := postDigest(db, now, kind, day, produce, lg)
	if err != nil || n != 1 || len(posted) != 1 || posted[0].Kind != "news" || !strings.Contains(posted[0].Body, "Minister resigns") || !strings.Contains(posted[0].Body, "• Minister resigns") || !strings.Contains(posted[0].Body, " · 2 outlets") {
		t.Fatalf("digest: %d %v %+v", n, err, posted)
	}
	if _, _, due := digestDue(db, time.Date(2026, 10, 1, 4, 20, 0, 0, time.UTC)); due {
		t.Fatal("the same digest offered twice in one day")
	}
	// nothing new since: the next digest sends nothing and still marks the hour
	n, err = postDigest(db, now.Add(12*time.Hour), "19:00", day, produce, lg)
	if err != nil || n != 0 || len(posted) != 1 {
		t.Fatalf("empty digest: %d %v %d", n, err, len(posted))
	}
	// the feed commands' SQL
	if err := db.Exec("INSERT INTO news_feeds (id, name, url, added_at) VALUES ($1,$2,$3,$4) ON CONFLICT (id) DO UPDATE SET name = EXCLUDED.name, url = EXCLUDED.url, enabled = true", "x", "X", "https://x/feed", now.Unix()); err != nil {
		t.Fatal(err)
	}
	if err := db.Exec("UPDATE news_feeds SET enabled = $2 WHERE id = $1", "x", false); err != nil {
		t.Fatal(err)
	}
}

// The box fetches the feeds itself only when the phone is not on Wi-Fi: with the phone's word
// "wifi" five minutes old it leaves them; on "mobile" it fetches the due feeds from the box and
// the batch says so.
func TestNewsFetchedByTheBoxWhenThePhoneIsAway(t *testing.T) {
	db := pgFresh(t, "lgtest_synthd_newsbox")
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	now := time.Date(2026, 10, 1, 8, 30, 0, 0, time.UTC)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<rss version="2.0"><channel><title>x</title><item><title>Box fetched this ` + r.URL.Path + `</title><link>https://x` + r.URL.Path + `</link><pubDate>` + now.Add(-time.Hour).Format(time.RFC1123Z) + `</pubDate></item></channel></rss>`))
	}))
	defer srv.Close()
	// two feeds of our own, the defaults off
	_ = db.Exec("INSERT INTO settings (key, value) VALUES ('news_seeded','1')")
	_ = db.Exec("INSERT INTO news_feeds (id, name, url, added_at) VALUES ('a','A',$1,1),('b','B',$2,1)", srv.URL+"/a", srv.URL+"/b")
	client := egress.New()
	// the phone on Wi-Fi five minutes ago: left to the phone
	_ = db.Exec("INSERT INTO settings (key, value) VALUES ('phone_net','wifi'),('phone_seen',$1)", strconv.FormatInt(now.Add(-5*time.Minute).Unix(), 10))
	what, err := newsFetchByBox(context.Background(), db, client, now, false, lg)
	if err != nil || !strings.HasPrefix(what, "left to the phone") {
		t.Fatalf("%q %v", what, err)
	}
	// on mobile: the box fetches both
	_ = db.Exec("UPDATE settings SET value = 'mobile' WHERE key = 'phone_net'")
	what, err = newsFetchByBox(context.Background(), db, client, now, false, lg)
	if err != nil || !strings.HasPrefix(what, "fetched 2 feeds (2 answered, 2 new entries)") {
		t.Fatalf("%q %v", what, err)
	}
	st, _ := newsStatus(db, now)
	if st.LastBy != "box" || st.LastByAt != now.Unix() || st.FeedsOKLast3h != 2 {
		t.Fatalf("%+v", st)
	}
	// fetched just now: nothing due, even on mobile
	if what, _ := newsFetchByBox(context.Background(), db, client, now.Add(time.Minute), false, lg); !strings.HasPrefix(what, "nothing due") {
		t.Fatalf("%q", what)
	}
	// forced: fetched again whatever the marks say
	if what, _ := newsFetchByBox(context.Background(), db, client, now.Add(time.Minute), true, lg); !strings.HasPrefix(what, "fetched 2 feeds") {
		t.Fatalf("%q", what)
	}
}

// A story's articles are read for its summary: a free page whole, a page that says it is not free
// only as far as it serves anyone, marked paywalled.
func TestArticlesRead(t *testing.T) {
	db := pgFresh(t, "lgtest_synthd_articles")
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	now := time.Date(2026, 10, 1, 8, 30, 0, 0, time.UTC)
	para := func(i int) string {
		return "<p>The minister resigned on Tuesday (part " + strconv.Itoa(i) + ") after a vote of 312 to 290 in the chamber, ending a week of talks.</p>"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/free" {
			var b strings.Builder
			for i := 0; i < 8; i++ {
				b.WriteString(para(i))
			}
			w.Write([]byte("<html><article>" + b.String() + "</article></html>"))
			return
		}
		w.Write([]byte(`<html><script type="application/ld+json">{"isAccessibleForFree": false}</script><article>` + para(0) + `</article></html>`))
	}))
	defer srv.Close()
	_ = db.Exec("INSERT INTO news_feeds (id, name, url, added_at) VALUES ('a','A','x',1), ('b','B','y',1)")
	_ = db.Exec("INSERT INTO news_stories (id, first_seen, last_seen, title, sources) VALUES (7, $1, $1, 'Minister resigns', 2)", now.Add(-time.Hour).Unix())
	for _, it := range []struct{ feed, guid, path string }{{"a", "g1", "/free"}, {"b", "g2", "/paid"}} {
		if err := db.Exec("INSERT INTO news_items (feed_id, guid, link, title, published, fetched, story_id) VALUES ($1,$2,$3,'Minister resigns',$4,$4,7)",
			it.feed, it.guid, srv.URL+it.path, now.Add(-time.Hour).Unix()); err != nil {
			t.Fatal(err)
		}
	}
	if n, err := articlePass(context.Background(), db, egress.New(), now, lg); err != nil || n != 2 {
		t.Fatalf("read: %d %v", n, err)
	}
	status := func(guid string) (string, int) {
		rows, _ := db.Query("SELECT body_status, length(body) FROM news_items WHERE guid = $1", guid)
		n, _ := strconv.Atoi(*rows.Vals[0][1])
		return *rows.Vals[0][0], n
	}
	if st, n := status("g1"); st != "ok" || n < 600 {
		t.Fatalf("free: %s %d", st, n)
	}
	if st, n := status("g2"); st != "paywalled" || n < 50 {
		t.Fatalf("not free: %s %d", st, n)
	}
	if a := storyArticle(db, 7); !strings.Contains(a, "(part 7)") {
		t.Fatalf("the story's article is the free whole one: %.80q", a)
	}
	// a day on, unsummarised: the article is let go, what was learnt of it stays
	if _, err := articlePass(context.Background(), db, egress.New(), now.Add(25*time.Hour), lg); err != nil {
		t.Fatal(err)
	}
	if a := storyArticle(db, 7); a != "" {
		t.Fatalf("an article outlived its day: %.40q", a)
	}
	if st, n := status("g1"); st != "ok" || n != 0 {
		t.Fatalf("after a day: %s %d", st, n)
	}
}

// The FT an earlier list seeded is taken off once (its entries and its count in the stories with
// it); a feed that has never once given a feed is switched off after six tries, and only then.
func TestFeedsRetiredAndGivenUp(t *testing.T) {
	db := pgFresh(t, "lgtest_synthd_feedsretired")
	now := time.Date(2026, 10, 1, 8, 30, 0, 0, time.UTC)
	// a box seeded by the earlier list: the FT among its feeds, an entry of it in a story
	_ = db.Exec("INSERT INTO settings (key, value) VALUES ('news_seeded','1')")
	_ = db.Exec("INSERT INTO news_feeds (id, name, url, added_at) VALUES ('ft','Financial Times','https://www.ft.com/rss/home',1), ('bbc','BBC','https://feeds.bbci.co.uk/news/rss.xml',1), ('dead','Dead','https://dead.example/feed',1)")
	_ = db.Exec("INSERT INTO news_stories (id, first_seen, last_seen, title, sources) VALUES (3, $1, $1, 'Rates held', 2)", now.Unix())
	_ = db.Exec("INSERT INTO news_items (feed_id, guid, title, published, fetched, story_id) VALUES ('ft','f1','Rates held',$1,$1,3), ('bbc','b1','Rates held',$1,$1,3)", now.Unix())
	if err := seedFeeds(db); err != nil {
		t.Fatal(err)
	}
	if err := seedFeeds(db); err != nil { // once only
		t.Fatal(err)
	}
	// the FT off; the paper the later list brings (the WSJ) on, once: taken off by the operator,
	// it stays off at the next start
	rows, _ := db.Query("SELECT id FROM news_feeds ORDER BY id")
	if len(rows.Vals) != 3 || *rows.Vals[0][0] != "bbc" || *rows.Vals[1][0] != "dead" || *rows.Vals[2][0] != "wsj-world" {
		t.Fatalf("feeds: %v", rows.Vals)
	}
	if rows, _ := db.Query("SELECT sources FROM news_stories WHERE id = 3"); *rows.Vals[0][0] != "1" {
		t.Fatalf("the story still counts the FT: %s", *rows.Vals[0][0])
	}
	_ = db.Exec("DELETE FROM news_feeds WHERE id = 'wsj-world'")
	if err := seedFeeds(db); err != nil {
		t.Fatal(err)
	}
	if rows, _ := db.Query("SELECT 1 FROM news_feeds WHERE id = 'wsj-world'"); len(rows.Vals) != 0 {
		t.Fatal("a feed the operator took off came back")
	}
	enabled := func() string {
		rows, _ := db.Query("SELECT enabled FROM news_feeds WHERE id = 'dead'")
		return *rows.Vals[0][0]
	}
	batch, _ := json.Marshal(map[string]any{"fetchedAt": now.Unix(), "feeds": []map[string]any{{"id": "dead", "status": 404}}})
	for i := 1; i <= 6; i++ {
		if _, err := ingestFetched(db, batch, now.Add(time.Duration(i)*2*time.Hour)); err != nil {
			t.Fatal(err)
		}
		if want := map[bool]string{true: "f", false: "t"}[i >= 6]; enabled() != want {
			t.Fatalf("after %d tries: enabled %s", i, enabled())
		}
	}
}

// An outlet that refused six pages in a day and gave none is left alone for the day: its next
// story is told from the feeds' words, and another outlet's pages are still read.
func TestRefusedOutletLeftAlone(t *testing.T) {
	db := pgFresh(t, "lgtest_synthd_refused")
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	now := time.Date(2026, 10, 2, 8, 30, 0, 0, time.UTC)
	asked := map[string]int{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked[r.URL.Path]++
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()
	_ = db.Exec("INSERT INTO news_feeds (id, name, url, added_at) VALUES ('tg','Telegraph','x',1), ('bbc','BBC','y',1)")
	_ = db.Exec("INSERT INTO news_stories (id, first_seen, last_seen, title, sources) VALUES (9, $1, $1, 'Storms', 2)", now.Add(-time.Hour).Unix())
	// six refusals from the Telegraph within the day
	for i := 0; i < 6; i++ {
		_ = db.Exec("INSERT INTO news_items (feed_id, guid, link, title, published, fetched, body_at, body_status) VALUES ('tg',$1,'http://x','Old',$2,$2,$2,'HTTP 403')",
			"old"+strconv.Itoa(i), now.Add(-3*time.Hour).Unix())
	}
	_ = db.Exec("INSERT INTO news_items (feed_id, guid, link, title, published, fetched, story_id) VALUES ('tg','t1',$1,'Storms',$2,$2,9), ('bbc','b1',$3,'Storms',$2,$2,9)",
		srv.URL+"/tg", now.Add(-time.Hour).Unix(), srv.URL+"/bbc")
	if _, err := articlePass(context.Background(), db, egress.New(), now, lg); err != nil {
		t.Fatal(err)
	}
	if asked["/tg"] != 0 || asked["/bbc"] != 1 {
		t.Fatalf("asked: %v", asked)
	}
	// a day on, the Telegraph is asked again
	if _, err := articlePass(context.Background(), db, egress.New(), now.Add(25*time.Hour), lg); err != nil {
		t.Fatal(err)
	}
	if asked["/tg"] != 1 {
		t.Fatalf("a day on: %v", asked)
	}
}

// The last day's model summaries are written again once in the lead-and-points shape, their
// articles to be read again; the brief's stories ride with /v1/news.
func TestRetellAndBriefStories(t *testing.T) {
	db := pgFresh(t, "lgtest_synthd_retell")
	now := time.Now()
	_ = db.Exec("INSERT INTO news_feeds (id, name, url, added_at) VALUES ('bbc','BBC','y',1)")
	_ = db.Exec(`INSERT INTO news_stories (id, first_seen, last_seen, title, sources, summary, written_by, tries) VALUES
		(1, $1, $1, 'Today', 2, 'A paragraph.', 'model', 1), (2, $2, $2, 'Last week', 2, 'Kept as it was.', 'model', 1)`, now.Add(-time.Hour).Unix(), now.Add(-72*time.Hour).Unix())
	_ = db.Exec("INSERT INTO news_items (feed_id, guid, link, title, published, fetched, story_id, body_at, body_status) VALUES ('bbc','b1','http://x','Today',$1,$1,1,$1,'ok')", now.Add(-time.Hour).Unix())
	for i := 0; i < 2; i++ { // once only
		if err := retellStories(db, now); err != nil {
			t.Fatal(err)
		}
		if i == 0 {
			_ = db.Exec("UPDATE news_stories SET summary = 'Told again.\n- A point of it.' WHERE id = 1")
		}
	}
	rows, _ := db.Query("SELECT id, summary FROM news_stories ORDER BY id")
	if *rows.Vals[0][1] != "Told again.\n- A point of it." || *rows.Vals[1][1] != "Kept as it was." {
		t.Fatalf("%v %v", *rows.Vals[0][1], *rows.Vals[1][1])
	}
	_ = db.Exec(`INSERT INTO settings (key, value) VALUES ('news_brief', '{"at":5,"text":"- One.\n- Two.","stories":[1,2]}')`)
	d, err := hw.NewsDocNow(db, now.Add(-48*time.Hour).Unix(), 0, now.Unix())
	if err != nil || len(d.Stories) != 1 || d.Brief != "- One.\n- Two." || len(d.BriefStories) != 2 || d.BriefStories[1] != 2 {
		t.Fatalf("%+v %v", d, err)
	}
}

// "Write the brief now" says why when it cannot: no summaries yet; an up-to-date brief stands
// unless the phone asks, and then the model is asked (here none answers, which it says).
func TestBriefNowSaysWhy(t *testing.T) {
	db := pgFresh(t, "lgtest_synthd_briefnow")
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	oc := oracle.NewClient(t.TempDir(), time.Second)
	now := time.Now()
	if wrote, why, err := briefPass(db, oc, now, lg, true); err != nil || wrote || !strings.Contains(why, "fewer than two") {
		t.Fatalf("%v %q %v", wrote, why, err)
	}
	_ = db.Exec(`INSERT INTO news_stories (id, first_seen, last_seen, title, sources, summary, written_by) VALUES
		(1, $1, $1, 'A', 3, 'The first story.', 'model'), (2, $1, $1, 'B', 2, 'The second story.', 'model')`, now.Unix())
	_ = db.Exec(`INSERT INTO settings (key, value) VALUES ('news_brief', $1)`, `{"at":`+strconv.FormatInt(now.Unix()-60, 10)+`,"text":"- One.\n- Two.","stories":[1,2]}`)
	if wrote, why, _ := briefPass(db, oc, now, lg, false); wrote || why != "the brief is up to date" {
		t.Fatalf("%v %q", wrote, why)
	}
	if wrote, why, _ := briefPass(db, oc, now, lg, true); wrote || why == "the brief is up to date" || why == "" {
		t.Fatalf("forced, past the age check: %v %q", wrote, why)
	}
}

// A fact about one of my people joins their memory; the same fact twice is kept once.
func TestNotePerson(t *testing.T) {
	db := pgFresh(t, "lgtest_synthd_person")
	if err := notePerson(db, "Cristina", "My partner loves the sea.", "chat:1", 1, 1000); err != nil {
		t.Fatal(err)
	}
	if err := notePerson(db, "cristina", "She is learning Greek.", "chat:2", 2, 2000); err != nil {
		t.Fatal(err)
	}
	_ = notePerson(db, "Cristina", "She is learning Greek.", "chat:3", 3, 3000)
	rows, _ := db.Query("SELECT title, body, kind FROM memories WHERE kind = 'person'")
	if len(rows.Vals) != 1 || *rows.Vals[0][1] != "My partner loves the sea. She is learning Greek." || *rows.Vals[0][0] != "Cristina" {
		t.Fatalf("%v", rows.Vals)
	}
	if n := peopleNames(db); len(n) != 1 || n[0] != "Cristina" {
		t.Fatal(n)
	}
}

// The coins to write: the top of the newest list, never written or due again; a try is not tried
// again for three days, a text kept for ninety; the coin page reads what was written.
func TestCoinsToWrite(t *testing.T) {
	db := pgFresh(t, "lgtest_synthd_coins")
	now := time.Unix(1_790_000_000, 0)
	for i, sym := range []string{"BTC", "ETH", "SOL", "XRP"} {
		if err := db.Exec("INSERT INTO coin_ranks (ts, rank, coin_id, symbol, name) VALUES ($1,$2,$3,$3,$4)", now.Unix()-3600, i+1, sym, sym+" coin"); err != nil {
			t.Fatal(err)
		}
	}
	_ = db.Exec("INSERT INTO coin_ranks (ts, rank, coin_id, symbol, name) VALUES ($1,1,'OLD','OLD','old list')", now.Unix()-90000)
	if err := db.Exec("INSERT INTO coin_info (symbol, name, description, website) VALUES ('BTC','Bitcoin','The first.','https://bitcoin.org')"); err != nil {
		t.Fatal(err)
	}
	got, err := coinsToWrite(db, now)
	if err != nil || len(got) != 2 || got[0] != [4]string{"BTC", "Bitcoin", "The first.", "https://bitcoin.org"} || got[1][0] != "ETH" || got[1][1] != "ETH coin" {
		t.Fatalf("%v %v", got, err)
	}
	if err := saveCoinText(db, "BTC", "Bitcoin", "Bitcoin is a cryptocurrency.", "Wikipedia, Coinbase", now); err != nil {
		t.Fatal(err)
	}
	if err := markCoinTried(db, "ETH", "ETH coin", now); err != nil {
		t.Fatal(err)
	}
	if got, _ := coinsToWrite(db, now); len(got) != 2 || got[0][0] != "SOL" || got[1][0] != "XRP" {
		t.Fatalf("written and tried are left: %v", got)
	}
	if got, _ := coinsToWrite(db, now.Add(4*24*time.Hour)); len(got) != 2 || got[0][0] != "ETH" {
		t.Fatalf("a try again after three days: %v", got)
	}
	if got, _ := coinsToWrite(db, now.Add(91*24*time.Hour)); len(got) != 2 || got[0][0] != "BTC" {
		t.Fatalf("a text again after ninety: %v", got)
	}
	rows, _ := db.Query("SELECT description, written, written_from FROM coin_info WHERE symbol = 'BTC'")
	if len(rows.Vals) != 1 || *rows.Vals[0][0] != "The first." || *rows.Vals[0][1] != "Bitcoin is a cryptocurrency." || *rows.Vals[0][2] != "Wikipedia, Coinbase" {
		t.Fatalf("%v", rows.Vals)
	}
	if s := coinDescStatus(db); s != "1 of 4 coins written" {
		t.Fatal(s)
	}
}

// A fact about me from a check-in joins the check-ins' memory of that title once; the note's own
// memory of the same title is left alone.
func TestNoteMe(t *testing.T) {
	db := pgFresh(t, "lgtest_synthd_noteme")
	if err := db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ('Home','Vlad lives in London.','me','about:me:0',1,1)"); err != nil {
		t.Fatal(err)
	}
	if ok, err := noteMe(db, "Home", "Vlad lives in Islington.", "checkin:7:0", 2); err != nil || !ok {
		t.Fatal(ok, err)
	}
	if ok, _ := noteMe(db, "home", "Vlad has a cat.", "checkin:8:0", 3); !ok {
		t.Fatal("joins by title")
	}
	if ok, _ := noteMe(db, "Home", "Vlad has a cat", "checkin:9:0", 4); ok {
		t.Fatal("the same fact twice")
	}
	rows, _ := db.Query("SELECT body, source_ref FROM memories WHERE kind = 'me' ORDER BY id")
	if len(rows.Vals) != 2 || *rows.Vals[0][0] != "Vlad lives in London." || *rows.Vals[1][0] != "Vlad lives in Islington. Vlad has a cat." || *rows.Vals[1][1] != "checkin:7:0" {
		t.Fatalf("%v", rows.Vals)
	}
}

// The places counted from the routes and the photos become memories, once until they change, and
// the sheet of the last weeks reads without an error.
func TestPlacesPassAndInsightFacts(t *testing.T) {
	db := pgFresh(t, "lgtest_synthd_places")
	mount := t.TempDir()
	paths := filepath.Join(mount, "frames", "paths")
	if err := os.MkdirAll(paths, 0o755); err != nil {
		t.Fatal(err)
	}
	now := time.Now()
	for i := 1; i <= 3; i++ {
		d := now.AddDate(0, 0, -7*i).UTC().Format("2006-01-02")
		b := `{"day":"` + d + `","walkM":4200,"stays":[{"name":"Regent's Park","from":0,"to":7200}]}`
		if err := os.WriteFile(filepath.Join(paths, d+".route.json"), []byte(b), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	for i, ts := range []int64{now.AddDate(0, 0, -3).Unix(), now.AddDate(0, 0, -2).Unix()} {
		for j := 0; j < 6; j++ {
			if err := db.Exec("INSERT INTO frames (hash, archive_path, taken_at, kind, place) VALUES ($1,'/x',$2,'photo','Europe / Greece / Corfu / Kassiopi')",
				"h"+strconv.Itoa(i)+strconv.Itoa(j), ts+int64(j)); err != nil {
				t.Fatal(err)
			}
		}
	}
	_ = setSetting(db, ownerKey, "Vlad")
	lg := slog.New(slog.NewTextHandler(io.Discard, nil))
	n, err := placesPass(db, mount, lg)
	if err != nil || n != 2 {
		t.Fatal(n, err)
	}
	rows, _ := db.Query("SELECT title, body FROM memories WHERE kind = 'place' ORDER BY title")
	if len(rows.Vals) != 2 || *rows.Vals[0][0] != "Kassiopi" || !strings.Contains(*rows.Vals[0][1], "12 photos taken there") ||
		!strings.HasPrefix(*rows.Vals[1][1], "Vlad has been at Regent's Park on 3 days") {
		t.Fatalf("%v", rows.Vals)
	}
	if n, _ := placesPass(db, mount, lg); n != 0 {
		t.Fatal("nothing changed: nothing written")
	}
	facts := insightFacts(db, mount, "Vlad", now)
	if len(facts) < 2 || !strings.Contains(strings.Join(facts, "\n"), "walked about 12 km in the last 30 days") {
		t.Fatalf("%q", facts)
	}
}
