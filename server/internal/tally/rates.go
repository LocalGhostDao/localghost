package tally

// THE RATES: the market bodies fetched for the box (by the phone on Wi-Fi, by ghost.tallyd itself
// otherwise; internal/rates says what each source is) into fx_rates, crypto_quotes, crypto_index,
// crypto_daily, crypto_daily_index and coin_ranks. Every source ingested leaves a mark (settings
// fetch_<id>), so whichever side fetched last, the other knows what is due. Pure over the database.

import (
	"encoding/json"
	"errors"
	"fmt"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/feedstat"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"github.com/LocalGhostDao/localghost/server/internal/rates"
)

// RatesBatch is what arrives: each source's body, or why there is none.
type RatesBatch struct {
	FetchedAt int64  `json:"fetchedAt"`
	By        string `json:"by,omitempty"` // "box" when ghost.tallyd fetched it; the phone sends none
	Sources   []struct {
		ID     string `json:"id"`
		Status int    `json:"status"`
		Error  string `json:"error,omitempty"`
		Body   string `json:"body,omitempty"`
		TookMs int    `json:"tookMs,omitempty"`
	} `json:"sources"`
}

// RatesResult says what one batch did.
type RatesResult struct {
	FXDay      string                 `json:"fxDay,omitempty"`
	FXDays     int                    `json:"fxDays"` // days of ECB rates written
	Quotes     int                    `json:"quotes"`
	Index      map[string]rates.Index `json:"index,omitempty"` // per symbol
	Candles    int                    `json:"candles"`
	DaysIndex  int                    `json:"daysIndexed"`
	Coins      int                    `json:"coins"`
	CoinSource string                 `json:"coinSource,omitempty"`
	Failed     map[string]string      `json:"failed,omitempty"`
	Marked     []string               `json:"-"` // the ids marked fetched
}

const (
	quotesKeep = 2 * 24 * time.Hour // a quote a minute per venue and pair: the series keeps the prices
	ranksKeep  = 7 * 24 * time.Hour
)

// ParseRates checks a batch's shape without storing it (secd calls it before spooling).
func ParseRates(raw []byte) (RatesBatch, bool) {
	var b RatesBatch
	if err := json.Unmarshal(raw, &b); err != nil || len(b.Sources) == 0 {
		return b, false
	}
	for _, s := range b.Sources {
		if s.ID == "" {
			return b, false
		}
	}
	return b, true
}

// Symbols is the list the box prices every minute: the hundred largest by market cap on the
// newest day of Coinbase's rank list (stablecoins and wrapped coins too: each is on the list and
// on CRYPTO), then whatever was added by hand (settings rates_symbols); BTC and ETH when there is
// no rank list yet.
func Symbols(db *poltergres.ReadWrite) []string {
	return rankSymbols(db, PricedSize, false)
}

// HistorySymbols is the list whose thirty days of hours and week of minutes are walked back from
// the venues: the fifty that can be CRYPTO50's constituents, and those added by hand. The others
// build their series from the box's own minutes from the day they joined the list.
func HistorySymbols(db *poltergres.ReadWrite) []string {
	return rankSymbols(db, rates.MarketIndexSize, true)
}

// PricedSize is how many of the rank list the box prices.
const PricedSize = 100

func rankSymbols(db *poltergres.ReadWrite, n int, constituents bool) []string {
	var out []string
	seen := map[string]bool{}
	add := func(s string) {
		s = strings.ToUpper(strings.TrimSpace(s))
		if s != "" && !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	// the newest day of Coinbase's list; before its first, the newest day of any list
	rows, err := db.Query(`SELECT symbol FROM coin_daily WHERE source = $1 AND day = (SELECT max(day) FROM coin_daily WHERE source = $1)
		ORDER BY rank LIMIT 160`, rates.CoinbaseRanks)
	if err == nil && len(rows.Vals) == 0 {
		rows, err = db.Query("SELECT symbol FROM coin_daily WHERE day = (SELECT max(day) FROM coin_daily) ORDER BY rank LIMIT 160")
	}
	if err == nil {
		var coins []rates.Coin
		for i, v := range rows.Vals {
			if len(v) > 0 && v[0] != nil {
				coins = append(coins, rates.Coin{Rank: i + 1, Symbol: *v[0], PriceUSD: 1})
			}
		}
		if constituents {
			for _, s := range rates.SymbolsFromRanks(coins, n) {
				add(s)
			}
		} else {
			for _, c := range coins {
				if len(out) >= n {
					break
				}
				add(c.Symbol)
			}
		}
	}
	for _, s := range ManualSymbols(db) {
		add(s)
	}
	if len(out) == 0 {
		for _, s := range rates.DefaultSymbols {
			add(s)
		}
	}
	return out
}

// ManualSymbols is what was added by hand (settings rates_symbols).
func ManualSymbols(db *poltergres.ReadWrite) []string {
	rows, err := db.Query("SELECT value FROM settings WHERE key = 'rates_symbols'")
	if err != nil || len(rows.Vals) != 1 || rows.Vals[0][0] == nil || *rows.Vals[0][0] == "" {
		return nil
	}
	var out []string
	for _, s := range strings.Split(*rows.Vals[0][0], ",") {
		if s = strings.ToUpper(strings.TrimSpace(s)); s != "" {
			out = append(out, s)
		}
	}
	return out
}

// SetSymbols writes the list (add= / remove= in the ctl).
func SetSymbols(db *poltergres.ReadWrite, syms []string) error {
	return db.Exec("INSERT INTO settings (key, value) VALUES ('rates_symbols', $1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", strings.Join(syms, ","))
}

// LastBy is who fetched the last rates batch and when ("" when none): "phone" or "box".
func LastBy(db *poltergres.ReadWrite, key string) (who string, at int64) {
	rows, err := db.Query("SELECT value FROM settings WHERE key = $1", key)
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return "", 0
	}
	v := *rows.Vals[0][0]
	if i := strings.IndexByte(v, '@'); i > 0 {
		at, _ = strconv.ParseInt(v[i+1:], 10, 64)
		return v[:i], at
	}
	return v, 0
}

// Marks is when each source was last fetched (settings fetch_<id>), for what is due.
func Marks(db *poltergres.ReadWrite) map[string]int64 {
	out := map[string]int64{}
	rows, err := db.Query("SELECT key, value FROM settings WHERE key LIKE 'fetch_%'")
	if err != nil {
		return out
	}
	for _, v := range rows.Vals {
		if len(v) < 2 || v[0] == nil || v[1] == nil {
			continue
		}
		if n, err := strconv.ParseInt(*v[1], 10, 64); err == nil {
			out[strings.TrimPrefix(*v[0], "fetch_")] = n
		}
	}
	return out
}

func mark(db *poltergres.ReadWrite, id string, at int64) {
	_ = db.Exec("INSERT INTO settings (key, value) VALUES ($1, $2) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", "fetch_"+id, strconv.FormatInt(at, 10))
}

// IngestRates stores one batch. A source that failed is noted, the rest still land; a source that
// answered is marked fetched whether or not it parsed (the address was reached: asking again in a
// minute would not help). The index per symbol is made from this batch's quotes and the newest
// quote of any venue missing from it, when that quote is fresh, USDT legs folded into dollars.
func IngestRates(db *poltergres.ReadWrite, raw []byte, now time.Time) (RatesResult, error) {
	b, ok := ParseRates(raw)
	if !ok {
		return RatesResult{}, errors.New("not a rates batch")
	}
	if b.FetchedAt == 0 {
		b.FetchedAt = now.Unix()
	}
	fetched := time.Unix(b.FetchedAt, 0)
	by := b.By
	if by == "" {
		by = "phone"
	}
	_ = db.Exec("INSERT INTO settings (key, value) VALUES ('rates_last_by', $1) ON CONFLICT (key) DO UPDATE SET value = EXCLUDED.value", by+"@"+strconv.FormatInt(b.FetchedAt, 10))
	res := RatesResult{Failed: map[string]string{}, Index: map[string]rates.Index{}}
	var quotes []rates.Quote
	var want map[string]bool
	coinBodies := map[string][]byte{}
	items := map[string]int{}                   // what each answer gave, for the fetch log
	touchedDays := map[string]map[string]bool{} // symbol -> days with new candles
	for _, s := range b.Sources {
		switch {
		case s.Error != "":
			res.Failed[s.ID] = "fetch failed: " + s.Error
			continue
		case s.Status != 0 && (s.Status < 200 || s.Status > 299):
			res.Failed[s.ID] = "HTTP " + strconv.Itoa(s.Status)
			mark(db, s.ID, b.FetchedAt)
			res.Marked = append(res.Marked, s.ID)
			continue
		case s.Body == "":
			res.Failed[s.ID] = "empty answer"
			continue
		}
		mark(db, s.ID, b.FetchedAt)
		res.Marked = append(res.Marked, s.ID)
		switch {
		case s.ID == "ecb" || s.ID == "ecb-90d" || s.ID == "ecb-hist":
			days, err := rates.ParseECBAll([]byte(s.Body))
			if err != nil {
				res.Failed[s.ID] = err.Error()
				continue
			}
			for _, d := range days {
				for code, r := range d.Rates {
					if err := db.Exec("INSERT INTO fx_rates (day, code, rate) VALUES ($1,$2,$3) ON CONFLICT (day, code) DO UPDATE SET rate = EXCLUDED.rate", d.Day, code, r); err != nil {
						return res, err
					}
				}
			}
			res.FXDays += len(days)
			items[s.ID] = len(days)
			if days[0].Day > res.FXDay {
				res.FXDay = days[0].Day
			}
		case strings.HasPrefix(s.ID, "hist:"):
			m, ok := rates.ParseMarketID(strings.TrimPrefix(s.ID, "hist:"))
			if !ok {
				res.Failed[s.ID] = "unknown market"
				continue
			}
			cs, err := rates.ParseCandles(m, []byte(s.Body))
			if err != nil {
				res.Failed[s.ID] = err.Error()
				continue
			}
			for _, c := range cs {
				if err := db.Exec(`INSERT INTO crypto_daily (day, symbol, exchange, quote, open, high, low, close, volume) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
					ON CONFLICT (day, symbol, exchange, quote) DO UPDATE SET open = EXCLUDED.open, high = EXCLUDED.high, low = EXCLUDED.low, close = EXCLUDED.close, volume = EXCLUDED.volume`,
					c.Day, m.Base, m.Exchange, m.Quote, c.Open, c.High, c.Low, c.Close, c.Volume); err != nil {
					return res, err
				}
				if touchedDays[m.Base] == nil {
					touchedDays[m.Base] = map[string]bool{}
				}
				touchedDays[m.Base][c.Day] = true
			}
			res.Candles += len(cs)
			items[s.ID] = len(cs)
		case rates.IsRankSource(s.ID):
			coinBodies[s.ID] = []byte(s.Body)
		case strings.HasSuffix(s.ID, ":all"):
			// a venue's every pair in one answer: keep the symbols followed and the USDT leg
			if want == nil {
				want = map[string]bool{}
				for _, sym := range Symbols(db) {
					want[sym] = true
				}
			}
			qs, err := rates.ParseBatch(strings.TrimSuffix(s.ID, ":all"), []byte(s.Body), want, fetched)
			if err != nil {
				res.Failed[s.ID] = err.Error()
				continue
			}
			if err := insertQuotes(db, b.FetchedAt, qs); err != nil {
				return res, err
			}
			quotes = append(quotes, qs...)
			res.Quotes += len(qs)
			items[s.ID] = len(qs)
		default:
			m, ok := rates.ParseMarketID(s.ID)
			if !ok {
				res.Failed[s.ID] = "unknown source"
				continue
			}
			q, err := rates.ParseTicker(m, []byte(s.Body), fetched)
			if err != nil {
				res.Failed[s.ID] = err.Error()
				continue
			}
			if err := db.Exec(`INSERT INTO crypto_quotes (ts, exchange, base, quote, price, volume, quote_ts) VALUES ($1,$2,$3,$4,$5,$6,$7)
				ON CONFLICT (ts, exchange, base, quote) DO UPDATE SET price = EXCLUDED.price, volume = EXCLUDED.volume, quote_ts = EXCLUDED.quote_ts`,
				b.FetchedAt, q.Exchange, q.Base, q.QuoteCcy, q.Price, q.Volume, q.At.Unix()); err != nil {
				return res, err
			}
			quotes = append(quotes, q)
			res.Quotes++
			items[s.ID] = 1
		}
	}
	// the index per symbol, blended from every market (rates.Blend): this batch's quotes, plus the
	// newest quote of a market that did not come, for as long as its weight has not run out
	if len(quotes) > 0 {
		have := map[string]bool{}
		for _, q := range quotes {
			have[q.Exchange+":"+q.Base+"-"+q.QuoteCcy] = true
		}
		rows, err := db.Query(`SELECT DISTINCT ON (exchange, base, quote) exchange, base, quote, price, volume, quote_ts FROM crypto_quotes
			WHERE ts >= $1 AND ts < $2 ORDER BY exchange, base, quote, ts DESC`, b.FetchedAt-1500, b.FetchedAt)
		if err == nil {
			for _, v := range rows.Vals {
				if len(v) < 6 || v[0] == nil || v[1] == nil || v[2] == nil || have[*v[0]+":"+*v[1]+"-"+*v[2]] {
					continue
				}
				price, _ := strconv.ParseFloat(deref(v[3]), 64)
				vol, _ := strconv.ParseFloat(deref(v[4]), 64)
				qts, _ := strconv.ParseInt(deref(v[5]), 10, 64)
				quotes = append(quotes, rates.Quote{Exchange: *v[0], Base: *v[1], QuoteCcy: *v[2], Price: price, Volume: vol, At: time.Unix(qts, 0)})
			}
		}
		if want == nil {
			want = map[string]bool{}
			for _, sym := range Symbols(db) {
				want[sym] = true
			}
		}
		var names []string
		for sym := range want {
			names = append(names, sym)
		}
		ixs, _, failed := rates.BlendAll(quotes, names, EURUSD(db), LastPrices(db, b.FetchedAt), fetched)
		for sym, why := range failed {
			res.Failed["index:"+sym] = why
		}
		var syms []string
		for sym := range ixs {
			syms = append(syms, sym)
		}
		sort.Strings(syms)
		for _, sym := range syms {
			ix := ixs[sym]
			dropped, _ := json.Marshal(ix.Dropped)
			if err := db.Exec(`INSERT INTO crypto_index (ts, symbol, price, n, spread, used, dropped, markets, paths) VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9)
				ON CONFLICT (ts, symbol) DO UPDATE SET price = EXCLUDED.price, n = EXCLUDED.n, spread = EXCLUDED.spread, used = EXCLUDED.used,
				dropped = EXCLUDED.dropped, markets = EXCLUDED.markets, paths = EXCLUDED.paths`,
				b.FetchedAt, sym, ix.Price, ix.N, ix.Spread, strings.Join(ix.Used, ","), string(dropped), ix.Markets, strings.Join(ix.Paths, ",")); err != nil {
				return res, err
			}
			res.Index[sym] = ix
		}
	}
	// the daily index for every day a candle touched
	for sym, days := range touchedDays {
		for day := range days {
			n, err := rebuildDailyIndex(db, sym, day)
			if err != nil {
				return res, err
			}
			if n > 0 {
				res.DaysIndex++
			}
		}
	}
	// the rank list: Coinbase's
	for _, src := range rates.RankSources {
		body, ok := coinBodies[src]
		if !ok {
			continue
		}
		coins, err := rates.ParseCoins(src, body)
		if err != nil {
			res.Failed[src] = err.Error()
			continue
		}
		// what each coin is, for its page: Coinbase's description, colour, site and white paper
		for _, ci := range rates.ParseCoinInfo(body) {
			if err := db.Exec(`INSERT INTO coin_info (symbol, name, description, color, website, whitepaper, updated_at) VALUES ($1,$2,$3,$4,$5,$6,$7)
				ON CONFLICT (symbol) DO UPDATE SET name = EXCLUDED.name, description = EXCLUDED.description, color = EXCLUDED.color,
				website = EXCLUDED.website, whitepaper = EXCLUDED.whitepaper, updated_at = EXCLUDED.updated_at`,
				ci.Symbol, ci.Name, ci.Description, ci.Color, ci.Website, ci.WhitePaper, b.FetchedAt); err != nil {
				return res, err
			}
		}
		for _, c := range coins {
			if err := db.Exec(`INSERT INTO coin_ranks (ts, rank, coin_id, symbol, name, price_usd, market_cap, volume_24h, change_24h, source, supply)
				VALUES ($1,$2,$3,$4,$5,$6,$7,$8,$9,$10,$11) ON CONFLICT (ts, rank) DO UPDATE SET coin_id = EXCLUDED.coin_id, symbol = EXCLUDED.symbol, name = EXCLUDED.name,
				price_usd = EXCLUDED.price_usd, market_cap = EXCLUDED.market_cap, volume_24h = EXCLUDED.volume_24h, change_24h = EXCLUDED.change_24h, source = EXCLUDED.source,
				supply = EXCLUDED.supply`,
				b.FetchedAt, c.Rank, c.ID, c.Symbol, c.Name, c.PriceUSD, c.MarketCap, c.Volume24, c.Change24, src, c.Supply); err != nil {
				return res, err
			}
		}
		// the day's row per coin: the newest snapshot of the day wins
		day := fetched.UTC().Format("2006-01-02")
		for _, c := range coins {
			if err := db.Exec(`INSERT INTO coin_daily (day, symbol, coin_id, rank, price_usd, market_cap, volume_usd, source) VALUES ($1,$2,$3,$4,$5,$6,$7,$8)
				ON CONFLICT (day, symbol) DO UPDATE SET coin_id = EXCLUDED.coin_id, rank = EXCLUDED.rank, price_usd = EXCLUDED.price_usd, market_cap = EXCLUDED.market_cap, volume_usd = EXCLUDED.volume_usd, source = EXCLUDED.source`,
				day, strings.ToUpper(c.Symbol), c.ID, c.Rank, c.PriceUSD, c.MarketCap, c.Volume24, src); err != nil {
				return res, err
			}
		}
		res.Coins, res.CoinSource = len(coins), src
		items[src] = len(coins)
		break
	}
	// the fetch log: every source, good or not
	entries := make([]feedstat.Entry, 0, len(b.Sources))
	for _, s := range b.Sources {
		e := feedstat.Entry{Source: s.ID, Kind: feedstat.KindOf(s.ID), By: by, Status: s.Status, TookMs: s.TookMs, Bytes: len(s.Body), Items: items[s.ID], Error: res.Failed[s.ID]}
		e.OK = e.Error == ""
		entries = append(entries, e)
	}
	_ = feedstat.Log(db, fetched, entries)
	// what is kept
	_ = db.Exec("DELETE FROM crypto_quotes WHERE ts < $1", now.Add(-quotesKeep).Unix())
	_ = db.Exec("DELETE FROM crypto_index WHERE ts < $1", now.Add(-quotesKeep).Unix())
	_ = db.Exec("DELETE FROM coin_ranks WHERE ts < $1", now.Add(-ranksKeep).Unix())
	if len(res.Failed) == 0 {
		res.Failed = nil
	}
	if len(res.Index) == 0 {
		res.Index = nil
	}
	return res, nil
}

// PurgeOldRankLists takes the aggregators' rank lists the box used before Coinbase's off what it
// shows: their snapshots, today's rows of theirs and their fetch log. The days they wrote before
// stay (the market index's weights were averaged from them). Run at start; nothing to do after.
func PurgeOldRankLists(db *poltergres.ReadWrite, now time.Time) {
	_ = db.Exec("DELETE FROM coin_ranks WHERE source IN ('coingecko','coinpaprika')")
	_ = db.Exec("DELETE FROM coin_daily WHERE day >= $1 AND source IN ('coingecko','coinpaprika')", now.UTC().Format("2006-01-02"))
	_ = db.Exec("DELETE FROM fetch_log WHERE source IN ('coingecko','coinpaprika')")
	_ = db.Exec("DELETE FROM settings WHERE key IN ('fetch_coingecko','fetch_coinpaprika')")
}

// rebuildDailyIndex writes one day's USD close for a symbol from the venues' closes.
func rebuildDailyIndex(db *poltergres.ReadWrite, symbol, day string) (int, error) {
	rows, err := db.Query("SELECT exchange, quote, close FROM crypto_daily WHERE day = $1 AND symbol = $2", day, symbol)
	if err != nil {
		return 0, err
	}
	closes := map[rates.Market]float64{}
	for _, v := range rows.Vals {
		if len(v) < 3 || v[0] == nil || v[1] == nil {
			continue
		}
		c, _ := strconv.ParseFloat(deref(v[2]), 64)
		closes[rates.Market{Exchange: *v[0], Base: symbol, Quote: *v[1]}] = c
	}
	usdt := 0.0
	if symbol != "USDT" {
		usdt, _ = DailyClose(db, "USDT", day)
	}
	price, n := rates.DailyIndex(closes, symbol, usdt)
	if n == 0 {
		return 0, nil
	}
	return n, db.Exec("INSERT INTO crypto_daily_index (day, symbol, close, n) VALUES ($1,$2,$3,$4) ON CONFLICT (day, symbol) DO UPDATE SET close = EXCLUDED.close, n = EXCLUDED.n", day, symbol, price, n)
}

// insertQuotes writes a batch of quotes, two hundred rows a statement.
func insertQuotes(db *poltergres.ReadWrite, ts int64, qs []rates.Quote) error {
	const per = 200
	for i := 0; i < len(qs); i += per {
		end := i + per
		if end > len(qs) {
			end = len(qs)
		}
		var sb strings.Builder
		args := make([]any, 0, (end-i)*7)
		sb.WriteString("INSERT INTO crypto_quotes (ts, exchange, base, quote, price, volume, quote_ts) VALUES ")
		for j, q := range qs[i:end] {
			if j > 0 {
				sb.WriteString(",")
			}
			n := len(args)
			fmt.Fprintf(&sb, "($%d,$%d,$%d,$%d,$%d,$%d,$%d)", n+1, n+2, n+3, n+4, n+5, n+6, n+7)
			args = append(args, ts, q.Exchange, q.Base, q.QuoteCcy, q.Price, q.Volume, q.At.Unix())
		}
		sb.WriteString(" ON CONFLICT (ts, exchange, base, quote) DO UPDATE SET price = EXCLUDED.price, volume = EXCLUDED.volume, quote_ts = EXCLUDED.quote_ts")
		if err := db.Exec(sb.String(), args...); err != nil {
			return err
		}
	}
	return nil
}

// EURUSD is the ECB's dollars per euro on its newest day (0 when the box has no table yet).
func EURUSD(db *poltergres.ReadWrite) float64 {
	rows, err := db.Query("SELECT rate FROM fx_rates WHERE code = 'USD' ORDER BY day DESC LIMIT 1")
	if err != nil || len(rows.Vals) != 1 || rows.Vals[0][0] == nil {
		return 0
	}
	v, _ := strconv.ParseFloat(*rows.Vals[0][0], 64)
	return v
}

// LastPrices is each coin's newest index price from the last half hour before ts: the reference
// the blend's outlier rule measures against.
func LastPrices(db *poltergres.ReadWrite, ts int64) map[string]float64 {
	out := map[string]float64{}
	rows, err := db.Query(`SELECT DISTINCT ON (symbol) symbol, price FROM crypto_index WHERE ts >= $1 AND ts < $2 ORDER BY symbol, ts DESC`, ts-1800, ts)
	if err != nil {
		return out
	}
	for _, v := range rows.Vals {
		if len(v) == 2 && v[0] != nil && v[1] != nil {
			out[*v[0]], _ = strconv.ParseFloat(*v[1], 64)
		}
	}
	return out
}

// DailyClose is the box's daily USD close for a symbol on a day, from the venues' closes directly
// (so the USDT leg can be read before its own index row exists).
func DailyClose(db *poltergres.ReadWrite, symbol, day string) (float64, int) {
	rows, err := db.Query("SELECT exchange, quote, close FROM crypto_daily WHERE day = $1 AND symbol = $2", day, symbol)
	if err != nil {
		return 0, 0
	}
	closes := map[rates.Market]float64{}
	for _, v := range rows.Vals {
		if len(v) < 3 || v[0] == nil || v[1] == nil {
			continue
		}
		c, _ := strconv.ParseFloat(deref(v[2]), 64)
		closes[rates.Market{Exchange: *v[0], Base: symbol, Quote: *v[1]}] = c
	}
	return rates.DailyIndex(closes, symbol, 1)
}

// MarketsSeen is every USD and USDT (venue, base, quote) quoted in the last two days: the pairs the
// venues trade for the symbols followed, which is what the daily candles are fetched for.
func MarketsSeen(db *poltergres.ReadWrite, now time.Time) []rates.Market {
	// the candles fold USD and USDT markets (DailyIndex, FoldBar); the other quote currencies
	// convert in the live blend only
	rows, err := db.Query("SELECT DISTINCT exchange, base, quote FROM crypto_quotes WHERE ts >= $1 AND quote IN ('USD','USDT') ORDER BY base, exchange, quote", now.Add(-48*time.Hour).Unix())
	if err != nil {
		return nil
	}
	var out []rates.Market
	for _, v := range rows.Vals {
		if len(v) == 3 && v[0] != nil && v[1] != nil && v[2] != nil {
			out = append(out, rates.Market{Exchange: *v[0], Base: *v[1], Quote: *v[2]})
		}
	}
	return out
}

// OldestDay is the oldest day a market has a candle for ("" when none): where the backfill walks
// back from.
func OldestDay(db *poltergres.ReadWrite, m rates.Market) string {
	rows, err := db.Query("SELECT min(day) FROM crypto_daily WHERE symbol = $1 AND exchange = $2 AND quote = $3", m.Base, m.Exchange, m.Quote)
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return ""
	}
	return *rows.Vals[0][0]
}

// NewestDay is the newest day a market has a candle for ("" when none).
func NewestDay(db *poltergres.ReadWrite, m rates.Market) string {
	rows, err := db.Query("SELECT max(day) FROM crypto_daily WHERE symbol = $1 AND exchange = $2 AND quote = $3", m.Base, m.Exchange, m.Quote)
	if err != nil || len(rows.Vals) == 0 || rows.Vals[0][0] == nil {
		return ""
	}
	return *rows.Vals[0][0]
}

func deref(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

// String is the health line's rates part.
func (r RatesResult) String() string {
	parts := []string{}
	if r.FXDays > 0 {
		parts = append(parts, fmt.Sprintf("ECB %d day(s) to %s", r.FXDays, r.FXDay))
	}
	if len(r.Index) > 0 {
		// a count and the three everyone looks for, not every coin: the whole list is the ctl's
		// (ghost-cli ghost.tallyd rates) and the COIN pages'
		lo, hi, thin := 0, 0, 0
		for _, ix := range r.Index {
			if lo == 0 || ix.N < lo {
				lo = ix.N
			}
			if ix.N > hi {
				hi = ix.N
			}
			if ix.N < 2 {
				thin++
			}
		}
		line := fmt.Sprintf("%d coins blended from %d to %d venues each", len(r.Index), lo, hi)
		if thin > 0 {
			line += fmt.Sprintf(" (%d from one)", thin)
		}
		for _, s := range []string{"BTC", "ETH", "SOL"} {
			if ix, ok := r.Index[s]; ok {
				line += " · " + s + " " + priceWord(ix.Price)
			}
		}
		parts = append(parts, line)
	}
	if r.Candles > 0 {
		parts = append(parts, fmt.Sprintf("%d candles, %d days indexed", r.Candles, r.DaysIndex))
	}
	if r.Coins > 0 {
		parts = append(parts, fmt.Sprintf("%d coins (%s)", r.Coins, r.CoinSource))
	}
	if len(r.Failed) > 0 {
		parts = append(parts, fmt.Sprintf("%d failed", len(r.Failed)))
	}
	if len(parts) == 0 {
		return "nothing usable in the batch"
	}
	return strings.Join(parts, " · ")
}

// priceWord is a price as the status line says it: whole dollars with a thousands separator
// above a hundred, two decimals below.
func priceWord(p float64) string {
	if p < 100 {
		return fmt.Sprintf("%.2f", p)
	}
	n := int64(p + 0.5)
	s := fmt.Sprintf("%d", n)
	for i := len(s) - 3; i > 0; i -= 3 {
		s = s[:i] + "," + s[i:]
	}
	return s
}
