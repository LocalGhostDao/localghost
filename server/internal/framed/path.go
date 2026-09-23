package framed

// The daily path. Watch/phone location points plus photo GPS positions become one GeoJSON document
// per day: a LineString for where you went, Point features for where you photographed. The box stores
// DATA ONLY , it never fetches map tiles or contacts any map service, because requesting tiles sends
// your coordinate history to a third-party server, which is the one outbound call this box must never
// make. The phone renders the GeoJSON over OpenStreetMap tiles client-side; the phone already talks
// to the internet, the box does not.

import (
	"encoding/json"
	"math"
	"time"
)

// TrackPoint is one location sample from the watch/phone.
type TrackPoint struct {
	TS  int64   `json:"ts"` // unix seconds
	Lat float64 `json:"lat"`
	Lon float64 `json:"lon"`
}

// PhotoPoint is one geotagged photo on the day's map.
type PhotoPoint struct {
	Hash    string  `json:"hash"`
	TakenAt int64   `json:"takenAt"`
	Lat     float64 `json:"lat"`
	Lon     float64 `json:"lon"`
}

// simplifyEpsilonDeg is the Douglas-Peucker tolerance in degrees. 0.0001 deg is roughly 11 metres of
// latitude , tight enough to keep the shape of a walk, loose enough to collapse GPS jitter while you
// sit in one cafe for an hour into a single point.
const simplifyEpsilonDeg = 0.0001

// BuildDayPath assembles the GeoJSON for one day from raw points and photo positions. Points are
// sorted by time, cleaned of glitches (clean.go) and simplified; photos become Point features with
// the frame hash as id so the app can tap a marker and open the preview. The points may reach a
// little outside the day: the glitch rules look at what comes before and after a hop, and a spike
// at midnight is judged by its neighbours across the boundary; only the day's own points are drawn
// and counted.
func BuildDayPath(day time.Time, points []TrackPoint, photos []PhotoPoint) ([]byte, error) {
	dayStart := time.Date(day.UTC().Year(), day.UTC().Month(), day.UTC().Day(), 0, 0, 0, 0, time.UTC).Unix()
	dayEnd := dayStart + 86400
	inDay := func(pts []TrackPoint) []TrackPoint {
		out := pts[:0:0]
		for _, p := range pts {
			if p.TS >= dayStart && p.TS < dayEnd {
				out = append(out, p)
			}
		}
		return out
	}
	raw := inDay(points)
	cleaned, _ := CleanTrack(points)
	points = inDay(cleaned)
	glitches := len(raw) - len(points)
	simplified := douglasPeucker(points, simplifyEpsilonDeg)

	type feature struct {
		Type       string         `json:"type"`
		Geometry   map[string]any `json:"geometry"`
		Properties map[string]any `json:"properties"`
	}
	var features []feature

	if len(simplified) >= 2 {
		coords := make([][2]float64, len(simplified))
		times := make([]int64, len(simplified))
		for i, p := range simplified {
			coords[i] = [2]float64{p.Lon, p.Lat} // GeoJSON is lon,lat
			times[i] = p.TS
		}
		features = append(features, feature{
			Type:     "Feature",
			Geometry: map[string]any{"type": "LineString", "coordinates": coords},
			Properties: map[string]any{
				"kind": "track", "day": day.Format("2006-01-02"),
				"points": len(raw), "simplified": len(simplified),
				// glitches is how many of the day's raw points the rules in clean.go threw out ,
				// spikes to a cell tower and back, hops faster than anything flies.
				"glitches": glitches,
				// times is parallel to coordinates , the second each kept vertex was recorded ,
				// so the map can put a clock on any point of the line (scrub along the day) and
				// say when the day's movement began and ended. distanceM is over the cleaned but
				// UNSIMPLIFIED points: the simplified line cuts corners, and a distance is a
				// claim, not a drawing.
				"times":     times,
				"distanceM": math.Round(TrackDistanceM(points)),
			},
		})
	}
	for _, ph := range photos {
		features = append(features, feature{
			Type:     "Feature",
			Geometry: map[string]any{"type": "Point", "coordinates": [2]float64{ph.Lon, ph.Lat}},
			Properties: map[string]any{
				"kind": "photo", "hash": ph.Hash, "takenAt": ph.TakenAt,
			},
		})
	}
	doc := map[string]any{"type": "FeatureCollection", "features": features}
	return json.MarshalIndent(doc, "", "  ")
}

// TrackDistanceM is the length of a time-ordered track in metres by the haversine formula, with the
// GPS jitter of standing still left out: a hop under 15m between consecutive samples (a quarter
// hour apart, so anything real is further) is not counted, or a day in a café walks a kilometre.
// Callers pass cleaned points (CleanTrack): a distance is a claim, and a spike is not a journey.
func TrackDistanceM(pts []TrackPoint) float64 {
	total := 0.0
	for i := 1; i < len(pts); i++ {
		if d := HaversineM(pts[i-1], pts[i]); d >= 15 {
			total += d
		}
	}
	return total
}

// douglasPeucker simplifies a polyline to within eps (in degrees, planar approximation , fine at the
// scale of a day's movement; we are drawing a journal map, not navigating a ship).
func douglasPeucker(pts []TrackPoint, eps float64) []TrackPoint {
	if len(pts) < 3 {
		return pts
	}
	// Find the point furthest from the first-last chord.
	first, last := pts[0], pts[len(pts)-1]
	maxDist, maxIdx := 0.0, 0
	for i := 1; i < len(pts)-1; i++ {
		d := perpDistDeg(pts[i], first, last)
		if d > maxDist {
			maxDist, maxIdx = d, i
		}
	}
	if maxDist <= eps {
		return []TrackPoint{first, last}
	}
	left := douglasPeucker(pts[:maxIdx+1], eps)
	right := douglasPeucker(pts[maxIdx:], eps)
	return append(left[:len(left)-1], right...)
}

// perpDistDeg is the perpendicular distance from p to the line a-b, in degrees, with longitude scaled
// by cos(lat) so a degree east counts the same as a degree north at this latitude.
func perpDistDeg(p, a, b TrackPoint) float64 {
	scale := math.Cos(a.Lat * math.Pi / 180)
	px, py := p.Lon*scale, p.Lat
	ax, ay := a.Lon*scale, a.Lat
	bx, by := b.Lon*scale, b.Lat
	dx, dy := bx-ax, by-ay
	if dx == 0 && dy == 0 {
		return math.Hypot(px-ax, py-ay)
	}
	t := ((px-ax)*dx + (py-ay)*dy) / (dx*dx + dy*dy)
	if t < 0 {
		t = 0
	} else if t > 1 {
		t = 1
	}
	return math.Hypot(px-(ax+t*dx), py-(ay+t*dy))
}
