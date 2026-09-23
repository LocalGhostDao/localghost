package framed

import (
	"bufio"
	"encoding/json"
	"os"
	"strconv"
	"strings"
	"testing"
	"time"
)

// glitchCase is one block of testdata/trail_glitches.txt: a name, points in time order, and which
// of them the rules must drop (marked x). The same file is read by the phone's TrailCleanTest, so
// the box and the phone are held to the same verdicts.
type glitchCase struct {
	name string
	pts  []TrackPoint
	drop map[int64]bool
}

func loadGlitchCases(t *testing.T) []glitchCase {
	t.Helper()
	f, err := os.Open("testdata/trail_glitches.txt")
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	var cases []glitchCase
	var cur *glitchCase
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		switch {
		case line == "":
			if cur != nil {
				cases = append(cases, *cur)
				cur = nil
			}
		case strings.HasPrefix(line, "#"):
			cur = &glitchCase{name: strings.TrimSpace(strings.SplitN(strings.TrimPrefix(line, "#"), ":", 2)[0]), drop: map[int64]bool{}}
		default:
			if cur == nil {
				t.Fatalf("point before a case header: %q", line)
			}
			parts := strings.Fields(line)
			ts, _ := strconv.ParseInt(parts[0], 10, 64)
			lat, _ := strconv.ParseFloat(parts[1], 64)
			lon, _ := strconv.ParseFloat(parts[2], 64)
			cur.pts = append(cur.pts, TrackPoint{TS: ts, Lat: lat, Lon: lon})
			if len(parts) > 3 && parts[3] == "x" {
				cur.drop[ts*1000000+int64(len(cur.pts))] = true
			}
		}
	}
	if cur != nil {
		cases = append(cases, *cur)
	}
	if len(cases) < 5 {
		t.Fatalf("only %d cases read", len(cases))
	}
	return cases
}

func TestCleanTrackAgainstTheSharedFixture(t *testing.T) {
	for _, c := range loadGlitchCases(t) {
		kept, dropped := CleanTrack(c.pts)
		// The expected kept set, by position in the case (points can share a second).
		var wantKept []TrackPoint
		for i, p := range c.pts {
			if !c.drop[p.TS*1000000+int64(i+1)] {
				wantKept = append(wantKept, p)
			}
		}
		if dropped != len(c.pts)-len(wantKept) {
			t.Errorf("%s: dropped %d, want %d (kept %d of %d)", c.name, dropped, len(c.pts)-len(wantKept), len(kept), len(c.pts))
		}
		if len(kept) != len(wantKept) {
			t.Errorf("%s: kept %d points, want %d", c.name, len(kept), len(wantKept))
			continue
		}
		for i := range kept {
			if kept[i] != wantKept[i] {
				t.Errorf("%s: kept[%d] = %+v, want %+v", c.name, i, kept[i], wantKept[i])
			}
		}
	}
}

func TestCleanTrackLeavesShortAndUnsortedInputSensible(t *testing.T) {
	if k, d := CleanTrack(nil); len(k) != 0 || d != 0 {
		t.Fatal("nil in, nil out")
	}
	one := []TrackPoint{{TS: 1, Lat: 39.15, Lon: 20.22}}
	if k, d := CleanTrack(one); len(k) != 1 || d != 0 {
		t.Fatal("one point is a trail of one")
	}
	// Out-of-order input is sorted first: the spike is judged in time order, not file order.
	pts := []TrackPoint{
		{TS: 1758001800, Lat: 39.1526, Lon: 20.22},
		{TS: 1758000900, Lat: 40.05, Lon: 20.22}, // the spike, second in time
		{TS: 1758000000, Lat: 39.15, Lon: 20.22},
		{TS: 1758002700, Lat: 39.1539, Lon: 20.22},
	}
	k, d := CleanTrack(pts)
	if d != 1 || len(k) != 3 || k[0].TS != 1758000000 || k[1].TS != 1758001800 {
		t.Fatalf("unsorted spike: kept %+v dropped %d", k, d)
	}
}

// A spike in the first minutes of a day is judged by its neighbours across midnight: rebuildDay
// hands BuildDayPath two hours either side, and the day's own count, distance and glitches are
// over the day's own points only.
func TestBuildDayPathCleansAcrossMidnight(t *testing.T) {
	day := time.Date(2026, 9, 21, 0, 0, 0, 0, time.UTC)
	mid := day.Unix()
	pts := []TrackPoint{
		{TS: mid - 1800, Lat: 39.1500, Lon: 20.22}, // yesterday, 23:30
		{TS: mid - 900, Lat: 39.1513, Lon: 20.22},  // yesterday, 23:45
		{TS: mid + 300, Lat: 40.0500, Lon: 20.22},  // 00:05, 100 km away: the spike
		{TS: mid + 1200, Lat: 39.1526, Lon: 20.22}, // 00:20, back on the island
		{TS: mid + 2100, Lat: 39.1539, Lon: 20.22},
		{TS: mid + 3000, Lat: 39.1552, Lon: 20.22},
	}
	doc, err := BuildDayPath(day, pts, nil)
	if err != nil {
		t.Fatal(err)
	}
	var fc struct {
		Features []struct {
			Geometry   struct{ Coordinates [][2]float64 }
			Properties map[string]any
		}
	}
	if err := json.Unmarshal(doc, &fc); err != nil {
		t.Fatal(err)
	}
	if len(fc.Features) != 1 {
		t.Fatalf("features: %d", len(fc.Features))
	}
	pr := fc.Features[0].Properties
	if pr["points"].(float64) != 4 || pr["glitches"].(float64) != 1 {
		t.Fatalf("points %v glitches %v, want 4 and 1", pr["points"], pr["glitches"])
	}
	if d := pr["distanceM"].(float64); d < 250 || d > 350 {
		t.Fatalf("distance %v: the spike must not count, three 145m hops must", d)
	}
	for _, c := range fc.Features[0].Geometry.Coordinates {
		if c[1] > 39.5 {
			t.Fatalf("the spike is drawn: %v", c)
		}
	}
	times := pr["times"].([]any)
	if first := int64(times[0].(float64)); first < mid {
		t.Fatalf("yesterday's context point %d leaked into the day", first)
	}
}
