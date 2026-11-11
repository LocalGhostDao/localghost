package main

// PLACES AND WHAT THE BOX NOTICES, as memories beside the distilled ones.
//
// Places (kind 'place'): where the person keeps going, from the day routes framed tells (the
// stays, named) and the photos (where they were taken): one memory per place, with how many days,
// from when to when, how long in all, the weekday it is most often, and the photos taken there.
// Counted, not written by the model: every number is the trail's or the archive's. Made again when
// the routes change; a place edited by hand or deleted stays as the person left it.
//
// What the box notices (kind 'insight'): once a day, on the GPU, the model reads a sheet of the
// last weeks counted from the box (the places, the walking, the steps by weekday, the photos and
// what is in them, the check-ins' feelings, the people mentioned) and writes up to three short
// memories that notice something: a habit, a change, a contrast. Kept only when every number in
// one is on the sheet; no advice, no guessing why.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/dayroute"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

const (
	placesSigKey  = "places_sig"  // the routes the places were counted from
	insightDayKey = "insight_day" // the day the last insights were written
	placesKept    = 40
)

// placeAgg is one place counted over the days.
type placeAgg struct {
	Name     string
	Days     map[string]bool
	Seconds  int64
	Photos   int
	PhotoDay map[string]bool
}

func (a *placeAgg) firstLast() (string, string) {
	var ds []string
	for d := range a.Days {
		ds = append(ds, d)
	}
	for d := range a.PhotoDay {
		ds = append(ds, d)
	}
	sort.Strings(ds)
	if len(ds) == 0 {
		return "", ""
	}
	return ds[0], ds[len(ds)-1]
}

func (a *placeAgg) allDays() int {
	n := len(a.Days)
	for d := range a.PhotoDay {
		if !a.Days[d] {
			n++
		}
	}
	return n
}

// placeKey folds the ways one place is named ("near Kassiopi" and "Kassiopi") into one.
func placeKey(name string) string {
	n := strings.TrimSpace(name)
	n = strings.TrimPrefix(strings.TrimPrefix(n, "near "), "Near ")
	return strings.ToLower(n)
}

// aggregatePlaces counts the named stays of the days (pure, for the tests).
func aggregatePlaces(days []dayroute.Day) map[string]*placeAgg {
	out := map[string]*placeAgg{}
	for _, d := range days {
		for _, st := range d.Stays {
			k := placeKey(st.Name)
			if k == "" {
				continue
			}
			a := out[k]
			if a == nil {
				a = &placeAgg{Name: strings.TrimPrefix(strings.TrimSpace(st.Name), "near "), Days: map[string]bool{}, PhotoDay: map[string]bool{}}
				out[k] = a
			}
			a.Days[d.Day] = true
			if st.To > st.From {
				a.Seconds += st.To - st.From
			}
		}
	}
	return out
}

// placeBody is a place's memory in words (pure, for the tests): by the person's name, "I" without.
func placeBody(owner string, a *placeAgg) string {
	who, have := "I", "have"
	if owner != "" {
		who, have = owner, "has"
	}
	first, last := a.firstLast()
	n := a.allDays()
	var b strings.Builder
	if len(a.Days) > 0 {
		fmt.Fprintf(&b, "%s %s been at %s on %d day%s", who, have, a.Name, n, plural(n))
	} else {
		fmt.Fprintf(&b, "%s %s taken photos at %s on %d day%s", who, have, a.Name, n, plural(n))
	}
	if first != last && first != "" {
		fmt.Fprintf(&b, ", from %s to %s", humanDay(first), humanDay(last))
	} else if first != "" {
		fmt.Fprintf(&b, ", on %s", humanDay(first))
	}
	if h := a.Seconds / 3600; h >= 2 {
		fmt.Fprintf(&b, ", about %d hours in all", h)
	}
	if wd := commonWeekday(a.Days); wd != "" {
		fmt.Fprintf(&b, ", most often on a %s", wd)
	}
	b.WriteString(".")
	if a.Photos > 0 {
		fmt.Fprintf(&b, " %d photo%s taken there.", a.Photos, plural(a.Photos))
	}
	return b.String()
}

func humanDay(day string) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.Format("2 January 2006")
}

// commonWeekday is the weekday most of the days fall on, when one stands out (a third of them or
// more, and three days at least); "" otherwise.
func commonWeekday(days map[string]bool) string {
	var n [7]int
	total := 0
	for d := range days {
		if t, err := time.Parse("2006-01-02", d); err == nil {
			n[t.Weekday()]++
			total++
		}
	}
	best := 0
	for i := 1; i < 7; i++ {
		if n[i] > n[best] {
			best = i
		}
	}
	if n[best] < 3 || 3*n[best] < total {
		return ""
	}
	return time.Weekday(best).String()
}

// routesSig is the day routes' signature without reading them: how many, and the newest.
func routesSig(mount string) string {
	ents, err := os.ReadDir(filepath.Join(mount, "frames", "paths"))
	if err != nil {
		return ""
	}
	n, newest := 0, int64(0)
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".route.json") {
			continue
		}
		n++
		if info, err := e.Info(); err == nil && info.ModTime().Unix() > newest {
			newest = info.ModTime().Unix()
		}
	}
	return strconv.Itoa(n) + ":" + strconv.FormatInt(newest, 10)
}

// readRoutes is every day route framed wrote, and a signature of them (how many, the newest).
func readRoutes(mount string) ([]dayroute.Day, string) {
	dir := filepath.Join(mount, "frames", "paths")
	ents, err := os.ReadDir(dir)
	if err != nil {
		return nil, ""
	}
	var out []dayroute.Day
	var newest int64
	for _, e := range ents {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".route.json") {
			continue
		}
		if info, err := e.Info(); err == nil && info.ModTime().Unix() > newest {
			newest = info.ModTime().Unix()
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		var d dayroute.Day
		if json.Unmarshal(b, &d) == nil && d.Day != "" {
			out = append(out, d)
		}
	}
	return out, strconv.Itoa(len(out)) + ":" + strconv.FormatInt(newest, 10)
}

// placesPass counts the places and keeps their memories. Returns how many it wrote or changed.
func placesPass(db *poltergres.ReadWrite, mount string, lg *slog.Logger) (int, error) {
	sig := routesSig(mount)
	var photoSig string
	if rows, err := db.Query("SELECT count(*)::text || ':' || coalesce(max(taken_at),0)::text FROM frames WHERE kind = 'photo' AND place <> ''"); err == nil && len(rows.Vals) == 1 && rows.Vals[0][0] != nil {
		photoSig = *rows.Vals[0][0]
	}
	owner := setting(db, ownerKey)
	full := sig + "|" + photoSig + "|" + owner
	if setting(db, placesSigKey) == full {
		return 0, nil
	}
	days, _ := readRoutes(mount)
	aggs := aggregatePlaces(days)
	// the photos, by the most precise part of where they were taken, and the days they were taken on
	if rows, err := db.Query(`SELECT place, to_char(to_timestamp(taken_at), 'YYYY-MM-DD'), count(*) FROM frames
		WHERE kind = 'photo' AND place <> '' AND taken_at > 0 GROUP BY 1, 2`); err == nil {
		for _, v := range rows.Vals {
			if len(v) < 3 || v[0] == nil || v[1] == nil {
				continue
			}
			name := lastPlacePart(*v[0])
			k := placeKey(name)
			if k == "" {
				continue
			}
			n, _ := strconv.Atoi(str(v[2]))
			a := aggs[k]
			if a == nil {
				a = &placeAgg{Name: name, Days: map[string]bool{}, PhotoDay: map[string]bool{}}
				aggs[k] = a
			}
			a.Photos += n
			a.PhotoDay[*v[1]] = true
		}
	}
	var keep []*placeAgg
	for _, a := range aggs {
		if len(a.Days) >= 3 || a.allDays() >= 3 || (len(a.PhotoDay) >= 2 && a.Photos >= 10) {
			keep = append(keep, a)
		}
	}
	sort.Slice(keep, func(i, j int) bool {
		if keep[i].allDays() != keep[j].allDays() {
			return keep[i].allDays() > keep[j].allDays()
		}
		return keep[i].Photos > keep[j].Photos
	})
	if len(keep) > placesKept {
		keep = keep[:placesKept]
	}
	now := time.Now().UnixMilli()
	wrote := 0
	for _, a := range keep {
		ref := "place:" + placeKey(a.Name)
		body := placeBody(owner, a)
		rows, err := db.Query("SELECT id, body, user_edited, tombstoned FROM memories WHERE kind = 'place' AND source_ref = $1 LIMIT 1", ref)
		if err != nil {
			return wrote, err
		}
		if len(rows.Vals) == 1 {
			v := rows.Vals[0]
			if str(v[2]) == "t" || str(v[3]) == "t" || str(v[1]) == body {
				continue
			}
			if err := db.Exec("UPDATE memories SET body = $2, updated_at = $3 WHERE id = $1", str(v[0]), body, now); err != nil {
				return wrote, err
			}
		} else if err := db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ($1,$2,'place',$3,$4,$4)",
			a.Name, body, ref, now); err != nil {
			return wrote, err
		}
		wrote++
	}
	if wrote > 0 {
		lg.Info("places counted into memories", "fn", "placesPass", "places", len(keep), "written", wrote)
	}
	return wrote, setSetting(db, placesSigKey, full)
}

// lastPlacePart is the most precise part of a place hierarchy ("Europe / Greece / Corfu / Kassiopi").
func lastPlacePart(place string) string {
	p := strings.Split(place, " / ")
	for i := len(p) - 1; i >= 0; i-- {
		if t := strings.TrimSpace(p[i]); t != "" {
			return t
		}
	}
	return ""
}

// --- what the box notices ---

// insightFacts is the sheet of the last weeks, counted from the box (every line a fact the model may
// use; nothing is said about a part the box has no data for).
func insightFacts(db *poltergres.ReadWrite, mount, owner string, now time.Time) []string {
	who := owner
	if who == "" {
		who = "I"
	}
	var facts []string
	add := func(format string, a ...any) { facts = append(facts, fmt.Sprintf(format, a...)) }
	day := func(t time.Time) string { return t.UTC().Format("2006-01-02") }
	m30, m60 := day(now.AddDate(0, 0, -30)), day(now.AddDate(0, 0, -60))
	// the places and the walking, this month and the one before
	routes, _ := readRoutes(mount)
	var recent, before []dayroute.Day
	walk30, walk60 := 0.0, 0.0
	for _, d := range routes {
		switch {
		case d.Day >= m30:
			recent = append(recent, d)
			walk30 += d.WalkM
		case d.Day >= m60:
			before = append(before, d)
			walk60 += d.WalkM
		}
	}
	if len(recent) > 0 {
		aggs := aggregatePlaces(recent)
		var top []*placeAgg
		for _, a := range aggs {
			top = append(top, a)
		}
		sort.Slice(top, func(i, j int) bool { return len(top[i].Days) > len(top[j].Days) })
		for i, a := range top {
			if i == 3 || len(a.Days) < 2 {
				break
			}
			add("In the last 30 days %s was at %s on %d days.", who, a.Name, len(a.Days))
		}
		if wd := commonWeekday(func() map[string]bool {
			m := map[string]bool{}
			for _, d := range recent {
				if d.WalkM >= 3000 {
					m[d.Day] = true
				}
			}
			return m
		}()); wd != "" {
			add("In the last 30 days %s walked furthest most often on a %s.", who, wd)
		}
	}
	if walk30 >= 1000 {
		add("%s walked about %d km in the last 30 days.", who, int(walk30/1000))
		if walk60 >= 1000 {
			add("%s walked about %d km in the 30 days before that.", who, int(walk60/1000))
		}
	}
	// the steps by weekday, the last eight weeks
	if rows, err := db.Query(`SELECT extract(dow FROM day::date)::int, round(avg(value))::bigint, count(*) FROM health_metrics
		WHERE metric = 'steps' AND day >= $1 AND value > 0 GROUP BY 1 ORDER BY 2 DESC`, day(now.AddDate(0, 0, -56))); err == nil && len(rows.Vals) >= 5 {
		hi, lo := rows.Vals[0], rows.Vals[len(rows.Vals)-1]
		hd, _ := strconv.Atoi(str(hi[0]))
		ld, _ := strconv.Atoi(str(lo[0]))
		add("Over the last eight weeks %s's steps were highest on %ss (about %s a day) and lowest on %ss (about %s a day).",
			who, time.Weekday(hd), str(hi[1]), time.Weekday(ld), str(lo[1]))
	}
	// the photos, and what is in them
	if rows, err := db.Query("SELECT count(*) FROM frames WHERE kind = 'photo' AND taken_at >= $1", now.AddDate(0, 0, -30).Unix()); err == nil && len(rows.Vals) == 1 {
		if n, _ := strconv.Atoi(str(rows.Vals[0][0])); n > 0 {
			add("%s took %d photos in the last 30 days.", who, n)
		}
	}
	if rows, err := db.Query(`SELECT t.tag, count(DISTINCT f.hash) FROM frames f JOIN frame_tags t ON t.hash = f.hash
		WHERE f.kind = 'photo' AND f.taken_at >= $1 AND t.category NOT IN ('text','style') GROUP BY 1 ORDER BY 2 DESC LIMIT 3`, now.AddDate(0, 0, -30).Unix()); err == nil {
		var bits []string
		for _, v := range rows.Vals {
			if len(v) == 2 && v[0] != nil {
				bits = append(bits, fmt.Sprintf("%s (%s photos)", *v[0], str(v[1])))
			}
		}
		if len(bits) > 0 {
			add("What was in most of those photos: %s.", strings.Join(bits, ", "))
		}
	}
	// the check-ins' feelings
	if rows, err := db.Query(`SELECT body FROM journal_entries WHERE source = 'ghost.noted' AND title LIKE 'Daily check-in %' AND ts >= $1`, now.AddDate(0, 0, -30).Unix()); err == nil && len(rows.Vals) > 0 {
		count := map[string]int{}
		for _, v := range rows.Vals {
			for _, line := range strings.Split(str(v[0]), "\n") {
				if f, ok := strings.CutPrefix(strings.TrimSpace(line), "Feeling: "); ok {
					for _, w := range strings.Split(f, ",") {
						if w = strings.ToLower(strings.TrimSpace(w)); w != "" {
							count[w]++
						}
					}
				}
			}
		}
		type wc struct {
			w string
			n int
		}
		var ws []wc
		for w, n := range count {
			ws = append(ws, wc{w, n})
		}
		sort.Slice(ws, func(i, j int) bool { return ws[i].n > ws[j].n })
		var bits []string
		for i, x := range ws {
			if i == 3 {
				break
			}
			bits = append(bits, fmt.Sprintf("%s (%d times)", x.w, x.n))
		}
		if len(bits) > 0 {
			add("At %d check-ins in the last 30 days %s felt most often: %s.", len(rows.Vals), who, strings.Join(bits, ", "))
		}
	}
	// the people mentioned in the last 30 days' notes, voice notes and chats
	for _, name := range peopleNames(db) {
		if rows, err := db.Query(`SELECT count(*) FROM journal_entries WHERE ts >= $1 AND source IN ('ghost.noted','ghost.voiced','ghost.synthd')
			AND (body ILIKE '%' || $2 || '%' OR title ILIKE '%' || $2 || '%')`, now.AddDate(0, 0, -30).Unix(), name); err == nil && len(rows.Vals) == 1 {
			if n, _ := strconv.Atoi(str(rows.Vals[0][0])); n >= 2 {
				add("%s came up in %d of %s's notes and chats in the last 30 days.", name, n, who)
			}
		}
	}
	return facts
}

// insightPrompt asks for what the sheet shows, as short memories (pure, for the tests).
func insightPrompt(owner string, facts []string) string {
	who := owner
	if who == "" {
		who = "me"
	}
	var b strings.Builder
	b.WriteString("Below are facts about " + who + "'s last weeks, counted by the box from the trail, the photos, the health sync, the check-ins and the notes. ")
	b.WriteString("Write up to 3 short memories that notice something in them: a habit, a change from one month to the next, a contrast, a small story the numbers tell. ")
	b.WriteString("Each one or two sentences, warm and plain, ")
	if owner != "" {
		b.WriteString("about " + owner + " by name in the third person. ")
	} else {
		b.WriteString("in the first person. ")
	}
	b.WriteString("Use only these facts: every number must be one of theirs; no advice, no judgement, no guessing why. One per line, exactly: TITLE | memory. If the facts show nothing worth a memory, reply with exactly: NONE\n\nFACTS:\n")
	for _, f := range facts {
		b.WriteString("- " + f + "\n")
	}
	return b.String()
}

// parseInsights reads the model's lines against the sheet (pure, for the tests).
func parseInsights(out, owner string, facts []string) [][2]string {
	var got [][2]string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(strings.TrimLeft(strings.TrimSpace(line), "-*•0123456789. "))
		p := strings.SplitN(line, "|", 2)
		if len(p) != 2 {
			continue
		}
		title := strings.Trim(strings.TrimSpace(p[0]), "\"*")
		body, ok := groundedProse(named(strings.TrimSpace(p[1]), owner), facts)
		if !ok || title == "" || len(title) > 80 || strings.Contains(strings.ToLower(body), "the user") {
			continue
		}
		got = append(got, [2]string{title, body})
		if len(got) == 3 {
			break
		}
	}
	return got
}

// insightPass writes the day's insights, once a day, on the GPU. Returns how many it kept.
func insightPass(db *poltergres.ReadWrite, oc *oracle.Client, mount string, lg *slog.Logger) (int, error) {
	now := time.Now()
	today := now.In(hw.LocalZone(db)).Format("2006-01-02")
	if setting(db, insightDayKey) == today {
		return 0, nil
	}
	if onGPU, err := oc.OnGPU(); err != nil || !onGPU {
		return 0, nil
	}
	owner := setting(db, ownerKey)
	facts := insightFacts(db, mount, owner, now)
	if len(facts) < 3 {
		return 0, setSetting(db, insightDayKey, today) // too little to notice anything in, tried tomorrow
	}
	resp, err := oc.Infer(oracle.Request{
		Capability: "summarize", Class: oracle.ClassLocalSmall, Priority: oracle.PriorityBackground,
		Input: insightPrompt(owner, facts), MaxTokens: 500, Temperature: 0.6, DeadlineMS: 120000,
	})
	if err != nil || resp.Err != "" {
		return 0, nil // tried again next pass
	}
	kept := 0
	ms := now.UnixMilli()
	for i, tb := range parseInsights(resp.Output, owner, facts) {
		// the same title within a month is the same thing noticed again
		// and one the person deleted is not noticed again at all, however long ago
		rows, err := db.Query("SELECT 1 FROM memories WHERE kind = 'insight' AND lower(title) = lower($1) AND (created_at > $2 OR tombstoned) LIMIT 1",
			tb[0], now.AddDate(0, 0, -30).UnixMilli())
		if err != nil {
			return kept, err
		}
		if len(rows.Vals) > 0 {
			continue
		}
		if err := db.Exec("INSERT INTO memories (title, body, kind, source_ref, created_at, updated_at) VALUES ($1,$2,'insight',$3,$4,$4)",
			tb[0], tb[1], "insight:"+today+":"+strconv.Itoa(i), ms); err != nil {
			return kept, err
		}
		kept++
	}
	if kept > 0 {
		lg.Info("the box noticed something", "fn", "insightPass", "memories", kept)
	}
	return kept, setSetting(db, insightDayKey, today)
}
