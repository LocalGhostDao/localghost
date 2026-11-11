package mailauth

// WHAT IS SHOWN IS WHAT WAS JUDGED.
//
// The verdicts speak about one sender address, one display name and one subject. Until the
// second audit (10 Nov 2026) the row the phone shows took those from a second parse of the
// message, by other rules: it clipped a long address to 254 bytes (so an address built as
// "ceo@bank.example<240 spaces>"@sender.example was judged as sender.example's, passed, and
// was shown as ceo@bank.example), it decoded names in charsets the screen could not read, and
// it read past the point where the screen had stopped. Now there is one reading, here, and
// the row carries it.

import (
	"io"
	"mime"
	"net/mail"
	"strings"
	"unicode"
	"unicode/utf8"
)

// MaxAddress is the longest address kept (RFC 5321's path limit, less the brackets).
const MaxAddress = 254

// PlainAddress reports whether a is an address a person can read for what it is: one local
// part, one @, a domain with a dot, no spaces, quotes, brackets, separators, controls or
// invisible characters, and no longer than MaxAddress. Quoted local parts are legal mail and
// are refused here on purpose: nothing genuine needs one, and they exist to be misread.
func PlainAddress(a string) bool {
	if a == "" || len(a) > MaxAddress || !utf8.ValidString(a) {
		return false
	}
	at := strings.LastIndexByte(a, '@')
	if at <= 0 || at == len(a)-1 || strings.Count(a, "@") != 1 {
		return false
	}
	local, domain := a[:at], a[at+1:]
	for _, r := range local {
		switch {
		case r > 0x7f:
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r) {
				return false
			}
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9':
		case strings.ContainsRune(".!#$%&'*+/=?^_`{|}~-", r):
		default:
			return false
		}
	}
	if strings.HasPrefix(local, ".") || strings.HasSuffix(local, ".") || strings.Contains(local, "..") {
		return false
	}
	if !strings.Contains(domain, ".") || strings.HasPrefix(domain, ".") || strings.HasSuffix(domain, ".") || strings.Contains(domain, "..") {
		return false
	}
	for _, r := range domain {
		switch {
		case r > 0x7f:
			if !unicode.IsLetter(r) && !unicode.IsDigit(r) && !unicode.IsMark(r) {
				return false
			}
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '-', r == '.':
		default:
			return false
		}
	}
	return true
}

// hidden reports a character that changes how the text around it reads without showing:
// the direction overrides and isolates, the marks, the zero-width space and joiner-less
// invisibles, the byte order mark, and the tag block. The zero-width joiner and non-joiner
// stay (emoji and several scripts need them).
func hidden(r rune) bool {
	switch {
	case r == 0x200c || r == 0x200d:
		return false
	case r >= 0x202a && r <= 0x202e, r >= 0x2066 && r <= 0x2069, r == 0x200e, r == 0x200f, r == 0x061c:
		return true
	case r == 0x200b, r == 0x2060, r == 0xfeff, r == 0x00ad, r == 0x180e, r >= 0xe0000 && r <= 0xe007f:
		return true
	}
	return unicode.Is(unicode.Cf, r)
}

// HasHidden reports whether s carries a hidden character (see hidden).
func HasHidden(s string) bool {
	for _, r := range s {
		if hidden(r) {
			return true
		}
	}
	return false
}

// OneLine is s as one line a list can show: every control character and line or paragraph
// separator becomes a space, hidden characters go, runs of space collapse, the ends are
// trimmed, and bytes that are not UTF-8 are dropped.
func OneLine(s string) string {
	var b strings.Builder
	b.Grow(len(s))
	space := true // leading space is dropped
	for _, r := range strings.ToValidUTF8(s, "") {
		switch {
		case hidden(r):
			continue
		case unicode.IsControl(r), unicode.IsSpace(r), r == 0x2028, r == 0x2029:
			if !space {
				b.WriteByte(' ')
				space = true
			}
		default:
			b.WriteRune(r)
			space = false
		}
	}
	return strings.TrimRight(b.String(), " ")
}

// clipRunes cuts s to at most n bytes on a character.
func clipRunes(s string, n int) string {
	if len(s) <= n {
		return s
	}
	for n > 0 && !utf8.RuneStart(s[n]) {
		n--
	}
	return s[:n]
}

var words = mime.WordDecoder{CharsetReader: func(charset string, input io.Reader) (io.Reader, error) {
	b, err := io.ReadAll(io.LimitReader(input, 64<<10))
	if err != nil {
		return nil, err
	}
	return strings.NewReader(ToUTF8(b, charset)), nil
}}

// ParseAddressList is net/mail's, with this package's decoder for display names: the standard
// one refuses a name in a charset it does not know, and the whole header then "does not parse".
func ParseAddressList(s string) ([]*mail.Address, error) {
	p := mail.AddressParser{WordDecoder: &words}
	return p.ParseList(s)
}

// ParseAddress is ParseAddressList for a header that holds one address.
func ParseAddress(s string) (*mail.Address, error) {
	p := mail.AddressParser{WordDecoder: &words}
	return p.Parse(s)
}

// DecodeHeader decodes the encoded words of a header value, in the charsets mail still arrives
// in. It is the one decoder: the screen judges what it returns and the row shows the same.
func DecodeHeader(s string) string {
	if d, err := words.DecodeHeader(s); err == nil {
		return d
	}
	return s
}

// ToUTF8 turns bytes in a named charset into UTF-8, without a table package: UTF-8 and ASCII
// as they are when valid, everything else as Windows-1252 (a superset of Latin-1 in print).
func ToUTF8(b []byte, charset string) string {
	switch strings.ToLower(strings.TrimSpace(charset)) {
	case "iso-8859-1", "latin1", "iso8859-1", "iso-8859-15", "windows-1252", "cp1252":
		return latin1(b)
	}
	if utf8.Valid(b) {
		return string(b)
	}
	return latin1(b)
}

var cp1252 = map[byte]rune{0x80: '€', 0x82: '‚', 0x83: 'ƒ', 0x84: '„', 0x85: '…', 0x86: '†', 0x87: '‡', 0x88: 'ˆ', 0x89: '‰', 0x8a: 'Š', 0x8b: '‹', 0x8c: 'Œ', 0x8e: 'Ž', 0x91: '‘', 0x92: '’', 0x93: '“', 0x94: '”', 0x95: '•', 0x96: '–', 0x97: '—', 0x98: '˜', 0x99: '™', 0x9a: 'š', 0x9b: '›', 0x9c: 'œ', 0x9e: 'ž', 0x9f: 'Ÿ'}

func latin1(b []byte) string {
	var sb strings.Builder
	for _, c := range b {
		if r, ok := cp1252[c]; ok {
			sb.WriteRune(r)
		} else {
			sb.WriteRune(rune(c))
		}
	}
	return sb.String()
}
