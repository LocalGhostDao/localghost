package secd

// MAIL , the app's side of docs/MAIL.md. secd reads the rows ghost.noted wrote (the inbox, a
// message with its verdicts and attachments, a thread), hands a tap on SEND to noted as a file
// in <mount>/noted/outbox (noted builds, signs, seals and posts; secd never touches a key), and
// keeps the settings the person sets in SETTINGS › MAIL. Nothing here sends anything anywhere.

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/mailwire"
)

func (s *Server) mailMount(mounted int) string {
	return filepath.Join(s.cfg.StateDir, "mnt", fmt.Sprintf("slot%d", mounted))
}

// handleMailList , GET /v1/mail?folder=inbox|sent|archive|all&q=&limit=&before=
func (s *Server) handleMailList(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	q := r.URL.Query()
	limit, _ := strconv.Atoi(q.Get("limit"))
	before, _ := strconv.ParseInt(q.Get("before"), 10, 64)
	rows, err := s.notif.MailList(mounted, q.Get("folder"), q.Get("q"), limit, before)
	if err != nil {
		secdLog.Warn("mail list failed", "fn", "handleMailList", "err", err)
		s.appearsDown(w)
		return
	}
	in, unseen, out, queued, _ := s.notif.MailCounts(mounted)
	writeJSON(w, map[string]any{"ok": true, "messages": rows, "counts": map[string]int{"in": in, "unseen": unseen, "sent": out, "waiting": queued}})
}

// handleMailMessage , GET /v1/mail/message?id=N , one message whole, with its thread.
func (s *Server) handleMailMessage(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	if id <= 0 {
		http.Error(w, "id=", http.StatusBadRequest)
		return
	}
	m, err := s.notif.MailGet(mounted, id)
	if err != nil {
		s.appearsDown(w)
		return
	}
	if m == nil {
		writeErr(w, http.StatusNotFound, "no such message")
		return
	}
	thread, _ := s.notif.MailThread(mounted, m.ThreadKey)
	if m.Direction == "in" && m.State == "new" {
		_ = s.notif.MailSetState(mounted, id, "seen", m.LastErr)
		m.State = "seen"
	}
	writeJSON(w, map[string]any{"ok": true, "message": m, "thread": thread})
}

// handleMailState , POST /v1/mail/state {id, state} , seen, new, archived.
func (s *Server) handleMailState(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	var req struct {
		ID    int64  `json:"id"`
		State string `json:"state"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || req.ID <= 0 {
		http.Error(w, "{id, state}", http.StatusBadRequest)
		return
	}
	switch req.State {
	case "seen", "new", "archived":
	default:
		http.Error(w, "state is seen, new or archived", http.StatusBadRequest)
		return
	}
	m, err := s.notif.MailGet(mounted, req.ID)
	if err != nil || m == nil {
		writeErr(w, http.StatusNotFound, "no such message")
		return
	}
	if err := s.notif.MailSetState(mounted, req.ID, req.State, m.LastErr); err != nil {
		s.appearsDown(w)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleMailDelete , POST /v1/mail/delete {id} , the row, its attachments and the sealed original.
func (s *Server) handleMailDelete(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	var req struct {
		ID int64 `json:"id"`
	}
	if err := json.NewDecoder(io.LimitReader(r.Body, 4096)).Decode(&req); err != nil || req.ID <= 0 {
		http.Error(w, "{id}", http.StatusBadRequest)
		return
	}
	atts, seal, err := s.notif.MailDelete(mounted, req.ID)
	if err != nil {
		s.appearsDown(w)
		return
	}
	root := filepath.Join(s.mailMount(mounted), "noted", "mail")
	for _, a := range atts {
		if a.Path != "" && strings.HasPrefix(a.Path, root) {
			_ = os.Remove(a.Path)
		}
	}
	if seal != "" && strings.HasPrefix(seal, root) {
		_ = os.Remove(seal)
	}
	writeJSON(w, map[string]any{"ok": true})
}

// handleMailAttachment , GET /v1/mail/attachment?id=N , the bytes, as the parser kept them.
func (s *Server) handleMailAttachment(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	a, err := s.notif.MailAttachment(mounted, id)
	if err != nil || a == nil || !a.Kept || a.Path == "" {
		writeErr(w, http.StatusNotFound, "no such attachment, or one the box did not keep")
		return
	}
	root := filepath.Join(s.mailMount(mounted), "noted", "mail", "att")
	if !strings.HasPrefix(filepath.Clean(a.Path), root) {
		writeErr(w, http.StatusNotFound, "no such attachment")
		return
	}
	f, err := os.Open(a.Path)
	if err != nil {
		writeErr(w, http.StatusNotFound, "the attachment's file is gone")
		return
	}
	defer f.Close()
	mt := a.Mime
	if mt == "" || strings.HasPrefix(mt, "text/html") || strings.Contains(mt, "javascript") || strings.Contains(mt, "svg") {
		mt = "application/octet-stream" // never rendered by a viewer as a page
	}
	w.Header().Set("Content-Type", mt)
	w.Header().Set("Content-Disposition", "attachment; filename=\""+strings.ReplaceAll(a.Name, "\"", "")+"\"")
	w.Header().Set("X-Content-Type-Options", "nosniff")
	_, _ = io.Copy(w, f)
}

// handleMailSend , POST /v1/mail/send {to[], cc[], subject, body, replyTo, forwardOf, quote} ,
// the person's tap. secd writes the request into noted's outbox; noted does the rest on its next
// pass (within two minutes). The row appears in SENT as "queued" once noted has it.
func (s *Server) handleMailSend(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 1<<20))
	if err != nil {
		http.Error(w, "a message under 1 MB", http.StatusBadRequest)
		return
	}
	var req struct {
		To        []string `json:"to"`
		Cc        []string `json:"cc"`
		Subject   string   `json:"subject"`
		Body      string   `json:"body"`
		ReplyTo   int64    `json:"replyTo"`
		ForwardOf int64    `json:"forwardOf"`
		Quote     bool     `json:"quote"`
	}
	if err := json.Unmarshal(body, &req); err != nil || len(req.To) == 0 {
		http.Error(w, "{to[], subject, body}", http.StatusBadRequest)
		return
	}
	addr, _ := s.notif.GetSetting(mounted, "mail_address")
	url, _ := s.notif.GetSetting(mounted, "mail_relay_url")
	if addr == "" || url == "" {
		writeErr(w, http.StatusConflict, "mail is not set up: the address and the relay in SETTINGS")
		return
	}
	outbox := filepath.Join(s.mailMount(mounted), "noted", "outbox")
	if err := os.MkdirAll(outbox, 0o750); err != nil {
		s.appearsDown(w)
		return
	}
	name := fmt.Sprintf("%d.json", time.Now().UnixNano())
	path := filepath.Join(outbox, name)
	if err := os.WriteFile(path+".part", body, 0o640); err != nil {
		s.appearsDown(w)
		return
	}
	// secd runs as root; noted runs as the run user and reads its own outbox
	if s.cfg.RunUser != "" {
		if u, uerr := user.Lookup(s.cfg.RunUser); uerr == nil {
			uid, _ := strconv.Atoi(u.Uid)
			gid, _ := strconv.Atoi(u.Gid)
			_ = os.Chown(path+".part", uid, gid)
			_ = os.Chown(outbox, uid, gid)
		}
	}
	if err := os.Rename(path+".part", path); err != nil {
		_ = os.Remove(path + ".part")
		s.appearsDown(w)
		return
	}
	secdLog.Info("mail handed to noted", "fn", "handleMailSend", "rcpts", len(req.To)+len(req.Cc), "reply", req.ReplyTo > 0, "forward", req.ForwardOf > 0)
	writeJSON(w, map[string]any{"ok": true, "queued": name})
}

// mailSettingsOut is what SETTINGS › MAIL shows and sets. The token never travels to the phone:
// the box makes it, and the enrol line carries its hash for the relay.
type mailSettingsOut struct {
	Address   string `json:"address"`
	Domain    string `json:"domain"`
	Shared    bool   `json:"shared"`
	RelayURL  string `json:"relayUrl"`
	RelayBox  string `json:"relayBox"`
	RelayHost string `json:"relayHost"`
	FromName  string `json:"fromName"`
	DNSChecks bool   `json:"dnsChecks"`
	TokenSet  bool   `json:"tokenSet"`
}

func (s *Server) mailSettings(mounted int) mailSettingsOut {
	get := func(k string) string {
		v, _ := s.notif.GetSetting(mounted, k)
		return v
	}
	o := mailSettingsOut{Address: get("mail_address"), Shared: get("mail_shared") == "on", RelayURL: get("mail_relay_url"), RelayBox: get("mail_relay_box"), RelayHost: get("mail_relay_host"),
		FromName: get("mail_from_name"), DNSChecks: get("mail_dns_checks") != "off", TokenSet: get("mail_relay_token") != ""}
	if at := strings.LastIndexByte(o.Address, '@'); at >= 0 {
		o.Domain = o.Address[at+1:]
	}
	return o
}

// handleMailStatus , GET /v1/mail/status , the settings, noted's status line, the DNS records to
// paste, and the line for the relay's boxes.conf.
func (s *Server) handleMailStatus(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	out := map[string]any{"ok": true, "settings": s.mailSettings(mounted)}
	root := filepath.Join(s.mailMount(mounted), "noted", "mail")
	if b, err := os.ReadFile(filepath.Join(root, "status.json")); err == nil && json.Valid(b) {
		out["status"] = json.RawMessage(b)
	}
	var dns struct {
		Domain    string          `json:"domain"`
		Records   json.RawMessage `json:"records"`
		SealPub   string          `json:"sealPub"`
		Warning   string          `json:"warning"`
		EnrolLine string          `json:"enrolLine"`
	}
	if b, err := os.ReadFile(filepath.Join(root, "dns.json")); err == nil && json.Unmarshal(b, &dns) == nil {
		if len(dns.Records) > 0 {
			out["dns"] = dns.Records
		}
		if dns.Warning != "" {
			out["warning"] = dns.Warning
		}
		if dns.EnrolLine != "" {
			out["enrolLine"] = dns.EnrolLine
		}
	}
	// the token is made here when the box has none; noted puts it into the relay's line on its
	// next pass (within two minutes), and until then the line is not shown
	if token, _ := s.notif.GetSetting(mounted, "mail_relay_token"); token == "" {
		var rb [32]byte
		if _, err := rand.Read(rb[:]); err == nil {
			_ = s.notif.SetSetting(mounted, "mail_relay_token", hex.EncodeToString(rb[:]))
		}
	}
	writeJSON(w, out)
}

// handleMailSettings , POST /v1/mail/settings {address, shared, relayUrl, relayBox, fromName, dnsChecks}.
// An absent field is left as it is; "" clears it.
func (s *Server) handleMailSettings(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	var req map[string]any
	if err := json.NewDecoder(io.LimitReader(r.Body, 8192)).Decode(&req); err != nil {
		http.Error(w, "a JSON object", http.StatusBadRequest)
		return
	}
	str := func(k string) (string, bool) {
		v, ok := req[k]
		if !ok {
			return "", false
		}
		sv, _ := v.(string)
		return strings.TrimSpace(sv), true
	}
	if a, ok := str("address"); ok {
		a = strings.ToLower(a)
		at := strings.LastIndexByte(a, '@')
		if a != "" && (at <= 0 || at == len(a)-1 || !strings.Contains(a[at+1:], ".") || strings.ContainsAny(a, " /:<>,;\"")) {
			http.Error(w, "the address is local@domain, like vlad@ghost.localghost.ai", http.StatusBadRequest)
			return
		}
		_ = s.notif.SetSetting(mounted, "mail_address", a)
	}
	if v, ok := req["shared"]; ok {
		on, _ := v.(bool)
		val := "off"
		if on {
			val = "on"
		}
		_ = s.notif.SetSetting(mounted, "mail_shared", val)
	}
	if u, ok := str("relayUrl"); ok {
		if u != "" && !strings.HasPrefix(u, "https://") {
			http.Error(w, "the relay is an https:// address", http.StatusBadRequest)
			return
		}
		old, _ := s.notif.GetSetting(mounted, "mail_relay_url")
		_ = s.notif.SetSetting(mounted, "mail_relay_url", strings.TrimRight(u, "/"))
		if old != strings.TrimRight(u, "/") {
			_ = s.notif.SetSetting(mounted, "mail_relay_pub", "")
		}
	}
	if b, ok := str("relayBox"); ok {
		if b != "" && !mailwire.BoxRe.MatchString(b) {
			http.Error(w, "the box id is lower case letters, digits and dashes, 3 to 32", http.StatusBadRequest)
			return
		}
		_ = s.notif.SetSetting(mounted, "mail_relay_box", b)
	}
	if n, ok := str("fromName"); ok {
		_ = s.notif.SetSetting(mounted, "mail_from_name", n)
	}
	if v, ok := req["dnsChecks"]; ok {
		on, _ := v.(bool)
		val := "on"
		if !on {
			val = "off"
		}
		_ = s.notif.SetSetting(mounted, "mail_dns_checks", val)
	}
	if v, ok := req["newToken"]; ok {
		if yes, _ := v.(bool); yes {
			var rb [32]byte
			if _, err := rand.Read(rb[:]); err == nil {
				_ = s.notif.SetSetting(mounted, "mail_relay_token", hex.EncodeToString(rb[:]))
			}
		}
	}
	writeJSON(w, map[string]any{"ok": true, "settings": s.mailSettings(mounted)})
}
