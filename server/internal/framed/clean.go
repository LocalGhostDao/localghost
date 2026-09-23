package framed

// Glitches in a trail. A phone's fix is sometimes not where the phone is: a cell-tower position
// from the network provider, a stale fix from another provider, a bad first fix after a cold
// start. On the map it shows as a spike , the line shoots off to a point tens or hundreds of
// kilometres away and comes straight back. The raw points stay in the database (a view can be
// wrong, the record should not be); the day path and the phone's own drawing run the same rules
// over them, and the day's distance is over what survived, so a spike does not add 200 km to a
// walk on an island.
//
// The rules judge shape and speed, never a coordinate's plausibility on its own:
//
//   1. an IMPOSSIBLE hop , faster than anything a person rides (maxSpeedMS, well above an
//      airliner's ground speed) , is dropped on its own. A flight's fixes are fast but not that fast.
//   2. a FAST hop (fastSpeedMS, faster than road or rail) that the trail COMES BACK from , within a
//      few points and a short time it is once more near where it left , is a spike, and the points
//      out there are dropped. A flight is a fast hop that does not come back within the window.
//   3. a LONE point reached and left at car speed or better (loneLegMS both ways) between walking
//      pace is a spike too: you do not go from a stroll to a forty-kilometre round trip with no fix
//      at the far end and back to a stroll, all inside two sampling gaps.
//
// Hops under minJumpM are never judged (jitter, sitting still). Everything else , a slow wander, a
// real errand with time spent at the far end, a whole day of travel , is left alone.

import (
	"math"
	"sort"
)

const (
	minJumpM        = 300.0 // shorter hops are jitter or a walk; never judged
	maxSpeedMS      = 350.0 // faster than an airliner over the ground: impossible, dropped alone
	fastSpeedMS     = 90.0  // faster than road or rail (~320 km/h): a spike if the trail comes back
	loneLegMS       = 12.0  // ~43 km/h averaged over a leg: a lone point in and out at this pace between slow movement
	slowMS          = 4.0   // the pace before and after a lone point that makes it implausible (a brisk walk)
	returnFrac      = 0.34  // "came back": within this fraction of the hop's length from where it left
	returnMaxPoints = 8     // how far ahead the return is looked for
	returnMaxS      = 5400  // and how long (90 min): a sightseeing flight is slower than fast, a day trip is longer than this
)

// CleanTrack returns the time-ordered points that survive the rules above and how many were
// dropped. Points are sorted by time first; ties keep their order.
func CleanTrack(pts []TrackPoint) ([]TrackPoint, int) {
	if len(pts) < 2 {
		return pts, 0
	}
	sorted := make([]TrackPoint, len(pts))
	copy(sorted, pts)
	sort.SliceStable(sorted, func(i, j int) bool { return sorted[i].TS < sorted[j].TS })
	kept := make([]TrackPoint, 0, len(sorted))
	kept = append(kept, sorted[0])
	dropped := 0
	i := 1
	for i < len(sorted) {
		a := kept[len(kept)-1]
		p := sorted[i]
		d := HaversineM(a, p)
		if d < minJumpM {
			kept = append(kept, p)
			i++
			continue
		}
		v := speedMS(a, p, d)
		if v > maxSpeedMS { // rule 1
			dropped++
			i++
			continue
		}
		if v > fastSpeedMS { // rule 2
			if j := returnsTo(sorted, i, a, d); j > 0 {
				dropped += j - i
				i = j
				continue
			}
		}
		if i+1 < len(sorted) && v > loneLegMS { // rule 3
			n := sorted[i+1]
			back := HaversineM(p, n)
			if back >= minJumpM && speedMS(p, n, back) > loneLegMS && HaversineM(a, n) <= d*returnFrac &&
				slowBefore(kept) && slowAfter(sorted, i+1) {
				dropped++
				i++
				continue
			}
		}
		kept = append(kept, p)
		i++
	}
	return kept, dropped
}

// returnsTo looks ahead of the fast hop at pts[i] for the first point back near the anchor a ,
// within returnFrac of the hop length d , inside the window; it returns that index, or 0.
func returnsTo(pts []TrackPoint, i int, a TrackPoint, d float64) int {
	for j := i + 1; j < len(pts) && j <= i+returnMaxPoints; j++ {
		if pts[j].TS-pts[i].TS > returnMaxS {
			return 0
		}
		if HaversineM(a, pts[j]) <= d*returnFrac {
			return j
		}
	}
	return 0
}

// slowBefore is whether the last hop the trail kept before the anchor was at walking pace or
// shorter than a judged hop (or there is nothing before it, which counts as slow: the day begins).
func slowBefore(kept []TrackPoint) bool {
	if len(kept) < 2 {
		return true
	}
	a, b := kept[len(kept)-2], kept[len(kept)-1]
	d := HaversineM(a, b)
	return d < minJumpM || speedMS(a, b, d) <= slowMS
}

// slowAfter is whether the hop that follows pts[k] is at walking pace (or there is none).
func slowAfter(pts []TrackPoint, k int) bool {
	if k+1 >= len(pts) {
		return true
	}
	d := HaversineM(pts[k], pts[k+1])
	return d < minJumpM || speedMS(pts[k], pts[k+1], d) <= slowMS
}

// speedMS is d metres over the time between a and b; two fixes with the same second are an
// infinite speed (a hop with no time is not a movement).
func speedMS(a, b TrackPoint, d float64) float64 {
	dt := b.TS - a.TS
	if dt <= 0 {
		return math.Inf(1)
	}
	return d / float64(dt)
}

// HaversineM is the great-circle distance between two points in metres on a 6371 km sphere , the
// same formula on the box and the phone, so both draw the same trail from the same points.
func HaversineM(a, b TrackPoint) float64 {
	const r = 6371000.0
	la1, la2 := a.Lat*math.Pi/180, b.Lat*math.Pi/180
	dla := la2 - la1
	dlo := (b.Lon - a.Lon) * math.Pi / 180
	h := math.Sin(dla/2)*math.Sin(dla/2) + math.Cos(la1)*math.Cos(la2)*math.Sin(dlo/2)*math.Sin(dlo/2)
	return 2 * r * math.Asin(math.Min(1, math.Sqrt(h)))
}
