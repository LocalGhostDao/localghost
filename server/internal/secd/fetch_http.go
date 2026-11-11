package secd

// WHAT THE PHONE FETCHES FOR THE BOX, and what comes back. The box opens no connection, so the
// news feeds and the market tickers are fetched by the phone, the way it runs the web search
// round: /v1/fetch/list says what to fetch (the box owns the list), /v1/news/fetched and
// /v1/rates/fetched take the raw bodies and spool them whole for ghost.synthd and ghost.tallyd,
// and /v1/news and /v1/rates read back what the daemons made of them.

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
	"github.com/LocalGhostDao/localghost/server/internal/egress"
	"github.com/LocalGhostDao/localghost/server/internal/feeds"
	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/monitor"
	"github.com/LocalGhostDao/localghost/server/internal/rates"
	"github.com/LocalGhostDao/localghost/server/internal/tally"
)

// fetchListDoc is what the phone fetches.
type fetchListDoc struct {
	Feeds []feeds.Source `json:"feeds"`
	Rates []rates.Source `json:"rates"`
	// FeedsEvery is how often the feeds want fetching, in minutes; the tickers carry their own.
	FeedsEvery int `json:"feedsEvery"`
}

// newsDoc is /v1/news (hw.NewsDoc): the stories, the marks for the screen's header, the brief.
type newsDoc = hw.NewsDoc

func (s *Server) mountedSlot() (int, bool) {
	s.mu.Lock()
	mounted := s.mounted
	s.mu.Unlock()
	return mounted, mounted >= 0
}

// handlePhoneNet , POST /v1/phone/net {"net":"wifi"|"mobile"|"none"} , the phone's word on its
// network, every quarter hour with its notification poll. While it says Wi-Fi the phone fetches
// the feeds and the tickers for the box (the box opens no connection); on mobile data, or when it
// falls silent, ghost.tallyd and ghost.synthd fetch for themselves (internal/egress).
func (s *Server) handlePhoneNet(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	var q struct {
		Net string `json:"net"`
	}
	if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1024)).Decode(&q) != nil {
		http.Error(w, "{\"net\":\"wifi|mobile|none\"}", http.StatusBadRequest)
		return
	}
	net, ok := egress.ParseNet(q.Net)
	if !ok {
		http.Error(w, "net is wifi, mobile or none", http.StatusBadRequest)
		return
	}
	if err := s.notif.SetSetting(mounted, "phone_net", net); err != nil {
		s.appearsDown(w)
		return
	}
	_ = s.notif.SetSetting(mounted, "phone_seen", egress.SeenString(time.Now()))
	w.Header().Set("Content-Type", "application/json")
	// the phone learns who fetches, so SETTINGS can say it
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "proxy": net == "wifi"})
}

// handleFetchList , GET /v1/fetch/list.
func (s *Server) handleFetchList(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	// the phone's part of the rates: the ECB and the rank lists (the tickers are the box's own,
	// every minute, whatever the phone is on)
	doc := fetchListDoc{Rates: rates.PhoneSources(), FeedsEvery: int(feeds.FetchEvery / time.Minute)}
	if db, err := s.notif.DB(mounted); err == nil {
		if fl, err := hw.FeedList(db); err == nil {
			doc.Feeds = fl
		}
	}
	if doc.Feeds == nil {
		doc.Feeds = feeds.DefaultSources()
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(doc)
}

// spool writes a body whole under dir and names it into place, owned by the daemons' user. Every
// step is beneath the volume and by descriptor or Lchown (vaultfs.go): the directory is the
// daemons' own and may be anything by the time this runs.
func (s *Server) spool(dir, prefix string, body []byte) error {
	uid, gid := s.spoolCred()
	st := s.cfg.StateDir
	if err := vaultMkdirAll(st, dir, 0o750, uid, gid); err != nil {
		return err
	}
	path := filepath.Join(dir, fmt.Sprintf("%s-%d.json", prefix, time.Now().UnixNano()))
	if err := vaultWrite(st, path+".part", body, 0o640, uid, gid); err != nil {
		return err
	}
	if err := vaultRename(st, path+".part", path); err != nil {
		vaultRemove(st, path+".part")
		return err
	}
	return nil
}

// handleNewsFetched , POST /v1/news/fetched , the feeds' bytes, as fetched, for ghost.synthd.
func (s *Server) handleNewsFetched(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 16<<20)) // a dozen feeds at a few hundred KB each
	if err != nil || len(body) == 0 {
		s.appearsDown(w)
		return
	}
	var b struct {
		FetchedAt int64 `json:"fetchedAt"`
		Feeds     []struct {
			ID string `json:"id"`
		} `json:"feeds"`
	}
	if json.Unmarshal(body, &b) != nil || len(b.Feeds) == 0 {
		http.Error(w, "a news batch has feeds", http.StatusBadRequest)
		return
	}
	for _, f := range b.Feeds {
		if f.ID == "" || len(f.ID) > 40 {
			http.Error(w, "every feed has an id", http.StatusBadRequest)
			return
		}
	}
	if err := s.spool(filepath.Join(s.cfg.StateDir, "mnt", fmt.Sprintf("slot%d", mounted), "synthd", "news", "inbox"), "news", body); err != nil {
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "feeds": len(b.Feeds)})
}

// handleRatesFetched , POST /v1/rates/fetched , the tickers' bodies for ghost.tallyd.
func (s *Server) handleRatesFetched(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, 4<<20))
	if err != nil || len(body) == 0 {
		s.appearsDown(w)
		return
	}
	b, ok := tally.ParseRates(body)
	if !ok {
		http.Error(w, "a rates batch has sources", http.StatusBadRequest)
		return
	}
	if err := s.spool(filepath.Join(s.cfg.StateDir, "mnt", fmt.Sprintf("slot%d", mounted), "tallyd", "rates"), "rates", body); err != nil {
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"ok": true, "sources": len(b.Sources)})
}

// handleNews , GET /v1/news?since=<unix>&limit=N , the stories for the NEWS screen and home. The
// last two days come from Redis (synthd rewrites the copy when a story, summary or brief changes);
// a miss, an older since or a limit of its own reads Postgres.
func (s *Server) handleNews(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	now := time.Now()
	window := now.Add(-hw.NewsHotDays * 24 * time.Hour).Unix()
	since, _ := strconv.ParseInt(r.URL.Query().Get("since"), 10, 64)
	if since <= 0 {
		since = window
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	hot := limit <= 0 && since >= window-3600 // the copy's two days, give or take its age
	rd, rerr := s.notif.Cache(mounted)
	if hot && rerr == nil {
		var d hw.NewsDoc
		if hw.HotGet(rd, hw.HotNews, &d) && d.Since <= since {
			writeJSON(w, d.Within(since))
			return
		}
	}
	db, err := s.notif.DB(mounted)
	if err != nil {
		s.appearsDown(w)
		return
	}
	from := since
	if hot {
		from = window
	}
	d, err := hw.NewsDocNow(db, from, limit, now.Unix())
	if err != nil {
		secdLog.Warn("news read failed", "fn", "handleNews", "err", err)
		s.appearsDown(w)
		return
	}
	if hot && rerr == nil {
		_ = hw.HotPut(rd, hw.HotNews, d, time.Minute) // synthd's own copy lasts longer
	}
	writeJSON(w, d.Within(since))
}

// handleRatesHistory , GET /v1/rates/history?code=BTC&days=N , a symbol's daily USD closes (the
// box's daily index) or a currency's daily ECB rate, newest first.
func (s *Server) handleRatesHistory(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	code := r.URL.Query().Get("code")
	if code == "" || len(code) > 8 {
		s.appearsDown(w)
		return
	}
	db, err := s.notif.DB(mounted)
	if err != nil {
		s.appearsDown(w)
		return
	}
	days, _ := strconv.Atoi(r.URL.Query().Get("days"))
	var hist []hw.DayPrice
	if strings.EqualFold(code, rates.MarketIndexCode) {
		mh, err := tally.MarketHistory(db, days)
		if err != nil {
			s.appearsDown(w)
			return
		}
		for _, d := range mh {
			hist = append(hist, hw.DayPrice{Day: d.Day, Close: d.Value})
		}
	} else {
		var err error
		hist, err = hw.RatesHistory(db, code, days)
		if err != nil {
			s.appearsDown(w)
			return
		}
	}
	if hist == nil {
		hist = []hw.DayPrice{}
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": strings.ToUpper(code), "days": hist})
}

// handleRatesSeries , GET /v1/rates/series?code=BTC|CRYPTO50&res=1m|1h&hours=N , a symbol's (or
// the market index's) price every minute (the last week) or every hour (the last thirty days),
// oldest first: {ts, o, h, l, c, n, src}. src says how each point was made: live (the box's
// minute), minutes (an hour rolled up from them), venues (folded from the venues' candles, before
// the box was watching).
func (s *Server) handleRatesSeries(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	q := r.URL.Query()
	code, res := q.Get("code"), q.Get("res")
	if res == "" {
		res = tally.ResHour
	}
	ri, ok := tally.Resolutions[res]
	if code == "" || len(code) > 10 || !ok {
		http.Error(w, "code=BTC|CRYPTO50 and res=1m|1h", http.StatusBadRequest)
		return
	}
	hours, _ := strconv.Atoi(q.Get("hours"))
	span := time.Duration(hours) * time.Hour
	if span <= 0 {
		span = 24 * time.Hour
		if res == tally.ResHour {
			span = ri.Window
		}
	}
	if span > ri.Keep {
		span = ri.Keep
	}
	db, err := s.notif.DB(mounted)
	if err != nil {
		s.appearsDown(w)
		return
	}
	now := time.Now()
	pts, err := tally.Series(db, code, res, now.Add(-span), now)
	if err != nil {
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{"code": strings.ToUpper(code), "res": res, "points": pts})
}

// handleRates , GET /v1/rates , the box's market numbers as they stand.
func (s *Server) handleRates(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	now := time.Now()
	rd, rerr := s.notif.Cache(mounted)
	var d hw.RatesDoc
	if rerr != nil || !hw.HotGet(rd, hw.HotRates, &d) {
		db, err := s.notif.DB(mounted)
		if err != nil {
			s.appearsDown(w)
			return
		}
		if d, err = hw.RatesDocNow(db, now); err != nil {
			secdLog.Warn("rates read failed", "fn", "handleRates", "err", err)
			s.appearsDown(w)
			return
		}
		if rerr == nil {
			_ = hw.HotPut(rd, hw.HotRates, d, time.Minute) // tallyd rewrites it on its minute
		}
	}
	if d.Index == nil {
		d.Index = map[string]hw.IndexRow{}
	}
	if d.Ranks == nil {
		d.Ranks = []hw.CoinRow{}
	}
	// BTC, ETH and SOL as of the last five seconds, over the minute's index
	var f hw.Fast
	if rerr == nil && hw.HotGet(rd, hw.HotFast, &f) {
		hw.ApplyFast(&d.RatesSnapshot, f, now)
	}
	writeJSON(w, d)
}

// handleCoinInfo , GET /v1/coins/info?symbol=BTC , one coin's page: what it is, where it stands
// on the list, the box's price and how it was blended, market by market.
func (s *Server) handleCoinInfo(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	sym := strings.ToUpper(strings.TrimSpace(r.URL.Query().Get("symbol")))
	if sym == "" || len(sym) > 12 {
		http.Error(w, "symbol=BTC", http.StatusBadRequest)
		return
	}
	db, err := s.notif.DB(mounted)
	if err != nil {
		s.appearsDown(w)
		return
	}
	d, err := hw.CoinNow(db, sym, time.Now())
	if err != nil {
		secdLog.Warn("coin read failed", "fn", "handleCoinInfo", "err", err)
		s.appearsDown(w)
		return
	}
	writeJSON(w, d)
}

// sparksDoc is /v1/rates/sparks.
type sparksDoc struct {
	Hours  int                  `json:"hours"`
	Sparks map[string][]float64 `json:"sparks"`
}

// handleRatesSparks , GET /v1/rates/sparks , a week of hourly closes for every coin, for CRYPTO's
// rows; from Redis for ten minutes at a time.
func (s *Server) handleRatesSparks(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	rd, rerr := s.notif.Cache(mounted)
	var d sparksDoc
	if rerr == nil && hw.HotGet(rd, hw.HotSpark, &d) {
		writeJSON(w, d)
		return
	}
	db, err := s.notif.DB(mounted)
	if err != nil {
		s.appearsDown(w)
		return
	}
	sp, err := hw.Sparks(db, 24*7, time.Now())
	if err != nil {
		s.appearsDown(w)
		return
	}
	d = sparksDoc{Hours: 24 * 7, Sparks: sp}
	if rerr == nil {
		_ = hw.HotPut(rd, hw.HotSpark, d, hw.HotSparkTTL)
	}
	writeJSON(w, d)
}

// briefNowDoc is what "write the brief now" did: written, or why not, and the brief as it stands.
type briefNowDoc struct {
	OK           bool    `json:"ok"`
	Written      bool    `json:"written"`
	Why          string  `json:"why"`
	Brief        string  `json:"brief"`
	BriefAt      int64   `json:"briefAt"`
	BriefStories []int64 `json:"briefStories"`
}

// handleNewsBrief , POST /v1/news/brief , the day's brief written now, whatever its age (home's
// button): synthd asks the model from the day's summaries and says why when it cannot (fewer than
// two summaries, the model on the CPU, an answer that did not hold to the stories). Up to two
// minutes; the brief as it stands comes back either way.
func (s *Server) handleNewsBrief(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodPost {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	out := briefNowDoc{BriefStories: []int64{}}
	runDir := fmt.Sprintf("%s/mnt/slot%d/run", s.cfg.StateDir, mounted)
	resp, err := ctlsock.NewClientTimeout("ghost.synthd", runDir, 3*time.Minute).Call("news", map[string]any{"brief": true})
	switch {
	case err != nil:
		out.Why = "ghost.synthd did not answer"
	case !resp.OK:
		out.Why = resp.Err
	default:
		var d struct {
			Written bool   `json:"briefWritten"`
			Why     string `json:"briefWhy"`
		}
		_ = json.Unmarshal(resp.Data, &d)
		out.OK, out.Written, out.Why = true, d.Written, d.Why
	}
	if db, derr := s.notif.DB(mounted); derr == nil {
		out.Brief, out.BriefAt, out.BriefStories = hw.NewsBrief(db)
		if out.BriefStories == nil {
			out.BriefStories = []int64{}
		}
	}
	writeJSON(w, out)
}

// fastDoc is /v1/rates/fast: the fast coins as the index rows /v1/rates gives, seconds old.
type fastDoc struct {
	At    int64                  `json:"at"` // unix ms of the fast pass; 0 when the lane is quiet
	Index map[string]hw.IndexRow `json:"index"`
}

// handleRatesFast , GET /v1/rates/fast , BTC, ETH and SOL every five seconds (tallyd's fast lane),
// each with its 24-hour change; Redis only, so home can ask every five seconds while it is open.
func (s *Server) handleRatesFast(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	out := fastDoc{Index: map[string]hw.IndexRow{}}
	rd, err := s.notif.Cache(mounted)
	if err != nil {
		writeJSON(w, out)
		return
	}
	var d hw.RatesDoc
	var f hw.Fast
	if !hw.HotGet(rd, hw.HotFast, &f) {
		writeJSON(w, out)
		return
	}
	if !hw.HotGet(rd, hw.HotRates, &d) || d.Index == nil {
		d.Index = map[string]hw.IndexRow{}
	}
	snap := d.RatesSnapshot
	hw.ApplyFast(&snap, f, time.Now())
	out.At = f.At
	for _, sym := range hw.FastSymbols {
		if row, ok := snap.Index[sym]; ok && row.Fast {
			out.Index[sym] = row
		}
	}
	writeJSON(w, out)
}

// handleFeedsStatus , GET /v1/feeds/status , how the data the box pulls in is doing: the prices
// every minute, the exchanges, the history, the market index, the ECB, the rank lists, the daily
// candles and the news (internal/monitor). Box Status polls it while it is open.
func (s *Server) handleFeedsStatus(w http.ResponseWriter, r *http.Request) {
	if !s.session.Valid(bearer(r)) || r.Method != http.MethodGet {
		s.appearsDown(w)
		return
	}
	mounted, ok := s.mountedSlot()
	if !ok {
		s.appearsDown(w)
		return
	}
	db, err := s.notif.DB(mounted)
	if err != nil {
		s.appearsDown(w)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Cache-Control", "no-store")
	_ = json.NewEncoder(w).Encode(monitor.Make(db, time.Now()))
}
