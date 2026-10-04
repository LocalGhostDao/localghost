package hw

// HOME, CARRIED ON WHAT THE PHONE SENDS ANYWAY. Every quarter hour the phone asks for its
// notifications, and while the trail is on it sends where it is; the box answers both with home's
// numbers as they stand (HomeSnap: BTC, ETH and SOL with the day's change, CRYPTO50, the brief and
// the day's most-told stories), so the phone keeps them and home and the lock screen open on the
// latest without asking. A few kilobytes, read from the Redis copies.

import (
	"crypto/sha256"
	"encoding/hex"
	"sort"
	"strings"
	"time"
)

// SnapPrice is one coin as home shows it.
type SnapPrice struct {
	Price     float64 `json:"price"`
	Change24  float64 `json:"change24"`
	HasChange bool    `json:"hasChange"`
	At        int64   `json:"at"`
	N         int     `json:"n"`
}

// SnapStory is one of the day's most-told stories, for the lock screen's cards.
type SnapStory struct {
	ID       int64    `json:"id"`
	Title    string   `json:"title"`
	Lead     string   `json:"lead"`
	Outlets  []string `json:"outlets"`
	Sources  int      `json:"sources"`
	LastSeen int64    `json:"lastSeen"`
}

// HomeSnap is home's numbers and news as they stand.
type HomeSnap struct {
	At           int64                `json:"at"`
	Prices       map[string]SnapPrice `json:"prices"`
	MarketCode   string               `json:"marketCode,omitempty"`
	MarketValue  float64              `json:"marketValue,omitempty"`
	MarketChange float64              `json:"marketChange,omitempty"`
	Brief        string               `json:"brief"`
	BriefAt      int64                `json:"briefAt"`
	BriefStories []int64              `json:"briefStories"`
	Top          []SnapStory          `json:"top"`
	// FOR YOU: places near the trail, this day in earlier years, the news that touches me (foryou.go)
	ForYou *ForYou `json:"forYou,omitempty"`
}

// HomeSymbols are the coins the snapshot carries.
var HomeSymbols = []string{"BTC", "ETH", "SOL"}

// MakeHomeSnap builds the snapshot from the rates doc (with the fast lane over it), the news doc
// and the time (pure, for the tests). Either doc may be nil.
func MakeHomeSnap(rd *RatesDoc, fast *Fast, nd *NewsDoc, now time.Time) HomeSnap {
	s := HomeSnap{At: now.Unix(), Prices: map[string]SnapPrice{}, BriefStories: []int64{}, Top: []SnapStory{}}
	if rd != nil {
		snap := rd.RatesSnapshot
		if snap.Index != nil {
			idx := make(map[string]IndexRow, len(snap.Index))
			for k, v := range snap.Index {
				idx[k] = v
			}
			snap.Index = idx
			if fast != nil {
				ApplyFast(&snap, *fast, now)
			}
			for _, sym := range HomeSymbols {
				if r, ok := snap.Index[sym]; ok && r.Price > 0 {
					s.Prices[sym] = SnapPrice{Price: r.Price, Change24: r.Change24, HasChange: r.HasChange, At: r.At, N: r.N}
				}
			}
		}
		if rd.Market != nil && rd.Market.Value > 0 {
			s.MarketCode, s.MarketValue, s.MarketChange = rd.Market.Code, rd.Market.Value, rd.Market.DayChange
		}
	}
	if nd != nil {
		s.Brief, s.BriefAt = nd.Brief, nd.BriefAt
		if nd.BriefStories != nil {
			s.BriefStories = nd.BriefStories
		}
		// the day's most-told, the newest first among equals, six at most
		var day []NewsStory
		for _, st := range nd.Stories {
			if now.Unix()-st.LastSeen <= 86400 && st.Title != "" {
				day = append(day, st)
			}
		}
		sort.SliceStable(day, func(i, j int) bool {
			if day[i].Sources != day[j].Sources {
				return day[i].Sources > day[j].Sources
			}
			return day[i].LastSeen > day[j].LastSeen
		})
		for i, st := range day {
			if i >= 6 {
				break
			}
			var outlets []string
			seen := map[string]bool{}
			for _, it := range st.Items {
				if it.Outlet != "" && !seen[it.Outlet] {
					seen[it.Outlet] = true
					outlets = append(outlets, it.Outlet)
				}
			}
			s.Top = append(s.Top, SnapStory{ID: st.ID, Title: st.Title, Lead: leadOf(st.Summary), Outlets: outlets, Sources: st.Sources, LastSeen: st.LastSeen})
		}
	}
	return s
}

// leadOf is a summary's lead: its first line (a summary is "lead\n- point\n- point").
func leadOf(summary string) string {
	return strings.TrimSpace(strings.SplitN(summary, "\n", 2)[0])
}

// AboutVersion changes when the way the note is made into memories changes, so a box makes them
// again: v2 writes them with the person's name ("Vlad lives…"), not "I"; v3 keeps one memory per
// person, the note's line a part of it beside what the chats and the check-ins added.
const AboutVersion = "v3-one-per-person"

// AboutHash is the note's fingerprint with the version: synthd keeps it once the note's memories
// are made, secd compares it to say whether they are.
func AboutHash(note string) string {
	h := sha256.Sum256([]byte(AboutVersion + "\n" + strings.TrimSpace(note)))
	return hex.EncodeToString(h[:8])
}
