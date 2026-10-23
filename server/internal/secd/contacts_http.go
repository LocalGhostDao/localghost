package secd

// THE CONTACTS , the app's side of hw/contactstore.go. The phone reads its own contacts and
// posts the whole set here; the box keeps it, answers a search and puts a name to a number or an
// address. Read-only: nothing here writes to any address book anywhere.

import (
	"encoding/json"
	"io"
	"net/http"
	"strconv"
	"strings"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
)

type contactsUpload struct {
	Device   string       `json:"device"`
	Contacts []hw.Contact `json:"contacts"`
}

const (
	contactsMaxBody = 16 << 20
	contactsMax     = 20000
)

// cleanContacts checks an upload and trims what it carries to sizes the box keeps.
func cleanContacts(u *contactsUpload) (string, bool) {
	u.Device = calClip(u.Device, 64)
	if u.Device == "" {
		u.Device = "phone"
	}
	if len(u.Contacts) > contactsMax {
		return "too many contacts in one upload", false
	}
	seen := map[string]bool{}
	kept := u.Contacts[:0]
	for _, c := range u.Contacts {
		c.Lookup = calClip(c.Lookup, 300)
		c.Name, c.Given, c.Family, c.Nickname = calClip(c.Name, 200), calClip(c.Given, 100), calClip(c.Family, 100), calClip(c.Nickname, 100)
		c.Org, c.Title = calClip(c.Org, 200), calClip(c.Title, 200)
		if c.ID <= 0 && c.Lookup == "" {
			continue
		}
		if c.Name == "" {
			c.Name = strings.TrimSpace(c.Given + " " + c.Family)
		}
		if len(c.Numbers) > 20 {
			c.Numbers = c.Numbers[:20]
		}
		nums := c.Numbers[:0]
		for _, n := range c.Numbers {
			n.Number, n.E164, n.Label = calClip(n.Number, 40), calClip(n.E164, 20), calClip(n.Label, 40)
			if hw.PhoneDigits(n.Number) != "" || hw.PhoneDigits(n.E164) != "" {
				nums = append(nums, n)
			}
		}
		c.Numbers = nums
		if len(c.Emails) > 20 {
			c.Emails = c.Emails[:20]
		}
		mails := c.Emails[:0]
		for _, e := range c.Emails {
			e.Address, e.Label = calClip(e.Address, 200), calClip(e.Label, 40)
			if strings.Contains(e.Address, "@") {
				mails = append(mails, e)
			}
		}
		c.Emails = mails
		c.Birthday = hw.ContactBirthday(c.Birthday)
		if c.Name == "" && len(c.Numbers) == 0 && len(c.Emails) == 0 {
			continue
		}
		k := hw.ContactKey(u.Device, c)
		if seen[k] {
			continue
		}
		seen[k] = true
		kept = append(kept, c)
	}
	u.Contacts = kept
	return "", true
}

// handleContactsUpload , POST /v1/contacts/upload , the phone's whole set, replacing what the box
// held from that phone.
func (s *Server) handleContactsUpload(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, contactsMaxBody))
	if err != nil {
		writeErr(w, http.StatusRequestEntityTooLarge, "a contacts upload is at most 16 MB")
		return
	}
	var u contactsUpload
	if err := json.Unmarshal(body, &u); err != nil {
		writeErr(w, http.StatusBadRequest, "not a contacts upload")
		return
	}
	if why, ok := cleanContacts(&u); !ok {
		writeErr(w, http.StatusBadRequest, why)
		return
	}
	m, err := s.notif.ContactsReplace(mounted, u.Device, u.Contacts)
	if err != nil {
		secdLog.Warn("contacts upload failed", "fn", "handleContactsUpload", "err", err)
		s.appearsDown(w)
		return
	}
	secdLog.Info("contacts upload", "contacts", m.Contacts, "removed", m.Removed)
	writeJSON(w, map[string]any{"ok": true, "contacts": m.Contacts, "removed": m.Removed})
}

// handleContacts , GET /v1/contacts?q=&limit= , the contacts matching every word of q (names,
// organisation, addresses, numbers), starred first, then by name; no q is everyone. With
// for=<number or address>, the one contact it belongs to, if one does. The meta comes with it.
func (s *Server) handleContacts(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	qv := r.URL.Query()
	out := map[string]any{"ok": true}
	if m, ok := s.notif.ContactsMeta(mounted); ok {
		out["meta"] = m
	}
	if f := calClip(qv.Get("for"), 200); f != "" {
		if c, ok := s.notif.ContactFor(mounted, f); ok {
			out["contact"] = c
		}
		writeJSON(w, out)
		return
	}
	limit, _ := strconv.Atoi(qv.Get("limit"))
	terms := strings.Fields(calClip(qv.Get("q"), 200))
	if len(terms) > 8 {
		terms = terms[:8]
	}
	cs, err := s.notif.ContactSearch(mounted, terms, limit)
	if err != nil {
		secdLog.Warn("contacts read failed", "fn", "handleContacts", "err", err)
		s.appearsDown(w)
		return
	}
	out["contacts"] = cs
	writeJSON(w, out)
}

// handleContactsForget , POST /v1/contacts/forget , drops every contact and the meta.
func (s *Server) handleContactsForget(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	if err := s.notif.ContactsForget(mounted); err != nil {
		s.appearsDown(w)
		return
	}
	writeJSON(w, map[string]any{"ok": true})
}
