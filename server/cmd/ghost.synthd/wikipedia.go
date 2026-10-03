package main

// THE BOX'S OWN WIKIPEDIA (internal/wiki; the mirror's set, under <volume>/wiki). Three uses:
//   - a coin's page: what the box reads about a coin comes from the copy on the disk first, so the
//     box asks the internet about no coin when it has the copy (coindesc.go);
//   - the chat: "what is X", "who was X", "tell me about X" brings the article's lead into the
//     context, read on the box, the question never leaving it (wikiSource);
//   - ctl `wiki` (title=…): which file the box has and, with a title, the lead it would use.

import (
	"encoding/json"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/wiki"
)

// boxWiki is the volume's Wikipedia; its folder is set at start (wikiDirOf).
var boxWiki = &wiki.Shared{}

func wikiDirOf(mount string) string { return filepath.Join(mount, "wiki") }

const wikiChatLead = 900 // characters of an article's lead given to the chat

// coin pages: the article about the coin, by the ways Wikipedia titles a coin's article
var coinTitleHints = []string{" (cryptocurrency)", " (blockchain platform)", " (blockchain)", " (cryptocurrency exchange)", " (token)", " (digital currency)", " (stablecoin)", ""}

// wikiAboutCoin is the box's Wikipedia on a coin: ok is false when the box has no copy (the
// network is asked instead); found is false when the copy has no article about it.
func wikiAboutCoin(name, sym string) (src coinSource, found, ok bool) {
	w, err := boxWiki.Get()
	if err != nil {
		return coinSource{}, false, false
	}
	var tries []string
	for _, h := range coinTitleHints {
		tries = append(tries, name+h)
	}
	// "Name (…)" titles the hints missed: the ones whose bracket says what a coin is
	if ts, err := w.Titles(name+" (", 12); err == nil {
		for _, t := range ts {
			if reCryptoWord.MatchString(t) {
				tries = append(tries, t)
			}
		}
	}
	tries = append(tries, sym)
	seen := map[string]bool{}
	for _, t := range tries {
		if seen[strings.ToLower(t)] {
			continue
		}
		seen[strings.ToLower(t)] = true
		a, got, err := w.Article(t, coinSourceKeep)
		if err != nil || !got || a.Disamb {
			continue
		}
		if namesCoin(a.Lead, name, sym) && reCryptoWord.MatchString(a.Lead) {
			return coinSource{From: "Wikipedia", Text: a.Lead}, true, true
		}
	}
	return coinSource{}, false, true
}

var (
	reWhatIs = regexp.MustCompile(`(?i)^\s*(?:(?:so|and|ok|hey)[, ]+)?(?:who|what)\s+(?:is|was|are|were)\s+(?:the\s+|a\s+|an\s+)?(.{2,80}?)\s*[?.!]*\s*$`)
	reAbout  = regexp.MustCompile(`(?i)(?:tell me about|explain|what do you know about|the history of|where is|when was|when did|who founded|who invented|who wrote)\s+(?:the\s+)?(.{2,80}?)\s*[?.!]*\s*$`)
	reMine   = regexp.MustCompile(`(?i)\b(my|our|me|mine|i|we|us|i'm|i've|today|yesterday|tomorrow|tonight)\b`)
)

// wikiSubject is what a question asks Wikipedia about, "" when it does not (pure, for the tests):
// "what is X", "who was X", "tell me about X", "where is X"; never a question about the person's
// own things ("what is my plan", "tell me about our trip").
func wikiSubject(prompt string) string {
	p := strings.TrimSpace(prompt)
	if strings.Contains(p, "\n") || len(p) > 200 {
		return ""
	}
	var x string
	if m := reWhatIs.FindStringSubmatch(p); m != nil {
		x = m[1]
	} else if m := reAbout.FindStringSubmatch(p); m != nil {
		x = m[1]
	}
	x = strings.TrimSpace(strings.Trim(x, "\"'“”"))
	if x == "" || reMine.MatchString(x) || len(strings.Fields(x)) > 6 {
		return ""
	}
	// "what is the time", "what is the weather": not Wikipedia's
	switch strings.ToLower(x) {
	case "time", "the time", "weather", "the weather", "it", "this", "that", "up", "going on", "new", "happening", "the news", "news":
		return ""
	}
	return x
}

// wikiSource brings the article's lead when a question asks what or who something is.
func wikiSource(runDir, prompt string) []ctxItem {
	x := wikiSubject(prompt)
	if x == "" {
		return nil
	}
	w, err := boxWiki.Get()
	if err != nil {
		return nil
	}
	a, ok, err := w.Article(x, wikiChatLead)
	if err != nil || !ok || a.Lead == "" {
		return nil
	}
	why := "Wikipedia on the box (" + w.Name + "): " + a.Title
	if a.Disamb {
		why += " , a page of meanings; say which one is meant"
	}
	return []ctxItem{{Source: "wikipedia", Snippet: a.Title + ": " + a.Lead, Why: why}}
}

// reFreshAsk is a question whose answer moves with time: not one the box's Wikipedia, dated as
// its file is, should close the web for.
var reFreshAsk = regexp.MustCompile(`(?i)\b(current|currently|now|today|latest|newest|recent|new|this (year|month|week)|20[2-9][0-9]|price|worth|score|result|weather)\b`)

// wikiCovers says whether the box's own Wikipedia answers a "what is X" / "who was X" question
// outright, so the phone searches nothing (and asks Wikipedia's own API nothing): the file is on
// the box, the subject is an article of its own (not a page of meanings), and the question is
// not about a thing that moves with time (a "current", a "president of", a "price"). Pure but
// for the file read; "" and false when the web should have its turn.
func wikiCovers(prompt string) (string, bool) {
	x := wikiSubject(prompt)
	if x == "" || reFreshAsk.MatchString(prompt) || strings.Contains(" "+strings.ToLower(x)+" ", " of ") {
		return "", false
	}
	w, err := boxWiki.Get()
	if err != nil {
		return "", false
	}
	a, ok, err := w.Article(x, 200)
	if err != nil || !ok || a.Disamb || a.Lead == "" {
		return "", false
	}
	return "Wikipedia on the box (" + w.Name + "): " + a.Title, true
}

// wikiCtl is the ctl command: which file, and a title's lead.
func wikiCtl(args json.RawMessage) (ctlsock.Response, error) {
	var a struct {
		Title string `json:"title"`
	}
	_ = json.Unmarshal(args, &a)
	w, err := boxWiki.Get()
	if err != nil {
		return ctlsock.Response{OK: false, Err: err.Error()}, nil
	}
	out := map[string]any{"file": w.Path, "name": w.Name, "entries": w.Entries()}
	if t := strings.TrimSpace(a.Title); t != "" {
		art, ok, err := w.Article(t, 2000)
		switch {
		case err != nil:
			out["error"] = err.Error()
		case !ok:
			out["found"] = false
			if ts, _ := w.Titles(t, 8); len(ts) > 0 {
				out["titles"] = ts
			}
		default:
			out["found"], out["title"], out["lead"], out["disambiguation"] = true, art.Title, art.Lead, art.Disamb
		}
	}
	b, _ := json.Marshal(out)
	return ctlsock.Response{OK: true, Data: b}, nil
}
