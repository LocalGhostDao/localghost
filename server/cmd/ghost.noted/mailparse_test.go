package main

import (
	"archive/zip"
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"net"
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/mailauth"
)

const multipartMsg = "From: =?utf-8?q?Cristina_=C8=98i?= <cristina@example.org>\r\n" +
	"To: agent@lgs.example\r\nCc: Paul <paul@example.org>\r\n" +
	"Subject: =?utf-8?q?the_flight_times_=E2=9C=88?=\r\n" +
	"Date: Mon, 12 Oct 2026 10:00:00 +0100\r\n" +
	"Message-ID: <m1@example.org>\r\nIn-Reply-To: <m0@lgs.example>\r\nReferences: <m0@lgs.example>\r\n" +
	"MIME-Version: 1.0\r\nContent-Type: multipart/mixed; boundary=\"B1\"\r\n\r\n" +
	"--B1\r\nContent-Type: multipart/alternative; boundary=\"B2\"\r\n\r\n" +
	"--B2\r\nContent-Type: text/plain; charset=iso-8859-1\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n" +
	"Thursday works, caf=E9 at nine.\r\n" +
	"--B2\r\nContent-Type: text/html; charset=utf-8\r\n\r\n<p>Thursday <b>works</b></p>\r\n--B2--\r\n" +
	"--B1\r\nContent-Type: application/pdf; name=\"tickets.pdf\"\r\nContent-Disposition: attachment; filename=\"tickets.pdf\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
	"JVBERi0xLjQK\r\n" +
	"--B1\r\nContent-Type: application/octet-stream; name=\"setup.exe\"\r\nContent-Disposition: attachment; filename=\"../setup.exe\"\r\nContent-Transfer-Encoding: base64\r\n\r\n" +
	"TVqQAAMAAAAEAAAA\r\n" +
	"--B1--\r\n"

func TestParseMultipart(t *testing.T) {
	p, err := parseMail([]byte(multipartMsg))
	if err != nil {
		t.Fatal(err)
	}
	if p.From != "cristina@example.org" || p.FromName != "Cristina Și" {
		t.Fatalf("from %q %q", p.From, p.FromName)
	}
	if p.Subject != "the flight times ✈" || p.MessageID != "m1@example.org" || p.InReplyTo != "m0@lgs.example" || len(p.References) != 1 {
		t.Fatalf("%+v", p)
	}
	if len(p.Cc) != 1 || p.Cc[0] != "paul@example.org" || p.To[0] != "agent@lgs.example" {
		t.Fatalf("%v %v", p.To, p.Cc)
	}
	if p.BodyKind != "text" || p.Body != "Thursday works, café at nine." {
		t.Fatalf("body %q (%s)", p.Body, p.BodyKind)
	}
	if len(p.Attach) != 2 {
		t.Fatalf("attachments %d", len(p.Attach))
	}
	if !p.Attach[0].Kept || p.Attach[0].Name != "tickets.pdf" || string(p.Attach[0].Bytes) != "%PDF-1.4\n" {
		t.Fatalf("%+v", p.Attach[0])
	}
	if p.Attach[1].Kept || p.Attach[1].Name != "setup.exe" || !strings.Contains(p.Attach[1].Why, "executable") || p.Attach[1].Bytes != nil {
		t.Fatalf("%+v", p.Attach[1])
	}
	if p.Date.Unix() != 1791795600 {
		t.Fatalf("date %v", p.Date)
	}
}

func TestParseHTMLOnlyAndPlain(t *testing.T) {
	h := "From: a@b.example\r\nSubject: x\r\nContent-Type: text/html\r\n\r\n<html><head><style>p{}</style><script>alert(1)</script></head><body><p>Hello&nbsp;there</p><div>line two</div><img src=\"http://t.example/pixel\"><ul><li>one</li><li>two</li></ul></body></html>"
	p, _ := parseMail([]byte(h))
	if p.BodyKind != "html-reduced" || p.Body != "Hello there\nline two\none\ntwo" {
		t.Fatalf("%q (%s)", p.Body, p.BodyKind)
	}
	plain := "From: a@b.example\r\nSubject: x\r\n\r\nline one\r\nline two\r\n"
	p, _ = parseMail([]byte(plain))
	if p.Body != "line one\nline two\n" || p.BodyKind != "text" {
		t.Fatalf("%q", p.Body)
	}
	docx := zipOf(t, map[string]string{"[Content_Types].xml": "<Types/>", "word/document.xml": "<w/>"})
	if dangerous("notes.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", docx) != "" || dangerous("photo.jpg", "image/jpeg", []byte{0xff, 0xd8, 0xff, 0}) != "" {
		t.Fatal("ordinary files refused")
	}
	if dangerous("a.zip", "application/zip", []byte("PK\x03\x04")) == "" || dangerous("x", "", []byte("\x7fELF")) == "" || dangerous("m.docm", "", nil) == "" {
		t.Fatal("dangerous files kept")
	}
	// the audit's cases: a package named like a document, by type and by content
	apk := zipOf(t, map[string]string{"AndroidManifest.xml": "<m/>", "classes.dex": "dex"})
	if dangerous("report.docx", "application/vnd.android.package-archive", apk) == "" {
		t.Fatal("a package declared as one, named .docx, kept")
	}
	if why := dangerous("report.docx", "application/vnd.openxmlformats-officedocument.wordprocessingml.document", apk); !strings.Contains(why, "AndroidManifest.xml") {
		t.Fatalf("a package inside a .docx kept: %q", why)
	}
	if dangerous("report.docx", "", zipOf(t, map[string]string{"word/x.xml": "<w/>"})) == "" {
		t.Fatal("a zip with no [Content_Types].xml kept as .docx")
	}
	if dangerous("macro.docx", "", zipOf(t, map[string]string{"[Content_Types].xml": "<Types/>", "word/vbaProject.bin": "x"})) == "" {
		t.Fatal("OOXML macros kept")
	}
	if dangerous("book.epub", "application/epub+zip", zipOf(t, map[string]string{"mimetype": "application/epub+zip", "OEBPS/a.xhtml": "x"})) != "" {
		t.Fatal("an epub refused")
	}
	ole := append([]byte{0xd0, 0xcf, 0x11, 0xe0, 0xa1, 0xb1, 0x1a, 0xe1}, utf16le("_VBA_PROJECT")...)
	if dangerous("old.doc", "application/msword", ole) == "" {
		t.Fatal("a compound file with a UTF-16LE VBA stream kept")
	}
	if storedMime("report.docx", "application/vnd.android.package-archive") != "application/vnd.openxmlformats-officedocument.wordprocessingml.document" ||
		storedMime("blob", "weird stuff") != "application/octet-stream" || storedMime("blob", "application/x-foo") != "application/x-foo" {
		t.Fatal(storedMime("report.docx", "application/vnd.android.package-archive"))
	}
	if safeName("../../etc/passwd") != "passwd" || safeName("") != "file" || safeName("a b (1).pdf") != "a b (1).pdf" {
		t.Fatal(safeName("../../etc/passwd"))
	}
}

func TestBuildSignVerifyRoundTrip(t *testing.T) {
	rk, _ := rsa.GenerateKey(rand.Reader, 2048)
	edPub, edPriv, _ := ed25519.GenerateKey(rand.Reader)
	o := outgoing{FromAddr: "agent@lgs.example", FromName: "Vlad's ghost", To: []string{"paul@example.org"}, Cc: []string{"cristina@example.org"},
		Subject: "Re: the flight times ✈", Body: "Thursday works.\n\nOn 12 Oct, Paul wrote:\n> café?\n", InReplyTo: "m1@example.org", Refs: []string{"m0@lgs.example", "m1@example.org"}, Domain: "lgs.example",
		Attach: []parsedAttach{{Name: "tickets.pdf", Mime: "application/pdf", Bytes: []byte("%PDF-1.4\n"), Kept: true}}}
	raw, msgID, err := buildMessage(o, time.Unix(1760000000, 0).UTC())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasSuffix(msgID, "@lgs.example") {
		t.Fatal(msgID)
	}
	signer := mailauth.Signer{Domain: "lgs.example", Selector: "ghost", EdSelector: "ghosted", RSA: rk, Ed25519: edPriv}
	signed, err := signer.Sign(raw)
	if err != nil {
		t.Fatal(err)
	}
	// what a receiver sees parses back to the same thing
	p, err := parseMail(signed)
	if err != nil {
		t.Fatal(err)
	}
	if p.From != "agent@lgs.example" || p.FromName != "Vlad's ghost" || p.Subject != "Re: the flight times ✈" || p.InReplyTo != "m1@example.org" || len(p.References) != 2 {
		t.Fatalf("%+v", p)
	}
	if p.Body != "Thursday works.\n\nOn 12 Oct, Paul wrote:\n> café?\n" {
		t.Fatalf("body %q", p.Body)
	}
	if len(p.Attach) != 1 || string(p.Attach[0].Bytes) != "%PDF-1.4\n" {
		t.Fatalf("%+v", p.Attach)
	}
	// and the signatures verify against the keys the DNS records carry
	_, rsaRec, _ := mailauth.DNSRecord("lgs.example", "ghost", &rk.PublicKey, nil)
	_, edRec, _ := mailauth.DNSRecord("lgs.example", "ghosted", nil, edPub)
	unquote := func(s string) string { return strings.ReplaceAll(strings.ReplaceAll(s, `" "`, ""), `"`, "") }
	dns := dnsMap{"ghost._domainkey.lgs.example": {unquote(rsaRec)}, "ghosted._domainkey.lgs.example": {unquote(edRec)}}
	res := mailauth.VerifyDKIM(context.Background(), dns, signed, time.Time{})
	if len(res) != 2 || !res[0].Pass() || !res[1].Pass() {
		t.Fatalf("%+v", res)
	}
	// no line over 998 bytes, CRLF throughout
	for _, ln := range strings.Split(string(signed), "\r\n") {
		if len(ln) > 998 || strings.Contains(ln, "\n") {
			t.Fatalf("line: %q", ln)
		}
	}
	if replySubject("Re: x") != "Re: x" || replySubject("x") != "Re: x" || forwardSubject("FW: x") != "FW: x" || forwardSubject("x") != "Fwd: x" {
		t.Fatal("subjects")
	}
	q := quoteForReply("Paul", "paul@example.org", time.Date(2026, 10, 12, 10, 0, 0, 0, time.UTC), "a\nb")
	if q != "On 12 Oct 2026 at 10:00, Paul <paul@example.org> wrote:\n> a\n> b\n" {
		t.Fatalf("%q", q)
	}
	if got := cleanAddrs([]string{"Paul <Paul@Example.org>", "paul@example.org", "nonsense", ""}); len(got) != 1 || got[0] != "paul@example.org" {
		t.Fatalf("%v", got)
	}
}

// dnsMap is a TXT-only resolver for the tests.
type dnsMap map[string][]string

func (d dnsMap) LookupTXT(_ context.Context, n string) ([]string, error) {
	if v, ok := d[strings.ToLower(n)]; ok {
		return v, nil
	}
	return nil, &notFoundErr{}
}
func (d dnsMap) LookupIP(context.Context, string, string) ([]net.IP, error) {
	return nil, &notFoundErr{}
}
func (d dnsMap) LookupMX(context.Context, string) ([]*net.MX, error)  { return nil, &notFoundErr{} }
func (d dnsMap) LookupAddr(context.Context, string) ([]string, error) { return nil, &notFoundErr{} }

type notFoundErr struct{}

func (*notFoundErr) Error() string { return "not found" }

// zipOf is a small zip with the given entries.
func zipOf(t *testing.T, entries map[string]string) []byte {
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	names := make([]string, 0, len(entries))
	for n := range entries {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, err := zw.Create(n)
		if err != nil {
			t.Fatal(err)
		}
		w.Write([]byte(entries[n]))
	}
	zw.Close()
	return buf.Bytes()
}
