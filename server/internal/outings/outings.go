// Package outings turns the photo archive into MEMORIES OF PLACES AND DAYS, and those into a
// picture of what the person likes , deterministically, from what the box already holds, with no
// model call. A photo archive is a diary nobody wrote: where the phone was, when, how many times it
// came out of the pocket, and (from the tags the pipeline gave each frame) what it saw. Grouping
// frames into OUTINGS , runs of photos close in time and place , gives one memory per trip or
// notable day ("Antipaxos · 19-23 Sep 2026 · 114 photos · mostly beaches, boats and the sea"); the
// tags across every day with a camera out give the TASTE ("you photograph the sea, beaches and
// boats most"); and the taste, laid over the GeoNames spots the box already has for geocoding,
// gives the box something to say when the person is somewhere new: "a beach 2 km north-east you
// have never photographed". Nothing here touches the network; the raw frames stay what they are.
package outings

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"
)

// Frame is one photo as the pipeline knows it.
type Frame struct {
	Hash    string
	TakenAt int64 // unix seconds
	Lat     float64
	Lon     float64
	HasGPS  bool
	Place   string // the on-box hierarchy, "Europe / Greece / Ionian Islands / Antipaxos"; '' when unknown
	Tags    []Tag
}

// Tag is one tag with its category (” when not yet assigned).
type Tag struct {
	Name     string `json:"tag"`
	Category string `json:"category,omitempty"`
}

// TagCount is a tag and how many of the outing's photos carry it.
type TagCount struct {
	Tag      string `json:"tag"`
	Category string `json:"category,omitempty"`
	Photos   int    `json:"photos"`
}

// Outing is one memory-worth of photos: a trip, or a notable run of days at home.
type Outing struct {
	Ref       string     `json:"ref"` // "outing:<start day>", the memory's source_ref
	Start     int64      `json:"start"`
	End       int64      `json:"end"`
	Days      int        `json:"days"`
	Photos    int        `json:"photos"`
	Lat       float64    `json:"lat"`
	Lon       float64    `json:"lon"`
	Place     string     `json:"place"`     // the most photographed place's last name, '' when unknown
	Country   string     `json:"country"`   // from the hierarchy, '' when unknown
	Places    []string   `json:"places"`    // distinct place names, most photographed first (≤ 4)
	Away      bool       `json:"away"`      // farther than awayKm from home (false when home is unknown)
	FromHomeM float64    `json:"fromHomeM"` // 0 when home is unknown
	Tags      []TagCount `json:"tags"`      // top tags, most frequent first
	Cover     string     `json:"cover"`     // the frame that carries the most tags
	Covers    []string   `json:"covers"`    // up to six frames spread across the outing, cover first
	DistanceM float64    `json:"distanceM"` // on foot/wheels over the outing's days; the caller fills it from the trail
	Hashes    []string   `json:"-"`         // every frame, for the caller's follow-up queries
}

// Home is where the person photographs on the most distinct days: a ~5 km cell.
type Home struct {
	Known bool
	Lat   float64
	Lon   float64
	Days  int
}

const (
	gapS       = 36 * 3600 // a silence this long ends an outing
	splitKm    = 30.0      // a photo this far from the outing's centre starts a new one
	maxSpanS   = 10 * 86400
	minPhotos  = 3
	awayKm     = 25.0
	homeCell   = 0.05 // degrees, ~5.5 km of latitude
	homeMinDay = 5    // fewer distinct days than this and there is no home yet
	maxCovers  = 6
	maxTags    = 24
)

// Build groups time-ordered frames into outings and finds home. Frames without a time are ignored.
func Build(frames []Frame) ([]Outing, Home) {
	fs := make([]Frame, 0, len(frames))
	for _, f := range frames {
		if f.TakenAt > 0 {
			fs = append(fs, f)
		}
	}
	sort.SliceStable(fs, func(i, j int) bool { return fs[i].TakenAt < fs[j].TakenAt })
	home := findHome(fs)

	var groups [][]Frame
	var cur []Frame
	var cLat, cLon float64 // running centroid of the geotagged frames in cur
	var cN int
	flush := func() {
		if len(cur) > 0 {
			groups = append(groups, cur)
		}
		cur, cLat, cLon, cN = nil, 0, 0, 0
	}
	for _, f := range fs {
		if len(cur) > 0 {
			last := cur[len(cur)-1]
			split := f.TakenAt-last.TakenAt > gapS || f.TakenAt-cur[0].TakenAt > maxSpanS
			if !split && f.HasGPS && cN > 0 && distKm(cLat, cLon, f.Lat, f.Lon) > splitKm {
				split = true
			}
			if split {
				flush()
			}
		}
		cur = append(cur, f)
		if f.HasGPS {
			cN++
			cLat += (f.Lat - cLat) / float64(cN)
			cLon += (f.Lon - cLon) / float64(cN)
		}
	}
	flush()

	var out []Outing
	for _, g := range groups {
		if len(g) < minPhotos {
			continue
		}
		out = append(out, summarize(g, home))
	}
	return out, home
}

// findHome is the ~5 km cell with the most distinct photo days.
func findHome(fs []Frame) Home {
	type cell struct {
		days     map[string]bool
		lat, lon float64
		n        int
	}
	cells := map[[2]int]*cell{}
	for _, f := range fs {
		if !f.HasGPS {
			continue
		}
		k := [2]int{int(math.Round(f.Lat / homeCell)), int(math.Round(f.Lon / homeCell))}
		c := cells[k]
		if c == nil {
			c = &cell{days: map[string]bool{}}
			cells[k] = c
		}
		c.days[dayOf(f.TakenAt)] = true
		c.n++
		c.lat += (f.Lat - c.lat) / float64(c.n)
		c.lon += (f.Lon - c.lon) / float64(c.n)
	}
	best := Home{}
	for _, c := range cells {
		if len(c.days) > best.Days {
			best = Home{Known: true, Lat: c.lat, Lon: c.lon, Days: len(c.days)}
		}
	}
	if best.Days < homeMinDay {
		return Home{}
	}
	return best
}

func summarize(g []Frame, home Home) Outing {
	o := Outing{Start: g[0].TakenAt, End: g[len(g)-1].TakenAt, Photos: len(g)}
	o.Ref = "outing:" + dayOf(o.Start)
	o.Days = daysBetween(o.Start, o.End)
	// centre and places
	var n int
	placeCount := map[string]int{}
	var placeFull string
	placeFullCount := 0
	for _, f := range g {
		o.Hashes = append(o.Hashes, f.Hash)
		if f.HasGPS {
			n++
			o.Lat += (f.Lat - o.Lat) / float64(n)
			o.Lon += (f.Lon - o.Lon) / float64(n)
		}
		if f.Place != "" {
			placeCount[f.Place]++
			if placeCount[f.Place] > placeFullCount {
				placeFull, placeFullCount = f.Place, placeCount[f.Place]
			}
		}
	}
	if placeFull != "" {
		parts := strings.Split(placeFull, " / ")
		o.Place = strings.TrimSpace(parts[len(parts)-1])
		if len(parts) >= 2 {
			o.Country = strings.TrimSpace(parts[1])
		}
	}
	type pc struct {
		name string
		n    int
	}
	var pcs []pc
	seen := map[string]bool{}
	for p, c := range placeCount {
		parts := strings.Split(p, " / ")
		name := strings.TrimSpace(parts[len(parts)-1])
		if name == "" || seen[name] {
			continue
		}
		seen[name] = true
		pcs = append(pcs, pc{name, c})
	}
	sort.Slice(pcs, func(i, j int) bool { return pcs[i].n > pcs[j].n || (pcs[i].n == pcs[j].n && pcs[i].name < pcs[j].name) })
	for i, p := range pcs {
		if i == 4 {
			break
		}
		o.Places = append(o.Places, p.name)
	}
	if home.Known && n > 0 {
		o.FromHomeM = distKm(home.Lat, home.Lon, o.Lat, o.Lon) * 1000
		o.Away = o.FromHomeM > awayKm*1000
	}
	// tags
	counts := map[Tag]int{}
	for _, f := range g {
		for _, t := range f.Tags {
			counts[t]++
		}
	}
	for t, c := range counts {
		o.Tags = append(o.Tags, TagCount{Tag: t.Name, Category: t.Category, Photos: c})
	}
	sort.Slice(o.Tags, func(i, j int) bool {
		if o.Tags[i].Photos != o.Tags[j].Photos {
			return o.Tags[i].Photos > o.Tags[j].Photos
		}
		return o.Tags[i].Tag < o.Tags[j].Tag
	})
	if len(o.Tags) > maxTags {
		o.Tags = o.Tags[:maxTags]
	}
	// covers: the frame with the most tags first, then frames spread evenly across the outing
	best, bestN := 0, -1
	for i, f := range g {
		if len(f.Tags) > bestN {
			best, bestN = i, len(f.Tags)
		}
	}
	o.Cover = g[best].Hash
	o.Covers = []string{o.Cover}
	if len(g) > 1 {
		want := maxCovers
		if len(g) < want {
			want = len(g)
		}
		for k := 0; k < want && len(o.Covers) < want; k++ {
			i := k * (len(g) - 1) / (want - 1)
			if g[i].Hash != o.Cover {
				o.Covers = append(o.Covers, g[i].Hash)
			}
		}
	}
	return o
}

// Title is the memory's title: the place and the dates.
func (o Outing) Title() string {
	place := o.Place
	if place == "" {
		if o.Lat != 0 || o.Lon != 0 {
			place = fmt.Sprintf("%.2f, %.2f", o.Lat, o.Lon)
		} else {
			place = "No place"
		}
	}
	return place + " · " + DateRange(o.Start, o.End)
}

// Body is the memory's text: counts, the place, what the photos were of, the distance, how far
// from home. Plain template prose from real numbers, so a memory exists the day it happened.
func (o Outing) Body() string {
	var b strings.Builder
	if o.Days <= 1 {
		fmt.Fprintf(&b, "%d photos on one day", o.Photos)
	} else {
		fmt.Fprintf(&b, "%d photos over %d days", o.Photos, o.Days)
	}
	switch {
	case o.Place != "" && o.Country != "" && o.Country != o.Place:
		fmt.Fprintf(&b, " around %s, %s.", o.Place, o.Country)
	case o.Place != "":
		fmt.Fprintf(&b, " around %s.", o.Place)
	default:
		b.WriteString(".")
	}
	mostly, also := o.tagPhrases()
	if len(mostly) > 0 {
		b.WriteString(" Mostly " + joinAnd(mostly))
		if len(also) > 0 {
			b.WriteString("; also " + joinAnd(also))
		}
		b.WriteString(".")
	}
	if len(o.Places) > 1 {
		b.WriteString(" Places: " + strings.Join(o.Places, ", ") + ".")
	}
	if o.DistanceM >= 500 {
		fmt.Fprintf(&b, " %s on the move.", km(o.DistanceM))
	}
	if o.Away {
		fmt.Fprintf(&b, " %s from home.", km(o.FromHomeM))
	}
	return b.String()
}

// tagPhrases picks the tags worth a sentence: the top five as "mostly", the next four as "also",
// leaving out text and style tags (a caption's "black and white" is not a thing you like).
func (o Outing) tagPhrases() (mostly, also []string) {
	for _, t := range o.Tags {
		if t.Category == "text" || t.Category == "style" {
			continue
		}
		if len(mostly) < 5 {
			mostly = append(mostly, t.Tag)
		} else if len(also) < 4 {
			also = append(also, t.Tag)
		}
	}
	return
}

// DateRange renders "21 Sep 2026", "19-23 Sep 2026", "28 Aug - 2 Sep 2026", "30 Dec 2025 - 2 Jan 2026".
func DateRange(start, end int64) string {
	s, e := time.Unix(start, 0).UTC(), time.Unix(end, 0).UTC()
	switch {
	case s.Year() == e.Year() && s.YearDay() == e.YearDay():
		return s.Format("2 Jan 2006")
	case s.Year() == e.Year() && s.Month() == e.Month():
		return fmt.Sprintf("%d-%s", s.Day(), e.Format("2 Jan 2006"))
	case s.Year() == e.Year():
		return s.Format("2 Jan") + " - " + e.Format("2 Jan 2006")
	}
	return s.Format("2 Jan 2006") + " - " + e.Format("2 Jan 2006")
}

func joinAnd(xs []string) string {
	switch len(xs) {
	case 0:
		return ""
	case 1:
		return xs[0]
	case 2:
		return xs[0] + " and " + xs[1]
	}
	return strings.Join(xs[:len(xs)-1], ", ") + " and " + xs[len(xs)-1]
}

func km(m float64) string {
	if m < 950 {
		return fmt.Sprintf("%d m", int(math.Round(m/10)*10))
	}
	if m < 10000 {
		return fmt.Sprintf("%.1f km", m/1000)
	}
	return fmt.Sprintf("%d km", int(math.Round(m/1000)))
}

func dayOf(ts int64) string { return time.Unix(ts, 0).UTC().Format("2006-01-02") }

func daysBetween(start, end int64) int {
	s := time.Unix(start, 0).UTC()
	e := time.Unix(end, 0).UTC()
	sd := time.Date(s.Year(), s.Month(), s.Day(), 0, 0, 0, 0, time.UTC)
	ed := time.Date(e.Year(), e.Month(), e.Day(), 0, 0, 0, 0, time.UTC)
	return int(ed.Sub(sd).Hours()/24) + 1
}

// distKm is the great-circle distance on a 6371 km sphere.
func distKm(lat1, lon1, lat2, lon2 float64) float64 {
	la1, la2 := lat1*math.Pi/180, lat2*math.Pi/180
	dla := la2 - la1
	dlo := (lon2 - lon1) * math.Pi / 180
	h := math.Sin(dla/2)*math.Sin(dla/2) + math.Cos(la1)*math.Cos(la2)*math.Sin(dlo/2)*math.Sin(dlo/2)
	return 2 * 6371 * math.Asin(math.Min(1, math.Sqrt(h)))
}
