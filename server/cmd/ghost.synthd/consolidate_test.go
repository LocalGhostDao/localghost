package main

import (
	"strings"
	"testing"
	"time"
)

func TestSamePersonAndMatching(t *testing.T) {
	if !samePerson("Cristina", "cristina") || !samePerson("Cristina", "Cristina Cealicu") || !samePerson("Cristina Cealicu", "Cristina") {
		t.Fatal("a first name and the full name are one person")
	}
	if samePerson("Cristina Cealicu", "Cristina Popescu") || samePerson("Ana", "Anamaria") || samePerson("", "Ana") {
		t.Fatal("two full names, or a longer first name, are not one person")
	}
	rows := []personRow{
		{id: "1", title: "Cristina Cealicu"},
		{id: "2", title: "James", meta: personMeta{Aliases: []string{"James Smith"}}},
		{id: "3", title: "Ana Pop"},
		{id: "4", title: "Ana Ionescu"},
	}
	if matchPerson(rows, "cristina") != 0 || matchPerson(rows, "Cristina Cealicu") != 0 {
		t.Fatal("Cristina is row 0")
	}
	if matchPerson(rows, "james smith") != 1 {
		t.Fatal("an alias matches")
	}
	if matchPerson(rows, "Ana") != -1 {
		t.Fatal("two Anas: not the box's to decide")
	}
	if matchPerson(rows, "Ana Pop") != 2 || matchPerson(rows, "Toby") != -1 {
		t.Fatal("the full name is exact; a stranger is nobody")
	}
}

func TestPersonBodyAndFolding(t *testing.T) {
	m := personMeta{Note: "Cristina is Vlad's wife.", Facts: []personFact{{T: "Cristina paints.", Ref: "chat:1"}, {T: "Cristina sails with Vlad.", Ref: "checkin:9"}}}
	if personBody(m) != "Cristina is Vlad's wife. Cristina paints. Cristina sails with Vlad." {
		t.Fatalf("rendered %q", personBody(m))
	}
	if !m.has("cristina paints") || m.has("Cristina runs") || !m.has("Vlad's wife") {
		t.Fatal("has: the note and the facts, case aside")
	}
	if m.factCount() != 3 {
		t.Fatal("the note counts as one")
	}
	// the prose covers the note and the first fact; the second shows after it until the next writing
	m.Prose, m.ProseN = "Cristina is Vlad's wife and paints.", 2
	if personBody(m) != "Cristina is Vlad's wife and paints. Cristina sails with Vlad." {
		t.Fatalf("rendered %q", personBody(m))
	}
	m.ProseN = 3
	if personBody(m) != "Cristina is Vlad's wife and paints." {
		t.Fatalf("rendered %q", personBody(m))
	}
	// folding an old row: the note's old row gives the note, a chats' row gives a fact, aliases carry
	k := personMeta{}
	k = foldPerson(k, personRow{ref: "about:person:cristina", body: "Cristina is Vlad's wife."})
	k = foldPerson(k, personRow{ref: "person:cristina", body: "Cristina paints.", meta: personMeta{Aliases: []string{"Cris"}}})
	k = foldPerson(k, personRow{ref: "person:cristina cealicu", meta: personMeta{Note: "Cristina is Vlad's wife.", Facts: []personFact{{T: "Cristina paints."}, {T: "Cristina sails."}}}})
	if k.Note != "Cristina is Vlad's wife." || len(k.Facts) != 2 || k.Facts[0].T != "Cristina paints." || k.Facts[0].Ref != "person:cristina" || k.Facts[1].T != "Cristina sails." {
		t.Fatalf("folded %+v", k)
	}
	if len(k.Aliases) != 1 || k.Aliases[0] != "Cris" {
		t.Fatalf("aliases %v", k.Aliases)
	}
	if a := addAlias(k.Aliases, "Cristina Cealicu", "Cristina Cealicu"); len(a) != 1 {
		t.Fatal("the title is no alias")
	}
}

func TestGroundedPerson(t *testing.T) {
	facts := []string{"Cristina is Vlad's wife.", "Cristina paints and shows her work in London.", "She has sailed with Vlad for 3 years."}
	names := []string{"Cristina", "Cristina Cealicu"}
	ok := "Cristina is Vlad's wife. She paints and shows her work in London, and has sailed with him for 3 years."
	if s, good := groundedPerson(ok, facts, names); !good || s != ok {
		t.Fatalf("kept nothing of %q (%q)", ok, s)
	}
	for _, bad := range []string{
		"Cristina is Vlad's wife. She paints and shows her work in Paris every spring.",        // a place the facts never gave
		"Cristina is Vlad's wife. She has sailed with him for 5 years and paints at home.",     // a number not in the facts
		"- Cristina is Vlad's wife and a painter in London\n- she sails with him every summer", // a list
		"Cristina is Vlad's wife. Her brother Marius sails with them most summers in London.",  // a person invented
	} {
		if _, good := groundedPerson(bad, facts, names); good {
			t.Fatalf("kept %q", bad)
		}
	}
	p := personPrompt("Vlad", "Cristina", facts)
	if !strings.Contains(p, "about Cristina") || !strings.Contains(p, "Vlad keeps") || !strings.Contains(p, "nothing the notes do not say") {
		t.Fatalf("prompt:\n%s", p)
	}
}

func day(s string) int64 {
	d, _ := time.Parse("2006-01-02", s)
	return d.Unix()
}

func TestChainTripsAndTheTripItMakes(t *testing.T) {
	outs := []tripOuting{
		{ref: "outing:2026-09-01", start: day("2026-09-01") + 36000, end: day("2026-09-01") + 60000, away: false, photos: 12, places: []string{"Greenwich"}, country: "United Kingdom"},
		{ref: "outing:2026-09-12", start: day("2026-09-12") + 40000, end: day("2026-09-14") + 70000, away: true, photos: 80, places: []string{"Toronto", "Niagara Falls"}, country: "Canada", fromHomeM: 5700000, distanceM: 31000, covers: []string{"a", "b", "c"}},
		{ref: "outing:2026-09-16", start: day("2026-09-16") + 30000, end: day("2026-09-18") + 60000, away: true, photos: 60, places: []string{"Montreal"}, country: "Canada", fromHomeM: 5200000, distanceM: 22000, covers: []string{"d"}},
		{ref: "outing:2026-09-19", start: day("2026-09-19") + 30000, end: day("2026-09-19") + 60000, away: true, photos: 20, places: []string{"Quebec City"}, country: "Canada", fromHomeM: 5000000, covers: []string{"e", "f", "g"}},
		{ref: "outing:2026-09-28", start: day("2026-09-28") + 30000, end: day("2026-09-28") + 50000, away: true, photos: 9, places: []string{"Brighton"}, country: "United Kingdom", fromHomeM: 80000},
		{ref: "outing:2026-09-28b", start: day("2026-09-28") + 55000, end: day("2026-09-28") + 70000, away: true, photos: 5, places: []string{"Hove"}, country: "United Kingdom", fromHomeM: 82000},
		{ref: "outing:2026-10-02", start: day("2026-10-02") + 30000, end: day("2026-10-03") + 50000, away: true, photos: 15, places: []string{"Paris"}, country: "France", fromHomeM: 340000},
	}
	groups := chainTrips(outs)
	// Canada: three outings within three days of each other; Brighton and Hove: one day, a day out,
	// not a trip; Paris: one outing alone, a night away, a trip of one outing
	if len(groups) != 2 || len(groups[0]) != 3 || groups[0][0] != 1 || groups[0][2] != 3 || len(groups[1]) != 1 || groups[1][0] != 6 {
		t.Fatalf("groups %v", groups)
	}
	if got := tripTitle(tripOf(outs, groups[1])); got != "France, 2 to 3 October 2026" {
		t.Fatalf("the lone outing's trip: %q", got)
	}
	tr := tripOf(outs, groups[0])
	if tr.Start != outs[1].start || tr.End != outs[3].end || tr.Days != 8 || tr.Photos != 160 || len(tr.Outings) != 3 {
		t.Fatalf("trip %+v", tr)
	}
	if len(tr.Countries) != 1 || tr.Countries[0] != "Canada" || tr.Places[0] != "Toronto" || len(tr.Places) != 4 {
		t.Fatalf("where %v %v", tr.Countries, tr.Places)
	}
	if len(tr.Covers) != 5 || tr.Covers[0] != "a" || tr.Covers[2] != "d" || tr.Covers[3] != "e" {
		t.Fatalf("covers %v", tr.Covers)
	}
	if tr.FromHomeM != 5700000 || tr.DistanceM != 53000 {
		t.Fatalf("distances %+v", tr)
	}
	if got := tripTitle(tr); got != "Canada, 12 to 19 September 2026" {
		t.Fatalf("title %q", got)
	}
	if got := tripBody(tr); got != "Eight days away in Canada: Toronto, Niagara Falls, Montreal and Quebec City. 160 photos over 3 outings; about 53 km moved over the days. About 5700 km from home." {
		t.Fatalf("body %q", got)
	}
	// a gap of four days breaks the chain
	outs[2].start, outs[2].end = day("2026-09-19")+30000, day("2026-09-20")+60000
	outs[3].start, outs[3].end = day("2026-09-21")+30000, day("2026-09-21")+60000
	if g := chainTrips(outs); len(g) != 3 || len(g[0]) != 1 || g[0][0] != 1 || len(g[1]) != 2 || g[1][0] != 2 {
		t.Fatalf("after the gap %v", g)
	}
	// titles without a country, with two, with many
	if got := tripTitle(tripMeta{Start: day("2026-09-28"), End: day("2026-10-03"), Places: []string{"Paris", "Lyon"}}); got != "Paris and Lyon, 28 September to 3 October 2026" {
		t.Fatalf("title %q", got)
	}
	if got := tripTitle(tripMeta{Start: day("2025-12-30"), End: day("2026-01-02"), Countries: []string{"France", "Italy"}}); got != "France and Italy, 30 December 2025 to 2 January 2026" {
		t.Fatalf("title %q", got)
	}
	if got := tripTitle(tripMeta{Start: day("2026-07-01"), End: day("2026-07-21"), Countries: []string{"A", "B", "C"}}); got != "three countries, 1 to 21 July 2026" {
		t.Fatalf("title %q", got)
	}
	if got := tripBody(tripMeta{Days: 7, Countries: []string{"Italy"}, Photos: 1, Outings: []string{"x"}}); got != "A week away in Italy. 1 photo over 1 outing." {
		t.Fatalf("body %q", got)
	}
}

func TestTripOutingsFate(t *testing.T) {
	outs := []tripOuting{
		{ref: "outing:2026-09-12", away: true},
		{ref: "outing:2026-09-16", away: false},
		{ref: "outing:2026-09-19", away: true},
	}
	partOf := map[string]string{"outing:2026-09-19": "trip:2026-09-19"}
	got := tripOutingsFate(outs, `["outing:2026-09-12","outing:2026-09-14","outing:2026-09-16","outing:2026-09-19"]`, partOf)
	if got != "outing:2026-09-12 alone; outing:2026-09-14 gone; outing:2026-09-16 home; outing:2026-09-19 in trip:2026-09-19" {
		t.Fatalf("fate %q", got)
	}
	if got := tripOutingsFate(outs, "", partOf); got != "none listed" {
		t.Fatalf("no list: %q", got)
	}
}
