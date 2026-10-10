// Package nwp is the box's own weather forecast: the forecast models' grids pulled from the
// weather centres that publish them (ECMWF's IFS, DWD's ICON-EU, NOAA's GFS), read with
// internal/grib2, reduced to the box's fixed list of places, kept on the volume a run at a
// time, and blended into one forecast per place with the models' disagreement kept beside
// every number, the way the box's crypto index blends the exchanges' tickers. docs/WEATHER.md
// is the design; internal/weather keeps the forecast rows the phone reads, and this package
// writes them in the same shape.
//
// The box pulls the grids itself. Every box asks the same centres for the same global surface
// fields, whoever and wherever the person is; nothing about them is in a request. No key is
// needed for the three models (the Met Office's, which wants one, is not here yet).
package nwp

import (
	"compress/gzip"
	"encoding/gob"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Field is one of the surface fields taken from every model.
type Field int

const (
	Temp2m Field = iota // 2 m temperature, °C (the models give kelvin)
	Dew2m               // 2 m dew point, °C
	Precip              // precipitation accumulated since the run's start, mm
	WindU               // 10 m wind, east component, m/s
	WindV               // 10 m wind, north component, m/s
	Cloud               // total cloud cover, %
	NumFields
)

var fieldNames = [...]string{"2t", "2d", "tp", "10u", "10v", "tcc"}

func (f Field) String() string {
	if int(f) < len(fieldNames) {
		return fieldNames[f]
	}
	return fmt.Sprintf("field%d", int(f))
}

// scale is the integer the run file keeps a field in: the value times it, rounded, in an int16.
func (f Field) scale() float64 {
	switch f {
	case Temp2m, Dew2m:
		return 100 // hundredths of a degree
	case Precip:
		return 10 // tenths of a millimetre: 3,276 mm in a run's five days is more than falls
	case WindU, WindV:
		return 10 // tenths of a metre a second
	}
	return 1 // percent
}

// missing is the run file's value for a point the model did not give.
const missing = math.MinInt16

// Bounds is a model's domain: nil for the globe.
type Bounds struct {
	South, North, West, East float64
}

func (b *Bounds) Has(lat, lon float64) bool {
	if b == nil {
		return true
	}
	return lat >= b.South && lat <= b.North && lon >= b.West && lon <= b.East
}

// Model is one of the forecast models the box pulls, and how.
type Model struct {
	ID     string // "ifs", "icon-eu", "gfs": the run file's name and the card's
	Name   string // "ECMWF IFS"
	Centre string
	// Runs are the hours of the day the model starts; Steps the forecast hours taken from
	// each run; Delay how long after the run's time the centre has it published, so the
	// loop does not ask for what is not there yet.
	Runs  []int
	Steps []int
	Delay time.Duration
	// Domain is where the model answers (nil: everywhere); Weight its say in the blend, and
	// MaxLead the hour past which it has none (0: every step).
	Domain  *Bounds
	Weight  float64
	MaxLead int
	// Regional marks a fine model over one region (ICON-D2 over Germany and its neighbours):
	// pulled only while the phone's last fix is in its domain, since its only use is the
	// forecast where the person is and the cells around them.
	Regional bool
	// Keyed marks a model behind a key the person adds (the Met Office's): pulled only when
	// the box has one.
	Keyed bool
	// Terms is the data's licence line for the notice and the card.
	Terms  string
	layout layout
}

// Cell is a place of the box's list in a run file.
type Cell struct {
	ID       int64
	Lat, Lon float32
	Orog     float32 // the model's ground height there, metres; NaN when the model gives none
}

// Run is one model run reduced to the cells: for every cell and step, the fields.
type Run struct {
	Model   string
	At      time.Time // the run's time (its analysis)
	Fetched time.Time
	Steps   []int // forecast hours, ascending
	// PrecipFrom is, for each step, the hour the precipitation accumulation starts at: 0 for
	// the models that accumulate from the run (IFS, ICON), the bucket's start for GFS, which
	// empties its bucket every six hours.
	PrecipFrom []int
	Cells      []Cell
	// Vals holds, for cell c, step s and field f, the scaled value at [c*len(Steps)*NumFields
	// + s*NumFields + f]; missing where the model gave none.
	Vals []int16
	// Win is the model's own grid around the phone's last fix at the pull, every field and
	// step, so the forecast where the person is can be read at the exact point (and anywhere
	// within the window when the fix moves) rather than at the nearest cell's town; nil when
	// there was no fix.
	Win *Window
}

// Window is a piece of a model's grid: WinDeg either side of the fix, in the model's own
// resolution, with the ground height.
type Window struct {
	Ni, Nj     int
	Lat1, Lon1 float64 // the first point (north-west corner as the model scans it)
	Di, Dj     float64
	ScanNorth  bool
	Orog       []float32 // Nj × Ni, NaN without
	// Vals holds, for step s, field f, row j and column i, the scaled value at
	// [((s*NumFields+f)*Nj+j)*Ni+i]; missing where the model gave none.
	Vals []int16
}

// WinDeg is how far the window reaches either side of the fix: a little over 60 km, so a
// day's moving about stays inside it between two pulls.
const WinDeg = 0.6

func (w *Window) at(s int, f Field, j, i int) int { return ((s*int(NumFields)+int(f))*w.Nj+j)*w.Ni + i }

// locate is the fractional column and row of a point in the window; ok false outside.
func (w *Window) locate(lat, lon float64) (float64, float64, bool) {
	if w == nil || w.Ni < 2 || w.Nj < 2 {
		return 0, 0, false
	}
	var fj float64
	if w.ScanNorth {
		fj = (lat - w.Lat1) / w.Dj
	} else {
		fj = (w.Lat1 - lat) / w.Dj
	}
	d := lon - w.Lon1
	for d < -180 {
		d += 360
	}
	for d > 180 {
		d -= 360
	}
	fi := d / w.Di
	if fi < 0 || fj < 0 || fi > float64(w.Ni-1) || fj > float64(w.Nj-1) {
		return 0, 0, false
	}
	return fi, fj, true
}

// Covers says whether a point is inside the window.
func (w *Window) Covers(lat, lon float64) bool {
	_, _, ok := w.locate(lat, lon)
	return ok
}

// sample reads a field at a point by bilinear interpolation over the window's four points
// around it; ok false outside or beside a missing point.
func (w *Window) sample(s int, f Field, lat, lon float64) (float64, bool) {
	fi, fj, ok := w.locate(lat, lon)
	if !ok {
		return 0, false
	}
	i0, j0 := int(math.Floor(fi)), int(math.Floor(fj))
	ti, tj := fi-float64(i0), fj-float64(j0)
	i1, j1 := i0+1, j0+1
	if i1 >= w.Ni {
		i1 = i0
	}
	if j1 >= w.Nj {
		j1 = j0
	}
	get := func(j, i int) (float64, bool) {
		v := w.Vals[w.at(s, f, j, i)]
		if v == missing {
			return 0, false
		}
		return float64(v) / f.scale(), true
	}
	v00, ok1 := get(j0, i0)
	v10, ok2 := get(j0, i1)
	v01, ok3 := get(j1, i0)
	v11, ok4 := get(j1, i1)
	if !ok1 || !ok2 || !ok3 || !ok4 {
		return 0, false
	}
	top := v00*(1-ti) + v10*ti
	bot := v01*(1-ti) + v11*ti
	return top*(1-tj) + bot*tj, true
}

// orogAt is the model's ground height at a point in the window, NaN without.
func (w *Window) orogAt(lat, lon float64) float64 {
	fi, fj, ok := w.locate(lat, lon)
	if !ok || w.Orog == nil {
		return math.NaN()
	}
	i, j := int(math.Round(fi)), int(math.Round(fj))
	if i >= w.Ni {
		i = w.Ni - 1
	}
	if j >= w.Nj {
		j = w.Nj - 1
	}
	return float64(w.Orog[j*w.Ni+i])
}

// reading is one run's view of a point: a cell of the list, or the window at the fix.
type reading interface {
	value(s int, f Field) (float64, bool)
	orog() float64
}

type cellReading struct {
	r *Run
	c int
}

func (x cellReading) value(s int, f Field) (float64, bool) { return x.r.Value(x.c, s, f) }
func (x cellReading) orog() float64                        { return float64(x.r.Cells[x.c].Orog) }

type windowReading struct {
	w        *Window
	lat, lon float64
}

func (x windowReading) value(s int, f Field) (float64, bool) { return x.w.sample(s, f, x.lat, x.lon) }
func (x windowReading) orog() float64                        { return x.w.orogAt(x.lat, x.lon) }

func (r *Run) at(c, s int, f Field) int { return (c*len(r.Steps)+s)*int(NumFields) + int(f) }

// Value is the field of cell c at step index s; ok false when missing.
func (r *Run) Value(c, s int, f Field) (float64, bool) {
	v := r.Vals[r.at(c, s, f)]
	if v == missing {
		return 0, false
	}
	return float64(v) / f.scale(), true
}

// Set puts a field of cell c at step index s (NaN for missing).
func (r *Run) Set(c, s int, f Field, v float64) {
	i := r.at(c, s, f)
	if math.IsNaN(v) {
		r.Vals[i] = missing
		return
	}
	x := math.Round(v * f.scale())
	switch {
	case x > math.MaxInt16:
		x = math.MaxInt16
	case x <= math.MinInt16+1:
		x = math.MinInt16 + 1
	}
	r.Vals[i] = int16(x)
}

// NewRun is an empty run for the cells and steps, every value missing.
func NewRun(model string, at time.Time, steps []int, cells []Cell) *Run {
	r := &Run{Model: model, At: at, Steps: steps, PrecipFrom: make([]int, len(steps)), Cells: cells,
		Vals: make([]int16, len(cells)*len(steps)*int(NumFields))}
	for i := range r.Vals {
		r.Vals[i] = missing
	}
	return r
}

// StepIndex is the index of a forecast hour in Steps, -1 when not taken.
func (r *Run) StepIndex(h int) int {
	for i, s := range r.Steps {
		if s == h {
			return i
		}
	}
	return -1
}

// Cumulative is cell c's precipitation from the run's start to step index s, in mm, built
// from the accumulations the model gave (a bucket that empties is added to the one before).
func (r *Run) Cumulative(c, s int) (float64, bool) { return r.cumulative(cellReading{r, c}, s) }

func (r *Run) cumulative(rd reading, s int) (float64, bool) {
	total := 0.0
	for i := s; i >= 0; {
		if r.Steps[i] == 0 {
			return total, true // nothing has fallen at the run's own time
		}
		v, ok := rd.value(i, Precip)
		if !ok {
			return 0, false
		}
		total += v
		from := r.PrecipFrom[i]
		if from <= 0 {
			return total, true
		}
		j := r.StepIndex(from)
		if j < 0 || j >= i {
			return 0, false
		}
		i = j
	}
	return total, true
}

// --- the run file ---

// Dir is where the runs live on the volume.
func Dir(mount string) string { return filepath.Join(mount, "weather", "runs") }

// Path is a run's file: <mount>/weather/runs/<model>-<YYYYMMDDHH>.wx.
func Path(mount, model string, at time.Time) string {
	return filepath.Join(Dir(mount), model+"-"+at.UTC().Format("2006010215")+".wx")
}

// Save writes a run (gob, gzipped), atomically.
func Save(path string, r *Run) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	tmp := path + ".part"
	f, err := os.Create(tmp)
	if err != nil {
		return err
	}
	zw := gzip.NewWriter(f)
	if err := gob.NewEncoder(zw).Encode(r); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := zw.Close(); err != nil {
		f.Close()
		os.Remove(tmp)
		return err
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return err
	}
	return os.Rename(tmp, path)
}

// Load reads a run file.
func Load(path string) (*Run, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		return nil, err
	}
	var r Run
	if err := gob.NewDecoder(zr).Decode(&r); err != nil {
		return nil, err
	}
	if len(r.Vals) != len(r.Cells)*len(r.Steps)*int(NumFields) || len(r.PrecipFrom) != len(r.Steps) {
		return nil, errors.New("nwp: run file inconsistent")
	}
	if w := r.Win; w != nil && (len(w.Vals) != len(r.Steps)*int(NumFields)*w.Nj*w.Ni || (w.Orog != nil && len(w.Orog) != w.Nj*w.Ni)) {
		r.Win = nil
	}
	return &r, nil
}

// Available is the runs on the volume, the newest keep of each model (by the run's time in
// the name), newest first within a model, the models in catalogue order; a model without a
// run is left out. The index reads the newest run that covers an hour, so the run before
// still serves the hours before the newest run's start.
func Available(mount string, models []Model, keep int) ([]*Run, error) {
	ents, err := os.ReadDir(Dir(mount))
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var out []*Run
	for _, m := range models {
		var names []string
		for _, e := range ents {
			n := e.Name()
			if strings.HasPrefix(n, m.ID+"-") && strings.HasSuffix(n, ".wx") {
				names = append(names, n)
			}
		}
		sort.Sort(sort.Reverse(sort.StringSlice(names)))
		for _, n := range names[:min(keep, len(names))] {
			r, err := Load(filepath.Join(Dir(mount), n))
			if err != nil {
				continue // a half-written or old-format file: the next pull replaces it
			}
			out = append(out, r)
		}
	}
	return out, nil
}

// Prune keeps the newest keep runs of each model and removes the rest.
func Prune(mount string, keep int) (removed []string) {
	ents, err := os.ReadDir(Dir(mount))
	if err != nil {
		return nil
	}
	byModel := map[string][]string{}
	for _, e := range ents {
		n := e.Name()
		if !strings.HasSuffix(n, ".wx") {
			if strings.HasSuffix(n, ".part") {
				os.Remove(filepath.Join(Dir(mount), n))
			}
			continue
		}
		id := n[:strings.LastIndex(n, "-")]
		byModel[id] = append(byModel[id], n)
	}
	for _, names := range byModel {
		sort.Sort(sort.Reverse(sort.StringSlice(names)))
		for _, n := range names[min(keep, len(names)):] {
			if os.Remove(filepath.Join(Dir(mount), n)) == nil {
				removed = append(removed, n)
			}
		}
	}
	return removed
}

// RunName is "ifs 2026-10-10 06Z", for the card and the log.
func RunName(r *Run) string {
	return r.Model + " " + r.At.UTC().Format("2006-01-02 15") + "Z"
}
