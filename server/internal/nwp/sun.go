package nwp

import (
	"math"
	"time"
)

// SunTimes is sunrise and sunset (UTC) on a date at a point, by the NOAA solar equations
// (the sun's centre 50 arc minutes below the horizon, refraction included); ok false where
// the sun does not rise or set that day (the polar night or the midnight sun).
func SunTimes(date time.Time, lat, lon float64) (rise, set time.Time, ok bool) {
	y, m, d := date.Date()
	noon := time.Date(y, m, d, 12, 0, 0, 0, time.UTC)
	eq, decl := solar(noon)
	rad := math.Pi / 180
	cosHA := (math.Cos(90.833*rad) / (math.Cos(lat*rad) * math.Cos(decl*rad))) - math.Tan(lat*rad)*math.Tan(decl*rad)
	if cosHA > 1 || cosHA < -1 {
		return rise, set, false
	}
	ha := math.Acos(cosHA) / rad
	solarNoon := 720 - 4*lon - eq // minutes from midnight UTC
	riseMin := solarNoon - ha*4
	setMin := solarNoon + ha*4
	midnight := time.Date(y, m, d, 0, 0, 0, 0, time.UTC)
	rise = midnight.Add(time.Duration(riseMin * float64(time.Minute)))
	set = midnight.Add(time.Duration(setMin * float64(time.Minute)))
	return rise, set, true
}

// solar is the equation of time (minutes) and the sun's declination (degrees) at t, by the NOAA
// solar equations.
func solar(t time.Time) (eqTime, decl float64) {
	jc := (julianDay(t) - 2451545) / 36525
	gml := math.Mod(280.46646+jc*(36000.76983+jc*0.0003032), 360)
	gma := 357.52911 + jc*(35999.05029-0.0001537*jc)
	e := 0.016708634 - jc*(0.000042037+0.0000001267*jc)
	rad := math.Pi / 180
	c := math.Sin(gma*rad)*(1.914602-jc*(0.004817+0.000014*jc)) + math.Sin(2*gma*rad)*(0.019993-0.000101*jc) + math.Sin(3*gma*rad)*0.000289
	tl := gml + c
	omega := 125.04 - 1934.136*jc
	apparent := tl - 0.00569 - 0.00478*math.Sin(omega*rad)
	obl := 23 + (26+(21.448-jc*(46.815+jc*(0.00059-jc*0.001813)))/60)/60
	oblCorr := obl + 0.00256*math.Cos(omega*rad)
	decl = math.Asin(math.Sin(oblCorr*rad)*math.Sin(apparent*rad)) / rad
	yy := math.Tan(oblCorr/2*rad) * math.Tan(oblCorr/2*rad)
	eqTime = 4 / rad * (yy*math.Sin(2*gml*rad) - 2*e*math.Sin(gma*rad) + 4*e*yy*math.Sin(gma*rad)*math.Cos(2*gml*rad) - 0.5*yy*yy*math.Sin(4*gml*rad) - 1.25*e*e*math.Sin(2*gma*rad))
	return
}

// SunElevation is how high the sun's centre stands above the horizon at t over a point, in
// degrees (negative below it), by the same equations as SunTimes; no refraction, which matters
// only within a degree of the horizon.
func SunElevation(t time.Time, lat, lon float64) float64 {
	t = t.UTC()
	eq, decl := solar(t)
	rad := math.Pi / 180
	minutes := float64(t.Hour()*60+t.Minute()) + float64(t.Second())/60
	tst := math.Mod(minutes+eq+4*lon+1440, 1440) // true solar time, minutes
	ha := tst/4 - 180                            // hour angle, degrees: 0 at solar noon
	sinEl := math.Sin(lat*rad)*math.Sin(decl*rad) + math.Cos(lat*rad)*math.Cos(decl*rad)*math.Cos(ha*rad)
	return math.Asin(math.Max(-1, math.Min(1, sinEl))) / rad
}

func julianDay(t time.Time) float64 {
	return float64(t.Unix())/86400 + 2440587.5
}
