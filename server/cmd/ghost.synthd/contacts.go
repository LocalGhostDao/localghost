package main

// THE CONTACTS, FROM THE BOX. The phone sends its contacts to the box (hw/contactstore.go); a
// question about someone's number, address or birthday, or about whose number this is, is
// answered from that table and never from the web. "Whose birthday is this week" reads the
// window the calendar's questions use (calendar.go).

import (
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

var (
	contactHint = regexp.MustCompile(`(?i)(\b(?:phone|mobile|cell|landline) ?(?:number|no)\b|\bnumber for\b|'s (?:number|phone|mobile|email|e-mail|address|birthday|details|contact)\b|` +
		`\bemail (?:address )?(?:for|of)\b|\bcontact (?:details|info)\b|\b(?:any|upcoming) birthdays\b|\bbirthdays? (?:this|next|coming|today|tomorrow|on)\b|\bwhose (?:number|birthday)\b|\bwho(?:'s| is| was| has)\b[^?]*\d[\d\s()-]{5,})`)
	contactNumber = regexp.MustCompile(`\+?\d[\d\s()-]{5,}\d`)
)

var contactStop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an the and or of in on at to for from with by about my me i what what's whats when when's who who's whose whom which how is are was were do does did
		have has had got give tell show find get send need want please can could would number numbers phone mobile cell landline email e-mail emails mail address
		addresses contact contacts details info birthday birthdays bday their his her its it this that these those next last coming week weekend month today tomorrow
		year old age turning turn again any anyone someone everyone`) {
		contactStop[w] = true
	}
}

// contactQuestion says whether the question is about someone in the contacts.
func contactQuestion(prompt string) bool {
	return contactHint.MatchString(prompt) && !weatherQuestion(prompt)
}

// contactTerms is who the question names ("what's Ana Pop's number" , ana, pop).
func contactTerms(prompt string) []string {
	var out []string
	for _, w := range calendarWord.FindAllString(strings.ToLower(prompt), -1) {
		w = strings.TrimSuffix(strings.TrimSuffix(strings.Trim(w, "'-"), "'s"), "’s")
		if len([]rune(w)) < 2 || contactStop[w] || calendarStop[w] {
			continue
		}
		if _, err := strconv.Atoi(w); err == nil {
			continue
		}
		out = append(out, w)
		if len(out) == 4 {
			break
		}
	}
	return out
}

// nextBirthday is the next time the birthday comes round from now, in loc, and the age then
// (0 when the year is not known).
func nextBirthday(c hw.Contact, now time.Time, loc *time.Location) (time.Time, int) {
	if len(c.Birthday) < 5 {
		return time.Time{}, 0
	}
	md := c.Birthday[len(c.Birthday)-5:]
	m, _ := strconv.Atoi(md[:2])
	d, _ := strconv.Atoi(md[3:])
	n := now.In(loc)
	today := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	next := time.Date(n.Year(), time.Month(m), d, 0, 0, 0, 0, loc)
	for next.Before(today) || next.Month() != time.Month(m) { // 29 February in a year without one waits for one
		next = time.Date(next.Year()+1, time.Month(m), d, 0, 0, 0, 0, loc)
	}
	age := 0
	if len(c.Birthday) == 10 && c.Birthday[0] != '-' {
		if y, err := strconv.Atoi(c.Birthday[:4]); err == nil {
			age = next.Year() - y
		}
	}
	return next, age
}

// contactItems is what a question about someone in the contacts gets; nothing found is said
// too, so the model does not guess.
func contactItems(db *poltergres.ReadWrite, prompt string, now time.Time) []ctxItem {
	if db == nil || !contactQuestion(prompt) {
		return nil
	}
	meta, ok := hw.ContactsMetaOf(db)
	if !ok {
		return []ctxItem{{Source: "contacts", Snippet: "the phone has not sent its contacts to the box (SETTINGS › CONTACTS on the phone turns it on); say so rather than guess", Why: "a contacts question with no contacts on the box"}}
	}
	loc := hw.CalendarZone(db)
	why := "the phone's contacts, synced " + time.Unix(meta.At, 0).In(loc).Format("Mon 2 Jan 15:04")
	item := func(s string) ctxItem { return ctxItem{Source: "contacts", Snippet: s, Why: why} }
	lower := strings.ToLower(prompt)
	birthday := strings.Contains(lower, "birthday") || strings.Contains(lower, "bday")

	// a number in the question: whose is it
	if n := contactNumber.FindString(prompt); n != "" && len(hw.PhoneDigits(n)) >= 6 {
		if c, ok := hw.ContactFor(db, n); ok {
			return []ctxItem{item(strings.TrimSpace(n) + " is in the contacts as " + hw.ContactLine(c))}
		}
		return []ctxItem{item(strings.TrimSpace(n) + " is not in the phone's contacts (or more than one contact has it); say so rather than guess")}
	}
	terms := contactTerms(prompt)
	// whose birthday falls in a window: the window the question names, else the next thirty days
	if birthday && len(terms) == 0 {
		from, to, words, named := calendarWindow(prompt, now, loc)
		if !named {
			to, words = from.AddDate(0, 0, 30), "the next thirty days"
		}
		var dates []time.Time
		for d := from; d.Before(to) && len(dates) < 62; d = d.AddDate(0, 0, 1) {
			dates = append(dates, d)
		}
		bs, err := hw.ContactBirthdays(db, dates)
		if err != nil {
			return nil
		}
		if len(bs) == 0 {
			return []ctxItem{item("no birthdays in the contacts in " + words + " (" + strconv.Itoa(meta.Birthday) + " contacts have one)")}
		}
		out := []ctxItem{item("birthdays in the contacts in " + words + ":")}
		for _, c := range bs {
			next, _ := nextBirthday(c, from, loc)
			out = append(out, ctxItem{When: next.Format("2006-01-02"), Source: "contacts", Snippet: hw.BirthdayLine(c, next) + ", " + next.Format("Mon 2 Jan"), Why: why})
			if len(out) == 16 {
				break
			}
		}
		return out
	}
	if len(terms) == 0 {
		return nil // "what's my number": nothing to look up
	}
	cs, err := hw.ContactSearch(db, terms, 6)
	if err == nil && len(cs) == 0 && len(terms) > 1 {
		cs, err = hw.ContactSearch(db, terms[:1], 6)
	}
	if err != nil {
		return nil
	}
	if len(cs) == 0 {
		return []ctxItem{item("nobody in the phone's contacts matches " + strings.Join(terms, " ") + "; say so rather than guess")}
	}
	out := []ctxItem{}
	if len(cs) > 1 {
		out = append(out, item(strconv.Itoa(len(cs))+" contacts match "+strings.Join(terms, " ")+"; if the question is about one of them, ask which"))
	}
	for i, c := range cs {
		if i == 5 {
			break
		}
		line := hw.ContactLine(c)
		if birthday && c.Birthday != "" {
			next, age := nextBirthday(c, now, loc)
			line += " · next birthday " + next.Format("Mon 2 Jan 2006")
			if age > 0 {
				line += " (turning " + strconv.Itoa(age) + ")"
			}
		} else if birthday {
			line += " · no birthday in the contact"
		}
		out = append(out, item(line))
	}
	return out
}

// contactCovers says the box answers a question about someone in the contacts: always, since
// nothing on the web knows the person's address book.
func contactCovers(prompt string) (string, bool) {
	if !contactQuestion(prompt) {
		return "", false
	}
	return "the phone's contacts, synced to the box", true
}
