// Package wiki is the box's own Wikipedia: the mirror's ZIM file (the English Wikipedia without
// pictures, as Kiwix packages it) imported into Postgres once, then every lookup from the tables.
// The chat's "what is Solana" and the coin pages are answered from the disk; the question reaches
// nothing outside the box.
//
// Two halves. The FILE (this file): opening the ZIM, reading an article's HTML into plain text (the
// lead, the sections). The STORE (store.go): the import into wiki_articles and wiki_redirects, in
// slices the daemon spreads over time, and the lookups: by title in any case, a qualified place
// without its comma, a prefix, a likeness (pg_trgm), and full-text search over the title and the
// lead. The file is read at import and never at a question: a question is a few SQL reads, and
// the file can go once it is in.
package wiki

import (
	"html"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"

	"github.com/LocalGhostDao/localghost/server/internal/zim"
)

// Article is what the box keeps of an article.
type Article struct {
	Idx    uint32 // the entry's place in the file it came from
	Title  string
	Lead   string
	Body   string // the sections after the lead, "== Heading ==" lines between them ("" when not asked for)
	Disamb bool   // a "may refer to" page, not an article about one thing
}

// Wiki is an open Wikipedia file, for the import.
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

// Size is the file's size in bytes.
func (w *Wiki) Size() int64 {
	fi, err := os.Stat(w.Path)
	if err != nil {
		return 0
	}
	return fi.Size()
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

// Body is the article after its lead as plain text: each section's heading on a line of its own
// between double equals, then its paragraphs; sections that are lists of references are left out;
// at most max characters in all.
func Body(page string, max int) string {
	var b strings.Builder
	for _, s := range Sections(page, 0) {
		if b.Len() > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString("== " + s.Heading + " ==\n")
		b.WriteString(s.Text)
		if max > 0 && b.Len() >= max {
			break
		}
	}
	out := b.String()
	if max > 0 && len(out) > max {
		out = cutAt(out, max)
	}
	return out
}
