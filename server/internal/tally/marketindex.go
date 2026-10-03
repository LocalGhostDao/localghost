package tally

// THE MARKET INDEX, built from the tables (internal/rates has the arithmetic): each month's
// constituents and weights from the previous month's coin days, each day's value chained from the
// end of the previous month, and the value right now from the live indexes. Rebuilt after every
// rates batch, from a couple of days back, so a late candle or a corrected coin day is taken up.

import (
	"errors"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"github.com/LocalGhostDao/localghost/server/internal/rates"
)

// MarketState is the index as it stands, for the snapshot and the drill-in.
type MarketState struct {
	Code         string              `json:"code"`
	Value        float64             `json:"value"`     // right now, from the live indexes
	DayValue     float64             `json:"dayValue"`  // the last complete day's
	DayChange    float64             `json:"dayChange"` // now against the last complete day, per cent
	Day          string              `json:"day"`       // that day
	Month        string              `json:"month"`     // the weights in force
	Constituents int                 `json:"constituents"`
	Priced       int                 `json:"priced"`        // of those with a live price right now
	Days         int                 `json:"days"`          // days of history
	Top          []rates.Constituent `json:"top,omitempty"` // the five heaviest
}

// coinDay is one coin's row of a day.
type coinDay struct {
	price, cap, vol float64
}

func coinDays(db *poltergres.ReadWrite, from, to string) (map[string]map[string]coinDay, []string, error) {
	rows, err := db.Query("SELECT day, symbol, price_usd, market_cap, volume_usd FROM coin_daily WHERE day >= $1 AND day <= $2 ORDER BY day", from, to)
	if err != nil {
		return nil, nil, err
	}
	out := map[string]map[string]coinDay{}
	var days []string
	for _, v := range rows.Vals {
		if len(v) < 5 || v[0] == nil || v[1] == nil {
			continue
		}
		d := *v[0]
		if out[d] == nil {
			out[d] = map[string]coinDay{}
			days = append(days, d)
		}
		var c coinDay
		c.price, _ = strconv.ParseFloat(deref(v[2]), 64)
		c.cap, _ = strconv.ParseFloat(deref(v[3]), 64)
		c.vol, _ = strconv.ParseFloat(deref(v[4]), 64)
		out[d][*v[1]] = c
	}
	return out, days, nil
}

// closes is the box's own daily close per symbol for a day (the venues'), the coin day's price
// where the venues have none.
func closes(db *poltergres.ReadWrite, day string, coin map[string]coinDay) map[string]float64 {
	out := map[string]float64{}
	for sym, c := range coin {
		if c.price > 0 {
			out[sym] = c.price
		}
	}
	rows, err := db.Query("SELECT symbol, close FROM crypto_daily_index WHERE day = $1", day)
	if err == nil {
		for _, v := range rows.Vals {
			if len(v) == 2 && v[0] != nil && v[1] != nil {
				if c, err := strconv.ParseFloat(*v[1], 64); err == nil && c > 0 {
					out[*v[0]] = c
				}
			}
		}
	}
	return out
}

func prevMonth(month string) string {
	t, err := time.Parse("2006-01", month)
	if err != nil {
		return month
	}
	return t.AddDate(0, -1, 0).Format("2006-01")
}

// loadWeights reads a month's constituents, nil when the month has none.
func loadWeights(db *poltergres.ReadWrite, month string) ([]rates.Constituent, error) {
	rows, err := db.Query("SELECT symbol, rank, weight, avg_volume_usd, base_price FROM crypto_market_weights WHERE month = $1 ORDER BY rank", month)
	if err != nil {
		return nil, err
	}
	var out []rates.Constituent
	for _, v := range rows.Vals {
		if len(v) < 5 || v[0] == nil {
			continue
		}
		var c rates.Constituent
		c.Symbol = *v[0]
		c.Rank, _ = strconv.Atoi(deref(v[1]))
		c.Weight, _ = strconv.ParseFloat(deref(v[2]), 64)
		c.AvgVolume, _ = strconv.ParseFloat(deref(v[3]), 64)
		c.BasePrice, _ = strconv.ParseFloat(deref(v[4]), 64)
		out = append(out, c)
	}
	return out, nil
}

// setWeights computes and stores a month's constituents from the days before it: the average
// daily volume over the previous month, the market caps and the closes on its last day. For the
// first month there is nothing before it: its first day with a coin row stands in, so the index
// starts at 1000 there.
func setWeights(db *poltergres.ReadWrite, month string) ([]rates.Constituent, error) {
	first := month + "-01"
	pm := prevMonth(month)
	days, order, err := coinDays(db, pm+"-01", month+"-31")
	if err != nil {
		return nil, err
	}
	var before []string
	for _, d := range order {
		if d < first {
			before = append(before, d)
		}
	}
	if len(before) == 0 {
		for _, d := range order {
			if d >= first {
				before = []string{d}
				break
			}
		}
		if len(before) == 0 {
			return nil, nil
		}
	}
	sum := map[string]float64{}
	n := map[string]int{}
	for _, d := range before {
		for sym, c := range days[d] {
			if c.vol > 0 {
				sum[sym] += c.vol
				n[sym]++
			}
		}
	}
	avg := map[string]float64{}
	for sym, s := range sum {
		avg[sym] = s / float64(n[sym])
	}
	last := before[len(before)-1]
	caps := map[string]float64{}
	for sym, c := range days[last] {
		caps[sym] = c.cap
	}
	base := closes(db, last, days[last])
	cons := rates.Weights(avg, caps, base, rates.MarketIndexSize)
	if len(cons) == 0 {
		return nil, nil
	}
	for _, c := range cons {
		if err := db.Exec(`INSERT INTO crypto_market_weights (month, symbol, rank, weight, avg_volume_usd, base_price) VALUES ($1,$2,$3,$4,$5,$6)
			ON CONFLICT (month, symbol) DO UPDATE SET rank = EXCLUDED.rank, weight = EXCLUDED.weight, avg_volume_usd = EXCLUDED.avg_volume_usd, base_price = EXCLUDED.base_price`,
			month, c.Symbol, c.Rank, c.Weight, c.AvgVolume, c.BasePrice); err != nil {
			return nil, err
		}
	}
	return cons, nil
}

// chainBefore is the index value on the last day before a month (the start value when none).
func chainBefore(db *poltergres.ReadWrite, month string) float64 {
	rows, err := db.Query("SELECT value FROM crypto_market_index WHERE day < $1 ORDER BY day DESC LIMIT 1", month+"-01")
	if err == nil && len(rows.Vals) == 1 && rows.Vals[0][0] != nil {
		if v, err := strconv.ParseFloat(*rows.Vals[0][0], 64); err == nil && v > 0 {
			return v
		}
	}
	return rates.MarketIndexStart
}

// RebuildMarketIndex writes the daily values up to yesterday (today is live), from two days before
// the last value held, or from the first coin day. Returns the days written.
func RebuildMarketIndex(db *poltergres.ReadWrite, now time.Time) (int, error) {
	yesterday := now.UTC().AddDate(0, 0, -1).Format("2006-01-02")
	from := ""
	if rows, err := db.Query("SELECT max(day) FROM crypto_market_index"); err == nil && len(rows.Vals) == 1 && rows.Vals[0][0] != nil {
		if t, err := time.Parse("2006-01-02", *rows.Vals[0][0]); err == nil {
			from = t.AddDate(0, 0, -2).Format("2006-01-02")
		}
	}
	if from == "" {
		rows, err := db.Query("SELECT min(day) FROM coin_daily")
		if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
			return 0, nil // no coin day yet
		}
		from = *rows.Vals[0][0]
	}
	if from > yesterday {
		return 0, nil
	}
	days, _, err := coinDays(db, from, yesterday)
	if err != nil {
		return 0, err
	}
	// a day with no coin row still gets a value (the venues' closes, carried prices): walk the calendar
	start, _ := time.Parse("2006-01-02", from)
	end, _ := time.Parse("2006-01-02", yesterday)
	carried := map[string]float64{}
	// seed the carried prices from the closes just before `from`
	if rows, err := db.Query("SELECT symbol, close FROM crypto_daily_index WHERE day < $1 AND day >= $2", from, start.AddDate(0, 0, -14).Format("2006-01-02")); err == nil {
		for _, v := range rows.Vals {
			if len(v) == 2 && v[0] != nil && v[1] != nil {
				carried[*v[0]], _ = strconv.ParseFloat(*v[1], 64)
			}
		}
	}
	var cons []rates.Constituent
	month := ""
	chain := 0.0
	written := 0
	for d := start; !d.After(end); d = d.AddDate(0, 0, 1) {
		day := d.Format("2006-01-02")
		if m := rates.MonthOf(day); m != month {
			month = m
			cons, err = loadWeights(db, month)
			if err != nil {
				return written, err
			}
			if len(cons) == 0 {
				cons, err = setWeights(db, month)
				if err != nil {
					return written, err
				}
			}
			chain = chainBefore(db, month)
		}
		if len(cons) == 0 {
			continue // nothing to price yet this month (no coin day before it)
		}
		prices := closes(db, day, days[day])
		value, priced, missing := rates.Value(chain, cons, prices, carried)
		if value <= 0 {
			continue
		}
		for sym, p := range prices {
			carried[sym] = p
		}
		sort.Strings(missing)
		if err := db.Exec("INSERT INTO crypto_market_index (day, value, priced, missing) VALUES ($1,$2,$3,$4) ON CONFLICT (day) DO UPDATE SET value = EXCLUDED.value, priced = EXCLUDED.priced, missing = EXCLUDED.missing",
			day, value, priced, strings.Join(missing, ",")); err != nil {
			return written, err
		}
		written++
	}
	return written, nil
}

// MarketNow is the index right now: this month's weights against the live index per symbol (the
// venues', within two hours), the newest rank price where the venues have none.
func MarketNow(db *poltergres.ReadWrite, now time.Time) (MarketState, error) {
	st := MarketState{Code: rates.MarketIndexCode}
	today := now.UTC().Format("2006-01-02")
	month := rates.MonthOf(today)
	cons, err := loadWeights(db, month)
	if err != nil {
		return st, err
	}
	if len(cons) == 0 {
		// the month has no weights yet (its first day, before a rebuild): the previous month's stand in
		cons, err = loadWeights(db, prevMonth(month))
		if err != nil || len(cons) == 0 {
			if err == nil {
				err = errors.New("no constituents yet (the rank list builds them from its first day)")
			}
			return st, err
		}
		month = prevMonth(month)
	}
	st.Month, st.Constituents = month, len(cons)
	prices := map[string]float64{}
	if rows, err := db.Query("SELECT DISTINCT ON (symbol) symbol, price FROM crypto_index WHERE ts >= $1 ORDER BY symbol, ts DESC", now.Add(-2*time.Hour).Unix()); err == nil {
		for _, v := range rows.Vals {
			if len(v) == 2 && v[0] != nil && v[1] != nil {
				prices[*v[0]], _ = strconv.ParseFloat(*v[1], 64)
			}
		}
	}
	carried := map[string]float64{}
	if rows, err := db.Query("SELECT symbol, price_usd FROM coin_daily WHERE day = (SELECT max(day) FROM coin_daily)"); err == nil {
		for _, v := range rows.Vals {
			if len(v) == 2 && v[0] != nil && v[1] != nil {
				carried[*v[0]], _ = strconv.ParseFloat(*v[1], 64)
			}
		}
	}
	chain := chainBefore(db, month)
	st.Value, st.Priced, _ = rates.Value(chain, cons, prices, carried)
	if rows, err := db.Query("SELECT day, value FROM crypto_market_index ORDER BY day DESC LIMIT 1"); err == nil && len(rows.Vals) == 1 && len(rows.Vals[0]) == 2 && rows.Vals[0][0] != nil {
		st.Day = *rows.Vals[0][0]
		st.DayValue, _ = strconv.ParseFloat(deref(rows.Vals[0][1]), 64)
		if st.DayValue > 0 && st.Value > 0 {
			st.DayChange = 100 * (st.Value/st.DayValue - 1)
		}
	}
	if rows, err := db.Query("SELECT count(*) FROM crypto_market_index"); err == nil && len(rows.Vals) == 1 && rows.Vals[0][0] != nil {
		st.Days, _ = strconv.Atoi(*rows.Vals[0][0])
	}
	top := append([]rates.Constituent(nil), cons...)
	sort.Slice(top, func(i, j int) bool { return top[i].Weight > top[j].Weight })
	if len(top) > 5 {
		top = top[:5]
	}
	st.Top = top
	return st, nil
}

// MarketHistory is the daily values, newest first.
func MarketHistory(db *poltergres.ReadWrite, limit int) ([]struct {
	Day   string
	Value float64
}, error) {
	if limit <= 0 || limit > 20000 {
		limit = 400
	}
	rows, err := db.Query("SELECT day, value FROM crypto_market_index ORDER BY day DESC LIMIT $1", limit)
	if err != nil {
		return nil, err
	}
	var out []struct {
		Day   string
		Value float64
	}
	for _, v := range rows.Vals {
		if len(v) == 2 && v[0] != nil {
			val, _ := strconv.ParseFloat(deref(v[1]), 64)
			out = append(out, struct {
				Day   string
				Value float64
			}{*v[0], val})
		}
	}
	return out, nil
}

// MarketTerms is the index's arithmetic laid out, for `ghost-cli ghost.tallyd rates index=1`:
// what each constituent contributes to the value right now and to the last complete day's, so a
// row that is not this coin's price shows itself (a part far from its weight).
type MarketTerms struct {
	Month string       `json:"month"`
	Chain float64      `json:"chain"` // the value the month started from
	Now   MarketSide   `json:"now"`
	Day   MarketSide   `json:"day"`
	Odd   []rates.Term `json:"odd,omitempty"` // the terms whose part is furthest from their weight, either side
}

// MarketSide is one value's terms: the live one or a stored day's, computed again from the tables.
type MarketSide struct {
	Day    string       `json:"day,omitempty"`
	Value  float64      `json:"value"`
	Stored float64      `json:"stored,omitempty"` // the day's value as the table has it
	Priced int          `json:"priced"`
	Held   []string     `json:"held,omitempty"`
	Terms  []rates.Term `json:"terms"`
}

// ExplainMarket computes the live value and the last stored day's value term by term.
func ExplainMarket(db *poltergres.ReadWrite, now time.Time) (MarketTerms, error) {
	var ex MarketTerms
	today := now.UTC().Format("2006-01-02")
	month := rates.MonthOf(today)
	cons, err := loadWeights(db, month)
	if err != nil {
		return ex, err
	}
	if len(cons) == 0 {
		month = prevMonth(month)
		if cons, err = loadWeights(db, month); err != nil || len(cons) == 0 {
			if err == nil {
				err = errors.New("no constituents yet")
			}
			return ex, err
		}
	}
	ex.Month, ex.Chain = month, chainBefore(db, month)
	side := func(prices, carried map[string]float64) MarketSide {
		var s MarketSide
		s.Terms = rates.Terms(cons, prices, carried)
		v, priced, missing := rates.Value(ex.Chain, cons, prices, carried)
		s.Value, s.Priced = v, priced
		for _, m := range missing {
			if strings.HasSuffix(m, "!") {
				s.Held = append(s.Held, strings.TrimSuffix(m, "!"))
			}
		}
		return s
	}
	// now, as MarketNow prices it
	live := map[string]float64{}
	if rows, err := db.Query("SELECT DISTINCT ON (symbol) symbol, price FROM crypto_index WHERE ts >= $1 ORDER BY symbol, ts DESC", now.Add(-2*time.Hour).Unix()); err == nil {
		for _, v := range rows.Vals {
			if len(v) == 2 && v[0] != nil && v[1] != nil {
				live[*v[0]], _ = strconv.ParseFloat(*v[1], 64)
			}
		}
	}
	carriedNow := map[string]float64{}
	if rows, err := db.Query("SELECT symbol, price_usd FROM coin_daily WHERE day = (SELECT max(day) FROM coin_daily)"); err == nil {
		for _, v := range rows.Vals {
			if len(v) == 2 && v[0] != nil && v[1] != nil {
				carriedNow[*v[0]], _ = strconv.ParseFloat(*v[1], 64)
			}
		}
	}
	ex.Now = side(live, carriedNow)
	// the last stored day, as the rebuild prices it: the day's closes (the venues' over the coin
	// day's), carried from the fortnight before
	if rows, err := db.Query("SELECT day, value FROM crypto_market_index ORDER BY day DESC LIMIT 1"); err == nil && len(rows.Vals) == 1 && len(rows.Vals[0]) == 2 && rows.Vals[0][0] != nil {
		day := *rows.Vals[0][0]
		days, _, _ := coinDays(db, day, day)
		prices := closes(db, day, days[day])
		carried := map[string]float64{}
		t, _ := time.Parse("2006-01-02", day)
		if crows, err := db.Query("SELECT symbol, close FROM crypto_daily_index WHERE day < $1 AND day >= $2 ORDER BY day", day, t.AddDate(0, 0, -14).Format("2006-01-02")); err == nil {
			for _, v := range crows.Vals {
				if len(v) == 2 && v[0] != nil && v[1] != nil {
					carried[*v[0]], _ = strconv.ParseFloat(*v[1], 64)
				}
			}
		}
		ex.Day = side(prices, carried)
		ex.Day.Day = day
		ex.Day.Stored, _ = strconv.ParseFloat(deref(rows.Vals[0][1]), 64)
	}
	// the terms furthest from flat, from both sides, the worst first
	var odd []rates.Term
	for _, s := range []MarketSide{ex.Now, ex.Day} {
		for _, t := range s.Terms {
			if t.Ratio > 1.5 || (t.Ratio > 0 && t.Ratio < 1/1.5) {
				odd = append(odd, t)
			}
		}
	}
	sort.Slice(odd, func(i, j int) bool {
		di, dj := odd[i].Part-odd[i].Weight, odd[j].Part-odd[j].Weight
		if di < 0 {
			di = -di
		}
		if dj < 0 {
			dj = -dj
		}
		return di > dj
	})
	if len(odd) > 12 {
		odd = odd[:12]
	}
	ex.Odd = odd
	return ex, nil
}
