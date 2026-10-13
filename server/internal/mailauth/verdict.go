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

// Judge runs every check. DNS goes through res; NoResolver turns the DNS verdicts into "not checked".
func Judge(ctx context.Context, res Resolver, in Input) Verdicts {
	var v Verdicts
	v.Screen = ScreenHeaders(in.Raw, in.BoxDomains)
	v.SPF = CheckSPF(ctx, res, in.IP, in.MailFrom, in.Helo)
	v.DKIM = VerifyDKIM(ctx, res, in.Raw, in.Now)
	fromDomain := ""
	if at := strings.LastIndexByte(v.Screen.From, '@'); at >= 0 {
		fromDomain = v.Screen.From[at+1:]
	}
	v.DMARC = CheckDMARC(ctx, res, fromDomain, v.SPF, v.DKIM)
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
	}
	anyPass := false
	for _, k := range v.DKIM {
		if k.Pass() {
			anyPass = true
		}
	}
	if !anyPass {
		if len(v.DKIM) == 0 {
			add(2, "no DKIM signature")
		} else {
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
		case strings.HasPrefix(r, "a bare CR"), strings.HasPrefix(r, "control characters"):
			add(3, r)
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
