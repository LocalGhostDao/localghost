package main

// daySummaryPass , THE DAYS, PREBUILT. One row per day in day_summaries: a fact sheet gathered from
// everything the box holds about that day (the photos: how many, where, what the tags and the
// captions say; the route: where you stayed, how you moved; the health sync: steps, sleep,
// exercise; the check-in's feeling; the journal's notes; the chats you started; the outing the day
// belongs to), a deterministic TEMPLATE sentence over it, and, when the model is on the GPU, a
// SUMMARY the model writes from the sheet and nothing else (prose.go's groundedProse decides
// whether it stays). Built once the day is over; built again whenever the sheet changes , the
// captions landing over the following days, a late sync, a route told on new streets , so a day
// grows richer without anyone asking; today gets a template in the evening. Backfilled through
// the whole archive, a slice per pass, newest first. "On this day" reads these rows; the memories
// feed carries the days with signal as kind='day'. Nothing here runs at request time.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log/slog"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

const (
	daySummaryVersion  = 1
	dayRecentDays      = 14 // ended days always looked at, for late syncs and captions
	dayBackfillPerPass = 26 // older days examined per pass, walking back to the first photo
	dayModelPerPass    = 4  // model calls per pass
	dayModelMaxTries   = 3  // per signature
	dayModelRewriteGap = 12 * time.Hour
	dayEvening         = 20 // today's template from this hour on
)

var lastDayPass time.Time

// dayFacts is the sheet: everything the summary may say, and nothing it may not.
type dayFacts struct {
	Day       string   `json:"day"`
	Weekday   string   `json:"weekday"`
	Photos    int      `json:"photos"`
	Described int      `json:"described"` // photos with a description so far (the sheet grows with them)
	Covers    []string `json:"covers,omitempty"`
	Places    []string `json:"places,omitempty"`
	Country   string   `json:"country,omitempty"`
	Tags      []string `json:"tags,omitempty"`
	Captions  []string `json:"captions,omitempty"`
	// the route, as framed told it
	Line   string        `json:"line,omitempty"`
	Stays  []dayStayFact `json:"stays,omitempty"`
	Moves  []dayMoveFact `json:"moves,omitempty"`
	WalkM  float64       `json:"walkM,omitempty"`
	RideM  float64       `json:"rideM,omitempty"`
	Fixes  int           `json:"fixes,omitempty"`
	Points int           `json:"points,omitempty"` // raw trail points that day
	// the health sync and the check-in
	Steps       float64 `json:"steps,omitempty"`
	SleepMin    float64 `json:"sleepMin,omitempty"`
	ExerciseMin float64 `json:"exerciseMin,omitempty"`
	Feeling     string  `json:"feeling,omitempty"`
	Why         string  `json:"why,omitempty"`       // the check-in's own words on why
	CheckedIn   bool    `json:"checkedIn,omitempty"` // the evening check-in is in: the day is told from the person's side
	// words of the person's own
	Notes  []string `json:"notes,omitempty"`  // journal titles that day (not the check-in, not a voice note)
	Spoken []string `json:"spoken,omitempty"` // what the person said in the day's voice notes (ghost.voiced's transcripts)
	Chats  []string `json:"chats,omitempty"`  // chats started that day, by title
	// the outing this day is part of
	Outing     string `json:"outing,omitempty"`
	OutingDay  int    `json:"outingDay,omitempty"`
	OutingDays int    `json:"outingDays,omitempty"`
	OutingAway bool   `json:"outingAway,omitempty"`

	// the ground under the trail (framed, from the elevation tiles): climbed and the highest point
	ClimbM float64 `json:"climbM,omitempty"`
	HighM  float64 `json:"highM,omitempty"`
}

type dayStayFact struct {
	Name   string `json:"name,omitempty"`
	Kind   string `json:"kind,omitempty"`
	From   int64  `json:"from"`
	To     int64  `json:"to"`
	Photos int    `json:"photos,omitempty"`
}

type dayMoveFact struct {
	Mode   string  `json:"mode"`
	From   int64   `json:"from"`
	To     int64   `json:"to"`
	Meters float64 `json:"meters"`
}

// empty: the box knows nothing about the day.
func (f *dayFacts) empty() bool {
	return f.Photos == 0 && f.Points == 0 && f.Steps == 0 && f.SleepMin == 0 && f.Feeling == "" && len(f.Notes) == 0 && len(f.Chats) == 0 && len(f.Spoken) == 0
}

// hasSignal: a day worth the model's time and a place in the memories feed.
func (f *dayFacts) hasSignal() bool {
	return len(f.Stays) >= 2 || f.WalkM >= 3000 || f.Photos >= 5 || f.Feeling != "" || len(f.Notes) > 0 || len(f.Spoken) > 0 || f.Outing != ""
}

// signature changes when anything the summary could say changes.
func (f *dayFacts) signature() string {
	b, _ := json.Marshal(f)
	h := sha256.Sum256(b)
	return hex.EncodeToString(h[:8]) + fmt.Sprintf(":%d", daySummaryVersion)
}

// daySummaryPass returns how many rows it built and how many the model wrote. only, when set,
// is the one day to build (the check-in just landed: today, now), and the pass's pacing does
// not apply.
func daySummaryPass(db *poltergres.ReadWrite, oc *oracle.Client, mount string, lg *slog.Logger, only ...string) (built, written int, err error) {
	if len(only) == 0 {
		if time.Since(lastDayPass) < 10*time.Minute {
			return 0, 0, nil
		}
		lastDayPass = time.Now()
	}
	// the person's day, not UTC's: the zone follows the trail (settings local_tz, ghost.framed)
	now := time.Now().In(hw.LocalZone(db))
	today := now.Format("2006-01-02")
	onGPU, gerr := oc.OnGPU()
	modelOK := gerr == nil && onGPU

	// the candidates: today in the evening (or once the check-in is in, whatever the hour: the
	// day is over from the person's side and they are waiting to read it), the last two weeks,
	// and a slice of the past
	var days []string
	if len(only) > 0 {
		days = only
	} else {
		if now.Hour() >= dayEvening || checkedIn(db, today) {
			days = append(days, today)
		}
		for i := 1; i <= dayRecentDays; i++ {
			days = append(days, now.AddDate(0, 0, -i).Format("2006-01-02"))
		}
		days = append(days, dayBackfillSlice(db, now, dayRecentDays+1, dayBackfillPerPass)...)
	}

	modelCalls := 0
	for _, day := range days {
		f := gatherDayFacts(db, mount, day)
		if f.empty() {
			continue
		}
		sig := f.signature()
		var have struct {
			ok        bool
			signature string
			summary   string
			writtenBy string
			modelAt   int64
			tries     int
			triesSig  string
		}
		if rows, qerr := db.Query("SELECT signature, summary, written_by, model_at, prose_tries, tries_sig FROM day_summaries WHERE day = $1", day); qerr != nil {
			return built, written, qerr
		} else if len(rows.Vals) == 1 && len(rows.Vals[0]) >= 6 {
			v := rows.Vals[0]
			have.ok = true
			have.signature = str(v[0])
			have.summary = str(v[1])
			have.writtenBy = str(v[2])
			have.modelAt, _ = strconv.ParseInt(str(v[3]), 10, 64)
			have.tries, _ = strconv.Atoi(str(v[4]))
			have.triesSig = str(v[5])
		}
		tries := 0
		if have.triesSig == sig {
			tries = have.tries
		}
		// the model writes: a day with signal, over, on the GPU, a few per pass, and either no
		// model text yet or the sheet changed and the last text is old enough (captions land one
		// by one; not a rewrite per caption)
		// today is written once the check-in is in (the day, told from the person's side); a
		// day asked for by name is written now
		over := day != today || f.CheckedIn || len(only) > 0
		wantModel := modelOK && over && f.hasSignal() && modelCalls < dayModelPerPass && tries < dayModelMaxTries &&
			(have.writtenBy != "model" || have.summary == "" || (have.signature != sig && time.Since(time.UnixMilli(have.modelAt)) > dayModelRewriteGap) || len(only) > 0)
		if have.ok && have.signature == sig && have.summary != "" && !wantModel {
			continue // nothing new to say, and nothing better to say it with
		}
		template := dayTemplate(f)
		title := dayTitle(f)
		summary, by := template, "template"
		if have.ok && have.writtenBy == "model" && have.summary != "" {
			summary, by = have.summary, "model" // keep the model's text until it writes again
		}
		modelAt := have.modelAt
		if wantModel {
			modelCalls++
			if prose, ok := writeMemory(oc, f.sheet(), "a day"); ok {
				summary, by = prose, "model"
				modelAt = time.Now().UnixMilli()
				written++
				lg.Info("day summary written by the model", "fn", "daySummaryPass", "day", day, "title", title, "chars", len(prose))
			} else {
				tries++
				lg.Info("day summary not kept", "fn", "daySummaryPass", "day", day, "tries", tries)
			}
		}
		fb, _ := json.Marshal(f)
		if err := db.Exec(`INSERT INTO day_summaries (day, built_at, version, signature, title, template, summary, written_by, model_at, prose_tries, tries_sig, facts)
			VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11,$12::jsonb)
			ON CONFLICT (day) DO UPDATE SET built_at = EXCLUDED.built_at, version = EXCLUDED.version, signature = EXCLUDED.signature, title = EXCLUDED.title,
			  template = EXCLUDED.template, summary = EXCLUDED.summary, written_by = EXCLUDED.written_by, model_at = EXCLUDED.model_at,
			  prose_tries = EXCLUDED.prose_tries, tries_sig = EXCLUDED.tries_sig, facts = EXCLUDED.facts`,
			day, time.Now().UnixMilli(), daySummaryVersion, sig, title, template, summary, by, modelAt, tries, sig, string(fb)); err != nil {
			return built, written, err
		}
		built++
		// the memories feed gets the days with signal that no outing already tells (an outing's
		// days are its own memory; a day inside one would say the same thing twice)
		if (day != today || f.CheckedIn) && f.hasSignal() && f.Outing == "" {
			if err := projectDayMemory(db, day, title, summary, by, f); err != nil {
				lg.Warn("day memory not projected", "fn", "daySummaryPass", "day", day, "err", err)
			}
		}
	}
	return built, written, nil
}

// dayBackfillSlice walks the past in slices: from the watermark (settings) back n days, stopping
// at the first photo's day; when the past is done it starts over from the recent edge, so a
// slow-growing archive is revisited about once a season.
func dayBackfillSlice(db *poltergres.ReadWrite, now time.Time, skip, n int) []string {
	earliest := ""
	if rows, err := db.Query("SELECT min(taken_at) FROM frames WHERE kind = 'photo' AND taken_at > 0"); err == nil && len(rows.Vals) == 1 && len(rows.Vals[0]) > 0 && rows.Vals[0][0] != nil {
		if ts, perr := strconv.ParseInt(*rows.Vals[0][0], 10, 64); perr == nil && ts > 0 {
			earliest = time.Unix(ts, 0).UTC().Format("2006-01-02")
		}
	}
	if rows, err := db.Query("SELECT to_char(to_timestamp(min(ts)), 'YYYY-MM-DD') FROM location_points"); err == nil && len(rows.Vals) == 1 && len(rows.Vals[0]) > 0 && rows.Vals[0][0] != nil {
		if d := *rows.Vals[0][0]; d != "" && (earliest == "" || d < earliest) {
			earliest = d
		}
	}
	if earliest == "" {
		return nil
	}
	edge := now.AddDate(0, 0, -skip)
	wm := edge
	if rows, err := db.Query("SELECT value FROM settings WHERE key = 'synthd_days_watermark'"); err == nil && len(rows.Vals) == 1 && len(rows.Vals[0]) > 0 && rows.Vals[0][0] != nil {
		if t, perr := time.Parse("2006-01-02", *rows.Vals[0][0]); perr == nil && t.Before(edge) {
			wm = t
		}
	}
	var out []string
	d := wm
	for i := 0; i < n; i++ {
		ds := d.Format("2006-01-02")
		if ds < earliest {
			d = edge // the past is done: start over from the recent edge next pass
			break
		}
		out = append(out, ds)
		d = d.AddDate(0, 0, -1)
	}
	_ = db.Exec("INSERT INTO settings (key, value) VALUES ('synthd_days_watermark',$1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", d.Format("2006-01-02"))
	return out
}

// gatherDayFacts reads everything the box holds about one day.
func gatherDayFacts(db *poltergres.ReadWrite, mount, day string) *dayFacts {
	f := &dayFacts{Day: day}
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return f
	}
	f.Weekday = t.Weekday().String()
	s0, s1 := t.Unix(), t.Unix()+86400
	// the photos: hashes in time order, places, how many are described
	var hashes []string
	if rows, err := db.Query(`SELECT hash, coalesce(place,''), coalesce(description,'') <> '' FROM frames
		WHERE kind = 'photo' AND taken_at >= $1 AND taken_at < $2 ORDER BY taken_at`, s0, s1); err == nil {
		places := map[string]int{}
		countries := map[string]int{}
		for _, v := range rows.Vals {
			if len(v) < 3 || v[0] == nil {
				continue
			}
			hashes = append(hashes, *v[0])
			if v[2] != nil && *v[2] == "t" {
				f.Described++
			}
			if v[1] != nil && *v[1] != "" {
				parts := strings.Split(*v[1], " / ")
				last := strings.TrimSpace(parts[len(parts)-1])
				if i := strings.LastIndex(last, ","); i >= 0 {
					last = strings.TrimSpace(last[i+1:])
				}
				if last != "" {
					places[last]++
				}
				if len(parts) > 1 {
					countries[strings.TrimSpace(parts[1])]++
				}
			}
		}
		f.Photos = len(hashes)
		f.Places = topKeys(places, 4)
		if c := topKeys(countries, 1); len(c) == 1 {
			f.Country = c[0]
		}
	}
	if n := len(hashes); n > 0 {
		k := 6
		if n < k {
			k = n
		}
		for i := 0; i < k; i++ {
			f.Covers = append(f.Covers, hashes[i*n/k])
		}
		if rows, err := db.Query(`SELECT tag, count(*) FROM frame_tags WHERE source <> 'user_removed' AND category NOT IN ('text','style')
			AND hash = ANY($1) GROUP BY tag ORDER BY count(*) DESC LIMIT 10`, "{"+strings.Join(hashes, ",")+"}"); err == nil {
			for _, v := range rows.Vals {
				if len(v) > 0 && v[0] != nil {
					f.Tags = append(f.Tags, *v[0])
				}
			}
		}
		f.Captions = captionsFor(db, f.Covers, 5)
	}
	// the route, as framed told it
	if r := routeOf(mount, day); r != nil {
		f.Line = r.Line
		f.WalkM, f.RideM, f.Fixes = r.WalkM, r.RideM, r.Fixes
		for _, st := range r.Stays {
			f.Stays = append(f.Stays, dayStayFact{Name: st.Name, Kind: st.Kind, From: st.From, To: st.To, Photos: st.Photos})
		}
		for _, m := range r.Moves {
			f.Moves = append(f.Moves, dayMoveFact{Mode: m.Mode, From: m.From, To: m.To, Meters: m.Meters})
		}
	}
	f.ClimbM, f.HighM = pathHeights(mount, day)
	if rows, err := db.Query("SELECT count(*) FROM location_points WHERE ts >= $1 AND ts < $2", s0, s1); err == nil && len(rows.Vals) == 1 && len(rows.Vals[0]) > 0 && rows.Vals[0][0] != nil {
		f.Points, _ = strconv.Atoi(*rows.Vals[0][0])
	}
	// the health sync
	if rows, err := db.Query("SELECT metric, value FROM health_metrics WHERE day = $1", day); err == nil {
		for _, v := range rows.Vals {
			if len(v) < 2 || v[0] == nil || v[1] == nil {
				continue
			}
			x, _ := strconv.ParseFloat(*v[1], 64)
			switch *v[0] {
			case "steps":
				f.Steps = x
			case "sleep_minutes":
				f.SleepMin = x
			case "exercise_minutes":
				f.ExerciseMin = x
			}
		}
	}
	// the check-in and the notes
	if rows, err := db.Query("SELECT source, title, body FROM journal_entries WHERE ts >= $1 AND ts < $2 ORDER BY ts", s0, s1); err == nil {
		for _, v := range rows.Vals {
			if len(v) < 3 || v[1] == nil {
				continue
			}
			title := strings.TrimSpace(*v[1])
			if strings.HasPrefix(title, "Daily check-in ") {
				f.CheckedIn = true
				if v[2] != nil {
					for _, line := range strings.Split(*v[2], "\n") {
						line = strings.TrimSpace(line)
						switch {
						case strings.HasPrefix(line, "Feeling: "):
							if fe := strings.TrimSpace(strings.TrimPrefix(line, "Feeling: ")); fe != "" && fe != "(unspecified)" {
								f.Feeling = fe
							}
						case strings.HasPrefix(line, "Why: "):
							// the person's own words on why: the part the summary was dropping
							if w := strings.TrimSpace(strings.TrimPrefix(line, "Why: ")); w != "" && w != "(unspecified)" {
								f.Why = clip(w, 600)
							}
						}
					}
				}
				continue
			}
			// a voice note: the words themselves, not the title (the body's first line says when
			// and how long; the rest is the transcript)
			if v[0] != nil && *v[0] == "ghost.voiced" {
				if v[2] != nil && len(f.Spoken) < 4 {
					body := *v[2]
					if i := strings.Index(body, "\n"); i >= 0 {
						body = body[i+1:]
					}
					if body = strings.TrimSpace(body); body != "" {
						f.Spoken = append(f.Spoken, clip(body, 1500))
					}
				}
				continue
			}
			// the person's own notes: not framed's line per photo, tallyd's health line or a
			// chat's title (the box talking to itself; "You wrote: photo at Lakka" was the result)
			if v[0] != nil && (*v[0] == "ghost.framed" || *v[0] == "ghost.tallyd") {
				continue
			}
			if strings.HasPrefix(title, "conversation:") {
				continue
			}
			if title != "" && len(f.Notes) < 6 {
				f.Notes = append(f.Notes, clip(title, 120))
			}
		}
	}
	// the chats started that day, by title
	if rows, err := db.Query("SELECT title FROM chats WHERE created_at >= $1 AND created_at < $2 AND title <> '' ORDER BY created_at LIMIT 6", s0*1000, s1*1000); err == nil {
		for _, v := range rows.Vals {
			if len(v) > 0 && v[0] != nil {
				f.Chats = append(f.Chats, clip(*v[0], 80))
			}
		}
	}
	// the outing this day belongs to
	if rows, err := db.Query(`SELECT title, (meta->>'start')::bigint, (meta->>'end')::bigint, coalesce((meta->>'away')::boolean, false)
		FROM memories WHERE kind = 'outing' AND NOT tombstoned AND (meta->>'start')::bigint < $1 AND (meta->>'end')::bigint >= $2 ORDER BY (meta->>'start')::bigint LIMIT 1`, s1, s0); err == nil && len(rows.Vals) == 1 {
		v := rows.Vals[0]
		if len(v) >= 4 && v[0] != nil && v[1] != nil && v[2] != nil {
			start, _ := strconv.ParseInt(*v[1], 10, 64)
			end, _ := strconv.ParseInt(*v[2], 10, 64)
			f.Outing = *v[0]
			startDay := time.Unix(start, 0).UTC().Truncate(24 * time.Hour)
			endDay := time.Unix(end, 0).UTC().Truncate(24 * time.Hour)
			f.OutingDays = int(endDay.Sub(startDay).Hours()/24) + 1
			f.OutingDay = int(t.Sub(startDay).Hours()/24) + 1
			f.OutingAway = v[3] != nil && *v[3] == "t"
		}
	}
	return f
}

func topKeys(m map[string]int, n int) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool {
		if m[keys[i]] != m[keys[j]] {
			return m[keys[i]] > m[keys[j]]
		}
		return keys[i] < keys[j]
	})
	if len(keys) > n {
		keys = keys[:n]
	}
	return keys
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// --- what the day says ---

// dayTitle: "Friday 25 September 2026 · Corner Café, Voutoumi", the places from the stays, else
// from the photos, else the outing.
func dayTitle(f *dayFacts) string {
	t, err := time.Parse("2006-01-02", f.Day)
	if err != nil {
		return f.Day
	}
	s := t.Format("Monday 2 January 2006")
	var names []string
	for _, st := range f.Stays {
		if st.Name != "" && st.Kind != "near" {
			names = append(names, st.Name)
		}
	}
	if len(names) == 0 {
		names = append(names, f.Places...)
	}
	names = uniqueStrings(names)
	if len(names) > 3 {
		names = names[:3]
	}
	switch {
	case len(names) > 0:
		s += " · " + strings.Join(names, ", ")
	case f.Outing != "":
		s += " · " + f.Outing
	}
	return s
}

// dayTemplate is the deterministic summary: every fact in a sentence, in a fixed order, so a day
// reads the same way whether or not the model ever touched it.
func dayTemplate(f *dayFacts) string {
	var parts []string
	if f.Outing != "" {
		if f.OutingDays > 1 {
			parts = append(parts, fmt.Sprintf("Day %d of %d of %s.", f.OutingDay, f.OutingDays, f.Outing))
		} else {
			parts = append(parts, f.Outing+".")
		}
	}
	if len(f.Stays) > 0 || f.WalkM >= 500 || f.RideM >= 1000 {
		var s string
		if f.Line != "" {
			s = f.Line
		} else {
			var bits []string
			if f.WalkM >= 500 {
				bits = append(bits, kmText(f.WalkM)+" on foot")
			}
			if f.RideM >= 1000 {
				bits = append(bits, kmText(f.RideM)+" by road")
			}
			s = strings.Join(bits, ", ")
		}
		if s != "" {
			parts = append(parts, strings.ToUpper(s[:1])+s[1:]+".")
		}
	}
	if f.Photos > 0 {
		s := fmt.Sprintf("%d photo%s", f.Photos, plural(f.Photos))
		if len(f.Places) > 0 {
			s += " around " + joinAnd(f.Places)
		}
		if len(f.Tags) > 0 {
			n := len(f.Tags)
			if n > 5 {
				n = 5
			}
			s += ", mostly " + joinAnd(f.Tags[:n])
		}
		parts = append(parts, s+".")
	}
	var health []string
	if f.Steps > 0 {
		health = append(health, fmt.Sprintf("%s steps", thousands(int(f.Steps))))
	}
	if f.ExerciseMin >= 10 {
		health = append(health, fmt.Sprintf("%d min of exercise", int(f.ExerciseMin)))
	}
	if f.SleepMin > 0 {
		health = append(health, fmt.Sprintf("%dh %02dm of sleep", int(f.SleepMin)/60, int(f.SleepMin)%60))
	}
	if len(health) > 0 {
		parts = append(parts, strings.ToUpper(health[0][:1])+strings.Join(health, ", ")[1:]+".")
	}
	if f.Feeling != "" {
		line := "You said you felt " + strings.TrimRight(f.Feeling, ".")
		if f.Why != "" {
			line += ": \u201c" + clip(f.Why, 200) + "\u201d"
		}
		parts = append(parts, line+".")
	} else if f.Why != "" {
		parts = append(parts, "At the check-in you wrote: \u201c"+clip(f.Why, 200)+"\u201d")
	}
	if len(f.Notes) > 0 {
		parts = append(parts, "You wrote: "+joinAnd(quoteAll(f.Notes))+".")
	}
	if len(f.Spoken) > 0 {
		parts = append(parts, "You said: \u201c"+clip(f.Spoken[0], 280)+"\u201d")
	}
	if len(f.Chats) > 0 {
		parts = append(parts, fmt.Sprintf("You asked the box about %s.", joinAnd(f.Chats)))
	}
	return strings.Join(parts, " ")
}

// sheet is the day as the model gets it: the facts as lines, and only the facts.
func (f *dayFacts) sheet() []string {
	var s []string
	t, _ := time.Parse("2006-01-02", f.Day)
	s = append(s, "Day: "+t.Format("Monday 2 January 2006"))
	if f.Outing != "" {
		if f.OutingDays > 1 {
			s = append(s, fmt.Sprintf("Part of a longer outing: day %d of %d of \"%s\"", f.OutingDay, f.OutingDays, f.Outing))
		} else {
			s = append(s, "Part of the outing \""+f.Outing+"\"")
		}
		if f.OutingAway {
			s = append(s, "Away from home")
		}
	}
	if f.Line != "" {
		s = append(s, "The day in one line: "+f.Line)
	}
	for _, st := range f.Stays {
		where := st.Name
		switch {
		case where == "":
			where = "a place with no name on the map"
		case st.Kind == "near":
			where = "near " + st.Name
		case st.Kind != "" && st.Kind != "spot":
			where = st.Name + " (" + st.Kind + ")"
		}
		s = append(s, fmt.Sprintf("Stayed %s to %s: %s%s", clockOf(st.From), clockOf(st.To), where, photosNote(st.Photos)))
	}
	for _, m := range f.Moves {
		how := "walked"
		if m.Mode != "walk" {
			how = "travelled by road or water"
		}
		s = append(s, fmt.Sprintf("%s to %s: %s about %s", clockOf(m.From), clockOf(m.To), how, kmText(m.Meters)))
	}
	if f.WalkM >= 500 {
		s = append(s, "On foot in all: about "+kmText(f.WalkM))
	}
	if f.RideM >= 1000 {
		s = append(s, "By road or water in all: about "+kmText(f.RideM))
	}
	if f.ClimbM >= 50 {
		s = append(s, fmt.Sprintf("Climbed about %d m in all; the highest point %d m above the sea", int(f.ClimbM), int(f.HighM)))
	}
	if f.Photos > 0 {
		s = append(s, fmt.Sprintf("Photos taken: %d", f.Photos))
	}
	if len(f.Places) > 0 {
		s = append(s, "Photographed at: "+strings.Join(f.Places, ", "))
	}
	if f.Country != "" {
		s = append(s, "Country: "+f.Country)
	}
	if len(f.Tags) > 0 {
		s = append(s, "Seen in the photos (most often first): "+strings.Join(f.Tags, ", "))
	}
	for _, c := range f.Captions {
		s = append(s, "A photo shows: "+c)
	}
	if f.Steps > 0 {
		s = append(s, fmt.Sprintf("Steps counted by the phone: %d", int(f.Steps)))
	}
	if f.ExerciseMin >= 10 {
		s = append(s, fmt.Sprintf("Exercise recorded: %d minutes", int(f.ExerciseMin)))
	}
	if f.SleepMin > 0 {
		s = append(s, fmt.Sprintf("Sleep recorded: %d hours %d minutes", int(f.SleepMin)/60, int(f.SleepMin)%60))
	}
	if f.Feeling != "" {
		s = append(s, "In the evening check-in you said you felt: "+f.Feeling)
	}
	if f.Why != "" {
		s = append(s, "In the evening check-in you wrote why, in your own words: "+f.Why)
	}
	for _, n := range f.Notes {
		s = append(s, "You wrote a note titled: "+n)
	}
	for _, sp := range f.Spoken {
		s = append(s, "In a voice note you said (transcribed by the box, may mishear words): "+sp)
	}
	for _, c := range f.Chats {
		s = append(s, "You started a chat with the box about: "+c)
	}
	return s
}

// projectDayMemory keeps the memories feed's copy of a day with signal (kind='day'). The person's
// edits and tombstones outrank it, and an unchanged body is left alone.
func projectDayMemory(db *poltergres.ReadWrite, day, title, summary, by string, f *dayFacts) error {
	ref := "day:" + day
	meta := map[string]any{"line": f.Line, "stays": len(f.Stays), "moves": len(f.Moves), "walkM": f.WalkM, "rideM": f.RideM,
		"photos": f.Photos, "fixes": f.Fixes, "covers": f.Covers, "by": by, "outing": f.Outing}
	mb, _ := json.Marshal(meta)
	ex, err := db.Query("SELECT id, user_edited, tombstoned, body FROM memories WHERE kind = 'day' AND source_ref = $1", ref)
	if err != nil {
		return err
	}
	now := time.Now().UnixMilli()
	if len(ex.Vals) > 0 {
		v := ex.Vals[0]
		if (len(v) > 1 && str(v[1]) == "t") || (len(v) > 2 && str(v[2]) == "t") {
			return nil
		}
		if len(v) > 3 && str(v[3]) == summary {
			return db.Exec("UPDATE memories SET title = $1, meta = $2::jsonb WHERE id = $3", title, string(mb), str(v[0]))
		}
		return db.Exec("UPDATE memories SET title = $1, body = $2, meta = $3::jsonb, updated_at = $4 WHERE id = $5", title, summary, string(mb), now, str(v[0]))
	}
	t, _ := time.Parse("2006-01-02", day)
	return db.Exec("INSERT INTO memories (title, body, kind, source_ref, meta, created_at, updated_at) VALUES ($1,$2,'day',$3,$4::jsonb,$5,$6)",
		title, summary, ref, string(mb), t.Add(24*time.Hour-time.Second).UnixMilli(), now)
}

// --- small words ---

func plural(n int) string {
	if n == 1 {
		return ""
	}
	return "s"
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func quoteAll(xs []string) []string {
	out := make([]string, len(xs))
	for i, x := range xs {
		out[i] = "\"" + x + "\""
	}
	return out
}

func thousands(n int) string {
	s := strconv.Itoa(n)
	if len(s) <= 3 {
		return s
	}
	var b strings.Builder
	pre := len(s) % 3
	if pre > 0 {
		b.WriteString(s[:pre])
	}
	for i := pre; i < len(s); i += 3 {
		if b.Len() > 0 {
			b.WriteByte(',')
		}
		b.WriteString(s[i : i+3])
	}
	return b.String()
}

// checkedIn: the day's check-in is in the journal (the phone's local day is in its title).
func checkedIn(db *poltergres.ReadWrite, day string) bool {
	rows, err := db.Query("SELECT 1 FROM journal_entries WHERE source = 'ghost.noted' AND title = $1 LIMIT 1", "Daily check-in "+day)
	return err == nil && len(rows.Vals) > 0
}
