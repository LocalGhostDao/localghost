package main

// Consolidation against a real Postgres: two memories for one person become one (the note's old
// row, the chats' row and a full-name row), a fact lands on the one row by any of the names, the
// note's part is replaced and cleared without losing the facts, a hand-edited row is left alone;
// the outings away that follow one another become a trip with the outings and the days folded
// under it, and a trip that dissolves goes and unfolds them. The model is away (no oracled socket):
// the templates stand.
//
//	GHOST_PG_SOCKET_DIR=/tmp GHOST_PG_PORT=55432 GHOST_PG_USER=claude go test -run ConsolidatePG ./cmd/ghost.synthd/

import (
	"encoding/json"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

func quietLog() *slog.Logger { return slog.New(slog.NewTextHandler(io.Discard, nil)) }

func memRows(t *testing.T, db *poltergres.ReadWrite, where string) [][]string {
	t.Helper()
	rows, err := db.Query("SELECT id, title, source_ref, body, coalesce(meta::text,''), user_edited::text FROM memories WHERE NOT tombstoned AND " + where + " ORDER BY id")
	if err != nil {
		t.Fatal(err)
	}
	var out [][]string
	for _, v := range rows.Vals {
		r := make([]string, len(v))
		for i, c := range v {
			if c != nil {
				r[i] = *c
			}
		}
		out = append(out, r)
	}
	return out
}

func TestConsolidatePGOnePersonOneMemory(t *testing.T) {
	db := pgFresh(t, "lg_consolidate_people")
	now := time.Now().UnixMilli()
	// the note's old row, the chats' row (no facts yet: a body), and a full-name row from a chat
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	must(db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ('Cristina','Cristina is Vlad''s wife.','person','about:person:cristina',$1,$1)", now-3))
	must(db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ('Cristina','Cristina paints.','person','person:cristina',$1,$1)", now-2))
	must(db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ('Cristina Cealicu','Cristina Cealicu sails with Vlad.','person','person:cristina cealicu',$1,$1)", now-1))
	// two Anas with full names stay two; a hand-edited James is the one kept, the note's row and
	// a misspelt row fold into it, the misspelling becomes an alias and his words stand
	must(db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ('Ana Pop','Ana Pop is a colleague.','person','person:ana pop',$1,$1)", now))
	must(db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ('Ana Ionescu','Ana Ionescu is a neighbour.','person','person:ana ionescu',$1,$1)", now))
	must(db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at, user_edited) VALUES ('James','James, as I wrote him.','person','person:james',$1,$1,TRUE)", now))
	must(db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ('James','James sails.','person','about:person:james',$1,$1)", now))
	must(db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at, meta) VALUES ('Jaymes','Jaymes rows.','person','person:jaymes',$1,$1,'{\"aliases\":[\"James\"]}'::jsonb)", now))
	// a distilled memory named for a person folds into theirs; one named for nobody known stays
	must(db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ('Cristina','Cristina is Vlad''s wife.','distilled','journal:5',$1,$1)", now))
	must(db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ('Cristina','Cristina likes Corfu.','distilled','journal:6',$1,$1)", now))
	must(db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ('Bitcoin','Vlad holds some.','distilled','journal:7',$1,$1)", now))

	folded, err := mergePeople(db, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if folded != 4 {
		t.Fatalf("folded %d", folded)
	}
	named, err := foldNamedMemories(db, quietLog())
	if err != nil || named != 2 {
		t.Fatalf("named %d %v", named, err)
	}
	if n := memRows(t, db, "kind = 'distilled'"); len(n) != 1 || n[0][1] != "Bitcoin" {
		t.Fatalf("distilled left: %v", n)
	}
	cr := memRows(t, db, "lower(title) LIKE 'cristina%'")
	if len(cr) != 1 {
		t.Fatalf("Cristina rows: %d", len(cr))
	}
	var m personMeta
	_ = json.Unmarshal([]byte(cr[0][4]), &m)
	if cr[0][1] != "Cristina Cealicu" || cr[0][2] != "person:cristina cealicu" {
		t.Fatalf("title %q ref %q", cr[0][1], cr[0][2])
	}
	// the distilled "wife" line was there already (the note); "likes Corfu" is a fact now
	if m.Note != "Cristina is Vlad's wife." || len(m.Facts) != 3 || m.Facts[2].T != "Cristina likes Corfu." || m.Facts[2].Ref != "journal:6" || len(m.Aliases) != 1 || m.Aliases[0] != "Cristina" {
		t.Fatalf("meta %+v", m)
	}
	if cr[0][3] != "Cristina is Vlad's wife. Cristina paints. Cristina Cealicu sails with Vlad. Cristina likes Corfu." {
		t.Fatalf("body %q", cr[0][3])
	}
	if len(memRows(t, db, "title LIKE 'Ana%'")) != 2 {
		t.Fatal("two Anas merged")
	}
	js := memRows(t, db, "title = 'James' OR title = 'Jaymes'")
	if len(js) != 1 || js[0][1] != "James" || js[0][5] != "true" || js[0][3] != "James, as I wrote him." {
		t.Fatalf("James rows %v: the hand-edited one is kept with its words and its spelling", js)
	}
	var jm0 personMeta
	_ = json.Unmarshal([]byte(js[0][4]), &jm0)
	if jm0.Note != "James sails." || len(jm0.Facts) != 1 || len(jm0.Aliases) != 1 || jm0.Aliases[0] != "Jaymes" {
		t.Fatalf("the edited James's meta %+v", jm0)
	}

	// a fact by the first name lands on the one row and is not added twice; a hand-edited row
	// takes the fact into its meta and keeps its body
	must(addPersonFact(db, "Cristina", "Cristina paints.", "chat:7", 7, now))
	must(addPersonFact(db, "cristina", "Cristina grew up in Brasov.", "chat:8", 8, now))
	must(addPersonFact(db, "James", "James moved to Lisbon.", "chat:9", 9, now))
	cr = memRows(t, db, "lower(title) LIKE 'cristina%'")
	m = personMeta{}
	_ = json.Unmarshal([]byte(cr[0][4]), &m)
	if len(cr) != 1 || len(m.Facts) != 4 || m.Facts[3].Ref != "chat:8" || !strings.HasSuffix(cr[0][3], "Cristina grew up in Brasov.") {
		t.Fatalf("after facts: %d rows, %+v, body %q", len(cr), m, cr[0][3])
	}
	js = memRows(t, db, "title = 'James' AND user_edited")
	var jm personMeta
	_ = json.Unmarshal([]byte(js[0][4]), &jm)
	if js[0][3] != "James, as I wrote him." || len(jm.Facts) != 2 {
		t.Fatalf("the edited James: body %q meta %+v", js[0][3], jm)
	}

	// the note rewritten: its part replaced, the facts kept; a person the note drops keeps the
	// facts and loses the note's line; one with nothing else goes
	must(setPersonNote(db, "Cristina Cealicu", "Cristina is Vlad's wife and a painter.", now))
	must(setPersonNote(db, "Toby", "Toby is a co-founder.", now))
	must(clearPersonNotes(db, []string{"Cristina Cealicu"}, now))
	cr = memRows(t, db, "lower(title) LIKE 'cristina%'")
	m = personMeta{}
	_ = json.Unmarshal([]byte(cr[0][4]), &m)
	if m.Note != "Cristina is Vlad's wife and a painter." || len(m.Facts) != 4 || !strings.HasPrefix(cr[0][3], "Cristina is Vlad's wife and a painter. Cristina paints.") {
		t.Fatalf("after the note: %+v body %q", m, cr[0][3])
	}
	if len(memRows(t, db, "title = 'Toby'")) != 0 {
		t.Fatal("Toby, dropped from the note with nothing else, stays")
	}
	// the people the distiller knows: one Cristina
	names := peopleNames(db)
	n := 0
	for _, x := range names {
		if strings.HasPrefix(strings.ToLower(x), "cristina") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("peopleNames %v", names)
	}
}

func TestConsolidatePGTripsFoldTheirParts(t *testing.T) {
	db := pgFresh(t, "lg_consolidate_trips")
	now := time.Now().UnixMilli()
	must := func(err error) {
		if err != nil {
			t.Fatal(err)
		}
	}
	outing := func(ref string, start, end int64, away bool, country, place string, photos int) {
		meta := map[string]any{"start": start, "end": end, "days": int((end-start)/86400) + 1, "photos": photos, "away": away,
			"country": country, "places": []string{place}, "covers": []string{"c" + ref[len(ref)-2:]}, "fromHomeM": 5000000.0, "distanceM": 12000.0}
		mb, _ := json.Marshal(meta)
		must(db.Exec("INSERT INTO memories (title, body, kind, source_ref, meta, created_at, updated_at) VALUES ($1,'template','outing',$2,$3::jsonb,$4,$4)", place, ref, string(mb), now))
	}
	outing("outing:2026-09-12", day("2026-09-12")+40000, day("2026-09-14")+70000, true, "Canada", "Toronto", 80)
	outing("outing:2026-09-16", day("2026-09-16")+30000, day("2026-09-18")+60000, true, "Canada", "Montreal", 60)
	outing("outing:2026-09-25", day("2026-09-25")+30000, day("2026-09-25")+60000, false, "United Kingdom", "Greenwich", 10)
	outing("outing:2026-10-02", day("2026-10-02")+30000, day("2026-10-03")+50000, true, "France", "Paris", 15) // alone, a night away: a trip of one outing
	for _, d := range []string{"2026-09-11", "2026-09-12", "2026-09-13", "2026-09-15", "2026-09-18", "2026-09-25", "2026-10-02", "2026-10-03"} {
		must(db.Exec("INSERT INTO memories (title, body, kind, source_ref, meta, created_at, updated_at) VALUES ($1,'a day','day',$2,'{}'::jsonb,$3,$3)", d, "day:"+d, now))
	}

	written, err := tripPass(db, quietLog())
	if err != nil {
		t.Fatal(err)
	}
	if written != 2 {
		t.Fatalf("trips written %d", written)
	}
	tr := memRows(t, db, "kind = 'trip' AND source_ref = 'trip:2026-09-12'")
	if len(tr) != 1 || tr[0][1] != "Canada, 12 to 18 September 2026" || tr[0][2] != "trip:2026-09-12" {
		t.Fatalf("trip %v", tr)
	}
	if paris := memRows(t, db, "kind = 'trip' AND source_ref = 'trip:2026-10-02'"); len(paris) != 1 || paris[0][1] != "France, 2 to 3 October 2026" {
		t.Fatalf("the lone outing's trip %v", paris)
	}
	var tm tripMeta
	_ = json.Unmarshal([]byte(tr[0][4]), &tm)
	if tm.Days != 7 || tm.Photos != 140 || len(tm.Outings) != 2 || tm.Template == "" || len(tm.Covers) != 2 {
		t.Fatalf("trip meta %+v", tm)
	}
	partOf := func(ref string) string {
		rows, err := db.Query("SELECT coalesce(meta->>'part_of','') FROM memories WHERE source_ref = $1", ref)
		if err != nil || len(rows.Vals) != 1 || rows.Vals[0][0] == nil {
			return "?"
		}
		return *rows.Vals[0][0]
	}
	for ref, want := range map[string]string{
		"outing:2026-09-12": "trip:2026-09-12", "outing:2026-09-16": "trip:2026-09-12", "outing:2026-09-25": "", "outing:2026-10-02": "trip:2026-10-02",
		"day:2026-09-11": "", "day:2026-09-12": "trip:2026-09-12", "day:2026-09-15": "trip:2026-09-12", "day:2026-09-18": "trip:2026-09-12",
		"day:2026-09-25": "", "day:2026-10-02": "trip:2026-10-02", "day:2026-10-03": "trip:2026-10-02",
	} {
		if got := partOf(ref); got != want {
			t.Fatalf("%s part_of %q, want %q", ref, got, want)
		}
	}
	// the same again: nothing written, nothing churned
	if written, err = tripPass(db, quietLog()); err != nil || written != 0 {
		t.Fatalf("second pass: %d %v", written, err)
	}
	// the person's edit outranks: a changed template leaves an edited trip alone
	must(db.Exec("UPDATE memories SET body = 'our Canada trip, as I tell it', user_edited = TRUE WHERE kind = 'trip'"))
	must(db.Exec("UPDATE memories SET meta = meta || '{\"photos\": 99}'::jsonb WHERE source_ref = 'outing:2026-09-16'"))
	if _, err = tripPass(db, quietLog()); err != nil {
		t.Fatal(err)
	}
	if tr = memRows(t, db, "kind = 'trip'"); tr[0][3] != "our Canada trip, as I tell it" {
		t.Fatalf("the edit was written over: %q", tr[0][3])
	}
	// Montreal re-clustered away: the trip is Toronto alone now (two nights, still a trip), its
	// facts changed, so the template is written again and the edit... was turned off above
	must(db.Exec("UPDATE memories SET user_edited = FALSE WHERE kind = 'trip'"))
	must(db.Exec("DELETE FROM memories WHERE source_ref = 'outing:2026-09-16'"))
	if _, err = tripPass(db, quietLog()); err != nil {
		t.Fatal(err)
	}
	if tr = memRows(t, db, "kind = 'trip' AND source_ref = 'trip:2026-09-12'"); len(tr) != 1 || tr[0][1] != "Canada, 12 to 14 September 2026" {
		t.Fatalf("the trip after Montreal went: %v", tr)
	}
	if got := partOf("day:2026-09-13"); got != "trip:2026-09-12" {
		t.Fatalf("day after Montreal went: %q", got)
	}
	// Toronto re-clustered as a day out (no night): the trip dissolves, the parts unfold
	must(db.Exec("UPDATE memories SET meta = meta || jsonb_build_object('end', $1::bigint) WHERE source_ref = 'outing:2026-09-12'", day("2026-09-12")+70000))
	if _, err = tripPass(db, quietLog()); err != nil {
		t.Fatal(err)
	}
	if len(memRows(t, db, "kind = 'trip' AND source_ref = 'trip:2026-09-12'")) != 0 {
		t.Fatal("a dissolved trip stays")
	}
	if got := partOf("outing:2026-09-12"); got != "" {
		t.Fatalf("outing still part of something: %q", got)
	}
	if got := partOf("day:2026-09-13"); got != "" {
		t.Fatalf("day still part of something: %q", got)
	}
	// the trip's sheet for the model names its outings and its days
	facts := tripFacts(db, "Canada, 12 to 18 September 2026", map[string]any{"start": float64(day("2026-09-12") + 40000), "end": float64(day("2026-09-18") + 60000), "days": float64(7), "photos": float64(140), "countries": []any{"Canada"}, "outings": []any{"outing:2026-09-12"}})
	joined := strings.Join(facts, "\n")
	if !strings.Contains(joined, "Days: 7") || !strings.Contains(joined, "Countries: Canada") || !strings.Contains(joined, "Part of it, Toronto: template") {
		t.Fatalf("facts:\n%s", joined)
	}
}
