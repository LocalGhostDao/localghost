package voiced

// THE PEOPLE'S NAMES, SPELT THE PERSON'S WAY. Whisper writes a name as it has heard it written
// most: "Christina" for a Cristina. The box knows better: the people's memories carry the names as
// the person spelt them (and the ones they corrected by hand). So every transcription is told the
// names first (whisper's initial prompt, which tilts it towards those spellings) and read once
// after: a capitalised word in the transcript that is a known name said another way (the same
// sound, or one letter off) becomes the name as the box has it.

import (
	"strings"
	"unicode"
)

// Names are the people's names from the memories (every person's title and other names) and the
// owner's, each as spelt there. Nothing on error: a transcript then stands as whisper wrote it.
func Names(db DB) []string {
	var out []string
	seen := map[string]bool{}
	add := func(n string) {
		n = strings.TrimSpace(n)
		if n == "" || seen[strings.ToLower(n)] {
			return
		}
		seen[strings.ToLower(n)] = true
		out = append(out, n)
	}
	if rows, err := db.Query("SELECT value FROM settings WHERE key = 'owner_name'"); err == nil {
		for _, v := range rows.Vals {
			if len(v) > 0 && v[0] != nil {
				add(*v[0])
			}
		}
	}
	rows, err := db.Query("SELECT title, coalesce(meta->'aliases', '[]'::jsonb)::text FROM memories WHERE kind = 'person' AND NOT tombstoned ORDER BY user_edited DESC, id")
	if err != nil {
		return out
	}
	for _, v := range rows.Vals {
		if len(v) > 0 && v[0] != nil {
			add(*v[0])
		}
		if len(v) > 1 && v[1] != nil {
			// a JSON list of strings, read without a parser: ["Cristina","Cristina Cealicu"]
			for _, a := range strings.Split(strings.Trim(strings.TrimSpace(*v[1]), "[]"), ",") {
				add(strings.Trim(strings.TrimSpace(a), `"`))
			}
		}
	}
	return out
}

// Prompt is whisper's initial prompt from the names: a sentence naming them, the way whisper
// takes a hint about spelling. "" with no names.
func Prompt(names []string) string {
	if len(names) == 0 {
		return ""
	}
	if len(names) > 40 {
		names = names[:40]
	}
	return "People: " + strings.Join(names, ", ") + "."
}

// FixNames reads a transcript once for the people's names said another way and puts the box's
// spelling in: a capitalised word that sounds the same as a name's word (SoundsLike) or is one
// letter off it. Words the person spelt as the box has them are left alone, as is everything that
// is not a name. Returns the text and how many words it changed.
func FixNames(text string, names []string) (string, int) {
	if len(names) == 0 || text == "" {
		return text, 0
	}
	// every word of every name, as spelt: "Cristina Cealicu" gives both
	var words []known
	seen := map[string]bool{}
	for _, n := range names {
		for _, w := range strings.Fields(n) {
			w = strings.Trim(w, ".,;:()")
			if len([]rune(w)) < 3 || seen[strings.ToLower(w)] {
				continue
			}
			seen[strings.ToLower(w)] = true
			words = append(words, known{w, SoundsLike(w)})
		}
	}
	if len(words) == 0 {
		return text, 0
	}
	changed := 0
	var b strings.Builder
	b.Grow(len(text))
	i := 0
	rs := []rune(text)
	for i < len(rs) {
		if !unicode.IsLetter(rs[i]) {
			b.WriteRune(rs[i])
			i++
			continue
		}
		j := i
		for j < len(rs) && (unicode.IsLetter(rs[j]) || rs[j] == '\'' || rs[j] == '’') {
			j++
		}
		word := string(rs[i:j])
		out := word
		if unicode.IsUpper(rs[i]) {
			if fixed, ok := fixWord(word, words); ok {
				out, changed = fixed, changed+1
			}
		}
		b.WriteString(out)
		i = j
	}
	return b.String(), changed
}

// known is one word of a name, as spelt and as it sounds.
type known struct{ word, sound string }

func fixWord(word string, words []known) (string, bool) {
	// a possessive stays: "Christina's" → "Cristina's"
	suffix := ""
	for _, s := range []string{"'s", "’s"} {
		if strings.HasSuffix(word, s) {
			word, suffix = strings.TrimSuffix(word, s), s
			break
		}
	}
	lw := strings.ToLower(word)
	for _, k := range words {
		if strings.EqualFold(word, k.word) {
			if word == k.word {
				return "", false
			}
			return k.word + suffix, true // the name, in the box's case
		}
	}
	if len([]rune(word)) < 4 {
		return "", false
	}
	sound := SoundsLike(word)
	for _, k := range words {
		if sound == k.sound {
			return k.word + suffix, true
		}
		// one letter off, for the longer names only: Tony is not a Toby, Christine may be a Cristina
		if len([]rune(k.word)) >= 6 && levenshtein(lw, strings.ToLower(k.word)) == 1 {
			return k.word + suffix, true
		}
	}
	return "", false
}

// SoundsLike folds the spellings that say the same: lower case, accents off, ch/k/ck/q to c, ph
// to f, y to i, doubled letters single, a trailing e off. Christina, Kristina and Cristina come
// out the same; Christian does not.
func SoundsLike(w string) string {
	w = strings.ToLower(w)
	var b strings.Builder
	for _, r := range w {
		switch r {
		case 'ă', 'â', 'á', 'à', 'ä', 'ã', 'å':
			r = 'a'
		case 'é', 'è', 'ê', 'ë':
			r = 'e'
		case 'í', 'ì', 'î', 'ï':
			r = 'i'
		case 'ó', 'ò', 'ô', 'ö', 'õ':
			r = 'o'
		case 'ú', 'ù', 'û', 'ü':
			r = 'u'
		case 'ș', 'ş', 'ß':
			r = 's'
		case 'ț', 'ţ':
			r = 't'
		case 'ñ':
			r = 'n'
		case 'ç':
			r = 'c'
		}
		b.WriteRune(r)
	}
	s := b.String()
	for _, p := range [][2]string{{"ch", "c"}, {"ck", "c"}, {"ph", "f"}, {"th", "t"}, {"k", "c"}, {"q", "c"}, {"y", "i"}, {"w", "v"}, {"ai", "a"}} {
		s = strings.ReplaceAll(s, p[0], p[1])
	}
	var out []rune
	for _, r := range s {
		if len(out) > 0 && out[len(out)-1] == r {
			continue
		}
		out = append(out, r)
	}
	if len(out) > 3 && out[len(out)-1] == 'e' {
		out = out[:len(out)-1]
	}
	return string(out)
}

func levenshtein(a, b string) int {
	ra, rb := []rune(a), []rune(b)
	if len(ra) == 0 {
		return len(rb)
	}
	if len(rb) == 0 {
		return len(ra)
	}
	prev := make([]int, len(rb)+1)
	cur := make([]int, len(rb)+1)
	for j := range prev {
		prev[j] = j
	}
	for i := 1; i <= len(ra); i++ {
		cur[0] = i
		for j := 1; j <= len(rb); j++ {
			cost := 1
			if ra[i-1] == rb[j-1] {
				cost = 0
			}
			cur[j] = min(prev[j]+1, cur[j-1]+1, prev[j-1]+cost)
		}
		prev, cur = cur, prev
	}
	return prev[len(rb)]
}
