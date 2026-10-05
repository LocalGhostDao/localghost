// Package feeds reads the news feeds the phone fetched and tells which stories several outlets
// are telling. The box never opens a connection: the phone fetches each feed's bytes (the way it
// already runs the web search round) and hands them over; everything that reads, keeps or
// summarises lives here and in ghost.synthd. RSS 2.0, Atom and RSS 1.0 (RDF) are read with the
// standard library; a feed's own HTML in its summaries is stripped to text.
package feeds

import (
	"bytes"
	"encoding/xml"
	"errors"
	"html"
	"io"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"
)

// Source is one publication the box asks the phone to fetch.
type Source struct {
	ID   string `json:"id"`   // short, stable, lower case: "bbc"
	Name string `json:"name"` // as shown: "BBC"
	URL  string `json:"url"`
}

// DigestHours are the local hours ghost.synthd posts the news digest at (Box Status says when the
// next one is due from the same list).
var DigestHours = []int{7, 19}

// Retired are the papers an earlier list seeded and this one does not, taken off a box that still
// has them at the address that list gave (one an operator re-pointed is left alone).
var Retired = []Source{
	{"ft", "Financial Times", "https://www.ft.com/rss/home"},
}

// Added are the papers this list has that an earlier one did not: put on a box seeded by the
// earlier list, once each (a box whose operator takes one off again keeps it off).
var Added = []Source{
	{"wsj-world", "The Wall Street Journal", "https://feeds.content.dowjones.io/public/rss/RSSWorldNews"},
}

// GiveUpAfter is how many fetches in a row a feed that has never once given a feed gets before it
// is switched off (twelve hours at one fetch every two).
const GiveUpAfter = 6

// FetchEvery is how often a feed wants fetching.
const FetchEvery = 2 * time.Hour

// DefaultSources is the list a new box starts with. Reuters and AP are left out (neither has an
// official feed any more), and so is the FT (Retired: read in its own app).
func DefaultSources() []Source {
	return []Source{
		{"bbc", "BBC", "https://feeds.bbci.co.uk/news/rss.xml"},
		{"guardian", "The Guardian", "https://www.theguardian.com/world/rss"},
		{"economist", "The Economist", "https://www.economist.com/latest/rss.xml"},
		{"telegraph", "The Telegraph", "https://www.telegraph.co.uk/rss.xml"},
		{"aljazeera", "Al Jazeera", "https://www.aljazeera.com/xml/rss/all.xml"},
		{"npr", "NPR", "https://feeds.npr.org/1001/rss.xml"},
		{"dw", "DW", "https://rss.dw.com/rdf/rss-en-all"},
		{"politico-eu", "Politico Europe", "https://www.politico.eu/feed/"},
		{"wsj-world", "The Wall Street Journal", "https://feeds.content.dowjones.io/public/rss/RSSWorldNews"},
		{"ars", "Ars Technica", "https://feeds.arstechnica.com/arstechnica/index"},
		{"hn", "Hacker News", "https://news.ycombinator.com/rss"},
		{"coindesk", "CoinDesk", "https://www.coindesk.com/arc/outboundfeeds/rss/"},
	}
}

// Item is one entry of a feed, as the box keeps it.
type Item struct {
	GUID      string // the feed's id for the entry, else its link
	Link      string // the article, which the PHONE opens, never the box
	Title     string
	Summary   string // the feed's own description, HTML stripped, capped
	Published time.Time
}

// Feed is one parsed feed.
type Feed struct {
	Title string
	Items []Item
}

// Wire shapes, loose enough for the three dialects.
type rss struct {
	Channel struct {
		Title string    `xml:"title"`
		Items []rssItem `xml:"item"`
	} `xml:"channel"`
	// RSS 1.0 (RDF) puts the items beside the channel (the channel's title is read the same way)
	RDFItems []rssItem `xml:"item"`
}

type rssItem struct {
	Title       string `xml:"title"`
	Link        string `xml:"link"`
	GUID        string `xml:"guid"`
	Description string `xml:"description"`
	Encoded     string `xml:"encoded"` // content:encoded
	PubDate     string `xml:"pubDate"`
	DCDate      string `xml:"date"` // dc:date (RDF feeds)
}

type atom struct {
	Title   string      `xml:"title"`
	Entries []atomEntry `xml:"entry"`
}

type atomEntry struct {
	Title   string     `xml:"title"`
	ID      string     `xml:"id"`
	Links   []atomLink `xml:"link"`
	Summary string     `xml:"summary"`
	Content string     `xml:"content"`
	Updated string     `xml:"updated"`
	Pub     string     `xml:"published"`
}

type atomLink struct {
	Rel  string `xml:"rel,attr"`
	Href string `xml:"href,attr"`
}

// Parse reads a feed's bytes. The dialect is told from the root element.
func Parse(body []byte) (Feed, error) {
	body = bytes.TrimLeft(body, " \t\r\n\xef\xbb\xbf")
	if len(body) == 0 {
		return Feed{}, errors.New("empty")
	}
	if len(body) > 8<<20 {
		return Feed{}, errors.New("feed over 8 MB")
	}
	root := rootName(body)
	dec := func(v any) error {
		d := xml.NewDecoder(bytes.NewReader(body))
		d.Strict = false
		d.CharsetReader = charsetReader
		return d.Decode(v)
	}
	switch root {
	case "feed":
		var a atom
		if err := dec(&a); err != nil {
			return Feed{}, err
		}
		f := Feed{Title: clean(a.Title, 200)}
		for _, e := range a.Entries {
			link := ""
			for _, l := range e.Links {
				if l.Rel == "" || l.Rel == "alternate" {
					link = l.Href
					break
				}
			}
			if link == "" && len(e.Links) > 0 {
				link = e.Links[0].Href
			}
			sum := e.Summary
			if strings.TrimSpace(sum) == "" {
				sum = e.Content
			}
			when := e.Pub
			if when == "" {
				when = e.Updated
			}
			f.Items = append(f.Items, item(e.ID, link, e.Title, sum, when))
		}
		return finish(f)
	case "rss", "RDF":
		var r rss
		if err := dec(&r); err != nil {
			return Feed{}, err
		}
		items := r.Channel.Items
		title := r.Channel.Title
		if len(items) == 0 {
			items = r.RDFItems
		}
		f := Feed{Title: clean(title, 200)}
		for _, it := range items {
			sum := it.Description
			if strings.TrimSpace(sum) == "" {
				sum = it.Encoded
			}
			when := it.PubDate
			if when == "" {
				when = it.DCDate
			}
			f.Items = append(f.Items, item(it.GUID, it.Link, it.Title, sum, when))
		}
		return finish(f)
	case "html", "HTML":
		return Feed{}, errors.New("an HTML page, not a feed (a consent or block page?)")
	}
	return Feed{}, errors.New("not a feed (root <" + root + ">)")
}

func finish(f Feed) (Feed, error) {
	kept := f.Items[:0]
	for _, it := range f.Items {
		if it.Title == "" || (it.Link == "" && it.GUID == "") {
			continue
		}
		kept = append(kept, it)
	}
	f.Items = kept
	if len(f.Items) == 0 {
		return f, errors.New("a feed with no entries")
	}
	return f, nil
}

func item(guid, link, title, summary, when string) Item {
	link = strings.TrimSpace(link)
	guid = strings.TrimSpace(guid)
	if guid == "" {
		guid = link
	}
	return Item{
		GUID:      clean(guid, 500),
		Link:      clean(link, 1000),
		Title:     clean(title, 300),
		Summary:   clean(summary, 600),
		Published: parseWhen(when),
	}
}

// rootName is the root element's local name, without reading the whole document.
func rootName(body []byte) string {
	d := xml.NewDecoder(bytes.NewReader(body))
	d.Strict = false
	d.CharsetReader = charsetReader
	for {
		tok, err := d.Token()
		if err != nil {
			return ""
		}
		if se, ok := tok.(xml.StartElement); ok {
			return se.Name.Local
		}
	}
}

// charsetReader accepts the labels feeds declare; everything that is not UTF-8 is read as Latin-1
// (a feed that lies about its charset still yields text, with the odd accent wrong).
func charsetReader(label string, in io.Reader) (io.Reader, error) {
	switch strings.ToLower(label) {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		return in, nil
	}
	return latin1{in}, nil
}

type latin1 struct{ r io.Reader }

func (l latin1) Read(p []byte) (int, error) {
	buf := make([]byte, len(p)/2)
	n, err := l.r.Read(buf)
	out := make([]byte, 0, n*2)
	for _, b := range buf[:n] {
		out = utf8.AppendRune(out, rune(b))
	}
	copy(p, out)
	return len(out), err
}

var (
	tagRe   = regexp.MustCompile(`(?s)<[^>]*>`)
	spaceRe = regexp.MustCompile(`\s+`)
)

// clean strips tags and entities, flattens whitespace and caps the length on a rune.
func clean(s string, max int) string {
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = spaceRe.ReplaceAllString(strings.TrimSpace(s), " ")
	if len(s) > max {
		cut := max
		for cut > 0 && cut < len(s) && !utf8.RuneStart(s[cut]) {
			cut--
		}
		s = strings.TrimSpace(s[:cut]) + "…"
	}
	return s
}

var whenLayouts = []string{
	time.RFC1123Z, time.RFC1123, time.RFC822Z, time.RFC822, time.RFC3339, time.RFC3339Nano,
	"Mon, 2 Jan 2006 15:04:05 -0700", "Mon, 2 Jan 2006 15:04:05 MST", "2 Jan 2006 15:04:05 -0700",
	"2006-01-02T15:04:05Z07:00", "2006-01-02T15:04:05", "2006-01-02 15:04:05", "2006-01-02",
}

func parseWhen(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	for _, l := range whenLayouts {
		if t, err := time.Parse(l, s); err == nil {
			return t.UTC()
		}
	}
	return time.Time{}
}

// --- which stories are the same story -----------------------------------------------------

var stop = map[string]bool{
	"the": true, "a": true, "an": true, "of": true, "in": true, "on": true, "at": true, "to": true, "for": true,
	"and": true, "or": true, "as": true, "by": true, "with": true, "from": true, "is": true, "are": true,
	"be": true, "it": true, "its": true, "has": true, "have": true, "after": true, "over": true, "into": true,
	"up": true, "out": true, "new": true, "says": true, "say": true, "said": true, "how": true, "what": true,
	"why": true, "who": true, "will": true, "this": true, "that": true, "amid": true, "live": true, "us": true,
	"uk": true, "s": true, "t": true, "about": true, "than": true, "more": true, "his": true, "her": true,
	"their": true, "not": true, "no": true, "but": true, "he": true, "she": true, "they": true, "we": true,
}

// Tokens is a title's content words, lower case, stop words out, light stemming (plural s,
// possessive 's), so "Minister resigns over leaked memo" and "Leaked memo: the minister's
// resignation" share "minist", "leak", "memo".
func Tokens(title string) []string {
	title = strings.ToLower(title)
	var out []string
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(title, func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsDigit(r) }) {
		w = strings.TrimSuffix(w, "'s")
		if len(w) < 3 || stop[w] {
			continue
		}
		w = stem(w)
		if !seen[w] {
			seen[w] = true
			out = append(out, w)
		}
	}
	sort.Strings(out)
	return out
}

func stem(w string) string {
	for _, suf := range []string{"ations", "ation", "ings", "ing", "ers", "ies", "ed", "es", "s"} {
		if strings.HasSuffix(w, suf) && len(w)-len(suf) >= 4 {
			return w[:len(w)-len(suf)]
		}
	}
	return w
}

// Similar says whether two titles tell the same story: at least half the smaller set's words in
// common, and at least two words.
func Similar(a, b []string) bool {
	if len(a) == 0 || len(b) == 0 {
		return false
	}
	set := map[string]bool{}
	for _, w := range a {
		set[w] = true
	}
	common := 0
	for _, w := range b {
		if set[w] {
			common++
		}
	}
	small := len(a)
	if len(b) < small {
		small = len(b)
	}
	if common < 2 {
		return false
	}
	return common*2 >= small
}
