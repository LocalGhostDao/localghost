package main

// WHAT A COIN IS, read up and written by the box. Coinbase's text for a coin is a line of
// marketing at best; a coin's page deserves a few plain sentences on what it is, what it is for,
// who made it and when. So for each of the top hundred coins the box reads what is served to
// anyone: Wikipedia's summary of the coin's article (found through Wikipedia's own search, and
// kept only when it is about a cryptocurrency of that name), the coin's own website (its
// description and its paragraphs) and Coinbase's text, and the model writes three or four
// sentences from those and nothing else: no price, no forecast, every number one the sources
// give. Two coins every slow pass, on the GPU only; a coin is written again after ninety days, a
// try that found too little is tried again after three. Kept in coin_info (written, written_at,
// written_from); the phone's coin page shows it, Coinbase's text until it exists.

import (
	"context"
	"encoding/json"
	"html"
	"log/slog"
	"net"
	"net/url"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LocalGhostDao/localghost/server/internal/egress"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

const (
	coinDescPerPass = 2
	coinDescTop     = 100
	coinDescEvery   = 90 * 24 * time.Hour // written again after
	coinDescRetry   = 3 * 24 * time.Hour  // a try that found too little, again after
	coinDescMinRead = 200                 // characters read, all sources together, to write from
	coinSourceKeep  = 2500                // characters of one source given to the model
)

// coinSource is one thing read about a coin: where from, and its text.
type coinSource struct {
	From string
	Text string
}

var reCryptoWord = regexp.MustCompile(`(?i)\b(crypto ?currenc(?:y|ies)|blockchains?|tokens?|stablecoins?|decentrali[sz]ed|cryptocurrency exchange|smart contracts?|proof[- ]of[- ](?:stake|work)|layer[- ]?(?:1|2|one|two)|coin)\b`)

// wikiSearchURL is Wikipedia's search for the coin's article.
func wikiSearchURL(name string) string {
	return "https://en.wikipedia.org/w/api.php?action=query&list=search&srlimit=4&format=json&srsearch=" +
		url.QueryEscape(name+" cryptocurrency")
}

// wikiSummaryURL is the summary of one article.
func wikiSummaryURL(title string) string {
	return "https://en.wikipedia.org/api/rest_v1/page/summary/" + url.PathEscape(strings.ReplaceAll(title, " ", "_"))
}

// namesCoin: the text names the coin, by its whole name or its symbol as a word.
func namesCoin(text, name, sym string) bool {
	if name != "" && strings.Contains(strings.ToLower(text), strings.ToLower(name)) {
		return true
	}
	if len(sym) >= 3 {
		return regexp.MustCompile(`\b` + regexp.QuoteMeta(sym) + `\b`).MatchString(text)
	}
	return false
}

// pickWikiTitle is the search's first article that is about a cryptocurrency of this name, ""
// when none is (pure, for the tests).
func pickWikiTitle(body, name, sym string) string {
	var r struct {
		Query struct {
			Search []struct {
				Title   string `json:"title"`
				Snippet string `json:"snippet"`
			} `json:"search"`
		} `json:"query"`
	}
	if json.Unmarshal([]byte(body), &r) != nil {
		return ""
	}
	for _, h := range r.Query.Search {
		snip := html.UnescapeString(reTag.ReplaceAllString(h.Snippet, ""))
		if strings.Contains(strings.ToLower(h.Title), "list of") {
			continue
		}
		if namesCoin(h.Title, name, sym) && reCryptoWord.MatchString(h.Title+" "+snip) {
			return h.Title
		}
	}
	return ""
}

// parseWikiSummary is the article's summary when it is about the coin, "" otherwise (pure, for the tests).
func parseWikiSummary(body, name, sym string) string {
	var r struct {
		Type    string `json:"type"`
		Extract string `json:"extract"`
	}
	if json.Unmarshal([]byte(body), &r) != nil || r.Type == "disambiguation" {
		return ""
	}
	ex := strings.TrimSpace(r.Extract)
	if len(ex) < 80 || !namesCoin(ex, name, sym) || !reCryptoWord.MatchString(ex) {
		return ""
	}
	return clipText(ex, coinSourceKeep)
}

var (
	reMeta    = regexp.MustCompile(`(?is)<meta\b[^>]*>`)
	reMetaKey = regexp.MustCompile(`(?is)\b(?:name|property)\s*=\s*["'](description|og:description|twitter:description)["']`)
	reContent = regexp.MustCompile(`(?is)\bcontent\s*=\s*(?:"([^"]*)"|'([^']*)')`)
)

// siteText is what a coin's own page says about it: its description and its paragraphs (pure,
// for the tests).
func siteText(page string) string {
	desc := ""
	for _, m := range reMeta.FindAllString(page, 60) {
		if !reMetaKey.MatchString(m) {
			continue
		}
		if c := reContent.FindStringSubmatch(m); c != nil {
			v := strings.TrimSpace(html.UnescapeString(c[1] + c[2]))
			if len(v) > len(desc) {
				desc = v
			}
		}
	}
	body, _, _ := readArticle(page)
	text := strings.TrimSpace(strings.ToValidUTF8(desc, "") + "\n" + body)
	return clipText(text, coinSourceKeep)
}

// publicSite: an http(s) address with a name, not a bare address nor this machine's (the
// website comes from Coinbase's list; the box asks only the internet for it).
func publicSite(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (u.Scheme != "https" && u.Scheme != "http") || u.Host == "" || u.User != nil {
		return "", false
	}
	h := strings.ToLower(u.Hostname())
	if net.ParseIP(h) != nil || h == "localhost" || !strings.Contains(h, ".") || strings.HasSuffix(h, ".local") || strings.HasSuffix(h, ".internal") {
		return "", false
	}
	return strings.TrimPrefix(h, "www."), true
}

func clipText(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n-- // on a character, never inside one
	}
	s = s[:n]
	if i := strings.LastIndexAny(s, ".\n"); i > n/2 {
		s = s[:i+1]
	}
	return s
}

// coinDescPrompt asks for the coin's few sentences from the sources (pure, for the tests).
func coinDescPrompt(name, sym string, src []coinSource) string {
	var b strings.Builder
	b.WriteString("Write what " + name + " (" + sym + ") is, for its page in my app, from the sources below and nothing else. " +
		"Three or four plain sentences: what it is, what it is for, who made or runs it and when it started, and how it works in a sentence, as far as the sources say. " +
		"When the sources say little, one or two sentences. No price, no market value, no forecast, no opinion, no advice, no marketing words. " +
		"Keep every number and date as the sources give them. Do not mention the sources. Reply with the text only.\n\nSOURCES:\n")
	for _, s := range src {
		b.WriteString("[" + s.From + "]\n" + s.Text + "\n\n")
	}
	return b.String()
}

var badCoinText = []string{"you should", "invest in", "financial advice", "price prediction", "to the moon", "the sources", "source:", "[wikipedia", "[coinbase"}

// groundedCoinText checks the model's text against what was read: groundedProse's length, form
// and numbers, the coin named, and nothing that reads as advice (pure, for the tests).
func groundedCoinText(out, name, sym string, src []coinSource) (string, bool) {
	facts := make([]string, 0, len(src))
	for _, s := range src {
		facts = append(facts, s.Text)
	}
	text, ok := groundedProse(out, facts)
	if !ok || !namesCoin(text, name, sym) {
		return "", false
	}
	low := strings.ToLower(text)
	for _, b := range badCoinText {
		if strings.Contains(low, b) {
			return "", false
		}
	}
	return text, true
}

// coinDescPass writes the next coins' descriptions. Returns how many it wrote.
func coinDescPass(ctx context.Context, db *poltergres.ReadWrite, oc *oracle.Client, client *egress.Client, now time.Time, lg *slog.Logger) (int, error) {
	if onGPU, err := oc.OnGPU(); err != nil || !onGPU {
		return 0, nil
	}
	coins, err := coinsToWrite(db, now)
	if err != nil {
		return 0, err
	}
	wrote := 0
	for _, c := range coins {
		sym, name, cb, site := c[0], c[1], c[2], c[3]
		src := readAboutCoin(ctx, client, name, sym, cb, site)
		if ctx.Err() != nil {
			return wrote, nil
		}
		total := 0
		var from []string
		for _, s := range src {
			total += len(s.Text)
			from = append(from, s.From)
		}
		text := ""
		if total >= coinDescMinRead {
			resp, ierr := oc.Infer(oracle.Request{
				Capability: "summarize", Class: oracle.ClassLocalSmall, Priority: oracle.PriorityBackground,
				Input: coinDescPrompt(name, sym, src), MaxTokens: 400, Temperature: 0.2, DeadlineMS: 120000,
			})
			if ierr != nil || resp.Err != "" {
				return wrote, nil // the model is busy or away: the same coins next pass
			}
			text, _ = groundedCoinText(resp.Output, name, sym, src)
		}
		if text == "" {
			// too little read, or the model's text strayed: marked tried, the old text kept
			lg.Info("coin not written: too little read or the text strayed", "fn", "coinDescPass", "symbol", sym, "read", total, "from", strings.Join(from, ", "))
			if err := markCoinTried(db, sym, name, now); err != nil {
				return wrote, err
			}
			continue
		}
		if err := saveCoinText(db, sym, name, text, strings.Join(from, ", "), now); err != nil {
			return wrote, err
		}
		wrote++
		lg.Info("coin written", "fn", "coinDescPass", "symbol", sym, "from", strings.Join(from, ", "))
	}
	return wrote, nil
}

// coinsToWrite is the next coins to write: of the top hundred by the newest list, the ones never
// written, tried more than three days ago or written more than ninety days ago, by rank:
// symbol, name, Coinbase's text, website.
func coinsToWrite(db *poltergres.ReadWrite, now time.Time) ([][4]string, error) {
	rows, err := db.Query(`SELECT r.symbol, COALESCE(NULLIF(i.name, ''), r.name), COALESCE(i.description, ''), COALESCE(i.website, '')
		FROM (SELECT symbol, name, rank FROM coin_ranks WHERE ts = (SELECT max(ts) FROM coin_ranks) ORDER BY rank LIMIT $1) r
		LEFT JOIN coin_info i ON i.symbol = r.symbol
		WHERE (COALESCE(i.written, '') = '' AND COALESCE(i.written_at, 0) < $2) OR (COALESCE(i.written, '') <> '' AND i.written_at < $3)
		ORDER BY r.rank LIMIT $4`, coinDescTop, now.Add(-coinDescRetry).Unix(), now.Add(-coinDescEvery).Unix(), coinDescPerPass)
	if err != nil {
		return nil, err
	}
	var out [][4]string
	for _, v := range rows.Vals {
		if len(v) < 4 || v[0] == nil {
			continue
		}
		c := [4]string{str(v[0]), str(v[1]), str(v[2]), str(v[3])}
		if c[1] == "" {
			c[1] = c[0]
		}
		out = append(out, c)
	}
	return out, nil
}

// markCoinTried and saveCoinText keep a try and a text.
func markCoinTried(db *poltergres.ReadWrite, sym, name string, now time.Time) error {
	return db.Exec(`INSERT INTO coin_info (symbol, name, written_at) VALUES ($1,$2,$3)
		ON CONFLICT (symbol) DO UPDATE SET written_at = EXCLUDED.written_at`, sym, name, now.Unix())
}

func saveCoinText(db *poltergres.ReadWrite, sym, name, text, from string, now time.Time) error {
	return db.Exec(`INSERT INTO coin_info (symbol, name, written, written_at, written_from) VALUES ($1,$2,$3,$4,$5)
		ON CONFLICT (symbol) DO UPDATE SET written = EXCLUDED.written, written_at = EXCLUDED.written_at, written_from = EXCLUDED.written_from`,
		sym, name, text, now.Unix(), from)
}

// readAboutCoin reads Wikipedia, the coin's site and Coinbase's text; what could not be read is
// left out.
func readAboutCoin(ctx context.Context, client *egress.Client, name, sym, coinbase, site string) []coinSource {
	var src []coinSource
	// the box's own Wikipedia first: then the internet is asked about no coin
	if w, found, have := wikiAboutCoin(name, sym); have {
		if found {
			src = append(src, w)
		}
	} else if f, err := client.Get(ctx, "coin:wiki-search:"+sym, wikiSearchURL(name)); err == nil && f.Status == 200 {
		if title := pickWikiTitle(f.Body, name, sym); title != "" {
			if s, err := client.Get(ctx, "coin:wiki:"+sym, wikiSummaryURL(title)); err == nil && s.Status == 200 {
				if ex := parseWikiSummary(s.Body, name, sym); ex != "" {
					src = append(src, coinSource{From: "Wikipedia", Text: ex})
				}
			}
		}
	}
	if host, ok := publicSite(site); ok {
		if f, err := client.GetPage(ctx, "coin:site:"+sym, site); err == nil && f.Status == 200 {
			if t := siteText(f.Body); len(t) >= 60 {
				src = append(src, coinSource{From: host, Text: t})
			}
		}
	}
	if cb := strings.TrimSpace(coinbase); len(cb) >= 40 {
		src = append(src, coinSource{From: "Coinbase", Text: clipText(cb, coinSourceKeep)})
	}
	return src
}

// coinDescStatus is the ctl's line: how many of the top hundred are written.
func coinDescStatus(db *poltergres.ReadWrite) string {
	rows, err := db.Query(`SELECT count(*) FILTER (WHERE COALESCE(i.written, '') <> ''), count(*)
		FROM (SELECT symbol FROM coin_ranks WHERE ts = (SELECT max(ts) FROM coin_ranks) ORDER BY rank LIMIT $1) r
		LEFT JOIN coin_info i ON i.symbol = r.symbol`, coinDescTop)
	if err != nil || len(rows.Vals) != 1 || len(rows.Vals[0]) != 2 {
		return ""
	}
	return str(rows.Vals[0][0]) + " of " + str(rows.Vals[0][1]) + " coins written"
}
