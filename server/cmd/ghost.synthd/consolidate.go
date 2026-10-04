package main

// CONSOLIDATION , what belongs together, together. The distiller makes memories as things come in:
// a fact about a person from a chat, another from a check-in, a memory per outing the photos make, a
// memory per day. Left alone that gives two memories for the same person (the note's and the
// chats'), and a trip of ten days told as four outings and ten days. This pass folds them:
//
//   PEOPLE. One memory per person (kind 'person', source_ref person:<name>). What the note about me
//   and my people says of them is kept in meta.note, every fact the chats and the check-ins added in
//   meta.facts with where it came from, other spellings of the name in meta.aliases; the body is
//   rendered from those (the note, then the facts), and once a day the model writes it as a few
//   sentences from the same facts, kept only when it stays inside them, with the facts added since
//   shown after it until the next writing. Two rows for one person (the same name, or a first name
//   and the full name) are merged into one: the facts move, the names become aliases, the duplicate
//   goes. A memory the person edited by hand is never merged into or rewritten; it stays theirs.
//
//   TRIPS. Outings away from home that follow one another within a few days are one trip (kind
//   'trip', source_ref trip:<first day>): the country or the places and the dates for a title, a
//   template body from the numbers, the outings and the days folded under it (meta.part_of on each),
//   and the model writes the trip from the outings' own texts and the days' stories when it is on
//   the GPU (prose.go). A lone outing away for a night or more folds its days under itself the same
//   way. MEMORIES shows a trip in place of its parts; the parts are still there, one tap down.
//
// Merging and folding are plain SQL and run every pass; the writing needs the GPU and runs once a
// day. Nothing here reads the journal: it works on the memories the other passes made.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

const (
	tripGapS       = 3 * 86400 // outings this close in time, both away from home, are one trip
	tripMinSpanS   = 86400     // a trip is at least a night away; two outings on one day are a day out
	consolidateKey = "synthd_consolidated_day"
	personProseMax = 3 // tries at writing a person before the facts stand as they are
)

// --- people ---

// personFact is one thing the box learnt about a person, and where.
type personFact struct {
	T   string `json:"t"`
	Ref string `json:"ref,omitempty"`
}

// personMeta is what a person's memory is made of; the body is rendered from it.
type personMeta struct {
	Note       string       `json:"note,omitempty"`    // what the note about me and my people says of them
	Facts      []personFact `json:"facts,omitempty"`   // from the chats and the check-ins, oldest first
	Aliases    []string     `json:"aliases,omitempty"` // other spellings of the name merged into this one
	Prose      string       `json:"prose,omitempty"`   // the model's telling of note + facts, when written
	ProseN     int          `json:"prose_n,omitempty"` // how many facts (the note counted as one) it covered
	ProseTries int          `json:"prose_tries,omitempty"`
}

func parsePersonMeta(s string) personMeta {
	var m personMeta
	if s != "" {
		_ = json.Unmarshal([]byte(s), &m)
	}
	return m
}

func (m personMeta) json() string {
	b, _ := json.Marshal(m)
	return string(b)
}

// factCount is what the prose has to cover: the facts, and the note as one.
func (m personMeta) factCount() int {
	n := len(m.Facts)
	if m.Note != "" {
		n++
	}
	return n
}

// has says whether the person's memory already holds a fact (the note or a fact containing it).
func (m personMeta) has(fact string) bool {
	f := strings.ToLower(strings.TrimRight(strings.TrimSpace(fact), "."))
	if f == "" {
		return true
	}
	if strings.Contains(strings.ToLower(m.Note), f) {
		return true
	}
	for _, x := range m.Facts {
		if strings.Contains(strings.ToLower(x.T), f) {
			return true
		}
	}
	return false
}

// personBody renders a person's memory: the model's prose when it covers everything, the prose
// with the facts added since after it, else the note and the facts in order.
func personBody(m personMeta) string {
	var parts []string
	if m.Prose != "" {
		parts = append(parts, m.Prose)
		covered := m.ProseN
		if m.Note != "" {
			covered-- // the note was one of them
		}
		if covered < 0 {
			covered = 0
		}
		for i := covered; i < len(m.Facts); i++ {
			parts = append(parts, m.Facts[i].T)
		}
		return strings.TrimSpace(strings.Join(parts, " "))
	}
	if m.Note != "" {
		parts = append(parts, m.Note)
	}
	for _, f := range m.Facts {
		parts = append(parts, f.T)
	}
	return strings.TrimSpace(strings.Join(parts, " "))
}

// firstName is the name's first word, lowercased, without a trailing possessive.
func firstName(name string) string {
	f := strings.Fields(strings.ToLower(strings.TrimSpace(name)))
	if len(f) == 0 {
		return ""
	}
	return strings.TrimSuffix(strings.TrimSuffix(f[0], "'s"), "’s")
}

// samePerson says whether two names name one person: the same name, or one of them a first name
// and the other that first name with more ("Cristina", "Cristina Cealicu"). Two full names that
// share a first name are two people.
func samePerson(a, b string) bool {
	la, lb := strings.ToLower(strings.TrimSpace(a)), strings.ToLower(strings.TrimSpace(b))
	if la == "" || lb == "" {
		return false
	}
	if la == lb {
		return true
	}
	fa, fb := strings.Fields(la), strings.Fields(lb)
	if len(fa) == 1 && len(fb) > 1 && firstName(lb) == firstName(la) {
		return true
	}
	if len(fb) == 1 && len(fa) > 1 && firstName(la) == firstName(lb) {
		return true
	}
	return false
}

// personRow is one person's memory as the table has it.
type personRow struct {
	id         string
	title      string
	ref        string
	body       string
	userEdited bool
	meta       personMeta
}

// names is every name the row answers to: its title and its aliases.
func (p personRow) names() []string { return append([]string{p.title}, p.meta.Aliases...) }

// matchPerson is the row that is the person named, or -1: a row with the name itself (or an alias)
// first; else the one row whose first name it is, when there is one such row and no other (a first
// name shared by two full names is left alone).
func matchPerson(rows []personRow, name string) int {
	ln := strings.ToLower(strings.TrimSpace(name))
	if ln == "" {
		return -1
	}
	for i, r := range rows {
		for _, n := range r.names() {
			if strings.ToLower(n) == ln {
				return i
			}
		}
	}
	found := -1
	for i, r := range rows {
		for _, n := range r.names() {
			if samePerson(n, name) {
				if found >= 0 && found != i {
					return -1 // two rows claim the first name: not the box's to decide
				}
				found = i
			}
		}
	}
	return found
}

// loadPeople reads every person's memory that is live.
func loadPeople(db *poltergres.ReadWrite) ([]personRow, error) {
	rows, err := db.Query("SELECT id, title, source_ref, body, user_edited, coalesce(meta::text,'') FROM memories WHERE kind = 'person' AND NOT tombstoned ORDER BY id")
	if err != nil {
		return nil, err
	}
	out := make([]personRow, 0, len(rows.Vals))
	for _, v := range rows.Vals {
		if len(v) < 6 || v[0] == nil || v[1] == nil {
			continue
		}
		p := personRow{id: *v[0], title: *v[1]}
		if v[2] != nil {
			p.ref = *v[2]
		}
		if v[3] != nil {
			p.body = *v[3]
		}
		p.userEdited = v[4] != nil && *v[4] == "t"
		if v[5] != nil {
			p.meta = parsePersonMeta(*v[5])
		}
		out = append(out, p)
	}
	return out, nil
}

// personKey is the source_ref a person's memory is kept under.
func personKey(name string) string { return "person:" + strings.ToLower(strings.TrimSpace(name)) }

// adopt brings a row from before the facts existed (a body and nothing in meta) into the facts
// model, so a change to its meta does not render its body away: the note's old row's body is the
// note, any other's is one fact.
func adopt(p personRow) personRow {
	if !p.userEdited && len(p.meta.Facts) == 0 && p.meta.Note == "" && strings.TrimSpace(p.body) != "" {
		p.meta = foldPerson(p.meta, p)
	}
	return p
}

// savePerson writes a row's meta and, unless the person edited it by hand, its body from the meta.
// A row from before (the note's about:person:<name>) comes under person:<name> when it is written,
// so the note's rewrite no longer deletes it.
func savePerson(db *poltergres.ReadWrite, p personRow, now int64) error {
	if p.userEdited {
		return db.Exec("UPDATE memories SET meta = $2::jsonb WHERE id = $1", p.id, p.meta.json())
	}
	if !strings.HasPrefix(p.ref, "person:") {
		p.ref = personKey(p.title)
	}
	return db.Exec("UPDATE memories SET title = $2, body = $3, meta = $4::jsonb, source_ref = $5, updated_at = $6 WHERE id = $1",
		p.id, p.title, personBody(p.meta), p.meta.json(), p.ref, now)
}

// addPersonFact adds what a chat or a check-in said about a person to their memory, or starts it.
// A fact the memory holds already (in the note or among the facts) is not added again; a memory
// the person edited keeps its body and takes the fact into its meta only.
func addPersonFact(db *poltergres.ReadWrite, name, fact, ref string, srcChat int64, now int64) error {
	name, fact = strings.TrimSpace(name), strings.TrimSpace(fact)
	if name == "" || fact == "" || len(name) > 60 || len(fact) > 500 {
		return nil
	}
	rows, err := loadPeople(db)
	if err != nil {
		return err
	}
	if i := matchPerson(rows, name); i >= 0 {
		p := adopt(rows[i])
		if p.meta.has(fact) || len(personBody(p.meta))+len(fact) > 4000 {
			return nil
		}
		p.meta.Facts = append(p.meta.Facts, personFact{T: fact, Ref: ref})
		return savePerson(db, p, now)
	}
	m := personMeta{Facts: []personFact{{T: fact, Ref: ref}}}
	return db.Exec("INSERT INTO memories (title, body, kind, source_chat, source_ref, meta, created_at, updated_at) VALUES ($1,$2,'person',NULLIF($3,0),$4,$5::jsonb,$6,$6)",
		name, personBody(m), srcChat, personKey(name), m.json(), now)
}

// setPersonNote keeps what the note about me and my people says of a person, replacing what it
// said before; a memory the person edited keeps its body.
func setPersonNote(db *poltergres.ReadWrite, name, note string, now int64) error {
	name, note = strings.TrimSpace(name), strings.TrimSpace(note)
	if name == "" || note == "" {
		return nil
	}
	rows, err := loadPeople(db)
	if err != nil {
		return err
	}
	if i := matchPerson(rows, name); i >= 0 {
		p := adopt(rows[i])
		if p.meta.Note == note {
			return nil
		}
		p.meta.Note = note
		// the prose was written over the old note: written again at the next consolidation
		p.meta.Prose, p.meta.ProseN, p.meta.ProseTries = "", 0, 0
		return savePerson(db, p, now)
	}
	m := personMeta{Note: note}
	return db.Exec("INSERT INTO memories (title, body, kind, source_ref, meta, created_at, updated_at) VALUES ($1,$2,'person',$3,$4::jsonb,$5,$5)",
		name, personBody(m), personKey(name), m.json(), now)
}

// clearPersonNotes drops the note's part from the people the note no longer names (the note was
// rewritten without them); a memory left with nothing goes, unless the person edited it.
func clearPersonNotes(db *poltergres.ReadWrite, named []string, now int64) error {
	rows, err := loadPeople(db)
	if err != nil {
		return err
	}
	for _, p := range rows {
		if p.meta.Note == "" {
			continue
		}
		keep := false
		for _, n := range named {
			if matchPerson([]personRow{p}, n) == 0 {
				keep = true
				break
			}
		}
		if keep {
			continue
		}
		p.meta.Note = ""
		p.meta.Prose, p.meta.ProseN, p.meta.ProseTries = "", 0, 0
		if len(p.meta.Facts) == 0 && !p.userEdited {
			if err := db.Exec("DELETE FROM memories WHERE id = $1 AND NOT user_edited", p.id); err != nil {
				return err
			}
			continue
		}
		if err := savePerson(db, p, now); err != nil {
			return err
		}
	}
	return nil
}

// mergePeople folds the rows that are one person into one. The rows the person edited by hand
// take no part (they are theirs, and never a merge's target or source). Among the rest the one
// kept is the one under person:<name>, else the oldest; the others' note, facts and body move into
// it (a body from before the facts existed becomes one fact, with the old source_ref as where it
// came from), their names become aliases, the longest name becomes the title, and they are deleted.
// Returns how many rows were folded away.
func mergePeople(db *poltergres.ReadWrite, lg *slog.Logger) (int, error) {
	rows, err := loadPeople(db)
	if err != nil {
		return 0, err
	}
	var free []personRow
	for _, r := range rows {
		if !r.userEdited {
			free = append(free, r)
		}
	}
	used := make([]bool, len(free))
	folded := 0
	now := time.Now().UnixMilli()
	for i := range free {
		if used[i] {
			continue
		}
		group := []int{i}
		for j := i + 1; j < len(free); j++ {
			if used[j] {
				continue
			}
			for _, g := range group {
				if namesMatch(free[g], free[j]) && !ambiguous(free, group, j) {
					group = append(group, j)
					break
				}
			}
		}
		if len(group) < 2 {
			continue
		}
		for _, g := range group {
			used[g] = true
		}
		// the survivor: the row under person:<name>, else the oldest (the first, rows come by id)
		keep := group[0]
		for _, g := range group {
			if strings.HasPrefix(free[g].ref, "person:") {
				keep = g
				break
			}
		}
		k := adopt(free[keep])
		for _, g := range group {
			if g == keep {
				continue
			}
			o := free[g]
			k.meta = foldPerson(k.meta, o)
			if len(o.title) > len(k.title) && samePerson(o.title, k.title) {
				k.meta.Aliases = addAlias(k.meta.Aliases, k.title, o.title)
				k.title = o.title
			} else {
				k.meta.Aliases = addAlias(k.meta.Aliases, o.title, k.title)
			}
			if err := db.Exec("DELETE FROM memories WHERE id = $1 AND NOT user_edited", o.id); err != nil {
				return folded, err
			}
			folded++
		}
		k.meta.Prose, k.meta.ProseN, k.meta.ProseTries = "", 0, 0 // written again from everything
		k.ref = personKey(k.title)
		if err := savePerson(db, k, now); err != nil {
			return folded, err
		}
		lg.Info("one person, one memory", "fn", "mergePeople", "name", k.title, "folded", len(group)-1)
	}
	return folded, nil
}

// namesMatch says whether two rows are the same person by any of their names.
func namesMatch(a, b personRow) bool {
	for _, x := range a.names() {
		for _, y := range b.names() {
			if samePerson(x, y) {
				return true
			}
		}
	}
	return false
}

// ambiguous says whether taking row j into the group would merge a first name shared by two full
// names: the group and j together must not hold two different multi-word names.
func ambiguous(rows []personRow, group []int, j int) bool {
	full := map[string]bool{}
	for _, g := range append(append([]int{}, group...), j) {
		for _, n := range rows[g].names() {
			ln := strings.ToLower(strings.TrimSpace(n))
			if len(strings.Fields(ln)) > 1 {
				full[ln] = true
			}
		}
	}
	return len(full) > 1
}

// foldPerson moves what another row holds into a meta: its note (when the keeper has none), its
// facts, and its body as one fact when it had no facts (a row from before the facts existed, or the
// note's old row, whose body was the note's text).
func foldPerson(m personMeta, o personRow) personMeta {
	if o.meta.Note != "" {
		if m.Note == "" {
			m.Note = o.meta.Note
		} else if !m.has(o.meta.Note) {
			m.Facts = append(m.Facts, personFact{T: o.meta.Note, Ref: "about"})
		}
	}
	for _, f := range o.meta.Facts {
		if !m.has(f.T) {
			m.Facts = append(m.Facts, f)
		}
	}
	if len(o.meta.Facts) == 0 && o.meta.Note == "" && strings.TrimSpace(o.body) != "" {
		if strings.HasPrefix(o.ref, "about:person:") {
			if m.Note == "" {
				m.Note = strings.TrimSpace(o.body)
			} else if !m.has(o.body) {
				m.Facts = append(m.Facts, personFact{T: strings.TrimSpace(o.body), Ref: "about"})
			}
		} else if !m.has(o.body) {
			m.Facts = append(m.Facts, personFact{T: strings.TrimSpace(o.body), Ref: o.ref})
		}
	}
	for _, a := range o.meta.Aliases {
		m.Aliases = addAlias(m.Aliases, a, "")
	}
	return m
}

// addAlias adds a name to the aliases unless it is the title or there already.
func addAlias(aliases []string, name, title string) []string {
	name = strings.TrimSpace(name)
	if name == "" || strings.EqualFold(name, title) {
		return aliases
	}
	for _, a := range aliases {
		if strings.EqualFold(a, name) {
			return aliases
		}
	}
	return append(aliases, name)
}

// peopleProse has the model write each person whose facts grew since it last wrote them, a few a
// pass, kept only when the text stays inside the facts (groundedPerson). Returns how many it wrote.
func peopleProse(db *poltergres.ReadWrite, oc *oracle.Client, owner string, lg *slog.Logger) (int, error) {
	rows, err := loadPeople(db)
	if err != nil {
		return 0, err
	}
	wrote, tried := 0, 0
	for _, p := range rows {
		if p.userEdited || p.meta.factCount() < 2 || p.meta.ProseN == p.meta.factCount() || p.meta.ProseTries >= personProseMax {
			continue
		}
		if tried == 4 {
			break // a few a day; the rest tomorrow
		}
		tried++
		facts := personFacts(p)
		resp, ierr := oc.Infer(oracle.Request{
			Capability: "summarize", Class: oracle.ClassLocalSmall, Priority: oracle.PriorityBackground,
			Input: personPrompt(owner, p.title, facts), MaxTokens: 320, Temperature: 0.3, DeadlineMS: 120000,
		})
		if ierr != nil || resp.Err != "" {
			return wrote, nil // the model is away; the same people next time
		}
		prose, ok := groundedPerson(resp.Output, facts, p.names())
		now := time.Now().UnixMilli()
		if !ok {
			p.meta.ProseTries++
			if err := savePerson(db, p, now); err != nil {
				return wrote, err
			}
			lg.Info("a person's prose not kept", "fn", "peopleProse", "name", p.title)
			continue
		}
		p.meta.Prose, p.meta.ProseN, p.meta.ProseTries = prose, p.meta.factCount(), 0
		if err := savePerson(db, p, now); err != nil {
			return wrote, err
		}
		wrote++
	}
	return wrote, nil
}

// personFacts is the sheet the model writes a person from: the note's line, then the facts.
func personFacts(p personRow) []string {
	var f []string
	if p.meta.Note != "" {
		f = append(f, p.meta.Note)
	}
	for _, x := range p.meta.Facts {
		f = append(f, x.T)
	}
	return f
}

// personPrompt asks for one telling of a person from the facts and nothing else (pure, for the
// tests).
func personPrompt(owner, name string, facts []string) string {
	var b strings.Builder
	who := "the person who keeps these notes"
	if owner != "" {
		who = owner
	}
	b.WriteString("Below are notes " + who + " keeps about " + name + ", gathered over time. Write them as one short portrait of " + name + ", from the notes and nothing else.\n\nNOTES:\n")
	for _, x := range facts {
		b.WriteString("- " + x + "\n")
	}
	b.WriteString("\nThird person, present tense, plain. Say who " + name + " is to " + who + " first when the notes say, then the rest in a natural order. Keep every fact the notes give, each once; say nothing the notes do not say, and do not guess at feelings, dates or reasons. Two to five sentences. No lists, no headings, no exclamation marks. Reply with the portrait only.")
	return b.String()
}

// groundedPerson checks the model's portrait against the facts: a sane length, no list or refusal,
// every number in it among the facts' numbers, and every capitalised word inside a sentence (a
// name, a place) present in the facts or among the person's names, so nothing is invented about a
// person.
func groundedPerson(out string, facts, names []string) (string, bool) {
	s, ok := groundedProse(out, facts)
	if !ok {
		return "", false
	}
	known := strings.ToLower(strings.Join(facts, " ") + " " + strings.Join(names, " "))
	words := strings.Fields(s)
	for i, w := range words {
		t := strings.Trim(w, ".,;:()\"'“”’")
		if len(t) < 2 || t[0] < 'A' || t[0] > 'Z' {
			continue
		}
		if i == 0 || strings.HasSuffix(strings.Trim(words[i-1], "\"'“”’"), ".") {
			continue // sentence-initial: an ordinary word capitalised
		}
		if !strings.Contains(known, strings.ToLower(strings.TrimSuffix(strings.TrimSuffix(t, "'s"), "’s"))) {
			return "", false
		}
	}
	return s, true
}

// --- trips ---

// tripOuting is an outing as the trip pass reads it.
type tripOuting struct {
	id        string
	ref       string
	title     string
	body      string
	start     int64
	end       int64
	days      int
	photos    int
	away      bool
	fromHomeM float64
	distanceM float64
	country   string
	places    []string
	covers    []string
}

// chainTrips groups the outings away from home into trips: time order, a new trip when the next
// outing starts more than tripGapS after the last ended; a chain of two or more outings is a trip
// when it spans a night at least. Returns the groups as indexes into outs.
func chainTrips(outs []tripOuting) [][]int {
	idx := make([]int, 0, len(outs))
	for i, o := range outs {
		if o.away && o.start > 0 && o.end >= o.start {
			idx = append(idx, i)
		}
	}
	sort.Slice(idx, func(a, b int) bool { return outs[idx[a]].start < outs[idx[b]].start })
	var groups [][]int
	var cur []int
	var curEnd int64
	flush := func() {
		if len(cur) >= 2 && outs[cur[len(cur)-1]].end-outs[cur[0]].start >= tripMinSpanS {
			groups = append(groups, cur)
		}
		cur = nil
	}
	for _, i := range idx {
		o := outs[i]
		if len(cur) > 0 && o.start-curEnd > tripGapS {
			flush()
		}
		cur = append(cur, i)
		if o.end > curEnd {
			curEnd = o.end
		}
	}
	flush()
	return groups
}

// tripMeta is the trip's JSON for the app and for the next pass.
type tripMeta struct {
	Start     int64    `json:"start"`
	End       int64    `json:"end"`
	Days      int      `json:"days"`
	Photos    int      `json:"photos"`
	Outings   []string `json:"outings"`
	Countries []string `json:"countries,omitempty"`
	Places    []string `json:"places,omitempty"`
	Covers    []string `json:"covers,omitempty"`
	DistanceM float64  `json:"distanceM,omitempty"`
	FromHomeM float64  `json:"fromHomeM,omitempty"`
	Away      bool     `json:"away"`
	Template  string   `json:"template,omitempty"`
	Prose     string   `json:"prose,omitempty"`
}

// tripOf folds a chain of outings into one trip: dates, counts, the countries and places most
// photographed first, two covers from each outing up to six.
func tripOf(outs []tripOuting, group []int) tripMeta {
	var t tripMeta
	t.Away = true
	countryN := map[string]int{}
	placeN := map[string]int{}
	var countries, places []string
	for n, i := range group {
		o := outs[i]
		if n == 0 || o.start < t.Start {
			t.Start = o.start
		}
		if o.end > t.End {
			t.End = o.end
		}
		t.Photos += o.photos
		t.DistanceM += o.distanceM
		if o.fromHomeM > t.FromHomeM {
			t.FromHomeM = o.fromHomeM
		}
		t.Outings = append(t.Outings, o.ref)
		if o.country != "" {
			if countryN[o.country] == 0 {
				countries = append(countries, o.country)
			}
			countryN[o.country] += o.photos
		}
		for _, p := range o.places {
			if p == "" {
				continue
			}
			if placeN[p] == 0 {
				places = append(places, p)
			}
			placeN[p] += o.photos
		}
		for k, c := range o.covers {
			if k == 2 || len(t.Covers) == 6 {
				break
			}
			t.Covers = append(t.Covers, c)
		}
	}
	sort.SliceStable(countries, func(a, b int) bool { return countryN[countries[a]] > countryN[countries[b]] })
	sort.SliceStable(places, func(a, b int) bool { return placeN[places[a]] > placeN[places[b]] })
	t.Countries = countries
	if len(places) > 6 {
		places = places[:6]
	}
	t.Places = places
	t.Days = int((dayStart(t.End)-dayStart(t.Start))/86400) + 1
	return t
}

func dayStart(ts int64) int64 { return ts - ts%86400 }

// tripTitle: "Canada, 12 to 19 September 2026"; the places when no country is known; "France and
// Italy" for two, "three countries" for more.
func tripTitle(t tripMeta) string {
	where := ""
	switch {
	case len(t.Countries) == 1:
		where = t.Countries[0]
	case len(t.Countries) == 2:
		where = t.Countries[0] + " and " + t.Countries[1]
	case len(t.Countries) > 2:
		where = numberWord(len(t.Countries)) + " countries"
	case len(t.Places) > 0:
		where = joinSome(t.Places, 3)
	default:
		where = "Away"
	}
	return where + ", " + dateRange(t.Start, t.End)
}

// tripBody is the template the trip has until the model writes it: the days, where, the photos,
// how far.
func tripBody(t tripMeta) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("%s away", daysWord(t.Days)))
	if len(t.Countries) > 0 {
		b.WriteString(" in " + joinSome(t.Countries, 3))
	}
	if len(t.Places) > 0 {
		b.WriteString(": " + joinSome(t.Places, 4))
	}
	b.WriteString(".")
	if t.Photos > 0 {
		b.WriteString(fmt.Sprintf(" %d photo%s over %d outing%s", t.Photos, plural(t.Photos), len(t.Outings), plural(len(t.Outings))))
		if t.DistanceM >= 950 {
			b.WriteString("; about " + kmText(t.DistanceM) + " moved over the days")
		}
		b.WriteString(".")
	}
	if t.FromHomeM >= 50000 {
		b.WriteString(fmt.Sprintf(" About %d km from home.", int(t.FromHomeM/1000+0.5)))
	}
	return b.String()
}

func daysWord(n int) string {
	switch {
	case n <= 1:
		return "A night"
	case n == 7:
		return "A week"
	case n == 14:
		return "Two weeks"
	default:
		return numberWordCap(n) + " days"
	}
}

var smallNumbers = []string{"zero", "one", "two", "three", "four", "five", "six", "seven", "eight", "nine", "ten", "eleven", "twelve"}

func numberWord(n int) string {
	if n >= 0 && n < len(smallNumbers) {
		return smallNumbers[n]
	}
	return strconv.Itoa(n)
}

func numberWordCap(n int) string {
	w := numberWord(n)
	if len(w) > 0 && w[0] >= 'a' && w[0] <= 'z' {
		return strings.ToUpper(w[:1]) + w[1:]
	}
	return w
}

// joinSome: "A, B and C", at most n names, "and more" past them.
func joinSome(names []string, n int) string {
	if len(names) > n {
		return joinAnd(names[:n]) + " and more"
	}
	return joinAnd(names)
}

// dateRange: "12 to 19 September 2026", "28 September to 3 October 2026", "30 December 2025 to 2 January 2026".
func dateRange(start, end int64) string {
	a, b := time.Unix(start, 0).UTC(), time.Unix(end, 0).UTC()
	switch {
	case a.Year() != b.Year():
		return a.Format("2 January 2006") + " to " + b.Format("2 January 2006")
	case a.Month() != b.Month():
		return a.Format("2 January") + " to " + b.Format("2 January 2006")
	case a.Day() != b.Day():
		return a.Format("2") + " to " + b.Format("2 January 2006")
	default:
		return a.Format("2 January 2006")
	}
}

// loadOutings reads the outing memories that are live, with what the trip pass needs of their meta.
func loadOutings(db *poltergres.ReadWrite) ([]tripOuting, error) {
	rows, err := db.Query("SELECT id, source_ref, title, body, coalesce(meta::text,'{}') FROM memories WHERE kind = 'outing' AND NOT tombstoned ORDER BY created_at")
	if err != nil {
		return nil, err
	}
	out := make([]tripOuting, 0, len(rows.Vals))
	for _, v := range rows.Vals {
		if len(v) < 5 || v[0] == nil || v[1] == nil {
			continue
		}
		o := tripOuting{id: *v[0], ref: *v[1]}
		if v[2] != nil {
			o.title = *v[2]
		}
		if v[3] != nil {
			o.body = *v[3]
		}
		var m map[string]any
		if v[4] != nil && json.Unmarshal([]byte(*v[4]), &m) == nil {
			o.start, _ = metaInt(m, "start")
			o.end, _ = metaInt(m, "end")
			if d, ok := metaInt(m, "days"); ok {
				o.days = int(d)
			}
			if p, ok := metaInt(m, "photos"); ok {
				o.photos = int(p)
			}
			o.away, _ = m["away"].(bool)
			o.fromHomeM, _ = metaFloat(m, "fromHomeM")
			o.distanceM, _ = metaFloat(m, "distanceM")
			o.country, _ = m["country"].(string)
			o.places = metaStrings(m, "places")
			if len(o.places) == 0 {
				if p, _ := m["place"].(string); p != "" {
					o.places = []string{p}
				}
			}
			o.covers = metaStrings(m, "covers")
		}
		out = append(out, o)
	}
	return out, nil
}

// tripPass writes the trips the outings make and folds the outings and the days under them (and
// the days of a lone outing away for a night or more under that outing). A trip the person edited
// or deleted is left alone; one that dissolves (the outings re-clustered) goes unless touched.
// Returns how many trips were written or refreshed.
func tripPass(db *poltergres.ReadWrite, lg *slog.Logger) (int, error) {
	outs, err := loadOutings(db)
	if err != nil {
		return 0, err
	}
	now := time.Now().UnixMilli()
	written := 0
	var keep []string
	partOf := map[string]string{} // outing ref → trip ref
	type span struct {
		ref        string
		start, end int64
	}
	var daySpans []span
	for _, group := range chainTrips(outs) {
		t := tripOf(outs, group)
		ref := "trip:" + time.Unix(t.Start, 0).UTC().Format("2006-01-02")
		keep = append(keep, ref)
		for _, i := range group {
			partOf[outs[i].ref] = ref
		}
		daySpans = append(daySpans, span{ref, t.Start, t.End})
		title, body := tripTitle(t), tripBody(t)
		t.Template = body
		ex, qerr := db.Query("SELECT id, user_edited, tombstoned, coalesce(meta->>'template',''), coalesce(meta->>'prose','') FROM memories WHERE kind = 'trip' AND source_ref = $1", ref)
		if qerr != nil {
			return written, qerr
		}
		if len(ex.Vals) > 0 {
			v := ex.Vals[0]
			if (len(v) > 1 && v[1] != nil && *v[1] == "t") || (len(v) > 2 && v[2] != nil && *v[2] == "t") {
				continue
			}
			oldTemplate, prose := "", ""
			if len(v) > 3 && v[3] != nil {
				oldTemplate = *v[3]
			}
			if len(v) > 4 && v[4] != nil {
				prose = *v[4]
			}
			if oldTemplate == body {
				// the same facts: the text stands (the model's or the template's); the meta is refreshed
				t.Prose = prose
				mb, _ := json.Marshal(t)
				if err := db.Exec("UPDATE memories SET title = $1, meta = $2::jsonb WHERE id = $3", title, string(mb), *v[0]); err != nil {
					return written, err
				}
				continue
			}
			// the facts changed: the template stands until the model writes it again (no prose)
			mb, _ := json.Marshal(t)
			if err := db.Exec("UPDATE memories SET title = $1, body = $2, meta = $3::jsonb, updated_at = $4 WHERE id = $5", title, body, string(mb), now, *v[0]); err != nil {
				return written, err
			}
			written++
			continue
		}
		mb, _ := json.Marshal(t)
		if err := db.Exec("INSERT INTO memories (title, body, kind, source_ref, meta, created_at, updated_at) VALUES ($1,$2,'trip',$3,$4::jsonb,$5,$6)",
			title, body, ref, string(mb), t.End*1000, now); err != nil {
			return written, err
		}
		written++
		lg.Info("a trip from its outings", "fn", "tripPass", "trip", title, "outings", len(group))
	}
	// trips that dissolved go, unless the person touched them
	if len(keep) > 0 {
		if err := db.Exec("DELETE FROM memories WHERE kind = 'trip' AND NOT user_edited AND NOT tombstoned AND NOT (source_ref = ANY($1))", "{"+strings.Join(keep, ",")+"}"); err != nil {
			return written, err
		}
	} else if err := db.Exec("DELETE FROM memories WHERE kind = 'trip' AND NOT user_edited AND NOT tombstoned"); err != nil {
		return written, err
	}
	// a lone outing away for a night or more folds its days under itself
	for _, o := range outs {
		if o.away && partOf[o.ref] == "" && o.end-o.start >= tripMinSpanS {
			daySpans = append(daySpans, span{o.ref, o.start, o.end})
		}
	}
	// FOLDING: part_of on the outings and the days, set where it belongs and cleared where it no
	// longer does (a trip that dissolved, an outing that moved)
	if err := db.Exec("UPDATE memories SET meta = meta - 'part_of' WHERE kind IN ('outing','day') AND meta IS NOT NULL AND jsonb_exists(meta, 'part_of')"); err != nil {
		return written, err
	}
	for ref, trip := range partOf {
		if err := db.Exec("UPDATE memories SET meta = coalesce(meta,'{}'::jsonb) || jsonb_build_object('part_of', $1::text) WHERE kind = 'outing' AND source_ref = $2", trip, ref); err != nil {
			return written, err
		}
	}
	for _, s := range daySpans {
		from := time.Unix(s.start, 0).UTC().Format("2006-01-02")
		to := time.Unix(s.end, 0).UTC().Format("2006-01-02")
		if err := db.Exec("UPDATE memories SET meta = coalesce(meta,'{}'::jsonb) || jsonb_build_object('part_of', $1::text) WHERE kind = 'day' AND source_ref >= $2 AND source_ref <= $3", s.ref, "day:"+from, "day:"+to); err != nil {
			return written, err
		}
	}
	return written, nil
}

// tripFacts is the sheet the model writes a trip from: the trip's own numbers and places, each
// outing as its own memory tells it, the days' stories, the covers' captions.
func tripFacts(db *poltergres.ReadWrite, title string, meta map[string]any) []string {
	var f []string
	f = append(f, "Title: "+title)
	start, _ := metaInt(meta, "start")
	end, _ := metaInt(meta, "end")
	if start > 0 {
		f = append(f, "From: "+time.Unix(start, 0).UTC().Format("Monday 2 January 2006")+" to "+time.Unix(end, 0).UTC().Format("Monday 2 January 2006"))
	}
	if d, ok := metaInt(meta, "days"); ok {
		f = append(f, "Days: "+strconv.FormatInt(d, 10))
	}
	if p, ok := metaInt(meta, "photos"); ok {
		f = append(f, "Photos taken: "+strconv.FormatInt(p, 10))
	}
	if cs := metaStrings(meta, "countries"); len(cs) > 0 {
		f = append(f, "Countries: "+strings.Join(cs, ", "))
	}
	if ps := metaStrings(meta, "places"); len(ps) > 0 {
		f = append(f, "Places (most photographed first): "+strings.Join(ps, ", "))
	}
	if m, ok := metaFloat(meta, "fromHomeM"); ok && m > 0 {
		f = append(f, fmt.Sprintf("A trip, about %d km from home", int(m/1000+0.5)))
	}
	if m, ok := metaFloat(meta, "distanceM"); ok && m >= 950 {
		f = append(f, fmt.Sprintf("Moved about %d km over the days (on foot or by vehicle)", int(m/1000+0.5)))
	}
	if refs := metaStrings(meta, "outings"); len(refs) > 0 {
		rows, err := db.Query("SELECT title, body FROM memories WHERE kind = 'outing' AND source_ref = ANY($1) ORDER BY created_at", "{"+strings.Join(refs, ",")+"}")
		if err == nil {
			for _, v := range rows.Vals {
				if len(v) < 2 || v[0] == nil || v[1] == nil {
					continue
				}
				f = append(f, "Part of it, "+*v[0]+": "+clip(strings.Join(strings.Fields(*v[1]), " "), 300))
			}
		}
	}
	if start > 0 {
		from := time.Unix(start, 0).UTC().Format("2006-01-02")
		to := time.Unix(end, 0).UTC().Format("2006-01-02")
		rows, err := db.Query("SELECT day, summary FROM day_summaries WHERE day >= $1 AND day <= $2 AND summary <> '' ORDER BY day LIMIT 12", from, to)
		if err == nil {
			for _, v := range rows.Vals {
				if len(v) < 2 || v[0] == nil || v[1] == nil {
					continue
				}
				if d, err := time.Parse("2006-01-02", *v[0]); err == nil {
					f = append(f, d.Format("Mon 2 Jan")+": "+clip(strings.Join(strings.Fields(*v[1]), " "), 240))
				}
			}
		}
	}
	for _, c := range captionsFor(db, metaStrings(meta, "covers"), 6) {
		f = append(f, "A photo shows: "+c)
	}
	return f
}

// consolidatePass runs the merging and the folding every pass (plain SQL), and once a day, with
// the model on the GPU, has it write the people whose facts grew. Returns what it folded and wrote.
func consolidatePass(db *poltergres.ReadWrite, oc *oracle.Client, outingsChanged bool, lg *slog.Logger) (folded, trips, people int, err error) {
	if folded, err = mergePeople(db, lg); err != nil {
		return
	}
	today := time.Now().UTC().Format("2006-01-02")
	daily := setting(db, consolidateKey) != today
	if outingsChanged || daily {
		if trips, err = tripPass(db, lg); err != nil {
			return
		}
	}
	if !daily {
		return
	}
	if onGPU, gerr := oc.OnGPU(); gerr != nil || !onGPU {
		return // the people wait for the GPU; the day is not marked, so the next pass tries again
	}
	if people, err = peopleProse(db, oc, setting(db, ownerKey), lg); err != nil {
		return
	}
	err = setSetting(db, consolidateKey, today)
	return
}

// consolidateSummary is what ghost-cli shows: the people as one row each (name, facts, whether the
// model wrote them, other names), the trips with their outings and days, the last daily writing.
func consolidateSummary(db *poltergres.ReadWrite) map[string]any {
	out := map[string]any{"lastWritten": setting(db, consolidateKey)}
	if rows, err := loadPeople(db); err == nil {
		people := make([]map[string]any, 0, len(rows))
		for _, p := range rows {
			e := map[string]any{"name": p.title, "facts": len(p.meta.Facts), "note": p.meta.Note != "", "written": p.meta.Prose != ""}
			if len(p.meta.Aliases) > 0 {
				e["alsoCalled"] = p.meta.Aliases
			}
			if p.userEdited {
				e["edited"] = true
			}
			people = append(people, e)
		}
		out["people"] = people
	}
	if rows, err := db.Query("SELECT title, source_ref, coalesce(meta->>'days',''), coalesce(meta->>'photos',''), coalesce(meta->>'prose','') <> '', (SELECT count(*) FROM memories d WHERE d.kind IN ('day','outing') AND NOT d.tombstoned AND d.meta->>'part_of' = m.source_ref) FROM memories m WHERE kind = 'trip' AND NOT tombstoned ORDER BY created_at DESC"); err == nil {
		trips := make([]map[string]any, 0, len(rows.Vals))
		for _, v := range rows.Vals {
			if len(v) < 6 || v[0] == nil {
				continue
			}
			e := map[string]any{"title": *v[0]}
			if v[1] != nil {
				e["ref"] = *v[1]
			}
			if v[2] != nil {
				e["days"] = *v[2]
			}
			if v[3] != nil {
				e["photos"] = *v[3]
			}
			e["written"] = v[4] != nil && *v[4] == "t"
			if v[5] != nil {
				e["parts"] = *v[5]
			}
			trips = append(trips, e)
		}
		out["trips"] = trips
	}
	return out
}
