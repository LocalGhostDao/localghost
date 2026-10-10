package main

import (
	"strings"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/hw"
	"github.com/LocalGhostDao/localghost/server/internal/rates"
	"github.com/LocalGhostDao/localghost/server/internal/tally"
)

func TestMoneyQuestion(t *testing.T) {
	codes, amount, ok := moneyQuestion("how much is 100 euros in pounds?")
	if !ok || amount != 100 || len(codes) != 2 || codes[0] != "EUR" || codes[1] != "GBP" {
		t.Fatalf("%v %v %v", codes, amount, ok)
	}
	codes, amount, ok = moneyQuestion("gbp to ron")
	if !ok || amount != 0 || codes[0] != "GBP" || codes[1] != "RON" {
		t.Fatalf("%v %v %v", codes, amount, ok)
	}
	if _, _, ok := moneyQuestion("what is bitcoin at today"); !ok {
		t.Fatal("bitcoin alone is a money question")
	}
	if codes, _, ok := moneyQuestion("eth price?"); !ok || codes[0] != "ETH" {
		t.Fatalf("eth: %v %v", codes, ok)
	}
	if _, _, ok := moneyQuestion("the real reason we went to the pound shop"); ok {
		t.Fatal("a pound shop is not a money question")
	}
	if _, amount, _ := moneyQuestion("convert 2.5k usd to eur"); amount != 2500 {
		t.Fatalf("2.5k: %v", amount)
	}
}

func TestMarketQuestionAndItem(t *testing.T) {
	for _, q := range []string{"how is crypto doing today?", "is the market up this week", "what's crypto doing", "how are the coins doing"} {
		if !marketQuestion(q) {
			t.Fatalf("%q is about the market", q)
		}
	}
	for _, q := range []string{"how much is 100 euros in pounds", "where did we eat on the corfu trip", "the farmers market on saturday"} {
		if marketQuestion(q) {
			t.Fatalf("%q is not about the market", q)
		}
	}
	it := marketItem(tally.MarketState{Code: "CRYPTO50", Value: 1234.5, DayChange: 1.234, Day: "2026-09-30", Constituents: 50, Priced: 38,
		Top: []rates.Constituent{{Symbol: "BTC", Weight: 0.41}, {Symbol: "ETH", Weight: 0.22}}})
	if it.Source != "rates" || !strings.Contains(it.Snippet, "CRYPTO50: 1234.5, +1.23%") || !strings.Contains(it.Snippet, "BTC 41%, ETH 22%") || !strings.Contains(it.Snippet, "38 priced live") {
		t.Fatalf("%+v", it)
	}
}

func TestRatesItems(t *testing.T) {
	now := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	snap := hw.RatesSnapshot{FXDay: "2026-09-30", FX: map[string]float64{"USD": 1.2, "GBP": 0.9, "RON": 5},
		Index: map[string]hw.IndexRow{
			"BTC": {Price: 65000, At: now.Add(-5 * time.Minute).Unix(), N: 4, Spread: 0.003, Used: "bitstamp,coinbase,gemini,kraken"},
			"ETH": {Price: 3250, At: now.Add(-5 * time.Minute).Unix(), N: 3, Spread: 0.001, Used: "binance,coinbase,kraken", Change24: -1.5, HasChange: true},
		}}
	items := ratesItems(snap, []string{"EUR", "GBP"}, 100, now)
	if len(items) != 2 || !strings.Contains(items[0].Snippet, "0.9000 GBP") || items[1].Snippet != "100.00 EUR = 90.00 GBP at these rates" {
		t.Fatalf("%+v", items)
	}
	items = ratesItems(snap, []string{"BTC"}, 0, now)
	if len(items) != 2 || !strings.Contains(items[1].Snippet, "BTC 65,000 USD , the box's average of 4 exchanges") || !strings.Contains(items[1].Snippet, "5m0s ago") {
		t.Fatalf("%+v", items)
	}
	items = ratesItems(snap, []string{"BTC", "GBP"}, 1, now)
	if len(items) != 3 || items[2].Snippet != "1.00 BTC = 48,750 GBP at these rates" {
		t.Fatalf("%+v", items)
	}
	items = ratesItems(snap, []string{"ETH", "USD"}, 2, now)
	if len(items) != 3 || !strings.Contains(items[1].Snippet, "ETH 3,250 USD, -1.50% in 24 h ,") || items[2].Snippet != "2.00 ETH = 6,500 USD at these rates" {
		t.Fatalf("%+v", items)
	}
	empty := ratesItems(hw.RatesSnapshot{FX: map[string]float64{}}, []string{"EUR", "GBP"}, 10, now)
	if len(empty) != 1 || !strings.Contains(empty[0].Snippet, "no rates yet") {
		t.Fatalf("%+v", empty)
	}
	if money(0.00012345) != "0.00012345" || money(12.5) != "12.50" || money(1234567) != "1,234,567" {
		t.Fatal(money(0.00012345), money(12.5), money(1234567))
	}
}

func TestGroundedNews(t *testing.T) {
	facts := []string{"BBC: Rates held at 4% as inflation cools , The Bank kept its rate at 4% on Thursday.", "FT: Bank holds at 4%"}
	if s, ok := groundedNews("The Bank of England kept its rate at 4% on Thursday, saying inflation is cooling.", facts); !ok || !strings.HasPrefix(s, "The Bank") {
		t.Fatalf("%q %v", s, ok)
	}
	if _, ok := groundedNews("The Bank cut its rate to 3.75% on Thursday.", facts); ok {
		t.Fatal("a number not in the reports passed")
	}
	if _, ok := groundedNews("- The Bank held\n- inflation cooled", facts); ok {
		t.Fatal("a list passed")
	}
	if _, ok := groundedNews("As an AI I cannot summarise the reports.", facts); ok {
		t.Fatal("a refusal passed")
	}
	// a lead and its points
	s, ok := groundedNews("The Bank of England kept its rate at 4% on Thursday.\n\n- Inflation is cooling, the Bank said.\n* The vote was close\nand split the committee.\n2. The next decision is in November.", facts)
	if !ok || s != "The Bank of England kept its rate at 4% on Thursday.\n- Inflation is cooling, the Bank said.\n- The vote was close and split the committee.\n- The next decision is in November." {
		t.Fatalf("%q %v", s, ok)
	}
	if _, ok := groundedNews("The Bank of England kept its rate at 4% on Thursday.\n- It was 3.5% last year.", facts); ok {
		t.Fatal("a point's number not in the reports passed")
	}
	if _, ok := groundedNews("The Bank of England kept its rate at 4% on Thursday.\n- ok", facts); ok {
		t.Fatal("a point that says nothing passed")
	}
	if lead, pts := splitStory("A paragraph from before the points.\nIts second line."); lead != "A paragraph from before the points. Its second line." || len(pts) != 0 {
		t.Fatalf("%q %v", lead, pts)
	}
	if body := digestBody([]digestStory{{1, "Rates held", 3, "The Bank kept its rate at 4%.\n- Inflation cooled."}}); body != "• The Bank kept its rate at 4%. · 3 outlets" {
		t.Fatalf("the digest tells the lead: %q", body)
	}
}

func TestDigestBody(t *testing.T) {
	body := digestBody([]digestStory{{1, "Rates held at 4%", 3, "The Bank kept its rate at 4%."}, {2, "A lone story", 1, ""}})
	if body != "• The Bank kept its rate at 4%. · 3 outlets\n• A lone story" {
		t.Fatalf("%q", body)
	}
	if digestBody(nil) != "" {
		t.Fatal("empty")
	}
}
