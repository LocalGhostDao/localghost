package mailauth

import (
	"context"
	"net"
	"strings"
	"time"
)

// Verdicts is everything the box concluded about one received message, the shape stored on the
// row and shown on the notification.
type Verdicts struct {
	SPF   SPFResult    `json:"spf"`
	DKIM  []DKIMResult `json:"dkim"`
	DMARC DMARCResult  `json:"dmarc"`
	// Reverse is the relay's PTR note: ok, none, mismatch, error, with the name when ok.
	Reverse     string `json:"reverse"`
	ReverseName string `json:"reverseName,omitempty"`
	HeloMatch   bool   `json:"heloMatch"`
	Screen      Screen `json:"screen"`
	// Spam: low, medium, high, with the reasons that moved it.
	Spam        string   `json:"spam"`
	SpamReasons []string `json:"spamReasons,omitempty"`
}

// Input is what the verifiers need: the message, the relay's note of the connection, and the
// box's own history of the sender.
type Input struct {
	Raw        []byte
	IP         net.IP
	Helo       string
	MailFrom   string
	PTRState   string
	PTR        string
	BoxDomains []string
	// SeenBefore is how many messages the box has from this From address already; FirstFromDomain
	// says whether the From domain is new to the box.
	SeenBefore      int
	FirstFromDomain bool
	Now             time.Time
}

// The shares of one message's judging (the caller's own deadline still bounds the whole).
var (
	stageSPF   = 20 * time.Second
	stageDKIM  = 20 * time.Second
	stageDMARC = 15 * time.Second
)

// Judge runs every check. DNS goes through res; NoResolver turns the DNS verdicts into "not checked".
func Judge(ctx context.Context, res Resolver, in Input) Verdicts {
	var v Verdicts
	v.Screen = ScreenHeaders(in.Raw, in.BoxDomains)
	// Each check has its own share of the caller's time. With one shared deadline, a sender
	// whose SPF record walks slow name servers used all of it, the policy lookup that came last
	// was starved, and a DMARC that could not be read counted for nothing (audit run 2).
	stage := func(share time.Duration) (context.Context, context.CancelFunc) {
		return context.WithTimeout(ctx, share)
	}
	sctx, cancel := stage(stageSPF)
	v.SPF = CheckSPF(sctx, res, in.IP, in.MailFrom, in.Helo)
	cancel()
	kctx, cancel := stage(stageDKIM)
	v.DKIM = VerifyDKIM(kctx, res, in.Raw, in.Now)
	cancel()
	fromDomain := ""
	if at := strings.LastIndexByte(v.Screen.From, '@'); at >= 0 {
		fromDomain = v.Screen.From[at+1:]
	}
	dctx, cancel := stage(stageDMARC)
	v.DMARC = CheckDMARC(dctx, res, fromDomain, v.SPF, v.DKIM)
	cancel()
	v.Reverse = in.PTRState
	if v.Reverse == "" {
		v.Reverse = "error"
	}
	v.ReverseName = in.PTR
	if in.PTR != "" && lowerDomain(in.Helo) == lowerDomain(in.PTR) {
		v.HeloMatch = true
	}
	v.Spam, v.SpamReasons = score(v, in)
	return v
}

// score is the spam judgement: what the verdicts and the box's own history say, no model.
func score(v Verdicts, in Input) (string, []string) {
	points := 0
	var why []string
	add := func(p int, r string) {
		points += p
		why = append(why, r)
	}
	switch v.SPF.Result {
	case "fail":
		add(3, "SPF fail")
	case "softfail":
		add(2, "SPF softfail")
	case "none":
		if v.SPF.Reason != "not checked" {
			add(1, "no SPF record")
		}
	case "permerror":
		add(1, "SPF record broken")
	case "temperror":
		add(1, "SPF could not be checked")
	}
	anyPass, partial := false, false
	for _, k := range v.DKIM {
		if k.Pass() {
			anyPass = true
		}
		if k.Partial && k.Result == "pass" {
			partial = true
		}
	}
	if !anyPass {
		switch {
		case len(v.DKIM) == 0:
			add(2, "no DKIM signature")
		case partial:
			add(3, "DKIM signs only part of the body")
		default:
			add(3, "DKIM fails")
		}
	}
	switch v.DMARC.Result {
	case "fail":
		if v.DMARC.Policy == "reject" || v.DMARC.Policy == "quarantine" {
			add(4, "DMARC fail under a "+v.DMARC.Policy+" policy")
		} else {
			add(2, "DMARC fail")
		}
	case "temperror":
		// the policy could not be read: not a pass, and not nothing
		add(2, "DMARC could not be checked")
	case "permerror":
		add(2, "DMARC cannot be judged: "+v.DMARC.Reason)
	case "none":
		// no policy, and nothing that passed is the From domain's: the message may be fine, but
		// nothing in it proves who it is from, and "low" must not read as "genuine"
		if v.DMARC.Aligned == "" && v.DMARC.Reason != "not checked" {
			add(2, "nothing proves the From domain")
		}
	}
	switch v.Reverse {
	case "none":
		add(2, "no reverse DNS")
	case "mismatch":
		add(2, "reverse DNS does not match")
	}
	for _, r := range v.Screen.Reasons {
		switch {
		case strings.HasPrefix(r, "the display name"):
			add(3, r)
		case strings.HasPrefix(r, "Reply-To goes"):
			add(2, r)
		case strings.HasPrefix(r, "a bare CR"), strings.HasPrefix(r, "control characters"), strings.HasPrefix(r, "hidden characters"):
			add(3, r)
		case r == "the From address is not a plain address":
			add(4, r)
		case strings.HasPrefix(r, "more than one Content-"):
			add(2, r)
		case strings.HasPrefix(r, "more than one From"):
			add(3, r)
		case r == "subject all in capitals":
			add(1, r)
		case r == "no Message-ID", r == "no Date":
			add(1, r)
		default:
			add(1, r)
		}
	}
	if in.SeenBefore == 0 {
		add(1, "first mail from this sender")
	}
	switch {
	case points >= 6:
		return "high", why
	case points >= 3:
		return "medium", why
	}
	return "low", why
}

// Line is the one-line account for the notification: the verdicts in the order a reader wants.
func (v Verdicts) Line() string {
	var parts []string
	parts = append(parts, "SPF "+v.SPF.Result)
	dk := "DKIM none"
	for _, k := range v.DKIM {
		if k.Pass() {
			dk = "DKIM pass by " + k.Domain
			break
		}
		dk = "DKIM " + k.Result
	}
	parts = append(parts, dk)
	switch v.DMARC.Result {
	case "pass":
		parts = append(parts, "DMARC aligned")
	case "fail":
		parts = append(parts, "DMARC not aligned")
	case "none":
		if v.DMARC.Aligned != "" {
			parts = append(parts, "aligned, no DMARC policy")
		} else {
			parts = append(parts, "no DMARC policy")
		}
	default:
		parts = append(parts, "DMARC "+v.DMARC.Result)
	}
	switch v.Reverse {
	case "ok":
		parts = append(parts, "reverse ok")
	case "none":
		parts = append(parts, "no reverse DNS")
	case "mismatch":
		parts = append(parts, "reverse mismatch")
	}
	if !v.Screen.Clean() {
		parts = append(parts, "headers: "+strings.Join(v.Screen.Reasons, ", "))
	}
	parts = append(parts, "spam "+v.Spam)
	return strings.Join(parts, " · ")
}
