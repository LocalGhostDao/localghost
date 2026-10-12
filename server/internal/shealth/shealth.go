// Package shealth reads a Samsung Health download (Samsung Health › Settings › Download personal
// data: a zip of CSV files, one per table, named like
// com.samsung.shealth.tracker.pedometer_day_summary.20261012.csv) into the box's day batches,
// the shape the phone's Health Connect sync ships (tally.Batch). Health Connect holds only what
// Samsung Health has written since it was told to share, which is weeks; the download holds the
// years the watch has been worn.
//
// The format as understood in October 2026, from the files and from what others have read of
// them: each CSV opens with one line naming the table and its version, then the header, whose
// column names carry the table's prefix (com.samsung.health.sleep.start_time), then the rows.
// Times are "2026-10-10 06:12:33.000" in UTC with a time_offset column ("UTC+0100") that gives
// the local clock; day_time is a millisecond epoch of the day's start. Columns are found by
// their suffix, so a prefix that differs between versions does not matter, and a table the box
// does not know is listed in the report, not refused.
package shealth

import (
	"archive/zip"
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"path"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/tally"
)

// Table is one CSV of the download as the report lists it.
type Table struct {
	Name string `json:"name"` // "sleep", "tracker.heart_rate": the file name's table after "com.samsung.(s)health."
	File string `json:"file"`
	Rows int    `json:"rows"`
	From string `json:"from,omitempty"` // the earliest day a row names
	To   string `json:"to,omitempty"`
	Read string `json:"read"` // what the box takes from it; "not read" for a table it does not know
	Err  string `json:"err,omitempty"`
}

// Report is the scan of a download: every table and what the import would take.
type Report struct {
	File   string  `json:"file"`
	Tables []Table `json:"tables"`
	Days   int     `json:"days"` // distinct days any known table names
	Note   string  `json:"note,omitempty"`
}

// what each known table gives, in the report's words
var reads = map[string]string{
	"tracker.pedometer_day_summary":  "steps, distance, active kcal per day (the largest count of the day's rows)",
	"sleep":                          "sleep minutes, under the day the night ends",
	"sleep_stage":                    "deep, light, REM and awake minutes",
	"tracker.heart_rate":             "heart rate: the day's mean, lowest and highest, and the readings thinned to five minutes",
	"exercise":                       "exercise minutes per day",
	"weight":                         "weight and body fat",
	"tracker.oxygen_saturation":      "blood oxygen, the day's mean",
	"stress":                         "stress score, the day's mean",
	"floors_climbed":                 "floors per day",
	"activity.day_summary":           "active minutes per day",
	"tracker.heart_rate_variability": "heart rate variability, the day's mean",
	"tracker.pedometer_step_count":   "not needed: the day summary has the steps",
	"tracker.respiratory_rate":       "breathing rate, the day's mean",
	"respiratory_rate":               "breathing rate, the day's mean",
	"tracker.skin_temperature":       "skin temperature, the day's mean",
	"skin_temperature":               "skin temperature, the day's mean",
}

// tableName takes "com.samsung.shealth.tracker.heart_rate.202610120312.csv" to "tracker.heart_rate".
func tableName(file string) string {
	base := path.Base(file)
	base = strings.TrimSuffix(base, ".csv")
	for _, p := range []string{"com.samsung.shealth.", "com.samsung.health."} {
		if strings.HasPrefix(base, p) {
			base = strings.TrimPrefix(base, p)
			break
		}
	}
	// the date stamp at the end
	if i := strings.LastIndex(base, "."); i > 0 {
		if _, err := strconv.Atoi(base[i+1:]); err == nil {
			base = base[:i]
		}
	}
	return base
}

// row is one CSV row with its columns reachable by suffix.
type row struct {
	cols map[string]int
	vals []string
}

// get finds the column whose name ends with ".<key>" (or is exactly key); "" when absent.
func (r row) get(key string) string {
	if i, ok := r.cols[key]; ok && i < len(r.vals) {
		return strings.TrimSpace(r.vals[i])
	}
	return ""
}

func (r row) num(key string) (float64, bool) {
	s := r.get(key)
	if s == "" {
		return 0, false
	}
	v, err := strconv.ParseFloat(s, 64)
	if err != nil || math.IsNaN(v) || math.IsInf(v, 0) {
		return 0, false
	}
	return v, true
}

// offset reads "UTC+0100" / "UTC-0530" as seconds; 0 when absent or odd.
func offset(s string) int {
	s = strings.TrimSpace(strings.TrimPrefix(s, "UTC"))
	if len(s) != 5 || (s[0] != '+' && s[0] != '-') {
		return 0
	}
	h, err1 := strconv.Atoi(s[1:3])
	m, err2 := strconv.Atoi(s[3:5])
	if err1 != nil || err2 != nil {
		return 0
	}
	sec := h*3600 + m*60
	if s[0] == '-' {
		sec = -sec
	}
	return sec
}

// at reads a Samsung time ("2026-10-10 06:12:33.000", UTC) or a millisecond epoch; false when
// neither.
func at(s string) (time.Time, bool) {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}, false
	}
	for _, layout := range []string{"2006-01-02 15:04:05.000", "2006-01-02 15:04:05", "2006-01-02T15:04:05.000", "2006-01-02"} {
		if t, err := time.Parse(layout, s); err == nil {
			return t.UTC(), true
		}
	}
	if ms, err := strconv.ParseInt(s, 10, 64); err == nil && ms > 1e11 {
		return time.UnixMilli(ms).UTC(), true
	}
	return time.Time{}, false
}

// localDay is the day the instant falls on by the row's clock.
func localDay(t time.Time, off int) string {
	return t.Add(time.Duration(off) * time.Second).Format("2006-01-02")
}

// readCSV walks a table's rows. The first line is the preamble when it has fewer fields than the
// line after it (the header); a file that opens with the header has no preamble.
func readCSV(r io.Reader, fn func(row)) (int, error) {
	br := bufio.NewReaderSize(r, 1<<20)
	// the files open with a UTF-8 byte order mark
	if b, err := br.Peek(3); err == nil && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF {
		_, _ = br.Discard(3)
	}
	cr := csv.NewReader(br)
	cr.FieldsPerRecord = -1
	cr.LazyQuotes = true
	cr.ReuseRecord = false
	first, err := cr.Read()
	if err != nil {
		return 0, err
	}
	header := first
	second, err := cr.Read()
	if err == io.EOF {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	n := 0
	var cols map[string]int
	index := func(h []string) map[string]int {
		m := map[string]int{}
		for i, name := range h {
			name = strings.TrimSpace(name)
			if name == "" {
				continue
			}
			m[name] = i
			if j := strings.LastIndex(name, "."); j >= 0 {
				if _, dup := m[name[j+1:]]; !dup {
					m[name[j+1:]] = i
				}
			}
		}
		return m
	}
	// the preamble is "<table>,<version>,<n>": a number in its second field, and the header
	// after it (a table of three columns has three fields on both lines, so the count alone
	// told nothing)
	_, verr := strconv.Atoi(strings.TrimSpace(first[min(1, len(first)-1)]))
	if !looksLikeHeader(first) && (verr == nil || len(first) < len(second)) && looksLikeHeader(second) {
		// preamble, then the header
		header = second
		cols = index(header)
	} else {
		cols = index(header)
		fn(row{cols: cols, vals: second})
		n++
	}
	for {
		rec, err := cr.Read()
		if err == io.EOF {
			break
		}
		if err != nil {
			return n, err
		}
		if len(rec) == 0 || (len(rec) == 1 && strings.TrimSpace(rec[0]) == "") {
			continue
		}
		fn(row{cols: cols, vals: rec})
		n++
	}
	return n, nil
}

func looksLikeHeader(rec []string) bool {
	for _, f := range rec {
		if strings.Contains(f, "start_time") || strings.Contains(f, "day_time") || strings.Contains(f, "create_time") {
			return true
		}
	}
	return false
}

// days accumulates the metrics per day while the tables are read.
type days struct {
	// bins opens a row's binning_data json (jsons/<table>/<name>) and returns its entries; nil
	// without the jsons folder. The heart rate's per-minute readings live there, the CSV row
	// being the session's summary.
	bins     func(name string) []map[string]any
	m        map[string]map[string]float64
	means    map[string]map[string][]float64 // metric -> day -> values to average
	samples  map[int64]float64               // heart rate by five-minute bucket
	sleeps   map[string][][2]int64           // end day -> intervals
	stages   map[string]map[string]float64   // sleep id -> stage metric -> minutes
	stageDay map[string]string               // sleep id -> the day its session ends ("?"+id: the stage's own end day)
}

func newDays() *days {
	return &days{m: map[string]map[string]float64{}, means: map[string]map[string][]float64{}, samples: map[int64]float64{},
		sleeps: map[string][][2]int64{}, stages: map[string]map[string]float64{}, stageDay: map[string]string{}}
}

func (d *days) day(day string) map[string]float64 {
	m := d.m[day]
	if m == nil {
		m = map[string]float64{}
		d.m[day] = m
	}
	return m
}

func (d *days) max(day, metric string, v float64) {
	m := d.day(day)
	if v > m[metric] {
		m[metric] = v
	}
}

func (d *days) add(day, metric string, v float64) {
	m := d.day(day)
	m[metric] += v
}

func (d *days) mean(day, metric string, v float64) {
	if d.means[metric] == nil {
		d.means[metric] = map[string][]float64{}
	}
	d.means[metric][day] = append(d.means[metric][day], v)
}

// take reads one table's row into the days; what the table is decides what is taken.
func (d *days) take(table string, r row) {
	off := offset(r.get("time_offset"))
	switch table {
	case "tracker.pedometer_day_summary":
		t, ok := at(r.get("day_time"))
		if !ok {
			return
		}
		day := localDay(t, off)
		if v, ok := r.num("step_count"); ok && v > 0 {
			d.max(day, "steps", v)
		}
		if v, ok := r.num("distance"); ok && v > 0 {
			d.max(day, "distance_km", v/1000)
		}
		if v, ok := r.num("calorie"); ok && v > 0 {
			d.max(day, "active_calories", v)
		}
	case "activity.day_summary":
		t, ok := at(r.get("day_time"))
		if !ok {
			return
		}
		day := localDay(t, off)
		if v, ok := r.num("active_time"); ok && v > 0 {
			d.max(day, "active_minutes", v/60000)
		}
	case "sleep":
		s, ok1 := at(r.get("start_time"))
		e, ok2 := at(r.get("end_time"))
		if !ok1 || !ok2 || !e.After(s) {
			return
		}
		day := localDay(e, off)
		d.sleeps[day] = append(d.sleeps[day], [2]int64{s.Unix(), e.Unix()})
		if id := r.get("datauuid"); id != "" {
			d.stageDay[id] = day
		}
	case "sleep_stage":
		s, ok1 := at(r.get("start_time"))
		e, ok2 := at(r.get("end_time"))
		if !ok1 || !ok2 || !e.After(s) {
			return
		}
		metric := ""
		switch r.get("stage") {
		case "40001":
			metric = "sleep_awake_minutes"
		case "40002":
			metric = "sleep_light_minutes"
		case "40003":
			metric = "sleep_deep_minutes"
		case "40004":
			metric = "sleep_rem_minutes"
		default:
			return
		}
		id := r.get("sleep_id")
		if d.stages[id] == nil {
			d.stages[id] = map[string]float64{}
		}
		d.stages[id][metric] += e.Sub(s).Minutes()
		if _, seen := d.stageDay[id]; !seen {
			d.stageDay["?"+id] = localDay(e, off) // the stage's own end day, used when its session never turns up
		}
	case "tracker.heart_rate":
		t, ok := at(r.get("start_time"))
		if !ok {
			return
		}
		// the per-minute readings from the row's binning file when there is one, else the
		// row's own value (the session's mean) at its start
		took := 0
		if d.bins != nil {
			if name := r.get("binning_data"); name != "" {
				for _, e := range d.bins(name) {
					v, okv := jsonNum(e["heart_rate"])
					ts, okt := jsonNum(e["start_time"])
					if !okv || !okt || v <= 20 || v > 250 {
						continue
					}
					bt := time.UnixMilli(int64(ts)).UTC()
					d.mean(localDay(bt, off), "hr", v)
					b := bt.Unix() / 300 * 300
					if _, had := d.samples[b]; !had {
						d.samples[b] = v
					}
					took++
				}
			}
		}
		if took > 0 {
			return
		}
		v, ok := r.num("heart_rate")
		if !ok || v <= 20 || v > 250 {
			return
		}
		day := localDay(t, off)
		d.mean(day, "hr", v)
		b := t.Unix() / 300 * 300
		if _, had := d.samples[b]; !had {
			d.samples[b] = v
		}
	case "tracker.respiratory_rate", "respiratory_rate":
		t, ok := at(r.get("start_time"))
		if !ok {
			return
		}
		took := 0
		if d.bins != nil {
			if name := r.get("binning_data"); name != "" {
				for _, e := range d.bins(name) {
					v, okv := jsonNum(e["respiratory_rate"])
					ts, okt := jsonNum(e["start_time"])
					if !okv || !okt || v <= 0 {
						continue
					}
					d.mean(localDay(time.UnixMilli(int64(ts)).UTC(), off), "resp_rate", v)
					took++
				}
			}
		}
		if took == 0 {
			if v, ok := r.num("respiratory_rate"); ok && v > 0 {
				d.mean(localDay(t, off), "resp_rate", v)
			}
		}
	case "tracker.skin_temperature", "skin_temperature":
		t, ok := at(r.get("start_time"))
		if !ok {
			return
		}
		if v, ok := r.num("temperature"); ok && v > 20 && v < 45 {
			d.mean(localDay(t, off), "skin_temp_c", v)
		}
	case "tracker.heart_rate_variability":
		t, ok := at(r.get("start_time"))
		if !ok {
			return
		}
		for _, key := range []string{"hrv", "rmssd", "heart_rate_variability"} {
			if v, ok := r.num(key); ok && v > 0 {
				d.mean(localDay(t, off), "hrv_ms", v)
				return
			}
		}
	case "exercise":
		t, ok := at(r.get("start_time"))
		if !ok {
			return
		}
		if v, ok := r.num("duration"); ok && v > 0 {
			d.add(localDay(t, off), "exercise_minutes", v/60000)
		}
	case "weight":
		t, ok := at(r.get("start_time"))
		if !ok {
			return
		}
		day := localDay(t, off)
		if v, ok := r.num("weight"); ok && v > 0 {
			d.day(day)["weight_kg"] = v
		}
		if v, ok := r.num("body_fat"); ok && v > 0 {
			d.day(day)["body_fat_pct"] = v
		}
	case "tracker.oxygen_saturation":
		t, ok := at(r.get("start_time"))
		if !ok {
			return
		}
		if v, ok := r.num("spo2"); ok && v > 0 {
			d.mean(localDay(t, off), "spo2_pct", v)
		}
	case "stress":
		t, ok := at(r.get("start_time"))
		if !ok {
			return
		}
		if v, ok := r.num("score"); ok && v > 0 {
			d.mean(localDay(t, off), "stress", v)
		}
	case "floors_climbed":
		t, ok := at(r.get("start_time"))
		if !ok {
			return
		}
		if v, ok := r.num("floor"); ok && v > 0 {
			d.add(localDay(t, off), "floors", v)
		}
	}
}

func jsonNum(v any) (float64, bool) {
	switch x := v.(type) {
	case float64:
		return x, !math.IsNaN(x)
	case json.Number:
		f, err := x.Float64()
		return f, err == nil
	case string:
		f, err := strconv.ParseFloat(x, 64)
		return f, err == nil
	}
	return 0, false
}

// finish turns the accumulators into the day metrics.
func (d *days) finish() {
	// the nights: overlapping sessions merged, the minutes under the day the night ends
	for day, ivs := range d.sleeps {
		sort.Slice(ivs, func(i, j int) bool { return ivs[i][0] < ivs[j][0] })
		var merged [][2]int64
		for _, iv := range ivs {
			if n := len(merged); n > 0 && iv[0] <= merged[n-1][1] {
				if iv[1] > merged[n-1][1] {
					merged[n-1][1] = iv[1]
				}
			} else {
				merged = append(merged, iv)
			}
		}
		var minutes float64
		for _, iv := range merged {
			minutes += float64(iv[1]-iv[0]) / 60
		}
		if minutes > 0 {
			d.day(day)["sleep_minutes"] = minutes
		}
	}
	// the stages, under their session's end day (or their own when the session is unknown)
	for id, st := range d.stages {
		day := d.stageDay[id]
		if day == "" {
			day = d.stageDay["?"+id]
		}
		if day == "" {
			continue
		}
		for metric, minutes := range st {
			d.add(day, metric, minutes)
		}
	}
	// the means, and the heart rate's three numbers
	for metric, byDay := range d.means {
		for day, vals := range byDay {
			if len(vals) == 0 {
				continue
			}
			sum, lo, hi := 0.0, vals[0], vals[0]
			for _, v := range vals {
				sum += v
				if v < lo {
					lo = v
				}
				if v > hi {
					hi = v
				}
			}
			if metric == "hr" {
				m := d.day(day)
				m["hr_avg"] = sum / float64(len(vals))
				m["hr_min"] = lo
				m["hr_max"] = hi
				continue
			}
			d.day(day)[metric] = sum / float64(len(vals))
		}
	}
}

// Scan lists the download's tables, their rows and spans, and what the import would take.
func Scan(zipPath string) (Report, error) {
	rep := Report{File: zipPath}
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return rep, fmt.Errorf("not a zip: %w", err)
	}
	defer zr.Close()
	seen := map[string]bool{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() || !strings.HasSuffix(strings.ToLower(f.Name), ".csv") {
			continue
		}
		name := tableName(f.Name)
		t := Table{Name: name, File: f.Name, Read: reads[name]}
		if t.Read == "" {
			t.Read = "not read"
		}
		rc, err := f.Open()
		if err != nil {
			t.Err = err.Error()
			rep.Tables = append(rep.Tables, t)
			continue
		}
		n, rerr := readCSV(rc, func(r row) {
			off := offset(r.get("time_offset"))
			for _, key := range []string{"day_time", "start_time"} {
				if tm, ok := at(r.get(key)); ok {
					day := localDay(tm, off)
					if t.From == "" || day < t.From {
						t.From = day
					}
					if t.To == "" || day > t.To {
						t.To = day
					}
					if reads[name] != "" {
						seen[day] = true
					}
					break
				}
			}
		})
		rc.Close()
		t.Rows = n
		if rerr != nil {
			t.Err = rerr.Error()
		}
		rep.Tables = append(rep.Tables, t)
	}
	sort.Slice(rep.Tables, func(i, j int) bool { return rep.Tables[i].Name < rep.Tables[j].Name })
	rep.Days = len(seen)
	if len(rep.Tables) == 0 {
		rep.Note = "no CSV files in the zip: a Samsung Health download has one per table"
	}
	return rep, nil
}

// Progress is how an import is going, for the readout.
type Progress struct {
	File      string `json:"file"`
	State     string `json:"state"` // reading, writing, done, failed
	Table     string `json:"table,omitempty"`
	Tables    int    `json:"tables"`
	Rows      int    `json:"rows"`
	Days      int    `json:"days"`    // days written
	Months    int    `json:"months"`  // batches written
	Samples   int    `json:"samples"` // heart rate readings written
	Oldest    string `json:"oldest,omitempty"`
	Newest    string `json:"newest,omitempty"`
	Error     string `json:"error,omitempty"`
	StartedAt int64  `json:"startedAt"`
	DoneAt    int64  `json:"doneAt,omitempty"`
}

// Batches reads the download into month batches, oldest first, the shape tally.Ingest takes.
// progress is told each table as it is read.
func Batches(zipPath string, progress func(table string, rows int)) ([]tally.Batch, error) {
	zr, err := zip.OpenReader(zipPath)
	if err != nil {
		return nil, fmt.Errorf("not a zip: %w", err)
	}
	defer zr.Close()
	d := newDays()
	// the binning files by name, wherever the zip keeps them (jsons/<table>/<name>)
	jsons := map[string]*zip.File{}
	for _, f := range zr.File {
		if !f.FileInfo().IsDir() && strings.HasSuffix(strings.ToLower(f.Name), ".json") {
			jsons[path.Base(f.Name)] = f
		}
	}
	if len(jsons) > 0 {
		d.bins = func(name string) []map[string]any {
			f := jsons[path.Base(strings.TrimSpace(name))]
			if f == nil {
				return nil
			}
			rc, err := f.Open()
			if err != nil {
				return nil
			}
			defer rc.Close()
			var out []map[string]any
			if json.NewDecoder(io.LimitReader(rc, 64<<20)).Decode(&out) != nil {
				return nil
			}
			return out
		}
	}
	// the sessions before their stages, so a stage finds its night
	order := func(name string) int {
		switch name {
		case "sleep":
			return 0
		default:
			return 1
		}
	}
	files := make([]*zip.File, 0, len(zr.File))
	for _, f := range zr.File {
		if !f.FileInfo().IsDir() && strings.HasSuffix(strings.ToLower(f.Name), ".csv") && reads[tableName(f.Name)] != "" && tableName(f.Name) != "tracker.pedometer_step_count" {
			files = append(files, f)
		}
	}
	sort.SliceStable(files, func(i, j int) bool { return order(tableName(files[i].Name)) < order(tableName(files[j].Name)) })
	for _, f := range files {
		name := tableName(f.Name)
		rc, err := f.Open()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, err)
		}
		n, rerr := readCSV(rc, func(r row) { d.take(name, r) })
		rc.Close()
		if rerr != nil {
			return nil, fmt.Errorf("%s: %w", f.Name, rerr)
		}
		if progress != nil {
			progress(name, n)
		}
	}
	d.finish()
	// into months
	byMonth := map[string]*tally.Batch{}
	for day, m := range d.m {
		if len(m) == 0 || len(day) < 7 {
			continue
		}
		month := day[:7]
		b := byMonth[month]
		if b == nil {
			b = &tally.Batch{}
			byMonth[month] = b
		}
		mm := make(map[string]float64, len(m))
		for k, v := range m {
			mm[k] = math.Round(v*100) / 100
		}
		b.Days = append(b.Days, tally.Day{Day: day, Metrics: mm})
	}
	for ts, v := range d.samples {
		month := time.Unix(ts, 0).UTC().Format("2006-01")
		b := byMonth[month]
		if b == nil {
			b = &tally.Batch{}
			byMonth[month] = b
		}
		b.Samples = append(b.Samples, tally.Sample{Metric: "heart_rate", TS: ts, Value: v})
	}
	months := make([]string, 0, len(byMonth))
	for m := range byMonth {
		months = append(months, m)
	}
	sort.Strings(months)
	out := make([]tally.Batch, 0, len(months))
	for _, m := range months {
		b := byMonth[m]
		sort.Slice(b.Days, func(i, j int) bool { return b.Days[i].Day < b.Days[j].Day })
		sort.Slice(b.Samples, func(i, j int) bool { return b.Samples[i].TS < b.Samples[j].TS })
		out = append(out, *b)
	}
	return out, nil
}
