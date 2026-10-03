package rates

import (
	"math"
	"strings"
	"testing"
	"time"
)

func TestWeightsAndValue(t *testing.T) {
	avg := map[string]float64{"BTC": 3e10, "ETH": 1.5e10, "SOL": 3e9, "USDT": 1e11, "WBTC": 1e9, "DOGE": 2e9, "NEW": 5e9}
	caps := map[string]float64{"BTC": 1.3e12, "ETH": 4e11, "SOL": 7e10, "USDT": 1.7e11, "WBTC": 1e10, "DOGE": 1.8e10, "NEW": 9e10}
	base := map[string]float64{"BTC": 65000, "ETH": 3500, "SOL": 150, "USDT": 1, "WBTC": 65000, "DOGE": 0.12} // NEW has no price yet
	cons := Weights(avg, caps, base, 3)
	if len(cons) != 3 || cons[0].Symbol != "BTC" || cons[1].Symbol != "ETH" || cons[2].Symbol != "SOL" {
		t.Fatalf("constituents: %+v", cons)
	}
	total := 3e10 + 1.5e10 + 3e9
	if math.Abs(cons[0].Weight-3e10/total) > 1e-12 || math.Abs(cons[0].Weight+cons[1].Weight+cons[2].Weight-1) > 1e-12 || cons[2].Rank != 3 {
		t.Fatalf("weights: %+v", cons)
	}
	// flat prices: the chain value stands
	v, priced, missing := Value(1000, cons, map[string]float64{"BTC": 65000, "ETH": 3500, "SOL": 150}, nil)
	if math.Abs(v-1000) > 1e-9 || priced != 3 || len(missing) != 0 {
		t.Fatalf("flat: %v %d %v", v, priced, missing)
	}
	// BTC up 10%, the others flat: up by BTC's weight times 10%
	v, _, _ = Value(1000, cons, map[string]float64{"BTC": 71500, "ETH": 3500, "SOL": 150}, nil)
	if math.Abs(v-1000*(1+0.1*cons[0].Weight)) > 1e-9 {
		t.Fatalf("btc up: %v", v)
	}
	// SOL has no price today: carried at yesterday's, named
	v, priced, missing = Value(1000, cons, map[string]float64{"BTC": 65000, "ETH": 3500}, map[string]float64{"SOL": 165})
	if priced != 2 || len(missing) != 1 || missing[0] != "SOL" || math.Abs(v-1000*(1+0.1*cons[2].Weight)) > 1e-9 {
		t.Fatalf("carried: %v %d %v", v, priced, missing)
	}
	// nothing carried either: flat at the base
	v, _, _ = Value(1000, cons, map[string]float64{"BTC": 65000, "ETH": 3500}, nil)
	if math.Abs(v-1000) > 1e-9 {
		t.Fatalf("base: %v", v)
	}
	if v, _, _ := Value(0, cons, nil, nil); v != 0 {
		t.Fatal("no chain")
	}
	// a price that is not this coin's (another asset under SOL's ticker, at 40 times the base) is
	// held at the carried price and named with "!", not let move the index by its weight times 40
	v, priced, missing = Value(1000, cons, map[string]float64{"BTC": 65000, "ETH": 3500, "SOL": 6000}, map[string]float64{"SOL": 165})
	if priced != 2 || len(missing) != 1 || missing[0] != "SOL!" || math.Abs(v-1000*(1+0.1*cons[2].Weight)) > 1e-9 {
		t.Fatalf("held: %v %d %v", v, priced, missing)
	}
	// a carried price past the bound is no better: the base holds the coin flat
	v, _, missing = Value(1000, cons, map[string]float64{"BTC": 65000, "ETH": 3500}, map[string]float64{"SOL": 0.001})
	if math.Abs(v-1000) > 1e-9 || len(missing) != 1 || missing[0] != "SOL" {
		t.Fatalf("flat past the bound: %v %v", v, missing)
	}
	terms := Terms(cons, map[string]float64{"BTC": 71500, "SOL": 6000}, map[string]float64{"ETH": 3500, "SOL": 165})
	if len(terms) != 3 || terms[0].From != "live" || math.Abs(terms[0].Ratio-1.1) > 1e-9 || terms[1].From != "carried" || terms[2].From != "held" || terms[2].Price != 165 {
		t.Fatalf("terms: %+v", terms)
	}
	if MonthOf("2026-10-01") != "2026-10" {
		t.Fatal(MonthOf("2026-10-01"))
	}
}

func TestBarsAndFold(t *testing.T) {
	from := time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)
	for _, c := range []struct {
		m    Market
		want string
	}{
		{Market{"binance", "BTC", "USDT"}, "interval=1m"},
		{Market{"coinbase", "BTC", "USD"}, "granularity=60&"},
		{Market{"bitstamp", "BTC", "USD"}, "step=60&"},
		{Market{"bitfinex", "BTC", "USD"}, "trade:1m:tBTCUSD"},
		{Market{"okx", "BTC", "USDT"}, "bar=1m&"},
		{Market{"gemini", "BTC", "USD"}, "/btcusd/1m"},
		{Market{"kraken", "BTC", "USD"}, "interval=1&"},
	} {
		if u := c.m.MinuteURL(from, from.Add(time.Hour)); !strings.Contains(u, c.want) {
			t.Fatalf("%s: %s", c.m.ID(), u)
		}
	}
	bars := map[Market]Bar{
		{"coinbase", "BTC", "USD"}: {TS: 60, Open: 100, High: 110, Low: 95, Close: 105, Volume: 2},
		{"binance", "BTC", "USDT"}: {TS: 60, Open: 100.2, High: 110.2, Low: 95.2, Close: 105.2, Volume: 3},
		{"gemini", "BTC", "USD"}:   {TS: 60, Open: 100, High: 110, Low: 95, Close: 140, Volume: 1}, // a bad close
		{"coinbase", "ETH", "USD"}: {TS: 60, Open: 1, High: 1, Low: 1, Close: 1, Volume: 1},
	}
	b, n := FoldBar(bars, "BTC", 0.999)
	if n != 2 || math.Abs(b.Close-(105+105.2*0.999)/2) > 1e-9 || b.TS != 60 || b.Volume != 6 {
		t.Fatalf("%+v %d", b, n)
	}
	if _, n := FoldBar(bars, "SOL", 1); n != 0 {
		t.Fatal("sol")
	}
}
