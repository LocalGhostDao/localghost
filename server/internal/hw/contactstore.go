package hw

// THE CONTACTS , what the phone's contacts hold (Android's ContactsContract: every account the
// phone syncs, Google's, Samsung's, the SIM's), read on the phone and sent to the box whole. One
// row per contact; an upload replaces the phone's set (rows it did not carry are deleted), so a
// contact deleted on the phone leaves the box at the next sync. Read-only: nothing here is ever
// written back. The box uses them to put a name to a number or an address, and for birthdays.

import (
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// ContactNumber is one phone number of a contact.
type ContactNumber struct {
	Number string `json:"number"`
	E164   string `json:"e164,omitempty"`
	Label  string `json:"label,omitempty"`
}

// ContactEmail is one address of a contact.
type ContactEmail struct {
	Address string `json:"address"`
	Label   string `json:"label,omitempty"`
}

// Contact is one person (or place, or company) in the phone's contacts.
type Contact struct {
	Key      string          `json:"key"`
	Device   string          `json:"device,omitempty"`
	ID       int64           `json:"id"`
	Lookup   string          `json:"lookup,omitempty"`
	Name     string          `json:"name"`
	Given    string          `json:"given,omitempty"`
	Family   string          `json:"family,omitempty"`
	Nickname string          `json:"nickname,omitempty"`
	Org      string          `json:"org,omitempty"`
	Title    string          `json:"title,omitempty"`
	Starred  bool            `json:"starred,omitempty"`
	Numbers  []ContactNumber `json:"numbers,omitempty"`
	Emails   []ContactEmail  `json:"emails,omitempty"`
	Birthday string          `json:"birthday,omitempty"` // YYYY-MM-DD, or --MM-DD when the year is not known
}

// ContactsMeta is the last upload's account, kept in settings (contacts_meta).
type ContactsMeta struct {
	At       int64  `json:"at"`
	Device   string `json:"device"`
	Contacts int    `json:"contacts"`
	Removed  int    `json:"removed"`
	Numbers  int    `json:"numbers"`
	Emails   int    `json:"emails"`
	Birthday int    `json:"birthdays"`
}

// PhoneDigits is a number's digits; a leading + is kept as the international form's mark.
func PhoneDigits(n string) string {
	var b strings.Builder
	for _, r := range n {
		if r >= '0' && r <= '9' {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// PhoneTail is the part of a number that matches across its forms (07700 900123, +44 7700
// 900123, 0044 7700 900123): the last nine digits, or all of a shorter one.
func PhoneTail(n string) string {
	d := PhoneDigits(n)
	if len(d) > 9 {
		return d[len(d)-9:]
	}
	return d
}

// contactPhones is the column the number lookup runs on: every number's tail, space-separated,
// with a space either side so a LIKE '% tail %' matches whole tails only.
func contactPhones(c Contact) string {
	seen := map[string]bool{}
	var parts []string
	for _, n := range c.Numbers {
		for _, form := range []string{n.E164, n.Number} {
			t := PhoneTail(form)
			if len(t) >= 5 && !seen[t] {
				seen[t] = true
				parts = append(parts, t)
			}
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " " + strings.Join(parts, " ") + " "
}

// contactSearch is the column a search runs on: names, nickname, organisation, addresses, the
// digits of the numbers, lower case, accents left as they are.
func contactSearch(c Contact) string {
	parts := []string{c.Name, c.Given, c.Family, c.Nickname, c.Org, c.Title}
	for _, e := range c.Emails {
		parts = append(parts, e.Address)
	}
	for _, n := range c.Numbers {
		parts = append(parts, PhoneDigits(n.Number), PhoneDigits(n.E164))
	}
	return strings.ToLower(strings.Join(parts, " "))
}

// ContactBirthday is a birthday in the forms the phone's contacts hold ("1986-03-14", "--03-14",
// "19860314", "14/03/1986", "14.03.1986") as YYYY-MM-DD or --MM-DD; "" when it is not one.
func ContactBirthday(s string) string {
	s = strings.TrimSpace(s)
	if s == "" {
		return ""
	}
	ok := func(y, m, d int) string {
		if m < 1 || m > 12 || d < 1 || d > 31 {
			return ""
		}
		if y > 0 {
			if y < 1880 || y > time.Now().Year()+1 {
				return ""
			}
			if time.Date(y, time.Month(m), d, 0, 0, 0, 0, time.UTC).Day() != d {
				return ""
			}
			return fmt.Sprintf("%04d-%02d-%02d", y, m, d)
		}
		if time.Date(2000, time.Month(m), d, 0, 0, 0, 0, time.UTC).Day() != d { // 2000 has a 29 February
			return ""
		}
		return fmt.Sprintf("--%02d-%02d", m, d)
	}
	num := func(x string) int {
		v, err := strconv.Atoi(x)
		if err != nil {
			return -1
		}
		return v
	}
	if strings.HasPrefix(s, "--") { // --MM-DD, or --MMDD
		r := strings.ReplaceAll(s[2:], "-", "")
		if len(r) == 4 {
			return ok(0, num(r[:2]), num(r[2:]))
		}
		return ""
	}
	if len(s) >= 10 && s[4] == '-' && s[7] == '-' { // YYYY-MM-DD, perhaps with a time after
		return ok(num(s[:4]), num(s[5:7]), num(s[8:10]))
	}
	if len(s) == 8 && PhoneDigits(s) == s { // YYYYMMDD
		return ok(num(s[:4]), num(s[4:6]), num(s[6:]))
	}
	f := strings.FieldsFunc(s, func(r rune) bool { return r == '/' || r == '.' || r == '-' || unicode.IsSpace(r) })
	if len(f) == 3 && num(f[0]) > 0 && num(f[1]) > 0 && num(f[2]) > 0 { // DD/MM/YYYY, the British way
		y := num(f[2])
		if len(f[0]) == 4 {
			return ok(num(f[0]), num(f[1]), num(f[2]))
		}
		return ok(y, num(f[1]), num(f[0]))
	}
	return ""
}

// ContactKey is a contact's identity: the device and the contact's lookup key (stable across
// the provider's re-aggregation where the id is not), else its id.
func ContactKey(device string, c Contact) string {
	if c.Lookup != "" {
		return device + "|" + c.Lookup
	}
	return device + "|" + strconv.FormatInt(c.ID, 10)
}

const contactCols = 18

// ContactsReplace writes an upload: every contact upserted with the upload's generation, then the
// device's rows of an older generation deleted.
func ContactsReplace(db *poltergres.ReadWrite, device string, cs []Contact) (ContactsMeta, error) {
	gen := time.Now().UnixNano()
	now := time.Now().Unix()
	meta := ContactsMeta{At: now, Device: device, Contacts: len(cs)}
	for start := 0; start < len(cs); start += 200 {
		end := start + 200
		if end > len(cs) {
			end = len(cs)
		}
		var sb strings.Builder
		sb.WriteString("INSERT INTO contacts (key, device, contact_id, lookup, name, given, family, nickname, org, title, starred, numbers, emails, birthday, bday_md, phones, search, emails_lc, gen, updated_at) VALUES ")
		args := make([]any, 0, (end-start)*contactCols)
		for i, c := range cs[start:end] {
			if i > 0 {
				sb.WriteString(",")
			}
			nums, mails := "[]", "[]"
			if len(c.Numbers) > 0 {
				b, _ := json.Marshal(c.Numbers)
				nums = string(b)
			}
			if len(c.Emails) > 0 {
				b, _ := json.Marshal(c.Emails)
				mails = string(b)
			}
			var lc []string
			for _, e := range c.Emails {
				lc = append(lc, strings.ToLower(strings.TrimSpace(e.Address)))
			}
			emailsLC := ""
			if len(lc) > 0 {
				emailsLC = " " + strings.Join(lc, " ") + " "
			}
			md := ""
			if len(c.Birthday) >= 5 {
				md = c.Birthday[len(c.Birthday)-5:]
			}
			meta.Numbers += len(c.Numbers)
			meta.Emails += len(c.Emails)
			if c.Birthday != "" {
				meta.Birthday++
			}
			n := len(args)
			sb.WriteString("(")
			for j := 1; j <= contactCols; j++ {
				if j > 1 {
					sb.WriteString(",")
				}
				sb.WriteString(fmt.Sprintf("$%d", n+j))
			}
			sb.WriteString(fmt.Sprintf(",%d,%d)", gen, now))
			args = append(args, ContactKey(device, c), device, c.ID, c.Lookup, c.Name, c.Given, c.Family, c.Nickname, c.Org, c.Title,
				c.Starred, nums, mails, c.Birthday, md, contactPhones(c), contactSearch(c), emailsLC)
		}
		sb.WriteString(" ON CONFLICT (key) DO UPDATE SET contact_id = EXCLUDED.contact_id, lookup = EXCLUDED.lookup, name = EXCLUDED.name, " +
			"given = EXCLUDED.given, family = EXCLUDED.family, nickname = EXCLUDED.nickname, org = EXCLUDED.org, title = EXCLUDED.title, " +
			"starred = EXCLUDED.starred, numbers = EXCLUDED.numbers, emails = EXCLUDED.emails, birthday = EXCLUDED.birthday, bday_md = EXCLUDED.bday_md, " +
			"phones = EXCLUDED.phones, search = EXCLUDED.search, emails_lc = EXCLUDED.emails_lc, gen = EXCLUDED.gen, updated_at = EXCLUDED.updated_at")
		if err := db.Exec(sb.String(), args...); err != nil {
			return meta, err
		}
	}
	rows, err := db.Query("DELETE FROM contacts WHERE device = $1 AND gen <> $2 RETURNING 1", device, gen)
	if err != nil {
		return meta, err
	}
	meta.Removed = len(rows.Vals)
	b, _ := json.Marshal(meta)
	return meta, db.Exec("INSERT INTO settings (key, value) VALUES ('contacts_meta', $1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", string(b))
}

// ContactsMetaOf is the last upload's account; ok false when the phone never sent one.
func ContactsMetaOf(q Querier) (ContactsMeta, bool) {
	var m ContactsMeta
	rows, err := q.Query("SELECT value FROM settings WHERE key = 'contacts_meta'")
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return m, false
	}
	return m, json.Unmarshal([]byte(*rows.Vals[0][0]), &m) == nil
}

const contactSelect = "SELECT key, device, contact_id, lookup, name, given, family, nickname, org, title, starred, numbers, emails, birthday FROM contacts"

func scanContacts(rows *poltergres.Rows) []Contact {
	out := []Contact{}
	for _, v := range rows.Vals {
		if len(v) < 14 {
			continue
		}
		s := func(i int) string {
			if v[i] == nil {
				return ""
			}
			return *v[i]
		}
		id, _ := strconv.ParseInt(s(2), 10, 64)
		c := Contact{Key: s(0), Device: s(1), ID: id, Lookup: s(3), Name: s(4), Given: s(5), Family: s(6), Nickname: s(7), Org: s(8),
			Title: s(9), Starred: s(10) == "t" || s(10) == "true", Birthday: s(13)}
		if a := s(11); a != "" && a != "[]" {
			_ = json.Unmarshal([]byte(a), &c.Numbers)
		}
		if a := s(12); a != "" && a != "[]" {
			_ = json.Unmarshal([]byte(a), &c.Emails)
		}
		out = append(out, c)
	}
	return out
}

// ContactSearch is the contacts whose names, organisation, addresses or numbers contain every
// term, starred first, then by name; no terms is everyone, by name. At most limit.
func ContactSearch(q Querier, terms []string, limit int) ([]Contact, error) {
	if limit <= 0 || limit > 10000 {
		limit = 5000
	}
	var where []string
	var args []any
	for _, t := range terms {
		t = strings.ToLower(strings.TrimSpace(t))
		if t == "" {
			continue
		}
		if d := PhoneDigits(t); len(d) >= 5 && len(d) >= len(t)-3 { // a number typed with spaces or a +
			t = d
		}
		args = append(args, "%"+t+"%")
		where = append(where, fmt.Sprintf("search LIKE $%d", len(args)))
	}
	sql := contactSelect
	if len(where) > 0 {
		sql += " WHERE " + strings.Join(where, " AND ")
	}
	args = append(args, limit)
	sql += fmt.Sprintf(" ORDER BY starred DESC, lower(name), key LIMIT $%d", len(args))
	rows, err := q.Query(sql, args...)
	if err != nil {
		return nil, err
	}
	return scanContacts(rows), nil
}

// ContactFor is the contact a number or an address belongs to; ok false when none does, or when
// more than one does and none is starred (a shared office number names nobody).
func ContactFor(q Querier, numberOrEmail string) (Contact, bool) {
	s := strings.TrimSpace(numberOrEmail)
	var rows *poltergres.Rows
	var err error
	if strings.Contains(s, "@") {
		rows, err = q.Query(contactSelect+" WHERE emails_lc LIKE $1 ORDER BY starred DESC LIMIT 3", "% "+strings.ToLower(s)+" %")
	} else {
		t := PhoneTail(s)
		if len(t) < 5 {
			return Contact{}, false
		}
		rows, err = q.Query(contactSelect+" WHERE phones LIKE $1 ORDER BY starred DESC LIMIT 3", "% "+t+" %")
	}
	if err != nil {
		return Contact{}, false
	}
	cs := scanContacts(rows)
	if len(cs) == 0 || (len(cs) > 1 && (!cs[0].Starred || cs[1].Starred)) {
		return Contact{}, false
	}
	return cs[0], true
}

// ContactBirthdays is the contacts whose birthday falls on one of the dates (month and day).
func ContactBirthdays(q Querier, dates []time.Time) ([]Contact, error) {
	if len(dates) == 0 {
		return []Contact{}, nil
	}
	var ph []string
	var args []any
	for _, d := range dates {
		args = append(args, d.Format("01-02"))
		ph = append(ph, fmt.Sprintf("$%d", len(args)))
	}
	rows, err := q.Query(contactSelect+" WHERE bday_md IN ("+strings.Join(ph, ",")+") ORDER BY bday_md, lower(name)", args...)
	if err != nil {
		return nil, err
	}
	return scanContacts(rows), nil
}

// BirthdayLine is "Ana's birthday (40)" for the given date, the age when the year is known.
func BirthdayLine(c Contact, on time.Time) string {
	name := strings.TrimSpace(c.Name)
	if name == "" {
		name = "someone in the contacts"
	}
	s := name + "'s birthday"
	if strings.HasSuffix(name, "s") {
		s = name + "' birthday"
	}
	if len(c.Birthday) == 10 && c.Birthday[0] != '-' {
		if y, err := strconv.Atoi(c.Birthday[:4]); err == nil && on.Year() > y {
			s += fmt.Sprintf(" (%d)", on.Year()-y)
		}
	}
	return s
}

// ContactLine is a contact in one line for the chat: the name, what they are, the numbers and
// addresses with their labels, the birthday.
func ContactLine(c Contact) string {
	parts := []string{strings.TrimSpace(c.Name)}
	if c.Nickname != "" && !strings.EqualFold(c.Nickname, c.Name) {
		parts[0] += " (" + c.Nickname + ")"
	}
	if c.Org != "" || c.Title != "" {
		parts = append(parts, strings.TrimSpace(strings.Join([]string{c.Title, c.Org}, ", ")))
		parts[len(parts)-1] = strings.Trim(parts[len(parts)-1], ", ")
	}
	for i, n := range c.Numbers {
		if i == 4 {
			break
		}
		v := n.Number
		if n.E164 != "" {
			v = n.E164
		}
		if n.Label != "" {
			v = n.Label + " " + v
		}
		parts = append(parts, v)
	}
	for i, e := range c.Emails {
		if i == 3 {
			break
		}
		v := e.Address
		if e.Label != "" {
			v = e.Label + " " + v
		}
		parts = append(parts, v)
	}
	if c.Birthday != "" {
		if strings.HasPrefix(c.Birthday, "--") {
			if t, err := time.Parse("01-02", c.Birthday[2:]); err == nil {
				parts = append(parts, "birthday "+t.Format("2 January"))
			}
		} else if t, err := time.Parse("2006-01-02", c.Birthday); err == nil {
			parts = append(parts, "birthday "+t.Format("2 January 2006"))
		}
	}
	return strings.Join(parts, " · ")
}

// ContactsForget drops every contact and the meta: what SETTINGS › CONTACTS › FORGET does.
func ContactsForget(db *poltergres.ReadWrite) error {
	if err := db.Exec("DELETE FROM contacts"); err != nil {
		return err
	}
	return db.Exec("DELETE FROM settings WHERE key = 'contacts_meta'")
}

// The NotifStore wrappers, for secd.

func (s *NotifStore) ContactsReplace(slot int, device string, cs []Contact) (ContactsMeta, error) {
	c, err := s.pg(slot)
	if err != nil {
		return ContactsMeta{}, err
	}
	return ContactsReplace(c, device, cs)
}

func (s *NotifStore) ContactSearch(slot int, terms []string, limit int) ([]Contact, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	return ContactSearch(c, terms, limit)
}

func (s *NotifStore) ContactFor(slot int, numberOrEmail string) (Contact, bool) {
	c, err := s.pg(slot)
	if err != nil {
		return Contact{}, false
	}
	return ContactFor(c, numberOrEmail)
}

func (s *NotifStore) ContactsMeta(slot int) (ContactsMeta, bool) {
	c, err := s.pg(slot)
	if err != nil {
		return ContactsMeta{}, false
	}
	return ContactsMetaOf(c)
}

func (s *NotifStore) ContactsForget(slot int) error {
	c, err := s.pg(slot)
	if err != nil {
		return err
	}
	return ContactsForget(c)
}
