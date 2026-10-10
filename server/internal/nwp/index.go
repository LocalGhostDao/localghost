package nwp

import (
	"fmt"
	"math"
	"sort"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/weather"
)

// THE INDEX. For every place and every hour of the next days, the models' values are read
// from their newest runs (a 3-hourly model interpolated to the hour, the precipitation from
// its cumulative curve), each model's temperature moved from its own ground height to the
// place's (the standard lapse rate over the difference, when the box has the heights), then
// blended: a weighted mean with a model that stands alone far from the others left out and
// noted, the disagreement kept as the spread. The weather word comes from the blended
// fields by a fixed table, the days from the hours, the sun from the place and the date.

// ZoneFinder names the time zone at a point ("" for none): internal/tzgrid's Lookup.
type ZoneFinder interface {
	Zone(lat, lon float64) string
}

// Heights is the ground's height at a point: internal/dem's Set.
type Heights interface {
	Height(lat, lon float64) (float64, bool)
}

// LapseRate is how much colder the air is for every metre up, in the standard atmosphere.
const LapseRate = 0.0065

// Outlier is how far (°C) a model may stand from the others' median before it is left out of
// the blend; it takes three models for anyone to be alone.
const Outlier = 4.0

// hourly is one hour's blended values.
type hourly struct {
	at                time.Time
	temp, dew, precip float64
	windKmh, cloud    float64
	rainPct           int
	spread            float64 // the models' temperature range
	models            int
	ok                bool
}

// modelHour is one model's reading for an hour.
type modelHour struct {
	weight              float64
	temp, dew, precip   float64
	windU, windV, cloud float64
	hasDew, hasPrecip   bool
	hasWind, hasCloud   bool
}

// modelRun is a run with its model and its cells by id.
type modelRun struct {
	m     Model
	r     *Run
	index map[int64]int
}

// prepare pairs the runs with their models and names the newest of each.
func prepare(runs []*Run) (mrs []modelRun, source string) {
	var names []string
	named := map[string]bool{}
	for _, r := range runs {
		m, ok := ModelByID(r.Model)
		if !ok {
			continue
		}
		idx := make(map[int64]int, len(r.Cells))
		for i, c := range r.Cells {
			idx[c.ID] = i
		}
		mrs = append(mrs, modelRun{m, r, idx})
		if !named[m.ID] {
			named[m.ID] = true
			names = append(names, RunName(r))
		}
	}
	return mrs, strings.Join(names, " · ")
}

// Compute is the forecast for every place from the runs (each model's newest first, the run
// before it after, as Available gives them: an hour is read from the newest run of a model
// that covers it), at now. Places with no model covering them are left out. The runs' cells
// must be the places (by id); a run missing a place gives nothing for it.
func Compute(runs []*Run, places []weather.Place, now time.Time, zones ZoneFinder, heights Heights) []weather.Forecast {
	mrs, source := prepare(runs)
	if len(mrs) == 0 {
		return nil
	}
	out := make([]weather.Forecast, 0, len(places))
	for _, p := range places {
		pick := func(mr modelRun) (reading, bool) {
			c, ok := mr.index[p.ID]
			if !ok {
				return nil, false
			}
			return cellReading{mr.r, c}, true
		}
		if f, ok := forecastAt(mrs, p, pick, now, zones, heights, source); ok {
			out = append(out, f)
		}
	}
	return out
}

// ComputeAt is the forecast at one point, the phone's fix, read from each run's window
// around it (the model's own grid, the exact height of the ground there from the box's
// heights): finer than the nearest cell's town. The place carries the fix as its position and
// the nearest listed town's name; ok false when no run's window covers the point.
func ComputeAt(runs []*Run, p weather.Place, now time.Time, zones ZoneFinder, heights Heights) (weather.Forecast, bool) {
	mrs, _ := prepare(runs)
	var names []string
	named := map[string]bool{}
	for _, mr := range mrs {
		if mr.r.Win != nil && mr.r.Win.Covers(p.Lat, p.Lon) && !named[mr.m.ID] {
			named[mr.m.ID] = true
			names = append(names, RunName(mr.r))
		}
	}
	if len(names) == 0 {
		return weather.Forecast{}, false
	}
	pick := func(mr modelRun) (reading, bool) {
		if mr.r.Win == nil || !mr.r.Win.Covers(p.Lat, p.Lon) {
			return nil, false
		}
		return windowReading{mr.r.Win, p.Lat, p.Lon}, true
	}
	f, ok := forecastAt(mrs, p, pick, now, zones, heights, strings.Join(names, " · "))
	f.Here = true
	return f, ok
}

// forecastAt blends the runs for one place, each run read through pick.
func forecastAt(mrs []modelRun, p weather.Place, pick func(modelRun) (reading, bool), now time.Time, zones ZoneFinder, heights Heights, source string) (weather.Forecast, bool) {
	loc := zoneOf(zones, p.Lat, p.Lon)
	local := now.In(loc)
	start := time.Date(local.Year(), local.Month(), local.Day(), 0, 0, 0, 0, loc)
	var height float64
	hasHeight := false
	if heights != nil {
		height, hasHeight = heights.Height(p.Lat, p.Lon)
	}
	hours := make([]hourly, 0, weather.HourDays*24)
	for h := 0; h < weather.Days*24; h++ {
		at := start.Add(time.Duration(h) * time.Hour)
		var readings []modelHour
		read := map[string]bool{}
		for _, mr := range mrs {
			if read[mr.m.ID] || !mr.m.Domain.Has(p.Lat, p.Lon) {
				continue
			}
			rd, ok := pick(mr)
			if !ok {
				continue
			}
			// the hour must be after the run (the hour the run starts at has no rain in it
			// yet; the run before serves that one), and within the model's reach
			lead := at.Sub(mr.r.At).Hours()
			if lead <= 0 || (mr.m.MaxLead > 0 && lead > float64(mr.m.MaxLead)) {
				continue
			}
			if mh, ok := readAt(mr.r, rd, lead, mr.m.Weight, height, hasHeight); ok {
				readings = append(readings, mh)
				read[mr.m.ID] = true
			}
		}
		hours = append(hours, blend(at, readings))
	}
	return assemble(p, loc, start, hours, now, source)
}

func zoneOf(zones ZoneFinder, lat, lon float64) *time.Location {
	if zones != nil {
		if name := zones.Zone(lat, lon); name != "" {
			if loc, err := time.LoadLocation(name); err == nil {
				return loc
			}
		}
	}
	// no zone set on the box: the sun's hour from the longitude, whole hours
	off := int(math.Round(lon/15)) * 3600
	return time.FixedZone(fmt.Sprintf("UTC%+d", off/3600), off)
}

// readAt is a model's reading at a lead time (hours from the run), interpolated between its
// steps; false when the model has no steps around it.
func readAt(r *Run, rd reading, lead, weight, height float64, hasHeight bool) (modelHour, bool) {
	// the steps around the lead
	j := sort.SearchInts(r.Steps, int(math.Ceil(lead-1e-9)))
	if j >= len(r.Steps) {
		return modelHour{}, false
	}
	i := j
	if float64(r.Steps[j]) > lead && j > 0 {
		i = j - 1
	}
	s0, s1 := r.Steps[i], r.Steps[j]
	t := 0.0
	if s1 > s0 {
		t = (lead - float64(s0)) / float64(s1-s0)
	}
	lerp := func(f Field) (float64, bool) {
		a, oka := rd.value(i, f)
		b, okb := rd.value(j, f)
		switch {
		case oka && okb:
			return a*(1-t) + b*t, true
		case oka && t < 0.5:
			return a, true
		case okb && t >= 0.5:
			return b, true
		}
		return 0, false
	}
	temp, ok := lerp(Temp2m)
	if !ok {
		return modelHour{}, false
	}
	mh := modelHour{weight: weight, temp: temp}
	// the model's ground to the place's: colder up, warmer down, by the standard lapse rate
	orog := rd.orog()
	if hasHeight && !math.IsNaN(orog) {
		mh.temp += LapseRate * (orog - height)
	}
	if v, ok := lerp(Dew2m); ok {
		mh.dew, mh.hasDew = v, true
		if hasHeight && !math.IsNaN(orog) {
			mh.dew += LapseRate * 0.3 * (orog - height) // the dew point falls more slowly
		}
		if mh.dew > mh.temp {
			mh.dew = mh.temp
		}
	}
	if u, ok := lerp(WindU); ok {
		if v, ok := lerp(WindV); ok {
			mh.windU, mh.windV, mh.hasWind = u, v, true
		}
	}
	if v, ok := lerp(Cloud); ok {
		mh.cloud, mh.hasCloud = v, true
	}
	// the hour's precipitation: the cumulative curve's rise over the interval the lead falls
	// in (the one ending at it when the lead is a step itself), an hour's share of it
	pi := i
	if pi == j && j > 0 {
		pi = j - 1
	}
	if c0, ok0 := r.cumulative(rd, pi); ok0 {
		if c1, ok1 := r.cumulative(rd, j); ok1 {
			rate := 0.0
			if r.Steps[j] > r.Steps[pi] {
				rate = (c1 - c0) / float64(r.Steps[j]-r.Steps[pi])
			}
			if rate < 0 {
				rate = 0
			}
			mh.precip, mh.hasPrecip = rate, true
		}
	}
	return mh, true
}

// blend combines the models' readings for an hour.
func blend(at time.Time, rs []modelHour) hourly {
	h := hourly{at: at}
	if len(rs) == 0 {
		return h
	}
	// a model alone far from the others' median is left out (three or more to tell)
	use := rs
	if len(rs) >= 3 {
		temps := make([]float64, len(rs))
		for i, r := range rs {
			temps[i] = r.temp
		}
		sort.Float64s(temps)
		median := temps[len(temps)/2]
		use = use[:0:0]
		for _, r := range rs {
			if math.Abs(r.temp-median) <= Outlier {
				use = append(use, r)
			}
		}
		if len(use) == 0 {
			use = rs
		}
	}
	var wt, wdew, wprec, wwind, wcloud float64
	var temp, dew, precip, u, v, cloud, wetVotes float64
	lo, hi := math.Inf(1), math.Inf(-1)
	for _, r := range use {
		wt += r.weight
		temp += r.temp * r.weight
		lo = math.Min(lo, r.temp)
		hi = math.Max(hi, r.temp)
		if r.hasDew {
			wdew += r.weight
			dew += r.dew * r.weight
		}
		if r.hasPrecip {
			wprec += r.weight
			precip += r.precip * r.weight
			if r.precip+1e-6 >= 0.1 {
				wetVotes += r.weight
			}
		}
		if r.hasWind {
			wwind += r.weight
			u += r.windU * r.weight
			v += r.windV * r.weight
		}
		if r.hasCloud {
			wcloud += r.weight
			cloud += r.cloud * r.weight
		}
	}
	h.ok = true
	h.models = len(use)
	h.temp = temp / wt
	h.spread = hi - lo
	if wdew > 0 {
		h.dew = dew / wdew
	} else {
		h.dew = h.temp - 5
	}
	if wprec > 0 {
		h.precip = precip / wprec
		h.rainPct = int(math.Round(100 * wetVotes / wprec))
	} else {
		h.rainPct = -1
	}
	if wwind > 0 {
		h.windKmh = math.Hypot(u/wwind, v/wwind) * 3.6
	}
	if wcloud > 0 {
		h.cloud = cloud / wcloud
	}
	return h
}

// Humidity is the relative humidity (%) from the temperature and the dew point (Magnus).
func Humidity(tempC, dewC float64) int {
	e := func(t float64) float64 { return math.Exp(17.625 * t / (243.04 + t)) }
	rh := 100 * e(dewC) / e(tempC)
	return int(math.Round(math.Max(1, math.Min(100, rh))))
}

// FeelsLike is the apparent temperature (the Australian Bureau of Meteorology's, without
// radiation): the humidity's vapour pressure warms it, the wind cools it.
func FeelsLike(tempC float64, humidity int, windKmh float64) float64 {
	ws := windKmh / 3.6
	e := float64(humidity) / 100 * 6.105 * math.Exp(17.27*tempC/(237.7+tempC))
	return tempC + 0.33*e - 0.70*ws - 4.0
}

// Code is the WMO-like weather code the phone maps (the ones Open-Meteo used): the sky from
// the cloud cover, the precipitation by its rate and the temperature, fog from a saturated
// still air.
func Code(tempC, dewC, precipMMh, cloudPct, windKmh float64) int {
	switch {
	case precipMMh >= 0.1:
		snow := tempC <= 0.5
		switch {
		case precipMMh < 0.5 && !snow:
			return 51 // light drizzle
		case precipMMh < 1.0 && !snow:
			return 53
		case snow && precipMMh < 1.0:
			return 71
		case snow && precipMMh < 3.0:
			return 73
		case snow:
			return 75
		case tempC < 2.0 && precipMMh >= 1.0:
			return 66 // freezing or sleety rain
		case precipMMh < 2.5:
			return 61
		case precipMMh < 7.5:
			return 63
		default:
			return 65
		}
	case Humidity(tempC, dewC) >= 97 && windKmh < 8 && cloudPct >= 60:
		return 45 // fog
	case cloudPct < 15:
		return 0
	case cloudPct < 45:
		return 1
	case cloudPct < 80:
		return 2
	default:
		return 3
	}
}

// assemble is the forecast rows from the blended hours.
func assemble(p weather.Place, loc *time.Location, start time.Time, hours []hourly, now time.Time, source string) (weather.Forecast, bool) {
	f := weather.Forecast{Place: p, Timezone: loc.String(), FetchedAt: now.Unix(), Source: source}
	_, off := start.Zone()
	f.UTCOffset = off
	anyHour := false
	for i, h := range hours {
		if !h.ok {
			continue
		}
		anyHour = true
		if i < weather.HourDays*24 {
			f.Hours = append(f.Hours, weather.Hour{At: h.at.Format("2006-01-02T15:04"), TempC: round1(h.temp), RainPct: h.rainPct,
				PrecipMM: round1(h.precip), Code: Code(h.temp, h.dew, h.precip, h.cloud, h.windKmh), WindKmh: round1(h.windKmh), SpreadC: round1(h.spread)})
		}
	}
	if !anyHour {
		return f, false
	}
	// the days: max and min, the sum of rain, the chance as the wettest hour's, the code from
	// the day's rain or the daytime's sky
	for d := 0; d < weather.Days; d++ {
		var day weather.Day
		date := start.AddDate(0, 0, d)
		day.Date = date.Format("2006-01-02")
		day.MaxC, day.MinC = math.Inf(-1), math.Inf(1)
		day.RainPct = -1
		codes := map[int]int{}
		var wettest, rate float64
		var temp, dew, cloud float64
		n := 0
		for i := d * 24; i < (d+1)*24 && i < len(hours); i++ {
			h := hours[i]
			if !h.ok {
				continue
			}
			n++
			day.MaxC = math.Max(day.MaxC, h.temp)
			day.MinC = math.Min(day.MinC, h.temp)
			day.PrecipMM += h.precip
			if h.rainPct > day.RainPct {
				day.RainPct = h.rainPct
			}
			if h.precip > rate {
				rate, wettest = h.precip, h.temp
			}
			if hr := i % 24; hr >= 7 && hr <= 19 {
				codes[Code(h.temp, h.dew, 0, h.cloud, h.windKmh)]++
				temp += h.temp
				dew += h.dew
				cloud += h.cloud
			}
		}
		if n == 0 {
			break
		}
		if day.PrecipMM >= 1.0 && rate > 0 {
			day.Code = Code(wettest, wettest, rate, 100, 0)
		} else {
			best, bestN := 0, -1
			for c, k := range codes {
				if k > bestN || (k == bestN && c > best) {
					best, bestN = c, k
				}
			}
			day.Code = best
		}
		day.MaxC, day.MinC, day.PrecipMM = round1(day.MaxC), round1(day.MinC), round1(day.PrecipMM)
		rise, set, ok := SunTimes(date, p.Lat, p.Lon)
		if ok {
			day.Sunrise, day.Sunset = rise.In(loc).Format("15:04"), set.In(loc).Format("15:04")
		}
		f.Days = append(f.Days, day)
	}
	// now: the hour the clock is in
	i := int(now.In(loc).Sub(start).Hours())
	if i >= 0 && i < len(hours) && hours[i].ok {
		h := hours[i]
		hum := Humidity(h.temp, h.dew)
		f.Now = weather.Now{At: now.In(loc).Format("2006-01-02T15:04"), TempC: round1(h.temp), FeelsC: round1(FeelsLike(h.temp, hum, h.windKmh)),
			Humidity: hum, PrecipMM: round1(h.precip), Code: Code(h.temp, h.dew, h.precip, h.cloud, h.windKmh), WindKmh: round1(h.windKmh)}
	}
	return f, true
}

func round1(v float64) float64 { return math.Round(v*10) / 10 }
