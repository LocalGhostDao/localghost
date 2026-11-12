package main

// MONTHS , the past at a coarser grain. The post "How memory gets made" (section 4): "the brain
// holds yesterday in detail, last week in summary, last year in chunks", and a system on its own
// disk should copy that shape of retrieval without copying the forgetting: "aggregating the past
// into summaries does not require destroying the past. The originals can stay on disk".
//
// So a month that ended six weeks ago or more becomes one memory (kind 'month', source_ref
// month:<yyyy-mm>), and the days and the outings of that month that are not already part of a
// trip fold under it (meta.part_of), the way a trip's days fold under the trip. MEMORIES shows
// the month where it showed twenty days; the days are one tap down, each as it was, and every
// photo, note and trail point they were told from is where it always was. A trip stays a whole
// of its own, linked to its month (links.go). Recent weeks stay day by day.
//
// The month's words are counted, not written: how many days, the photos, the places most days
// were spent at, the trips, the walking, the notes, the feeling the evening check-ins gave most
// often, all from the days' own fact sheets (day_summaries). No model reads anything here.
//
// A month the person edited keeps their words; one they deleted stays deleted and its days
// stand on their own again. A month left with too few days to be worth a fold is unfolded.

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

const (
	monthFoldAfterD = 45 // days after a month ends before it folds
	monthMinParts   = 3  // fewer days and outings than this are left as they are
	monthCovers     = 6
)

// monthMeta is what a month is counted from, kept beside its words.
type monthMeta struct {
	Month     string   `json:"month"` // "2026-08"
	Days      int      `json:"days"`
	Outings   int      `json:"outings,omitempty"`
	Photos    int      `json:"photos,omitempty"`
	Places    []string `json:"places,omitempty"`
	Countries []string `json:"countries,omitempty"`
	Trips     []string `json:"trips,omitempty"`
	Covers    []string `json:"covers,omitempty"`
	WalkM     float64  `json:"walkM,omitempty"`
	Notes     int      `json:"notes,omitempty"`
	Feeling   string   `json:"feeling,omitempty"`
	FeelingN  int      `json:"feelingN,omitempty"`
	Sig       string   `json:"sig,omitempty"`
	Template  string   `json:"template,omitempty"`
}

// monthOf is the month a day or an outing is filed under, from its ref ("day:2026-08-14",
// "outing:2026-08-14"), "" when the ref carries no date.
func monthOf(ref string) string {
	_, d, ok := strings.Cut(ref, ":")
	if !ok || len(d) < 10 || d[4] != '-' || d[7] != '-' {
		return ""
	}
	if _, err := time.Parse("2006-01-02", d[:10]); err != nil {
		return ""
	}
	return d[:7]
}

// monthIsOld says whether a month ended long enough ago to fold (pure, for the tests).
func monthIsOld(ym string, now time.Time) bool {
	t, err := time.Parse("2006-01", ym)
	if err != nil {
		return false
	}
	end := t.AddDate(0, 1, 0) // the first moment of the next month
	return !now.Before(end.AddDate(0, 0, monthFoldAfterD))
}

// monthTitle is "August 2026".
func monthTitle(ym string) string {
	t, err := time.Parse("2006-01", ym)
	if err != nil {
		return ym
	}
	return t.Format("January 2006")
}

// topCounted is the names counted most, the most first, a tie by name; at most n.
func topCounted(c map[string]int, n int) []string {
	names := make([]string, 0, len(c))
	for k := range c {
		if strings.TrimSpace(k) != "" {
			names = append(names, k)
		}
	}
	sort.Slice(names, func(i, j int) bool {
		if c[names[i]] != c[names[j]] {
			return c[names[i]] > c[names[j]]
		}
		return names[i] < names[j]
	})
	if len(names) > n {
		names = names[:n]
	}
	return names
}

// monthFrom counts a month from its days' fact sheets (pure, for the tests): days is the folded
// days, outings the folded outings, trips the titles of the trips that began in it.
func monthFrom(ym string, sheets []dayFacts, days, outings int, trips []string) monthMeta {
	m := monthMeta{Month: ym, Days: days, Outings: outings, Trips: trips}
	places, countries, feelings := map[string]int{}, map[string]int{}, map[string]int{}
	var covers []string
	for _, f := range sheets {
		m.Photos += f.Photos
		m.WalkM += f.WalkM
		m.Notes += len(f.Notes)
		seen := map[string]bool{}
		for _, p := range f.Places {
			if p = strings.TrimSpace(p); p != "" && !seen[p] {
				seen[p] = true
				places[p]++
			}
		}
		for _, s := range f.Stays {
			if p := strings.TrimSpace(s.Name); p != "" && !seen[p] {
				seen[p] = true
				places[p]++
			}
		}
		if f.Country != "" {
			countries[f.Country]++
		}
		for _, w := range strings.FieldsFunc(strings.ToLower(f.Feeling), func(r rune) bool { return r == ',' || r == ';' || r == '/' }) {
			if w = strings.TrimSpace(w); w != "" {
				feelings[w]++
			}
		}
		if len(f.Covers) > 0 {
			covers = append(covers, f.Covers[0])
		}
	}
	m.Places = topCounted(places, 4)
	m.Countries = topCounted(countries, 3)
	if top := topCounted(feelings, 1); len(top) == 1 && feelings[top[0]] >= 2 {
		m.Feeling, m.FeelingN = top[0], feelings[top[0]]
	}
	// the covers spread over the month, not its first week
	if len(covers) > monthCovers {
		step := float64(len(covers)) / monthCovers
		for i := 0; i < monthCovers; i++ {
			m.Covers = append(m.Covers, covers[int(float64(i)*step)])
		}
	} else {
		m.Covers = covers
	}
	return m
}

// monthBody says a month from its counts (pure, for the tests). Every number is the fact sheets'.
func monthBody(m monthMeta) string {
	var b strings.Builder
	fmt.Fprintf(&b, "%d day%s kept", m.Days, plural(m.Days))
	if m.Outings > 0 {
		fmt.Fprintf(&b, " and %d outing%s", m.Outings, plural(m.Outings))
	}
	if m.Photos > 0 {
		fmt.Fprintf(&b, ", %d photo%s", m.Photos, plural(m.Photos))
	}
	b.WriteString(".")
	if len(m.Places) > 0 {
		b.WriteString(" Mostly at " + joinAnd(m.Places) + ".")
	}
	if len(m.Countries) > 1 {
		b.WriteString(" In " + numberWord(len(m.Countries)) + " countries: " + joinAnd(m.Countries) + ".")
	}
	switch len(m.Trips) {
	case 0:
	case 1:
		b.WriteString(" A trip began: " + m.Trips[0] + ".")
	default:
		b.WriteString(" Trips began: " + strings.Join(m.Trips, "; ") + ".")
	}
	if m.WalkM >= 950 {
		b.WriteString(" About " + kmText(m.WalkM) + " on foot.")
	}
	if m.Notes > 0 {
		fmt.Fprintf(&b, " %d note%s written.", m.Notes, plural(m.Notes))
	}
	if m.Feeling != "" {
		fmt.Fprintf(&b, " The evening check-ins said %s most often, on %d days.", m.Feeling, m.FeelingN)
	}
	return b.String()
}

// monthPart is a day or an outing that could fold under a month.
type monthPart struct {
	id     string
	kind   string
	ref    string
	partOf string
}

// monthPass makes the old months, folds their days and outings under them, and unfolds what no
// longer belongs under one. It returns how many months it wrote.
func monthPass(db *poltergres.ReadWrite, now time.Time, lg *slog.Logger) (int, error) {
	rows, err := db.Query("SELECT id, kind, source_ref, COALESCE(meta->>'part_of','') FROM memories WHERE kind IN ('day','outing') AND NOT tombstoned")
	if err != nil {
		return 0, err
	}
	members := map[string][]monthPart{}
	for _, v := range rows.Vals {
		if len(v) < 4 || v[0] == nil {
			continue
		}
		p := monthPart{id: *v[0], kind: str(v[1]), ref: str(v[2]), partOf: str(v[3])}
		ym := monthOf(p.ref)
		if ym == "" || !monthIsOld(ym, now) {
			continue
		}
		// under a trip or an outing already: that is its whole, the month is only linked
		if p.partOf != "" && p.partOf != "month:"+ym {
			continue
		}
		members[ym] = append(members[ym], p)
	}
	// the months as they stand, and the ones the person deleted
	exist := map[string][]string{} // ym → id, user_edited, tombstoned, template, sig
	mrows, err := db.Query("SELECT source_ref, id, user_edited, tombstoned, COALESCE(meta->>'template',''), COALESCE(meta->>'sig',''), body FROM memories WHERE kind = 'month'")
	if err != nil {
		return 0, err
	}
	for _, v := range mrows.Vals {
		if len(v) < 7 || v[0] == nil {
			continue
		}
		exist[strings.TrimPrefix(*v[0], "month:")] = []string{str(v[1]), str(v[2]), str(v[3]), str(v[4]), str(v[5]), str(v[6])}
	}
	// the trips, by the month they began in
	trips := map[string][]string{}
	trows, err := db.Query("SELECT source_ref, title FROM memories WHERE kind = 'trip' AND NOT tombstoned ORDER BY source_ref")
	if err != nil {
		return 0, err
	}
	for _, v := range trows.Vals {
		if len(v) == 2 && v[0] != nil {
			if ym := monthOf(*v[0]); ym != "" {
				trips[ym] = append(trips[ym], str(v[1]))
			}
		}
	}
	// when each month's fact sheets were last built, so an unchanged month is not counted again
	built := map[string]string{}
	brows, err := db.Query("SELECT substr(day, 1, 7), max(built_at) FROM day_summaries GROUP BY 1")
	if err != nil {
		return 0, err
	}
	for _, v := range brows.Vals {
		if len(v) == 2 && v[0] != nil {
			built[*v[0]] = str(v[1])
		}
	}

	nowMs := now.UnixMilli()
	wrote := 0
	keep := map[string]bool{}
	yms := make([]string, 0, len(members))
	for ym := range members {
		yms = append(yms, ym)
	}
	sort.Strings(yms)
	for _, ym := range yms {
		parts := members[ym]
		ex, has := exist[ym]
		if len(parts) < monthMinParts || (has && ex[2] == "t") {
			continue // too little to fold, or the person deleted this month: its days stand alone
		}
		keep[ym] = true
		days, outs := 0, 0
		var fold []string
		for _, p := range parts {
			if p.kind == "day" {
				days++
			} else {
				outs++
			}
			if p.partOf == "" {
				fold = append(fold, p.id)
			}
		}
		ref := "month:" + ym
		sig := fmt.Sprintf("%d:%d:%s:%s", days, outs, built[ym], strings.Join(trips[ym], "|"))
		if !has || (ex[1] != "t" && ex[4] != sig) {
			var sheets []dayFacts
			srows, serr := db.Query("SELECT facts::text FROM day_summaries WHERE day >= $1 AND day <= $2 AND facts IS NOT NULL ORDER BY day", ym+"-01", ym+"-31")
			if serr != nil {
				return wrote, serr
			}
			for _, v := range srows.Vals {
				var f dayFacts
				if len(v) == 1 && v[0] != nil && json.Unmarshal([]byte(*v[0]), &f) == nil {
					sheets = append(sheets, f)
				}
			}
			m := monthFrom(ym, sheets, days, outs, trips[ym])
			m.Sig = sig
			body := monthBody(m)
			m.Template = body
			mb, _ := json.Marshal(m)
			switch {
			case !has:
				t, _ := time.Parse("2006-01", ym)
				if err := db.Exec("INSERT INTO memories (title, body, kind, source_ref, meta, created_at, updated_at) VALUES ($1,$2,'month',$3,$4::jsonb,$5,$6)",
					monthTitle(ym), body, ref, string(mb), t.AddDate(0, 1, 0).Add(-time.Second).UnixMilli(), nowMs); err != nil {
					return wrote, err
				}
				lg.Info("a month from its days", "fn", "monthPass", "month", ym, "days", days, "outings", outs)
			case ex[3] == body:
				// the same words: only what is beside them is refreshed
				if err := db.Exec("UPDATE memories SET meta = $2::jsonb WHERE id = $1", ex[0], string(mb)); err != nil {
					return wrote, err
				}
			default:
				if err := db.Exec("UPDATE memories SET title = $2, body = $3, meta = $4::jsonb, updated_at = $5 WHERE id = $1", ex[0], monthTitle(ym), body, string(mb), nowMs); err != nil {
					return wrote, err
				}
			}
			wrote++
		}
		if len(fold) > 0 {
			if err := db.Exec("UPDATE memories SET meta = coalesce(meta,'{}'::jsonb) || jsonb_build_object('part_of', $1::text) WHERE id = ANY($2::bigint[])",
				ref, "{"+strings.Join(fold, ",")+"}"); err != nil {
				return wrote, err
			}
		}
	}
	// months that no longer hold enough go (the person's own, edited or anchored, stay), and
	// whatever is folded under a month that is not there stands on its own again
	for ym, ex := range exist {
		if keep[ym] || ex[1] == "t" || ex[2] == "t" {
			continue
		}
		if err := db.Exec("DELETE FROM memories WHERE id = $1 AND NOT user_edited AND anchored_at IS NULL", ex[0]); err != nil {
			return wrote, err
		}
	}
	if err := db.Exec(`UPDATE memories SET meta = meta - 'part_of' WHERE kind IN ('day','outing') AND meta IS NOT NULL AND meta->>'part_of' LIKE 'month:%'
		AND NOT EXISTS (SELECT 1 FROM memories w WHERE w.kind = 'month' AND NOT w.tombstoned AND w.source_ref = memories.meta->>'part_of')`); err != nil {
		return wrote, err
	}
	return wrote, nil
}
