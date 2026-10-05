package wiki

import (
	"html"
	"regexp"
	"sort"
	"strings"
	"unicode"
)

// THE REST OF AN ARTICLE. The lead answers "what is X"; "when was X built" or "how tall is X" is
// in a section. Sections splits a page at its headings (the Kiwix build wraps each in <details>
// with an <h2> or <h3> in a <summary>; an older build has bare headings), keeps each section's
// paragraphs as plain text, and BestSection picks the one a question's words point at.

// Section is one heading and the text under it.
type Section struct {
	Heading string
	Text    string
}

var (
	reHeading = regexp.MustCompile(`(?is)<h([23])\b[^>]*>(.*?)</h[23]>`)
	reSkipSec = regexp.MustCompile(`(?i)^(see also|references|external links|notes|further reading|bibliography|sources|citations|footnotes|gallery)$`)
)

// Sections is the article's sections after the lead, each cut to maxPer characters of text; the
// bookkeeping sections (references, see also, external links) are left out.
func Sections(page string, maxPer int) []Section {
	if i := strings.Index(strings.ToLower(page), "<body"); i >= 0 {
		page = page[i:]
	}
	page = reStyle.ReplaceAllString(page, " ")
	page = dropElement(page, "table")
	page = reRefSup.ReplaceAllString(page, "")
	locs := reHeading.FindAllStringSubmatchIndex(page, -1)
	var out []Section
	for n, loc := range locs {
		heading := cleanText(page[loc[4]:loc[5]])
		if heading == "" || reSkipSec.MatchString(heading) {
			continue
		}
		end := len(page)
		if n+1 < len(locs) {
			end = locs[n+1][0]
		}
		body := page[loc[1]:end]
		var paras []string
		size := 0
		for _, m := range rePara.FindAllStringSubmatch(body, -1) {
			t := cleanText(m[1])
			if len(t) < 20 {
				continue
			}
			paras = append(paras, t)
			size += len(t) + 1
			if maxPer > 0 && size >= maxPer {
				break
			}
		}
		if len(paras) == 0 {
			continue
		}
		text := strings.Join(paras, "\n")
		if maxPer > 0 && len(text) > maxPer {
			text = cutAt(text, maxPer)
		}
		out = append(out, Section{Heading: heading, Text: text})
	}
	return out
}

func cleanText(s string) string {
	t := html.UnescapeString(reTag.ReplaceAllString(s, ""))
	t = reBrackets.ReplaceAllString(t, "")
	t = strings.TrimSpace(reSpace.ReplaceAllString(t, " "))
	return reSpaceDot.ReplaceAllString(t, "$1")
}

// cutAt cuts text to max characters, after a full stop when one is past the middle.
func cutAt(text string, max int) string {
	if len(text) <= max {
		return text
	}
	cut := text[:max]
	if i := strings.LastIndex(cut, ". "); i > max/2 {
		return cut[:i+1]
	}
	for len(cut) > 0 && !utf8Valid(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut + "…"
}

func utf8Valid(s string) bool {
	for _, r := range s {
		if r == unicode.ReplacementChar {
			return false
		}
	}
	return true
}

// stop words a question's match ignores
var stopWords = map[string]bool{"what": true, "when": true, "where": true, "which": true, "who": true, "how": true, "does": true, "did": true, "was": true,
	"were": true, "the": true, "and": true, "for": true, "with": true, "from": true, "that": true, "this": true, "about": true, "tell": true, "have": true,
	"has": true, "had": true, "are": true, "its": true, "their": true, "there": true, "they": true, "them": true, "some": true, "into": true, "than": true}

// BestSection is the section a question points at: the one whose heading and text share the most
// of the question's words (four letters and longer, stop words out), the heading counting double;
// nil when no section shares two words (one, for a question with one word to give). The lead is
// not a section; the caller has it already.
func BestSection(secs []Section, question string) *Section {
	var words []string
	for _, w := range strings.FieldsFunc(strings.ToLower(question), func(r rune) bool { return !unicode.IsLetter(r) && !unicode.IsNumber(r) }) {
		if len(w) >= 4 && !stopWords[w] {
			words = append(words, w)
		}
	}
	if len(words) == 0 {
		return nil
	}
	// two words in common at least, or the one word when the question has only one to give
	best, bestScore := -1, 1
	if len(words) == 1 {
		bestScore = 0
	}
	for i, s := range secs {
		h, t := strings.ToLower(s.Heading), strings.ToLower(s.Text)
		score := 0
		for _, w := range words {
			if strings.Contains(h, w) {
				score += 2
			}
			if strings.Contains(t, w) {
				score++
			}
		}
		if score > bestScore {
			best, bestScore = i, score
		}
	}
	if best < 0 {
		return nil
	}
	return &secs[best]
}

// Names are the capitalised runs in a question, the likeliest things to look up when the question
// has no "what is X" shape ("How tall is the Eiffel Tower" gives "Eiffel Tower"): two or more
// letters, the first word of the question left out when it is an ordinary word, longest first.
func Names(question string) []string {
	words := strings.Fields(strings.Trim(question, " ?!."))
	var out []string
	var run []string
	flush := func() {
		if len(run) > 0 {
			out = append(out, strings.Join(run, " "))
		}
		run = nil
	}
	for i, w := range words {
		t := strings.Trim(w, ",.;:!?\"'()“”’")
		if i == 0 && len(words) > 1 && isCommonStart(t) {
			continue // "How", "Tell", "Is": the question's first word, not a name
		}
		r := []rune(t)
		if len(r) >= 2 && unicode.IsUpper(r[0]) {
			run = append(run, t)
			if strings.ContainsAny(w, ",.;:!?") {
				flush()
			}
			continue
		}
		flush()
	}
	flush()
	sort.SliceStable(out, func(a, b int) bool { return len(out[a]) > len(out[b]) })
	return out
}

// isCommonStart says whether a question's first word is an ordinary word capitalised ("How",
// "Is", "Tell"), not a name.
func isCommonStart(w string) bool {
	switch strings.ToLower(w) {
	case "how", "is", "are", "was", "were", "do", "does", "did", "can", "could", "would", "should", "tell", "what", "when", "where", "who", "why", "which", "give", "show", "find", "explain", "describe", "any", "please", "the", "a", "an", "in", "on", "at", "about":
		return true
	}
	return false
}
