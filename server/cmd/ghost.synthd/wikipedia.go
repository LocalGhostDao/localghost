package main

// THE BOX'S OWN WIKIPEDIA (internal/wiki). The mirror's file (set wikipedia, the English Wikipedia
// without pictures, under <volume>/wiki) is IMPORTED into Postgres by wikiImportLoop, a slice at a
// time in the background with the progress in Box Status, and removed once it is in (a marker,
// .imported, tells update.sh the edition the box holds). From then on every lookup is SQL on the
// volume's database and the file is never opened at a question. Three uses:
//   - a coin's page: what the box reads about a coin comes from the copy first, so the box asks
//     the internet about no coin when it has the copy (coindesc.go);
//   - the chat: "what is X", "who was X", "tell me about X", and the names in any question,
//     bring the article's lead (and the section the question points at) into the context, read on
//     the box, the question never leaving it (wikiSource);
//   - ctl `wiki` (q=…, idx=…), and GET /v1/wiki through secd: the import's state, a search, an
//     article whole, for the phone's WIKIPEDIA page.

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"github.com/LocalGhostDao/localghost/server/internal/wiki"
)

func wikiDirOf(mount string) string { return filepath.Join(mount, "wiki") }

const (
	wikiChatLead    = 900              // characters of an article's lead given to the chat
	wikiChatSection = 700              // characters of the section a question points at
	wikiImportSlice = 55 * time.Second // one slice of the import, then the state is saved and the loop breathes
	wikiImportRoom  = 8 << 30          // free bytes the volume must have for a slice to run
)

// freeBytes is the room on the filesystem a directory is on, 0 when it cannot be asked.
func freeBytes(dir string) int64 {
	var st syscall.Statfs_t
	if err := syscall.Statfs(dir, &st); err != nil {
		return 0
	}
	return int64(st.Bavail) * int64(st.Bsize)
}

var (
	wikiMount string // the volume, set at start
	// wikiAnswers counts the chat questions the box's article answered since synthd started (Box
	// Status shows it; it is not kept across restarts).
	wikiAnswers atomic.Int64
	wikiMu      sync.Mutex
	wikiStoreV  *wiki.Store
)

// wikiStore is the store over the volume's database, nil until the box is unlocked.
func wikiStore() *wiki.Store {
	wikiMu.Lock()
	defer wikiMu.Unlock()
	if wikiStoreV == nil {
		if db := chatStore(wikiMount); db != nil {
			wikiStoreV = &wiki.Store{DB: db, NewConn: func() *poltergres.ReadWrite { return chatConn(wikiMount) }}
		}
	}
	return wikiStoreV
}

// wikiReady is the store when it answers (an import finished), with the edition's name.
func wikiReady() (*wiki.Store, string, bool) {
	s := wikiStore()
	if s == nil {
		return nil, "", false
	}
	st, ok := s.Ready()
	if !ok {
		return nil, "", false
	}
	return s, st.Edition, true
}

// wikiImportLoop brings the file into the database: every minute it looks for a .zim under the
// volume's wiki folder; one that is not imported yet (or is a new edition) is read a slice at a time,
// the state saved after each; once every entry is in, the file is removed and .imported written
// beside where it was (its name and the edition), so update.sh knows the box has it without the
// file. An error ends the slice and is said in Box Status; the next minute tries again.
func wikiImportLoop(ctx context.Context, mount string, lg *slog.Logger) {
	dir := wikiDirOf(mount)
	var open *wiki.Wiki
	defer func() {
		if open != nil {
			open.Close()
		}
	}()
	t := time.NewTicker(time.Minute)
	defer t.Stop()
	indexed := false // the lookup indexes seen to (once a process, with a finished import)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		}
		s := wikiStore()
		if s == nil {
			continue // locked
		}
		if !indexed {
			if st := s.State(); st.Done {
				indexed = true
				if err := s.EnsureIndexes(); err != nil {
					lg.Warn("the Wikipedia lookup indexes", "fn", "wikiImportLoop", "err", err)
				}
			}
		}
		path, ok := wiki.Find(dir)
		if !ok {
			if open != nil {
				open.Close()
				open = nil
			}
			// no file and no state, but the tables full: the state was lost after the import
			// (wiki again=1 after it finished, or a settings row gone); it comes back from the tables
			if st, did := s.Recover(dir); did {
				indexed = false // seen to on the next tick, with the state done
				lg.Info("the Wikipedia import's state was rebuilt from the tables", "fn", "wikiImportLoop", "edition", st.Edition, "articles", st.Articles, "redirects", st.Redirects)
			}
			continue
		}
		if open != nil && open.Path != path {
			open.Close()
			open = nil
		}
		if open == nil {
			w, err := wiki.Open(path)
			if err != nil {
				lg.Warn("the Wikipedia file would not open", "fn", "wikiImportLoop", "file", filepath.Base(path), "err", err)
				st := s.State()
				st.Error = "the file would not open: " + err.Error()
				_ = s.Save(st)
				continue
			}
			open = w
		}
		st := s.State()
		if st.Done && s.Current(open) {
			// imported: the file goes, the marker stays
			if !st.Removed {
				// the marker: "<file> <sha256> <edition>", the hash from the record mirror_fetch.sh
				// left beside the file, so update.sh fetches the file again only for a new edition
				sha := "-"
				if rec, err := os.ReadFile(filepath.Join(dir, "."+filepath.Base(path)+".sha256")); err == nil {
					if f := strings.Fields(string(rec)); len(f) > 0 {
						sha = f[0]
					}
				}
				marker := filepath.Join(dir, ".imported")
				_ = os.WriteFile(marker, []byte(filepath.Base(path)+" "+sha+" "+st.Edition+"\n"), 0o640)
				open.Close()
				open = nil
				if err := os.Remove(path); err != nil {
					lg.Warn("the imported file could not be removed", "fn", "wikiImportLoop", "file", filepath.Base(path), "err", err)
					continue
				}
				for _, side := range []string{".mirror-files"} {
					_ = os.Remove(filepath.Join(dir, side))
				}
				st.Removed = true
				_ = s.Save(st)
				lg.Info("Wikipedia imported, the file removed", "fn", "wikiImportLoop", "edition", st.Edition, "articles", st.Articles, "redirects", st.Redirects)
			}
			continue
		}
		// room: the tables come to a third of the file's size while the file is still there; the
		// import waits rather than fill the volume, and says so
		if free := freeBytes(dir); free > 0 && free < wikiImportRoom {
			st.Error = fmt.Sprintf("the volume has %d GB free; the import wants %d GB to be safe (the file goes when it is done)", free>>30, wikiImportRoom>>30)
			_ = s.Save(st)
			continue
		}
		before := st.Read()
		st, err := s.ImportWith(open, wikiImportWorkers(), wikiImportSlice)
		if err != nil {
			lg.Warn("Wikipedia import slice failed", "fn", "wikiImportLoop", "at", st.Next, "err", err)
			continue
		}
		if st.Done {
			indexed = true // made at the end of the import
			lg.Info("Wikipedia import finished", "fn", "wikiImportLoop", "edition", st.Edition, "articles", st.Articles, "redirects", st.Redirects, "skipped", st.Skipped)
		} else if st.Read() > before {
			lg.Info("Wikipedia import", "fn", "wikiImportLoop", "entries", st.Read(), "of", st.Total, "articles", st.Articles, "redirects", st.Redirects, "readers", len(st.Shards), "left", wikiLeft(st).Round(time.Minute).String())
		}
	}
}

// wikiImportWorkers is how many readers an import slice runs: half the cores, two to four. The
// box's CPU also runs the photo pipeline and whisper; the file is read in parallel, the inserts
// go over as many connections.
func wikiImportWorkers() int {
	n := runtime.NumCPU() / 2
	if n < 2 {
		n = 2
	}
	if n > 4 {
		n = 4
	}
	return n
}

// wikiLeft is how long the import has to go at the pace so far (0 when it cannot say).
func wikiLeft(st wiki.ImportState) time.Duration {
	read := st.Read()
	if st.Done || read == 0 || st.StartedAt == 0 || st.Total <= read {
		return 0
	}
	took := time.Since(time.Unix(st.StartedAt, 0))
	if took <= 0 {
		return 0
	}
	return time.Duration(float64(took) * float64(st.Total-read) / float64(read))
}

// wikiStatus is the box's Wikipedia for Box Status: ready with its edition and counts; importing
// with how far; downloading when a hidden part file lies in the folder (update.sh's fetch);
// missing; failed with the last error.
func wikiStatus() hw.SynthWiki {
	st := hw.SynthWiki{Answers: int(wikiAnswers.Load())}
	s := wikiStore()
	if s == nil {
		st.State = "locked"
		return st
	}
	is := s.State()
	if is.Total == 0 && !is.Done {
		// a full Wikipedia behind a lost state is said as ready, not missing (and the state put back)
		is, _ = s.Recover(wikiDirOf(wikiMount))
	}
	st.Name, st.File, st.Articles, st.Redirects, st.Next, st.Total, st.Error = is.Edition, is.File, is.Articles, is.Redirects, int64(is.Read()), int64(is.Total), is.Error
	path, haveFile := wiki.Find(wikiDirOf(wikiMount))
	switch {
	case is.Done && is.Articles > 0:
		st.State = "ready"
		if haveFile && !is.Removed {
			st.File = filepath.Base(path)
		}
	case is.Total > 0 && haveFile:
		st.State = "importing"
		st.Left = int64(wikiLeft(is).Seconds())
		if is.Error != "" {
			st.State = "failed"
		}
	case is.Total > 0 && !haveFile:
		st.State, st.Error = "failed", "the file went away before the import finished"
	default:
		st.State = "missing"
		if parts, _ := filepath.Glob(filepath.Join(wikiDirOf(wikiMount), ".*.part")); len(parts) > 0 {
			if fi, err := os.Stat(parts[0]); err == nil {
				st.State, st.Downloading, st.File = "downloading", fi.Size(), strings.TrimSuffix(strings.TrimPrefix(filepath.Base(parts[0]), "."), ".part")
			}
		} else if haveFile {
			st.State, st.File = "importing", filepath.Base(path) // found, the first slice not yet run
		}
	}
	return st
}

// --- the coin pages ---

// coin pages: the article about the coin, by the ways Wikipedia titles a coin's article
var coinTitleHints = []string{" (cryptocurrency)", " (blockchain platform)", " (blockchain)", " (cryptocurrency exchange)", " (token)", " (digital currency)", " (stablecoin)", ""}

// wikiAboutCoin is the box's Wikipedia on a coin: ok is false when the box has no copy (the
// network is asked instead); found is false when the copy has no article about it.
func wikiAboutCoin(name, sym string) (src coinSource, found, ok bool) {
	s, _, ready := wikiReady()
	if !ready {
		return coinSource{}, false, false
	}
	seen := map[uint32]bool{}
	try := func(q string, hows string) (coinSource, bool) {
		hits, err := s.Lookup(q, 8, coinSourceKeep)
		if err != nil {
			return coinSource{}, false
		}
		for _, h := range hits {
			if seen[h.Idx] || h.Disamb || !strings.Contains(hows, h.How) {
				continue
			}
			seen[h.Idx] = true
			if namesCoin(h.Lead, name, sym) && reCryptoWord.MatchString(h.Lead) {
				return coinSource{From: "Wikipedia", Text: h.Lead}, true
			}
		}
		return coinSource{}, false
	}
	for _, h := range coinTitleHints {
		if src, got := try(name+h, "exact redirect"); got {
			return src, true, true
		}
	}
	// "Name (…)" titles the hints missed, and the symbol
	if src, got := try(name, "exact redirect prefix like"); got {
		return src, true, true
	}
	if src, got := try(sym, "exact redirect"); got {
		return src, true, true
	}
	return coinSource{}, false, true
}

// --- the chat ---

var (
	reWhatIs = regexp.MustCompile(`(?i)^\s*(?:(?:so|and|ok|hey)[, ]+)?(?:who|what)\s+(?:is|was|are|were)\s+(?:the\s+|a\s+|an\s+)?(.{2,80}?)\s*[?.!]*\s*$`)
	reAbout  = regexp.MustCompile(`(?i)(?:tell me about|explain|what do you know about|the history of|where is|when was|when did|who founded|who invented|who wrote|how (?:tall|old|big|long|far|high|deep) is)\s+(?:the\s+)?(.{2,80}?)\s*[?.!]*\s*$`)
	reMine   = regexp.MustCompile(`(?i)\b(my|our|me|mine|i|we|us|i'm|i've|today|yesterday|tomorrow|tonight)\b`)
)

// wikiSubject is what a question asks Wikipedia about, "" when it does not (pure, for the tests):
// "what is X", "who was X", "tell me about X", "where is X", "how tall is X"; never a question
// about the person's own things ("what is my plan", "tell me about our trip").
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

// wikiNames are the names in a question worth a lookup when it has no "what is X" shape: the
// capitalised runs (wiki.Names), without the person's own name and people (those are the
// memories' business, and "Vlad" is also a prince of Wallachia), and without a question about the
// person's own things.
func wikiNames(prompt string, owner string, people []string) []string {
	if reMine.MatchString(prompt) || strings.Contains(prompt, "\n") || len(prompt) > 240 {
		return nil
	}
	skip := map[string]bool{}
	for _, p := range append(people, owner) {
		for _, w := range strings.Fields(strings.ToLower(p)) {
			skip[w] = true
		}
	}
	var out []string
	for _, n := range wiki.Names(prompt) {
		first := strings.ToLower(strings.Fields(n)[0])
		if skip[first] || skip[strings.ToLower(n)] {
			continue
		}
		out = append(out, n)
		if len(out) == 3 {
			break
		}
	}
	return out
}

// wikiFind is the article a question is about, if any: the question's subject by any sure way
// (the title, a redirect, a qualified place, a prefix, a likeness), else the first of its names
// found by title, redirect or qualified place and no looser.
func wikiFind(s *wiki.Store, prompt string) (wiki.Hit, bool) {
	if x := wikiSubject(prompt); x != "" {
		if h, ok, err := s.Best(x, wikiChatLead); err == nil && ok && h.How != "text" {
			return h, true
		}
	}
	owner := ""
	var people []string
	if db := chatStore(wikiMount); db != nil {
		owner, people = setting(db, ownerKey), peopleNames(db)
	}
	for _, n := range wikiNames(prompt, owner, people) {
		hits, err := s.Lookup(n, 3, wikiChatLead)
		if err != nil {
			break
		}
		for _, h := range hits {
			if !h.Disamb && (h.How == "exact" || h.How == "redirect" || h.How == "qualified") {
				return h, true
			}
		}
	}
	return wiki.Hit{}, false
}

// wikiSource brings the article's lead when a question asks what or who something is, or names
// something the box's Wikipedia has, and the section the question points at when it points at one.
func wikiSource(runDir, prompt string) []ctxItem {
	s, edition, ready := wikiReady()
	if !ready {
		return nil
	}
	h, ok := wikiFind(s, prompt)
	if !ok || h.Lead == "" {
		return nil
	}
	why := "Wikipedia on the box (" + edition + "): " + h.Title
	if h.Disamb {
		why += " , a page of meanings; say which one is meant"
	}
	snippet := h.Title + ": " + h.Lead
	// the section the question points at, when it asks for more than what the thing is
	if !h.Disamb && (wikiSubject(prompt) == "" || reDetailAsk.MatchString(prompt)) {
		if a, got, err := s.Article(h.Idx); err == nil && got && a.Body != "" {
			if heading, text := wiki.SectionFor(a.Body, prompt, wikiChatSection); text != "" {
				snippet += "\n" + heading + ": " + text
			}
		}
	}
	wikiAnswers.Add(1)
	return []ctxItem{{Source: "wikipedia", Snippet: snippet, Why: why}}
}

// reDetailAsk is a question that asks for a particular of a thing, not what it is: a section
// may hold the answer.
var reDetailAsk = regexp.MustCompile(`(?i)\b(when|how (tall|old|big|long|far|high|deep|many|much|fast|heavy)|where|why|which|built|founded|born|died|population|height|length|capital)\b`)

// reFreshAsk is a question whose answer moves with time: not one the box's Wikipedia, dated as
// its edition is, should close the web for.
var reFreshAsk = regexp.MustCompile(`(?i)\b(current|currently|now|today|latest|newest|recent|new|this (year|month|week)|20[2-9][0-9]|price|worth|score|result|weather)\b`)

// wikiCovers says whether the box's own Wikipedia answers a "what is X" / "who was X" question
// outright, so the phone searches nothing (and asks Wikipedia's own API nothing): the import is
// in, the subject is an article of its own (by title, redirect or qualified place, not a guess),
// not a page of meanings, and the question is not about a thing that moves with time (a
// "current", a "president of", a "price"). "" and false when the web should have its turn.
func wikiCovers(prompt string) (string, bool) {
	x := wikiSubject(prompt)
	if x == "" || reFreshAsk.MatchString(prompt) || strings.Contains(" "+strings.ToLower(x)+" ", " of ") {
		return "", false
	}
	s, edition, ready := wikiReady()
	if !ready {
		return "", false
	}
	h, ok, err := s.Best(x, 200)
	if err != nil || !ok || h.Disamb || h.Lead == "" || (h.How != "exact" && h.How != "redirect" && h.How != "qualified") {
		return "", false
	}
	return "the box's own Wikipedia (" + edition + ") has an article on " + h.Title, true
}

// wikiCtl is the ctl command, and what GET /v1/wiki answers through secd: the import's state and
// the counts; q= a search (the hits, surest first); idx= one article whole; title= as q (the
// older form).
func wikiCtl(args json.RawMessage) (ctlsock.Response, error) {
	var a struct {
		Q     string `json:"q"`
		Title string `json:"title"`
		Idx   uint32 `json:"idx"`
		N     int    `json:"n"`
		Again bool   `json:"again"` // start the import over from the file's first entry
	}
	_ = json.Unmarshal(args, &a)
	s := wikiStore()
	if s == nil {
		return ctlsock.Response{OK: false, Err: "no database (box locked?)"}, nil
	}
	if a.Again {
		// a state for no file: the next slice sees another file, empties the tables and reads
		// from the start, with this build's readers
		if err := s.Save(wiki.ImportState{}); err != nil {
			return ctlsock.Response{}, err
		}
		return ctlsock.Response{OK: true, Text: "the import starts over at the next slice (within a minute)"}, nil
	}
	st := s.State()
	status := wikiStatus()
	out := map[string]any{"state": status.State, "edition": st.Edition, "articles": st.Articles, "redirects": st.Redirects,
		"imported": st.Read(), "entries": st.Total, "file": status.File, "answers": status.Answers, "readers": len(st.Shards),
		"likeness": s.Likeness()}
	if status.Error != "" {
		out["error"] = status.Error
	}
	if status.Downloading > 0 {
		out["downloadingBytes"] = status.Downloading
	}
	if st.StartedAt > 0 && !st.Done {
		out["importingSince"] = st.StartedAt
		if status.Left > 0 {
			out["leftMinutes"] = status.Left / 60
		}
	}
	// the stats the WIKIPEDIA page shows: when the import began and ended, what was skipped, the
	// tables' size, whether the words-of-a-lead and likeness lookups are on yet
	out["startedAt"], out["doneAt"], out["skipped"], out["bytes"], out["indexed"] = st.StartedAt, st.DoneAt, st.Skipped, s.Bytes(), s.Indexed()
	if q := strings.TrimSpace(a.Q + a.Title); q != "" {
		n := a.N
		if n <= 0 || n > 20 {
			n = 8
		}
		hits, err := s.Lookup(q, n, 400)
		if err != nil {
			out["error"] = err.Error()
		} else {
			if hits == nil {
				hits = []wiki.Hit{}
			}
			out["hits"] = hits
			out["found"] = len(hits) > 0
		}
	}
	if a.Idx > 0 {
		art, ok, err := s.Article(a.Idx)
		switch {
		case err != nil:
			out["error"] = err.Error()
		case !ok:
			out["found"] = false
		default:
			out["article"] = map[string]any{"idx": art.Idx, "title": art.Title, "lead": art.Lead, "body": art.Body, "disamb": art.Disamb}
		}
	}
	b, _ := json.Marshal(out)
	return ctlsock.Response{OK: true, Data: b}, nil
}
