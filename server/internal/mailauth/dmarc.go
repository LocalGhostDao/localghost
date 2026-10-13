package mailauth

import (
	"context"
	"strings"
)

// DMARCResult says whether the From domain's policy was met by an aligned SPF or DKIM pass.
type DMARCResult struct {
	// Result: pass, fail, none (no policy published), temperror, permerror.
	Result string `json:"result"`
	Domain string `json:"domain"` // the From domain
	Policy string `json:"policy,omitempty"`
	// Aligned names what passed in alignment: "dkim", "spf", "dkim+spf", or "".
	Aligned string `json:"aligned,omitempty"`
	Reason  string `json:"reason,omitempty"`
}

// CheckDMARC looks up the From domain's policy and judges alignment. fromDomain is the domain of
// the RFC 5322 From address; spf and dkim are the verdicts already computed.
func CheckDMARC(ctx context.Context, res Resolver, fromDomain string, spf SPFResult, dkim []DKIMResult) DMARCResult {
	d := lowerDomain(fromDomain)
	r := DMARCResult{Domain: d}
	if d == "" {
		r.Result, r.Reason = "permerror", "no From domain"
		return r
	}
	rec, how := dmarcRecord(ctx, res, d)
	switch how {
	case "temperror":
		r.Result, r.Reason = "temperror", "policy lookup failed"
		return r
	case "off":
		r.Result, r.Reason = "none", "not checked"
		return r
	case "none":
		r.Result, r.Reason = "none", "no policy published"
		// alignment is still worth reporting without a policy
	}
	adkim, aspf := "r", "r"
	if rec != nil {
		r.Policy = strings.ToLower(rec["p"])
		if v := strings.ToLower(rec["adkim"]); v == "s" {
			adkim = "s"
		}
		if v := strings.ToLower(rec["aspf"]); v == "s" {
			aspf = "s"
		}
	}
	var aligned []string
	for _, k := range dkim {
		if k.Pass() && alignedDomains(k.Domain, d, adkim) {
			aligned = append(aligned, "dkim")
			break
		}
	}
	if spf.Result == "pass" && alignedDomains(spf.Domain, d, aspf) {
		aligned = append(aligned, "spf")
	}
	r.Aligned = strings.Join(aligned, "+")
	if rec == nil {
		return r
	}
	if len(aligned) > 0 {
		r.Result = "pass"
	} else {
		r.Result = "fail"
		r.Reason = "nothing that passed is aligned with " + d
	}
	return r
}

func alignedDomains(a, from, mode string) bool {
	a, from = lowerDomain(a), lowerDomain(from)
	if a == "" || from == "" {
		return false
	}
	if mode == "s" {
		return a == from
	}
	return OrgDomain(a) == OrgDomain(from)
}

// dmarcRecord fetches _dmarc.<domain>, then _dmarc.<org domain>. how: "ok", "none",
// "temperror", "off".
func dmarcRecord(ctx context.Context, res Resolver, d string) (map[string]string, string) {
	try := func(name string) (map[string]string, string) {
		txts, err := res.LookupTXT(ctx, "_dmarc."+name)
		if err != nil {
			if isNotFound(err) {
				return nil, "none"
			}
			if _, off := err.(errNoDNS); off {
				return nil, "off"
			}
			return nil, "temperror"
		}
		for _, t := range txts {
			if strings.HasPrefix(strings.ToLower(strings.TrimSpace(t)), "v=dmarc1") {
				return tags(t), "ok"
			}
		}
		return nil, "none"
	}
	rec, how := try(d)
	if how != "none" {
		return rec, how
	}
	if od := OrgDomain(d); od != d {
		return try(od)
	}
	return nil, "none"
}
