package main

// THE NEWS, read by the box, fetched by the phone. The publication list, the entries, the stories
// (one story for the same event across outlets), the model's summaries and the two digests a day
// all live here; the bytes arrive from the phone, which fetched each feed the way it runs the web
// search round (the box opens no connection, so publishers see the phone's address, and the box
// gets nothing while the phone is off). The box never fetches an article: the phone does when the
// person opens one.
//
// The inbox: secd drops what the phone posted (/v1/news/fetched) as JSON files under
// <mount>/synthd/news/inbox; this drains them, parses each feed (internal/feeds), keeps the new
// entries, and groups them into stories by title. A story's summary is written by the model from
// the outlets' own titles and descriptions and held to the same "every number must appear in a
// source" check as the day memories. A digest goes out at 07:00 and 19:00 in the person's zone.

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/apparedis"
	"github.com/LocalGhostDao/localghost/server/internal/egress"
	"github.com/LocalGhostDao/localghost/server/internal/feeds"
	"github.com/LocalGhostDao/localghost/server/internal/feedstat"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

const (
	newsStoryWindow  = 48 * time.Hour // entries this far apart can be one story
	newsKeepDays     = 30             // entries older than this go
	newsModelPerPass = 6              // summaries written per pass
	newsModelTries   = 3
	newsDigestTop    = 8
)

var newsDigestHours = feeds.DigestHours

// fetchedBatch is what the phone posts: each feed's bytes, or why there are none.
type fetchedBatch struct {
	FetchedAt int64  `json:"fetchedAt"`
	By        string `json:"by,omitempty"` // "box" when synthd fetched it; the phone sends none
	Feeds     []struct {
		ID     string `json:"id"`
		Status int    `json:"status"` // HTTP status, 0 when the fetch itself failed
		Error  string `json:"error,omitempty"`
		Body   string `json:"body,omitempty"`
		TookMs int    `json:"tookMs,omitempty"`
	} `json:"feeds"`
}

// newsResult is what one batch did.
type newsResult struct {
	Feeds, OK, NewItems, NewStories int
	Failed                          []string
}

// seedFeeds puts the default list in once; an operator's edits stand after that. The papers an
// earlier list seeded and this one does not are taken off once (feeds.Retired).
func seedFeeds(db *poltergres.ReadWrite) error {
	if err := retireFeeds(db); err != nil {
		return err
	}
	if err := retellStories(db, time.Now()); err != nil {
		return err
	}
	rows, err := db.Query("SELECT value FROM settings WHERE key = 'news_seeded'")
	if err != nil {
		return err
	}
	if len(rows.Vals) > 0 {
		return nil
	}
	now := time.Now().Unix()
	for _, s := range feeds.DefaultSources() {
		if err := db.Exec("INSERT INTO news_feeds (id, name, url, added_at) VALUES ($1,$2,$3,$4) ON CONFLICT (id) DO NOTHING", s.ID, s.Name, s.URL, now); err != nil {
			return err
		}
	}
	return db.Exec("INSERT INTO settings (key, value) VALUES ('news_seeded', $1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", strconv.FormatInt(now, 10))
}

// retireFeeds takes the retired papers off, once: the feed, its entries, and its count in the
// stories it told.
func retireFeeds(db *poltergres.ReadWrite) error {
	rows, err := db.Query("SELECT value FROM settings WHERE key = 'news_retired_v1'")
	if err != nil {
		return err
	}
	if len(rows.Vals) > 0 {
		return nil
	}
	for _, r := range feeds.Retired {
		if err := db.Exec("DELETE FROM news_items WHERE feed_id = $1 AND EXISTS (SELECT 1 FROM news_feeds f WHERE f.id = $1 AND f.url = $2)", r.ID, r.URL); err != nil {
			return err
		}
		if err := db.Exec("DELETE FROM news_feeds WHERE id = $1 AND url = $2", r.ID, r.URL); err != nil {
			return err
		}
	}
	if err := db.Exec(`UPDATE news_stories s SET sources = greatest(1, (SELECT count(DISTINCT feed_id) FROM news_items i WHERE i.story_id = s.id))
		WHERE s.last_seen >= extract(epoch from now())::bigint - 30*86400`); err != nil {
		return err
	}
	return db.Exec("INSERT INTO settings (key, value) VALUES ('news_retired_v1', $1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", strconv.FormatInt(time.Now().Unix(), 10))
}

// retellStories asks once for the last day's summaries again, in the lead-and-points shape (2 Oct
// 2026): the model's summaries cleared, their articles to be read again, the brief rewritten from
// them. Older stories keep the paragraph they had; the phone shows it as a lead alone.
func retellStories(db *poltergres.ReadWrite, now time.Time) error {
	rows, err := db.Query("SELECT value FROM settings WHERE key = 'news_points_v1'")
	if err != nil {
		return err
	}
	if len(rows.Vals) > 0 {
		return nil
	}
	since := now.Unix() - 86400
	if err := db.Exec(`UPDATE news_items SET body = '', body_at = 0, body_status = ''
		WHERE story_id IN (SELECT id FROM news_stories WHERE last_seen >= $1 AND written_by = 'model')`, since); err != nil {
		return err
	}
	if err := db.Exec("UPDATE news_stories SET summary = '', tries = 0 WHERE last_seen >= $1 AND written_by = 'model'", since); err != nil {
		return err
	}
	return db.Exec("INSERT INTO settings (key, value) VALUES ('news_points_v1', $1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", strconv.FormatInt(now.Unix(), 10))
}

// ingestFetched takes one batch: feed health, new entries, their stories.
func ingestFetched(db *poltergres.ReadWrite, raw []byte, now time.Time) (newsResult, error) {
	var b fetchedBatch
	if err := json.Unmarshal(raw, &b); err != nil {
		return newsResult{}, err
	}
	if b.FetchedAt == 0 {
		b.FetchedAt = now.Unix()
	}
	by := b.By
	if by == "" {
		by = "phone"
	}
	_ = db.Exec("INSERT INTO settings (key, value) VALUES ('news_last_by', $1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", by+"@"+strconv.FormatInt(b.FetchedAt, 10))
	var res newsResult
	var logged []feedstat.Entry
	defer func() { _ = feedstat.Log(db, time.Unix(b.FetchedAt, 0), logged) }()
	for _, f := range b.Feeds {
		if f.ID == "" {
			continue
		}
		res.Feeds++
		e := feedstat.Entry{Source: f.ID, Kind: feedstat.KindNews, By: by, Status: f.Status, TookMs: f.TookMs, Bytes: len(f.Body)}
		status := ""
		switch {
		case f.Error != "":
			status = "fetch failed: " + clip(f.Error, 120)
		case f.Status != 0 && (f.Status < 200 || f.Status > 299):
			status = "HTTP " + strconv.Itoa(f.Status)
		case f.Body == "":
			status = "empty answer"
		}
		if status != "" {
			if err := db.Exec("UPDATE news_feeds SET last_fetch = $2, last_status = $3, failures = failures + 1 WHERE id = $1", f.ID, b.FetchedAt, status); err != nil {
				return res, err
			}
			giveUp(db, f.ID)
			res.Failed = append(res.Failed, f.ID+": "+status)
			e.Error = status
			logged = append(logged, e)
			continue
		}
		parsed, err := feeds.Parse([]byte(f.Body))
		if err != nil {
			if err := db.Exec("UPDATE news_feeds SET last_fetch = $2, last_status = $3, failures = failures + 1 WHERE id = $1", f.ID, b.FetchedAt, "not a feed: "+clip(err.Error(), 100)); err != nil {
				return res, err
			}
			giveUp(db, f.ID)
			res.Failed = append(res.Failed, f.ID+": "+err.Error())
			e.Error = "not a feed: " + err.Error()
			logged = append(logged, e)
			continue
		}
		added := 0
		for _, it := range parsed.Items {
			pub := it.Published.Unix()
			if it.Published.IsZero() || pub > b.FetchedAt+3600 {
				pub = b.FetchedAt // no date, or one in the future: when we saw it
			}
			if now.Unix()-pub > int64(newsKeepDays)*86400 {
				continue // an old entry on a long feed
			}
			rows, err := db.Query(`INSERT INTO news_items (feed_id, guid, link, title, summary, published, fetched)
				VALUES ($1,$2,$3,$4,$5,$6,$7) ON CONFLICT (feed_id, guid) DO NOTHING RETURNING id`,
				f.ID, it.GUID, it.Link, it.Title, it.Summary, pub, b.FetchedAt)
			if err != nil {
				return res, err
			}
			if len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
				continue // seen before
			}
			id, _ := strconv.ParseInt(*rows.Vals[0][0], 10, 64)
			added++
			created, err := placeInStory(db, id, f.ID, it.Title, pub)
			if err != nil {
				return res, err
			}
			if created {
				res.NewStories++
			}
		}
		res.OK++
		res.NewItems += added
		e.OK, e.Items = true, added
		logged = append(logged, e)
		if err := db.Exec("UPDATE news_feeds SET last_fetch = $2, last_ok = $2, last_status = 'ok', last_items = $3, failures = 0 WHERE id = $1", f.ID, b.FetchedAt, len(parsed.Items)); err != nil {
			return res, err
		}
	}
	// the old entries go; the stories they made stay as long as any entry does
	_ = db.Exec("DELETE FROM news_items WHERE published < $1", now.Unix()-int64(newsKeepDays)*86400)
	_ = db.Exec("DELETE FROM news_stories WHERE last_seen < $1 AND NOT EXISTS (SELECT 1 FROM news_items i WHERE i.story_id = news_stories.id)", now.Unix()-int64(newsKeepDays)*86400)
	return res, nil
}

// giveUp switches off a feed that has never once given a feed after feeds.GiveUpAfter fetches in a
// row: a publication with no feed at that address (a page, a block, a feed that moved) is not
// asked again every two hours. `ghost-cli ghost.synthd news enable=<id>` asks again.
func giveUp(db *poltergres.ReadWrite, id string) {
	_ = db.Exec("UPDATE news_feeds SET enabled = false WHERE id = $1 AND enabled AND last_ok = 0 AND failures >= $2", id, feeds.GiveUpAfter)
}

// placeInStory finds the story an entry belongs to (a story of the last two days with a similar
// title) or starts one. The story's source count is how many feeds tell it.
func placeInStory(db *poltergres.ReadWrite, itemID int64, feedID, title string, pub int64) (created bool, err error) {
	toks := feeds.Tokens(title)
	rows, err := db.Query("SELECT id, tokens FROM news_stories WHERE last_seen >= $1 ORDER BY last_seen DESC LIMIT 600", pub-int64(newsStoryWindow.Seconds()))
	if err != nil {
		return false, err
	}
	var storyID int64
	for _, v := range rows.Vals {
		if len(v) < 2 || v[0] == nil || v[1] == nil {
			continue
		}
		if feeds.Similar(toks, strings.Fields(*v[1])) {
			storyID, _ = strconv.ParseInt(*v[0], 10, 64)
			break
		}
	}
	if storyID == 0 {
		r, err := db.Query("INSERT INTO news_stories (first_seen, last_seen, title, tokens) VALUES ($1,$1,$2,$3) RETURNING id", pub, title, strings.Join(toks, " "))
		if err != nil || len(r.Vals) == 0 || r.Vals[0][0] == nil {
			if err == nil {
				err = errors.New("no id for the new story")
			}
			return false, err
		}
		storyID, _ = strconv.ParseInt(*r.Vals[0][0], 10, 64)
		created = true
	}
	if err := db.Exec("UPDATE news_items SET story_id = $2 WHERE id = $1", itemID, storyID); err != nil {
		return created, err
	}
	// a story told by more outlets, or told again, is a story the summary and the digest want
	return created, db.Exec(`UPDATE news_stories SET
		last_seen = GREATEST(last_seen, $2),
		sources = (SELECT count(DISTINCT feed_id) FROM news_items WHERE story_id = $1),
		summary = CASE WHEN (SELECT count(DISTINCT feed_id) FROM news_items WHERE story_id = $1) > sources THEN '' ELSE summary END
		WHERE id = $1`, storyID, pub)
}

// storyFacts is what the model may use: each outlet's own title and description.
func storyFacts(db *poltergres.ReadWrite, storyID int64) ([]string, error) {
	rows, err := db.Query(`SELECT f.name, i.title, i.summary FROM news_items i JOIN news_feeds f ON f.id = i.feed_id
		WHERE i.story_id = $1 ORDER BY i.published LIMIT 8`, storyID)
	if err != nil {
		return nil, err
	}
	var facts []string
	for _, v := range rows.Vals {
		if len(v) < 3 || v[0] == nil || v[1] == nil {
			continue
		}
		line := *v[0] + ": " + *v[1]
		if v[2] != nil && *v[2] != "" {
			line += " , " + *v[2]
		}
		facts = append(facts, line)
	}
	return facts, nil
}

// newsPrompt asks for a short account from the reports (and the article, when one was read) and
// nothing else. With an article the account is a lead and a few points: what happened in one
// sentence, then the key facts as bullets, the way the phone shows a story.
func newsPrompt(facts []string, article string) string {
	var b strings.Builder
	if article == "" {
		b.WriteString("Below are the headline and the feed description of one news story as several outlets reported it. Write what happened in one or two plain sentences, from these reports only.\n\nREPORTS:\n")
	} else {
		b.WriteString("Below are the headline and the feed description of one news story as several outlets reported it, and the text of one of the articles. From these only, write what happened as a lead and a few points: first line, what happened in one plain sentence; then two to four lines each starting with \"- \", one key fact each (who, how much, when, what comes next), the most important first.\n\nREPORTS:\n")
	}
	for _, f := range facts {
		b.WriteString("- " + f + "\n")
	}
	if article != "" {
		b.WriteString("\nARTICLE:\n" + article + "\n")
	}
	if article == "" {
		b.WriteString("\nKeep every number, name and place exactly as the reports give them; add nothing the reports do not say; no opinion, no headline, no list, no mention of the outlets or of \"the reports\". Reply with the summary only.")
	} else {
		b.WriteString("\nKeep every number, name and place exactly as the reports give them; add nothing the reports do not say; no opinion, no headline, no mention of the outlets or of \"the reports\". Reply with the lead and the points only.")
	}
	return b.String()
}

const (
	newsPointsMax = 5   // points under a story's lead
	newsPointMin  = 8   // characters, under this a point says nothing
	newsPointMax  = 320 // characters, over this a point is a paragraph
)

// bulletLine says whether a line is a point ("- ", "• ", "* ", "1. ", "2) ") and gives its text.
func bulletLine(line string) (string, bool) {
	t := strings.TrimSpace(line)
	for _, p := range []string{"- ", "• ", "* ", "– ", "—"} {
		if strings.HasPrefix(t, p) {
			return strings.TrimSpace(t[len(p):]), true
		}
	}
	if t == "-" || t == "•" || t == "*" {
		return "", true
	}
	if i := strings.IndexAny(t, ".)"); i > 0 && i <= 2 && len(t) > i+1 && t[i+1] == ' ' {
		if _, err := strconv.Atoi(t[:i]); err == nil {
			return strings.TrimSpace(t[i+2:]), true
		}
	}
	return t, false
}

// splitStory reads a summary as its lead and its points: the lines before the first point are the
// lead (joined), each point is its own line, a plain line after a point carries on that point. A
// summary written before the points were asked for is all lead.
func splitStory(s string) (lead string, points []string) {
	var head []string
	for _, line := range strings.Split(s, "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		t, isPoint := bulletLine(line)
		switch {
		case isPoint:
			if t != "" {
				points = append(points, t)
			}
		case len(points) > 0:
			points[len(points)-1] += " " + t
		default:
			head = append(head, t)
		}
	}
	lead = plainDashes(strings.Join(strings.Fields(strings.Join(head, " ")), " "))
	for i, p := range points {
		points[i] = plainDashes(strings.Join(strings.Fields(p), " "))
	}
	return lead, points
}

// newsLead is a summary's first sentence or lead, for the digest and the brief.
func newsLead(summary string) string {
	lead, _ := splitStory(summary)
	return lead
}

// groundedNews holds a summary to the reports: a sane length, no refusal, a lead before any
// point, a few points of a sane length, and every number in it present in a report (the same
// test the day memories pass). What it keeps is written "lead\n- point\n- point".
func groundedNews(out string, facts []string) (string, bool) {
	s := strings.TrimSpace(strings.Trim(strings.TrimSpace(out), "\"“”"))
	if len(s) < 30 || len(s) > 1600 {
		return "", false
	}
	low := strings.ToLower(s)
	for _, b := range badProse {
		if strings.Contains(low, b) {
			return "", false
		}
	}
	if strings.Contains(low, "the reports") {
		return "", false
	}
	if _, first := bulletLine(strings.SplitN(s, "\n", 2)[0]); first {
		return "", false // a list with no lead
	}
	lead, points := splitStory(s)
	if len(lead) < 30 || len(points) > newsPointsMax {
		return "", false
	}
	for _, p := range points {
		if len(p) < newsPointMin || len(p) > newsPointMax {
			return "", false
		}
	}
	allowed := map[string]bool{}
	for _, f := range facts {
		for _, n := range numberRe.FindAllString(f, -1) {
			allowed[n] = true
			allowed[strings.ReplaceAll(n, ",", "")] = true
			if i := strings.IndexAny(n, ".,"); i > 0 {
				allowed[n[:i]] = true
			}
		}
	}
	text := lead
	for _, p := range points {
		text += "\n- " + p
	}
	for _, n := range numberRe.FindAllString(text, -1) { // the kept text: a list's own numbering is not a fact
		if !allowed[n] && !allowed[strings.ReplaceAll(n, ",", "")] {
			return "", false
		}
	}
	return text, true
}

// newsSummaryPass writes summaries for the stories that want one: told by two outlets or more, or
// alone for two hours (a single outlet's story still gets its sentence before the digest). GPU
// only, a few per pass, three tries per story.
func newsSummaryPass(db *poltergres.ReadWrite, oc *oracle.Client, now time.Time, lg *slog.Logger) (int, error) {
	if onGPU, err := oc.OnGPU(); err != nil || !onGPU {
		return 0, nil
	}
	rows, err := db.Query(`SELECT id FROM news_stories WHERE summary = '' AND tries < $1 AND (sources >= 2 OR first_seen < $2)
		ORDER BY sources DESC, last_seen DESC LIMIT $3`, newsModelTries, now.Unix()-7200, newsModelPerPass)
	if err != nil {
		return 0, err
	}
	written := 0
	for _, v := range rows.Vals {
		if len(v) == 0 || v[0] == nil {
			continue
		}
		id, _ := strconv.ParseInt(*v[0], 10, 64)
		facts, err := storyFacts(db, id)
		if err != nil {
			return written, err
		}
		if len(facts) == 0 {
			continue
		}
		_ = db.Exec("UPDATE news_stories SET tries = tries + 1 WHERE id = $1", id)
		article := storyArticle(db, id)
		resp, err := oc.Infer(oracle.Request{
			Capability: "summarize", Class: oracle.ClassLocalSmall, Priority: oracle.PriorityBackground,
			Input: newsPrompt(facts, article), MaxTokens: 420, Temperature: 0.2, DeadlineMS: 120000,
		})
		if err != nil || resp.Err != "" {
			lg.Debug("news summary: no answer", "fn", "newsSummaryPass", "story", id, "err", err)
			continue
		}
		grounds := facts
		if article != "" {
			grounds = append(append([]string(nil), facts...), article) // a number the article gives is the article's
		}
		text, ok := groundedNews(resp.Output, grounds)
		if !ok {
			lg.Debug("news summary: not grounded, dropped", "fn", "newsSummaryPass", "story", id)
			continue
		}
		if err := db.Exec("UPDATE news_stories SET summary = $2, written_by = 'model', model_at = $3 WHERE id = $1", id, text, now.UnixMilli()); err != nil {
			return written, err
		}
		// the summary is kept, the article is not: read for this and let go
		_ = db.Exec("UPDATE news_items SET body = '' WHERE story_id = $1 AND body <> ''", id)
		written++
	}
	return written, nil
}

// digestDue says which digest the hour wants and has not had today, in the person's zone.
func digestDue(db *poltergres.ReadWrite, now time.Time) (kind, day string, due bool) {
	loc := hw.LocalZone(db)
	local := now.In(loc)
	for _, h := range newsDigestHours {
		if local.Hour() != h {
			continue
		}
		kind = fmt.Sprintf("%02d:00", h)
		day = local.Format("2006-01-02")
		rows, err := db.Query("SELECT value FROM settings WHERE key = $1", "news_digest_"+kind)
		if err == nil && len(rows.Vals) == 1 && rows.Vals[0][0] != nil && *rows.Vals[0][0] == day {
			return kind, day, false
		}
		return kind, day, true
	}
	return "", "", false
}

// digestBody is the digest's text: the stories since the last digest (twelve hours when there
// was none), the most-told first, a line each. Pure over the rows.
type digestStory struct {
	ID      int64
	Title   string
	Sources int
	Summary string
}

func digestBody(stories []digestStory) string {
	if len(stories) == 0 {
		return ""
	}
	var b strings.Builder
	for i, s := range stories {
		if i > 0 {
			b.WriteString("\n")
		}
		line := s.Title
		if s.Summary != "" {
			line = newsLead(s.Summary) // the lead; the points wait on the phone's NEWS
		}
		if s.Sources > 1 {
			line += fmt.Sprintf(" (%d outlets)", s.Sources)
		}
		b.WriteString(line)
	}
	return b.String()
}

// postDigest builds and posts the digest due now; produce is the notification store's Produce.
func postDigest(db *poltergres.ReadWrite, now time.Time, kind, day string, produce func(hw.Notification) error, lg *slog.Logger) (int, error) {
	since := now.Add(-12 * time.Hour).Unix()
	if rows, err := db.Query("SELECT max(at) FROM news_digests"); err == nil && len(rows.Vals) == 1 && rows.Vals[0][0] != nil {
		if last, _ := strconv.ParseInt(*rows.Vals[0][0], 10, 64); last > since {
			since = last
		}
	}
	rows, err := db.Query(`SELECT id, title, sources, summary FROM news_stories WHERE last_seen >= $1
		ORDER BY sources DESC, last_seen DESC LIMIT $2`, since, newsDigestTop)
	if err != nil {
		return 0, err
	}
	var stories []digestStory
	var ids []string
	for _, v := range rows.Vals {
		if len(v) < 4 || v[0] == nil || v[1] == nil {
			continue
		}
		id, _ := strconv.ParseInt(*v[0], 10, 64)
		n, _ := strconv.Atoi(str(v[2]))
		stories = append(stories, digestStory{id, *v[1], n, str(v[3])})
		ids = append(ids, *v[0])
	}
	_ = db.Exec("INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", "news_digest_"+kind, day)
	if len(stories) == 0 {
		lg.Info("news digest: nothing new, none sent", "fn", "postDigest", "kind", kind)
		return 0, nil
	}
	body := digestBody(stories)
	if err := produce(hw.Notification{
		Service: "ghost.synthd", Kind: "news", Link: "news",
		Title: fmt.Sprintf("the news at %s: %d %s", kind, len(stories), pluralOf(len(stories), "story", "stories")),
		Body:  body,
	}); err != nil {
		return 0, err
	}
	if err := db.Exec("INSERT INTO news_digests (at, kind, body, story_ids) VALUES ($1,$2,$3,$4)", now.Unix(), kind, body, strings.Join(ids, ",")); err != nil {
		return len(stories), err
	}
	_ = db.Exec("UPDATE news_stories SET digested = $1 WHERE id = ANY(string_to_array($2, ',')::bigint[])", now.Unix(), strings.Join(ids, ","))
	return len(stories), nil
}

func pluralOf(n int, one, many string) string {
	if n == 1 {
		return one
	}
	return many
}

// newsFetchByBox fetches the feeds that are due when the phone is not on Wi-Fi (the phone fetches
// otherwise and the box stays off the wire): one pass, through the one outbound client, into the
// same ingest the phone's batches take. Returns what it did, or why it did nothing.
func newsFetchByBox(ctx context.Context, db *poltergres.ReadWrite, client *egress.Client, now time.Time, forced bool, lg *slog.Logger) (string, error) {
	proxy, why := hw.PhoneNetFrom(db).Proxy(now)
	if proxy && !forced {
		return "left to the phone: " + why, nil
	}
	rows, err := db.Query("SELECT id, url, last_fetch FROM news_feeds WHERE enabled ORDER BY last_fetch, id")
	if err != nil {
		return "", err
	}
	var due []struct{ id, url string }
	for _, v := range rows.Vals {
		if len(v) < 3 || v[0] == nil || v[1] == nil {
			continue
		}
		last, _ := strconv.ParseInt(str(v[2]), 10, 64)
		if forced || egress.Due(last, feeds.FetchEvery, now) {
			due = append(due, struct{ id, url string }{*v[0], *v[1]})
		}
	}
	if len(due) == 0 {
		return "nothing due (" + why + ")", nil
	}
	var fetched []egress.Fetched
	for _, d := range due {
		f, err := client.Get(ctx, d.id, d.url)
		if err != nil {
			return "", err
		}
		fetched = append(fetched, f)
		time.Sleep(300 * time.Millisecond)
	}
	raw, _ := json.Marshal(map[string]any{"fetchedAt": now.Unix(), "by": "box", "feeds": fetched})
	res, err := ingestFetched(db, raw, now)
	if err != nil {
		return "", err
	}
	lg.Info("news fetched by the box", "fn", "newsFetchByBox", "why", why, "feeds", res.Feeds, "ok", res.OK, "newItems", res.NewItems, "newStories", res.NewStories, "failed", len(res.Failed))
	return fmt.Sprintf("fetched %d feeds (%d answered, %d new entries): %s", res.Feeds, res.OK, res.NewItems, why), nil
}

// newsLoop: drain the inbox every 30 s; summaries and the digest check every 5 min; the box's own
// fetch every 10 min when the phone is not on Wi-Fi.
func newsLoop(ctx context.Context, mount, runDir string, produce func(hw.Notification) error, lg *slog.Logger) {
	inbox := filepath.Join(mount, "synthd", "news", "inbox")
	if err := os.MkdirAll(inbox, 0o750); err != nil {
		lg.Error("news inbox", "fn", "newsLoop", "err", err)
		return
	}
	oc := oracle.NewClient(runDir, 2*time.Minute)
	var db *poltergres.ReadWrite
	seeded := false
	connect := func() bool {
		if db != nil {
			return true
		}
		cfg, err := hw.LoadServicesConfig(mount)
		if err != nil {
			return false
		}
		db = poltergres.NewReadWrite(hw.SocketForMount(mount), cfg.Postgres.Port, cfg.Postgres.RWUser, cfg.Postgres.RWPass, cfg.Postgres.Name)
		return true
	}
	// the phone's copy of the last two days in Redis (hw.HotNews), rewritten after every batch and
	// every slow pass, so /v1/news answers from memory
	putHot := func() {
		if db != nil {
			putHotNews(db, mount, lg)
		}
	}
	drain := func() {
		if !connect() {
			return
		}
		if !seeded {
			if err := seedFeeds(db); err != nil {
				lg.Warn("news feeds not seeded", "fn", "newsLoop", "err", err)
				db = nil
				return
			}
			seeded = true
		}
		entries, err := os.ReadDir(inbox)
		if err != nil {
			return
		}
		taken := 0
		defer func() {
			if taken > 0 {
				putHot()
			}
		}()
		for _, e := range entries {
			if e.IsDir() || strings.HasSuffix(e.Name(), ".part") {
				continue
			}
			path := filepath.Join(inbox, e.Name())
			raw, err := os.ReadFile(path)
			if err != nil {
				continue
			}
			res, err := ingestFetched(db, raw, time.Now())
			if err != nil {
				lg.Warn("news batch failed, will retry", "fn", "newsLoop", "file", e.Name(), "err", err)
				db = nil
				return
			}
			lg.Info("news batch taken", "fn", "newsLoop", "feeds", res.Feeds, "ok", res.OK, "newItems", res.NewItems, "newStories", res.NewStories, "failed", len(res.Failed))
			taken++
			if len(res.Failed) > 0 {
				lg.Debug("news feeds that did not come", "fn", "newsLoop", "which", res.Failed)
			}
			_ = os.Remove(path)
		}
	}
	client := egress.New()
	slow := func() {
		if !connect() {
			return
		}
		defer putHot()
		now := time.Now()
		// the articles of the stories about to be summarised, then the summaries, then the brief
		if _, err := articlePass(ctx, db, client, now, lg); err != nil {
			lg.Warn("articles not read", "fn", "newsLoop", "err", err)
		}
		if n, err := newsSummaryPass(db, oc, now, lg); err != nil {
			lg.Warn("news summaries failed", "fn", "newsLoop", "err", err)
			db = nil
			return
		} else if n > 0 {
			lg.Info("news summaries written", "fn", "newsLoop", "stories", n)
		}
		if wrote, _, err := briefPass(db, oc, now, lg, false); err != nil {
			lg.Warn("news brief failed", "fn", "newsLoop", "err", err)
		} else if wrote {
			lg.Info("news brief written", "fn", "newsLoop")
		}
		// what the top coins are, read up and written for their pages (coindesc.go)
		if _, err := coinDescPass(ctx, db, oc, client, now, lg); err != nil {
			lg.Warn("coin descriptions failed", "fn", "newsLoop", "err", err)
		}
		if kind, day, due := digestDue(db, now); due {
			n, err := postDigest(db, now, kind, day, produce, lg)
			if err != nil {
				lg.Warn("news digest failed", "fn", "newsLoop", "err", err)
				db = nil
				return
			}
			if n > 0 {
				lg.Info("news digest posted", "fn", "newsLoop", "kind", kind, "stories", n)
			}
		}
	}
	fetch := func(forced bool) {
		if !connect() {
			return
		}
		what, err := newsFetchByBox(ctx, db, client, time.Now(), forced, lg)
		if err != nil {
			if ctx.Err() == nil {
				lg.Warn("news fetch by the box failed", "fn", "newsLoop", "err", err)
				db = nil
			}
			return
		}
		lg.Debug("news fetch by the box", "fn", "newsLoop", "what", what)
		putHot()
	}
	drain()
	fast := time.NewTicker(30 * time.Second)
	defer fast.Stop()
	long := time.NewTicker(5 * time.Minute)
	defer long.Stop()
	own := time.NewTicker(10 * time.Minute)
	defer own.Stop()
	first := time.NewTimer(90 * time.Second)
	defer first.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-fast.C:
			drain()
		case <-long.C:
			slow()
		case <-first.C:
			fetch(false)
		case <-own.C:
			fetch(false)
		case <-newsForce:
			fetch(true)
		}
	}
}

var (
	hotNewsMu  sync.Mutex
	hotNewsRD  *apparedis.ReadWrite
	briefNowMu sync.Mutex // one "write the brief now" at a time
)

// putHotNews writes the last two days of /v1/news to Redis (hw.HotNews), from the news loop and
// from "write the brief now".
func putHotNews(db *poltergres.ReadWrite, mount string, lg *slog.Logger) {
	hotNewsMu.Lock()
	defer hotNewsMu.Unlock()
	if hotNewsRD == nil {
		r, err := hw.HotRedis(mount)
		if err != nil {
			return
		}
		hotNewsRD = r
	}
	now := time.Now()
	d, err := hw.NewsDocNow(db, now.Add(-hw.NewsHotDays*24*time.Hour).Unix(), 0, now.Unix())
	if err != nil {
		return
	}
	if err := hw.HotPut(hotNewsRD, hw.HotNews, d, hw.HotNewsTTL); err != nil {
		lg.Debug("news not put in redis", "fn", "putHotNews", "err", err)
	}
}

// newsForce asks the loop to fetch now, whatever the phone is on (`news fetch=true`).
var newsForce = make(chan struct{}, 1)

// newsFeed is one feed's health, for the `news` command.
type newsFeed struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	URL       string `json:"url"`
	Enabled   bool   `json:"enabled"`
	LastFetch int64  `json:"lastFetch"`
	LastOK    int64  `json:"lastOk"`
	Status    string `json:"status"`
	Items     int    `json:"items"`
	Failures  int    `json:"failures"`
}

// newsStat is the `news` command's answer: the feeds with their health, the counts.
type newsStat struct {
	LastBy        string     `json:"lastBy,omitempty"` // phone or box
	LastByAt      int64      `json:"lastByAt,omitempty"`
	Feeds         []newsFeed `json:"feeds"`
	FeedsOKLast3h int        `json:"feedsOkLast3h"`
	Items7d       int        `json:"items7d"`
	Stories24h    int        `json:"stories24h"`
	Summaries     int        `json:"summaries"`
	LastDigestAt  int64      `json:"lastDigestAt"`
	LastFetchAt   int64      `json:"lastFetchAt"`
}

func newsStatus(db *poltergres.ReadWrite, now time.Time) (newsStat, error) {
	var out newsStat
	rows, err := db.Query("SELECT id, name, url, enabled, last_fetch, last_ok, last_status, last_items, failures FROM news_feeds ORDER BY name")
	if err != nil {
		return out, err
	}
	for _, v := range rows.Vals {
		if len(v) < 9 {
			continue
		}
		lf, _ := strconv.ParseInt(str(v[4]), 10, 64)
		lo, _ := strconv.ParseInt(str(v[5]), 10, 64)
		items, _ := strconv.Atoi(str(v[7]))
		fails, _ := strconv.Atoi(str(v[8]))
		r := newsFeed{str(v[0]), str(v[1]), str(v[2]), str(v[3]) == "t" || str(v[3]) == "true", lf, lo, str(v[6]), items, fails}
		if r.Enabled && now.Unix()-lo < 3*3600 {
			out.FeedsOKLast3h++
		}
		out.Feeds = append(out.Feeds, r)
	}
	one := func(q string, args ...any) string {
		r, err := db.Query(q, args...)
		if err != nil || len(r.Vals) == 0 || r.Vals[0][0] == nil {
			return "0"
		}
		return *r.Vals[0][0]
	}
	out.Items7d, _ = strconv.Atoi(one("SELECT count(*) FROM news_items WHERE published >= $1", now.Unix()-7*86400))
	out.Stories24h, _ = strconv.Atoi(one("SELECT count(*) FROM news_stories WHERE last_seen >= $1", now.Unix()-86400))
	out.Summaries, _ = strconv.Atoi(one("SELECT count(*) FROM news_stories WHERE summary <> ''"))
	out.LastDigestAt, _ = strconv.ParseInt(one("SELECT coalesce(max(at),0) FROM news_digests"), 10, 64)
	out.LastFetchAt, _ = strconv.ParseInt(one("SELECT coalesce(max(last_fetch),0) FROM news_feeds"), 10, 64)
	if v := one("SELECT value FROM settings WHERE key = 'news_last_by'"); v != "0" {
		if i := strings.IndexByte(v, '@'); i > 0 {
			out.LastBy = v[:i]
			out.LastByAt, _ = strconv.ParseInt(v[i+1:], 10, 64)
		}
	}
	return out, nil
}

// newsSource is the chat's news: recent entries whose title or description match the question,
// after the archive and before the web (the phone's). Full-text search, the last seven days.
func newsSource(runDir, prompt string) []ctxItem {
	mount := filepath.Dir(runDir)
	db := chatStore(mount)
	if db == nil {
		return nil
	}
	// "what's the news": the day's brief and the most-told stories, which the plan told the phone
	// the box has
	if headlineHint.MatchString(strings.TrimSpace(prompt)) {
		return headlineItems(db, time.Now())
	}
	terms := memoryTerms(prompt)
	if len(terms) < 2 {
		return nil
	}
	return newsItemsFor(db, terms, time.Now())
}

// headlineItems is the day's news for a chat answer: the brief, then the five most-told stories of
// the last day by their leads (or titles before a summary).
func headlineItems(db *poltergres.ReadWrite, now time.Time) []ctxItem {
	var out []ctxItem
	day := now.UTC().Format("2006-01-02")
	if text, at, _ := hw.NewsBrief(db); text != "" && now.Unix()-at < 86400 {
		out = append(out, ctxItem{When: day, Source: "news", Snippet: "the day's brief: " + strings.ReplaceAll(strings.TrimPrefix(text, "- "), "\n- ", "; "),
			Why: "the box's brief of the day's most-told stories, from the feeds it gathered"})
	}
	rows, err := db.Query(`SELECT title, summary, sources FROM news_stories WHERE last_seen >= $1 ORDER BY sources DESC, last_seen DESC LIMIT 5`, now.Unix()-86400)
	if err != nil {
		return out
	}
	for _, v := range rows.Vals {
		if len(v) < 3 || v[0] == nil {
			continue
		}
		line := *v[0]
		if l := newsLead(str(v[1])); l != "" {
			line = l
		}
		out = append(out, ctxItem{When: day, Source: "news", Snippet: line + " (" + str(v[2]) + " outlets)",
			Why: "one of the day's most-told stories, from the feeds the box gathered"})
	}
	return out
}

// newsItemsFor is the query behind newsSource (the Postgres test calls it).
func newsItemsFor(db *poltergres.ReadWrite, terms []string, now time.Time) []ctxItem {
	rows, err := db.Query(`SELECT f.name, i.title, i.summary, i.published, ts_rank(to_tsvector('english', i.title || ' ' || i.summary), plainto_tsquery('english', $1)) AS r
		FROM news_items i JOIN news_feeds f ON f.id = i.feed_id
		WHERE i.published >= $2 AND to_tsvector('english', i.title || ' ' || i.summary) @@ plainto_tsquery('english', $1)
		ORDER BY r DESC, i.published DESC LIMIT 3`, strings.Join(terms, " "), now.Add(-7*24*time.Hour).Unix())
	if err != nil {
		return nil
	}
	var out []ctxItem
	for _, v := range rows.Vals {
		if len(v) < 4 || v[0] == nil || v[1] == nil {
			continue
		}
		pub, _ := strconv.ParseInt(str(v[3]), 10, 64)
		snippet := *v[0] + ": " + *v[1]
		if s := str(v[2]); s != "" {
			snippet += ". " + s
		}
		out = append(out, ctxItem{
			When: time.Unix(pub, 0).UTC().Format("2006-01-02"), Source: "news", Snippet: snippet,
			Why: "a recent story from the feeds your phone fetched, matching your question",
		})
	}
	return out
}
