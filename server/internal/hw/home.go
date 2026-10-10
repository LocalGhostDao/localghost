package hw

import (
	"math"
	"strconv"
	"time"
)

// HomeGuess is where the phone spends its nights: of the trail's points of the last sixty
// days that fall between one and five in the morning by the sun (the longitude's hour, near
// enough for a night), the 0.02° cell with the most distinct nights, as its centre; ok false
// under three nights. Nothing leaves the box with it; it is what the weather's Met Office
// order wants a region around, and SETTINGS says it so the person can type it there.
func HomeGuess(c Querier, now time.Time) (lat, lon float64, nights int, ok bool) {
	rows, err := c.Query("SELECT ts, lat, lon FROM location_points WHERE ts > $1", now.Add(-60*24*time.Hour).Unix())
	if err != nil {
		return 0, 0, 0, false
	}
	type cell struct{ i, j int }
	nightsOf := map[cell]map[string]bool{}
	for _, v := range rows.Vals {
		if len(v) < 3 || v[0] == nil || v[1] == nil || v[2] == nil {
			continue
		}
		ts, _ := strconv.ParseInt(*v[0], 10, 64)
		la, _ := strconv.ParseFloat(*v[1], 64)
		lo, _ := strconv.ParseFloat(*v[2], 64)
		if la == 0 && lo == 0 {
			continue
		}
		local := time.Unix(ts+int64(lo*240), 0).UTC() // the sun's hour: 4 minutes a degree
		if h := local.Hour(); h < 1 || h > 5 {
			continue
		}
		k := cell{int(math.Floor(la / 0.02)), int(math.Floor(lo / 0.02))}
		if nightsOf[k] == nil {
			nightsOf[k] = map[string]bool{}
		}
		nightsOf[k][local.Format("2006-01-02")] = true
	}
	var best cell
	for k, n := range nightsOf {
		if len(n) > nights || (len(n) == nights && (k.i < best.i || (k.i == best.i && k.j < best.j))) {
			best, nights = k, len(n)
		}
	}
	if nights < 3 {
		return 0, 0, nights, false
	}
	return (float64(best.i) + 0.5) * 0.02, (float64(best.j) + 0.5) * 0.02, nights, true
}
