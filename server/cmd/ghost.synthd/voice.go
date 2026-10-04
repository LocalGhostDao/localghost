package main

// THE VOICE. The box writes the way the site writes (the web repo's WRITING GUIDELINES, October
// 2026): plain, direct, hedged where unsure and flat where sure, contractions, parentheticals, no
// em dashes, no colons in sentences, no polish words, no closing verdict, no "hope this helps".
// Two halves. voiceRules is the part a model can follow, short enough to ride with every chat
// question (and the day stories' and the brief's prompts carry their own, longer-standing
// versions of it). plainDashes and voiceFilter are the part a model will not follow: em dashes
// come out of a 12B model whatever it is told, so they are turned into commas on the way out,
// in the stream token by token (an em dash can arrive as its own token, its spaces in the
// neighbours) and in every stored answer.

import "strings"

// voiceRules rides with every chat question, after who the box is.
const voiceRules = `How you write. Plainly and to the point, the way someone who built this would say it, not an essay. ` +
	`Say what you know flat; say "I think" or "I'm not sure" when you are not, and say when the box does not have something rather than fill it in. ` +
	`Use contractions. A short aside in brackets is fine. ` +
	`No em dashes (a comma or a full stop instead), no colons inside sentences, no bullet points or headings unless asked for a list, no bold, no exclamation marks. ` +
	`Never "it's worth noting", "in today's", "at the end of the day", "the honest answer is", "needless to say", "delve", "leverage", "harness", "game-changer", "deep dive", nor "actually", "obviously" or "exactly" for emphasis, nor three near-synonyms in a row, nor "it's not X, it's Y". ` +
	`Do not praise the question, do not summarise what you just said, do not end on a verdict or "hope this helps"; when something is open, say what is open.`

// plainDashes turns em dashes, and en dashes used as dashes, into commas: " — " and "word—word"
// become ", "; "9–16" (a range) is left alone.
func plainDashes(s string) string {
	if !strings.ContainsAny(s, "—–") {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	rs := []rune(s)
	for i := 0; i < len(rs); i++ {
		r := rs[i]
		// an en dash counts only between spaces ("10 – 20 km"); "9–16°C" is a range and stays
		isDash := r == '—' || (r == '–' && i > 0 && rs[i-1] == ' ' && i+1 < len(rs) && rs[i+1] == ' ')
		if !isDash {
			b.WriteRune(r)
			continue
		}
		// drop the space before, write the comma, and one space after unless one follows
		out := strings.TrimRight(b.String(), " ")
		b.Reset()
		b.WriteString(out)
		b.WriteString(", ")
		for i+1 < len(rs) && rs[i+1] == ' ' {
			i++
		}
	}
	return b.String()
}

// voiceFilter is plainDashes over a stream: a token may end in a space that a dash in the next
// token should eat, and a token may end in a dash whose spacing the next token decides.
type voiceFilter struct {
	heldSpace bool // the last token ended in a space, held back in case a dash follows
	needSpace bool // the last token ended in a dash (now a comma): the next word wants a space
}

func (f *voiceFilter) pass(s string) string {
	if s == "" {
		return ""
	}
	if f.heldSpace {
		s = " " + s
		f.heldSpace = false
	}
	if f.needSpace {
		if s[0] != ' ' && s[0] != '\n' {
			s = " " + s
		}
		f.needSpace = false
	}
	if strings.HasSuffix(s, " –") { // an en dash after a space at the seam: a dash, the same as an em dash
		s = strings.TrimSuffix(s, " –") + "—"
	}
	endsDash := strings.HasSuffix(s, "—")
	s = plainDashes(s)
	if endsDash {
		s = strings.TrimRight(s, " ")
		f.needSpace = true
	} else if strings.HasSuffix(s, " ") && !strings.HasSuffix(s, "\n ") {
		s = s[:len(s)-1]
		f.heldSpace = true
	}
	return s
}

// flush is what the filter still holds when the stream ends.
func (f *voiceFilter) flush() string {
	if f.heldSpace {
		f.heldSpace = false
		return " "
	}
	return ""
}
