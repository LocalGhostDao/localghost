package main

import (
	"context"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/LocalGhostDao/localghost/server/internal/mailauth"
)

// What the row shows is what was judged (audit run 2). An address that is not plain is never
// shown and never clipped into another address; names and subjects are one line with nothing
// hidden; fields are text the database takes.
func TestTheShownSenderIsTheJudgedOne(t *testing.T) {
	head := "To: agent@lgs.example\r\nDate: Mon, 09 Nov 2026 10:00:00 +0000\r\nMessage-ID: <m1@sender.example>\r\n"
	long := "From: \"ceo@bank.example" + strings.Repeat(" ", 240) + "\"@sender.example\r\n" + head + "Subject: Friday\r\n\r\nhi\r\n"
	sc := mailauth.ScreenHeaders([]byte(long), []string{"lgs.example"})
	if !strings.HasSuffix(sc.From, "@sender.example") {
		t.Fatalf("the judge lost the real domain: %q", sc.From)
	}
	from, _, _, _ := sc.Shown()
	if from != "" {
		t.Fatalf("an address that is not plain is shown: %q", from)
	}
	found := false
	for _, r := range sc.Reasons {
		found = found || r == "the From address is not a plain address"
	}
	if !found {
		t.Fatalf("no reason named: %v", sc.Reasons)
	}
	v := mailauth.Judge(context.Background(), mailauth.NoResolver{}, mailauth.Input{Raw: []byte(long), BoxDomains: []string{"lgs.example"}, Now: time.Now()})
	if v.Spam == "low" {
		t.Fatalf("judged low: %v", v.SpamReasons)
	}
	if p, _ := parseMail([]byte(long)); p.From != "" {
		t.Fatalf("parseMail keeps a clipped address: %q", p.From)
	}
	// Cc and To: left out, never clipped into a third party's address
	cc := "From: a@sender.example\r\nCc: \"victim@corp.example" + strings.Repeat(" ", 240) + "\"@sender.example, ok@corp.example\r\n" + head + "Subject: s\r\n\r\nhi\r\n"
	if p, _ := parseMail([]byte(cc)); len(p.Cc) != 1 || p.Cc[0] != "ok@corp.example" {
		t.Fatalf("Cc: %q", p.Cc)
	}
	// a name and a subject with line breaks and direction overrides: one line, nothing hidden, named
	odd := "From: =?utf-8?q?Pay=0A=0APal_=E2=80=AEten?= <x@sender.example>\r\n" + head + "Subject: =?utf-8?q?Invoice=0Afrom_the_bank=E2=80=AE?=\r\n\r\nhi\r\n"
	sc = mailauth.ScreenHeaders([]byte(odd), nil)
	_, name, _, subj := sc.Shown()
	if strings.ContainsAny(name+subj, "\r\n‮") || name != "Pay Pal ten" || subj != "Invoice from the bank" {
		t.Fatalf("name %q subject %q", name, subj)
	}
	joined := strings.Join(sc.Reasons, "; ")
	for _, want := range []string{"control characters in the display name", "hidden characters in the display name", "control characters in the subject", "hidden characters in the subject"} {
		if !strings.Contains(joined, want) {
			t.Fatalf("reasons lack %q: %v", want, sc.Reasons)
		}
	}
	// a name in a charset the old screen could not read is read by the one decoder
	cp := "From: =?windows-1252?q?service=40paypal.com_=96_billing?= <x@sender.example>\r\n" + head + "Subject: s\r\n\r\nhi\r\n"
	sc = mailauth.ScreenHeaders([]byte(cp), nil)
	if !strings.Contains(strings.Join(sc.Reasons, "; "), "the display name is an address on another domain") {
		t.Fatalf("the screen did not read the name: %q %v", sc.FromName, sc.Reasons)
	}
	// a From that does not parse: the guess is the last address, and it is not shown
	bad := "From: service@bank.example via <<x@sender.example\r\n" + head + "Subject: s\r\n\r\nhi\r\n"
	sc = mailauth.ScreenHeaders([]byte(bad), nil)
	if from, _, _, _ := sc.Shown(); from != "" || sc.From != "x@sender.example" || !sc.Unsure {
		t.Fatalf("unparsed From: shown %q, judged %q, unsure %v", from, sc.From, sc.Unsure)
	}
	// plain addresses
	for a, want := range map[string]bool{"paul@example.org": true, "a.b+c@sub.example.co.uk": true, "josé@exämple.org": true,
		"a b@example.org": false, "a@b": false, "\"a\"@example.org": false, "a@@example.org": false, "a@example.org‮": false,
		"a​@example.org": false, strings.Repeat("a", 250) + "@example.org": false, "<a@example.org>": false, "": false, ".a@example.org": false} {
		if mailauth.PlainAddress(a) != want {
			t.Errorf("PlainAddress(%q) = %v", a, !want)
		}
	}
}

func TestMailFieldsAreTextTheDatabaseTakes(t *testing.T) {
	raw := "From: a@sender.example\r\nTo: agent@lgs.example\r\nSubject: s\r\nMessage-ID: <m\x00\xff1@sender.example>\r\nReferences: <r\x001@x> <r2@x>\r\n" +
		"Content-Type: text/plain; charset=utf-8\r\n\r\nline one \x00 and \xff\xfe bytes\r\n" + strings.Repeat("é", maxBodyText) + "\r\n"
	p, err := parseMail([]byte(raw))
	if err != nil {
		t.Fatal(err)
	}
	fields := append([]string{p.MessageID, p.InReplyTo, p.From, p.FromName, p.Subject, p.Body}, p.References...)
	for _, f := range fields {
		if !utf8.ValidString(f) || strings.IndexByte(f, 0) >= 0 {
			t.Fatalf("a field the insert would refuse: %q", clipField(f, 60))
		}
	}
	if !strings.HasSuffix(p.Body, "[cut at 512 KB]") {
		t.Fatal("the body was not cut")
	}
}

// One long paragraph costs its length, not its square, and a short line stays the author's.
func TestUnwrapIsLinearAndStopsAtAShortLine(t *testing.T) {
	line := "the quick brown fox jumps over the lazy dog and keeps going until column 72"
	var b strings.Builder
	for i := 0; i < 20000; i++ {
		b.WriteString(line)
		b.WriteByte('\n')
	}
	big := b.String() // 1.5 MB, one hard-wrapped paragraph
	start := time.Now()
	out := unwrap(big)
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("unwrap took %v for %d bytes", took, len(big))
	}
	if strings.Count(out, "\n") > 1 {
		t.Fatalf("not joined: %d lines", strings.Count(out, "\n"))
	}
	start = time.Now()
	flowed := strings.ReplaceAll(big, "\n", " \n")
	if out := unflow(flowed, false); strings.Count(out, "\n") > 1 {
		t.Fatalf("flowed text not joined: %d lines", strings.Count(out, "\n"))
	}
	if took := time.Since(start); took > 3*time.Second {
		t.Fatalf("unflow took %v", took)
	}
	// a wrapped paragraph, then a short last line, then lines the author kept short on purpose
	text := "This is a paragraph that the sending client wrapped at seventy-two columns\n" +
		"because that is what it always does with plain text, and the last line is\n" +
		"short.\n" +
		"Regards\n" +
		"Paul"
	got := unwrap(text)
	want := "This is a paragraph that the sending client wrapped at seventy-two columns because that is what it always does with plain text, and the last line is short.\nRegards\nPaul"
	if got != want {
		t.Fatalf("got:\n%s\nwant:\n%s", got, want)
	}
}

func TestAttachmentsThatAreNotKept(t *testing.T) {
	var atts []parsedAttach
	var total int64
	addAttach(&atts, &total, "report.pdf/", "text/html", []byte("<html><script>x</script></html>"))
	if atts[0].Name != "report.pdf" || atts[0].Mime != "application/pdf" {
		t.Fatalf("named %q typed %q", atts[0].Name, atts[0].Mime)
	}
	for name, mt := range map[string]string{"invoice.html": "text/html", "logo.svg": "image/svg+xml", "page.xhtml": "application/octet-stream", "x.bin": "text/html", "form.HTM": ""} {
		if why := dangerous(safeName(name), mt, []byte("<html>")); why == "" {
			t.Errorf("%s (%s) would be kept", name, mt)
		}
	}
	if why := dangerous("notes.txt", "text/plain", []byte("hello")); why != "" {
		t.Fatalf("a text file is refused: %s", why)
	}
}
