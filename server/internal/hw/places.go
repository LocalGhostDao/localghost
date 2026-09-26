package hw

import (
	"strconv"
)

// GeoLabel is one name the map draws: where, what it is, how it ranks against the others in view.
type GeoLabel struct {
	Name string  `json:"name"`
	Lat  float64 `json:"lat"`
	Lon  float64 `json:"lon"`
	// Kind: C a country, R a first-level region, X a capital, P any other populated place.
	Kind string `json:"k"`
	Pop  int64  `json:"pop"`
	Rank int64  `json:"-"`
}

// GeoLabels returns the highest-ranked places inside a lat/lon window, best first: the labels a map
// draws for that view. rank is materialised at import (framed's labelRank), so the partial index on
// (rank DESC) answers a world-sized window in a handful of rows and a small window falls back to the
// lat/lon indexes; either way the query never sorts millions. limit is capped at 200.
func (s *NotifStore) GeoLabels(slot int, minLat, maxLat, minLon, maxLon float64, limit int) ([]GeoLabel, error) {
	c, err := s.pg(slot)
	if err != nil {
		return nil, err
	}
	if limit <= 0 || limit > 200 {
		limit = 200
	}
	// NaN and out-of-range become the world edge (a comparison with NaN is false; see FramesGeoLOD)
	if !(minLat >= -90) {
		minLat = -90
	}
	if !(maxLat <= 90) {
		maxLat = 90
	}
	if !(minLon >= -180) {
		minLon = -180
	}
	if !(maxLon <= 180) {
		maxLon = 180
	}
	rows, err := c.Query(`SELECT name, lat, lon, kind, fcode, population, rank FROM geo_points
		WHERE rank > 0 AND lat BETWEEN $1 AND $2 AND lon BETWEEN $3 AND $4
		ORDER BY rank DESC LIMIT $5`, minLat, maxLat, minLon, maxLon, limit)
	if err != nil {
		return nil, err
	}
	out := make([]GeoLabel, 0, len(rows.Vals))
	for _, r := range rows.Vals {
		if len(r) < 7 || r[0] == nil || r[1] == nil || r[2] == nil {
			continue
		}
		lat, _ := strconv.ParseFloat(*r[1], 64)
		lon, _ := strconv.ParseFloat(*r[2], 64)
		kind, fcode := str(r[3]), str(r[4])
		k := "P"
		switch {
		case kind == "A" && len(fcode) >= 3 && fcode[:3] == "PCL":
			k = "C"
		case kind == "A":
			k = "R"
		case fcode == "PPLC":
			k = "X"
		}
		pop, _ := strconv.ParseInt(str(r[5]), 10, 64)
		rank, _ := strconv.ParseInt(str(r[6]), 10, 64)
		out = append(out, GeoLabel{Name: *r[0], Lat: lat, Lon: lon, Kind: k, Pop: pop, Rank: rank})
	}
	return out, nil
}

func str(p *string) string {
	if p == nil {
		return ""
	}
	return *p
}
