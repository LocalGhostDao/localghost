package main

// NEIGHBOURHOODS , which memories belong together. The post "How memory gets made" says a memory
// survives by the company it keeps: a dinner in Milan is kept because three later dinners sit
// beside it, and touching any one of them warms the others. This pass draws that company for the
// memories the box holds, as rows in memory_links, from nothing but the memories themselves:
//
//	part     a day or an outing folded under a trip, a day under an outing away or under its
//	         month (meta.part_of): the part and its whole
//	names    a person, and a day, an outing, a trip, a note or a fact whose words name them
//	         (the name, an alias, or the first name when only one person has it)
//	place    a place the person keeps going to, and the days, outings and trips that were there
//	written  a note, a fact about me or about one of my people, and the day it was written on
//	month    a trip or an outing, and the month it happened in
//
// No model: every link is a thing the box can point at, and the memory's page says which. What
// this does NOT draw is the link the post opens with, two things that are alike in meaning and
// share no word (four men who ate too much, in four cities); that one needs the meanings, the
// emb column, which is still empty. docs/MEMORY.md says so.
//
// The table is made equal to what was derived, every pass: a memory that went (deleted by the
// person, folded into another, dissolved with its trip) loses its links here if it had not
// already (the person's own delete drops them at once, hw.MemoryTombstone). A hub is capped: a
// place or a person linked to hundreds of days is not a neighbourhood, so each keeps its newest.

import (
	"encoding/json"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

const (
	linkPart    = "part"
	linkNames   = "names"
	linkPlace   = "place"
	linkWritten = "written"
	linkMonth   = "month"

	linkHubMax  = 40 // links one person or one place keeps of a kind (the newest)
	linkNameMin = 3  // a name shorter than this matches too much to mean anything
)

// linkRank orders the reasons: when two memories are linked for two reasons, the stronger is kept.
var linkRank = map[string]int{linkPart: 5, linkNames: 4, linkPlace: 3, linkWritten: 2, linkMonth: 1}

// memNode is what the linking pass reads of one live memory.
type memNode struct {
	id      int64
	kind    string
	title   string
	body    string
	ref     string
	partOf  string   // meta.part_of: the ref of the whole this is folded under
	aliases []string // a person's other names
	refs    []string // a person's facts' refs: the records they came from
	places  []string // a trip's, an outing's or a day's places and route, as text to look in
	created int64    // unix ms
	day     string   // the local day this was written on (a note, a fact), "" when that means nothing
	start   int64    // a trip's or an outing's first moment, unix s
}

// memLink is one pair, the lower id first.
type memLink struct {
	a, b int64
	why  string
}

func pair(x, y int64, why string) memLink {
	if x > y {
		x, y = y, x
	}
	return memLink{x, y, why}
}

// hasName says whether text holds name (lower case) as words of its own: "ana" is in "with Ana,
// at the pier" and in "Ana's boat", not in "banana" or "Anatomy". With capital, the word in the
// text must begin with a capital letter, as a person's name does: Will and May are people, "will"
// and "may" are not, and the box's own writing always gives a name its capital.
func hasName(text, name string, capital bool) bool {
	nm := []rune(name)
	if len(nm) == 0 {
		return false
	}
	t := []rune(text)
	for i := 0; i+len(nm) <= len(t); i++ {
		if unicode.ToLower(t[i]) != nm[0] {
			continue
		}
		if capital && !unicode.IsUpper(t[i]) {
			continue
		}
		if i > 0 && wordRune(t[i-1]) {
			continue
		}
		ok := true
		for j := 1; j < len(nm); j++ {
			if unicode.ToLower(t[i+j]) != nm[j] {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		if end := i + len(nm); end < len(t) && wordRune(t[end]) {
			continue
		}
		return true
	}
	return false
}

func wordRune(r rune) bool { return unicode.IsLetter(r) || unicode.IsDigit(r) }

// personNames is every name each person answers to, lower case: the title, the aliases, and the
// first name when no other person shares it (two Anas with surnames are told apart by the whole
// name or not at all).
func personNames(people []memNode) map[int64][]string {
	firsts := map[string]int{}
	for _, p := range people {
		if f := firstName(p.title); f != "" {
			firsts[f]++
		}
	}
	out := make(map[int64][]string, len(people))
	for _, p := range people {
		seen := map[string]bool{}
		add := func(n string) {
			n = strings.ToLower(strings.TrimSpace(n))
			if len([]rune(n)) < linkNameMin || seen[n] {
				return
			}
			seen[n] = true
			out[p.id] = append(out[p.id], n)
		}
		add(p.title)
		for _, a := range p.aliases {
			add(a)
		}
		if f := firstName(p.title); firsts[f] == 1 {
			add(f)
		}
	}
	return out
}

// deriveLinks is the whole set of links the memories imply (pure, for the tests). refDay gives
// the local day a record was written on, by its ref ("" when unknown).
func deriveLinks(nodes []memNode, refDay func(ref string) string) []memLink {
	byRef := map[string]int64{}
	dayID := map[string]int64{} // "2026-10-03" → the day's memory
	var people, places []memNode
	for _, n := range nodes {
		switch n.kind {
		case "trip", "outing", "month":
			byRef[n.ref] = n.id
		case "day":
			byRef[n.ref] = n.id
			dayID[strings.TrimPrefix(n.ref, "day:")] = n.id
		case "person":
			people = append(people, n)
		case "place":
			places = append(places, n)
		}
	}
	best := map[[2]int64]string{}
	put := func(l memLink) {
		if l.a == l.b || l.a <= 0 {
			return
		}
		k := [2]int64{l.a, l.b}
		if old, ok := best[k]; !ok || linkRank[l.why] > linkRank[old] {
			best[k] = l.why
		}
	}
	// hubs: a person's or a place's links of one kind, the newest kept
	type hubLink struct {
		other   int64
		created int64
	}
	hub := func(self int64, why string, found []hubLink) {
		sort.Slice(found, func(i, j int) bool {
			if found[i].created != found[j].created {
				return found[i].created > found[j].created
			}
			return found[i].other > found[j].other
		})
		if len(found) > linkHubMax {
			found = found[:linkHubMax]
		}
		for _, f := range found {
			put(pair(self, f.other, why))
		}
	}

	// part and whole; a trip or an outing and its month
	for _, n := range nodes {
		if n.partOf != "" {
			if whole, ok := byRef[n.partOf]; ok {
				put(pair(n.id, whole, linkPart))
			}
		}
		if (n.kind == "trip" || n.kind == "outing") && n.start > 0 {
			if m, ok := byRef["month:"+time.Unix(n.start, 0).UTC().Format("2006-01")]; ok {
				put(pair(n.id, m, linkMonth))
			}
		}
	}

	// a person and what names them; a person and the day a fact about them was written
	names := personNames(people)
	lower := make(map[int64]string, len(nodes))
	for _, n := range nodes {
		lower[n.id] = strings.ToLower(n.title + " " + n.body)
	}
	for _, p := range people {
		var found []hubLink
		for _, n := range nodes {
			switch n.kind {
			case "person", "place", "month":
				continue
			}
			for _, nm := range names[p.id] {
				// the cheap look first: most memories name nobody
				if strings.Contains(lower[n.id], nm) && hasName(n.title+" "+n.body, nm, true) {
					found = append(found, hubLink{n.id, n.created})
					break
				}
			}
		}
		hub(p.id, linkNames, found)
		var written []hubLink
		seen := map[int64]bool{}
		for _, r := range p.refs {
			if d := refDay(r); d != "" {
				if id, ok := dayID[d]; ok && !seen[id] {
					seen[id] = true
					written = append(written, hubLink{id, 0})
				}
			}
		}
		hub(p.id, linkWritten, written)
	}

	// a place and what happened there: its name in the day's, the outing's or the trip's places
	for _, pl := range places {
		name := strings.ToLower(strings.TrimSpace(pl.title))
		if len([]rune(name)) < linkNameMin {
			continue
		}
		var found []hubLink
		for _, n := range nodes {
			if n.kind != "day" && n.kind != "outing" && n.kind != "trip" {
				continue
			}
			if hasName(n.title+" "+strings.Join(n.places, " , "), name, false) {
				found = append(found, hubLink{n.id, n.created})
			}
		}
		hub(pl.id, linkPlace, found)
	}

	// a note or a fact, and the day it was written on
	for _, n := range nodes {
		if n.day == "" {
			continue
		}
		if id, ok := dayID[n.day]; ok {
			put(pair(n.id, id, linkWritten))
		}
	}

	out := make([]memLink, 0, len(best))
	for k, why := range best {
		out = append(out, memLink{k[0], k[1], why})
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].a != out[j].a {
			return out[i].a < out[j].a
		}
		return out[i].b < out[j].b
	})
	return out
}

// pgArray writes a list as a Postgres array literal, every element quoted.
func pgArray(xs []string) string {
	var b strings.Builder
	b.WriteByte('{')
	for i, x := range xs {
		if i > 0 {
			b.WriteByte(',')
		}
		b.WriteByte('"')
		b.WriteString(strings.NewReplacer(`\`, `\\`, `"`, `\"`).Replace(x))
		b.WriteByte('"')
	}
	b.WriteByte('}')
	return b.String()
}

// refDays resolves record refs to the local day each record was written on: a journal entry's
// own ref ("chat:12", "note:…"), or "checkin:<row id>" with or without a fact's index after it.
func refDays(db *poltergres.ReadWrite, refs []string, loc *time.Location) (map[string]string, error) {
	out := map[string]string{}
	var plain, ids []string
	idOf := map[string]string{} // ref → the check-in's row id
	seen := map[string]bool{}
	for _, r := range refs {
		if r == "" || seen[r] {
			continue
		}
		seen[r] = true
		kind, rest, ok := strings.Cut(r, ":")
		if !ok || rest == "" {
			continue
		}
		if kind == "checkin" {
			id, _, _ := strings.Cut(rest, ":")
			if _, err := strconv.ParseInt(id, 10, 64); err == nil {
				idOf[r] = id
				ids = append(ids, id)
			}
			continue
		}
		plain = append(plain, r)
	}
	day := func(ts *string) string {
		if ts == nil {
			return ""
		}
		s, err := strconv.ParseInt(*ts, 10, 64)
		if err != nil || s <= 0 {
			return ""
		}
		return time.Unix(s, 0).In(loc).Format("2006-01-02")
	}
	if len(plain) > 0 {
		rows, err := db.Query("SELECT ref, max(ts) FROM journal_entries WHERE ref = ANY($1::text[]) GROUP BY ref", pgArray(plain))
		if err != nil {
			return nil, err
		}
		for _, v := range rows.Vals {
			if len(v) == 2 && v[0] != nil {
				out[*v[0]] = day(v[1])
			}
		}
	}
	if len(ids) > 0 {
		rows, err := db.Query("SELECT id::text, ts FROM journal_entries WHERE id = ANY($1::bigint[])", "{"+strings.Join(ids, ",")+"}")
		if err != nil {
			return nil, err
		}
		byID := map[string]string{}
		for _, v := range rows.Vals {
			if len(v) == 2 && v[0] != nil {
				byID[*v[0]] = day(v[1])
			}
		}
		for r, id := range idOf {
			out[r] = byID[id]
		}
	}
	return out, nil
}

// loadMemNodes reads every live memory as the linking pass needs it, and the day each note and
// fact was written on.
func loadMemNodes(db *poltergres.ReadWrite, loc *time.Location) ([]memNode, map[string]string, error) {
	rows, err := db.Query("SELECT id, kind, title, body, COALESCE(source_ref,''), COALESCE(meta::text,''), created_at FROM memories WHERE NOT tombstoned ORDER BY id")
	if err != nil {
		return nil, nil, err
	}
	nodes := make([]memNode, 0, len(rows.Vals))
	var refs []string
	for _, v := range rows.Vals {
		if len(v) < 7 || v[0] == nil {
			continue
		}
		n := memNode{kind: str(v[1]), title: str(v[2]), body: str(v[3]), ref: str(v[4])}
		n.id, _ = strconv.ParseInt(*v[0], 10, 64)
		n.created, _ = strconv.ParseInt(str(v[6]), 10, 64)
		if m := str(v[5]); m != "" {
			var meta struct {
				PartOf  string       `json:"part_of"`
				Aliases []string     `json:"aliases"`
				Facts   []personFact `json:"facts"`
				Place   string       `json:"place"`
				Places  []string     `json:"places"`
				Line    string       `json:"line"`
				Start   int64        `json:"start"`
			}
			if json.Unmarshal([]byte(m), &meta) == nil {
				n.partOf, n.start = meta.PartOf, meta.Start
				if n.kind == "person" {
					n.aliases = meta.Aliases
					for _, f := range meta.Facts {
						if f.Ref != "" {
							n.refs = append(n.refs, f.Ref)
						}
					}
				}
				n.places = append(n.places, meta.Places...)
				if meta.Place != "" {
					n.places = append(n.places, meta.Place)
				}
				if meta.Line != "" {
					n.places = append(n.places, meta.Line)
				}
			}
		}
		refs = append(refs, n.refs...)
		switch n.kind {
		case "distilled", "me":
			refs = append(refs, n.ref)
		case "user":
			// written by hand: the day it was written is the day it was made
			if n.created > 0 {
				n.day = time.UnixMilli(n.created).In(loc).Format("2006-01-02")
			}
		}
		nodes = append(nodes, n)
	}
	days, err := refDays(db, refs, loc)
	if err != nil {
		return nil, nil, err
	}
	for i := range nodes {
		if nodes[i].kind == "distilled" || nodes[i].kind == "me" {
			nodes[i].day = days[nodes[i].ref]
		}
	}
	return nodes, days, nil
}

// linkPass makes memory_links equal to what the memories imply: the missing links are added,
// the ones that no longer hold are removed, a reason that changed is rewritten. It returns how
// many links there are and how many it changed.
func linkPass(db *poltergres.ReadWrite, loc *time.Location, lg *slog.Logger) (total, changed int, err error) {
	nodes, days, err := loadMemNodes(db, loc)
	if err != nil {
		return 0, 0, err
	}
	want := deriveLinks(nodes, func(ref string) string { return days[ref] })
	have := map[[2]int64]string{}
	rows, err := db.Query("SELECT a, b, why FROM memory_links")
	if err != nil {
		return 0, 0, err
	}
	for _, v := range rows.Vals {
		if len(v) < 3 || v[0] == nil || v[1] == nil {
			continue
		}
		a, _ := strconv.ParseInt(*v[0], 10, 64)
		b, _ := strconv.ParseInt(*v[1], 10, 64)
		have[[2]int64{a, b}] = str(v[2])
	}
	now := time.Now().UnixMilli()
	for _, l := range want {
		k := [2]int64{l.a, l.b}
		old, ok := have[k]
		delete(have, k)
		if ok && old == l.why {
			continue
		}
		if err := db.Exec("INSERT INTO memory_links (a, b, why, made_at) VALUES ($1,$2,$3,$4) ON CONFLICT (a, b) DO UPDATE SET why = EXCLUDED.why, made_at = EXCLUDED.made_at",
			l.a, l.b, l.why, now); err != nil {
			return len(want), changed, err
		}
		changed++
	}
	for k := range have { // what is left no longer holds
		if err := db.Exec("DELETE FROM memory_links WHERE a = $1 AND b = $2", k[0], k[1]); err != nil {
			return len(want), changed, err
		}
		changed++
	}
	if changed > 0 {
		lg.Info("neighbourhoods redrawn", "fn", "linkPass", "links", len(want), "changed", changed)
	}
	return len(want), changed, nil
}
