package main

// THE CALENDAR, FROM THE BOX. The phone reads its own calendar store and posts a window of it to
// the box (hw/calendarstore.go); a question about the person's plans is answered from that table,
// never from a calendar service and never from the web. The window the question names (today,
// tomorrow, a weekday, this or next week, the weekend) is read in the zone the phone's calendar
// was read in; a question naming a thing instead ("when is the dentist") is a search, nearest to
// now first; neither is the next seven days.

import (
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

var (
	// the words that make a question a calendar one
	calendarHint = regexp.MustCompile(`(?i)\b(calendar|agenda|diary|schedule|scheduled|meetings?|appointments?|(?:my|any) (?:events?|bookings?)|booked|` +
		`what(?:'s| is| have i got| do i have| am i doing)\s+(?:on|planned|happening|next|up)|am i (?:free|busy|doing)|` +
		`(?:free|busy) (?:on|this|next|tomorrow|today|tonight)|my plans?|plans? (?:for|on) |when(?:'s| is| was) my)\b`)
	calendarWeekday = regexp.MustCompile(`(?i)\b(monday|tuesday|wednesday|thursday|friday|saturday|sunday)\b`)
	calendarWord    = regexp.MustCompile(`[\p{L}\p{N}][\p{L}\p{N}'-]*`)
)

// calendarStop is what a question says around the thing it asks about.
var calendarStop = map[string]bool{}

func init() {
	for _, w := range strings.Fields(`a an the and or but of in on at to for from with by about my me i i'm im am is are was were be been do does did have has had got
		what what's whats when when's whens where who whom which how why is it its it's this that these those next last coming upcoming
		today tonight tomorrow yesterday week weeks weekend month months year day days morning afternoon evening night time
		monday tuesday wednesday thursday friday saturday sunday calendar agenda diary schedule scheduled meeting meetings appointment
		appointments event events booked booking bookings plan plans planned free busy doing happening up any anything something
		there there's again soon later early late can could would should will shall please tell show list find check see know`) {
		calendarStop[w] = true
	}
}

// calendarQuestion says whether the question is about the person's plans.
func calendarQuestion(prompt string) bool {
	return calendarHint.MatchString(prompt) && !weatherQuestion(prompt)
}

// calendarWindow is the window the question names, in loc, with its words ("tomorrow, Fri 17
// Oct"); named false when it names none (pure, for the tests).
func calendarWindow(prompt string, now time.Time, loc *time.Location) (from, to time.Time, words string, named bool) {
	q := strings.ToLower(prompt)
	n := now.In(loc)
	day := time.Date(n.Year(), n.Month(), n.Day(), 0, 0, 0, 0, loc)
	add := func(t time.Time, d int) time.Time { return t.AddDate(0, 0, d) }
	monday := add(day, -((int(day.Weekday()) + 6) % 7))
	one := func(d time.Time, w string) (time.Time, time.Time, string, bool) {
		return d, add(d, 1), w + ", " + d.Format("Mon 2 Jan"), true
	}
	has := func(s string) bool { return strings.Contains(q, s) }
	switch {
	case has("yesterday"):
		return one(add(day, -1), "yesterday")
	case has("tomorrow"):
		return one(add(day, 1), "tomorrow")
	case has("today") || has("tonight") || has("this morning") || has("this afternoon") || has("this evening"):
		return one(day, "today")
	case has("next weekend"):
		sat := add(monday, 12)
		return sat, add(sat, 2), "next weekend, " + sat.Format("Mon 2 Jan") + " and " + add(sat, 1).Format("Mon 2 Jan"), true
	case has("weekend"):
		sat := add(monday, 5)
		if day.After(add(sat, 1)) {
			sat = add(sat, 7)
		}
		return sat, add(sat, 2), "the weekend, " + sat.Format("Mon 2 Jan") + " and " + add(sat, 1).Format("Mon 2 Jan"), true
	case has("next week"):
		return add(monday, 7), add(monday, 14), "next week, from " + add(monday, 7).Format("Mon 2 Jan"), true
	case has("last week"):
		return add(monday, -7), monday, "last week, from " + add(monday, -7).Format("Mon 2 Jan"), true
	case has("this week"):
		return day, add(monday, 7), "the rest of this week, to " + add(monday, 6).Format("Mon 2 Jan"), true
	case has("next month"):
		first := time.Date(n.Year(), n.Month()+1, 1, 0, 0, 0, 0, loc)
		return first, first.AddDate(0, 1, 0), "next month, " + first.Format("January"), true
	case has("this month"):
		first := time.Date(n.Year(), n.Month(), 1, 0, 0, 0, 0, loc)
		return day, first.AddDate(0, 1, 0), "the rest of " + first.Format("January"), true
	}
	if m := calendarWeekday.FindStringSubmatch(q); m != nil {
		want := map[string]time.Weekday{"sunday": 0, "monday": 1, "tuesday": 2, "wednesday": 3, "thursday": 4, "friday": 5, "saturday": 6}[m[1]]
		ahead := (int(want) - int(day.Weekday()) + 7) % 7
		if strings.Contains(q, "last "+m[1]) || strings.Contains(q, "on "+m[1]+" just gone") {
			back := (int(day.Weekday()) - int(want) + 7) % 7
			if back == 0 {
				back = 7
			}
			return one(add(day, -back), "last "+m[1])
		}
		if strings.Contains(q, "next "+m[1]) && ahead == 0 {
			ahead = 7
		}
		return one(add(day, ahead), m[1])
	}
	return day, add(day, 7), "the next seven days, from today", false
}

// calendarTerms is what the question asks about once the words around it are gone ("when is my
// dentist appointment" , "dentist").
func calendarTerms(prompt string) []string {
	var out []string
	for _, w := range calendarWord.FindAllString(strings.ToLower(prompt), -1) {
		w = strings.Trim(w, "'-")
		if len([]rune(w)) < 3 || calendarStop[w] {
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

// calendarFound is what a question about the calendar found: the events, the window in words,
// whether it was a search (pure past the reads, for the tests).
func calendarFound(db hw.Querier, prompt string, now time.Time) (evs []hw.CalendarEvent, words string, loc *time.Location, err error) {
	loc = hw.CalendarZone(db)
	from, to, words, named := calendarWindow(prompt, now, loc)
	if terms := calendarTerms(prompt); len(terms) > 0 && !named {
		a, b := now.AddDate(-1, 0, 0).Unix(), now.AddDate(1, 0, 0).Unix()
		evs, err = hw.CalendarSearch(db, terms, a, b, 200)
		if err == nil && len(evs) == 0 && len(terms) > 1 {
			longest := terms[0]
			for _, t := range terms[1:] {
				if len(t) > len(longest) {
					longest = t
				}
			}
			evs, err = hw.CalendarSearch(db, []string{longest}, a, b, 200)
		}
		if err != nil {
			return nil, "", loc, err
		}
		// nearest to now first, the next one ahead of the last one behind, then in order
		sort.SliceStable(evs, func(i, j int) bool {
			di, dj := evs[i].Begin-now.Unix(), evs[j].Begin-now.Unix()
			if (di >= 0) != (dj >= 0) {
				return di >= 0
			}
			if di < 0 {
				di = -di
			}
			if dj < 0 {
				dj = -dj
			}
			return di < dj
		})
		if len(evs) > 8 {
			evs = evs[:8]
		}
		sort.SliceStable(evs, func(i, j int) bool { return evs[i].Begin < evs[j].Begin })
		return evs, "the events matching " + strings.Join(terms, ", ") + " within a year either side of today", loc, nil
	}
	evs, err = hw.CalendarRange(db, from.Unix(), to.Unix(), 200)
	return evs, words, loc, err
}

// calendarItems is the calendar a question about the person's plans gets, one item an event
// after one that says which window it is; nothing found is said too, so the model does not guess.
func calendarItems(db *poltergres.ReadWrite, prompt string, now time.Time) []ctxItem {
	if db == nil || !calendarQuestion(prompt) {
		return nil
	}
	meta, ok := hw.CalendarMetaOf(db)
	if !ok {
		return []ctxItem{{Source: "calendar", Snippet: "the phone has not sent its calendar to the box (SETTINGS › CALENDAR on the phone turns it on); say so rather than guess", Why: "a calendar question with no calendar on the box"}}
	}
	evs, words, loc, err := calendarFound(db, prompt, now)
	if err != nil {
		return nil
	}
	synced := "the phone's calendar, synced " + time.Unix(meta.At, 0).In(loc).Format("Mon 2 Jan 15:04")
	live := 0
	for _, e := range evs {
		if e.Live() {
			live++
		}
	}
	head := ctxItem{When: now.In(loc).Format("2006-01-02"), Source: "calendar", Why: synced}
	switch {
	case len(evs) == 0:
		head.Snippet = "the phone's calendar has nothing in " + words + "; say so rather than guess"
		return []ctxItem{head}
	case live == len(evs):
		head.Snippet = "the phone's calendar for " + words + ": " + strconv.Itoa(len(evs)) + " event(s), each below; times are in " + loc.String()
	default:
		head.Snippet = "the phone's calendar for " + words + ": " + strconv.Itoa(len(evs)) + " event(s), " + strconv.Itoa(len(evs)-live) + " of them cancelled or declined; times are in " + loc.String()
	}
	out := []ctxItem{head}
	for i, e := range evs {
		if i == 20 {
			out = append(out, ctxItem{Source: "calendar", Snippet: strconv.Itoa(len(evs)-20) + " more in the window, not listed", Why: synced})
			break
		}
		line := hw.CalendarLine(e, loc, true)
		if e.Calendar != "" {
			line += " [" + e.Calendar + "]"
		}
		if n := strings.TrimSpace(e.Notes); n != "" {
			line += " , notes: " + n
		}
		out = append(out, ctxItem{When: time.Unix(e.Begin, 0).In(loc).Format("2006-01-02"), Source: "calendar", Snippet: line, Why: synced})
	}
	return out
}

// calendarCovers says the box answers a calendar question: always, since nothing on the web
// knows the person's plans and the phone never sends them anywhere for it.
func calendarCovers(prompt string) (string, bool) {
	if !calendarQuestion(prompt) {
		return "", false
	}
	return "the phone's calendar, synced to the box", true
}
