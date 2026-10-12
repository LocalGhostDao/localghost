// Package tally is ghost.tallyd's work: the phone's Health Connect readout (steps, sleep, heart
// rate and the rest, a day-batch at a time) into health_metrics and health_samples, and a journal
// line per day for the distiller. Pure over the database, so the tests run it against Postgres.
package tally

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

// Day is one day's totals as the phone sends them.
type Day struct {
	Day     string             `json:"day"`
	Metrics map[string]float64 `json:"metrics"`
}

// Sample is one high-resolution reading (heart rate, thinned to five minutes on the phone).
type Sample struct {
	Metric string  `json:"metric"`
	TS     int64   `json:"ts"`
	Value  float64 `json:"value"`
}

// Batch is the upload's shape.
type Batch struct {
	Days    []Day    `json:"days"`
	Samples []Sample `json:"samples"`
}

// Result says what an ingest did.
type Result struct {
	Days       int    `json:"days"`    // days written
	Metrics    int    `json:"metrics"` // day-metric rows written
	Samples    int    `json:"samples"` // samples written (after de-duplication)
	Dropped    int    `json:"dropped"` // rows the batch carried that could not be stored (a bad day, a long name)
	NewestDay  string `json:"newestDay"`
	OldestDay  string `json:"oldestDay"`
	Unparsable bool   `json:"unparsable"` // the file was not a batch at all (dropped, never retried)
}

// RestingEstimate is the metric older phone builds sent as "calories": Health Connect's total
// calories, which on a box fed by Samsung Health is a resting estimate (the same 1,564 kcal every
// day). It is not kept; "active_calories" is the measurement.
const RestingEstimate = "calories"

// KeepHigher names the day counters that only grow as a day's data arrives: an upload with a
// lower value than the box holds leaves the box's.
var KeepHigher = map[string]bool{
	"steps": true, "distance_km": true, "active_calories": true, "floors": true, "exercise_minutes": true, "active_minutes": true,
}

// metricOK: a short lowercase name with underscores and digits, nothing a phone could smuggle
// SQL or a novel into.
func metricOK(m string) bool {
	if m == "" || len(m) > 40 {
		return false
	}
	for _, r := range m {
		if !(r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r == '_') {
			return false
		}
	}
	return true
}

// Parse reads a batch; a file that is not one is reported, not an error (it is never retried).
func Parse(raw []byte) (Batch, bool) {
	var b Batch
	if err := json.Unmarshal(raw, &b); err != nil {
		return Batch{}, false
	}
	return b, true
}

// Ingest writes a batch. Samples go 500 to a statement, de-duplicated within the batch first: two
// readings at the same second (a watch and a phone both writing heart rate) in one statement
// made Postgres refuse it ("ON CONFLICT DO UPDATE command cannot affect row a second time"), the
// file stayed in the inbox failing every tick, and that day's totals never landed. Days go after
// the samples; a day's journal line is written once and REPLACED when a later upload refines the
// day (it used to be frozen at the first, partial, upload).
func Ingest(db *poltergres.ReadWrite, raw []byte) (Result, error) {
	var res Result
	batch, ok := Parse(raw)
	if !ok {
		res.Unparsable = true
		return res, nil
	}
	// samples: the newest value per (metric, ts) wins, in time order
	byKey := map[string]Sample{}
	for _, sm := range batch.Samples {
		if sm.TS <= 0 || !metricOK(sm.Metric) {
			res.Dropped++
			continue
		}
		byKey[sm.Metric+"|"+fmt.Sprint(sm.TS)] = sm
	}
	samples := make([]Sample, 0, len(byKey))
	for _, sm := range byKey {
		samples = append(samples, sm)
	}
	sort.Slice(samples, func(i, j int) bool {
		if samples[i].Metric != samples[j].Metric {
			return samples[i].Metric < samples[j].Metric
		}
		return samples[i].TS < samples[j].TS
	})
	for start := 0; start < len(samples); start += 500 {
		end := start + 500
		if end > len(samples) {
			end = len(samples)
		}
		chunk := samples[start:end]
		var sb strings.Builder
		sb.WriteString("INSERT INTO health_samples (metric, ts, value) VALUES ")
		args := make([]any, 0, len(chunk)*3)
		for i, sm := range chunk {
			if i > 0 {
				sb.WriteString(",")
			}
			fmt.Fprintf(&sb, "($%d,$%d,$%d)", i*3+1, i*3+2, i*3+3)
			args = append(args, sm.Metric, sm.TS, sm.Value)
		}
		sb.WriteString(" ON CONFLICT (metric, ts) DO UPDATE SET value = EXCLUDED.value")
		if err := db.Exec(sb.String(), args...); err != nil {
			return res, fmt.Errorf("samples: %w", err)
		}
		res.Samples += len(chunk)
	}
	for _, d := range batch.Days {
		t, perr := time.Parse("2006-01-02", d.Day)
		if perr != nil {
			res.Dropped++
			continue
		}
		wrote := 0
		for metric, val := range d.Metrics {
			if metric == RestingEstimate {
				res.Dropped++ // Health Connect's resting estimate from an older phone build: not a measurement
				continue
			}
			if !metricOK(metric) {
				res.Dropped++
				continue
			}
			// A COUNTER NEVER GOES DOWN. Two writers describe the same day: Samsung Health's own
			// download (the watch's full count) and Health Connect (what Samsung Health shared,
			// or the phone's own counter when it shared nothing); the six-hourly sync rewrites
			// the last week, and a lower count must not replace a fuller one. The counters keep
			// the larger value; a measurement (a weight, a mean heart rate) takes the latest.
			upsert := "INSERT INTO health_metrics (day, metric, value) VALUES ($1,$2,$3) ON CONFLICT (day, metric) DO UPDATE SET value = EXCLUDED.value"
			if KeepHigher[metric] {
				upsert = "INSERT INTO health_metrics (day, metric, value) VALUES ($1,$2,$3) ON CONFLICT (day, metric) DO UPDATE SET value = GREATEST(health_metrics.value, EXCLUDED.value)"
			}
			if err := db.Exec(upsert, d.Day, metric, val); err != nil {
				return res, fmt.Errorf("day %s: %w", d.Day, err)
			}
			wrote++
		}
		if wrote == 0 {
			continue
		}
		res.Days++
		res.Metrics += wrote
		if res.NewestDay == "" || d.Day > res.NewestDay {
			res.NewestDay = d.Day
		}
		if res.OldestDay == "" || d.Day < res.OldestDay {
			res.OldestDay = d.Day
		}
		if line := JournalLine(d.Metrics); line != "" {
			if err := db.Exec(
				"INSERT INTO journal_entries (source, ref, ts, title, body, created_at) VALUES ('ghost.tallyd', $1, $2, $3, $4, $5) "+
					"ON CONFLICT (source, ref) DO UPDATE SET body = EXCLUDED.body, ts = EXCLUDED.ts",
				"health:"+d.Day, t.Unix()+43200, "health , "+d.Day, line, time.Now().UnixMilli()); err != nil {
				return res, fmt.Errorf("journal %s: %w", d.Day, err)
			}
		}
	}
	return res, nil
}

// JournalLine is the day's measurements in a sentence for the distiller: what was measured, no
// interpretation. "" when nothing worth a line was.
func JournalLine(m map[string]float64) string {
	parts := ""
	if v, ok := m["sleep_minutes"]; ok && v > 0 {
		parts += fmt.Sprintf("Slept %dh%02dm. ", int(v)/60, int(v)%60)
	}
	if v, ok := m["steps"]; ok && v > 0 {
		parts += fmt.Sprintf("%d steps. ", int(v))
	}
	if v, ok := m["exercise_minutes"]; ok && v > 0 {
		parts += fmt.Sprintf("%d min of exercise. ", int(v))
	}
	if v, ok := m["distance_km"]; ok && v > 0.1 {
		parts += fmt.Sprintf("%.1f km. ", v)
	}
	if v, ok := m["floors"]; ok && v > 0 {
		parts += fmt.Sprintf("%d floors. ", int(v))
	}
	if v, ok := m["active_calories"]; ok && v > 0 {
		parts += fmt.Sprintf("%d active kcal. ", int(v))
	}
	if v, ok := m["hr_avg"]; ok && v > 0 {
		hi := ""
		if mx, ok2 := m["hr_max"]; ok2 && mx > 0 {
			hi = fmt.Sprintf(" (peak %d)", int(mx))
		}
		parts += fmt.Sprintf("Avg heart rate %d%s. ", int(v), hi)
	}
	if v, ok := m["weight_kg"]; ok && v > 0 {
		parts += fmt.Sprintf("Weight %.1f kg. ", v)
	}
	return strings.TrimSpace(parts)
}

// Status is what the box holds and what the last ingest did: `ghost-cli ghost.tallyd health` and
// the Box Status drill-in.
type Status struct {
	Days       int                `json:"days"`      // days with any metric
	Samples    int                `json:"samples"`   // high-resolution rows
	NewestDay  string             `json:"newestDay"` // across metrics
	Metrics    map[string]string  `json:"metrics"`   // metric -> its newest day
	LastValues map[string]float64 `json:"lastValues"`
}

// Query reads the Status from the tables.
func Query(db *poltergres.ReadWrite) (Status, error) {
	st := Status{Metrics: map[string]string{}, LastValues: map[string]float64{}}
	rows, err := db.Query("SELECT count(DISTINCT day), coalesce(max(day),'') FROM health_metrics")
	if err != nil {
		return st, err
	}
	if len(rows.Vals) == 1 && rows.Vals[0][0] != nil {
		fmt.Sscan(*rows.Vals[0][0], &st.Days)
		if rows.Vals[0][1] != nil {
			st.NewestDay = *rows.Vals[0][1]
		}
	}
	rows, err = db.Query("SELECT count(*) FROM health_samples")
	if err == nil && len(rows.Vals) == 1 && rows.Vals[0][0] != nil {
		fmt.Sscan(*rows.Vals[0][0], &st.Samples)
	}
	rows, err = db.Query(`SELECT DISTINCT ON (metric) metric, day, value FROM health_metrics ORDER BY metric, day DESC`)
	if err != nil {
		return st, err
	}
	for _, r := range rows.Vals {
		if len(r) == 3 && r[0] != nil && r[1] != nil {
			st.Metrics[*r[0]] = *r[1]
			if r[2] != nil {
				var v float64
				fmt.Sscan(*r[2], &v)
				st.LastValues[*r[0]] = v
			}
		}
	}
	return st, nil
}
