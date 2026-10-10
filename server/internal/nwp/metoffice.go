package nwp

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"strings"
	"sync"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/egress"
	"github.com/LocalGhostDao/localghost/server/internal/grib2"
)

// THE MET OFFICE. Its models come through the Weather DataHub, which wants an account and a
// key: the person makes an order on the site (the UK 2 km model on the latitude-longitude grid,
// a region around where they live, the six fields, hourly steps) and puts the key and the
// order's name in SETTINGS › SERVER; the box keeps both and asks for nothing else. The API as
// read here is the one the DataHub documents behind its login (the orders list, an order's
// latest files, a file's data), so the first pull on a box with a key is its test; a path
// that answers 404 is reported with the address in tallyd's log. The free plan is a gigabyte
// a month, which a region of two degrees with hourly steps to two days, four runs a day,
// stays under.

// MetOffice is a box's DataHub credentials: nil or empty, and the model is not pulled.
type MetOffice struct {
	Key   string
	Order string // the order's id as the API names it (lower case, hyphens for spaces)
}

// MetOfficeBase is the DataHub's atmospheric models API.
var MetOfficeBase = "https://data.hub.api.metoffice.gov.uk/atmospheric-models/1.0.0"

// ukvDomain is the UK 2 km model's reach, roughly; a window and the cells come only where
// the order's region is, which the files themselves say.
var ukvDomain = &Bounds{South: 47.0, North: 61.5, West: -13.0, East: 5.0}

type ukvLayout struct {
	mu     sync.Mutex
	listed time.Time
	files  []ukvFile
	run    time.Time
	err    error
}

// ukvFile is one file of the order's latest run as the listing names it.
type ukvFile struct {
	ID   string
	Run  time.Time
	Step int // from the listing when it says; -1 when the file must say
}

// listing fetches the order's latest files, once a few minutes (the loop asks several times
// a pass).
func (l *ukvLayout) listing(ctx context.Context, f *Fetcher) ([]ukvFile, time.Time, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if time.Since(l.listed) < 5*time.Minute && (l.files != nil || l.err != nil) {
		return l.files, l.run, l.err
	}
	l.listed = time.Now()
	l.files, l.run, l.err = nil, time.Time{}, nil
	if f.MetOffice == nil || f.MetOffice.Key == "" || f.MetOffice.Order == "" {
		l.err = fmt.Errorf("no Met Office key or order in SETTINGS")
		return nil, time.Time{}, l.err
	}
	url := MetOfficeBase + "/orders/" + f.MetOffice.Order + "/latest"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		l.err = err
		return nil, time.Time{}, err
	}
	req.Header.Set("User-Agent", egress.UA)
	req.Header.Set("Accept", "application/json")
	req.Header.Set("apikey", f.MetOffice.Key)
	resp, err := f.hc.Do(req)
	if err != nil {
		l.err = err
		return nil, time.Time{}, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode != http.StatusOK {
		l.err = fmt.Errorf("%s: %s: %s", url, resp.Status, clipBytes(body, 200))
		return nil, time.Time{}, l.err
	}
	files, run, err := parseUKVListing(body)
	if err != nil {
		l.err = fmt.Errorf("%s: %v", url, err)
		return nil, time.Time{}, l.err
	}
	l.files, l.run = files, run
	return files, run, nil
}

func clipBytes(b []byte, n int) string {
	s := strings.TrimSpace(string(b))
	if len(s) > n {
		return s[:n] + "…"
	}
	return s
}

// parseUKVListing reads the files out of the listing whatever wraps them: every object with a
// fileId and a runDateTime, the newest run's files kept.
func parseUKVListing(body []byte) ([]ukvFile, time.Time, error) {
	var root any
	if err := json.Unmarshal(body, &root); err != nil {
		return nil, time.Time{}, fmt.Errorf("the listing is not JSON: %v", err)
	}
	var files []ukvFile
	var walk func(v any)
	walk = func(v any) {
		switch x := v.(type) {
		case map[string]any:
			id, _ := x["fileId"].(string)
			rd, _ := x["runDateTime"].(string)
			if id != "" && rd != "" {
				run, ok := parseRunTime(rd)
				if ok {
					uf := ukvFile{ID: id, Run: run, Step: -1}
					if ts, ok := x["timeSteps"].(string); ok {
						uf.Step = parseStep(ts)
					} else if ts, ok := x["timeStep"].(string); ok {
						uf.Step = parseStep(ts)
					}
					files = append(files, uf)
				}
			}
			for _, c := range x {
				walk(c)
			}
		case []any:
			for _, c := range x {
				walk(c)
			}
		}
	}
	walk(root)
	if len(files) == 0 {
		return nil, time.Time{}, fmt.Errorf("no files in the listing (%s)", clipBytes(body, 160))
	}
	var newest time.Time
	for _, f := range files {
		if f.Run.After(newest) {
			newest = f.Run
		}
	}
	var out []ukvFile
	for _, f := range files {
		if f.Run.Equal(newest) {
			out = append(out, f)
		}
	}
	return out, newest, nil
}

func parseRunTime(s string) (time.Time, bool) {
	for _, layout := range []string{time.RFC3339, "2006-01-02T15:04Z", "2006-01-02T15:04:05", "2006-01-02T15:04", "20060102T150000Z", "2006010215"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	return time.Time{}, false
}

// parseStep reads "+03", "PT3H", "3", "+03:00" as hours; -1 when it cannot.
func parseStep(s string) int {
	s = strings.TrimSpace(strings.TrimPrefix(strings.ToUpper(s), "PT"))
	s = strings.TrimSuffix(s, "H")
	s = strings.TrimPrefix(s, "+")
	if i := strings.Index(s, ":"); i >= 0 {
		s = s[:i]
	}
	var h int
	if _, err := fmt.Sscan(s, &h); err != nil {
		return -1
	}
	return h
}

func (l *ukvLayout) complete(ctx context.Context, f *Fetcher, m Model, run time.Time) bool {
	_, newest, err := l.listing(ctx, f)
	return err == nil && newest.Equal(run)
}

// ukvField names a field by its GRIB numbers: discipline 0 throughout.
type ukvField struct {
	cat, num int
}

func (l *ukvLayout) pull(ctx context.Context, f *Fetcher, m Model, run time.Time, p *Progress, emit emitFn, orog orogFn) error {
	files, newest, err := l.listing(ctx, f)
	if err != nil || !newest.Equal(run) {
		note(p, err)
		return nil
	}
	// the wind may come as speed and direction: both are kept per step until the pair is in
	type wind struct {
		speed, dir []float32
		g          grib2.Grid
	}
	winds := map[int]*wind{}
	for _, uf := range files {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		url := MetOfficeBase + "/orders/" + f.MetOffice.Order + "/latest/" + uf.ID + "/data"
		msg, vals, err := f.decodeMetOffice(ctx, url, p)
		if err != nil {
			note(p, err)
			continue
		}
		step := msg.Param.Step
		if uf.Step >= 0 {
			step = uf.Step
		}
		from := 0
		if msg.Param.Interval {
			from = msg.Param.StepFrom
		}
		switch (ukvField{msg.Param.Category, msg.Param.Number}) {
		case ukvField{0, 0}:
			emit(Temp2m, step, 0, msg.Grid, vals, kelvin)
		case ukvField{0, 6}:
			emit(Dew2m, step, 0, msg.Grid, vals, kelvin)
		case ukvField{1, 8}, ukvField{1, 52}, ukvField{1, 49}, ukvField{1, 65}:
			// an accumulation over the file's interval
			emit(Precip, step, from, msg.Grid, vals, same)
		case ukvField{1, 7}, ukvField{1, 59}:
			// a rate (kg m-2 s-1): the hour's accumulation, a bucket of one hour
			if step > 0 {
				emit(Precip, step, step-1, msg.Grid, vals, func(v float64) float64 { return v * 3600 })
			}
		case ukvField{2, 2}:
			emit(WindU, step, 0, msg.Grid, vals, same)
		case ukvField{2, 3}:
			emit(WindV, step, 0, msg.Grid, vals, same)
		case ukvField{2, 1}, ukvField{2, 0}:
			w := winds[step]
			if w == nil {
				w = &wind{g: msg.Grid}
				winds[step] = w
			}
			if msg.Param.Number == 1 {
				w.speed = vals
			} else {
				w.dir = vals
			}
			if w.speed != nil && w.dir != nil {
				u, v := windComponents(w.speed, w.dir)
				emit(WindU, step, 0, w.g, u, same)
				emit(WindV, step, 0, w.g, v, same)
				delete(winds, step)
			}
		case ukvField{6, 1}:
			// a fraction of one or a percentage: whichever the values say
			conv := same
			if maxOf(vals) <= 1.01 {
				conv = fraction
			}
			emit(Cloud, step, 0, msg.Grid, vals, conv)
		case ukvField{3, 5}, ukvField{3, 6}:
			orog(msg.Grid, vals)
		default:
			// a field of the order the box has no use for: skipped, not an error
		}
	}
	return nil
}

// decodeMetOffice fetches one file with the key and reads its first message.
func (f *Fetcher) decodeMetOffice(ctx context.Context, url string, p *Progress) (*grib2.Message, []float32, error) {
	if f.pause > 0 && p.Requests > 0 {
		select {
		case <-ctx.Done():
			return nil, nil, ctx.Err()
		case <-time.After(f.pause):
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, nil, err
	}
	req.Header.Set("User-Agent", egress.UA)
	req.Header.Set("Accept", "application/x-grib")
	req.Header.Set("apikey", f.MetOffice.Key)
	p.Requests++
	resp, err := f.hc.Do(req)
	if err != nil {
		return nil, nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 200))
		return nil, nil, fmt.Errorf("%s: %s: %s", url, resp.Status, clipBytes(body, 120))
	}
	rd := &counting{r: resp.Body, p: p}
	msg, err := grib2.NewReader(rd).Next()
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %v", url, err)
	}
	vals, err := msg.Values()
	if err != nil {
		return nil, nil, fmt.Errorf("%s: %v", url, err)
	}
	return msg, vals, nil
}

// windComponents turns a speed and a direction the wind blows from (degrees, meteorological)
// into east and north components.
func windComponents(speed, dir []float32) (u, v []float32) {
	u = make([]float32, len(speed))
	v = make([]float32, len(speed))
	for i := range speed {
		if i >= len(dir) || speed[i] != speed[i] || dir[i] != dir[i] {
			u[i], v[i] = grib2.Missing, grib2.Missing
			continue
		}
		rad := float64(dir[i]) * math.Pi / 180
		u[i] = float32(-float64(speed[i]) * math.Sin(rad))
		v[i] = float32(-float64(speed[i]) * math.Cos(rad))
	}
	return u, v
}

func maxOf(vals []float32) float64 {
	m := math.Inf(-1)
	for _, v := range vals {
		if v == v && float64(v) > m {
			m = float64(v)
		}
	}
	return m
}
