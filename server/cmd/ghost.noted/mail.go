package main

// MAIL , noted's third inbox, after notes and chats (docs/MAIL.md). Every two minutes while the
// box is open it asks the relay for what has arrived, unseals each message and the relay's note
// of the connection, judges it (SPF, DKIM, DMARC, reverse DNS, the header screen, a spam score),
// takes it apart (text body, attachments that are safe to keep), stores it, raises one
// notification, and deletes the relay's copy. The other way, it takes what secd drops in
// <mount>/noted/outbox (the person's tap in the app), builds the message, signs it with the
// box's own DKIM keys, seals it to the relay's key and posts it; the relay delivers. The relay
// never sees a key that opens anything stored, and the box never listens for anything.

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/rand"
	"crypto/rsa"
	"crypto/x509"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/mailauth"
	"github.com/LocalGhostDao/localghost/server/internal/mailseal"
	"github.com/LocalGhostDao/localghost/server/internal/mailwire"
)

const (
	mailPullEvery = 2 * time.Minute
	mailMaxRaw    = 26 << 20
	burstPerHour  = 5
)

// mailKeys are the box's: the X25519 pair the relay seals to, and the DKIM pair it signs with
// (RSA-2048 for the receivers that verify nothing else, Ed25519 for the ones that do).
type mailKeys struct {
	sealPriv []byte
	sealPub  []byte
	rsa      *rsa.PrivateKey
	ed       ed25519.PrivateKey
}

func loadOrMakeKeys(dir string) (*mailKeys, error) {
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, err
	}
	k := &mailKeys{}
	sealPath := filepath.Join(dir, "mail.key")
	if priv, err := mailseal.ReadKeyFile(sealPath); err == nil {
		k.sealPriv = priv
	} else {
		priv, _, err := mailseal.GenerateKey()
		if err != nil {
			return nil, err
		}
		if err := mailseal.WriteKeyFile(sealPath, priv); err != nil {
			return nil, err
		}
		k.sealPriv = priv
	}
	pub, err := mailseal.PublicOf(k.sealPriv)
	if err != nil {
		return nil, err
	}
	k.sealPub = pub
	rsaPath := filepath.Join(dir, "dkim_rsa.key")
	if b, err := os.ReadFile(rsaPath); err == nil {
		blk, _ := pem.Decode(b)
		if blk == nil {
			return nil, errors.New("dkim_rsa.key is not PEM")
		}
		rk, err := x509.ParsePKCS1PrivateKey(blk.Bytes)
		if err != nil {
			return nil, fmt.Errorf("dkim_rsa.key: %w", err)
		}
		k.rsa = rk
	} else {
		rk, err := rsa.GenerateKey(rand.Reader, 2048)
		if err != nil {
			return nil, err
		}
		b := pem.EncodeToMemory(&pem.Block{Type: "RSA PRIVATE KEY", Bytes: x509.MarshalPKCS1PrivateKey(rk)})
		if err := os.WriteFile(rsaPath, b, 0o600); err != nil {
			return nil, err
		}
		k.rsa = rk
	}
	edPath := filepath.Join(dir, "dkim_ed.key")
	if seed, err := mailseal.ReadKeyFile(edPath); err == nil {
		k.ed = ed25519.NewKeyFromSeed(seed)
	} else {
		_, priv, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			return nil, err
		}
		if err := mailseal.WriteKeyFile(edPath, priv.Seed()); err != nil {
			return nil, err
		}
		k.ed = priv
	}
	return k, nil
}

// mailSettings is what the person set: the address, the relay, the token, the name. The
// address is either on a domain of the person's own (then its DNS carries the records the box
// prints) or on the relay's shared domain (then LocalGhost publishes the box's keys under it and
// nothing in DNS is the person's). A sender learns the domain either way, so a domain whose zone
// holds the box's own address anywhere (the name the phone connects to) is never the one to use.
type mailSettings struct {
	Address   string `json:"address"`
	Shared    bool   `json:"shared"`
	RelayURL  string `json:"relayUrl"`
	RelayBox  string `json:"relayBox"`
	Token     string `json:"-"`
	RelayPub  string `json:"-"`
	RelayHost string `json:"relayHost"`
	FromName  string `json:"fromName"`
	DNSChecks bool   `json:"dnsChecks"`
}

func (s mailSettings) address() string { return s.Address }

// domain is the part after the @.
func (s mailSettings) domain() string {
	if at := strings.LastIndexByte(s.Address, '@'); at >= 0 {
		return s.Address[at+1:]
	}
	return ""
}

func (s mailSettings) ready() bool {
	return s.domain() != "" && s.RelayURL != "" && s.RelayBox != "" && s.Token != ""
}

// validAddress is a local part, an @, and a domain with a dot.
func validAddress(a string) bool {
	a = strings.ToLower(strings.TrimSpace(a))
	at := strings.LastIndexByte(a, '@')
	if at <= 0 || at == len(a)-1 {
		return false
	}
	d := a[at+1:]
	return strings.Contains(d, ".") && !strings.ContainsAny(a, " /:<>,;\"") && addrRe.MatchString(a)
}

// mailer holds the loop's state.
type mailer struct {
	mount  string
	store  *hw.NotifStore
	slot   int
	lg     *slog.Logger
	keys   *mailKeys
	hc     *http.Client
	mu     sync.Mutex
	last   mailStatus
	force  chan struct{}
	dnsKey string
}

// mailStatus is what `ghost-cli ghost.noted mail` and secd's /v1/mail/status read, written to
// <mount>/noted/mail/status.json after every pass.
type mailStatus struct {
	Address    string `json:"address"`
	Ready      bool   `json:"ready"`
	LastPull   int64  `json:"lastPull"`
	LastOK     int64  `json:"lastOk"`
	LastErr    string `json:"lastErr,omitempty"`
	Pulled     int    `json:"pulled"`
	Queued     int    `json:"queuedOnRelay"`
	In         int    `json:"in"`
	Unseen     int    `json:"unseen"`
	Out        int    `json:"out"`
	OutWaiting int    `json:"outWaiting"`
	RelayHost  string `json:"relayHost,omitempty"`
	Keys       bool   `json:"keys"`
}

func newMailer(mount string, lg *slog.Logger) *mailer {
	store := hw.NewNotifStore(func(int) string { return hw.SocketForMount(mount) })
	tr := &http.Transport{Proxy: http.ProxyFromEnvironment, DialContext: (&net.Dialer{Timeout: 15 * time.Second}).DialContext,
		TLSHandshakeTimeout: 15 * time.Second, ResponseHeaderTimeout: 30 * time.Second, MaxIdleConns: 2, IdleConnTimeout: 60 * time.Second}
	return &mailer{mount: mount, store: store, lg: lg, hc: &http.Client{Transport: tr, Timeout: 3 * time.Minute}, force: make(chan struct{}, 1)}
}

func (m *mailer) dir(parts ...string) string {
	return filepath.Join(append([]string{m.mount, "noted", "mail"}, parts...)...)
}

func (m *mailer) settings() mailSettings {
	get := func(k string) string {
		v, _ := m.store.GetSetting(m.slot, k)
		return v
	}
	return mailSettings{Address: strings.ToLower(strings.TrimSpace(get("mail_address"))), Shared: get("mail_shared") == "on", RelayURL: strings.TrimRight(get("mail_relay_url"), "/"), RelayBox: get("mail_relay_box"),
		Token: get("mail_relay_token"), RelayPub: get("mail_relay_pub"), RelayHost: get("mail_relay_host"), FromName: get("mail_from_name"), DNSChecks: get("mail_dns_checks") != "off"}
}

// mailLoop is the pull and the outbox, every two minutes, or now on `mail-pull`.
func (m *mailer) run(ctx context.Context) {
	for _, d := range []string{m.dir("in"), m.dir("att"), m.dir("keys"), filepath.Join(m.mount, "noted", "outbox")} {
		if err := os.MkdirAll(d, 0o750); err != nil {
			m.lg.Error("mail dirs", "fn", "mailer.run", "err", err)
			return
		}
	}
	k, err := loadOrMakeKeys(m.dir("keys"))
	if err != nil {
		m.lg.Error("mail keys", "fn", "mailer.run", "err", err)
		return
	}
	m.keys = k
	m.writeDNS()
	t := time.NewTicker(mailPullEvery)
	defer t.Stop()
	m.pass(ctx)
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
		case <-m.force:
		}
		m.pass(ctx)
	}
}

func (m *mailer) pass(ctx context.Context) {
	s := m.settings()
	if key := s.Address + "|" + s.RelayHost + "|" + s.RelayBox + "|" + strconv.FormatBool(s.Shared) + "|" + strconv.FormatBool(s.Token != ""); key != m.dnsKey {
		m.dnsKey = key
		m.writeDNS()
	}
	st := mailStatus{Address: s.address(), Ready: s.ready(), RelayHost: s.RelayHost, Keys: m.keys != nil}
	m.mu.Lock()
	prev := m.last
	m.mu.Unlock()
	st.LastOK = prev.LastOK
	st.Pulled = prev.Pulled
	if s.ready() {
		st.LastPull = time.Now().Unix()
		n, queued, err := m.pull(ctx, s)
		st.Pulled += n
		st.Queued = queued
		if err != nil {
			st.LastErr = err.Error()
			m.lg.Warn("mail pull", "fn", "mailer.pass", "err", err)
		} else {
			st.LastOK = st.LastPull
		}
		if err := m.outbox(ctx, s); err != nil {
			m.lg.Warn("mail outbox", "fn", "mailer.pass", "err", err)
			if st.LastErr == "" {
				st.LastErr = err.Error()
			}
		}
		if err := m.outStatus(ctx, s); err != nil {
			m.lg.Warn("mail out status", "fn", "mailer.pass", "err", err)
		}
	}
	if in, unseen, out, queued, err := m.store.MailCounts(m.slot); err == nil {
		st.In, st.Unseen, st.Out, st.OutWaiting = in, unseen, out, queued
	}
	m.mu.Lock()
	m.last = st
	m.mu.Unlock()
	b, _ := json.Marshal(st)
	_ = os.WriteFile(m.dir("status.json.part"), b, 0o640)
	_ = os.Rename(m.dir("status.json.part"), m.dir("status.json"))
}

// relay calls.
func (m *mailer) call(ctx context.Context, s mailSettings, method, path string, body []byte, cap int64) (int, []byte, error) {
	var rd io.Reader
	if body != nil {
		rd = bytes.NewReader(body)
	}
	req, err := http.NewRequestWithContext(ctx, method, s.RelayURL+"/box/"+s.RelayBox+path, rd)
	if err != nil {
		return 0, nil, err
	}
	req.Header.Set("Authorization", "Bearer "+s.Token)
	req.Header.Set("User-Agent", "ghost.noted")
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := m.hc.Do(req)
	if err != nil {
		return 0, nil, err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(io.LimitReader(resp.Body, cap))
	if err != nil {
		return resp.StatusCode, nil, err
	}
	return resp.StatusCode, b, nil
}

// pull fetches and stores what the relay holds. Returns how many were stored and how many
// remain on the relay.
func (m *mailer) pull(ctx context.Context, s mailSettings) (int, int, error) {
	code, b, err := m.call(ctx, s, http.MethodGet, "/in/", nil, 1<<20)
	if err != nil {
		return 0, 0, fmt.Errorf("relay unreachable: %w", err)
	}
	if code == http.StatusUnauthorized {
		return 0, 0, errors.New("the relay refuses the token (is the box enrolled?)")
	}
	if code != http.StatusOK {
		return 0, 0, fmt.Errorf("relay answered %d", code)
	}
	var list []mailwire.Entry
	if err := json.Unmarshal(b, &list); err != nil {
		return 0, 0, fmt.Errorf("relay listing: %w", err)
	}
	stored := 0
	for _, e := range list {
		if ctx.Err() != nil {
			return stored, len(list) - stored, ctx.Err()
		}
		if !mailwire.NameRe.MatchString(e.Name) || !e.Note {
			continue
		}
		if have, _ := m.store.MailHave(m.slot, "in", e.Name); have {
			m.call(ctx, s, http.MethodDelete, "/in/"+e.Name, nil, 1024)
			continue
		}
		if err := m.fetchOne(ctx, s, e); err != nil {
			m.lg.Warn("mail message not stored", "fn", "mailer.pull", "name", e.Name, "err", err)
			continue
		}
		stored++
		if code, _, err := m.call(ctx, s, http.MethodDelete, "/in/"+e.Name, nil, 1024); err != nil || (code != http.StatusNoContent && code != http.StatusNotFound) {
			m.lg.Warn("relay copy not deleted", "fn", "mailer.pull", "name", e.Name, "code", code, "err", err)
		}
	}
	return stored, len(list) - stored, nil
}

func (m *mailer) fetchOne(ctx context.Context, s mailSettings, e mailwire.Entry) error {
	code, sealedNote, err := m.call(ctx, s, http.MethodGet, "/in/"+mailwire.NoteName(e.Name), nil, 1<<20)
	if err != nil || code != http.StatusOK {
		return fmt.Errorf("note: %d %v", code, err)
	}
	code, sealed, err := m.call(ctx, s, http.MethodGet, "/in/"+e.Name, nil, mailMaxRaw+4096)
	if err != nil || code != http.StatusOK {
		return fmt.Errorf("message: %d %v", code, err)
	}
	noteBytes, err := mailseal.Open(bytes.NewReader(sealedNote), m.keys.sealPriv, 1<<20)
	if err != nil {
		return fmt.Errorf("note does not open: %w", err)
	}
	var note mailwire.Note
	if err := json.Unmarshal(noteBytes, &note); err != nil {
		return fmt.Errorf("note: %w", err)
	}
	raw, err := mailseal.Open(bytes.NewReader(sealed), m.keys.sealPriv, mailMaxRaw)
	if err != nil {
		return fmt.Errorf("message does not open: %w", err)
	}
	// the sealed original stays as fetched
	sealPath := m.dir("in", e.Name)
	if err := os.WriteFile(sealPath+".part", sealed, 0o640); err != nil {
		return err
	}
	if err := os.Rename(sealPath+".part", sealPath); err != nil {
		return err
	}
	return m.keep(ctx, s, e.Name, note, raw, sealPath)
}

// keep judges, parses, writes the row and the attachments, and raises the notification.
func (m *mailer) keep(ctx context.Context, s mailSettings, name string, note mailwire.Note, raw []byte, sealPath string) error {
	p, perr := parseMail(raw)
	seen, domainSeen, _ := m.store.MailSenderHistory(m.slot, p.From)
	var res mailauth.Resolver = mailauth.NetResolver{Timeout: 5 * time.Second}
	if !s.DNSChecks {
		res = mailauth.NoResolver{}
	}
	var ip net.IP
	if note.IP != "" {
		ip = net.ParseIP(note.IP)
	}
	v := mailauth.Judge(ctx, res, mailauth.Input{Raw: raw, IP: ip, Helo: note.Helo, MailFrom: note.MailFrom, PTRState: note.PTRState, PTR: note.PTR,
		BoxDomains: []string{s.domain()}, SeenBefore: seen, FirstFromDomain: !domainSeen, Now: time.Now()})
	if note.PTRState == "relay" {
		v.Reverse = "relay"
		v.Spam, v.SpamReasons = "low", []string{"a delivery report from the relay"}
	}
	vb, _ := json.Marshal(v)
	row := hw.MailRow{Direction: "in", State: "new", MessageID: p.MessageID, InReplyTo: p.InReplyTo, FromAddr: p.From, FromName: p.FromName,
		To: p.To, Cc: p.Cc, ReplyTo: p.ReplyTo, Subject: p.Subject, ReceivedAt: note.Received, BodyKind: p.BodyKind, RawSize: int64(len(raw)),
		Spam: v.Spam, RelayName: name}
	if row.To == nil {
		row.To = note.RcptTo
	}
	if !p.Date.IsZero() {
		row.SentAt = p.Date.Unix()
	}
	if row.ReceivedAt == 0 {
		row.ReceivedAt = time.Now().Unix()
	}
	switch {
	case len(p.References) > 0:
		row.ThreadKey = p.References[0]
	case p.InReplyTo != "":
		row.ThreadKey = p.InReplyTo
	default:
		row.ThreadKey = p.MessageID
	}
	if row.ThreadKey == "" {
		row.ThreadKey = name
	}
	body := p.Body
	if perr != nil {
		body = ""
		row.BodyKind = "none"
		row.LastErr = "the message did not parse: " + perr.Error()
	}
	id, err := m.store.MailInsert(m.slot, row, body, vb, sealPath)
	if err != nil {
		return fmt.Errorf("row: %w", err)
	}
	for _, a := range p.Attach {
		att := hw.MailAttach{MessageID: id, Name: a.Name, Mime: a.Mime, Bytes: int64(len(a.Bytes)), Kept: a.Kept}
		if !a.Kept {
			att.Name = a.Name + " (" + a.Why + ")"
		} else {
			att.SHA256 = sha256Hex(a.Bytes)
			att.Path = m.dir("att", fmt.Sprintf("%d-%s", id, a.Name))
			if err := os.WriteFile(att.Path, a.Bytes, 0o640); err != nil {
				m.lg.Warn("attachment not written", "fn", "mailer.keep", "err", err)
				att.Kept, att.Path = false, ""
			}
		}
		if _, err := m.store.MailAttachAdd(m.slot, att); err != nil {
			m.lg.Warn("attachment row", "fn", "mailer.keep", "err", err)
		}
	}
	m.notify(id, p, v, note)
	m.lg.Info("mail stored", "fn", "mailer.keep", "id", id, "spam", v.Spam, "attachments", len(p.Attach), "bytes", len(raw))
	return nil
}

// notify raises one notification per message, or one line for a burst from one sender.
func (m *mailer) notify(id int64, p parsedMail, v mailauth.Verdicts, note mailwire.Note) {
	from := p.From
	if from == "" {
		from = "an address the box could not read"
	}
	recent, _ := m.store.MailRecentFrom(m.slot, p.From, time.Now().Add(-time.Hour).Unix())
	if recent > burstPerHour {
		return
	}
	n := hw.Notification{Service: "ghost.noted", Kind: "mail", Link: fmt.Sprintf("mail:%d", id)}
	if recent == burstPerHour {
		n.Title = fmt.Sprintf("%d messages from %s in an hour", recent, from)
		n.Body = "the rest are kept quietly in MAIL"
	} else {
		n.Title = "mail from " + from
		subj := p.Subject
		if subj == "" {
			subj = "(no subject)"
		}
		n.Body = subj + " · " + v.Line()
		if note.PTRState == "relay" {
			n.Title = "a delivery report from the relay"
			n.Body = subj
		}
	}
	if err := m.store.Produce(m.slot, n); err != nil {
		m.lg.Warn("mail notification", "fn", "mailer.notify", "err", err)
	}
}

// outboxRequest is what secd drops for a tap on SEND in the app.
type outboxRequest struct {
	To        []string `json:"to"`
	Cc        []string `json:"cc"`
	Subject   string   `json:"subject"`
	Body      string   `json:"body"`
	ReplyTo   int64    `json:"replyTo"`   // the row replied to, 0 for none
	ForwardOf int64    `json:"forwardOf"` // the row forwarded, 0 for none
	Quote     bool     `json:"quote"`     // quote the replied message under the text
}

// outbox builds, signs, seals and posts each request, oldest first.
func (m *mailer) outbox(ctx context.Context, s mailSettings) error {
	dir := filepath.Join(m.mount, "noted", "outbox")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			names = append(names, e.Name())
		}
	}
	sort.Strings(names)
	for _, n := range names {
		path := filepath.Join(dir, n)
		b, err := os.ReadFile(path)
		if err != nil {
			continue
		}
		var req outboxRequest
		if err := json.Unmarshal(b, &req); err != nil {
			m.lg.Warn("outbox request unreadable", "fn", "mailer.outbox", "file", n)
			os.Remove(path)
			continue
		}
		if err := m.send(ctx, s, req); err != nil {
			m.lg.Warn("mail not sent", "fn", "mailer.outbox", "file", n, "err", err)
		}
		os.Remove(path)
	}
	return nil
}

func (m *mailer) relayPub(ctx context.Context, s mailSettings) ([]byte, error) {
	if s.RelayPub != "" {
		if k, err := mailseal.ParseKey(s.RelayPub); err == nil {
			return k, nil
		}
	}
	code, b, err := m.call(ctx, s, http.MethodGet, "/key", nil, 4096)
	if err != nil || code != http.StatusOK {
		return nil, fmt.Errorf("relay key: %d %v", code, err)
	}
	var kr struct {
		Pub      string `json:"pub"`
		Hostname string `json:"hostname"`
	}
	if err := json.Unmarshal(b, &kr); err != nil {
		return nil, err
	}
	k, err := mailseal.ParseKey(kr.Pub)
	if err != nil {
		return nil, err
	}
	_ = m.store.SetSetting(m.slot, "mail_relay_pub", kr.Pub)
	_ = m.store.SetSetting(m.slot, "mail_relay_host", kr.Hostname)
	return k, nil
}

func (m *mailer) send(ctx context.Context, s mailSettings, req outboxRequest) error {
	o := outgoing{FromAddr: s.address(), FromName: s.FromName, To: cleanAddrs(req.To), Cc: cleanAddrs(req.Cc), Subject: strings.TrimSpace(req.Subject), Body: req.Body, Domain: s.domain()}
	if len(o.To) == 0 {
		return errors.New("no recipient")
	}
	threadKey := ""
	if req.ReplyTo > 0 {
		orig, err := m.store.MailGet(m.slot, req.ReplyTo)
		if err != nil || orig == nil {
			return errors.New("the replied message is gone")
		}
		o.InReplyTo = orig.MessageID
		if orig.MessageID != "" {
			o.Refs = append(o.Refs, orig.MessageID)
		}
		threadKey = orig.ThreadKey
		if o.Subject == "" {
			o.Subject = replySubject(orig.Subject)
		}
		if req.Quote {
			o.Body = strings.TrimRight(o.Body, "\n") + "\n\n" + quoteForReply(orig.FromName, orig.FromAddr, time.Unix(orig.SentAt, 0), orig.Body)
		}
	}
	if req.ForwardOf > 0 {
		orig, err := m.store.MailGet(m.slot, req.ForwardOf)
		if err != nil || orig == nil {
			return errors.New("the forwarded message is gone")
		}
		if o.Subject == "" {
			o.Subject = forwardSubject(orig.Subject)
		}
		o.Body = strings.TrimRight(o.Body, "\n") + "\n\n" + forwardHeader(orig.FromName, orig.FromAddr, orig.To, time.Unix(orig.SentAt, 0), orig.Subject) + orig.Body
		for _, a := range orig.Attach {
			if !a.Kept || a.Path == "" {
				continue
			}
			b, err := os.ReadFile(a.Path)
			if err != nil {
				continue
			}
			o.Attach = append(o.Attach, parsedAttach{Name: a.Name, Mime: a.Mime, Bytes: b, Kept: true})
		}
	}
	if o.Subject == "" {
		o.Subject = "(no subject)"
	}
	now := time.Now()
	raw, msgID, err := buildMessage(o, now)
	if err != nil {
		return err
	}
	signer := mailauth.Signer{Domain: s.domain(), Selector: s.RelayBox + "-rsa", EdSelector: s.RelayBox + "-ed", RSA: m.keys.rsa, Ed25519: m.keys.ed}
	signed, err := signer.Sign(raw)
	if err != nil {
		return fmt.Errorf("sign: %w", err)
	}
	if threadKey == "" {
		threadKey = msgID
	}
	row := hw.MailRow{Direction: "out", State: "queued", MessageID: msgID, InReplyTo: o.InReplyTo, ThreadKey: threadKey, FromAddr: o.FromAddr, FromName: o.FromName,
		To: o.To, Cc: o.Cc, Subject: o.Subject, SentAt: now.Unix(), ReceivedAt: now.Unix(), BodyKind: "text", RawSize: int64(len(signed)), RelayName: "out-" + strconv.FormatInt(now.UnixNano(), 10)}
	id, err := m.store.MailInsert(m.slot, row, o.Body, nil, "")
	if err != nil {
		return fmt.Errorf("row: %w", err)
	}
	for _, a := range o.Attach {
		att := hw.MailAttach{MessageID: id, Name: a.Name, Mime: a.Mime, Bytes: int64(len(a.Bytes)), Kept: true, SHA256: sha256Hex(a.Bytes), Path: m.dir("att", fmt.Sprintf("%d-%s", id, a.Name))}
		if err := os.WriteFile(att.Path, a.Bytes, 0o640); err == nil {
			m.store.MailAttachAdd(m.slot, att)
		}
	}
	pub, err := m.relayPub(ctx, s)
	if err != nil {
		m.store.MailSetState(m.slot, id, "failed", err.Error())
		return err
	}
	sealed, err := mailseal.SealBytes(signed, pub)
	if err != nil {
		m.store.MailSetState(m.slot, id, "failed", err.Error())
		return err
	}
	var rcpts []string
	rcpts = append(rcpts, o.To...)
	rcpts = append(rcpts, o.Cc...)
	body, _ := json.Marshal(mailwire.Outbound{From: o.FromAddr, To: rcpts, Sealed: sealed})
	code, resp, err := m.call(ctx, s, http.MethodPost, "/out", body, 4096)
	if err != nil {
		m.store.MailSetState(m.slot, id, "failed", "relay unreachable: "+err.Error())
		return err
	}
	if code != http.StatusOK {
		msg := strings.TrimSpace(string(resp))
		if msg == "" {
			msg = fmt.Sprintf("relay answered %d", code)
		}
		m.store.MailSetState(m.slot, id, "failed", msg)
		return errors.New(msg)
	}
	var ans struct {
		Name string `json:"name"`
	}
	_ = json.Unmarshal(resp, &ans)
	if ans.Name != "" {
		m.store.MailSetRelayName(m.slot, id, ans.Name)
	}
	m.lg.Info("mail posted to the relay", "fn", "mailer.send", "id", id, "rcpts", len(rcpts), "bytes", len(signed))
	return nil
}

func cleanAddrs(in []string) []string {
	var out []string
	seen := map[string]bool{}
	for _, a := range in {
		a = strings.TrimSpace(a)
		if a == "" {
			continue
		}
		if i := strings.LastIndexByte(a, '<'); i >= 0 {
			a = strings.TrimSuffix(a[i+1:], ">")
		}
		a = strings.ToLower(strings.TrimSpace(a))
		if !addrRe.MatchString(a) || seen[a] {
			continue
		}
		seen[a] = true
		out = append(out, a)
	}
	return out
}

// outStatus asks the relay about every message still queued and records what it says.
func (m *mailer) outStatus(ctx context.Context, s mailSettings) error {
	rows, err := m.store.MailOutQueued(m.slot)
	if err != nil {
		return err
	}
	for _, r := range rows {
		if !strings.HasSuffix(r.RelayName, ".seal") {
			if time.Since(time.Unix(r.ReceivedAt, 0)) > time.Hour {
				m.store.MailSetState(m.slot, r.ID, "failed", "never reached the relay")
			}
			continue
		}
		code, b, err := m.call(ctx, s, http.MethodGet, "/out/"+r.RelayName, nil, 1<<16)
		if err != nil {
			return err
		}
		if code == http.StatusNotFound {
			if time.Since(time.Unix(r.ReceivedAt, 0)) > 48*time.Hour {
				m.store.MailSetState(m.slot, r.ID, "failed", "the relay has no record of it")
			}
			continue
		}
		var j struct {
			State   string            `json:"state"`
			Failed  map[string]string `json:"failed"`
			LastErr string            `json:"lastErr"`
		}
		if err := json.Unmarshal(b, &j); err != nil {
			continue
		}
		switch j.State {
		case "sent":
			m.store.MailSetState(m.slot, r.ID, "sent", "")
		case "bounced":
			var parts []string
			for k, v := range j.Failed {
				parts = append(parts, k+": "+v)
			}
			sort.Strings(parts)
			m.store.MailSetState(m.slot, r.ID, "bounced", strings.Join(parts, "; "))
		}
	}
	return nil
}

// dnsRecords are the four records the person pastes at their DNS provider, written to
// <mount>/noted/mail/dns.json for secd and the app, and printed by `ghost-cli ghost.noted mail-dns`.
type dnsRecord struct {
	Name  string `json:"name"`
	Type  string `json:"type"`
	Value string `json:"value"`
	Why   string `json:"why"`
}

func (m *mailer) dnsRecords(s mailSettings) []dnsRecord {
	d := s.domain()
	if d == "" || m.keys == nil || s.RelayBox == "" {
		return nil
	}
	host := s.RelayHost
	if host == "" {
		host = "mx.localghost.ai"
	}
	relayDomain := host
	if i := strings.IndexByte(host, '.'); i >= 0 {
		relayDomain = host[i+1:]
	}
	rsaName, rsaRec, _ := mailauth.DNSRecord(d, s.RelayBox+"-rsa", &m.keys.rsa.PublicKey, nil)
	edName, edRec, _ := mailauth.DNSRecord(d, s.RelayBox+"-ed", nil, m.keys.ed.Public().(ed25519.PublicKey))
	keys := []dnsRecord{
		{Name: rsaName + ".", Type: "TXT", Value: rsaRec, Why: "the box's RSA signing key"},
		{Name: edName + ".", Type: "TXT", Value: edRec, Why: "the box's Ed25519 signing key"},
	}
	if s.Shared {
		// the domain is the relay's: LocalGhost publishes these two under it from the enrol line;
		// the MX, SPF and DMARC of the shared domain are LocalGhost's once for everyone
		for i := range keys {
			keys[i].Why += " (LocalGhost publishes it; it is in the relay's line)"
		}
		return keys
	}
	return append([]dnsRecord{
		{Name: d + ".", Type: "MX", Value: "10 " + host + ".", Why: "mail for the domain goes to the relay"},
		{Name: d + ".", Type: "TXT", Value: `"v=spf1 include:_spf.` + relayDomain + ` -all"`, Why: "the relay's addresses may send for the domain"},
	}, append(keys, dnsRecord{Name: "_dmarc." + d + ".", Type: "TXT", Value: `"v=DMARC1; p=reject; adkim=s; aspf=r"`, Why: "receivers refuse mail claiming the domain that the box did not sign"})...)
}

func (m *mailer) writeDNS() {
	s := m.settings()
	recs := m.dnsRecords(s)
	out := map[string]any{"domain": s.domain(), "address": s.address(), "shared": s.Shared, "records": recs, "sealPub": hex.EncodeToString(m.keys.sealPub)}
	if line, err := m.enrolLine(s, false); err == nil {
		out["enrolLine"] = line
	}
	if w := m.domainWarning(s); w != "" {
		out["warning"] = w
	}
	b, _ := json.Marshal(out)
	_ = os.WriteFile(m.dir("dns.json.part"), b, 0o640)
	_ = os.Rename(m.dir("dns.json.part"), m.dir("dns.json"))
}

// domainWarning is the one thing a mail domain must not be: a name with an address record,
// because every sender has the name and one lookup turns it into the address it points at. A
// box reached at lgs.example.com must take its mail at a name with an MX record and nothing
// else (ghost.example.com, say). Looked up once when the domain is set, with the DNS switch.
func (m *mailer) domainWarning(s mailSettings) string {
	d := s.domain()
	if d == "" || s.Shared || !s.DNSChecks {
		return ""
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	ips, err := net.DefaultResolver.LookupIP(ctx, "ip", d)
	if err != nil || len(ips) == 0 {
		return ""
	}
	var list []string
	for _, ip := range ips {
		list = append(list, ip.String())
	}
	return d + " has an address record (" + strings.Join(list, ", ") + "); every sender has the name and one lookup gives them that address. A domain whose zone holds the box's address anywhere is the wrong one; the relay's shared domain tells a sender nothing about you."
}

// enrolLine is the line for the relay's boxes.conf. With make, a token is made and kept when
// there is none; without, a missing token is an error (secd makes it on the status call).
func (m *mailer) enrolLine(s mailSettings, make bool) (string, error) {
	if s.domain() == "" {
		return "", errors.New("set the address first (mail-address address=...)")
	}
	if s.RelayBox == "" {
		return "", errors.New("set the box id first (mail-relay box=...)")
	}
	token := s.Token
	if token == "" {
		if !make {
			return "", errors.New("no token yet")
		}
		var r [32]byte
		if _, err := rand.Read(r[:]); err != nil {
			return "", err
		}
		token = hex.EncodeToString(r[:])
		if err := m.store.SetSetting(m.slot, "mail_relay_token", token); err != nil {
			return "", err
		}
	}
	rsaDer, err := x509.MarshalPKIXPublicKey(&m.keys.rsa.PublicKey)
	if err != nil {
		return "", err
	}
	who := "domains=" + s.domain()
	if s.Shared {
		who = "addresses=" + s.Address
	}
	return fmt.Sprintf("box=%s %s pub=%s token=%s dkim_rsa=%s dkim_ed=%s", s.RelayBox, who, hex.EncodeToString(m.keys.sealPub), sha256Hex([]byte(token)),
		base64.StdEncoding.EncodeToString(rsaDer), base64.StdEncoding.EncodeToString(m.keys.ed.Public().(ed25519.PublicKey))), nil
}

// bindMailCtl registers the mail commands on the control socket.
func (m *mailer) bindCtl(ctl *ctlsock.Server) {
	ctl.Handle("mail", func(json.RawMessage) (ctlsock.Response, error) {
		m.mu.Lock()
		st := m.last
		m.mu.Unlock()
		s := m.settings()
		rows, _ := m.store.MailList(m.slot, "all", "", 20, 0)
		out := map[string]any{"status": st, "settings": s, "recent": rows}
		data, _ := json.Marshal(out)
		return ctlsock.Response{OK: true, Data: data}, nil
	})
	ctl.Handle("mail-pull", func(json.RawMessage) (ctlsock.Response, error) {
		select {
		case m.force <- struct{}{}:
		default:
		}
		return ctlsock.Response{OK: true, Text: "a pass is on its way"}, nil
	})
	ctl.Handle("mail-address", func(args json.RawMessage) (ctlsock.Response, error) {
		var a struct {
			Address string `json:"address"`
			Shared  bool   `json:"shared"`
			Own     bool   `json:"own"`
		}
		_ = json.Unmarshal(args, &a)
		addr := strings.ToLower(strings.TrimSpace(a.Address))
		if !validAddress(addr) {
			return ctlsock.Response{OK: false, Err: "address=<local@domain>, e.g. vlad@ghost.localghost.ai (shared=1) or agent@ghost.example.org (own=1)"}, nil
		}
		if err := m.store.SetSetting(m.slot, "mail_address", addr); err != nil {
			return ctlsock.Response{OK: false, Err: err.Error()}, nil
		}
		if a.Shared || a.Own {
			m.store.SetSetting(m.slot, "mail_shared", map[bool]string{true: "on", false: "off"}[a.Shared])
		}
		m.writeDNS()
		text := "the address is " + addr + "; `mail-dns` prints the records, `mail-enrol` the relay's line"
		if w := m.domainWarning(m.settings()); w != "" {
			text += "\nWARNING: " + w
		}
		return ctlsock.Response{OK: true, Text: text}, nil
	})
	ctl.Handle("mail-relay", func(args json.RawMessage) (ctlsock.Response, error) {
		var a struct {
			URL   string `json:"url"`
			Box   string `json:"box"`
			Token string `json:"token"`
		}
		_ = json.Unmarshal(args, &a)
		if a.URL != "" {
			if !strings.HasPrefix(a.URL, "https://") {
				return ctlsock.Response{OK: false, Err: "url= must be https://"}, nil
			}
			m.store.SetSetting(m.slot, "mail_relay_url", strings.TrimRight(a.URL, "/"))
			m.store.SetSetting(m.slot, "mail_relay_pub", "")
		}
		if a.Box != "" {
			if !mailwire.BoxRe.MatchString(a.Box) {
				return ctlsock.Response{OK: false, Err: "box= is lower case letters, digits and dashes, 3 to 32"}, nil
			}
			m.store.SetSetting(m.slot, "mail_relay_box", a.Box)
		}
		if a.Token != "" {
			m.store.SetSetting(m.slot, "mail_relay_token", a.Token)
		}
		s := m.settings()
		return ctlsock.Response{OK: true, Text: fmt.Sprintf("relay %s box %s token %s", s.RelayURL, s.RelayBox, map[bool]string{true: "set", false: "unset"}[s.Token != ""])}, nil
	})
	ctl.Handle("mail-enrol", func(json.RawMessage) (ctlsock.Response, error) {
		line, err := m.enrolLine(m.settings(), true)
		if err != nil {
			return ctlsock.Response{OK: false, Err: err.Error()}, nil
		}
		return ctlsock.Response{OK: true, Text: "add this line to the relay's /etc/ghost-mail/boxes.conf:\n" + line}, nil
	})
	ctl.Handle("mail-dns", func(json.RawMessage) (ctlsock.Response, error) {
		recs := m.dnsRecords(m.settings())
		if recs == nil {
			return ctlsock.Response{OK: false, Err: "set the address and the box id first (mail-address, mail-relay box=)"}, nil
		}
		var sb strings.Builder
		for _, r := range recs {
			fmt.Fprintf(&sb, "%s  %s  %s\n    (%s)\n", r.Name, r.Type, r.Value, r.Why)
		}
		return ctlsock.Response{OK: true, Text: sb.String()}, nil
	})
	ctl.Handle("mail-from", func(args json.RawMessage) (ctlsock.Response, error) {
		var a struct {
			Name string `json:"name"`
		}
		_ = json.Unmarshal(args, &a)
		m.store.SetSetting(m.slot, "mail_from_name", strings.TrimSpace(a.Name))
		return ctlsock.Response{OK: true, Text: "mail leaves as " + strings.TrimSpace(a.Name) + " <" + m.settings().address() + ">"}, nil
	})
	ctl.Handle("mail-checks", func(args json.RawMessage) (ctlsock.Response, error) {
		var a struct {
			On  bool `json:"on"`
			Off bool `json:"off"`
		}
		_ = json.Unmarshal(args, &a)
		v := "on"
		if a.Off {
			v = "off"
		}
		m.store.SetSetting(m.slot, "mail_dns_checks", v)
		return ctlsock.Response{OK: true, Text: "DNS checks on received mail: " + v}, nil
	})
}
