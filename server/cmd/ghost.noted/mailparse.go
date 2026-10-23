package main

// MAIL PARSING AND BUILDING , the pure half of noted's mail: a received message's bytes into
// headers, a text body and attachments, and the person's reply or forward into a message the
// box signs. Standard library: net/mail, mime, mime/multipart, mime/quotedprintable, html.

import (
	"archive/zip"
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"html"
	"io"
	"mime"
	"mime/multipart"
	"mime/quotedprintable"
	"net/mail"
	"path"
	"regexp"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	maxAttachment = 20 << 20
	maxAttachAll  = 25 << 20
	maxBodyText   = 512 << 10
	// the header-derived fields the row carries whole and every list page returns: a sender
	// does not get to make them megabytes (the body has its own clip)
	maxSubject   = 998
	maxFromName  = 256
	maxAddrs     = 100
	maxAddrBytes = 254
)

// parsedMail is what a received message becomes.
type parsedMail struct {
	MessageID  string
	InReplyTo  string
	References []string
	From       string
	FromName   string
	To         []string
	Cc         []string
	ReplyTo    string
	Subject    string
	Date       time.Time
	Body       string
	BodyKind   string // text, html-reduced, none
	Attach     []parsedAttach
}

type parsedAttach struct {
	Name  string
	Mime  string
	Bytes []byte
	Kept  bool
	Why   string // why not kept
}

var wordDec = mime.WordDecoder{CharsetReader: charsetReader}

func decodeHeader(s string) string {
	if d, err := wordDec.DecodeHeader(s); err == nil {
		return d
	}
	return s
}

func addrList(h mail.Header, key string) []string {
	v := h.Get(key)
	if v == "" {
		return nil
	}
	list, err := mail.ParseAddressList(v)
	if err != nil {
		// keep what looks like addresses
		var out []string
		for _, m := range addrRe.FindAllString(v, maxAddrs) {
			out = append(out, strings.ToLower(m))
		}
		return clipAddrs(out)
	}
	var out []string
	for _, a := range list {
		out = append(out, strings.ToLower(a.Address))
	}
	return clipAddrs(out)
}

// clipAddrs bounds an address list: at most maxAddrs entries, each at most maxAddrBytes.
func clipAddrs(in []string) []string {
	if len(in) > maxAddrs {
		in = in[:maxAddrs]
	}
	for i, a := range in {
		if len(a) > maxAddrBytes {
			in[i] = a[:maxAddrBytes]
		}
	}
	return in
}

// clipField bounds a header-derived string at n bytes on a rune boundary.
func clipField(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut := n
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut]
}

var addrRe = regexp.MustCompile(`[A-Za-z0-9._%+-]+@[A-Za-z0-9.-]+\.[A-Za-z]{2,}`)

func msgIDs(s string) []string {
	var out []string
	for _, f := range strings.Fields(s) {
		f = strings.Trim(f, "<>")
		if f != "" {
			out = append(out, f)
		}
	}
	return out
}

// parseMail takes the raw bytes apart. It never fetches anything and never runs anything.
func parseMail(raw []byte) (parsedMail, error) {
	var p parsedMail
	m, err := mail.ReadMessage(bytes.NewReader(raw))
	if err != nil {
		return p, err
	}
	h := m.Header
	if ids := msgIDs(h.Get("Message-ID")); len(ids) > 0 {
		p.MessageID = ids[0]
	}
	if ids := msgIDs(h.Get("In-Reply-To")); len(ids) > 0 {
		p.InReplyTo = ids[0]
	}
	p.References = msgIDs(h.Get("References"))
	if from, err := mail.ParseAddressList(h.Get("From")); err == nil && len(from) > 0 {
		p.From = clipField(strings.ToLower(from[0].Address), maxAddrBytes)
		p.FromName = clipField(decodeHeader(from[0].Name), maxFromName)
	} else if m := addrRe.FindString(h.Get("From")); m != "" {
		p.From = clipField(strings.ToLower(m), maxAddrBytes)
	}
	p.To = addrList(h, "To")
	p.Cc = addrList(h, "Cc")
	if rt := addrList(h, "Reply-To"); len(rt) > 0 {
		p.ReplyTo = rt[0]
	}
	p.Subject = clipField(strings.TrimSpace(decodeHeader(h.Get("Subject"))), maxSubject)
	if d, err := h.Date(); err == nil {
		p.Date = d
	}
	var texts, htmls []string
	var total int64
	walkPart(h.Get("Content-Type"), h.Get("Content-Transfer-Encoding"), h.Get("Content-Disposition"), m.Body, 0, &texts, &htmls, &p.Attach, &total)
	switch {
	case len(texts) > 0:
		p.Body, p.BodyKind = clipText(strings.Join(texts, "\n\n")), "text"
	case len(htmls) > 0:
		p.Body, p.BodyKind = clipText(htmlToText(strings.Join(htmls, "\n"))), "html-reduced"
	default:
		p.BodyKind = "none"
	}
	return p, nil
}

func clipText(s string) string {
	if len(s) > maxBodyText {
		return s[:maxBodyText] + "\n[cut at 512 KB]"
	}
	return s
}

// walkPart descends a MIME tree, collecting text, HTML and attachments.
func walkPart(ctype, cte, cdisp string, body io.Reader, depth int, texts, htmls *[]string, atts *[]parsedAttach, total *int64) {
	if depth > 8 {
		return
	}
	mt, params, err := mime.ParseMediaType(ctype)
	if err != nil || ctype == "" {
		mt, params = "text/plain", map[string]string{}
	}
	mt = strings.ToLower(mt)
	if strings.HasPrefix(mt, "multipart/") {
		boundary := params["boundary"]
		if boundary == "" {
			return
		}
		mr := multipart.NewReader(body, boundary)
		for i := 0; i < 200; i++ {
			part, err := mr.NextPart()
			if err != nil {
				return
			}
			walkPart(part.Header.Get("Content-Type"), part.Header.Get("Content-Transfer-Encoding"), part.Header.Get("Content-Disposition"), part, depth+1, texts, htmls, atts, total)
		}
		return
	}
	if mt == "message/rfc822" {
		// an attached message: keep as an attachment named by its subject
		b, _ := io.ReadAll(io.LimitReader(decodeBody(body, cte), maxAttachment+1))
		name := "message.eml"
		if inner, err := mail.ReadMessage(bytes.NewReader(b)); err == nil {
			if s := decodeHeader(inner.Header.Get("Subject")); s != "" {
				name = safeName(s) + ".eml"
			}
		}
		addAttach(atts, total, name, mt, b)
		return
	}
	disp, dparams, _ := mime.ParseMediaType(cdisp)
	filename := dparams["filename"]
	if filename == "" {
		filename = params["name"]
	}
	filename = decodeHeader(filename)
	isAttachment := strings.EqualFold(disp, "attachment") || filename != ""
	if !isAttachment && (mt == "text/plain" || mt == "text/html") {
		b, _ := io.ReadAll(io.LimitReader(decodeBody(body, cte), maxBodyText*2))
		s := toUTF8(b, params["charset"])
		if mt == "text/plain" {
			*texts = append(*texts, strings.ReplaceAll(s, "\r\n", "\n"))
		} else {
			*htmls = append(*htmls, s)
		}
		return
	}
	b, _ := io.ReadAll(io.LimitReader(decodeBody(body, cte), maxAttachment+1))
	if filename == "" {
		filename = "part" + extFor(mt)
	}
	addAttach(atts, total, filename, mt, b)
}

func addAttach(atts *[]parsedAttach, total *int64, name, mt string, b []byte) {
	a := parsedAttach{Name: safeName(name), Mime: storedMime(name, mt), Kept: true}
	switch {
	case len(b) > maxAttachment:
		a.Kept, a.Why = false, "larger than 20 MB"
	case *total+int64(len(b)) > maxAttachAll:
		a.Kept, a.Why = false, "attachments over 25 MB in all"
	default:
		if why := dangerous(a.Name, mt, b); why != "" {
			a.Kept, a.Why = false, why
		}
	}
	if a.Kept {
		a.Bytes = b
		*total += int64(len(b))
	} else {
		a.Bytes = nil
	}
	*atts = append(*atts, a)
}

// storedMime is the type the row carries and the phone hands to its media store: the one the
// file name says when the name has a known extension, otherwise the sender's declared type if
// it looks like one, otherwise octet-stream. The sender does not get to call a .docx a package.
func storedMime(name, declared string) string {
	if ext := strings.ToLower(path.Ext(name)); ext != "" {
		if t := mime.TypeByExtension(ext); t != "" {
			if i := strings.IndexByte(t, ';'); i >= 0 {
				t = t[:i]
			}
			return t
		}
	}
	declared = strings.ToLower(strings.TrimSpace(declared))
	if i := strings.IndexByte(declared, ';'); i >= 0 {
		declared = strings.TrimSpace(declared[:i])
	}
	if len(declared) > 100 || !mimeRe.MatchString(declared) {
		return "application/octet-stream"
	}
	return declared
}

var mimeRe = regexp.MustCompile(`^[a-z0-9!#$&^_.+-]+/[a-z0-9!#$&^_.+-]+$`)

// dangerous names what the box will not keep: things that run, things that hide things that
// run, and office files with macros. By extension, by the declared type, by the first bytes,
// and for a zip (an office file is one) by what is inside it.
func dangerous(name, mt string, b []byte) string {
	ext := strings.ToLower(path.Ext(name))
	switch ext {
	case ".exe", ".scr", ".bat", ".cmd", ".com", ".pif", ".js", ".jse", ".vbs", ".vbe", ".wsf", ".wsh", ".ps1", ".msi", ".dll", ".jar", ".apk", ".sh", ".hta", ".cpl", ".reg", ".lnk", ".app", ".dmg", ".deb", ".rpm":
		return "an executable (" + ext + ") is not kept"
	case ".zip", ".rar", ".7z", ".tar", ".gz", ".tgz", ".bz2", ".xz", ".iso", ".img", ".cab", ".ace", ".arj":
		return "an archive (" + ext + ") is not kept"
	case ".docm", ".xlsm", ".pptm", ".dotm", ".xltm", ".potm":
		return "an office file with macros (" + ext + ") is not kept"
	}
	mt = strings.ToLower(strings.TrimSpace(mt))
	if i := strings.IndexByte(mt, ';'); i >= 0 {
		mt = strings.TrimSpace(mt[:i])
	}
	switch mt {
	case "application/vnd.android.package-archive", "application/java-archive", "application/x-msdownload", "application/x-dosexec",
		"application/x-executable", "application/x-sharedlib", "application/x-mach-binary", "application/x-sh", "application/x-shellscript",
		"application/x-msi", "application/x-ms-installer", "application/vnd.microsoft.portable-executable", "application/x-elf",
		"application/x-apple-diskimage", "application/vnd.debian.binary-package", "application/x-rpm", "application/hta", "text/javascript", "application/javascript":
		return "declared as something that runs (" + mt + "), not kept"
	case "application/zip", "application/x-zip-compressed", "application/x-rar-compressed", "application/vnd.rar", "application/x-7z-compressed",
		"application/x-tar", "application/gzip", "application/x-gzip", "application/x-bzip2", "application/x-xz", "application/x-iso9660-image":
		return "declared as an archive (" + mt + "), not kept"
	}
	if len(b) >= 4 {
		if b[0] == 'M' && b[1] == 'Z' {
			return "a Windows executable is not kept"
		}
		if b[0] == 0x7f && b[1] == 'E' && b[2] == 'L' && b[3] == 'F' {
			return "a Linux executable is not kept"
		}
		if b[0] == 0xcf && b[1] == 0xfa && b[2] == 0xed && b[3] == 0xfe {
			return "a Mac executable is not kept"
		}
		if b[0] == 'P' && b[1] == 'K' {
			switch ext {
			case ".docx", ".xlsx", ".pptx", ".dotx", ".xltx", ".potx", ".odt", ".ods", ".odp", ".epub":
				return zipLooksLike(ext, b)
			}
			return "an archive is not kept"
		}
		if b[0] == 0xd0 && b[1] == 0xcf && b[2] == 0x11 && b[3] == 0xe0 {
			// a compound file: directory entry names are UTF-16LE
			if bytes.Contains(b, []byte("_VBA_PROJECT")) || bytes.Contains(b, utf16le("_VBA_PROJECT")) || bytes.Contains(b, utf16le("VBA")) && bytes.Contains(b, utf16le("Macros")) {
				return "an office file with macros is not kept"
			}
		}
	}
	return ""
}

// utf16le is s as UTF-16LE bytes (ASCII input), the way compound-file directory names are stored.
func utf16le(s string) []byte {
	out := make([]byte, 0, 2*len(s))
	for i := 0; i < len(s); i++ {
		out = append(out, s[i], 0)
	}
	return out
}

// zipLooksLike opens a zip that claims to be an office or EPUB file and says why it is not one:
// an OOXML file has [Content_Types].xml and no vbaProject.bin, an ODF or EPUB file has a
// mimetype entry; nothing inside may be a class, a dex, an executable or an Android manifest.
func zipLooksLike(ext string, b []byte) string {
	zr, err := zip.NewReader(bytes.NewReader(b), int64(len(b)))
	if err != nil {
		return "a zip that does not open is not kept"
	}
	if len(zr.File) > 10000 {
		return "a zip with more than ten thousand entries is not kept"
	}
	hasTypes, hasMime := false, false
	for _, f := range zr.File {
		n := strings.ToLower(f.Name)
		switch {
		case n == "[content_types].xml":
			hasTypes = true
		case n == "mimetype":
			hasMime = true
		case n == "androidmanifest.xml", n == "classes.dex", strings.HasSuffix(n, ".dex"), strings.HasSuffix(n, ".class"),
			strings.HasSuffix(n, ".exe"), strings.HasSuffix(n, ".dll"), strings.HasSuffix(n, ".so"), strings.HasSuffix(n, ".jar"):
			return "a zip holding something that runs (" + path.Base(f.Name) + ") is not kept"
		case strings.HasSuffix(n, "vbaproject.bin"), strings.Contains(n, "/vba"):
			return "an office file with macros is not kept"
		}
	}
	switch ext {
	case ".odt", ".ods", ".odp", ".epub":
		if !hasMime {
			return "a zip named " + ext + " with no mimetype entry is not kept"
		}
	default:
		if !hasTypes {
			return "a zip named " + ext + " with no [Content_Types].xml is not kept"
		}
	}
	return ""
}

func extFor(mt string) string {
	if exts, err := mime.ExtensionsByType(mt); err == nil && len(exts) > 0 {
		return exts[0]
	}
	return ".bin"
}

var unsafeName = regexp.MustCompile(`[^A-Za-z0-9._ ()-]+`)

func safeName(s string) string {
	s = path.Base(strings.ReplaceAll(s, "\\", "/"))
	s = unsafeName.ReplaceAllString(s, "_")
	s = strings.Trim(s, ". ")
	if s == "" || s == "." || s == ".." {
		s = "file"
	}
	if len(s) > 120 {
		s = s[:120]
	}
	return s
}

func decodeBody(r io.Reader, cte string) io.Reader {
	switch strings.ToLower(strings.TrimSpace(cte)) {
	case "base64":
		return base64.NewDecoder(base64.StdEncoding, &b64Clean{r: r})
	case "quoted-printable":
		return quotedprintable.NewReader(r)
	}
	return r
}

// b64Clean drops whitespace so the base64 decoder sees a single run.
type b64Clean struct {
	r   io.Reader
	buf []byte
}

func (c *b64Clean) Read(p []byte) (int, error) {
	for {
		if len(c.buf) > 0 {
			n := copy(p, c.buf)
			c.buf = c.buf[n:]
			return n, nil
		}
		tmp := make([]byte, 4096)
		n, err := c.r.Read(tmp)
		if n > 0 {
			out := tmp[:0]
			for _, ch := range tmp[:n] {
				if ch != '\r' && ch != '\n' && ch != ' ' && ch != '\t' {
					out = append(out, ch)
				}
			}
			c.buf = append(c.buf, out...)
			if len(c.buf) == 0 && err == nil {
				continue
			}
			if len(c.buf) == 0 {
				return 0, err
			}
			continue
		}
		return 0, err
	}
}

// charsetReader covers the charsets mail still arrives in without a table package.
func charsetReader(charset string, input io.Reader) (io.Reader, error) {
	b, err := io.ReadAll(input)
	if err != nil {
		return nil, err
	}
	return strings.NewReader(toUTF8(b, charset)), nil
}

func toUTF8(b []byte, charset string) string {
	cs := strings.ToLower(strings.TrimSpace(charset))
	switch cs {
	case "", "utf-8", "utf8", "us-ascii", "ascii":
		if utf8.Valid(b) {
			return string(b)
		}
		return latin1(b)
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

var (
	scriptRe = regexp.MustCompile(`(?is)<(script|style|head)[^>]*>.*?</(script|style|head)>`)
	breakRe  = regexp.MustCompile(`(?i)<(br|/p|/div|/li|/tr|/h[1-6]|/blockquote)[^>]*>`)
	tagRe    = regexp.MustCompile(`(?s)<[^>]*>`)
	spaceRe  = regexp.MustCompile(`[ \t\x{a0}]+`)
	linesRe  = regexp.MustCompile(`\n{3,}`)
)

// htmlToText is the reduction of an HTML part to text: no tags, no scripts, no remote
// anything, entities decoded, block ends as line breaks.
func htmlToText(s string) string {
	s = scriptRe.ReplaceAllString(s, "")
	s = breakRe.ReplaceAllString(s, "\n")
	s = tagRe.ReplaceAllString(s, " ")
	s = html.UnescapeString(s)
	s = strings.ReplaceAll(s, "\r\n", "\n")
	var out []string
	for _, ln := range strings.Split(s, "\n") {
		out = append(out, strings.TrimSpace(spaceRe.ReplaceAllString(ln, " ")))
	}
	s = strings.Join(out, "\n")
	s = linesRe.ReplaceAllString(s, "\n\n")
	return strings.TrimSpace(s)
}

// outgoing is what the person asked to send.
type outgoing struct {
	FromAddr  string
	FromName  string
	To        []string
	Cc        []string
	Subject   string
	Body      string
	InReplyTo string   // the replied message's Message-ID
	Refs      []string // the thread so far
	Domain    string
	Attach    []parsedAttach // forwarded attachments
}

// buildMessage makes the RFC 5322 text the box signs: UTF-8, quoted-printable, CRLF, nothing
// about the box in it but its domain.
func buildMessage(o outgoing, now time.Time) ([]byte, string, error) {
	if o.FromAddr == "" || len(o.To) == 0 {
		return nil, "", fmt.Errorf("a sender and at least one recipient")
	}
	var r [8]byte
	_, _ = rand.Read(r[:])
	msgID := hex.EncodeToString(r[:]) + "@" + o.Domain
	var b strings.Builder
	write := func(k, v string) { fmt.Fprintf(&b, "%s: %s\r\n", k, v) }
	from := o.FromAddr
	if o.FromName != "" {
		from = mime.QEncoding.Encode("utf-8", o.FromName) + " <" + o.FromAddr + ">"
	}
	write("From", from)
	write("To", strings.Join(o.To, ", "))
	if len(o.Cc) > 0 {
		write("Cc", strings.Join(o.Cc, ", "))
	}
	write("Subject", mime.QEncoding.Encode("utf-8", o.Subject))
	write("Date", now.Format(time.RFC1123Z))
	write("Message-ID", "<"+msgID+">")
	if o.InReplyTo != "" {
		write("In-Reply-To", "<"+o.InReplyTo+">")
	}
	if len(o.Refs) > 0 {
		var refs []string
		for _, x := range o.Refs {
			refs = append(refs, "<"+x+">")
		}
		write("References", strings.Join(refs, " "))
	}
	write("MIME-Version", "1.0")
	body := strings.ReplaceAll(o.Body, "\r\n", "\n")
	body = strings.ReplaceAll(body, "\n", "\r\n")
	var qp bytes.Buffer
	w := quotedprintable.NewWriter(&qp)
	w.Write([]byte(body))
	w.Close()
	var kept []parsedAttach
	for _, a := range o.Attach {
		if a.Kept && len(a.Bytes) > 0 {
			kept = append(kept, a)
		}
	}
	if len(kept) == 0 {
		write("Content-Type", "text/plain; charset=utf-8")
		write("Content-Transfer-Encoding", "quoted-printable")
		b.WriteString("\r\n")
		b.Write(qp.Bytes())
		if !strings.HasSuffix(b.String(), "\r\n") {
			b.WriteString("\r\n")
		}
		return []byte(b.String()), msgID, nil
	}
	var rb [12]byte
	_, _ = rand.Read(rb[:])
	boundary := "ghost-" + hex.EncodeToString(rb[:])
	write("Content-Type", "multipart/mixed; boundary=\""+boundary+"\"")
	b.WriteString("\r\n")
	b.WriteString("--" + boundary + "\r\n")
	b.WriteString("Content-Type: text/plain; charset=utf-8\r\nContent-Transfer-Encoding: quoted-printable\r\n\r\n")
	b.Write(qp.Bytes())
	b.WriteString("\r\n")
	for _, a := range kept {
		b.WriteString("--" + boundary + "\r\n")
		mt := a.Mime
		if mt == "" {
			mt = "application/octet-stream"
		}
		fmt.Fprintf(&b, "Content-Type: %s; name=\"%s\"\r\n", mt, a.Name)
		fmt.Fprintf(&b, "Content-Disposition: attachment; filename=\"%s\"\r\n", a.Name)
		b.WriteString("Content-Transfer-Encoding: base64\r\n\r\n")
		enc := base64.StdEncoding.EncodeToString(a.Bytes)
		for len(enc) > 76 {
			b.WriteString(enc[:76] + "\r\n")
			enc = enc[76:]
		}
		b.WriteString(enc + "\r\n")
	}
	b.WriteString("--" + boundary + "--\r\n")
	return []byte(b.String()), msgID, nil
}

// quoteForReply is the original under the person's text, the way every mail client does it.
func quoteForReply(fromName, fromAddr string, sent time.Time, body string) string {
	who := fromAddr
	if fromName != "" {
		who = fromName + " <" + fromAddr + ">"
	}
	when := "earlier"
	if !sent.IsZero() {
		when = sent.Format("2 Jan 2006 at 15:04")
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "On %s, %s wrote:\n", when, who)
	for _, ln := range strings.Split(strings.ReplaceAll(body, "\r\n", "\n"), "\n") {
		sb.WriteString("> " + ln + "\n")
	}
	return sb.String()
}

// forwardHeader is the block above a forwarded message.
func forwardHeader(fromName, fromAddr string, to []string, sent time.Time, subject string) string {
	who := fromAddr
	if fromName != "" {
		who = fromName + " <" + fromAddr + ">"
	}
	when := ""
	if !sent.IsZero() {
		when = sent.Format("2 Jan 2006 at 15:04")
	}
	return fmt.Sprintf("---------- Forwarded message ----------\nFrom: %s\nDate: %s\nSubject: %s\nTo: %s\n\n", who, when, subject, strings.Join(to, ", "))
}

// replySubject and forwardSubject add the prefix once.
func replySubject(s string) string {
	if strings.HasPrefix(strings.ToLower(s), "re:") {
		return s
	}
	return "Re: " + s
}

func forwardSubject(s string) string {
	l := strings.ToLower(s)
	if strings.HasPrefix(l, "fwd:") || strings.HasPrefix(l, "fw:") {
		return s
	}
	return "Fwd: " + s
}

func sha256Hex(b []byte) string {
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:])
}
