// Package monitor is Box Status's view of the data the box pulls in: the prices every minute,
// the exchanges, the history being walked back, the market index, the ECB, the rank list, the
// daily candles and the news. Each section says how it is doing in one word (ok, filling,
// waiting, flaky, late, failing), one line, and how old its newest piece is; opened, it says the
// rest. It reads the database straight (the fetch log, the tables, ghost.tallyd's progress), so it
// answers while a daemon is down, which is when it matters.
package monitor

import (
	"encoding/json"
	"fmt"
	"math"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/feeds"
	"github.com/LocalGhostDao/localghost/server/internal/feedstat"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"github.com/LocalGhostDao/localghost/server/internal/rates"
	"github.com/LocalGhostDao/localghost/server/internal/tally"
	"github.com/LocalGhostDao/localghost/server/internal/weather"
)

// The states, from best to worst. Filling and waiting are not faults: the history is still
// being walked back, or a thing has not had its first day yet.
const (
	OK      = "ok"
	Filling = "filling"
	Waiting = "waiting"
	Flaky   = "flaky"
	Late    = "late"
	Failing = "failing"
)

var severity = map[string]int{OK: 0, Filling: 1, Waiting: 1, Flaky: 2, Late: 3, Failing: 4}

// Worst is the worst of the states given (ok for none).
func Worst(states ...string) string {
	w := OK
	for _, s := range states {
		if severity[s] > severity[w] {
			w = s
		}
	}
	return w
}

// Row is one line of an opened section.
type Row struct {
	K     string `json:"k"`
	V     string `json:"v"`
	State string `json:"state,omitempty"`
}

// Section is one feed's account.
type Section struct {
	ID    string `json:"id"`
	Title string `json:"title"`
	State string `json:"state"`
	Line  string `json:"line"`
	AgeS  int64  `json:"ageS"` // the newest piece's age, seconds; -1 when there is none
	Every string `json:"every,omitempty"`
	Rows  []Row  `json:"rows,omitempty"`
}

// Report is the whole.
type Report struct {
	At       int64     `json:"at"`
	State    string    `json:"state"`
	Summary  string    `json:"summary"`
	Sections []Section `json:"sections"`
}

// Make reads every section. A section whose reading failed says so and is counted as waiting.
func Make(db *poltergres.ReadWrite, now time.Time) Report {
	r := Report{At: now.Unix()}
	prog, hasProg := tally.LoadProgress(db)
	for _, f := range []func() Section{
		func() Section { return fetching(db, now) },
		func() Section { return prices(db, now) },
		func() Section { return venues(db, now) },
		func() Section { return history(db, now, prog, hasProg) },
		func() Section { return market(db, now) },
		func() Section { return ecb(db, now) },
		func() Section { return ranks(db, now) },
		func() Section { return daily(db, now, prog, hasProg) },
		func() Section { return news(db, now) },
		func() Section { return forecasts(db, now) },
	} {
		r.Sections = append(r.Sections, f())
	}
	states := make([]string, 0, len(r.Sections))
	for _, s := range r.Sections {
		states = append(states, s.State)
	}
	r.State = Worst(states...)
	r.Summary = Summary(r.Sections)
	return r
}

// Summary is the one line over all of it: what wants a look, or that all is well.
func Summary(secs []Section) string {
	var bad, filling []string
	for _, s := range secs {
		switch s.State {
		case Flaky, Late, Failing:
			bad = append(bad, strings.ToLower(s.Title)+" "+s.State)
		case Filling:
			filling = append(filling, strings.ToLower(s.Title))
		}
	}
	switch {
	case len(bad) == 1:
		return "1 wants a look: " + bad[0]
	case len(bad) > 1:
		return strconv.Itoa(len(bad)) + " want a look: " + strings.Join(bad, ", ")
	case len(filling) > 0:
		return "all well, still filling " + strings.Join(filling, " and ")
	}
	return "all well"
}

// --- who fetches ---------------------------------------------------------------------------

func fetching(db *poltergres.ReadWrite, now time.Time) Section {
	s := Section{ID: "fetching", Title: "Who fetches", State: OK, AgeS: -1}
	p := hw.PhoneNetFrom(db)
	proxy, why := p.Proxy(now)
	phone := "not heard from yet"
	if !p.Seen.IsZero() {
		s.AgeS = age(now, p.Seen.Unix())
		phone = netName(p.Net) + ", heard " + Ago(s.AgeS) + " ago"
	}
	s.Rows = append(s.Rows, Row{K: "phone", V: phone})
	s.Rows = append(s.Rows, Row{K: "prices", V: "the box, every minute"})
	side := "the box"
	if proxy {
		side = "the phone, on Wi-Fi"
	}
	s.Rows = append(s.Rows, Row{K: "news, ECB, rank list", V: side})
	if who, at := lastBy(db, "kind IN ('ecb','ranks')"); who != "" {
		s.Rows = append(s.Rows, Row{K: "ECB and rank list last", V: "by the " + who + ", " + Ago(age(now, at)) + " ago"})
	}
	if who, at := tally.LastBy(db, "news_last_by"); who != "" {
		s.Rows = append(s.Rows, Row{K: "news last", V: "by the " + who + ", " + Ago(age(now, at)) + " ago"})
	}
	if proxy {
		s.Line = "the phone on Wi-Fi fetches news, ECB and the rank list; the box fetches prices"
	} else {
		s.Line = "the box fetches everything (" + why + ")"
	}
	return s
}

func netName(n string) string {
	switch n {
	case "wifi":
		return "on Wi-Fi"
	case "mobile":
		return "on mobile data"
	case "none":
		return "offline"
	}
	return "on " + n
}

func lastBy(db *poltergres.ReadWrite, where string) (string, int64) {
	rows, err := db.Query("SELECT by_who, ts FROM fetch_log WHERE " + where + " ORDER BY ts DESC, id DESC LIMIT 1")
	if err != nil || len(rows.Vals) != 1 {
		return "", 0
	}
	return str(rows.Vals[0][0]), i64(rows.Vals[0][1])
}

// --- prices --------------------------------------------------------------------------------

// PriceState judges the minute: lag is the newest price's age, have the minutes held in the last
// hour, running how long the box has been taking minutes.
func PriceState(lagS int64, have int, runningS int64) string {
	switch {
	case lagS < 0:
		return Waiting
	case lagS > 15*60:
		return Failing
	case lagS > 150:
		return Late
	case runningS >= 3600 && have < 57:
		return Flaky
	}
	return OK
}

func prices(db *poltergres.ReadWrite, now time.Time) Section {
	s := Section{ID: "prices", Title: "Prices", Every: "every minute", AgeS: -1}
	newest := one(db, "SELECT coalesce(max(ts),0) FROM crypto_index")
	oldestLive := one(db, "SELECT coalesce(min(ts),0) FROM crypto_series WHERE res = '1m' AND source = 'live'")
	// the sixty minutes up to now: the minute that started an hour ago is one too many
	have := int(one(db, "SELECT count(DISTINCT ts) FROM crypto_series WHERE res = '1m' AND source = 'live' AND ts > $1", now.Unix()-3600))
	lag := int64(-1)
	if newest > 0 {
		lag = age(now, newest)
		s.AgeS = lag
	}
	running := int64(0)
	if oldestLive > 0 {
		running = now.Unix() - oldestLive
	}
	s.State = PriceState(lag, have, running)
	if newest == 0 {
		s.Line = "no price yet: the first minute comes a minute after tallyd starts"
		return s
	}
	// the newest minute's index per symbol
	type ix struct {
		sym    string
		price  float64
		n      int
		spread float64
	}
	var all []ix
	if rows, err := db.Query("SELECT symbol, price, n, spread FROM crypto_index WHERE ts = $1", newest); err == nil {
		for _, v := range rows.Vals {
			if len(v) < 4 || v[0] == nil {
				continue
			}
			all = append(all, ix{*v[0], f64(v[1]), int(i64(v[2])), f64(v[3])})
		}
	}
	followed := tally.Symbols(db)
	var ns []int
	var single, wide []string
	bySym := map[string]ix{}
	for _, x := range all {
		bySym[x.sym] = x
		ns = append(ns, x.n)
		if x.n == 1 && x.sym != "USDT" {
			single = append(single, x.sym)
		}
		if x.spread > 0.01 {
			wide = append(wide, fmt.Sprintf("%s %.1f%%", x.sym, 100*x.spread))
		}
	}
	priced := 0
	var missing []string
	for _, sym := range followed {
		if _, ok := bySym[sym]; ok {
			priced++
		} else {
			missing = append(missing, sym)
		}
	}
	for _, sym := range []string{"BTC", "ETH"} {
		if x, ok := bySym[sym]; ok {
			s.Rows = append(s.Rows, Row{K: sym, V: fmt.Sprintf("%s USD · %s · spread %.2f%%", Money(x.price), plural(x.n, "venue"), 100*x.spread)})
		}
	}
	s.Rows = append(s.Rows, Row{K: "newest price", V: Ago(lag) + " old"})
	expect := 60
	if running < 3600 {
		expect = int(running/60) + 1
		if expect > 60 {
			expect = 60
		}
	}
	gap := Row{K: "minutes in the last hour", V: fmt.Sprintf("%d of %d", have, expect)}
	if running >= 3600 && have < 57 {
		gap.State = Flaky
	}
	s.Rows = append(s.Rows, gap)
	sym := Row{K: "symbols priced", V: fmt.Sprintf("%d of %d followed", priced, len(followed))}
	if len(missing) > 0 {
		sym.V += " (none for " + list(missing, 6) + ")"
	}
	s.Rows = append(s.Rows, sym)
	if len(ns) > 0 {
		sort.Ints(ns)
		s.Rows = append(s.Rows, Row{K: "venues per symbol", V: fmt.Sprintf("%d typical, %d at least", ns[len(ns)/2], ns[0])})
	}
	if len(single) > 0 {
		s.Rows = append(s.Rows, Row{K: "on one venue only", V: list(single, 8)})
	}
	if len(wide) > 0 {
		s.Rows = append(s.Rows, Row{K: "venues disagree by over 1%", V: list(wide, 6), State: Flaky})
	}
	if st, err := feedstat.Stats(db, feedstat.KindTick, now.Add(-time.Hour), false); err == nil && len(st) > 0 {
		t := st[0]
		r := Row{K: "each minute takes", V: fmt.Sprintf("%s typical, %s at worst (last hour)", Ms(t.P50Ms), Ms(t.MaxMs))}
		if t.MaxMs > 55_000 {
			r.State, r.V = Flaky, r.V+": a minute ran into the next"
		}
		s.Rows = append(s.Rows, r)
	}
	// the fast lane: BTC, ETH and SOL every five seconds from every venue, one fetch-log line per
	// venue a minute; a venue that misses gets its own row
	if st, err := feedstat.Stats(db, feedstat.KindFast, now.Add(-time.Hour), false); err == nil && len(st) > 0 {
		answering := 0
		var p50s []int
		var bad []Row
		for _, v := range st {
			ok := v.LastOKAt > 0 && now.Unix()-v.LastOKAt <= 5*60
			if ok {
				answering++
			}
			if v.P50Ms > 0 {
				p50s = append(p50s, v.P50Ms)
			}
			if !ok || v.Rate() < 0.9 {
				r := Row{K: "every 5 s, " + strings.TrimPrefix(v.Key, "fast:"), V: fmt.Sprintf("%.0f%% of minutes clean (last hour)", 100*v.Rate()), State: Flaky}
				if !ok {
					r.State = Failing
				}
				if v.LastError != "" {
					r.V += " · last: " + v.LastError
				}
				bad = append(bad, r)
			}
		}
		r := Row{K: "BTC, ETH, SOL every 5 s", V: fmt.Sprintf("%d of %d venues answering", answering, len(st))}
		if len(p50s) > 0 {
			sort.Ints(p50s)
			r.V += " · " + Ms(p50s[len(p50s)/2]) + " typical"
		}
		switch {
		case answering == 0:
			r.State = Failing
		case len(bad) > 0:
			r.State = Flaky
		}
		s.Rows = append(s.Rows, r)
		s.Rows = append(s.Rows, bad...)
	}
	s.Line = fmt.Sprintf("%d of %d symbols", priced, len(followed))
	if x, ok := bySym["BTC"]; ok {
		s.Line += " · BTC " + Money(x.price)
	}
	s.Line += fmt.Sprintf(" · %d of %d minutes this hour", have, expect)
	return s
}

// --- the exchanges -------------------------------------------------------------------------

// VenueState judges one exchange from its fetch log.
func VenueState(st feedstat.Stat, now time.Time) string {
	switch {
	case st.LastAt == 0:
		return Waiting
	case now.Unix()-st.LastAt > 10*60:
		return Failing // not asked for ten minutes: the minute itself has stopped
	case st.LastOKAt == 0 || now.Unix()-st.LastOKAt > 10*60 || st.FailsInRow >= 3:
		return Failing
	case st.Rate() < 0.9:
		return Flaky
	case st.P95Ms > 8000:
		return Late
	}
	return OK
}

func venues(db *poltergres.ReadWrite, now time.Time) Section {
	s := Section{ID: "venues", Title: "Exchanges", Every: "every minute", AgeS: -1}
	stats, err := feedstat.Stats(db, feedstat.KindTicker, now.Add(-time.Hour), true)
	if err != nil {
		s.State, s.Line = Waiting, "could not read the fetch log: "+err.Error()
		return s
	}
	byVenue := map[string]feedstat.Stat{}
	for _, st := range stats {
		byVenue[st.Key] = st
	}
	pairs := map[string]int{}
	if rows, err := db.Query("SELECT exchange, count(*) FROM crypto_quotes WHERE ts = (SELECT max(ts) FROM crypto_quotes) GROUP BY exchange"); err == nil {
		for _, v := range rows.Vals {
			if len(v) == 2 && v[0] != nil {
				pairs[*v[0]] = int(i64(v[1]))
			}
		}
	}
	// the venue's own clock against the minute it was asked (the venues that stamp their quotes)
	behind := map[string]int64{}
	if rows, err := db.Query(`SELECT exchange, percentile_cont(0.5) WITHIN GROUP (ORDER BY ts - quote_ts)::bigint FROM crypto_quotes
		WHERE ts >= $1 AND quote_ts > 0 GROUP BY exchange`, now.Add(-10*time.Minute).Unix()); err == nil {
		for _, v := range rows.Vals {
			if len(v) == 2 && v[0] != nil {
				behind[*v[0]] = i64(v[1])
			}
		}
	}
	use := indexUse(db, now)
	names := append([]string(nil), rates.BatchExchanges...) // Coinbase among them since its products list
	sort.Strings(names)
	answering := 0
	var states []string
	var p50s []int
	slowest, slowMs := "", 0
	var trouble []string
	for _, name := range names {
		st := byVenue[name]
		state := VenueState(st, now)
		states = append(states, state)
		if state == OK || state == Late || state == Flaky {
			answering++
		}
		if st.LastOKAt > 0 {
			if a := age(now, st.LastOKAt); s.AgeS < 0 || a < s.AgeS {
				s.AgeS = a
			}
		}
		var parts []string
		if st.Calls > 0 {
			parts = append(parts, fmt.Sprintf("%.0f%% ok", 100*st.Rate()), Ms(st.P50Ms)+" (p95 "+Ms(st.P95Ms)+")")
			p50s = append(p50s, st.P50Ms)
			if st.P95Ms > slowMs {
				slowest, slowMs = name, st.P95Ms
			}
		}
		if n := pairs[name]; n > 0 {
			parts = append(parts, plural(n, "pair"))
		}
		if b, ok := behind[name]; ok && b > 1 {
			parts = append(parts, "its prices "+Ago(b)+" behind")
		}
		if u, ok := use[name]; ok {
			parts = append(parts, u)
		}
		switch state {
		case Waiting:
			parts = []string{"not asked yet"}
		case Failing:
			why := st.LastError
			if why == "" {
				why = "no answer"
			}
			lastGood := "never"
			if st.LastOKAt > 0 {
				lastGood = Ago(age(now, st.LastOKAt)) + " ago"
			}
			parts = append([]string{"failing: " + why + ", last good " + lastGood}, parts...)
			trouble = append(trouble, name+" failing")
		case Flaky:
			if st.LastError != "" {
				parts = append(parts, "last error "+st.LastError)
			}
			trouble = append(trouble, name+" flaky")
		case Late:
			trouble = append(trouble, name+" slow")
		}
		s.Rows = append(s.Rows, Row{K: name, V: strings.Join(parts, " · "), State: state})
	}
	failing := 0
	for _, st := range states {
		if st == Failing {
			failing++
		}
	}
	switch {
	case answering == 0 && failing == 0:
		s.State = Waiting
	case failing*2 > len(names):
		s.State = Failing
	case Worst(states...) != OK && Worst(states...) != Waiting:
		s.State = Flaky // one venue down leaves the index to the others
	default:
		s.State = OK
	}
	s.Line = fmt.Sprintf("%d of %d answering", answering, len(names))
	if len(p50s) > 0 {
		sort.Ints(p50s)
		s.Line += " · " + Ms(p50s[len(p50s)/2]) + " typical"
	}
	if len(trouble) > 0 {
		s.Line += " · " + strings.Join(trouble, ", ")
	} else if slowest != "" {
		s.Line += " · slowest " + slowest + " " + Ms(slowMs)
	}
	return s
}

// indexUse says per venue how often its quotes went into the index over the last ten minutes, and
// the commonest reason when they did not.
func indexUse(db *poltergres.ReadWrite, now time.Time) map[string]string {
	rows, err := db.Query("SELECT used, dropped FROM crypto_index WHERE ts >= $1", now.Add(-10*time.Minute).Unix())
	if err != nil {
		return nil
	}
	used := map[string]int{}
	dropped := map[string]map[string]int{}
	for _, v := range rows.Vals {
		if len(v) < 2 {
			continue
		}
		seen := map[string]bool{}
		for _, ex := range strings.Split(str(v[0]), ",") {
			if ex != "" && !seen[ex] {
				seen[ex] = true
				used[ex]++
			}
		}
		for ex, why := range parseDropped(str(v[1])) {
			if seen[ex] {
				continue
			}
			if dropped[ex] == nil {
				dropped[ex] = map[string]int{}
			}
			dropped[ex][Reason(why)]++
		}
	}
	out := map[string]string{}
	for ex := range unionKeys(used, dropped) {
		u := used[ex]
		d := 0
		top, topN := "", 0
		for why, n := range dropped[ex] {
			d += n
			if n > topN || (n == topN && why < top) {
				top, topN = why, n
			}
		}
		if u+d == 0 {
			continue
		}
		v := fmt.Sprintf("in the index %d%%", int(math.Round(100*float64(u)/float64(u+d))))
		if top != "" && d*2 >= u+d {
			v += " (left out: " + top + ")"
		}
		out[ex] = v
	}
	return out
}

// Reason folds MakeIndex's words for leaving a quote out into a few: "stale (3m0s old)" and
// "off by 2.4% from the median" are one reason each, whatever the numbers.
func Reason(why string) string {
	switch {
	case strings.HasPrefix(why, "stale"):
		return "stale"
	case strings.HasPrefix(why, "off by"):
		return "too far from the others"
	}
	return why
}

func parseDropped(s string) map[string]string {
	m := map[string]string{}
	if s != "" {
		_ = json.Unmarshal([]byte(s), &m)
	}
	return m
}

func unionKeys(a map[string]int, b map[string]map[string]int) map[string]bool {
	out := map[string]bool{}
	for k := range a {
		out[k] = true
	}
	for k := range b {
		out[k] = true
	}
	return out
}

// --- the history ---------------------------------------------------------------------------

var resNames = map[string]string{tally.ResHour: "hourly, 30 days", tally.ResMinute: "minutes, 7 days"}

func history(db *poltergres.ReadWrite, now time.Time, p tally.Progress, ok bool) Section {
	s := Section{ID: "history", Title: "Price history", AgeS: -1}
	if !ok {
		s.State, s.Line = Waiting, "starts with tallyd's first minute"
		return s
	}
	s.AgeS = age(now, p.At)
	pace := float64(feedstat.Count(db, feedstat.KindHistory, now.Add(-10*time.Minute))) / 10
	var lines []string
	state := OK
	for _, h := range p.History {
		ri := tally.Resolutions[h.Res]
		expect := int(ri.Window / ri.Step)
		pts := int(one(db, "SELECT count(*) FROM crypto_market_series WHERE res = $1 AND ts >= $2", h.Res, now.Add(-ri.Window).Unix()))
		name := resNames[h.Res]
		short := strings.Split(name, ",")[0]
		var v string
		switch {
		case h.Markets == 0:
			v = "waits for the venues' first quotes"
			lines = append(lines, short+" waiting")
		case h.Walking > 0:
			state = Worst(state, Filling)
			v = fmt.Sprintf("%.0f%% held · %d of %d markets still walking back · about %d pages left", 100*h.Covered, h.Walking, h.Markets, h.PagesLeft)
			eta := ""
			if pace > 0 {
				eta = "≈ " + Ago(int64(float64(h.PagesLeft)/pace*60))
				v += " (" + eta + " at " + strconv.FormatFloat(pace, 'f', -1, 64) + " pages a minute)"
			}
			l := fmt.Sprintf("%s %.0f%%", short, 100*h.Covered)
			if eta != "" {
				l += ", " + eta + " left"
			}
			lines = append(lines, l)
		default:
			v = fmt.Sprintf("whole · %.0f%% of the window the venues have", 100*h.Covered)
			lines = append(lines, short+" whole")
		}
		if h.Folded {
			v += " · the market index made over it"
		}
		s.Rows = append(s.Rows, Row{K: name, V: v})
		s.Rows = append(s.Rows, Row{K: "CRYPTO50 " + short + " points", V: fmt.Sprintf("%s of %s", Count(pts), Count(expect))})
	}
	if st, err := feedstat.Stats(db, feedstat.KindHistory, now.Add(-time.Hour), true); err == nil {
		for _, v := range st {
			if v.Calls < 5 || v.Rate() >= 0.8 {
				continue
			}
			state = Worst(state, Flaky)
			s.Rows = append(s.Rows, Row{K: v.Key + " history", V: fmt.Sprintf("%.0f%% of %d pages came · %s", 100*v.Rate(), v.Calls, v.LastError), State: Flaky})
		}
	}
	if s.AgeS > 5*60 {
		state = Worst(state, Late)
		s.Rows = append(s.Rows, Row{K: "worked out", V: Ago(s.AgeS) + " ago (tallyd's minute has stopped)", State: Late})
	}
	s.State = state
	s.Line = strings.Join(lines, " · ")
	return s
}

// --- the market index ----------------------------------------------------------------------

func market(db *poltergres.ReadWrite, now time.Time) Section {
	s := Section{ID: "market", Title: "CRYPTO50", Every: "every minute", AgeS: -1}
	st, err := tally.MarketNow(db, now)
	if err != nil || st.Value <= 0 {
		s.State, s.Line = Waiting, "waits for the rank list's first day, which sets the fifty"
		return s
	}
	if ts := one(db, "SELECT coalesce(max(ts),0) FROM crypto_market_series WHERE res = '1m'"); ts > 0 {
		s.AgeS = age(now, ts)
	}
	s.State = OK
	if st.Constituents > 0 && float64(st.Priced) < 0.8*float64(st.Constituents) {
		s.State = Flaky
	}
	if s.AgeS > 5*60 {
		s.State = Worst(s.State, Late)
	}
	s.Line = fmt.Sprintf("%.1f · %+.2f%% today · %d of %d priced", st.Value, st.DayChange, st.Priced, st.Constituents)
	s.Rows = append(s.Rows, Row{K: "now", V: fmt.Sprintf("%.1f (%+.2f%% on %s's close of %.1f)", st.Value, st.DayChange, st.Day, st.DayValue)})
	// a day change no market makes is a price that is not a constituent's (a venue's ticker naming
	// another asset, a base in another unit): the arithmetic names it
	if st.DayChange > 25 || st.DayChange < -25 {
		s.State = Worst(s.State, Flaky)
		s.Rows = append(s.Rows, Row{K: "that change is a fault, not the market", V: "one constituent is priced as another coin, now or on " + st.Day + "; ghost-cli ghost.tallyd rates index=1 lays the terms out", State: Flaky})
	}
	if held := heldToday(db, st.Day); held != "" {
		s.State = Worst(s.State, Flaky)
		s.Rows = append(s.Rows, Row{K: "held flat", V: held + " (a price past 20× its month base is not this coin's; held at the last good one)", State: Flaky})
	}
	pr := Row{K: "priced live", V: fmt.Sprintf("%d of %d constituents", st.Priced, st.Constituents)}
	if s.State == Flaky {
		pr.State = Flaky
	}
	s.Rows = append(s.Rows, pr)
	s.Rows = append(s.Rows, Row{K: "weights", V: "of " + st.Month + ", by the month before's average daily volume"})
	s.Rows = append(s.Rows, Row{K: "daily history", V: Count(st.Days) + " days"})
	if len(st.Top) > 0 {
		var top []string
		for _, c := range st.Top {
			top = append(top, fmt.Sprintf("%s %.1f%%", c.Symbol, 100*c.Weight))
		}
		s.Rows = append(s.Rows, Row{K: "heaviest", V: strings.Join(top, " · ")})
	}
	if s.AgeS >= 0 {
		s.Rows = append(s.Rows, Row{K: "newest minute", V: Ago(s.AgeS) + " old"})
	}
	return s
}

// heldToday is the constituents the last stored day held at a carried price (named with "!" in
// the day's missing list), as one string; "" when none.
func heldToday(db *poltergres.ReadWrite, day string) string {
	if day == "" {
		return ""
	}
	rows, err := db.Query("SELECT missing FROM crypto_market_index WHERE day = $1", day)
	if err != nil || len(rows.Vals) != 1 || len(rows.Vals[0]) != 1 || rows.Vals[0][0] == nil {
		return ""
	}
	var held []string
	for _, m := range strings.Split(*rows.Vals[0][0], ",") {
		if strings.HasSuffix(m, "!") {
			held = append(held, strings.TrimSuffix(m, "!"))
		}
	}
	return strings.Join(held, ", ")
}

// --- the ECB -------------------------------------------------------------------------------

// ECBMissed counts the working days whose table should be out by now (the ECB publishes around
// 16:00 Frankfurt time, Monday to Friday) and is not: the days after the newest held, up to the
// last one due. A TARGET holiday counts as missed, so one missed day is let pass.
func ECBMissed(newest string, now time.Time) int {
	d, err := time.Parse("2006-01-02", newest)
	if err != nil {
		return -1
	}
	loc, lerr := time.LoadLocation("Europe/Berlin")
	if lerr != nil {
		loc = time.FixedZone("CET", 3600)
	}
	local := now.In(loc)
	due := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, time.UTC)
	if local.Hour() < 16 || (local.Hour() == 16 && local.Minute() < 30) {
		due = due.AddDate(0, 0, -1)
	}
	missed := 0
	for day := d.AddDate(0, 0, 1); !day.After(due); day = day.AddDate(0, 0, 1) {
		if wd := day.Weekday(); wd != time.Saturday && wd != time.Sunday {
			missed++
		}
	}
	return missed
}

func ecb(db *poltergres.ReadWrite, now time.Time) Section {
	s := Section{ID: "ecb", Title: "ECB rates", Every: "working days, 16:00 Frankfurt", AgeS: -1}
	var newest string
	days := 0
	if rows, err := db.Query("SELECT coalesce(max(day),''), count(DISTINCT day), coalesce(min(day),'') FROM fx_rates"); err == nil && len(rows.Vals) == 1 {
		newest = str(rows.Vals[0][0])
		days = int(i64(rows.Vals[0][1]))
		if oldest := str(rows.Vals[0][2]); oldest != "" {
			s.Rows = append(s.Rows, Row{K: "history", V: Count(days) + " days, back to " + oldest})
		}
	}
	stats, _ := feedstat.Stats(db, feedstat.KindECB, now.Add(-24*time.Hour), false)
	for _, st := range stats {
		if st.LastOKAt > 0 {
			if a := age(now, st.LastOKAt); s.AgeS < 0 || a < s.AgeS {
				s.AgeS = a
			}
		}
		s.Rows = append(s.Rows, fetchRow(st, now, map[string]string{"ecb": "the day's table", "ecb-90d": "the 90 days", "ecb-hist": "the history since 1999"}[st.Key]))
	}
	if newest == "" {
		s.State, s.Line = Waiting, "no table yet"
		return s
	}
	n := int(one(db, "SELECT count(*) FROM fx_rates WHERE day = $1", newest))
	missed := ECBMissed(newest, now)
	switch {
	case missed >= 5:
		s.State = Failing
	case missed >= 2:
		s.State = Late
	default:
		s.State = OK
	}
	s.Line = fmt.Sprintf("%s · %d currencies", dayName(newest), n)
	if missed >= 2 {
		s.Line += fmt.Sprintf(" · %d working days behind", missed)
	}
	s.Rows = append([]Row{{K: "newest table", V: fmt.Sprintf("%s · %d currencies", dayName(newest), n)}}, s.Rows...)
	return s
}

// fetchRow is one source's line: how its last day went.
func fetchRow(st feedstat.Stat, now time.Time, name string) Row {
	if name == "" {
		name = st.Key
	}
	r := Row{K: name, State: OK}
	switch {
	case st.LastOKAt == 0:
		r.State = Failing
		r.V = "never came: " + nonEmpty(st.LastError, "no answer")
	case st.FailsInRow > 0:
		r.State = Flaky
		r.V = fmt.Sprintf("failing %d in a row (%s) · last good %s ago", st.FailsInRow, nonEmpty(st.LastError, "no answer"), Ago(age(now, st.LastOKAt)))
		if st.FailsInRow >= 3 {
			r.State = Failing
		}
	default:
		r.V = "came " + Ago(age(now, st.LastOKAt)) + " ago"
		if st.LastBy != "" {
			r.V += " by the " + st.LastBy
		}
		if st.P50Ms > 0 {
			r.V += " · " + Ms(st.P50Ms)
		}
	}
	return r
}

var rankNames = map[string]string{"coinbase-ranks": "Coinbase"}

// --- the rank list -------------------------------------------------------------------------

func ranks(db *poltergres.ReadWrite, now time.Time) Section {
	s := Section{ID: "ranks", Title: "Rank list", Every: "hourly", AgeS: -1}
	var at int64
	var src string
	coins := 0
	if rows, err := db.Query("SELECT ts, source, count(*) FROM coin_ranks WHERE ts = (SELECT max(ts) FROM coin_ranks) GROUP BY ts, source"); err == nil && len(rows.Vals) > 0 {
		at, src, coins = i64(rows.Vals[0][0]), str(rows.Vals[0][1]), int(i64(rows.Vals[0][2]))
	}
	stats, _ := feedstat.Stats(db, feedstat.KindRanks, now.Add(-24*time.Hour), false)
	for _, st := range stats {
		s.Rows = append(s.Rows, fetchRow(st, now, rankNames[st.Key]))
	}
	if at == 0 {
		s.State, s.Line = Waiting, "no list yet"
		return s
	}
	s.AgeS = age(now, at)
	switch {
	case s.AgeS > 6*3600:
		s.State = Failing
	case s.AgeS > 2*3600:
		s.State = Late
	default:
		s.State = OK
	}
	name := rankNames[src]
	if name == "" {
		name = src
	}
	s.Line = fmt.Sprintf("%s · %d coins · %s ago", name, coins, Ago(s.AgeS))
	s.Rows = append([]Row{{K: "newest list", V: s.Line}}, s.Rows...)
	return s
}

// --- the daily candles ---------------------------------------------------------------------

func daily(db *poltergres.ReadWrite, now time.Time, p tally.Progress, ok bool) Section {
	s := Section{ID: "daily", Title: "Daily candles", Every: "once a day per pair", AgeS: -1}
	if !ok {
		s.State, s.Line = Waiting, "starts with tallyd's first minute"
		return s
	}
	var first string
	n := 0
	if rows, err := db.Query("SELECT coalesce(min(day),''), count(*), coalesce(max(day),'') FROM crypto_daily_index WHERE symbol = 'BTC'"); err == nil && len(rows.Vals) == 1 {
		first, n = str(rows.Vals[0][0]), int(i64(rows.Vals[0][1]))
		if n > 0 {
			s.Rows = append(s.Rows, Row{K: "BTC closes", V: fmt.Sprintf("%s days, %s to %s", Count(n), first, str(rows.Vals[0][2]))})
		}
	}
	s.Rows = append(s.Rows, Row{K: "refreshed in the last day", V: fmt.Sprintf("%d of %d pairs", p.DailyFresh, p.Seen)})
	b := p.Backfill
	bv := fmt.Sprintf("%d of %d markets as far back as the venue goes", b.Done, b.Markets)
	if b.OldestDay != "" {
		bv += " · oldest " + b.OldestDay
	}
	s.Rows = append(s.Rows, Row{K: "the years", V: bv})
	if b.Next != "" {
		s.Rows = append(s.Rows, Row{K: "next", V: b.Next})
	}
	state := OK
	if b.Markets > 0 && b.Done < b.Markets {
		state = Filling
	}
	if st, err := feedstat.Stats(db, feedstat.KindDaily, now.Add(-24*time.Hour), true); err == nil {
		for _, v := range st {
			if v.LastOKAt > 0 {
				if a := age(now, v.LastOKAt); s.AgeS < 0 || a < s.AgeS {
					s.AgeS = a
				}
			}
			if v.Calls < 5 || v.Rate() >= 0.8 {
				continue
			}
			state = Worst(state, Flaky)
			s.Rows = append(s.Rows, Row{K: v.Key + " candles", V: fmt.Sprintf("%.0f%% of %d came · %s", 100*v.Rate(), v.Calls, v.LastError), State: Flaky})
		}
	}
	if p.Seen == 0 {
		state = Waiting
	}
	s.State = state
	if n > 0 {
		s.Line = "BTC back to " + first[:4] + " · "
	}
	s.Line += fmt.Sprintf("years %d of %d · %d of %d refreshed today", b.Done, b.Markets, p.DailyFresh, p.Seen)
	return s
}

// --- the news ------------------------------------------------------------------------------

// NewsState judges the feeds: the newest fetch's age, how many failing of how many.
func NewsState(lastFetchAgeS int64, failing, enabled int) string {
	every := int64(feeds.FetchEvery / time.Second)
	switch {
	case lastFetchAgeS < 0 || enabled == 0:
		return Waiting
	case lastFetchAgeS > 3*every:
		return Failing
	case lastFetchAgeS > every+3600:
		return Late
	case failing*3 >= enabled:
		return Flaky
	}
	return OK
}

// NextDigest is when the next digest is due, in the zone given.
func NextDigest(now time.Time, loc *time.Location) time.Time {
	local := now.In(loc)
	for d := 0; d < 2; d++ {
		day := local.AddDate(0, 0, d)
		for _, h := range feeds.DigestHours {
			t := time.Date(day.Year(), day.Month(), day.Day(), h, 0, 0, 0, loc)
			if t.After(now) {
				return t
			}
		}
	}
	return now
}

func news(db *poltergres.ReadWrite, now time.Time) Section {
	s := Section{ID: "news", Title: "News", Every: "every " + Ago(int64(feeds.FetchEvery/time.Second)), AgeS: -1}
	rows, err := db.Query("SELECT id, name, enabled, last_fetch, last_ok, last_status, failures FROM news_feeds ORDER BY name")
	if err != nil {
		s.State, s.Line = Waiting, "could not read the feeds: "+err.Error()
		return s
	}
	enabled, okRecent, failing := 0, 0, 0
	var lastFetch int64
	var bad []Row
	var off []string
	for _, v := range rows.Vals {
		if len(v) < 7 || v[0] == nil {
			continue
		}
		if on := str(v[2]); on != "t" && on != "true" {
			off = append(off, nonEmpty(str(v[1]), str(v[0])))
			continue
		}
		enabled++
		lf, lo := i64(v[3]), i64(v[4])
		if lf > lastFetch {
			lastFetch = lf
		}
		if lo > 0 && now.Unix()-lo < int64(feeds.FetchEvery/time.Second)+3600 {
			okRecent++
		}
		if fails := i64(v[6]); fails >= 2 {
			failing++
			lastGood := "never"
			if lo > 0 {
				lastGood = Ago(age(now, lo)) + " ago"
			}
			bad = append(bad, Row{K: nonEmpty(str(v[1]), str(v[0])), V: fmt.Sprintf("%s, %d in a row · last good %s", str(v[5]), fails, lastGood), State: Failing})
		}
	}
	if lastFetch > 0 {
		s.AgeS = age(now, lastFetch)
	}
	s.State = NewsState(s.AgeS, failing, enabled)
	if lastFetch == 0 {
		s.Line = fmt.Sprintf("%d feeds, none fetched yet", enabled)
		return s
	}
	who, _ := tally.LastBy(db, "news_last_by")
	since := now.Add(-24 * time.Hour).Unix()
	entries := one(db, "SELECT count(*) FROM news_items WHERE fetched >= $1", since)
	stories := one(db, "SELECT count(*) FROM news_stories WHERE last_seen >= $1", since)
	summaries := one(db, "SELECT count(*) FROM news_stories WHERE model_at >= $1", since*1000)
	delay := one(db, "SELECT coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY fetched - published), 0)::bigint FROM news_items WHERE fetched >= $1 AND published < fetched", since)
	s.Rows = append(s.Rows, Row{K: "feeds", V: fmt.Sprintf("%d of %d answered in the last %s", okRecent, enabled, Ago(int64(feeds.FetchEvery/time.Second)+3600))})
	last := Ago(s.AgeS) + " ago"
	if who != "" {
		last += " by the " + who
	}
	s.Rows = append(s.Rows, Row{K: "last fetched", V: last})
	if st, err := feedstat.Stats(db, feedstat.KindNews, now.Add(-24*time.Hour), false); err == nil && len(st) > 0 {
		var p50s []int
		for _, v := range st {
			if v.P50Ms > 0 {
				p50s = append(p50s, v.P50Ms)
			}
		}
		if len(p50s) > 0 {
			sort.Ints(p50s)
			s.Rows = append(s.Rows, Row{K: "a feed takes", V: Ms(p50s[len(p50s)/2]) + " typical, " + Ms(p50s[len(p50s)-1]) + " the slowest"})
		}
	}
	s.Rows = append(s.Rows, Row{K: "last 24 hours", V: fmt.Sprintf("%d new entries · %d stories · %d summaries written", entries, stories, summaries)})
	// the articles read for the summaries: free, behind a paywall (its free part only), or failed
	if rows, err := db.Query(`SELECT count(*), count(*) FILTER (WHERE body_status = 'ok'), count(*) FILTER (WHERE body_status = 'paywalled'),
		count(*) FILTER (WHERE body_status = 'short'), count(*) FILTER (WHERE body_status NOT IN ('ok','paywalled','short'))
		FROM news_items WHERE body_at >= $1`, since); err == nil && len(rows.Vals) == 1 {
		v := rows.Vals[0]
		if n := i64(v[0]); n > 0 {
			s.Rows = append(s.Rows, Row{K: "articles read", V: fmt.Sprintf("%d · %d whole · %d paywalled (free part only) · %d with little text · %d failed", n, i64(v[1]), i64(v[2]), i64(v[3]), i64(v[4]))})
		}
	}
	s.Rows = append(s.Rows, articleOutlets(db, since)...)
	if text, at, _ := hw.NewsBrief(db); text != "" {
		s.Rows = append(s.Rows, Row{K: "brief", V: "written " + Ago(age(now, at)) + " ago"})
	}
	if delay > 0 {
		s.Rows = append(s.Rows, Row{K: "stories reach the box", V: Ago(delay) + " after they are published (median)"})
	}
	loc := hw.LocalZone(db)
	next := NextDigest(now, loc)
	dg := "next " + next.In(loc).Format("15:04")
	if at := one(db, "SELECT coalesce(max(at),0) FROM news_digests"); at > 0 {
		dg = "last " + Ago(age(now, at)) + " ago · " + dg
	}
	s.Rows = append(s.Rows, Row{K: "digest", V: dg})
	s.Rows = append(s.Rows, bad...)
	if len(off) > 0 {
		s.Rows = append(s.Rows, Row{K: "switched off", V: list(off, 6) + " (never gave a feed, or turned off by hand)"})
	}
	s.Line = fmt.Sprintf("%d of %d feeds · fetched %s · %d new today", okRecent, enabled, last, entries)
	return s
}

// --- weather -------------------------------------------------------------------------------

// forecasts is the pull of the world's larger places (each again after sixteen hours) (internal/weather). The box asks for
// the same places whoever and wherever the person is, so the section counts places and the
// pull's age, never a position.
func forecasts(db *poltergres.ReadWrite, now time.Time) Section {
	s := Section{ID: "weather", Title: "Weather", Every: strconv.Itoa(weather.Batch) + " places every " + weather.BatchEvery.String() + ", " + strconv.Itoa(weather.MaxPlaces) + " places a day", AgeS: -1}
	st := weather.Load(db)
	if st.Places == 0 {
		s.State = Waiting
		if n := one(db, "SELECT count(*) FROM geo_points WHERE kind = 'P' AND population >= $1", weather.MinPopulation); n == 0 {
			s.Line = "waits for the geo set (tools/fetch_geo.sh), which names the places"
		} else {
			s.Line = "no forecast yet; the first pull comes a minute or two after tallyd starts"
		}
		return s
	}
	s.AgeS = age(now, st.FetchedAt)
	s.State = WeatherState(s.AgeS)
	s.Rows = append(s.Rows, Row{K: "places", V: fmt.Sprintf("%d with a forecast, the oldest from %s ago", st.Places, Ago(age(now, st.OldestAt)))})
	s.Rows = append(s.Rows, Row{K: "last pulled", V: Ago(s.AgeS) + " ago by the box"})
	if stats, err := feedstat.Stats(db, feedstat.KindWeather, now.Add(-48*time.Hour), false); err == nil {
		for _, v := range stats {
			if v.Calls == 0 {
				continue
			}
			r := Row{K: "batches (2 days)", V: fmt.Sprintf("%d of %d came · %s typical", v.OK, v.Calls, Ms(v.P50Ms))}
			if v.OK < v.Calls {
				r.V += " · " + v.LastError
				r.State = Flaky
				if v.FailsInRow >= 3 {
					r.State = Failing
				}
				s.State = Worst(s.State, r.State)
			}
			s.Rows = append(s.Rows, r)
		}
	}
	s.Line = fmt.Sprintf("%d places · pulled %s ago", st.Places, Ago(s.AgeS))
	return s
}

// WeatherState: a pull a day with a lot of slack, since the forecast is four days long.
func WeatherState(ageS int64) string {
	switch {
	case ageS < 0:
		return Waiting
	case ageS > int64(3*weather.Stale/time.Second):
		return Failing
	case ageS > int64(weather.Stale/time.Second):
		return Late
	}
	return OK
}

// --- words and numbers ---------------------------------------------------------------------

// Ago is a duration in seconds the way the panel says it: "12 s", "4 min", "3 h", "2 days".
func Ago(sec int64) string {
	switch {
	case sec < 0:
		return "?"
	case sec < 90:
		return strconv.FormatInt(sec, 10) + " s"
	case sec < 90*60:
		return strconv.FormatInt((sec+30)/60, 10) + " min"
	case sec < 48*3600:
		return strconv.FormatInt((sec+1800)/3600, 10) + " h"
	}
	return strconv.FormatInt(sec/86400, 10) + " days"
}

// Ms is a latency: "180 ms", "1.2 s".
func Ms(ms int) string {
	if ms < 1000 {
		return strconv.Itoa(ms) + " ms"
	}
	return strconv.FormatFloat(float64(ms)/1000, 'f', 1, 64) + " s"
}

// Money is a price the way the lock-screen brief writes it.
func Money(v float64) string {
	switch {
	case v <= 0:
		return "0"
	case v < 1:
		return strconv.FormatFloat(v, 'f', 4, 64)
	case v < 1000:
		return strconv.FormatFloat(v, 'f', 2, 64)
	}
	return Count(int(math.Round(v)))
}

// Count writes a whole number with thousands separated: "10,080".
func Count(n int) string {
	s := strconv.Itoa(n)
	neg := strings.HasPrefix(s, "-")
	s = strings.TrimPrefix(s, "-")
	var b strings.Builder
	for i, c := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(c)
	}
	if neg {
		return "-" + b.String()
	}
	return b.String()
}

func dayName(day string) string {
	t, err := time.Parse("2006-01-02", day)
	if err != nil {
		return day
	}
	return t.Format("Mon 2 Jan")
}

func list(xs []string, n int) string {
	if len(xs) <= n {
		return strings.Join(xs, ", ")
	}
	return strings.Join(xs[:n], ", ") + fmt.Sprintf(" and %d more", len(xs)-n)
}

func plural(n int, one string) string {
	if n == 1 {
		return "1 " + one
	}
	return Count(n) + " " + one + "s"
}

func nonEmpty(s, alt string) string {
	if s == "" {
		return alt
	}
	return s
}

func age(now time.Time, unix int64) int64 {
	if a := now.Unix() - unix; a > 0 {
		return a
	}
	return 0
}

func one(db *poltergres.ReadWrite, q string, args ...any) int64 {
	rows, err := db.Query(q, args...)
	if err != nil || len(rows.Vals) != 1 || len(rows.Vals[0]) == 0 {
		return 0
	}
	return i64(rows.Vals[0][0])
}

// articleOutlets is a row per outlet whose pages did not all come whole in the last day: how
// many were read, and what the rest answered (an HTTP status, a failed fetch, little text). An
// outlet that refused six pages and gave none is left alone for the day (synthd's articlePass).
func articleOutlets(db *poltergres.ReadWrite, since int64) []Row {
	rows, err := db.Query(`SELECT f.name, count(*), count(*) FILTER (WHERE i.body_status IN ('ok','paywalled')),
		count(*) FILTER (WHERE i.body_status = 'short'),
		(array_agg(i.body_status ORDER BY i.body_at DESC) FILTER (WHERE i.body_status NOT IN ('ok','paywalled','short')))[1]
		FROM news_items i JOIN news_feeds f ON f.id = i.feed_id WHERE i.body_at >= $1 GROUP BY f.name ORDER BY f.name`, since)
	if err != nil {
		return nil
	}
	var out []Row
	for _, v := range rows.Vals {
		if len(v) < 5 || v[0] == nil {
			continue
		}
		n, read, short, why := i64(v[1]), i64(v[2]), i64(v[3]), str(v[4])
		if n == 0 || read == n {
			continue
		}
		val := fmt.Sprintf("%d of %d read", read, n)
		if short > 0 {
			val += fmt.Sprintf(" · %d with little text on the page", short)
		}
		if failed := n - read - short; failed > 0 {
			val += fmt.Sprintf(" · %d refused or failed (last: %s)", failed, why)
		}
		r := Row{K: "articles, " + *v[0], V: val}
		if read == 0 {
			r.State = Flaky
			if short == 0 && n >= 6 {
				r.V += " · left alone for the day"
			}
		}
		out = append(out, r)
	}
	return out
}

func i64(p *string) int64 {
	if p == nil {
		return 0
	}
	n, err := strconv.ParseInt(*p, 10, 64)
	if err != nil {
		f, ferr := strconv.ParseFloat(*p, 64)
		if ferr != nil {
			return 0
		}
		return int64(f)
	}
	return n
}

func f64(p *string) float64 {
	if p == nil {
		return 0
	}
	f, _ := strconv.ParseFloat(*p, 64)
	return f
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
