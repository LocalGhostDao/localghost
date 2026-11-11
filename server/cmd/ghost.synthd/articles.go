package main

// THE ARTICLES BEHIND THE HEADLINES, and the day in a few sentences. A feed gives a headline and
// a line or two; a story the box summarises is better told from the article itself. So for each
// story about to be summarised the box reads up to two of its articles' pages, the way a browser
// would, and keeps the paragraphs the page serves anyone. A page that says it is behind a paywall
// (schema.org's isAccessibleForFree: false) keeps only its free part, marked paywalled, and the
// story is told from that and the feeds' words. Then, from the summaries of the day's most-told
// stories, the brief: the day's news as one point per story, in the stories' order, for the
// phone's home screen (settings news_brief); a tap on a point opens its story.

import (
	"context"
	"encoding/json"
	"html"
	"log/slog"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LocalGhostDao/localghost/server/internal/egress"
	"github.com/LocalGhostDao/localghost/server/internal/feedstat"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

const (
	articlesPerPass  = 12   // pages read per slow pass (every five minutes)
	articlesPerStory = 2    // of each story's reports
	articleKeep      = 8000 // characters of an article read (held until its story is summarised, a day at most)
	articleShort     = 600  // under this, the page gave too little to count as the article
)

var (
	reScript    = regexp.MustCompile(`(?is)<(script|style|noscript|svg|form|nav|header|footer|aside|figure)\b.*?</(script|style|noscript|svg|form|nav|header|footer|aside|figure)>`)
	reParagraph = regexp.MustCompile(`(?is)<p\b[^>]*>(.*?)</p>`)
	reTag       = regexp.MustCompile(`(?s)<[^>]+>`)
	reSpace     = regexp.MustCompile(`\s+`)
	rePaywall   = regexp.MustCompile(`(?i)"isAccessibleForFree"\s*:\s*"?(false|no)"?`)
	reLDJSON    = regexp.MustCompile(`(?is)<script[^>]+type=["']application/ld\+json["'][^>]*>(.*?)</script>`)
	boilerplate = []string{"cookie", "subscribe", "sign up", "newsletter", "advertisement", "all rights reserved", "follow us", "share this",
		"read more", "click here", "javascript", "your browser", "log in", "sign in", "terms of use", "privacy policy"}
)

// articleText reads an article's own paragraphs out of its page: within <article> when there is
// one, else <main>, else the whole page; scripts and page furniture out; every <p>, tags
// stripped, entities decoded; boilerplate and lines too short to be prose dropped. paywalled is
// the page's own word that the article is not free.
func articleText(page string) (text string, paywalled bool) {
	paywalled = rePaywall.MatchString(page)
	// A-Z only, byte for byte: strings.ToLower writes U+FFFD for every byte that is not UTF-8 and
	// changes the length of a few letters, and the indices below are used on the page itself. A
	// page in another encoding made the copy longer and the slice panicked, every pass, with the
	// row never marked (audit run 2).
	low := lowerASCII(page)
	span := page
	for _, tag := range []string{"article", "main"} {
		if i := strings.Index(low, "<"+tag); i >= 0 {
			if j := strings.LastIndex(low, "</"+tag+">"); j > i {
				span = page[i:j]
				break
			}
		}
	}
	span = reScript.ReplaceAllString(span, " ")
	var paras []string
	seen := map[string]bool{}
	size := 0
	for _, m := range reParagraph.FindAllStringSubmatch(span, -1) {
		t := html.UnescapeString(reTag.ReplaceAllString(m[1], " "))
		t = strings.TrimSpace(reSpace.ReplaceAllString(t, " "))
		if len(t) < 40 || seen[t] {
			continue
		}
		lt := strings.ToLower(t)
		skip := false
		for _, b := range boilerplate {
			if strings.Contains(lt, b) && len(t) < 200 {
				skip = true
				break
			}
		}
		if skip {
			continue
		}
		seen[t] = true
		paras = append(paras, t)
		size += len(t) + 1
		if size >= articleKeep {
			break
		}
	}
	text = strings.Join(paras, "\n")
	// a page that builds its paragraphs in the browser often carries the article in its
	// schema.org block (articleBody), served to anyone; used only when the page says it is free
	if len(text) < articleShort && !paywalled {
		if body := ldArticleBody(page); len(body) > len(text) {
			text = body
		}
	}
	// what is kept is text the database will take: valid UTF-8, no NUL, cut on a character
	text = strings.ReplaceAll(strings.ToValidUTF8(text, ""), "\x00", "")
	if len(text) > articleKeep {
		cut := articleKeep
		for cut > 0 && !utf8.RuneStart(text[cut]) {
			cut--
		}
		text = text[:cut]
	}
	return text, paywalled
}

// lowerASCII lowers A-Z and nothing else, so an index into the result is an index into s.
func lowerASCII(s string) string {
	for i := 0; i < len(s); i++ {
		if c := s[i]; c >= 'A' && c <= 'Z' {
			b := []byte(s)
			for ; i < len(b); i++ {
				if b[i] >= 'A' && b[i] <= 'Z' {
					b[i] += 'a' - 'A'
				}
			}
			return string(b)
		}
	}
	return s
}

// readArticle is articleText that cannot take the daemon down: a page is a stranger's bytes, and
// a fault in reading one costs that page ("unreadable"), not the pass.
func readArticle(page string) (text string, paywalled, ok bool) {
	defer func() {
		if recover() != nil {
			text, paywalled, ok = "", false, false
		}
	}()
	text, paywalled = articleText(page)
	return text, paywalled, true
}

// ldArticleBody is the longest articleBody in a page's schema.org blocks, "" when none.
func ldArticleBody(page string) string {
	best := ""
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			if b, ok := x["articleBody"].(string); ok && len(b) > len(best) {
				best = b
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	for _, m := range reLDJSON.FindAllStringSubmatch(page, 8) {
		var v any
		if json.Unmarshal([]byte(strings.TrimSpace(m[1])), &v) == nil {
			walk(v)
		}
	}
	best = html.UnescapeString(reTag.ReplaceAllString(best, " "))
	var paras []string
	for _, p := range strings.Split(best, "\n") {
		if p = strings.TrimSpace(reSpace.ReplaceAllString(p, " ")); p != "" {
			paras = append(paras, p)
		}
	}
	return strings.Join(paras, "\n")
}

// articlePass reads the pages of the stories about to be summarised. Returns how many it read.
func articlePass(ctx context.Context, db *poltergres.ReadWrite, client *egress.Client, now time.Time, lg *slog.Logger) (int, error) {
	// an outlet that refused every page it was asked for in the last day is left alone until the
	// day has passed: its stories are told from the feeds' words (refusedAfter)
	rows, err := db.Query(`SELECT i.id, i.link, i.story_id, i.feed_id FROM news_items i JOIN news_stories s ON s.id = i.story_id
		WHERE i.body_at = 0 AND i.link LIKE 'http%' AND s.summary = '' AND s.tries < $1 AND (s.sources >= 2 OR s.first_seen < $2)
		AND i.feed_id NOT IN (SELECT feed_id FROM news_items WHERE body_at >= $4 GROUP BY feed_id
			HAVING count(*) >= $5 AND count(*) FILTER (WHERE body_status IN ('ok','paywalled','short')) = 0)
		ORDER BY s.sources DESC, s.last_seen DESC, i.published DESC LIMIT $3`, newsModelTries, now.Unix()-7200, articlesPerPass*3, now.Unix()-86400, refusedAfter)
	if err != nil {
		return 0, err
	}
	perStory := map[string]int{}
	read := 0
	var logged []feedstat.Entry
	for _, v := range rows.Vals {
		if len(v) < 4 || v[0] == nil || v[1] == nil || read >= articlesPerPass {
			continue
		}
		story := str(v[2])
		if perStory[story] >= articlesPerStory {
			continue
		}
		id, link := *v[0], *v[1]
		if client.Allows(link) != nil {
			// a link the box will never follow (plain http, a bare or private address, a local
			// name): set aside once, without a request and without a place in the pass
			if err := db.Exec("UPDATE news_items SET body_at = $2, body_status = 'not followed' WHERE id = $1", id, now.Unix()); err != nil {
				return read, err
			}
			continue
		}
		perStory[story]++
		f, ferr := client.GetPage(ctx, "article:"+str(v[3]), link)
		if ferr != nil {
			return read, nil // the context ended
		}
		read++
		status, body := "", ""
		switch {
		case f.Error != "":
			status = "fetch failed: " + clip(f.Error, 100)
		case f.Status < 200 || f.Status > 299:
			status = "HTTP " + strconv.Itoa(f.Status)
		default:
			text, paywalled, readable := readArticle(f.Body)
			body = text
			switch {
			case !readable:
				status = "unreadable"
			case paywalled:
				status = "paywalled"
			case len(text) < articleShort:
				status = "short"
			default:
				status = "ok"
			}
		}
		if err := db.Exec("UPDATE news_items SET body = $2, body_at = $3, body_status = $4 WHERE id = $1", id, body, now.Unix(), status); err != nil {
			return read, err
		}
		logged = append(logged, feedstat.Entry{Source: "article:" + str(v[3]), Kind: feedstat.KindArticle, By: "box", Status: f.Status,
			OK: status == "ok" || status == "paywalled" || status == "short", TookMs: f.TookMs, Bytes: len(f.Body), Items: len(body), Error: errIf(status)})
		time.Sleep(400 * time.Millisecond)
	}
	_ = feedstat.Log(db, now, logged)
	// an article is read for its story's summary and let go once that is written; whatever was not
	// summarised within a day goes too
	_ = db.Exec("UPDATE news_items SET body = '' WHERE body <> '' AND body_at < $1", now.Add(-24*time.Hour).Unix())
	if read > 0 {
		lg.Debug("articles read", "fn", "articlePass", "pages", read)
	}
	return read, nil
}

// refusedAfter pages refused in a day, none read, and an outlet's articles are left for the day
// (the monitor names the outlet on Box Status).
const refusedAfter = 6

func errIf(status string) string {
	switch status {
	case "ok", "paywalled", "short":
		return ""
	}
	return status
}

// storyArticle is the best article text a story has: the longest free one, cut to fit a prompt.
func storyArticle(db *poltergres.ReadWrite, storyID int64) string {
	rows, err := db.Query(`SELECT body FROM news_items WHERE story_id = $1 AND body <> '' ORDER BY (body_status = 'ok') DESC, length(body) DESC LIMIT 1`, storyID)
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return ""
	}
	b := *rows.Vals[0][0]
	if len(b) > 3000 {
		b = b[:3000]
		if i := strings.LastIndexAny(b, ".!?"); i > 2000 {
			b = b[:i+1]
		}
	}
	return b
}

// --- the brief ------------------------------------------------------------------------------

const (
	briefStories = 6
	briefEvery   = 2 * time.Hour // a brief stands this long when its stories have not changed
)

// Brief is the day's news as home shows it: "- point" lines, the nth telling Stories[n] when the
// counts agree.
type Brief struct {
	At      int64   `json:"at"`
	Text    string  `json:"text"`
	Stories []int64 `json:"stories"`
}

func loadBrief(db *poltergres.ReadWrite) (Brief, bool) {
	var b Brief
	rows, err := db.Query("SELECT value FROM settings WHERE key = 'news_brief'")
	if err != nil || len(rows.Vals) != 1 || rows.Vals[0][0] == nil {
		return b, false
	}
	return b, json.Unmarshal([]byte(*rows.Vals[0][0]), &b) == nil
}

// briefPrompt asks for one point per story from the stories' own leads and nothing else.
func briefPrompt(leads []string) string {
	var b strings.Builder
	b.WriteString("Below are the day's main news stories, each already told in a sentence, the most widely reported first. Write the day's news as a brief: one line per story, in the order given, each starting with \"- \" and saying in one short plain sentence what happened.\n\nSTORIES:\n")
	for i, s := range leads {
		b.WriteString(strconv.Itoa(i+1) + ". " + s + "\n")
	}
	b.WriteString("\nKeep every number, name and place exactly as the stories give them; add nothing they do not say; no opinion, no headline, no greeting, no numbering. Reply with the lines only.")
	return b.String()
}

const (
	briefPointMin = 20
	briefPointMax = 280
)

// groundedBrief holds the brief to the stories, the way a story's summary is held to its reports:
// one point per story at most, each a sane length, every number from a story. What it keeps is
// written "- point\n- point".
func groundedBrief(out string, summaries []string) (string, bool) {
	s := strings.TrimSpace(strings.Trim(strings.TrimSpace(out), "\"“”"))
	if len(s) < briefPointMin || len(s) > 1800 {
		return "", false
	}
	low := strings.ToLower(s)
	for _, b := range badProse {
		if strings.Contains(low, b) {
			return "", false
		}
	}
	if strings.Contains(low, "the stories") {
		return "", false
	}
	lead, points := splitStory(s)
	if lead != "" || len(points) < 2 || len(points) > len(summaries) {
		return "", false // a preamble, or not one point per story
	}
	for _, p := range points {
		if len(p) < briefPointMin || len(p) > briefPointMax {
			return "", false
		}
	}
	allowed := map[string]bool{}
	for _, f := range summaries {
		for _, n := range numberRe.FindAllString(f, -1) {
			allowed[n] = true
			allowed[strings.ReplaceAll(n, ",", "")] = true
		}
	}
	text := "- " + strings.Join(points, "\n- ")
	for _, n := range numberRe.FindAllString(text, -1) {
		if !allowed[n] && !allowed[strings.ReplaceAll(n, ",", "")] {
			return "", false
		}
	}
	return text, true
}

// briefPass writes the brief when the day's most-told stories changed, or when it is two hours
// old; force (the phone's "write it now") writes it whatever its age. why says what it did, or
// why it did not, in the phone's words.
func briefPass(db *poltergres.ReadWrite, oc *oracle.Client, now time.Time, lg *slog.Logger, force bool) (wrote bool, why string, err error) {
	rows, err := db.Query(`SELECT id, summary FROM news_stories WHERE last_seen >= $1 AND summary <> ''
		ORDER BY sources DESC, last_seen DESC LIMIT $2`, now.Unix()-86400, briefStories)
	if err != nil {
		return false, "", err
	}
	var ids []int64
	var sums, leads []string
	for _, v := range rows.Vals {
		if len(v) < 2 || v[0] == nil || v[1] == nil {
			continue
		}
		id, _ := strconv.ParseInt(*v[0], 10, 64)
		ids = append(ids, id)
		sums = append(sums, *v[1])
		leads = append(leads, newsLead(*v[1]))
	}
	if len(sums) < 2 {
		return false, "fewer than two of the day's stories have a summary yet", nil
	}
	// a brief from before the points (prose) is written again at once
	if old, ok := loadBrief(db); !force && ok && strings.HasPrefix(old.Text, "- ") && sameIDs(old.Stories, ids) && now.Sub(time.Unix(old.At, 0)) < briefEvery {
		return false, "the brief is up to date", nil
	}
	if onGPU, err := oc.OnGPU(); err != nil || !onGPU {
		return false, "the model is on the CPU, the brief waits for the GPU", nil
	}
	resp, err := oc.Infer(oracle.Request{
		Capability: "summarize", Class: oracle.ClassLocalSmall, Priority: oracle.PriorityBackground,
		Input: briefPrompt(leads), MaxTokens: 360, Temperature: 0.2, DeadlineMS: 90000,
	})
	if err != nil || resp.Err != "" {
		return false, "the model did not answer", nil
	}
	text, ok := groundedBrief(resp.Output, sums)
	if !ok {
		lg.Debug("news brief not grounded, kept the last", "fn", "briefPass")
		return false, "the model's brief did not hold to the stories, the last one stands", nil
	}
	b, _ := json.Marshal(Brief{At: now.Unix(), Text: text, Stories: ids})
	if err := db.Exec("INSERT INTO settings (key, value) VALUES ('news_brief', $1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", string(b)); err != nil {
		return false, "", err
	}
	return true, "written", nil
}

func sameIDs(a, b []int64) bool {
	if len(a) != len(b) {
		return false
	}
	x := append([]int64(nil), a...)
	y := append([]int64(nil), b...)
	sort.Slice(x, func(i, j int) bool { return x[i] < x[j] })
	sort.Slice(y, func(i, j int) bool { return y[i] < y[j] })
	for i := range x {
		if x[i] != y[i] {
			return false
		}
	}
	return true
}
