package mailauth

import (
	"context"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// modifierRe is a modifier term (name=value) as against a mechanism (name[:arg]).
var modifierRe = regexp.MustCompile(`^([A-Za-z][A-Za-z0-9_.-]*)=`)

// SPFResult is RFC 7208's: none, neutral, pass, fail, softfail, temperror, permerror.
type SPFResult struct {
	Result string `json:"result"`
	// Domain is the identity checked: the MAIL FROM domain, or the HELO name when MAIL FROM was
	// empty (a bounce), which is what DMARC aligns on.
	Domain string `json:"domain"`
	Reason string `json:"reason,omitempty"`
}

// spfMaxLookups is RFC 7208's ten terms that need DNS. The MX hosts behind an mx term and the
// names behind a ptr term are not counted there (the RFC bounds each at ten), so a record of
// ten mx terms could still cost a hundred sequential queries; spfMaxQueries bounds every query
// the evaluation makes, whatever the term.
const (
	spfMaxLookups = 10
	spfMaxQueries = 40
)

type spfEval struct {
	ctx     context.Context
	res     Resolver
	ip      net.IP
	sender  string // the full MAIL FROM address
	helo    string
	lookups int
	queries int
	void    int
}

// query counts one DNS query against spfMaxQueries; false when the budget is spent.
func (e *spfEval) query() bool {
	e.queries++
	return e.queries <= spfMaxQueries
}

// CheckSPF evaluates the sender's SPF for the connecting ip. mailFrom is the envelope sender
// ("" for a bounce), helo the EHLO name.
func CheckSPF(ctx context.Context, res Resolver, ip net.IP, mailFrom, helo string) SPFResult {
	sender := strings.TrimSpace(mailFrom)
	helo = lowerDomain(helo)
	if sender == "" {
		sender = "postmaster@" + helo
	}
	at := strings.LastIndexByte(sender, '@')
	if at < 0 || at == len(sender)-1 {
		return SPFResult{Result: "none", Domain: helo, Reason: "no sender domain"}
	}
	domain := lowerDomain(sender[at+1:])
	if ip == nil {
		return SPFResult{Result: "none", Domain: domain, Reason: "no connecting address noted"}
	}
	e := &spfEval{ctx: ctx, res: res, ip: ip, sender: sender, helo: helo}
	r, reason := e.check(domain)
	return SPFResult{Result: r, Domain: domain, Reason: reason}
}

func (e *spfEval) record(domain string) (string, string, string) {
	if !e.query() {
		return "", "permerror", "too many DNS queries"
	}
	txts, err := e.res.LookupTXT(e.ctx, domain)
	if err != nil {
		if isNotFound(err) {
			return "", "none", "no SPF record"
		}
		if _, off := err.(errNoDNS); off {
			return "", "none", "not checked"
		}
		return "", "temperror", "DNS lookup failed"
	}
	var found []string
	for _, t := range txts {
		if strings.HasPrefix(strings.ToLower(t), "v=spf1 ") || strings.EqualFold(t, "v=spf1") {
			found = append(found, t)
		}
	}
	if len(found) == 0 {
		return "", "none", "no SPF record"
	}
	if len(found) > 1 {
		return "", "permerror", "more than one SPF record"
	}
	return found[0], "", ""
}

// check evaluates one domain's record. Returns the result and a reason.
func (e *spfEval) check(domain string) (string, string) {
	rec, r, reason := e.record(domain)
	if r != "" {
		return r, reason
	}
	terms := strings.Fields(rec)[1:]
	var redirect string
	for _, term := range terms {
		if m := modifierRe.FindStringSubmatch(term); m != nil {
			if strings.EqualFold(m[1], "redirect") {
				redirect = term[len(m[0]):]
			}
			continue
		}
		qual := byte('+')
		if strings.ContainsAny(term[:1], "+-~?") {
			qual = term[0]
			term = term[1:]
		}
		match, res, why := e.mechanism(domain, term)
		if res != "" {
			return res, why
		}
		if match {
			switch qual {
			case '+':
				return "pass", "matched " + term
			case '-':
				return "fail", "matched " + term
			case '~':
				return "softfail", "matched " + term
			case '?':
				return "neutral", "matched " + term
			}
		}
	}
	if redirect != "" {
		if e.lookups++; e.lookups > spfMaxLookups {
			return "permerror", "too many DNS lookups"
		}
		target, err := e.expand(redirect, domain)
		if err != nil {
			return "permerror", err.Error()
		}
		r, why := e.check(target)
		if r == "none" {
			return "permerror", "redirect to a domain with no record"
		}
		return r, why
	}
	return "neutral", "no mechanism matched"
}

// mechanism evaluates one term. Returns matched, or a terminal result with a reason.
func (e *spfEval) mechanism(domain, term string) (bool, string, string) {
	name, arg := term, ""
	if i := strings.IndexAny(term, ":/"); i >= 0 {
		name, arg = term[:i], term[i:]
	}
	name = strings.ToLower(name)
	switch name {
	case "all":
		return true, "", ""
	case "ip4", "ip6":
		if !strings.HasPrefix(arg, ":") {
			return false, "permerror", name + " without an address"
		}
		return e.matchCIDR(arg[1:], name == "ip4"), "", ""
	case "a", "mx", "ptr", "exists", "include":
	default:
		return false, "permerror", "unknown mechanism " + name
	}
	if e.lookups++; e.lookups > spfMaxLookups {
		return false, "permerror", "too many DNS lookups"
	}
	target := domain
	cidr4, cidr6 := 32, 128
	if strings.HasPrefix(arg, ":") {
		arg = arg[1:]
		if s := strings.IndexByte(arg, '/'); s >= 0 {
			target, arg = arg[:s], arg[s:]
		} else {
			target, arg = arg, ""
		}
		t, err := e.expand(target, domain)
		if err != nil {
			return false, "permerror", err.Error()
		}
		target = t
	}
	if strings.HasPrefix(arg, "/") {
		var err error
		cidr4, cidr6, err = parseDualCIDR(arg)
		if err != nil {
			return false, "permerror", err.Error()
		}
	}
	switch name {
	case "include":
		r, _ := e.check(target)
		switch r {
		case "pass":
			return true, "", ""
		case "fail", "softfail", "neutral":
			return false, "", ""
		case "temperror":
			return false, "temperror", "include: lookup failed"
		default:
			return false, "permerror", "include: " + target + " has no usable record"
		}
	case "a":
		ips, err := e.lookupIPs(target)
		if err != nil {
			if err.Error() == "too many DNS queries" {
				return false, "permerror", err.Error()
			}
			return false, err.Error(), "lookup of " + target + " failed"
		}
		return e.anyMatch(ips, cidr4, cidr6), "", ""
	case "mx":
		if !e.query() {
			return false, "permerror", "too many DNS queries"
		}
		mxs, err := e.res.LookupMX(e.ctx, target)
		if err != nil {
			if isNotFound(err) {
				e.void++
				return false, "", ""
			}
			return false, "temperror", "MX lookup failed"
		}
		if len(mxs) > 10 {
			return false, "permerror", "more than ten MX records"
		}
		for _, mx := range mxs {
			ips, err := e.lookupIPs(mx.Host)
			if err != nil && err.Error() == "temperror" {
				return false, "temperror", "MX host lookup failed"
			}
			if err != nil && err.Error() == "too many DNS queries" {
				return false, "permerror", "too many DNS queries"
			}
			if e.anyMatch(ips, cidr4, cidr6) {
				return true, "", ""
			}
		}
		return false, "", ""
	case "ptr":
		if !e.query() {
			return false, "permerror", "too many DNS queries"
		}
		names, err := e.res.LookupAddr(e.ctx, e.ip.String())
		if err != nil {
			return false, "", ""
		}
		if len(names) > 10 {
			names = names[:10] // RFC 7208 section 4.6.4: at most ten names evaluated
		}
		for _, n := range names {
			n = lowerDomain(n)
			if n == target || strings.HasSuffix(n, "."+target) {
				ips, _ := e.lookupIPs(n)
				if e.anyMatch(ips, 32, 128) {
					return true, "", ""
				}
			}
		}
		return false, "", ""
	case "exists":
		if !e.query() {
			return false, "permerror", "too many DNS queries"
		}
		ips, err := e.res.LookupIP(e.ctx, "ip4", target)
		if err != nil {
			if isNotFound(err) {
				return false, "", ""
			}
			return false, "temperror", "exists lookup failed"
		}
		return len(ips) > 0, "", ""
	}
	return false, "", ""
}

func (e *spfEval) lookupIPs(host string) ([]net.IP, error) {
	if !e.query() {
		return nil, fmt.Errorf("too many DNS queries")
	}
	ips, err := e.res.LookupIP(e.ctx, "ip", host)
	if err != nil {
		if isNotFound(err) {
			e.void++
			if e.void > 2 {
				return nil, fmt.Errorf("permerror")
			}
			return nil, nil
		}
		if _, off := err.(errNoDNS); off {
			return nil, nil
		}
		return nil, fmt.Errorf("temperror")
	}
	return ips, nil
}

func (e *spfEval) anyMatch(ips []net.IP, cidr4, cidr6 int) bool {
	for _, ip := range ips {
		if ip4 := ip.To4(); ip4 != nil {
			if e.ip.To4() != nil && cidrContains(ip4, e.ip.To4(), cidr4, 32) {
				return true
			}
		} else if e.ip.To4() == nil && cidrContains(ip.To16(), e.ip.To16(), cidr6, 128) {
			return true
		}
	}
	return false
}

func cidrContains(network, ip net.IP, bits, size int) bool {
	if bits < 0 || bits > size {
		return false
	}
	mask := net.CIDRMask(bits, size)
	return network.Mask(mask).Equal(ip.Mask(mask))
}

func (e *spfEval) matchCIDR(s string, v4 bool) bool {
	if strings.Contains(s, "/") {
		_, n, err := net.ParseCIDR(s)
		if err != nil {
			return false
		}
		return n.Contains(e.ip)
	}
	ip := net.ParseIP(s)
	if ip == nil {
		return false
	}
	return ip.Equal(e.ip)
}

func parseDualCIDR(s string) (int, int, error) {
	c4, c6 := 32, 128
	s = strings.TrimPrefix(s, "/")
	parts := strings.SplitN(s, "//", 2)
	if parts[0] != "" {
		n, err := strconv.Atoi(parts[0])
		if err != nil || n < 0 || n > 32 {
			return 0, 0, fmt.Errorf("bad CIDR length")
		}
		c4 = n
	}
	if len(parts) == 2 {
		n, err := strconv.Atoi(parts[1])
		if err != nil || n < 0 || n > 128 {
			return 0, 0, fmt.Errorf("bad IPv6 CIDR length")
		}
		c6 = n
	}
	return c4, c6, nil
}

// expand applies the macros of RFC 7208 section 7 to a domain-spec. Enough of them for the
// records in the wild: %{s} %{l} %{o} %{d} %{i} %{h} %{v}, with digits and reverse.
func (e *spfEval) expand(spec, domain string) (string, error) {
	if !strings.Contains(spec, "%") {
		return lowerDomain(spec), nil
	}
	var out strings.Builder
	for i := 0; i < len(spec); i++ {
		if spec[i] != '%' {
			out.WriteByte(spec[i])
			continue
		}
		if i+1 >= len(spec) {
			return "", fmt.Errorf("bad macro")
		}
		switch spec[i+1] {
		case '%':
			out.WriteByte('%')
			i++
			continue
		case '_':
			out.WriteByte(' ')
			i++
			continue
		case '-':
			out.WriteString("%20")
			i++
			continue
		case '{':
		default:
			return "", fmt.Errorf("bad macro")
		}
		end := strings.IndexByte(spec[i:], '}')
		if end < 0 {
			return "", fmt.Errorf("unterminated macro")
		}
		body := spec[i+2 : i+end]
		i += end
		if body == "" {
			return "", fmt.Errorf("empty macro")
		}
		letter := body[0] | 0x20
		rest := body[1:]
		var val string
		at := strings.LastIndexByte(e.sender, '@')
		switch letter {
		case 's':
			val = e.sender
		case 'l':
			val = e.sender[:at]
		case 'o':
			val = e.sender[at+1:]
		case 'd':
			val = domain
		case 'i':
			if ip4 := e.ip.To4(); ip4 != nil {
				val = ip4.String()
			} else {
				var sb strings.Builder
				for _, b := range e.ip.To16() {
					sb.WriteString(fmt.Sprintf("%x.%x.", b>>4, b&0xf))
				}
				val = strings.TrimSuffix(sb.String(), ".")
			}
		case 'h':
			val = e.helo
		case 'v':
			if e.ip.To4() != nil {
				val = "in-addr"
			} else {
				val = "ip6"
			}
		case 'p':
			val = "unknown"
		default:
			return "", fmt.Errorf("unknown macro %c", letter)
		}
		// digits, reverse, delimiters
		digits := 0
		reverse := false
		delims := "."
		j := 0
		for j < len(rest) && rest[j] >= '0' && rest[j] <= '9' {
			digits = digits*10 + int(rest[j]-'0')
			j++
		}
		if j < len(rest) && (rest[j] == 'r' || rest[j] == 'R') {
			reverse = true
			j++
		}
		if j < len(rest) {
			delims = rest[j:]
		}
		parts := strings.FieldsFunc(val, func(r rune) bool { return strings.ContainsRune(delims, r) })
		if reverse {
			for a, b := 0, len(parts)-1; a < b; a, b = a+1, b-1 {
				parts[a], parts[b] = parts[b], parts[a]
			}
		}
		if digits > 0 && digits < len(parts) {
			parts = parts[len(parts)-digits:]
		}
		out.WriteString(strings.Join(parts, "."))
	}
	return lowerDomain(out.String()), nil
}
