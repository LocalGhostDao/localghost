package main

// The memory model against a real Postgres: the linking pass makes the table equal to what the
// memories imply and lets go of what went; a question brings its best match, the heavier of two
// equal ones first, and the best match's neighbour, and touches only what was asked about; the
// front page is chosen once and kept for the day, leads with the anchors, and is chosen again
// when it is emptied; an old month becomes one memory with its days under it, a day under a trip
// stays there, a month the person deleted stays deleted and its days stand alone; and the passes
// keep an anchor through a merge, a fold and a dissolved trip.
//
//	GHOST_PG_SOCKET_DIR=/tmp GHOST_PG_PORT=55432 GHOST_PG_USER=claude go test -run NeighboursPG ./cmd/ghost.synthd/

import (
	"encoding/json"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

type pgT struct {
	t  *testing.T
	db *poltergres.ReadWrite
}

func (p pgT) exec(q string, args ...any) {
	p.t.Helper()
	if err := p.db.Exec(q, args...); err != nil {
		p.t.Fatalf("%v\n  %s", err, q)
	}
}

func (p pgT) one(q string, args ...any) string {
	p.t.Helper()
	rows, err := p.db.Query(q, args...)
	if err != nil || len(rows.Vals) != 1 || len(rows.Vals[0]) == 0 {
		p.t.Fatalf("%s: %v %+v", q, err, rows)
	}
	return str(rows.Vals[0][0])
}

// mem inserts a memory and returns its id.
func (p pgT) mem(kind, ref, title, body, meta string, createdMs int64) int64 {
	p.t.Helper()
	if meta == "" {
		meta = "{}"
	}
	rows, err := p.db.Query("INSERT INTO memories (title, body, kind, source_ref, meta, created_at, updated_at) VALUES ($1,$2,$3,$4,$5::jsonb,$6,$6) RETURNING id",
		title, body, kind, ref, meta, createdMs)
	if err != nil || len(rows.Vals) != 1 {
		p.t.Fatalf("insert %s: %v", title, err)
	}
	id, _ := strconv.ParseInt(*rows.Vals[0][0], 10, 64)
	return id
}

func (p pgT) links() map[[2]int64]string {
	p.t.Helper()
	rows, err := p.db.Query("SELECT a, b, why FROM memory_links ORDER BY a, b")
	if err != nil {
		p.t.Fatal(err)
	}
	out := map[[2]int64]string{}
	for _, v := range rows.Vals {
		a, _ := strconv.ParseInt(*v[0], 10, 64)
		b, _ := strconv.ParseInt(*v[1], 10, 64)
		out[[2]int64{a, b}] = str(v[2])
	}
	return out
}

func lk(a, b int64) [2]int64 {
	if a > b {
		a, b = b, a
	}
	return [2]int64{a, b}
}

func TestNeighboursPGLinksGroundingFrontPage(t *testing.T) {
	p := pgT{t, pgFresh(t, "lgtest_synthd_neighbours")}
	db, lg := p.db, quietLog()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	ms := func(y int, m time.Month, d int) int64 { return time.Date(y, m, d, 20, 0, 0, 0, time.UTC).UnixMilli() }

	// a note written on 13 September, in a chat the journal holds; a check-in on 20 October
	p.exec("INSERT INTO journal_entries (source, ref, ts, title, body, created_at, distilled) VALUES ('chat','chat:9',$1,'the boat','we talked about the boat',$1,TRUE)",
		time.Date(2026, 9, 13, 18, 0, 0, 0, time.UTC).Unix())
	checkin := p.one("INSERT INTO journal_entries (source, ref, ts, title, body, created_at, distilled) VALUES ('ghost.noted','checkin-2026-10-20',$1,'Daily check-in 2026-10-20','Cristina cooked',$1,TRUE) RETURNING id",
		time.Date(2026, 10, 20, 21, 0, 0, 0, time.UTC).Unix())

	trip := p.mem("trip", "trip:2026-09-12", "Greece, 12 to 20 September 2026", "9 days away in Greece: Lakka and Voutoumi.",
		`{"start":`+strconv.FormatInt(time.Date(2026, 9, 12, 9, 0, 0, 0, time.UTC).Unix(), 10)+`,"places":["Lakka","Voutoumi"]}`, ms(2026, 9, 20))
	daySwim := p.mem("day", "day:2026-09-13", "Sunday 13 September 2026 · Voutoumi", "Cristina and Vlad swam off the boat at Voutoumi.",
		`{"part_of":"trip:2026-09-12","line":"Lakka → Voutoumi"}`, ms(2026, 9, 13))
	dayHome := p.mem("day", "day:2026-10-20", "Tuesday 20 October 2026", "A quiet day at home; it will rain, they said.", `{}`, ms(2026, 10, 20))
	cristina := p.mem("person", "person:cristina", "Cristina", "Cristina is Vlad's wife.",
		`{"note":"Cristina is Vlad's wife.","facts":[{"t":"Cristina cooked.","ref":"checkin:`+checkin+`"}],"aliases":["Cris"]}`, ms(2026, 7, 1))
	will := p.mem("person", "person:will", "Will", "Will is a colleague.", `{}`, ms(2026, 7, 2))
	place := p.mem("place", "place:voutoumi", "Voutoumi", "Vlad has been at Voutoumi on 2 days.", `{}`, ms(2026, 9, 21))
	boat := p.mem("distilled", "chat:9", "The boat", "Vlad wants a bigger anchor for the boat.", `{}`, ms(2026, 9, 13))
	old := p.mem("distilled", "chat:404", "The old boat", "Vlad sold the old boat in 2019.", `{}`, ms(2019, 5, 1))

	total, changed, err := linkPass(db, time.UTC, lg)
	if err != nil {
		t.Fatal(err)
	}
	want := map[[2]int64]string{
		lk(trip, daySwim):     linkPart,
		lk(cristina, daySwim): linkNames,
		lk(cristina, dayHome): linkWritten, // the check-in that day said something about her
		lk(place, trip):       linkPlace,
		lk(place, daySwim):    linkPlace,
		lk(boat, daySwim):     linkWritten, // the chat was that day
	}
	got := p.links()
	if total != len(want) || changed != len(want) || len(got) != len(want) {
		t.Fatalf("links: total %d changed %d\n got %v\nwant %v", total, changed, got, want)
	}
	for k, why := range want {
		if got[k] != why {
			t.Errorf("link %v: %q, want %q", k, got[k], why)
		}
	}
	if _, ok := got[lk(will, dayHome)]; ok {
		t.Error("\"it will rain\" linked a day to Will")
	}
	// the same memories, the same links: nothing is written the second time
	if _, changed, err = linkPass(db, time.UTC, lg); err != nil || changed != 0 {
		t.Fatalf("a second pass changed %d links: %v", changed, err)
	}

	// WEIGHT: the old boat note stands alone; the boat note is kept with the day (and so the trip's day)
	w, err := memWeights(db, nil, now.UnixMilli())
	if err != nil || w[boat] <= w[old] || w[cristina] <= w[will] {
		t.Fatalf("weights: %v boat %.3f old %.3f cristina %.3f will %.3f", err, w[boat], w[old], w[cristina], w[will])
	}
	only, err := memWeights(db, []int64{old}, now.UnixMilli())
	if err != nil || len(only) != 1 || only[old] != w[old] {
		t.Fatalf("weights for one: %v %v", err, only)
	}

	// A QUESTION about the boat: three memories hold the word (the day on the boat, the note about
	// its anchor, the note about the old one). The two that are kept in company go, the heavier
	// first; the old note, alone since 2019, does not. And the best match brings a neighbour the
	// question did not name: the trip, the person or the place that day is kept with.
	items, err := memoriesFor(db, []string{"boat"}, now.UnixMilli())
	if err != nil || len(items) != 3 {
		t.Fatalf("grounding: %v %+v", err, items)
	}
	if !strings.HasPrefix(items[0].Snippet, "Sunday 13 September") || !strings.HasPrefix(items[1].Snippet, "The boat ,") {
		t.Fatalf("order: %q / %q", items[0].Snippet, items[1].Snippet)
	}
	last := items[2]
	if !strings.Contains(last.Why, "kept with \"Sunday 13 September 2026 · Voutoumi\"") || last.Score >= items[0].Score ||
		strings.HasPrefix(last.Snippet, "The boat") || strings.HasPrefix(last.Snippet, "The old boat") {
		t.Fatalf("the neighbour: %+v", last)
	}
	if p.one("SELECT touched_at FROM memories WHERE id = $1", boat) != strconv.FormatInt(now.UnixMilli(), 10) {
		t.Fatal("a matched memory was not touched")
	}
	if n := p.one("SELECT count(*) FROM memories WHERE touched_at > 0"); n != "2" {
		t.Fatalf("%s memories touched: only the two that matched should be", n)
	}
	// an anchor wins a tie outright, and says so
	p.exec("UPDATE memories SET anchored_at = 5 WHERE id = $1", old)
	items, _ = memoriesFor(db, []string{"sold", "anchor"}, now.UnixMilli())
	if len(items) < 2 || !strings.HasPrefix(items[0].Snippet, "The old boat") || !strings.Contains(items[0].Why, "anchor") {
		t.Fatalf("an anchor did not lead a tie: %+v", items)
	}
	// a memory in doubt is not brought, as a match or as a neighbour
	p.exec("UPDATE memories SET meta = meta || '{\"doubt\":{\"at\":1}}'::jsonb WHERE id = $1", daySwim)
	items, _ = memoriesFor(db, []string{"bigger"}, now.UnixMilli())
	for _, it := range items {
		if strings.HasPrefix(it.Snippet, "Sunday 13 September") {
			t.Fatalf("a doubted memory reached the chat: %+v", it)
		}
	}
	p.exec("UPDATE memories SET meta = meta - 'doubt' WHERE id = $1", daySwim)

	// THE FRONT PAGE: chosen once, kept for the day, the anchor first
	p.exec("INSERT INTO settings (key, value) VALUES ('about_me', 'I am Vlad. I live in London.')")
	page := frontPageFor(db, now)
	if !strings.HasPrefix(page, "What I have told you about me and my people:\nI am Vlad. I live in London.") ||
		!strings.Contains(page, "The memories I marked as the ones my life hangs on:\n- The old boat: Vlad sold the old boat in 2019.") ||
		!strings.Contains(page, "- Cristina is Vlad's wife.") || strings.Contains(page, "Sunday 13 September") {
		t.Fatalf("the page:\n%s", page)
	}
	// a distilled memory the box has not yet read again is not at the head of every chat; once
	// the read-again pass has confirmed it against its record it may be
	if strings.Contains(page, "bigger anchor") {
		t.Fatalf("an unconfirmed distilled memory is on the front page:\n%s", page)
	}
	p.exec("UPDATE memories SET meta = meta || '{\"verified_at\":1}'::jsonb WHERE id = $1", boat)
	p.exec("UPDATE settings SET value = '' WHERE key = $1", hw.FrontPageKey)
	page = frontPageFor(db, now)
	if !strings.Contains(page, frontHeavyHead) || !strings.Contains(page, "- The boat: Vlad wants a bigger anchor for the boat.") {
		t.Fatalf("a confirmed distilled memory is missing from the page:\n%s", page)
	}
	var fp frontPage
	if err := json.Unmarshal([]byte(p.one("SELECT value FROM settings WHERE key = $1", hw.FrontPageKey)), &fp); err != nil || fp.Text != page || len(fp.IDs) == 0 || fp.IDs[0] != old {
		t.Fatalf("the stored page: %v %+v", err, fp)
	}
	// a memory made later that day does not change the page: the chat's opening stays the same bytes
	p.mem("user", "", "A new thing", "Written this afternoon.", `{}`, now.UnixMilli())
	if again := frontPageFor(db, now.Add(3*time.Hour)); again != page {
		t.Fatal("the page changed within the day")
	}
	// tomorrow it is chosen again, and so it is when the store empties it (an anchor, an edit, a delete)
	if next := frontPageFor(db, now.Add(24*time.Hour)); !strings.Contains(next, "A new thing") {
		t.Fatalf("tomorrow's page:\n%s", next)
	}
	p.exec("UPDATE memories SET tombstoned = TRUE WHERE id = $1", old)
	p.exec("UPDATE settings SET value = '' WHERE key = $1", hw.FrontPageKey)
	if after := frontPageFor(db, now.Add(24*time.Hour)); strings.Contains(after, "The old boat") {
		t.Fatal("a deleted memory is still on the front page")
	}
	// the note changing chooses again too
	p.exec("UPDATE settings SET value = 'I am Vlad. I moved to Lisbon.' WHERE key = 'about_me'")
	if moved := frontPageFor(db, now.Add(24*time.Hour)); !strings.Contains(moved, "Lisbon") {
		t.Fatal("the page kept the old note")
	}

	// LETTING GO: a memory that is gone loses its links at the next pass
	p.exec("DELETE FROM memories WHERE id = $1", place)
	if _, changed, err = linkPass(db, time.UTC, lg); err != nil || changed != 2 {
		t.Fatalf("the place's two links should go: changed %d, %v", changed, err)
	}
	for k := range p.links() {
		if k[0] == place || k[1] == place {
			t.Fatal("a link to a memory that is gone")
		}
	}
}

func TestNeighboursPGMonths(t *testing.T) {
	p := pgT{t, pgFresh(t, "lgtest_synthd_months")}
	db, lg := p.db, quietLog()
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	dayMs := func(d string) int64 { tt, _ := time.Parse("2006-01-02", d); return tt.Add(23 * time.Hour).UnixMilli() }
	day := func(d, meta string) int64 { return p.mem("day", "day:"+d, d, "a day", meta, dayMs(d)) }
	sheet := func(d string, photos int, place string) {
		f, _ := json.Marshal(dayFacts{Day: d, Photos: photos, Places: []string{place}, Covers: []string{"cover-" + d}})
		p.exec("INSERT INTO day_summaries (day, built_at, facts) VALUES ($1, 100, $2::jsonb)", d, string(f))
	}
	// August: four days on their own, one under a trip; September: two days (too few);
	// October and November: recent, left day by day
	var aug []int64
	for _, d := range []string{"2026-08-01", "2026-08-02", "2026-08-09", "2026-08-30"} {
		aug = append(aug, day(d, `{}`))
		sheet(d, 10, "Woolwich")
	}
	tripDay := day("2026-08-20", `{"part_of":"trip:2026-08-18"}`)
	p.mem("trip", "trip:2026-08-18", "Greece, 18 to 26 August 2026", "9 days away.", `{}`, dayMs("2026-08-26"))
	outing := p.mem("outing", "outing:2026-08-09", "Greenwich", "a day out", `{}`, dayMs("2026-08-09"))
	sep1, sep2 := day("2026-09-03", `{}`), day("2026-09-04", `{}`)
	recent := day("2026-10-20", `{}`)

	n, err := monthPass(db, now, lg)
	if err != nil || n != 1 {
		t.Fatalf("months written %d: %v", n, err)
	}
	if got := p.one("SELECT title || ' | ' || body || ' | ' || created_at FROM memories WHERE kind = 'month'"); got !=
		"August 2026 | 4 days kept and 1 outing, 40 photos. Mostly at Woolwich. A trip began: Greece, 18 to 26 August 2026. | "+
			strconv.FormatInt(time.Date(2026, 8, 31, 23, 59, 59, 0, time.UTC).UnixMilli(), 10) {
		t.Fatalf("the month: %s", got)
	}
	part := func(id int64) string {
		return p.one("SELECT COALESCE(meta->>'part_of','') FROM memories WHERE id = $1", id)
	}
	for _, id := range append(aug, outing) {
		if part(id) != "month:2026-08" {
			t.Fatalf("memory %d is not under August: %q", id, part(id))
		}
	}
	if part(tripDay) != "trip:2026-08-18" {
		t.Fatalf("a trip's day was taken by the month: %q", part(tripDay))
	}
	if part(sep1) != "" || part(sep2) != "" || part(recent) != "" {
		t.Fatal("two days are not a month, and October is not old yet")
	}
	// nothing changed: nothing is written again
	if n, err = monthPass(db, now, lg); err != nil || n != 0 {
		t.Fatalf("a second pass wrote %d months: %v", n, err)
	}
	// the trip pass's own clearing of folds leaves the month's alone
	p.exec("UPDATE memories SET meta = meta - 'part_of' WHERE kind IN ('outing','day') AND meta IS NOT NULL AND jsonb_exists(meta, 'part_of') AND meta->>'part_of' NOT LIKE 'month:%'")
	if part(aug[0]) != "month:2026-08" || part(tripDay) != "" {
		t.Fatal("the trip pass's clearing touched the wrong folds")
	}
	// the trip's day, let go by a trip that dissolved, is picked up by its month; the month is counted again
	if n, err = monthPass(db, now, lg); err != nil || n != 1 || part(tripDay) != "month:2026-08" {
		t.Fatalf("after the trip let go: wrote %d, %v, part %q", n, err, part(tripDay))
	}
	if !strings.HasPrefix(p.one("SELECT body FROM memories WHERE kind = 'month'"), "5 days kept and 1 outing") {
		t.Fatal("the month was not counted again")
	}
	// a day told again keeps its fold (projectDayMemory writes the meta whole)
	if err := projectDayMemory(db, "2026-08-01", "Saturday 1 August 2026", "told again", "template", &dayFacts{Day: "2026-08-01", Photos: 11}); err != nil {
		t.Fatal(err)
	}
	if part(aug[0]) != "month:2026-08" || p.one("SELECT body FROM memories WHERE id = $1", aug[0]) != "told again" {
		t.Fatal("a day told again lost its fold")
	}
	// the links: each folded day is part of the month, and the trip is linked to the month it began in
	if _, _, err := linkPass(db, time.UTC, lg); err != nil {
		t.Fatal(err)
	}
	month, _ := strconv.ParseInt(p.one("SELECT id FROM memories WHERE kind = 'month'"), 10, 64)
	links := p.links()
	if links[lk(month, aug[0])] != linkPart || links[lk(month, outing)] != linkPart {
		t.Fatalf("the month's parts are not linked: %v", links)
	}
	tripID, _ := strconv.ParseInt(p.one("SELECT id FROM memories WHERE kind = 'trip'"), 10, 64)
	_ = tripID // the trip has no meta.start here, so no month link: links come from what the rows hold

	// the person edits the month: their words stand, the days stay under it
	p.exec("UPDATE memories SET body = 'The summer we fixed the boat.', user_edited = TRUE WHERE id = $1", month)
	sheet("2026-08-15", 3, "Greenwich")
	extra := day("2026-08-15", `{}`)
	if _, err = monthPass(db, now, lg); err != nil {
		t.Fatal(err)
	}
	if p.one("SELECT body FROM memories WHERE id = $1", month) != "The summer we fixed the boat." || part(extra) != "month:2026-08" {
		t.Fatal("an edited month was rewritten, or stopped folding its days")
	}
	// the person deletes the month: it stays deleted, and its days stand on their own again
	p.exec("UPDATE memories SET tombstoned = TRUE WHERE id = $1", month)
	if n, err = monthPass(db, now, lg); err != nil || n != 0 {
		t.Fatalf("a deleted month was written again: %d %v", n, err)
	}
	if p.one("SELECT count(*) FROM memories WHERE kind = 'month' AND NOT tombstoned") != "0" || part(aug[0]) != "" || part(outing) != "" {
		t.Fatal("a deleted month came back, or kept its days hidden under it")
	}
}

func TestNeighboursPGAnchorsSurviveThePasses(t *testing.T) {
	p := pgT{t, pgFresh(t, "lgtest_synthd_anchors")}
	db, lg := p.db, quietLog()
	now := time.Now().UnixMilli()
	anchored := func(id int64) bool {
		return p.one("SELECT (anchored_at IS NOT NULL)::text FROM memories WHERE id = $1", id) == "true"
	}
	live := func(id int64) bool {
		return p.one("SELECT count(*) FROM memories WHERE id = $1 AND NOT tombstoned", id) == "1"
	}
	// two rows for one person, the anchor on the one that will be merged away
	keep := p.mem("person", "person:cristina", "Cristina", "Cristina paints.", `{}`, now-2)
	dup := p.mem("person", "about:person:cristina", "Cristina", "Cristina is Vlad's wife.", `{}`, now-3)
	p.exec("UPDATE memories SET anchored_at = 7, touched_at = 9 WHERE id = $1", dup)
	// a distilled memory named for a person: anchored, it is not folded into theirs
	named := p.mem("distilled", "journal:5", "Cristina", "Cristina likes Corfu.", `{}`, now)
	p.exec("UPDATE memories SET anchored_at = 8 WHERE id = $1", named)
	plain := p.mem("distilled", "journal:6", "Cristina", "Cristina sails.", `{}`, now)
	if _, err := mergePeople(db, lg); err != nil {
		t.Fatal(err)
	}
	if live(dup) || !anchored(keep) || p.one("SELECT touched_at FROM memories WHERE id = $1", keep) != "9" {
		t.Fatal("the anchor was not carried to the row that stayed")
	}
	if _, err := foldNamedMemories(db, lg); err != nil {
		t.Fatal(err)
	}
	if !live(named) || !anchored(named) || live(plain) {
		t.Fatal("an anchored memory was folded away, or a plain one was not")
	}
	// a trip that no longer holds: gone, unless anchored
	gone := p.mem("trip", "trip:2020-01-01", "Away, 1 to 3 January 2020", "x", `{}`, now)
	stays := p.mem("trip", "trip:2021-01-01", "Away, 1 to 3 January 2021", "x", `{}`, now)
	p.exec("UPDATE memories SET anchored_at = 3 WHERE id = $1", stays)
	if _, err := tripPass(db, lg); err != nil {
		t.Fatal(err)
	}
	if live(gone) || !live(stays) || !anchored(stays) {
		t.Fatal("a dissolved trip: the anchored one must stay, the other go")
	}
	// the note's own memories are made again when the note changes: the mark is carried by title.
	// (aboutPass needs the model; this is its carrying, on its own.)
	me := p.mem("me", "about:me:0", "Home", "Vlad lives in London.", `{}`, now)
	p.exec("UPDATE memories SET anchored_at = 11 WHERE id = $1", me)
	carried := p.one("SELECT anchored_at FROM memories WHERE source_ref LIKE 'about:%' AND NOT tombstoned AND NOT user_edited AND anchored_at IS NOT NULL AND lower(title) = 'home'")
	p.exec("DELETE FROM memories WHERE source_ref LIKE 'about:%' AND NOT user_edited AND NOT tombstoned")
	p.exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at, anchored_at) VALUES ($1,$2,'me',$3,$4,$4,NULLIF($5,'')::bigint)", "Home", "Vlad lives in Lisbon.", "about:me:0", now, carried)
	p.exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at, anchored_at) VALUES ($1,$2,'me',$3,$4,$4,NULLIF($5,'')::bigint)", "Work", "Vlad builds things.", "about:me:1", now, "")
	if p.one("SELECT anchored_at FROM memories WHERE source_ref = 'about:me:0'") != "11" || p.one("SELECT (anchored_at IS NULL)::text FROM memories WHERE source_ref = 'about:me:1'") != "true" {
		t.Fatal("the note's remade row lost its anchor, or another gained one")
	}
}

// A day emptied of everything it was told from goes, anchor or not, and when it was an anchor
// the front page is chosen again at once; an empty day that had no memory leaves the page alone
// (the pass asks about every empty day it looks at, every ten minutes).
func TestNeighboursPGAnEmptiedAnchoredDayLeavesTheFrontPage(t *testing.T) {
	p := pgT{t, pgFresh(t, "lgtest_synthd_emptyday")}
	db := p.db
	now := time.Date(2026, 11, 12, 12, 0, 0, 0, time.UTC)
	day := p.mem("day", "day:2026-11-01", "Sunday 1 November 2026", "The day at the lake.", `{}`, now.UnixMilli())
	p.exec("UPDATE memories SET anchored_at = 5 WHERE id = $1", day)
	page := frontPageFor(db, now)
	if !strings.Contains(page, "The day at the lake.") {
		t.Fatalf("the anchored day is not on the page:\n%s", page)
	}
	// a day with no memory at all: nothing to drop, the page stands
	if err := dropEmptyDay(db, "2026-10-30"); err != nil {
		t.Fatal(err)
	}
	if p.one("SELECT value FROM settings WHERE key = $1", hw.FrontPageKey) == "" {
		t.Fatal("an empty day with no memory emptied the front page")
	}
	// the anchored day's records are gone: the memory goes and the page is chosen again
	if err := dropEmptyDay(db, "2026-11-01"); err != nil {
		t.Fatal(err)
	}
	if p.one("SELECT count(*) FROM memories WHERE id = $1", day) != "0" {
		t.Fatal("an emptied day was kept because it was anchored")
	}
	if p.one("SELECT value FROM settings WHERE key = $1", hw.FrontPageKey) != "" {
		t.Fatal("the front page still holds the emptied day")
	}
	if page = frontPageFor(db, now); strings.Contains(page, "lake") {
		t.Fatalf("the emptied day is still quoted:\n%s", page)
	}
}
