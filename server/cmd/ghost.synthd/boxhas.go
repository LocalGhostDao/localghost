package main

// WHAT THE BOX ALREADY KNOWS, so the phone does not search the web for it. The box keeps the price
// of every coin it follows (a minute old, from seven exchanges), Coinbase's rank list, the market
// index, the ECB's table and the day's news. A question about any of those is answered from them,
// offline and fresher than a search page: the plan says so before the model is asked (search
// false, boxHas the reason), and the chat's context sources (rates.go, news.go) put the numbers in.

import (
	"fmt"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
	"github.com/LocalGhostDao/localghost/server/internal/tally"
)

var (
	// a question about a price: the words around a coin's name that make it one
	priceHint = regexp.MustCompile(`(?i)\b(price|prices|priced|worth|cost|costs|trading|value|how much|doing|up|down|today|now|chart|ath|all.time high|market cap|mcap|usd|dollars?|euros?|pounds?|sell|buy)\b|[$€£]`)
	// the top of the market: "top 50 coins", "biggest cryptos", "crypto rankings"
	topHint = regexp.MustCompile(`(?i)\b(top|biggest|largest|leading)\s+(\d{1,3}\s+)?(coins?|cryptos?|crypto ?currenc(y|ies)|tokens?|altcoins?)\b|\bcrypto\s+(rank(ing)?s?|list|leaderboard)\b`)
	topN    = regexp.MustCompile(`(?i)\btop\s+(\d{1,3})\b`)
	// the day's headlines, with no topic of their own ("what's the news", "today's headlines");
	// "news about the strike in France" is a topic, and may want the web
	headlineHint = regexp.MustCompile(`(?i)^\W*(hey\W+)?((what'?s|what is|whats|give me|show me|tell me|any|read me)\W+)?(the\W+)?((latest|today'?s|top|main|morning|evening|big)\W+)?(news|headlines|stories)(\W+(today|now|this morning|this evening|summary|so far|please))*\W*$|^\W*what'?s (happening|going on)( in the world| today)?\W*$`)
	// coins every phone writes in lower case; any other symbol counts only in capitals ("NEAR",
	// not "near"), and any name only from five letters ("chainlink", not "sky")
	commonCoins = map[string]string{"btc": "BTC", "bitcoin": "BTC", "eth": "ETH", "ether": "ETH", "ethereum": "ETH", "sol": "SOL", "solana": "SOL",
		"xrp": "XRP", "ripple": "XRP", "doge": "DOGE", "dogecoin": "DOGE", "ada": "ADA", "cardano": "ADA", "ltc": "LTC", "litecoin": "LTC"}
)

// coinNames is lower-case name or symbol to symbol, for the coins the box follows and the rank
// list's, kept ten minutes.
type coinNames struct {
	mu  sync.Mutex
	at  time.Time
	all map[string]string // symbol (upper) and name (lower) to symbol
}

var knownCoinNames coinNames

func knownCoins(db *poltergres.ReadWrite, now time.Time) map[string]string {
	knownCoinNames.mu.Lock()
	defer knownCoinNames.mu.Unlock()
	if knownCoinNames.all != nil && now.Sub(knownCoinNames.at) < 10*time.Minute {
		return knownCoinNames.all
	}
	m := map[string]string{}
	if db != nil {
		for _, s := range tally.Symbols(db) {
			m[s] = s
		}
		if snap, err := hw.RatesNow(db); err == nil {
			for _, c := range snap.Ranks {
				sym := strings.ToUpper(c.Symbol)
				m[sym] = sym
				if name := strings.ToLower(strings.TrimSpace(c.Name)); len(name) >= 5 {
					m[name] = sym
				}
			}
		}
	}
	knownCoinNames.all, knownCoinNames.at = m, now
	return m
}

// coinsIn reads the coins a question names, in the order it names them.
func coinsIn(prompt string, known map[string]string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(sym string) {
		if sym != "" && !seen[sym] {
			seen[sym] = true
			out = append(out, sym)
		}
	}
	words := strings.FieldsFunc(prompt, func(r rune) bool {
		return !(r == '-' || r == '.' || (r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z'))
	})
	low := strings.ToLower(prompt)
	for _, w := range words {
		w = strings.Trim(w, ".-")
		if w == "" {
			continue
		}
		if sym, ok := commonCoins[strings.ToLower(w)]; ok {
			add(sym)
			continue
		}
		// a symbol in capitals, two letters or more ("I" and "A" are words)
		if len(w) >= 2 && w == strings.ToUpper(w) && strings.ToLower(w) != w {
			if sym, ok := known[w]; ok && sym == w {
				add(sym)
			}
		}
	}
	// names of five letters or more, matched as whole words ("chainlink", "polkadot")
	for name, sym := range known {
		if len(name) < 5 || name != strings.ToLower(name) || name == strings.ToUpper(name) {
			continue
		}
		if i := strings.Index(low, name); i >= 0 {
			before := i == 0 || !isWordByte(low[i-1])
			after := i+len(name) == len(low) || !isWordByte(low[i+len(name)])
			if before && after {
				add(sym)
			}
		}
	}
	return out
}

func isWordByte(b byte) bool { return b == '_' || (b >= 'a' && b <= 'z') || (b >= '0' && b <= '9') }

// boxHas says whether the box answers a question from what it keeps, and from what. The phone
// leaves the web alone for these (a price, the top coins, crypto as a whole, a rate between
// currencies, the day's headlines).
func boxHas(prompt string, known map[string]string) (string, bool) {
	if topHint.MatchString(prompt) {
		return "Coinbase's rank list, kept on the box", true
	}
	if marketQuestion(prompt) {
		return "the box's crypto index", true
	}
	if coins := coinsIn(prompt, known); len(coins) > 0 && (priceHint.MatchString(prompt) || len(strings.Fields(prompt)) <= 3) {
		return "the box's own prices for " + strings.Join(coins, ", "), true
	}
	if codes, _, ok := moneyQuestion(prompt); ok {
		fiat := 0
		for _, c := range codes {
			if !cryptoCode(c) {
				fiat++
			}
		}
		if fiat == len(codes) {
			return "the ECB's rates, kept on the box", true
		}
	}
	if headlineHint.MatchString(strings.TrimSpace(prompt)) {
		return "the news the box gathered today", true
	}
	return "", false
}

func cryptoCode(c string) bool {
	switch c {
	case "BTC", "ETH", "USDT", "SOL", "XRP", "ADA", "DOGE":
		return true
	}
	return false
}

// boxCovers is boxHas over the box's own coin list, the weather the box pulled (weather.go: a
// weather question naming no place never goes to the web, whatever the box holds, since the web
// would need the phone's position to answer it) and the box's own Wikipedia (wikipedia.go).
func boxCovers(runDir, prompt string) (string, bool) {
	if why, ok := weatherCovers(runDir, prompt); ok {
		return why, true
	}
	if why, ok := wikiCovers(prompt); ok {
		return why, true // "what is X" with X an article of the box's own Wikipedia (wikipedia.go)
	}
	db := chatStore(filepath.Dir(runDir))
	return boxHas(prompt, knownCoins(db, time.Now()))
}

// topCount is how many coins a "top N" question wants: its number, else ten; fifty at most.
func topCount(prompt string) int {
	n := 10
	if m := topN.FindStringSubmatch(prompt); m != nil {
		if v, err := strconv.Atoi(m[1]); err == nil && v > 0 {
			n = v
		}
	}
	if n > 50 {
		n = 50
	}
	return n
}

// topItem is the rank list's first n as one prompt line, each coin at the box's own price where it
// has one (a minute old) and the list's otherwise (pure, for the tests).
func topItem(snap hw.RatesSnapshot, n int, now time.Time) (ctxItem, bool) {
	if len(snap.Ranks) == 0 {
		return ctxItem{}, false
	}
	if n > len(snap.Ranks) {
		n = len(snap.Ranks)
	}
	parts := make([]string, 0, n)
	for _, c := range snap.Ranks[:n] {
		price := c.PriceUSD
		if r, ok := snap.Index[strings.ToUpper(c.Symbol)]; ok && r.Price > 0 {
			price = r.Price
		}
		capUSD := c.MarketCap
		if c.Supply > 0 && price > 0 {
			capUSD = price * c.Supply // the cap at the box's price: Coinbase's circulating supply times it
		}
		parts = append(parts, fmt.Sprintf("%d. %s (%s) %s USD, cap %s, %+.1f%% 24 h", c.Rank, strings.ToUpper(c.Symbol), c.Name, money(price), bigMoney(capUSD), c.Change24))
	}
	src := snap.Source
	if src == "coinbase-ranks" {
		src = "Coinbase"
	}
	age := ""
	if snap.RanksAt > 0 {
		age = ", " + now.Sub(time.Unix(snap.RanksAt, 0)).Truncate(time.Minute).String() + " old"
	}
	return ctxItem{When: time.Unix(snap.RanksAt, 0).UTC().Format("2006-01-02"), Source: "rates",
		Snippet: fmt.Sprintf("the %d largest coins by market cap (%s's list%s): %s", n, src, age, strings.Join(parts, " · ")),
		Why:     "the rank list kept on the box, with the box's own prices"}, true
}

// bigMoney writes a market cap: "1.69T", "412B", "88.1M".
func bigMoney(v float64) string {
	switch {
	case v >= 1e12:
		return strconv.FormatFloat(v/1e12, 'f', 2, 64) + "T"
	case v >= 1e9:
		return strconv.FormatFloat(v/1e9, 'f', 1, 64) + "B"
	case v >= 1e6:
		return strconv.FormatFloat(v/1e6, 'f', 1, 64) + "M"
	}
	return money(v)
}
