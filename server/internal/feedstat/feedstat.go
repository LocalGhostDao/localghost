// Package feedstat is the fetch log: one row per address fetched for the box (by ghost.tallyd, by
// ghost.synthd, or by the phone on Wi-Fi) and one per minute's tick, with who fetched it, how long
// it took, what came back and how much of it was of use. Box Status reads it back as success
// rates, latencies and runs of failures, per source or per exchange (internal/monitor). Two days
// are kept: enough to see a venue that fails every night, small enough to never matter.
package feedstat

import (
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// Querier is any connection that reads (secd's, a daemon's).
type Querier interface {
	Query(sql string, args ...any) (*poltergres.Rows, error)
}

// Keep is how long the log is kept.
const Keep = 48 * time.Hour

// The kinds of row.
const (
	KindTicker  = "ticker"  // a venue's prices, every minute (binance:all, coinbase:BTC-USD)
	KindECB     = "ecb"     // the ECB's tables
	KindRanks   = "ranks"   // the rank list (coinbase-ranks)
	KindDaily   = "daily"   // a market's daily candles (hist:kraken:BTC-USD)
	KindHistory = "history" // a page of hourly or minute candles (bars:1h:binance:BTC-USDT)
	KindNews    = "news"    // a news feed
	KindArticle = "article" // a news story's article page, read for its summary
	KindTick    = "tick"    // ghost.tallyd's minute, as a whole
	KindFast    = "fast"    // a minute of the fast lane (BTC, ETH, SOL every five seconds), as one line
	KindWeather = "weather" // a batch of the daily weather pull (open-meteo)
)

// Entry is one fetch.
type Entry struct {
	Source string
	Kind   string
	By     string // box or phone
	Status int    // the HTTP status, 0 when the fetch itself failed
	OK     bool   // it came and was of use
	TookMs int
	Bytes  int
	Items  int // quotes, days, candles, coins or new entries the answer gave
	Error  string
}

// KindOf is a rates source id's kind.
func KindOf(id string) string {
	switch {
	case strings.HasPrefix(id, "ecb"):
		return KindECB
	case strings.HasPrefix(id, "hist:"):
		return KindDaily
	case strings.HasPrefix(id, "bars:"):
		return KindHistory
	case id == "coinbase-ranks":
		return KindRanks
	case id == "open-meteo":
		return KindWeather
	}
	return KindTicker
}

// VenueOf is the exchange a source id belongs to ("binance:all", "coinbase:BTC-USD",
// "hist:kraken:BTC-USD", "bars:1h:okx:ETH-USDT"); other ids are their own venue.
func VenueOf(id string) string {
	parts := strings.Split(id, ":")
	switch {
	case parts[0] == "hist" && len(parts) > 1:
		return parts[1]
	case parts[0] == "bars" && len(parts) > 2:
		return parts[2]
	}
	return parts[0]
}

// venueSQL is VenueOf in SQL, so the grouping happens where the rows are.
const venueSQL = `CASE WHEN source LIKE 'hist:%' THEN split_part(source, ':', 2) WHEN source LIKE 'bars:%' THEN split_part(source, ':', 3) ELSE split_part(source, ':', 1) END`

// Log writes a batch's rows, fifty to a statement. Best effort: the log is for looking, and a row
// that did not land changes nothing the box does.
func Log(db *poltergres.ReadWrite, at time.Time, entries []Entry) error {
	const per = 50
	for i := 0; i < len(entries); i += per {
		end := i + per
		if end > len(entries) {
			end = len(entries)
		}
		var sb strings.Builder
		sb.WriteString("INSERT INTO fetch_log (ts, source, kind, by_who, status, ok, took_ms, bytes, items, error) VALUES ")
		args := make([]any, 0, (end-i)*10)
		for j, e := range entries[i:end] {
			if j > 0 {
				sb.WriteByte(',')
			}
			b := j * 10
			sb.WriteString("(")
			for k := 1; k <= 10; k++ {
				if k > 1 {
					sb.WriteByte(',')
				}
				sb.WriteString("$" + strconv.Itoa(b+k))
			}
			sb.WriteString(")")
			args = append(args, at.Unix(), e.Source, e.Kind, e.By, e.Status, e.OK, e.TookMs, e.Bytes, e.Items, clip(e.Error, 200))
		}
		if err := db.Exec(sb.String(), args...); err != nil {
			return err
		}
	}
	return nil
}

// Prune drops what is older than Keep.
func Prune(db *poltergres.ReadWrite, now time.Time) {
	_ = db.Exec("DELETE FROM fetch_log WHERE ts < $1", now.Add(-Keep).Unix())
}

// Stat is one source's (or one exchange's) record: over the window asked for, and over the whole
// log for the last good fetch and the run of failures since.
type Stat struct {
	Key         string `json:"key"`
	Calls       int    `json:"calls"`
	OK          int    `json:"ok"`
	P50Ms       int    `json:"p50Ms"`
	P95Ms       int    `json:"p95Ms"`
	MaxMs       int    `json:"maxMs"`
	Bytes       int64  `json:"bytes"`
	Items       int    `json:"items"`
	LastAt      int64  `json:"lastAt"`
	LastOKAt    int64  `json:"lastOkAt"`
	FailsInRow  int    `json:"failsInRow"` // fetches failed since the last good one (a minute's calls count once)
	LastOK      bool   `json:"lastOk"`
	LastItems   int    `json:"lastItems"`
	LastBy      string `json:"lastBy,omitempty"`
	LastError   string `json:"lastError,omitempty"`
	LastErrorAt int64  `json:"lastErrorAt,omitempty"`
}

// Rate is the share of the window's fetches that came good (1 when there were none: nothing
// failed).
func (s Stat) Rate() float64 {
	if s.Calls == 0 {
		return 1
	}
	return float64(s.OK) / float64(s.Calls)
}

// Stats reads the log for one kind, grouped by source or by exchange, the window's numbers
// from since on.
func Stats(db Querier, kind string, since time.Time, byVenue bool) ([]Stat, error) {
	k := "source"
	if byVenue {
		k = venueSQL
	}
	out := map[string]*Stat{}
	var order []string
	get := func(key string) *Stat {
		if s, ok := out[key]; ok {
			return s
		}
		s := &Stat{Key: key}
		out[key] = s
		order = append(order, key)
		return s
	}
	// the whole log: the newest fetch, the newest good one, the failures since, the newest error
	rows, err := db.Query(`WITH x AS (SELECT `+k+` AS k, id, ts, ok, status, error, items, by_who FROM fetch_log WHERE kind = $1),
		lo AS (SELECT k, coalesce(max(ts) FILTER (WHERE ok), 0) AS last_ok, max(ts) AS last_at FROM x GROUP BY k),
		fr AS (SELECT x.k, count(DISTINCT x.ts) FILTER (WHERE NOT x.ok AND x.ts > lo.last_ok) AS fails FROM x JOIN lo USING (k) GROUP BY x.k),
		lt AS (SELECT x.k, coalesce(sum(x.items) FILTER (WHERE x.ok), 0) AS items, bool_or(x.ok) AS ok, string_agg(DISTINCT x.by_who, ',') AS by_who
			FROM x JOIN lo USING (k) WHERE x.ts = lo.last_at GROUP BY x.k),
		le AS (SELECT DISTINCT ON (k) k, ts, error, status FROM x WHERE NOT ok ORDER BY k, ts DESC, id DESC)
		SELECT lo.k, lo.last_at, lo.last_ok, fr.fails, lt.items, lt.ok, lt.by_who, coalesce(le.ts, 0), coalesce(le.error, ''), coalesce(le.status, 0)
		FROM lo JOIN fr USING (k) JOIN lt USING (k) LEFT JOIN le USING (k) ORDER BY lo.k`, kind)
	if err != nil {
		return nil, err
	}
	for _, v := range rows.Vals {
		if len(v) < 10 || v[0] == nil {
			continue
		}
		s := get(*v[0])
		s.LastAt = i64(v[1])
		s.LastOKAt = i64(v[2])
		s.FailsInRow = int(i64(v[3]))
		s.LastItems = int(i64(v[4]))
		s.LastOK = str(v[5]) == "t" || str(v[5]) == "true"
		s.LastBy = str(v[6])
		s.LastErrorAt = i64(v[7])
		s.LastError = str(v[8])
		if s.LastError == "" && s.LastErrorAt > 0 {
			if st := i64(v[9]); st > 0 {
				s.LastError = "HTTP " + strconv.FormatInt(st, 10)
			}
		}
	}
	// the window: how many, how many good, how long
	rows, err = db.Query(`SELECT k, count(*), count(*) FILTER (WHERE ok),
		coalesce(percentile_cont(0.5) WITHIN GROUP (ORDER BY took_ms) FILTER (WHERE took_ms > 0), 0)::bigint,
		coalesce(percentile_cont(0.95) WITHIN GROUP (ORDER BY took_ms) FILTER (WHERE took_ms > 0), 0)::bigint,
		coalesce(max(took_ms), 0), coalesce(sum(bytes), 0), coalesce(sum(items) FILTER (WHERE ok), 0)
		FROM (SELECT `+k+` AS k, ok, took_ms, bytes, items FROM fetch_log WHERE kind = $1 AND ts >= $2) w GROUP BY k ORDER BY k`, kind, since.Unix())
	if err != nil {
		return nil, err
	}
	for _, v := range rows.Vals {
		if len(v) < 8 || v[0] == nil {
			continue
		}
		s := get(*v[0])
		s.Calls = int(i64(v[1]))
		s.OK = int(i64(v[2]))
		s.P50Ms = int(i64(v[3]))
		s.P95Ms = int(i64(v[4]))
		s.MaxMs = int(i64(v[5]))
		s.Bytes = i64(v[6])
		s.Items = int(i64(v[7]))
	}
	res := make([]Stat, 0, len(order))
	for _, key := range order {
		res = append(res, *out[key])
	}
	return res, nil
}

// Count is how many rows of a kind landed since a time (the history's pace).
func Count(db Querier, kind string, since time.Time) int {
	rows, err := db.Query("SELECT count(*) FROM fetch_log WHERE kind = $1 AND ts >= $2", kind, since.Unix())
	if err != nil || len(rows.Vals) != 1 {
		return 0
	}
	return int(i64(rows.Vals[0][0]))
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

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
