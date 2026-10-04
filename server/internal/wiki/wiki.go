// Package wiki is the box's own Wikipedia: Kiwix's English Wikipedia without pictures (the mirror's
// set wikipedia, one ZIM file of about 50 GB under <volume>/wiki), read with internal/zim. Asking it
// reaches nothing outside the box: "what is Solana" is answered from the disk, so the question
// never leaves. What the box takes from it is an article's lead: the paragraphs before the first
// section, references and tables out, the text plain.
package wiki

import (
	"errors"
	"html"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/LocalGhostDao/localghost/server/internal/zim"
)

// Article is what the box keeps of an article.
type Article struct {
	Title  string
	Path   string
	Lead   string
	Disamb bool // a "may refer to" page, not an article about one thing
}

// Wiki is an open Wikipedia file.
type Wiki struct {
	z    *zim.File
	ns   byte
	Path string
	Name string // the file's own title and date ("Wikipedia, 2026-06")
}

// Find is the Wikipedia file in a directory: the largest .zim there (the set's one file).
func Find(dir string) (string, bool) {
	ms, _ := filepath.Glob(filepath.Join(dir, "*.zim"))
	best, size := "", int64(0)
	for _, m := range ms {
		if st, err := os.Stat(m); err == nil && st.Mode().IsRegular() && st.Size() > size {
			best, size = m, st.Size()
		}
	}
	return best, best != ""
}

// Open opens a Wikipedia ZIM file.
func Open(path string) (*Wiki, error) {
	z, err := zim.Open(path)
	if err != nil {
		return nil, err
	}
	w := &Wiki{z: z, ns: z.ArticleNamespace(), Path: path}
	w.Name = strings.TrimSpace(z.Title())
	if d := strings.TrimSpace(z.Metadata("Date")); d != "" {
		if len(d) >= 7 {
			d = d[:7]
		}
		w.Name = strings.TrimSpace(w.Name + ", " + d)
	}
	if w.Name == "" {
		w.Name = filepath.Base(path)
	}
	return w, nil
}

// Close closes the file.
func (w *Wiki) Close() error { return w.z.Close() }

// Entries is how many entries the file holds (articles, redirects and the rest).
func (w *Wiki) Entries() uint32 { return w.z.H.EntryCount }

// variants of what was asked: as written, the first letter capitalised (Wikipedia's titles all
// start with one), each word capitalised, and all capitals for a short one (an acronym).
func variants(q string) []string {
	q = strings.Join(strings.Fields(q), " ")
	if q == "" {
		return nil
	}
	seen := map[string]bool{}
	var out []string
	add := func(s string) {
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	add(q)
	add(upperFirst(q))
	words := strings.Fields(strings.ToLower(q))
	for i, wd := range words {
		if i == 0 || len(wd) > 3 {
			words[i] = upperFirst(wd)
		}
	}
	add(strings.Join(words, " "))
	add(upperFirst(strings.ToLower(q)))
	if utf8.RuneCountInString(q) <= 5 && !strings.Contains(q, " ") {
		add(strings.ToUpper(q))
	}
	return out
}

func upperFirst(s string) string {
	r, n := utf8.DecodeRuneInString(s)
	if r == utf8.RuneError {
		return s
	}
	return string(unicode.ToUpper(r)) + s[n:]
}

// Article is the article a title names (redirects followed), trying the ways it may be written,
// and then, for a phrase of two or more words, the ways a place is qualified ("Greenwich London"
// as "Greenwich, London", "Greenwich (London)", and "Greenwich" alone when the last word is a
// qualifier like a city or a country).
func (w *Wiki) Article(title string, maxLead int) (Article, bool, error) {
	a, ok, err := w.article(title, maxLead)
	if err != nil || ok {
		return a, ok, err
	}
	words := strings.Fields(title)
	if len(words) < 2 {
		return Article{}, false, nil
	}
	head, last := strings.Join(words[:len(words)-1], " "), words[len(words)-1]
	for _, t := range []string{head + ", " + last, head + " (" + last + ")"} {
		if a, ok, err := w.article(t, maxLead); err != nil || ok {
			return a, ok, err
		}
	}
	if qualifier[strings.ToLower(last)] {
		return w.article(head, maxLead)
	}
	return Article{}, false, nil
}

// qualifier is a trailing word that places a thing rather than names it.
var qualifier = map[string]bool{"london": true, "uk": true, "england": true, "scotland": true, "wales": true, "ireland": true, "france": true,
	"italy": true, "spain": true, "greece": true, "romania": true, "germany": true, "europe": true, "usa": true, "us": true, "america": true,
	"city": true, "town": true, "village": true, "island": true, "county": true, "borough": true, "district": true, "park": true, "area": true}

func (w *Wiki) article(title string, maxLead int) (Article, bool, error) {
	for _, v := range variants(title) {
		e, ok, err := w.z.FindPath(w.ns, strings.ReplaceAll(v, " ", "_"))
		if err != nil {
			return Article{}, false, err
		}
		if !ok {
			if e, ok, err = w.z.FindTitle(w.ns, v); err != nil {
				return Article{}, false, err
			}
		}
		if !ok {
			continue
		}
		e, err = w.z.Resolve(e)
		if err != nil {
			return Article{}, false, err
		}
		if !strings.HasPrefix(e.Mime, "text/html") {
			continue
		}
		body, err := w.z.Content(e)
		if err != nil {
			return Article{}, false, err
		}
		lead := Lead(string(body), maxLead)
		return Article{Title: e.Title, Path: e.Path, Lead: lead, Disamb: disamb(lead)}, true, nil
	}
	return Article{}, false, nil
}

// Titles is up to n titles that start with prefix (the first letter capitalised).
func (w *Wiki) Titles(prefix string, n int) ([]string, error) {
	es, err := w.z.TitlesWithPrefix(w.ns, upperFirst(strings.TrimSpace(prefix)), n)
	if err != nil {
		return nil, err
	}
	out := make([]string, 0, len(es))
	for _, e := range es {
		out = append(out, e.Title)
	}
	sort.Strings(out)
	return out, nil
}

var reDisamb = regexp.MustCompile(`(?i)\bmay (also )?refer to\b|\bmay stand for\b`)

func disamb(lead string) bool {
	if len(lead) > 600 {
		lead = lead[:600]
	}
	return reDisamb.MatchString(lead)
}

var (
	reCut      = regexp.MustCompile(`(?i)<h2\b|data-mw-section-id="[1-9]|<details\b`)
	reStyle    = regexp.MustCompile(`(?is)<(style|script|noscript|figure|figcaption)\b.*?</(style|script|noscript|figure|figcaption)>`)
	reRefSup   = regexp.MustCompile(`(?is)<sup\b[^>]*(reference|mw-ref|noprint)[^>]*>.*?</sup>`)
	reEmptyP   = regexp.MustCompile(`(?is)<p\b[^>]*class="[^"]*mw-empty-elt[^"]*"[^>]*>.*?</p>`)
	rePara     = regexp.MustCompile(`(?is)<p\b[^>]*>(.*?)</p>`)
	reTag      = regexp.MustCompile(`(?s)<[^>]+>`)
	reSpace    = regexp.MustCompile(`\s+`)
	reBrackets = regexp.MustCompile(`\[(\d+|[a-z]|note \d+|nb \d+|citation needed|clarification needed|when\?|who\?|according to whom\?)\]`)
	reSpaceDot = regexp.MustCompile(`\s+([.,;:])`)
)

// Lead is an article's opening paragraphs as plain text, up to max characters (cut after a full
// stop when it can be): the paragraphs before the first section, without tables, references, the
// footnote marks or anything empty.
func Lead(page string, max int) string {
	if i := strings.Index(strings.ToLower(page), "<body"); i >= 0 {
		page = page[i:]
	}
	if loc := reCut.FindStringIndex(page); loc != nil {
		page = page[:loc[0]]
	}
	page = reStyle.ReplaceAllString(page, " ")
	page = dropElement(page, "table")
	page = reRefSup.ReplaceAllString(page, "")
	page = reEmptyP.ReplaceAllString(page, "")
	var paras []string
	size := 0
	for _, m := range rePara.FindAllStringSubmatch(page, -1) {
		t := html.UnescapeString(reTag.ReplaceAllString(m[1], ""))
		t = reBrackets.ReplaceAllString(t, "")
		t = strings.TrimSpace(reSpace.ReplaceAllString(t, " "))
		t = reSpaceDot.ReplaceAllString(t, "$1")
		if len(t) < 20 {
			continue
		}
		paras = append(paras, t)
		size += len(t) + 1
		if max > 0 && size >= max {
			break
		}
	}
	out := strings.Join(paras, "\n")
	if max > 0 && len(out) > max {
		cut := out[:max]
		if i := strings.LastIndex(cut, ". "); i > max/2 {
			cut = cut[:i+1]
		} else {
			for !utf8.ValidString(cut) && len(cut) > 0 {
				cut = cut[:len(cut)-1]
			}
			cut += "…"
		}
		out = cut
	}
	return out
}

// dropElement takes out every <tag …>…</tag>, nested ones counted (an infobox holds tables).
func dropElement(s, tag string) string {
	low := strings.ToLower(s)
	open, closeTag := "<"+tag, "</"+tag+">"
	var b strings.Builder
	i := 0
	for {
		j := strings.Index(low[i:], open)
		if j < 0 {
			b.WriteString(s[i:])
			return b.String()
		}
		j += i
		// "<tablex" is not "<table"
		if k := j + len(open); k < len(low) && low[k] != ' ' && low[k] != '>' && low[k] != '\n' && low[k] != '\t' && low[k] != '/' {
			b.WriteString(s[i : j+len(open)])
			i = j + len(open)
			continue
		}
		b.WriteString(s[i:j])
		depth, k := 0, j
		for k < len(low) {
			o := strings.Index(low[k:], open)
			c := strings.Index(low[k:], closeTag)
			if c < 0 {
				return b.String() // never closed: the rest goes
			}
			if o >= 0 && o < c {
				depth++
				k += o + len(open)
				continue
			}
			depth--
			k += c + len(closeTag)
			if depth == 0 {
				break
			}
		}
		i = k
	}
}

// Shared is the box's Wikipedia for a daemon: opened when first asked for, opened again when the
// file changes (an update put a newer one in place), nil while the volume has none.
type Shared struct {
	Dir string

	mu      sync.Mutex
	w       *Wiki
	modTime time.Time
	checked time.Time
	err     error
}

// ErrNone says the volume holds no Wikipedia.
var ErrNone = errors.New("no Wikipedia on this box (sudo ./tools/update.sh wiki fetches it from the mirror, about 50 GB)")

// Get is the open file, or why there is none.
func (s *Shared) Get() (*Wiki, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if time.Since(s.checked) < time.Minute {
		if s.w != nil {
			return s.w, nil
		}
		if s.err != nil {
			return nil, s.err
		}
	}
	s.checked = time.Now()
	s.err = nil
	path, ok := Find(s.Dir)
	if !ok {
		if s.w != nil {
			s.w.Close()
			s.w = nil
		}
		s.err = ErrNone
		return nil, ErrNone
	}
	st, err := os.Stat(path)
	if err != nil {
		return nil, err
	}
	if s.w != nil && s.w.Path == path && st.ModTime().Equal(s.modTime) {
		return s.w, nil
	}
	if s.w != nil {
		s.w.Close()
		s.w = nil
	}
	w, err := Open(path)
	if err != nil {
		s.err = err
		return nil, err
	}
	s.w, s.modTime = w, st.ModTime()
	return w, nil
}
