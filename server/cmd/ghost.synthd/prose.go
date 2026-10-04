package main

// prosePass , THE MEMORIES, WRITTEN. The outing memories were built without the model on purpose
// (a memory of a trip exists the week it happened, GPU or no GPU): a template over real numbers.
// With the model on the GPU, this pass asks it to write the memory FROM A FACT SHEET , the
// numbers, places, dates, what the photos showed, the day's route , a few sentences in the second
// person, and keeps the result only when it stays inside the facts: every number in the text must
// be a number in the sheet, nothing that reads like a list or a refusal, a sane length. Fail the
// check and the template stands, unchanged. The person's edits and tombstones outrank all of it.
//
// The outings only (kind='outing', body rewritten, template kept in meta so a changed outing
// gets written again); the DAYS have their own pass and table (days.go, day_summaries). A few per
// pass, newest first, background priority, never when the model is on the CPU (minutes each, and
// the template is already there).

import (
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/dayroute"
	"github.com/LocalGhostDao/localghost/server/internal/oracle"
	"github.com/LocalGhostDao/localghost/server/internal/poltergres"
)

const (
	proseOutingsPerPass = 3
	proseMaxTries       = 3
)

var lastProsePass time.Time

// prosePass returns how many memories the model wrote this pass.
func prosePass(db *poltergres.ReadWrite, oc *oracle.Client, mount string, lg *slog.Logger) (int, error) {
	if time.Since(lastProsePass) < 10*time.Minute {
		return 0, nil
	}
	lastProsePass = time.Now()
	onGPU, err := oc.OnGPU()
	if err != nil || !onGPU {
		return 0, nil // the template is already there; minutes per memory on the CPU is not worth it
	}
	return outingProse(db, oc, mount, lg)
}

// --- the outings ---

func outingProse(db *poltergres.ReadWrite, oc *oracle.Client, mount string, lg *slog.Logger) (int, error) {
	rows, err := db.Query(`SELECT id, title, body, meta::text FROM memories
		WHERE kind = 'outing' AND NOT user_edited AND NOT tombstoned
		  AND (meta->>'prose') IS NULL AND coalesce((meta->>'prose_tries')::int, 0) < $1
		ORDER BY created_at DESC LIMIT $2`, proseMaxTries, proseOutingsPerPass)
	if err != nil {
		return 0, err
	}
	written := 0
	for _, v := range rows.Vals {
		if len(v) < 4 || v[0] == nil || v[1] == nil || v[2] == nil || v[3] == nil {
			continue
		}
		id := *v[0]
		var meta map[string]any
		if json.Unmarshal([]byte(*v[3]), &meta) != nil {
			continue
		}
		facts := outingFacts(db, mount, *v[1], meta)
		prose, ok := writeMemory(oc, facts, "an outing")
		if !ok {
			lg.Info("outing prose not kept", "fn", "outingProse", "id", id, "title", *v[1])
			_ = db.Exec("UPDATE memories SET meta = jsonb_set(coalesce(meta,'{}'::jsonb), '{prose_tries}', to_jsonb(coalesce((meta->>'prose_tries')::int, 0) + 1)) WHERE id = $1", id)
			continue
		}
		if err := db.Exec(`UPDATE memories SET body = $1,
			meta = coalesce(meta,'{}'::jsonb) || jsonb_build_object('prose', $1::text, 'template', $2::text), updated_at = $3 WHERE id = $4`,
			prose, *v[2], time.Now().UnixMilli(), id); err != nil {
			return written, err
		}
		written++
		lg.Info("outing memory written by the model", "fn", "outingProse", "id", id, "title", *v[1], "chars", len(prose))
	}
	return written, nil
}

// outingFacts is the sheet for an outing: the memory's own numbers and places, the top tags, the
// captions of its cover frames, and the route lines of its days.
func outingFacts(db *poltergres.ReadWrite, mount, title string, meta map[string]any) []string {
	var f []string
	f = append(f, "Title: "+title)
	start, _ := metaInt(meta, "start")
	end, _ := metaInt(meta, "end")
	if start > 0 {
		f = append(f, "From: "+time.Unix(start, 0).UTC().Format("Monday 2 January 2006")+" to "+time.Unix(end, 0).UTC().Format("Monday 2 January 2006"))
	}
	if d, ok := metaInt(meta, "days"); ok {
		f = append(f, "Days: "+strconv.FormatInt(d, 10))
	}
	if p, ok := metaInt(meta, "photos"); ok {
		f = append(f, "Photos taken: "+strconv.FormatInt(p, 10))
	}
	if s, _ := meta["place"].(string); s != "" {
		f = append(f, "Main place: "+s)
	}
	if s, _ := meta["country"].(string); s != "" {
		f = append(f, "Country: "+s)
	}
	if ps := metaStrings(meta, "places"); len(ps) > 0 {
		f = append(f, "Places: "+strings.Join(ps, ", "))
	}
	if away, _ := meta["away"].(bool); away {
		if m, ok := metaFloat(meta, "fromHomeM"); ok && m > 0 {
			f = append(f, fmt.Sprintf("A trip, about %d km from home", int(m/1000+0.5)))
		} else {
			f = append(f, "A trip away from home")
		}
	}
	if m, ok := metaFloat(meta, "distanceM"); ok && m >= 500 {
		f = append(f, fmt.Sprintf("Moved about %d km over the days (on foot or by vehicle)", int(m/1000+0.5)))
	}
	if tags, ok := meta["tags"].([]any); ok && len(tags) > 0 {
		var names []string
		for _, t := range tags {
			if tm, ok := t.(map[string]any); ok {
				if nm, _ := tm["name"].(string); nm != "" {
					names = append(names, nm)
				}
			}
			if len(names) == 10 {
				break
			}
		}
		if len(names) > 0 {
			f = append(f, "Seen in the photos (most often first): "+strings.Join(names, ", "))
		}
	}
	for _, c := range captionsFor(db, metaStrings(meta, "covers"), 6) {
		f = append(f, "A photo shows: "+c)
	}
	if start > 0 {
		lines := 0
		for d := start; d <= end && lines < 8; d += 86400 {
			if r := routeOf(mount, time.Unix(d, 0).UTC().Format("2006-01-02")); r != nil && r.Line != "" {
				f = append(f, time.Unix(d, 0).UTC().Format("Mon 2 Jan")+": "+r.Line)
				lines++
			}
		}
	}
	return f
}

func photosNote(n int) string {
	switch n {
	case 0:
		return ""
	case 1:
		return " (took a photo there)"
	}
	return fmt.Sprintf(" (took %d photos there)", n)
}

func clockOf(ts int64) string { return time.Unix(ts, 0).UTC().Format("15:04") + " UTC" }

func kmText(m float64) string {
	if m < 10000 {
		return strconv.FormatFloat(float64(int(m/100+0.5))/10, 'f', -1, 64) + " km"
	}
	return strconv.Itoa(int(m/1000+0.5)) + " km"
}

// --- the model, and the check ---

// writeMemory asks the model for the memory and keeps it only when it stays inside the facts.
func writeMemory(oc *oracle.Client, facts []string, what string) (string, bool) {
	if len(facts) < 3 {
		return "", false
	}
	resp, err := oc.Infer(oracle.Request{
		Capability: "summarize", Class: oracle.ClassLocalSmall, Priority: oracle.PriorityBackground,
		Input: memoryPrompt(facts, what), MaxTokens: 260, Temperature: 0.4, DeadlineMS: 120000,
	})
	if err != nil || resp.Err != "" {
		return "", false
	}
	return groundedProse(resp.Output, facts)
}

func memoryPrompt(facts []string, what string) string {
	var b strings.Builder
	b.WriteString("You write a short memory of " + what + " for the person who lived it, from the facts below and nothing else.\n\nFACTS:\n")
	for _, f := range facts {
		b.WriteString("- " + f + "\n")
	}
	b.WriteString(`
Write it in the second person ("you"), past tense, plain and warm, as a memory rather than a report: start with what mattered most (a place, a walk, an outing, something you wrote), keep the order of the day, name the places, keep every number exactly as the facts give it, and end on the evening (how you felt, the sleep) when the facts have it. Two to five sentences; when the facts are thin, one or two. Use only what the facts say: every place, number, time and activity must come from them. No headings, no lists, no exclamation marks, no mention of photos as data, tags, fixes, steps counted or "the facts". Do not add weather, people, feelings or reasons the facts do not give. Reply with the memory only.`)
	return b.String()
}

var (
	numberRe = regexp.MustCompile(`\d+(?:[.,]\d+)?`)
	badProse = []string{"as an ai", "i cannot", "i can't", "i'm sorry", "i am sorry", "the facts", "fact sheet", "here is", "here's a", "memory:", "**", "##"}
)

// groundedProse trims the model's answer and checks it against the sheet: a sane length, no list
// or heading, no refusal, and every number in it present in the facts (so "27 km" may appear,
// "three weeks" may not become "21 days"). Numbers in the facts are compared as written and as
// their integer part, so "2.3 km" in the facts allows "2.3" and "2".
func groundedProse(out string, facts []string) (string, bool) {
	s := strings.TrimSpace(out)
	s = strings.Trim(s, "\"“”")
	if i := strings.Index(s, "\n\n"); i > 0 && strings.Contains(strings.ToLower(s[:i]), "memory") && i < 40 {
		s = strings.TrimSpace(s[i:]) // "Here is the memory:" on its own line
	}
	if len(s) < 60 || len(s) > 900 {
		return "", false
	}
	low := strings.ToLower(s)
	for _, b := range badProse {
		if strings.Contains(low, b) {
			return "", false
		}
	}
	for _, line := range strings.Split(s, "\n") {
		t := strings.TrimSpace(line)
		if strings.HasPrefix(t, "-") || strings.HasPrefix(t, "*") || strings.HasPrefix(t, "•") || strings.HasPrefix(t, "#") {
			return "", false
		}
	}
	if strings.Contains(s, "!") {
		s = strings.ReplaceAll(s, "!", ".")
	}
	allowed := map[string]bool{}
	for _, f := range facts {
		for _, n := range numberRe.FindAllString(f, -1) {
			allowed[n] = true
			allowed[strings.ReplaceAll(n, ",", ".")] = true
			allowed[strings.ReplaceAll(n, ",", "")] = true
			if i := strings.IndexAny(n, ".,"); i > 0 {
				allowed[n[:i]] = true
			}
		}
	}
	for _, n := range numberRe.FindAllString(s, -1) {
		// "8,400" is 8400 with a thousands separator; "2,3" is 2.3 to a continental hand
		if !allowed[n] && !allowed[strings.ReplaceAll(n, ",", ".")] && !allowed[strings.ReplaceAll(n, ",", "")] {
			return "", false
		}
	}
	return plainDashes(strings.Join(strings.Fields(strings.ReplaceAll(s, "\n", " ")), " ")), true
}

// --- helpers ---

// pathHeights is the day's climb and highest point from its path (framed writes them when the box
// has elevation tiles); zeros when it has none.
func pathHeights(mount, day string) (climb, high float64) {
	b, err := os.ReadFile(filepath.Join(mount, "frames", "paths", day+".geojson"))
	if err != nil {
		return 0, 0
	}
	var doc struct {
		Features []struct {
			Geometry struct {
				Type string `json:"type"`
			} `json:"geometry"`
			Properties struct {
				ClimbM float64 `json:"climbM"`
				HighM  float64 `json:"highM"`
			} `json:"properties"`
		} `json:"features"`
	}
	if json.Unmarshal(b, &doc) != nil {
		return 0, 0
	}
	for _, f := range doc.Features {
		if f.Geometry.Type == "LineString" {
			return f.Properties.ClimbM, f.Properties.HighM
		}
	}
	return 0, 0
}

// routeOf reads a day's route as framed wrote it; nil when the day has none.
func routeOf(mount, day string) *dayroute.Day {
	b, err := os.ReadFile(filepath.Join(mount, "frames", "paths", day+".route.json"))
	if err != nil {
		return nil
	}
	var d dayroute.Day
	if json.Unmarshal(b, &d) != nil {
		return nil
	}
	return &d
}

var captionsUnavailable bool

// captionsFor is the SCENE of a few frames' captions, from searchd's originals (the frame hash is
// the first 16 bytes of the original's sha256). Empty when the search schema is out of reach.
func captionsFor(db *poltergres.ReadWrite, hashes []string, max int) []string {
	if db == nil || captionsUnavailable || len(hashes) == 0 || max <= 0 {
		return nil
	}
	var out []string
	for _, h := range hashes {
		if len(out) >= max {
			break
		}
		if len(h) != 32 {
			continue
		}
		rows, err := db.Query(`SELECT meta->>'caption' FROM search.originals WHERE source = 'image' AND substring(sha256 from 1 for 16) = decode($1, 'hex') LIMIT 1`, h)
		if err != nil {
			captionsUnavailable = true
			slog.Info("captions not readable from synthd; memories are written without them", "fn", "captionsFor", "err", err)
			return out
		}
		if len(rows.Vals) == 0 || len(rows.Vals[0]) == 0 || rows.Vals[0][0] == nil {
			continue
		}
		c := *rows.Vals[0][0]
		if i := strings.Index(c, "SCENE:"); i >= 0 {
			c = c[i+len("SCENE:"):]
			for _, next := range []string{"OBJECTS:", "PEOPLE:", "TEXT:", "COLOURS_STYLE:", "SETTING_GUESS:"} {
				if j := strings.Index(c, next); j >= 0 {
					c = c[:j]
				}
			}
		} else {
			continue // a caption without sections is not one (searchd discards these at its next pass)
		}
		c = strings.Join(strings.Fields(c), " ")
		if len(c) < 20 {
			continue
		}
		out = append(out, clip(c, 280))
	}
	return out
}

func metaInt(m map[string]any, k string) (int64, bool) {
	switch v := m[k].(type) {
	case float64:
		return int64(v), true
	case string:
		n, err := strconv.ParseInt(v, 10, 64)
		return n, err == nil
	}
	return 0, false
}

func metaFloat(m map[string]any, k string) (float64, bool) {
	if v, ok := m[k].(float64); ok {
		return v, true
	}
	return 0, false
}

func metaStrings(m map[string]any, k string) []string {
	var out []string
	if a, ok := m[k].([]any); ok {
		for _, x := range a {
			if s, ok := x.(string); ok && s != "" {
				out = append(out, s)
			}
		}
	}
	return out
}

func uniqueStrings(in []string) []string {
	seen := map[string]bool{}
	var out []string
	for _, s := range in {
		if !seen[s] {
			seen[s] = true
			out = append(out, s)
		}
	}
	return out
}
