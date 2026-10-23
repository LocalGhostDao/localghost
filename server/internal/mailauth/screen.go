package mailauth

import (
	"bytes"
	"mime"
	"net/mail"
	"regexp"
	"strings"
)

// Screen is the structural look at a message's headers: the ways a message lies about where it
// came from, or is built to break a reader, each as one reason. An empty list is "clean".
type Screen struct {
	Reasons []string `json:"reasons,omitempty"`
	// From is the one address in From, once the screen has decided which it is ("" when none).
	From     string `json:"from"`
	FromName string `json:"fromName,omitempty"`
	ReplyTo  string `json:"replyTo,omitempty"`
	Subject  string `json:"subject"`
}

// Clean is true when the screen found nothing.
func (s Screen) Clean() bool { return len(s.Reasons) == 0 }

var (
	controlRe = regexp.MustCompile(`[\x00-\x08\x0b\x0c\x0e-\x1f\x7f]`)
	addrLike  = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)
)

var dec = mime.WordDecoder{}

// ScreenHeaders judges the raw message's header block. boxDomains are the addresses' domains the
// box answers at, so a stranger's display name that reads as one of them is named.
func ScreenHeaders(raw []byte, boxDomains []string) Screen {
	var s Screen
	add := func(r string) { s.Reasons = append(s.Reasons, r) }
	m := parseMessage(raw)
	if m.clipped {
		add("a header block over 128 KB or 2000 lines, cut there")
	}
	// the raw block: a bare CR or LF inside a field is header injection; a line over 998 bytes
	// breaks parsers that stop there
	for _, h := range m.headers {
		body := strings.TrimSuffix(h.raw, "\r\n")
		for _, ln := range strings.Split(body, "\r\n") {
			if len(ln) > 998 {
				add("a header line longer than 998 bytes")
				break
			}
		}
		if strings.Contains(strings.ReplaceAll(body, "\r\n", ""), "\r") || strings.Contains(strings.ReplaceAll(body, "\r\n", ""), "\n") {
			add("a bare CR or LF inside " + strings.TrimSpace(h.name))
		}
	}
	count := map[string]int{}
	for _, h := range m.headers {
		count[strings.ToLower(strings.TrimSpace(h.name))]++
	}
	for _, n := range []string{"from", "date", "subject", "sender", "reply-to", "to", "message-id"} {
		if count[n] > 1 {
			add("more than one " + strings.ToUpper(n[:1]) + n[1:] + " header")
		}
	}
	if count["message-id"] == 0 {
		add("no Message-ID")
	}
	if count["date"] == 0 {
		add("no Date")
	}
	msg, err := mail.ReadMessage(bytes.NewReader(m.bounded))
	if err != nil {
		add("headers do not parse: " + clip(err.Error(), 60))
		return s
	}
	// From
	fromRaw := msg.Header.Get("From")
	if fromRaw == "" {
		add("no From")
	} else {
		addrs, err := mail.ParseAddressList(fromRaw)
		switch {
		case err != nil:
			add("From does not parse")
			if m := addrLike.FindString(fromRaw); m != "" {
				s.From = strings.ToLower(m)
			}
		case len(addrs) == 0:
			add("From has no address")
		default:
			if len(addrs) > 1 {
				add("From has more than one address")
			}
			s.From = strings.ToLower(addrs[0].Address)
			s.FromName = addrs[0].Name
			if d, err := dec.DecodeHeader(addrs[0].Name); err == nil {
				s.FromName = d
			}
			s.FromName = clip(s.FromName, 256)
			name := s.FromName
			if controlRe.MatchString(name) {
				add("control characters in the display name")
			}
			if m := addrLike.FindString(name); m != "" {
				md := strings.ToLower(m[strings.LastIndexByte(m, '@')+1:])
				fd := s.From[strings.LastIndexByte(s.From, '@')+1:]
				if md != fd {
					add("the display name is an address on another domain (" + strings.ToLower(m) + ")")
				}
			}
			for _, bd := range boxDomains {
				bd = lowerDomain(bd)
				if bd != "" && strings.Contains(strings.ToLower(name), "@"+bd) && !strings.HasSuffix(s.From, "@"+bd) {
					add("the display name pretends to be this box")
				}
			}
		}
	}
	// Reply-To on another domain than From
	if rt := msg.Header.Get("Reply-To"); rt != "" {
		if a, err := mail.ParseAddress(rt); err == nil {
			s.ReplyTo = strings.ToLower(a.Address)
			if s.From != "" {
				fd := s.From[strings.LastIndexByte(s.From, '@')+1:]
				rd := s.ReplyTo[strings.LastIndexByte(s.ReplyTo, '@')+1:]
				if OrgDomain(fd) != OrgDomain(rd) {
					add("Reply-To goes to another domain (" + rd + ")")
				}
			}
		} else {
			add("Reply-To does not parse")
		}
	}
	// Subject
	subj := msg.Header.Get("Subject")
	if d, err := dec.DecodeHeader(subj); err == nil {
		subj = d
	}
	s.Subject = clip(subj, 512) // the verdict is stored on the row; the subject's size is the sender's
	if controlRe.MatchString(subj) {
		add("control characters in the subject")
	}
	if letters, upper := countCase(subj); letters >= 12 && upper == letters {
		add("subject all in capitals")
	}
	// Received chain: each hop's "by" should be the next hop's "from" host, loosely
	recv := msg.Header["Received"]
	if len(recv) > 30 {
		add("more than thirty Received lines")
	}
	// Content-Type: a body that is HTML only
	ct := strings.ToLower(msg.Header.Get("Content-Type"))
	if strings.HasPrefix(ct, "text/html") {
		add("the body is HTML only, no text part")
	}
	return s
}

func countCase(s string) (letters, upper int) {
	for _, r := range s {
		switch {
		case r >= 'A' && r <= 'Z':
			letters++
			upper++
		case r >= 'a' && r <= 'z':
			letters++
		}
	}
	return
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
