package hw

// MAIL , the rows ghost.noted writes for what arrived through the relay and what the person
// sent, and what secd reads for the app's MAIL screen. A message is one row; its attachments
// are rows of their own with the bytes under <mount>/noted/mail/att/. The sealed original stays
// on the volume at seal_path, so nothing is ever lost to a parser's opinion.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
)

// MailRow is one message as the app sees it.
type MailRow struct {
	ID         int64           `json:"id"`
	Direction  string          `json:"direction"` // in, out
	State      string          `json:"state"`     // in: new, seen, archived; out: queued, sent, bounced, failed
	MessageID  string          `json:"messageId"`
	InReplyTo  string          `json:"inReplyTo,omitempty"`
	ThreadKey  string          `json:"threadKey"`
	FromAddr   string          `json:"from"`
	FromName   string          `json:"fromName,omitempty"`
	To         []string        `json:"to"`
	Cc         []string        `json:"cc,omitempty"`
	ReplyTo    string          `json:"replyTo,omitempty"`
	Subject    string          `json:"subject"`
	SentAt     int64           `json:"sentAt"`
	ReceivedAt int64           `json:"receivedAt"`
	Body       string          `json:"body,omitempty"`
	BodyKind   string          `json:"bodyKind"` // text, html-reduced, none
	RawSize    int64           `json:"rawSize"`
	Verdicts   json.RawMessage `json:"verdicts,omitempty"`
	Spam       string          `json:"spam,omitempty"`
	RelayName  string          `json:"-"`
	LastErr    string          `json:"lastErr,omitempty"`
	Attach     []MailAttach    `json:"attachments,omitempty"`
	Preview    string          `json:"preview,omitempty"` // the first line or so, for the list
	AttachN    int             `json:"attachmentCount"`
}

// MailAttach is one attachment's row.
type MailAttach struct {
	ID        int64  `json:"id"`
	MessageID int64  `json:"messageId"`
	Name      string `json:"name"`
	Mime      string `json:"mime"`
	Bytes     int64  `json:"bytes"`
	Kept      bool   `json:"kept"`
	Path      string `json:"-"`
	SHA256    string `json:"sha256,omitempty"`
}

func jsonList(ss []string) string {
	if len(ss) == 0 {
		return "[]"
	}
	b, _ := json.Marshal(ss)
	return string(b)
}

func listOf(s *string) []string {
	if s == nil || *s == "" {
		return nil
	}
	var out []string
	_ = json.Unmarshal([]byte(*s), &out)
	return out
}

func mstr(s *string) string {
	if s == nil {
		return ""
	}
	return *s
}

func i64(s *string) int64 {
	if s == nil {
		return 0
	}
	n, _ := strconv.ParseInt(*s, 10, 64)
	return n
}

const mailCols = "id, direction, state, message_id, in_reply_to, thread_key, from_addr, from_name, to_addrs, cc_addrs, reply_to, subject, sent_at, received_at, body_kind, raw_size, spam, relay_name, last_err, left(body, 160), (SELECT count(*) FROM mail_attachments a WHERE a.message_id = m.id)"

func scanMail(v []*string) MailRow {
	r := MailRow{ID: i64(v[0]), Direction: mstr(v[1]), State: mstr(v[2]), MessageID: mstr(v[3]), InReplyTo: mstr(v[4]), ThreadKey: mstr(v[5]),
		FromAddr: mstr(v[6]), FromName: mstr(v[7]), To: listOf(v[8]), Cc: listOf(v[9]), ReplyTo: mstr(v[10]), Subject: mstr(v[11]),
		SentAt: i64(v[12]), ReceivedAt: i64(v[13]), BodyKind: mstr(v[14]), RawSize: i64(v[15]), Spam: mstr(v[16]), RelayName: mstr(v[17]), LastErr: mstr(v[18])}
	p := strings.TrimSpace(mstr(v[19]))
	if i := strings.IndexByte(p, '\n'); i >= 0 {
		p = p[:i]
	}
	r.Preview = p
	r.AttachN = int(i64(v[20]))
	if r.To == nil {
		r.To = []string{}
	}
	return r
}

// MailList lists messages newest first. folder: "inbox" (in, not archived), "sent" (out),
// "archive", "all". q narrows by sender, subject or body. beforeID pages.
func (s *NotifStore) MailList(slot int, folder, q string, limit int, beforeID int64) ([]MailRow, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 50
	}
	where := []string{"TRUE"}
	var args []any
	switch folder {
	case "inbox", "":
		where = append(where, "direction = 'in' AND state <> 'archived'")
	case "sent":
		where = append(where, "direction = 'out'")
	case "archive":
		where = append(where, "state = 'archived'")
	}
	if beforeID > 0 {
		args = append(args, beforeID)
		where = append(where, fmt.Sprintf("id < $%d", len(args)))
	}
	if q = strings.TrimSpace(q); q != "" {
		args = append(args, "%"+strings.ToLower(q)+"%")
		n := len(args)
		where = append(where, fmt.Sprintf("(lower(from_addr) LIKE $%d OR lower(from_name) LIKE $%d OR lower(subject) LIKE $%d OR lower(body) LIKE $%d OR lower(to_addrs) LIKE $%d)", n, n, n, n, n))
	}
	args = append(args, limit)
	sql := "SELECT " + mailCols + " FROM mail_messages m WHERE " + strings.Join(where, " AND ") + fmt.Sprintf(" ORDER BY id DESC LIMIT $%d", len(args))
	rows, err := c.Query(sql, args...)
	if err != nil {
		return nil, err
	}
	out := []MailRow{}
	for _, v := range rows.Vals {
		if len(v) < 21 {
			continue
		}
		out = append(out, scanMail(v))
	}
	return out, nil
}

// MailGet is one message with its body, verdicts and attachments.
func (s *NotifStore) MailGet(slot int, id int64) (*MailRow, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query("SELECT "+mailCols+", body, verdicts FROM mail_messages m WHERE id = $1", id)
	if err != nil {
		return nil, err
	}
	if len(rows.Vals) == 0 || len(rows.Vals[0]) < 23 {
		return nil, nil
	}
	v := rows.Vals[0]
	r := scanMail(v)
	r.Body = mstr(v[21])
	if vd := mstr(v[22]); vd != "" && json.Valid([]byte(vd)) {
		r.Verdicts = json.RawMessage(vd)
	}
	atts, err := s.MailAttachments(slot, id)
	if err != nil {
		return nil, err
	}
	r.Attach = atts
	return &r, nil
}

// MailAttachments lists a message's attachments.
func (s *NotifStore) MailAttachments(slot int, msgID int64) ([]MailAttach, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query("SELECT id, message_id, name, mime, bytes, kept, path, sha256 FROM mail_attachments WHERE message_id = $1 ORDER BY id", msgID)
	if err != nil {
		return nil, err
	}
	out := []MailAttach{}
	for _, v := range rows.Vals {
		if len(v) < 8 {
			continue
		}
		out = append(out, MailAttach{ID: i64(v[0]), MessageID: i64(v[1]), Name: mstr(v[2]), Mime: mstr(v[3]), Bytes: i64(v[4]), Kept: mstr(v[5]) == "t" || mstr(v[5]) == "true", Path: mstr(v[6]), SHA256: mstr(v[7])})
	}
	return out, nil
}

// MailAttachment is one attachment's row, for serving its bytes.
func (s *NotifStore) MailAttachment(slot int, id int64) (*MailAttach, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query("SELECT id, message_id, name, mime, bytes, kept, path, sha256 FROM mail_attachments WHERE id = $1", id)
	if err != nil || len(rows.Vals) == 0 || len(rows.Vals[0]) < 8 {
		return nil, err
	}
	v := rows.Vals[0]
	return &MailAttach{ID: i64(v[0]), MessageID: i64(v[1]), Name: mstr(v[2]), Mime: mstr(v[3]), Bytes: i64(v[4]), Kept: mstr(v[5]) == "t" || mstr(v[5]) == "true", Path: mstr(v[6]), SHA256: mstr(v[7])}, nil
}

// MailInsert stores a message row and returns its id. A second insert of the same inbound relay
// name is the same row (the pull is idempotent across a crash).
func (s *NotifStore) MailInsert(slot int, m MailRow, body string, verdicts []byte, sealPath string) (int64, error) {
	c, err := s.pg(slot)
	if err != nil {
		return 0, err
	}
	rows, err := c.Query("INSERT INTO mail_messages (direction, state, message_id, in_reply_to, thread_key, from_addr, from_name, to_addrs, cc_addrs, reply_to, subject, sent_at, received_at, body, body_kind, raw_size, verdicts, spam, seal_path, relay_name, last_err) "+
		"VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12,$13,$14,$15,$16,$17,$18,$19,$20,$21) ON CONFLICT (direction, relay_name) DO UPDATE SET state = mail_messages.state RETURNING id",
		m.Direction, m.State, m.MessageID, m.InReplyTo, m.ThreadKey, m.FromAddr, m.FromName, jsonList(m.To), jsonList(m.Cc), m.ReplyTo, m.Subject,
		m.SentAt, m.ReceivedAt, body, m.BodyKind, m.RawSize, string(verdicts), m.Spam, sealPath, m.RelayName, m.LastErr)
	if err != nil {
		return 0, err
	}
	if len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return 0, fmt.Errorf("insert returned no id")
	}
	return i64(rows.Vals[0][0]), nil
}

// MailAttachAdd stores one attachment row.
func (s *NotifStore) MailAttachAdd(slot int, a MailAttach) (int64, error) {
	c, err := s.pg(slot)
	if err != nil {
		return 0, err
	}
	rows, err := c.Query("INSERT INTO mail_attachments (message_id, name, mime, bytes, kept, path, sha256) VALUES ($1,$2,$3,$4,$5,$6,$7) RETURNING id",
		a.MessageID, a.Name, a.Mime, a.Bytes, a.Kept, a.Path, a.SHA256)
	if err != nil {
		return 0, err
	}
	if len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return 0, fmt.Errorf("insert returned no id")
	}
	return i64(rows.Vals[0][0]), nil
}

// MailSetState changes a message's state (seen, archived, sent, bounced...) and last error.
func (s *NotifStore) MailSetState(slot int, id int64, state, lastErr string) error {
	c, err := s.pg(slot)
	if err != nil {
		return err
	}
	return c.Exec("UPDATE mail_messages SET state = $2, last_err = $3 WHERE id = $1", id, state, lastErr)
}

// MailSetRelayName records the relay's name for an outbound message once posted.
func (s *NotifStore) MailSetRelayName(slot int, id int64, name string) error {
	c, err := s.pg(slot)
	if err != nil {
		return err
	}
	return c.Exec("UPDATE mail_messages SET relay_name = $2 WHERE id = $1", id, name)
}

// MailDelete removes a message and its attachment rows (the files are the caller's).
func (s *NotifStore) MailDelete(slot int, id int64) ([]MailAttach, string, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, "", err
	}
	atts, err := s.MailAttachments(slot, id)
	if err != nil {
		return nil, "", err
	}
	rows, err := c.Query("SELECT seal_path FROM mail_messages WHERE id = $1", id)
	if err != nil {
		return nil, "", err
	}
	seal := ""
	if len(rows.Vals) > 0 {
		seal = mstr(rows.Vals[0][0])
	}
	if err := c.Exec("DELETE FROM mail_attachments WHERE message_id = $1", id); err != nil {
		return nil, "", err
	}
	return atts, seal, c.Exec("DELETE FROM mail_messages WHERE id = $1", id)
}

// MailHave reports whether an inbound relay name is already stored.
func (s *NotifStore) MailHave(slot int, direction, relayName string) (bool, error) {
	c, err := s.pg(slot)
	if err != nil {
		return false, err
	}
	rows, err := c.Query("SELECT 1 FROM mail_messages WHERE direction = $1 AND relay_name = $2", direction, relayName)
	if err != nil {
		return false, err
	}
	return len(rows.Vals) > 0, nil
}

// MailSenderHistory is how many messages the box holds from an address, and whether the
// domain has written before, for the spam score.
func (s *NotifStore) MailSenderHistory(slot int, from string) (count int, domainSeen bool, err error) {
	c, err := s.pg(slot)
	if err != nil {
		return 0, false, err
	}
	rows, err := c.Query("SELECT count(*) FROM mail_messages WHERE direction = 'in' AND from_addr = $1", strings.ToLower(from))
	if err != nil {
		return 0, false, err
	}
	if len(rows.Vals) > 0 {
		count = int(i64(rows.Vals[0][0]))
	}
	if at := strings.LastIndexByte(from, '@'); at >= 0 {
		rows, err = c.Query("SELECT 1 FROM mail_messages WHERE direction = 'in' AND from_addr LIKE $1 LIMIT 1", "%@"+strings.ToLower(from[at+1:]))
		if err != nil {
			return count, false, err
		}
		domainSeen = len(rows.Vals) > 0
	}
	return count, domainSeen, nil
}

// MailRecentFrom counts messages from an address in the last hour, for the burst line.
func (s *NotifStore) MailRecentFrom(slot int, from string, since int64) (int, error) {
	c, err := s.pg(slot)
	if err != nil {
		return 0, err
	}
	rows, err := c.Query("SELECT count(*) FROM mail_messages WHERE direction = 'in' AND from_addr = $1 AND received_at >= $2", strings.ToLower(from), since)
	if err != nil || len(rows.Vals) == 0 {
		return 0, err
	}
	return int(i64(rows.Vals[0][0])), nil
}

// MailCounts is the inbox's state for the status line: messages in, unseen, sent, waiting.
func (s *NotifStore) MailCounts(slot int) (in, unseen, out, queued int, err error) {
	c, err := s.pg(slot)
	if err != nil {
		return
	}
	rows, err := c.Query("SELECT count(*) FILTER (WHERE direction = 'in'), count(*) FILTER (WHERE direction = 'in' AND state = 'new'), count(*) FILTER (WHERE direction = 'out'), count(*) FILTER (WHERE direction = 'out' AND state = 'queued') FROM mail_messages")
	if err != nil || len(rows.Vals) == 0 || len(rows.Vals[0]) < 4 {
		return
	}
	v := rows.Vals[0]
	return int(i64(v[0])), int(i64(v[1])), int(i64(v[2])), int(i64(v[3])), nil
}

// MailOutQueued lists outbound messages the relay has not reported done.
func (s *NotifStore) MailOutQueued(slot int) ([]MailRow, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	rows, err := c.Query("SELECT " + mailCols + " FROM mail_messages m WHERE direction = 'out' AND state = 'queued' ORDER BY id")
	if err != nil {
		return nil, err
	}
	var out []MailRow
	for _, v := range rows.Vals {
		if len(v) >= 21 {
			out = append(out, scanMail(v))
		}
	}
	return out, nil
}

// MailThread is every message sharing a thread key, oldest first.
func (s *NotifStore) MailThread(slot int, key string) ([]MailRow, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	// the newest 200 of a thread: a sender who reuses one References value does not get to make
	// every message view return everything
	rows, err := c.Query("SELECT * FROM (SELECT "+mailCols+" FROM mail_messages m WHERE thread_key = $1 ORDER BY id DESC LIMIT 200) t ORDER BY id", key)
	if err != nil {
		return nil, err
	}
	out := []MailRow{}
	for _, v := range rows.Vals {
		if len(v) >= 21 {
			out = append(out, scanMail(v))
		}
	}
	return out, nil
}
