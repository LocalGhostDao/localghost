package nwp

import (
	"bufio"
	"compress/bzip2"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/egress"
	"github.com/LocalGhostDao/localghost/server/internal/grib2"
)

// Bases are the centres' roots; a test points them at its own server.
var Bases = map[string]string{
	"ifs":     "https://data.ecmwf.int/forecasts",
	"icon-eu": "https://opendata.dwd.de/weather/nwp/icon-eu/grib",
	"icon-d2": "https://opendata.dwd.de/weather/nwp/icon-d2/grib",
	"gfs":     "https://nomads.ncep.noaa.gov/pub/data/nccf/com/gfs/prod",
}

// threeHourly is the steps taken from the global models: every three hours to five days,
// enough to fill three days of hours from today's midnight however old the newest run is.
// hourlyThenThree is ICON-EU's: every hour to two days (the model has them), then every three
// to five. hourlyTwoDays is ICON-D2's, which stops at 48 h.
var threeHourly, hourlyThenThree, hourlyTwoDays = func() (a, b, c []int) {
	for h := 0; h <= 120; h += 3 {
		a = append(a, h)
	}
	for h := 0; h <= 120; h++ {
		if h <= 48 || h%3 == 0 {
			b = append(b, h)
		}
		if h <= 48 {
			c = append(c, h)
		}
	}
	return
}()

// Models is the catalogue, in the order the blend weighs them.
var Models = []Model{
	{ID: "icon-d2", Name: "ICON-D2", Centre: "Deutscher Wetterdienst", Runs: []int{0, 6, 12, 18}, Steps: hourlyTwoDays,
		Delay: 2 * time.Hour, Domain: &Bounds{South: 43.2, North: 58.0, West: -3.9, East: 20.3}, Weight: 1.3, MaxLead: 48, Regional: true,
		Terms: "DWD open data (GeoNutzV), source: Deutscher Wetterdienst", layout: iconD2Layout{}},
	{ID: "icon-eu", Name: "ICON-EU", Centre: "Deutscher Wetterdienst", Runs: []int{0, 6, 12, 18}, Steps: hourlyThenThree,
		Delay: 3 * time.Hour, Domain: &Bounds{South: 29.5, North: 70.5, West: -23.5, East: 62.5}, Weight: 1.0, MaxLead: 78,
		Terms: "DWD open data (GeoNutzV), source: Deutscher Wetterdienst", layout: iconLayout{}},
	{ID: "ifs", Name: "IFS", Centre: "ECMWF", Runs: []int{0, 6, 12, 18}, Steps: threeHourly,
		Delay: 7 * time.Hour, Weight: 0.8,
		Terms: "ECMWF open data, CC-BY-4.0, source: www.ecmwf.int", layout: ifsLayout{}},
	{ID: "gfs", Name: "GFS", Centre: "NOAA", Runs: []int{0, 6, 12, 18}, Steps: threeHourly,
		Delay: 5 * time.Hour, Weight: 0.5,
		Terms: "NOAA/NCEP GFS, public domain", layout: gfsLayout{}},
	{ID: "ukv", Name: "UKV", Centre: "Met Office", Runs: []int{0, 6, 12, 18}, Steps: hourlyTwoDays,
		Delay: 2 * time.Hour, Domain: ukvDomain, Weight: 1.3, MaxLead: 48, Regional: true, Keyed: true,
		Terms: "Met Office Weather DataHub, under the key holder's plan", layout: &ukvLayout{}},
}

// ModelByID finds a model in the catalogue.
func ModelByID(id string) (Model, bool) {
	for _, m := range Models {
		if m.ID == id {
			return m, true
		}
	}
	return Model{}, false
}

// RunsADay is how many of a model's runs a box takes: 0 for all of them, 2 for the 00 and
// 12 only (GHOST_WEATHER_RUNS=2 in tallyd's environment, for a box on a metered line: half
// the bytes, a forecast up to twelve hours older).
var RunsADay = 0

// Candidates are a model's runs that should be published by now, newest first, four at most.
func Candidates(m Model, now time.Time) []time.Time {
	latest := now.Add(-m.Delay).UTC()
	var out []time.Time
	day := time.Date(latest.Year(), latest.Month(), latest.Day(), 0, 0, 0, 0, time.UTC)
	for d := 0; d < 3 && len(out) < 4; d++ {
		for i := len(m.Runs) - 1; i >= 0 && len(out) < 4; i-- {
			if RunsADay == 2 && m.Runs[i] != 0 && m.Runs[i] != 12 {
				continue
			}
			t := day.Add(time.Duration(m.Runs[i]) * time.Hour)
			if !t.After(latest) {
				out = append(out, t)
			}
		}
		day = day.Add(-24 * time.Hour)
	}
	return out
}

// Fetcher pulls runs from the centres.
type Fetcher struct {
	hc    *http.Client
	pause time.Duration // between requests to the same centre: a courtesy, and NOMADS's limit
	// MetOffice is the box's DataHub key and order, for the keyed model; nil without.
	MetOffice *MetOffice
}

func NewFetcher() *Fetcher {
	tr := &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		TLSHandshakeTimeout:   15 * time.Second,
		ResponseHeaderTimeout: 60 * time.Second,
		MaxIdleConns:          4,
		MaxIdleConnsPerHost:   2,
		IdleConnTimeout:       90 * time.Second,
	}
	return &Fetcher{hc: &http.Client{Transport: tr, Timeout: 10 * time.Minute}, pause: 600 * time.Millisecond}
}

// Progress is what a pull reports as it goes.
type Progress struct {
	Model    string
	Run      time.Time
	Fields   int // decoded
	Missing  int // asked and not there, or undecodable
	Bytes    int64
	Requests int
	LastErr  string
	Took     time.Duration
}

// Complete says whether a run is fully published: its last step is there.
func (f *Fetcher) Complete(ctx context.Context, m Model, run time.Time) bool {
	return m.layout.complete(ctx, f, m, run)
}

// Fix is the phone's last position, for the window a run keeps around it; nil for none.
type Fix struct {
	Lat, Lon float64
}

// Pull fetches a run's fields and reduces them to the cells as they arrive, one field in
// memory at a time, and keeps the model's grid around the fix. The run is returned with
// whatever decoded; the progress says how much.
func (f *Fetcher) Pull(ctx context.Context, m Model, run time.Time, cells []Cell, fix *Fix) (*Run, Progress, error) {
	t0 := time.Now()
	r := NewRun(m.ID, run, m.Steps, cells)
	for i := range r.Cells {
		r.Cells[i].Orog = float32(math.NaN())
	}
	p := Progress{Model: m.ID, Run: run}
	window := func(g grib2.Grid) *Window {
		if fix == nil || !m.Domain.Has(fix.Lat, fix.Lon) {
			return nil
		}
		if r.Win == nil {
			r.Win = newWindow(g, fix.Lat, fix.Lon, len(r.Steps))
		}
		if r.Win == nil {
			return nil
		}
		if _, _, ni, nj, ok := windowCorner(g, fix.Lat, fix.Lon); !ok || r.Win.Ni != ni || r.Win.Nj != nj {
			return nil // a field on another grid than the run's first: left out of the window
		}
		return r.Win
	}
	emit := func(fld Field, step, from int, g grib2.Grid, vals []float32, conv func(float64) float64) {
		s := r.StepIndex(step)
		if s < 0 {
			return
		}
		if fld == Precip {
			r.PrecipFrom[s] = from
		}
		for c, cell := range r.Cells {
			v, ok := g.Sample(vals, float64(cell.Lat), float64(cell.Lon))
			if !ok {
				continue
			}
			r.Set(c, s, fld, conv(float64(v)))
		}
		if w := window(g); w != nil {
			w.fill(g, vals, s, fld, conv, fix.Lat, fix.Lon)
		}
		p.Fields++
	}
	emitOrog := func(g grib2.Grid, vals []float32) {
		for c, cell := range r.Cells {
			if v, ok := g.Sample(vals, float64(cell.Lat), float64(cell.Lon)); ok {
				r.Cells[c].Orog = v
			}
		}
		if w := window(g); w != nil {
			w.fillOrog(g, vals, fix.Lat, fix.Lon)
		}
	}
	err := m.layout.pull(ctx, f, m, run, &p, emit, emitOrog)
	r.Fetched = time.Now()
	p.Took = time.Since(t0)
	return r, p, err
}

// windowCorner is the grid column and row of the window's first point around a fix, and
// its size: WinDeg either side, clipped to the grid.
func windowCorner(g grib2.Grid, lat, lon float64) (i0, j0, ni, nj int, ok bool) {
	fi, fj, ok := g.Locate(lat, lon)
	if !ok {
		return 0, 0, 0, 0, false
	}
	half := int(math.Ceil(WinDeg / g.Di))
	halfJ := int(math.Ceil(WinDeg / g.Dj))
	i0 = int(math.Floor(fi)) - half
	j0 = int(math.Floor(fj)) - halfJ
	i1 := int(math.Floor(fi)) + half + 1
	j1 := int(math.Floor(fj)) + halfJ + 1
	if j0 < 0 {
		j0 = 0
	}
	if j1 > g.Nj-1 {
		j1 = g.Nj - 1
	}
	global := math.Abs(float64(g.Ni)*g.Di-360) < g.Di/2
	if !global {
		if i0 < 0 {
			i0 = 0
		}
		if i1 > g.Ni-1 {
			i1 = g.Ni - 1
		}
	}
	return i0, j0, i1 - i0 + 1, j1 - j0 + 1, true
}

func newWindow(g grib2.Grid, lat, lon float64, steps int) *Window {
	i0, j0, ni, nj, ok := windowCorner(g, lat, lon)
	if !ok || ni < 2 || nj < 2 {
		return nil
	}
	lat1, lon1 := g.LatLon(((i0%g.Ni)+g.Ni)%g.Ni, j0)
	if i0 < 0 {
		lon1 = g.Lon1 + float64(i0)*g.Di // west across the seam
		if g.ScanWest {
			lon1 = g.Lon1 - float64(i0)*g.Di
		}
	}
	w := &Window{Ni: ni, Nj: nj, Lat1: lat1, Lon1: lon1, Di: g.Di, Dj: g.Dj, ScanNorth: g.ScanNorth,
		Vals: make([]int16, steps*int(NumFields)*nj*ni)}
	if g.ScanWest {
		w.Di = -g.Di
	}
	for i := range w.Vals {
		w.Vals[i] = missing
	}
	return w
}

// fill copies a field's values into the window.
func (w *Window) fill(g grib2.Grid, vals []float32, s int, f Field, conv func(float64) float64, lat, lon float64) {
	i0, j0, _, _, ok := windowCorner(g, lat, lon)
	if !ok {
		return
	}
	for j := 0; j < w.Nj; j++ {
		for i := 0; i < w.Ni; i++ {
			gi := ((i0+i)%g.Ni + g.Ni) % g.Ni
			v := vals[g.Index(gi, j0+j)]
			k := w.at(s, f, j, i)
			if v != v {
				w.Vals[k] = missing
				continue
			}
			x := math.Round(conv(float64(v)) * f.scale())
			switch {
			case x > math.MaxInt16:
				x = math.MaxInt16
			case x <= math.MinInt16+1:
				x = math.MinInt16 + 1
			}
			w.Vals[k] = int16(x)
		}
	}
}

func (w *Window) fillOrog(g grib2.Grid, vals []float32, lat, lon float64) {
	i0, j0, _, _, ok := windowCorner(g, lat, lon)
	if !ok {
		return
	}
	w.Orog = make([]float32, w.Nj*w.Ni)
	for j := 0; j < w.Nj; j++ {
		for i := 0; i < w.Ni; i++ {
			gi := ((i0+i)%g.Ni + g.Ni) % g.Ni
			w.Orog[j*w.Ni+i] = vals[g.Index(gi, j0+j)]
		}
	}
}

// get fetches an address, a byte range of it when length > 0 (a server that ignores the
// range is read from the offset), with the pause before. The body is the caller's to close.
func (f *Fetcher) get(ctx context.Context, url string, offset, length int64, p *Progress) (io.ReadCloser, error) {
	if f.pause > 0 && p.Requests > 0 {
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(f.pause):
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", egress.UA)
	if length > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-%d", offset, offset+length-1))
	} else if offset > 0 {
		req.Header.Set("Range", fmt.Sprintf("bytes=%d-", offset))
	}
	p.Requests++
	resp, err := f.hc.Do(req)
	if err != nil {
		return nil, err
	}
	switch resp.StatusCode {
	case http.StatusOK:
		if offset > 0 {
			// the whole file came: skip to the offset
			if _, err := io.CopyN(io.Discard, resp.Body, offset); err != nil {
				resp.Body.Close()
				return nil, err
			}
		}
		if length > 0 {
			return &limited{Reader: io.LimitReader(resp.Body, length), c: resp.Body}, nil
		}
		return resp.Body, nil
	case http.StatusPartialContent:
		return resp.Body, nil
	default:
		resp.Body.Close()
		return nil, fmt.Errorf("%s: %s", url, resp.Status)
	}
}

type limited struct {
	io.Reader
	c io.Closer
}

func (l *limited) Close() error { return l.c.Close() }

// head says whether an address answers.
func (f *Fetcher) head(ctx context.Context, url string) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodHead, url, nil)
	if err != nil {
		return false
	}
	req.Header.Set("User-Agent", egress.UA)
	resp, err := f.hc.Do(req)
	if err != nil {
		return false
	}
	resp.Body.Close()
	return resp.StatusCode == http.StatusOK
}

// readAll reads a small text (an index) whole.
func (f *Fetcher) readAll(ctx context.Context, url string, p *Progress) ([]byte, error) {
	body, err := f.get(ctx, url, 0, 0, p)
	if err != nil {
		return nil, err
	}
	defer body.Close()
	b, err := io.ReadAll(io.LimitReader(body, 8<<20))
	if err != nil {
		return nil, err
	}
	p.Bytes += int64(len(b))
	return b, nil
}

// decodeOne reads one GRIB2 message from a body and gives its field.
func (f *Fetcher) decodeOne(ctx context.Context, url string, offset, length int64, p *Progress, bz bool) (*grib2.Message, []float32, error) {
	body, err := f.get(ctx, url, offset, length, p)
	if err != nil {
		return nil, nil, err
	}
	defer body.Close()
	var rd io.Reader = &counting{r: body, p: p}
	if bz {
		rd = bzip2.NewReader(rd)
	}
	m, err := grib2.NewReader(rd).Next()
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %v", url, err)
	}
	vals, err := m.Values()
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %v", url, err)
	}
	return m, vals, nil
}

type counting struct {
	r io.Reader
	p *Progress
}

func (c *counting) Read(b []byte) (int, error) {
	n, err := c.r.Read(b)
	c.p.Bytes += int64(n)
	return n, err
}

type emitFn func(fld Field, step, from int, g grib2.Grid, vals []float32, conv func(float64) float64)
type orogFn func(g grib2.Grid, vals []float32)

type layout interface {
	complete(ctx context.Context, f *Fetcher, m Model, run time.Time) bool
	pull(ctx context.Context, f *Fetcher, m Model, run time.Time, p *Progress, emit emitFn, orog orogFn) error
}

func kelvin(v float64) float64   { return v - 273.15 }
func same(v float64) float64     { return v }
func metres(v float64) float64   { return v * 1000 }
func fraction(v float64) float64 { return v * 100 }

func note(p *Progress, err error) {
	p.Missing++
	if err != nil {
		p.LastErr = err.Error()
	}
}

// --- ECMWF: one file per step with every field, an index of byte ranges beside it ---

type ifsLayout struct{}

// stream is the IFS's: the main runs at 00 and 12, the short cut-off runs at 06 and 18.
func ifsStream(run time.Time) string {
	if run.Hour() == 6 || run.Hour() == 18 {
		return "scda"
	}
	return "oper"
}

func ifsURL(run time.Time, step int) string {
	st := ifsStream(run)
	return fmt.Sprintf("%s/%s/%02dz/ifs/0p25/%s/%s0000-%dh-%s-fc.grib2", Bases["ifs"], run.Format("20060102"), run.Hour(), st, run.Format("2006010215"), step, st)
}

var ifsParams = map[string]struct {
	f    Field
	conv func(float64) float64
}{
	"2t": {Temp2m, kelvin}, "2d": {Dew2m, kelvin}, "tp": {Precip, metres},
	"10u": {WindU, same}, "10v": {WindV, same}, "tcc": {Cloud, fraction},
}

func (ifsLayout) complete(ctx context.Context, f *Fetcher, m Model, run time.Time) bool {
	return f.head(ctx, ifsURL(run, m.Steps[len(m.Steps)-1])+".index")
}

func (ifsLayout) pull(ctx context.Context, f *Fetcher, m Model, run time.Time, p *Progress, emit emitFn, orog orogFn) error {
	for _, step := range m.Steps {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		url := ifsURL(run, step)
		idx, err := f.readAll(ctx, url+".index", p)
		if err != nil {
			note(p, err)
			continue
		}
		type entry struct {
			Param   string `json:"param"`
			Levtype string `json:"levtype"`
			Offset  int64  `json:"_offset"`
			Length  int64  `json:"_length"`
		}
		sc := bufio.NewScanner(strings.NewReader(string(idx)))
		for sc.Scan() {
			var e entry
			if json.Unmarshal(sc.Bytes(), &e) != nil || e.Levtype != "sfc" {
				continue
			}
			pm, ok := ifsParams[e.Param]
			if !ok {
				continue
			}
			if step == 0 && pm.f == Precip {
				continue
			}
			msg, vals, err := f.decodeOne(ctx, url, e.Offset, e.Length, p, false)
			if err != nil {
				note(p, err)
				continue
			}
			from := 0
			if msg.Param.Interval {
				from = msg.Param.StepFrom
			}
			emit(pm.f, step, from, msg.Grid, vals, pm.conv)
		}
	}
	return nil
}

// --- DWD: one bz2 file per field per step, named by both ---

type iconLayout struct{}

var iconParams = []struct {
	dir, name string
	f         Field
	conv      func(float64) float64
}{
	{"t_2m", "T_2M", Temp2m, kelvin}, {"td_2m", "TD_2M", Dew2m, kelvin}, {"tot_prec", "TOT_PREC", Precip, same},
	{"u_10m", "U_10M", WindU, same}, {"v_10m", "V_10M", WindV, same}, {"clct", "CLCT", Cloud, same},
}

func iconURL(run time.Time, dir, name string, step int) string {
	return fmt.Sprintf("%s/%02d/%s/icon-eu_europe_regular-lat-lon_single-level_%s_%03d_%s.grib2.bz2", Bases["icon-eu"], run.Hour(), dir, run.Format("2006010215"), step, name)
}

func (iconLayout) complete(ctx context.Context, f *Fetcher, m Model, run time.Time) bool {
	return f.head(ctx, iconURL(run, "t_2m", "T_2M", m.Steps[len(m.Steps)-1]))
}

func (iconLayout) pull(ctx context.Context, f *Fetcher, m Model, run time.Time, p *Progress, emit emitFn, orog orogFn) error {
	// the ground's height, once a run (it never changes, but it is small and the run is the
	// unit the loop works in)
	if msg, vals, err := f.decodeOne(ctx, iconURL(run, "hsurf", "HSURF", 0), 0, 0, p, true); err == nil {
		orog(msg.Grid, vals)
	} else {
		note(p, err)
	}
	for _, step := range m.Steps {
		for _, pm := range iconParams {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if step == 0 && pm.f == Precip {
				continue
			}
			msg, vals, err := f.decodeOne(ctx, iconURL(run, pm.dir, pm.name, step), 0, 0, p, true)
			if err != nil {
				note(p, err)
				continue
			}
			from := 0
			if msg.Param.Interval {
				from = msg.Param.StepFrom
			}
			emit(pm.f, step, from, msg.Grid, vals, pm.conv)
		}
	}
	return nil
}

// --- DWD's ICON-D2: as ICON-EU, the file names in the newer manner (the level kind and the
// parameter in lower case after the step; the ground in a time-invariant file) ---

type iconD2Layout struct{}

var iconD2Params = []struct {
	name string
	f    Field
	conv func(float64) float64
}{
	{"t_2m", Temp2m, kelvin}, {"td_2m", Dew2m, kelvin}, {"tot_prec", Precip, same},
	{"u_10m", WindU, same}, {"v_10m", WindV, same}, {"clct", Cloud, same},
}

func iconD2URL(run time.Time, name string, step int) string {
	return fmt.Sprintf("%s/%02d/%s/icon-d2_germany_regular-lat-lon_single-level_%s_%03d_2d_%s.grib2.bz2", Bases["icon-d2"], run.Hour(), name, run.Format("2006010215"), step, name)
}

func iconD2OrogURL(run time.Time) string {
	return fmt.Sprintf("%s/%02d/hsurf/icon-d2_germany_regular-lat-lon_time-invariant_%s_000_0_hsurf.grib2.bz2", Bases["icon-d2"], run.Hour(), run.Format("2006010215"))
}

func (iconD2Layout) complete(ctx context.Context, f *Fetcher, m Model, run time.Time) bool {
	return f.head(ctx, iconD2URL(run, "t_2m", m.Steps[len(m.Steps)-1]))
}

func (iconD2Layout) pull(ctx context.Context, f *Fetcher, m Model, run time.Time, p *Progress, emit emitFn, orog orogFn) error {
	if msg, vals, err := f.decodeOne(ctx, iconD2OrogURL(run), 0, 0, p, true); err == nil {
		orog(msg.Grid, vals)
	} else {
		note(p, err)
	}
	for _, step := range m.Steps {
		for _, pm := range iconD2Params {
			if ctx.Err() != nil {
				return ctx.Err()
			}
			if step == 0 && pm.f == Precip {
				continue
			}
			msg, vals, err := f.decodeOne(ctx, iconD2URL(run, pm.name, step), 0, 0, p, true)
			if err != nil {
				note(p, err)
				continue
			}
			from := 0
			if msg.Param.Interval {
				from = msg.Param.StepFrom
			}
			emit(pm.f, step, from, msg.Grid, vals, pm.conv)
		}
	}
	return nil
}

// --- NOAA: one file per step with every field, a .idx of offsets beside it ---

type gfsLayout struct{}

func gfsURL(run time.Time, step int) string {
	return fmt.Sprintf("%s/gfs.%s/%02d/atmos/gfs.t%02dz.pgrb2.0p25.f%03d", Bases["gfs"], run.Format("20060102"), run.Hour(), run.Hour(), step)
}

var gfsParams = map[string]struct {
	f    Field
	conv func(float64) float64
}{
	"TMP:2 m above ground": {Temp2m, kelvin}, "DPT:2 m above ground": {Dew2m, kelvin}, "APCP:surface": {Precip, same},
	"UGRD:10 m above ground": {WindU, same}, "VGRD:10 m above ground": {WindV, same}, "TCDC:entire atmosphere": {Cloud, same},
}

func (gfsLayout) complete(ctx context.Context, f *Fetcher, m Model, run time.Time) bool {
	return f.head(ctx, gfsURL(run, m.Steps[len(m.Steps)-1])+".idx")
}

// gfsIndex reads a .idx: "n:offset:d=2026101000:TMP:2 m above ground:3 hour fcst:" a line.
func gfsIndex(idx []byte) (entries []struct {
	key            string
	offset, length int64
}) {
	type ent = struct {
		key            string
		offset, length int64
	}
	sc := bufio.NewScanner(strings.NewReader(string(idx)))
	for sc.Scan() {
		parts := strings.Split(sc.Text(), ":")
		if len(parts) < 5 {
			continue
		}
		off, err := strconv.ParseInt(parts[1], 10, 64)
		if err != nil {
			continue
		}
		entries = append(entries, ent{key: parts[3] + ":" + parts[4], offset: off})
	}
	for i := range entries {
		if i+1 < len(entries) {
			entries[i].length = entries[i+1].offset - entries[i].offset
		}
	}
	return entries
}

func (gfsLayout) pull(ctx context.Context, f *Fetcher, m Model, run time.Time, p *Progress, emit emitFn, orog orogFn) error {
	orogDone := false
	for _, step := range m.Steps {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		url := gfsURL(run, step)
		idx, err := f.readAll(ctx, url+".idx", p)
		if err != nil {
			note(p, err)
			continue
		}
		seen := map[Field]bool{}
		for _, e := range gfsIndex(idx) {
			if !orogDone && e.key == "HGT:surface" {
				if msg, vals, err := f.decodeOne(ctx, url, e.offset, e.length, p, false); err == nil {
					orog(msg.Grid, vals)
					orogDone = true
				} else {
					note(p, err)
				}
				continue
			}
			pm, ok := gfsParams[e.key]
			if !ok || seen[pm.f] {
				continue
			}
			if step == 0 && pm.f == Precip {
				continue
			}
			msg, vals, err := f.decodeOne(ctx, url, e.offset, e.length, p, false)
			if err != nil {
				note(p, err)
				continue
			}
			seen[pm.f] = true
			from := 0
			if msg.Param.Interval {
				from = msg.Param.StepFrom
			}
			emit(pm.f, step, from, msg.Grid, vals, pm.conv)
		}
	}
	return nil
}

// ErrIncomplete is a pull that decoded too little to stand for the run.
var ErrIncomplete = errors.New("nwp: too few fields decoded")

// Enough says whether a pulled run can stand for the model: the model's grid answered for at
// least half the cells in its domain, and those cells have their temperature at four fifths
// of the steps.
func Enough(r *Run, m Model) bool {
	if len(r.Cells) == 0 || len(r.Steps) == 0 {
		return false
	}
	inDomain, covered, want, have := 0, 0, 0, 0
	for c, cell := range r.Cells {
		if !m.Domain.Has(float64(cell.Lat), float64(cell.Lon)) {
			continue
		}
		inDomain++
		n := 0
		for s := range r.Steps {
			if _, ok := r.Value(c, s, Temp2m); ok {
				n++
			}
		}
		if n == 0 {
			continue
		}
		covered++
		want += len(r.Steps)
		have += n
	}
	return inDomain > 0 && covered*2 >= inDomain && have*5 >= want*4
}
