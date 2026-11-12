package main

// WEIGHT , how much a memory counts when the box has to choose: which of two that match a
// question equally goes to the model, and what is on the front page every chat starts from.
// The post "How memory gets made" gives the two ways a memory earns it, and this is both:
//
//	the slow way   from the company it keeps. A memory with neighbours (links.go) weighs more
//	               than one alone, and a neighbourhood is as fresh as its most recent member:
//	               "the cluster's age is the age of its most recent member, not the average
//	               age of its members". So a dinner from 2012 is warm again the day a fourth
//	               dinner like it is written, and opening any one of them warms the rest.
//	the fast way   an anchor. The person marked it, so its weight starts high and stays high
//	               whether or not anything on the box is related to it, however old it is.
//
// The person's own words (a memory written or rewritten by hand) count a little more than the
// box's. Nothing here is stored: the weight is worked out from the rows when it is wanted, so
// there is no number to go stale and nothing to migrate when the rule changes.

import (
	"math"
	"strconv"
	"strings"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

const (
	weightAnchor   = 10.0 // above anything the other terms can add up to
	weightOwnWords = 1.0
	weightWarmMax  = 2.0
	warmHalfLifeD  = 90.0 // days for a neighbourhood nobody touched to cool by half
	weightCompanyN = 60   // neighbours counted; past this a hub is not weightier
)

// weighIn is what the weight is worked out from.
type weighIn struct {
	anchored   bool
	ownWords   bool
	neighbours int
	freshest   int64 // unix ms: the newest moment in the neighbourhood, the memory's own included
}

// memWeight is the weight of one memory at a moment (pure, for the tests).
func memWeight(w weighIn, nowMs int64) float64 {
	v := 0.0
	if w.anchored {
		v += weightAnchor
	}
	if w.ownWords {
		v += weightOwnWords
	}
	n := w.neighbours
	if n > weightCompanyN {
		n = weightCompanyN
	}
	if n > 0 {
		v += math.Log1p(float64(n))
	}
	if w.freshest > 0 {
		days := float64(nowMs-w.freshest) / 86400000
		if days < 0 {
			days = 0
		}
		v += weightWarmMax * math.Pow(0.5, days/warmHalfLifeD)
	}
	return v
}

// memWeights works the weights out for the given memories, or for every live one when ids is
// nil: each memory with how many live neighbours it has and the newest moment among them (when
// one was made, or last in front of the person). Three plain reads and no join (docs/DATA.md):
// the memories, their links, and the neighbours' moments in one batched lookup.
func memWeights(db *poltergres.ReadWrite, ids []int64, nowMs int64) (map[int64]float64, error) {
	where, lwhere := "", ""
	var args []any
	if ids != nil {
		if len(ids) == 0 {
			return map[int64]float64{}, nil
		}
		where, lwhere = " AND id = ANY($1::bigint[])", " WHERE a = ANY($1::bigint[]) OR b = ANY($1::bigint[])"
		args = append(args, idArray(ids))
	}
	rows, err := db.Query("SELECT id, (anchored_at IS NOT NULL), (user_edited OR kind = 'user'), GREATEST(created_at, touched_at) FROM memories WHERE NOT tombstoned"+where, args...)
	if err != nil {
		return nil, err
	}
	in := make(map[int64]*weighIn, len(rows.Vals))
	moment := make(map[int64]int64, len(rows.Vals)) // every live memory read so far → its newest moment
	for _, v := range rows.Vals {
		if len(v) < 4 || v[0] == nil {
			continue
		}
		id, _ := strconv.ParseInt(*v[0], 10, 64)
		own, _ := strconv.ParseInt(str(v[3]), 10, 64)
		in[id] = &weighIn{anchored: str(v[1]) == "t", ownWords: str(v[2]) == "t", freshest: own}
		moment[id] = own
	}
	lrows, err := db.Query("SELECT a, b FROM memory_links"+lwhere, args...)
	if err != nil {
		return nil, err
	}
	type edge struct{ a, b int64 }
	edges := make([]edge, 0, len(lrows.Vals))
	var missing []int64
	asked := map[int64]bool{}
	for _, v := range lrows.Vals {
		if len(v) < 2 || v[0] == nil || v[1] == nil {
			continue
		}
		a, _ := strconv.ParseInt(*v[0], 10, 64)
		b, _ := strconv.ParseInt(*v[1], 10, 64)
		edges = append(edges, edge{a, b})
		for _, x := range [2]int64{a, b} {
			if _, ok := moment[x]; !ok && !asked[x] {
				asked[x] = true
				missing = append(missing, x)
			}
		}
	}
	if len(missing) > 0 { // neighbours outside the set asked for (never when every memory was read)
		nrows, nerr := db.Query("SELECT id, GREATEST(created_at, touched_at) FROM memories WHERE NOT tombstoned AND id = ANY($1::bigint[])", idArray(missing))
		if nerr != nil {
			return nil, nerr
		}
		for _, v := range nrows.Vals {
			if len(v) == 2 && v[0] != nil {
				id, _ := strconv.ParseInt(*v[0], 10, 64)
				moment[id], _ = strconv.ParseInt(str(v[1]), 10, 64)
			}
		}
	}
	for _, e := range edges {
		ma, aLive := moment[e.a]
		mb, bLive := moment[e.b]
		if !aLive || !bLive { // a link to a memory that is gone counts for nothing
			continue
		}
		if w := in[e.a]; w != nil {
			w.neighbours++
			if mb > w.freshest {
				w.freshest = mb
			}
		}
		if w := in[e.b]; w != nil {
			w.neighbours++
			if ma > w.freshest {
				w.freshest = ma
			}
		}
	}
	out := make(map[int64]float64, len(in))
	for id, w := range in {
		out[id] = memWeight(*w, nowMs)
	}
	return out, nil
}

// idArray writes ids as a Postgres bigint array literal.
func idArray(ids []int64) string {
	s := make([]string, len(ids))
	for i, id := range ids {
		s[i] = strconv.FormatInt(id, 10)
	}
	return "{" + strings.Join(s, ",") + "}"
}
