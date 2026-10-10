package main

// THE BOX'S OWN MARKET NUMBERS in a chat answer (rates.go beside news.go): the ECB's table, the
// BTC index and the rank list ghost.tallyd keeps from what the phone fetched. A money question
// ("100 euros in pounds", "what is bitcoin at", "gbp to ron") gets the box's numbers in the
// prompt, offline, with the day and the source named; an amount with two currencies is converted
// here so the model never does the arithmetic.

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/rates"
	"github.com/LocalGhostDao/localghost/server/internal/tally"
)

var (
	currencyWords = map[string]string{
		"eur": "EUR", "euro": "EUR", "euros": "EUR", "€": "EUR",
		"usd": "USD", "dollar": "USD", "dollars": "USD", "$": "USD", "bucks": "USD",
		"gbp": "GBP", "pound": "GBP", "pounds": "GBP", "£": "GBP", "quid": "GBP", "sterling": "GBP",
		"ron": "RON", "lei": "RON", "leu": "RON",
		"chf": "CHF", "franc": "CHF", "francs": "CHF",
		"jpy": "JPY", "yen": "JPY",
		"sek": "SEK", "nok": "NOK", "dkk": "DKK", "pln": "PLN", "zloty": "PLN", "czk": "CZK", "huf": "HUF", "forint": "HUF",
		"bgn": "BGN", "lev": "BGN", "try": "TRY", "lira": "TRY", "cad": "CAD", "aud": "AUD", "nzd": "NZD",
		"cny": "CNY", "yuan": "CNY", "inr": "INR", "rupee": "INR", "rupees": "INR", "krw": "KRW",
		"brl": "BRL", "reais": "BRL", "mxn": "MXN", "peso": "MXN", "pesos": "MXN", "zar": "ZAR",
		"hkd": "HKD", "sgd": "SGD", "thb": "THB", "baht": "THB", "idr": "IDR", "rupiah": "IDR", "ils": "ILS", "shekel": "ILS",
		"btc": "BTC", "xbt": "BTC", "bitcoin": "BTC", "bitcoins": "BTC",
		"eth": "ETH", "ether": "ETH", "ethereum": "ETH", "usdt": "USDT", "tether": "USDT",
		"sol": "SOL", "solana": "SOL", "xrp": "XRP", "ada": "ADA", "doge": "DOGE", "dogecoin": "DOGE",
	}
	moneyHint = regexp.MustCompile(`(?i)\b(exchange rate|rate of|convert|how much is|worth|price of|bitcoin|btc|ethereum|eth|crypto|market cap|top (10|20|50|100) coins?|in (pounds|euros|dollars|lei|yen|francs))\b|[€$£]`)
	// the market as a whole: "how is crypto doing", "is the market up", "crypto today"
	marketHint = regexp.MustCompile(`(?i)\b(crypto|the market|markets|coins?)\b.*\b(doing|up|down|today|this week|overall|mood|how)\b|\b(how|what).{0,20}\b(crypto|the market)\b`)
	amountRe   = regexp.MustCompile(`(?i)(?:^|[\s€$£])(\d[\d,]*(?:\.\d+)?)\s*(k|m|thousand|million)?`)
)

// moneyQuestion reads the currencies and the amount out of a question.
func moneyQuestion(prompt string) (codes []string, amount float64, ok bool) {
	low := strings.ToLower(prompt)
	seen := map[string]bool{}
	for _, w := range strings.FieldsFunc(low, func(r rune) bool {
		return r == ' ' || r == ',' || r == '?' || r == '.' || r == '!' || r == '(' || r == ')' || r == ':'
	}) {
		if c, hit := currencyWords[w]; hit && !seen[c] {
			seen[c] = true
			codes = append(codes, c)
		}
	}
	for _, sym := range []string{"€", "$", "£"} {
		if strings.Contains(low, sym) && !seen[currencyWords[sym]] {
			seen[currencyWords[sym]] = true
			codes = append(codes, currencyWords[sym])
		}
	}
	if m := amountRe.FindStringSubmatch(prompt); m != nil {
		amount, _ = strconv.ParseFloat(strings.ReplaceAll(m[1], ",", ""), 64)
		switch strings.ToLower(m[2]) {
		case "k", "thousand":
			amount *= 1000
		case "m", "million":
			amount *= 1e6
		}
	}
	ok = len(codes) > 0 && (len(codes) >= 2 || moneyHint.MatchString(prompt))
	return codes, amount, ok
}

// marketQuestion says whether the question is about crypto as a whole.
func marketQuestion(prompt string) bool { return marketHint.MatchString(prompt) }

// ratesSource is the chat's money source: nothing unless the question is about money, or about
// crypto as a whole (then the market index).
func ratesSource(runDir, prompt string) []ctxItem {
	codes, amount, ok := moneyQuestion(prompt)
	market := marketQuestion(prompt)
	top := topHint.MatchString(prompt)
	db := chatStore(filepath.Dir(runDir))
	if db == nil {
		return nil
	}
	// any coin the box follows, by symbol or name ("chainlink price", "how is AVAX doing")
	if coins := coinsIn(prompt, knownCoins(db, time.Now())); len(coins) > 0 && (ok || priceHint.MatchString(prompt) || len(strings.Fields(prompt)) <= 3) {
		have := map[string]bool{}
		for _, c := range codes {
			have[c] = true
		}
		for _, c := range coins {
			if !have[c] {
				codes = append(codes, c)
			}
		}
		ok = true
	}
	if !ok && !market && !top {
		return nil
	}
	var out []ctxItem
	if market {
		if st, err := tally.MarketNow(db, time.Now()); err == nil && st.Value > 0 {
			out = append(out, marketItem(st))
		}
	}
	if ok || top {
		snap, err := hw.RatesNow(db)
		if err != nil {
			return out
		}
		if top {
			if it, has := topItem(snap, topCount(prompt), time.Now()); has {
				out = append(out, it)
			}
		}
		if ok {
			out = append(out, ratesItems(snap, codes, amount, time.Now())...)
			// a look back ("compared to a few days ago", "last week", "trend"): the daily
			// closes of the box's own index for the coins asked, so the comparison is made
			// from the box's numbers and not from the model's memory of a price
			if lookBack.MatchString(prompt) {
				for _, c := range codes {
					if _, has := snap.Index[c]; !has {
						continue // a fiat code: its ECB line above is the rate of the day
					}
					if days, err := hw.RatesHistory(db, c, 8); err == nil {
						if it, has := historyItem(c, days); has {
							out = append(out, it)
						}
					}
				}
			}
		}
	}
	return out
}

// lookBack is a question that compares with a time before now.
var lookBack = regexp.MustCompile(`(?i)\b(ago|yesterday|last (week|month|night)|this (week|month)|past (few|\d+) (days|weeks)|compared?|since|trend|over the (week|month|last)|week|month|days)\b`)

// historyItem is the daily closes of the box's index for one symbol as one prompt line, oldest
// first (pure, for the tests): "BTC daily closes (USD, the box's index): 2026-10-03 112,100 · …".
func historyItem(sym string, days []hw.DayPrice) (ctxItem, bool) {
	var parts []string
	for i := len(days) - 1; i >= 0; i-- {
		if days[i].Close > 0 {
			parts = append(parts, days[i].Day+" "+money(days[i].Close))
		}
	}
	if len(parts) < 2 {
		return ctxItem{}, false
	}
	return ctxItem{When: days[0].Day, Source: "rates",
		Snippet: sym + " daily closes (USD, the box's index): " + strings.Join(parts, " · "),
		Why:     "the box's own daily " + sym + " index, for the comparison asked"}, true
}

// marketItem is the market index's line (pure, for the tests).
func marketItem(st tally.MarketState) ctxItem {
	var top []string
	for _, c := range st.Top {
		top = append(top, fmt.Sprintf("%s %.0f%%", c.Symbol, 100*c.Weight))
	}
	return ctxItem{When: st.Day, Source: "rates",
		Snippet: fmt.Sprintf("the box's crypto index %s: %.1f, %+.2f%% against yesterday's close (%d constituents, the largest by market cap, weighted by last month's volume: %s; %d priced live)",
			st.Code, st.Value, st.DayChange, st.Constituents, strings.Join(top, ", "), st.Priced),
		Why: "one number for crypto as a whole, made on the box from the venues' prices"}
}

// ratesItems is the prompt lines for a question (pure, for the tests).
func ratesItems(snap hw.RatesSnapshot, codes []string, amount float64, now time.Time) []ctxItem {
	var out []ctxItem
	if line := snap.FXLine(codes...); line != "" {
		out = append(out, ctxItem{When: snap.FXDay, Source: "rates", Snippet: line, Why: "the ECB's daily reference rates, kept on the box"})
	}
	// the symbols asked for, else bitcoin when the question has one currency or none
	var want []string
	for _, c := range codes {
		if _, ok := snap.Index[c]; ok {
			want = append(want, c)
		}
	}
	if len(want) == 0 && len(codes) < 2 {
		if _, ok := snap.Index["BTC"]; ok {
			want = []string{"BTC"}
		}
	}
	for _, sym := range want {
		r := snap.Index[sym]
		if r.Price <= 0 {
			continue
		}
		age := now.Sub(time.Unix(r.At, 0)).Truncate(time.Minute)
		change := ""
		if r.HasChange {
			change = fmt.Sprintf(", %+.2f%% in 24 h", r.Change24)
		}
		out = append(out, ctxItem{When: time.Unix(r.At, 0).UTC().Format("2006-01-02"), Source: "rates",
			Snippet: fmt.Sprintf("%s %s USD%s , the box's average of %d exchanges (%s), spread %.2f%%, %s ago", sym, money(r.Price), change, r.N, r.Used, 100*r.Spread, age),
			Why:     "the box's own " + sym + " index from the public tickers (USDT markets folded into dollars)"})
	}
	if amount > 0 && len(codes) >= 2 {
		if v, err := rates.Convert(amount, codes[0], codes[1], snap.FX, snap.USD()); err == nil {
			out = append(out, ctxItem{When: snap.FXDay, Source: "rates",
				Snippet: fmt.Sprintf("%s %s = %s %s at these rates", money(amount), codes[0], money(v), codes[1]),
				Why:     "converted on the box with the rates above"})
		}
	}
	if len(out) == 0 {
		out = append(out, ctxItem{Source: "rates", Snippet: "the box has no rates yet (fetched hourly, by the phone on Wi-Fi or by the box); say so rather than guess a number", Why: "a money question with no rates on the box"})
	}
	return out
}

// money writes a number the way a person reads it: thousands separated, two decimals under a
// thousand, none above, eight for a bitcoin fraction.
func money(v float64) string {
	switch {
	case v == 0:
		return "0"
	case v < 0.01:
		return strconv.FormatFloat(v, 'f', 8, 64)
	case v < 1000:
		return strconv.FormatFloat(v, 'f', 2, 64)
	}
	s := strconv.FormatFloat(v, 'f', 0, 64)
	var b strings.Builder
	for i, r := range s {
		if i > 0 && (len(s)-i)%3 == 0 {
			b.WriteByte(',')
		}
		b.WriteRune(r)
	}
	return b.String()
}
