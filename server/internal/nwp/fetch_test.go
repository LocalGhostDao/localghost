package nwp

import (
	"bytes"
	"compress/bzip2"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/grib2"
	"github.com/LocalGhostDao/localghost/server/internal/weather"
)

// The three centres' layouts, served from internal/grib2's fixtures: ECMWF's one file a step
// with an index of byte ranges, DWD's one bz2 file a field a step, NOAA's one file a step
// with a .idx. The values pulled are checked against the fixtures sampled by hand.
func fixture(t *testing.T, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("..", "grib2", "testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func sampleFixture(t *testing.T, name string, lat, lon float64) (float64, bool) {
	t.Helper()
	m, err := grib2.NewReader(bytes.NewReader(fixture(t, name))).Next()
	if err != nil {
		t.Fatal(err)
	}
	vals, err := m.Values()
	if err != nil {
		t.Fatal(err)
	}
	v, ok := m.Grid.Sample(vals, lat, lon)
	return float64(v), ok
}

func TestPullsEveryLayout(t *testing.T) {
	run := time.Date(2026, 10, 10, 6, 0, 0, 0, time.UTC)
	steps := []int{0, 3, 6}
	t2 := fixture(t, "simple.grib2")             // ~283 K
	d2 := fixture(t, "simple12.grib2")           // rough, 280 ± 12
	tp := fixture(t, "complex_sd2_precip.grib2") // 0..3, an interval 3-6
	tcc := fixture(t, "ccsds.grib2")
	iconBz := fixture(t, "icon.grib2.bz2")
	// ECMWF: the fields concatenated, the index naming each
	ecmwfFile := append(append(append(append([]byte{}, t2...), d2...), tp...), tcc...)
	ecmwfIndex := func() string {
		var sb strings.Builder
		off := 0
		for _, f := range []struct {
			name string
			b    []byte
		}{{"2t", t2}, {"2d", d2}, {"tp", tp}, {"tcc", tcc}} {
			line, _ := json.Marshal(map[string]any{"param": f.name, "levtype": "sfc", "_offset": off, "_length": len(f.b), "step": "3"})
			sb.Write(line)
			sb.WriteByte('\n')
			off += len(f.b)
		}
		return sb.String()
	}()
	gfsFile := append(append(append([]byte{}, t2...), tp...), tcc...)
	gfsIdx := fmt.Sprintf("1:0:d=2026101006:TMP:2 m above ground:3 hour fcst:\n2:%d:d=2026101006:APCP:surface:0-3 hour acc fcst:\n3:%d:d=2026101006:TCDC:entire atmosphere:3 hour fcst:\n4:%d:d=2026101006:HGT:surface:3 hour fcst:\n",
		len(t2), len(t2)+len(tp), len(t2)+len(tp)+len(tcc))
	gfsFile = append(gfsFile, t2...) // HGT: the temperature field stands in for the ground
	requests := 0
	ranges := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		if r.Header.Get("Range") != "" {
			ranges++
		}
		p := r.URL.Path
		serve := func(b []byte) { http.ServeContent(w, r, "f", time.Time{}, bytes.NewReader(b)) }
		switch {
		case strings.HasPrefix(p, "/ecmwf/"):
			if !strings.Contains(p, "/20261010/06z/ifs/0p25/scda/2026101006") {
				http.NotFound(w, r)
				return
			}
			if strings.HasSuffix(p, ".index") {
				serve([]byte(ecmwfIndex))
			} else {
				serve(ecmwfFile)
			}
		case strings.HasPrefix(p, "/dwd/"):
			if !strings.Contains(p, "/06/") || !strings.Contains(p, "_2026101006_") {
				http.NotFound(w, r)
				return
			}
			serve(iconBz)
		case strings.HasPrefix(p, "/dwd2/"):
			// ICON-D2's names: ..._single-level_2026101006_003_2d_t_2m.grib2.bz2 and the ground
			// in ..._time-invariant_2026101006_000_0_hsurf.grib2.bz2
			if !strings.Contains(p, "/06/") || !strings.Contains(p, "_2026101006_") || !(strings.Contains(p, "_2d_") || strings.Contains(p, "time-invariant_2026101006_000_0_hsurf")) {
				http.NotFound(w, r)
				return
			}
			serve(iconBz)
		case strings.HasPrefix(p, "/noaa/"):
			if !strings.Contains(p, "/gfs.20261010/06/atmos/gfs.t06z.pgrb2.0p25.f") {
				http.NotFound(w, r)
				return
			}
			if strings.HasSuffix(p, ".idx") {
				serve([]byte(gfsIdx))
			} else {
				serve(gfsFile)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	old := map[string]string{}
	for k, v := range Bases {
		old[k] = v
	}
	Bases["ifs"], Bases["icon-eu"], Bases["icon-d2"], Bases["gfs"] = srv.URL+"/ecmwf", srv.URL+"/dwd", srv.URL+"/dwd2", srv.URL+"/noaa"
	t.Cleanup(func() {
		for k, v := range old {
			Bases[k] = v
		}
	})
	cells := []Cell{{ID: 1, Lat: 46.1, Lon: -5.3}, {ID: 2, Lat: 49.9, Lon: -9.9}, {ID: 3, Lat: 55, Lon: 2}}
	fix := &Fix{Lat: 46.1, Lon: -5.3}
	f := NewFetcher()
	f.pause = 0
	ctx := context.Background()
	var pulled []*Run
	for _, id := range []string{"ifs", "icon-eu", "icon-d2", "gfs"} {
		m, _ := ModelByID(id)
		m.Steps = steps
		m.Domain = nil // the fixtures cover the Bay of Biscay, not the model's own domain
		if !f.Complete(ctx, m, run) {
			t.Fatalf("%s: the run is not complete", id)
		}
		if f.Complete(ctx, m, run.Add(6*time.Hour)) {
			t.Fatalf("%s: a run not published is complete", id)
		}
		r, p, err := f.Pull(ctx, m, run, cells, fix)
		if err != nil {
			t.Fatal(err)
		}
		if !Enough(r, m) {
			t.Fatalf("%s: not enough: %+v", id, p)
		}
		pulled = append(pulled, r)
		// the window around the fix: the model's grid, three points either way of the fix's
		// cell at 0.25° (eight across), the fix's own value the same as the cell's (the same
		// four points, the same weights)
		if r.Win == nil || r.Win.Ni != 8 || r.Win.Nj != 8 || !r.Win.Covers(46.1, -5.3) || r.Win.Covers(46.1, -3.0) {
			t.Fatalf("%s: window %+v", id, r.Win)
		}
		if wv, ok := r.Win.sample(1, Temp2m, 46.1, -5.3); !ok {
			t.Fatalf("%s: no window value", id)
		} else if cv, _ := r.Value(0, 1, Temp2m); math.Abs(wv-cv) > 0.011 {
			t.Fatalf("%s: window %v, cell %v", id, wv, cv)
		}
		if id == "icon-d2" {
			if math.IsNaN(r.Win.orogAt(46.1, -5.3)) || p.Fields != 6*3-1 {
				t.Fatalf("d2: ground %v, progress %+v", r.Win.orogAt(46.1, -5.3), p)
			}
			continue
		}
		// cell 1 inside the grid: the temperature from the fixture, in °C
		want, _ := sampleFixture(t, "simple.grib2", 46.1, -5.3)
		got, ok := r.Value(0, 1, Temp2m)
		if !ok || math.Abs(got-(want-273.15)) > 0.011 {
			t.Fatalf("%s: temperature %v %v, want %v", id, got, ok, want-273.15)
		}
		if _, ok := r.Value(2, 1, Temp2m); ok {
			t.Fatalf("%s: a cell outside the grid has a value", id)
		}
		switch id {
		case "ifs":
			// tp in metres times a thousand; the interval field says it accumulates from 3
			w, _ := sampleFixture(t, "complex_sd2_precip.grib2", 46.1, -5.3)
			if got, ok := r.Value(0, 1, Precip); !ok || math.Abs(got-w*1000) > 0.06 || r.PrecipFrom[1] != 3 {
				t.Fatalf("ifs precip %v %v from %d, want %v", got, ok, r.PrecipFrom[1], w*1000)
			}
			// tcc as a fraction of one, times a hundred
			wc, _ := sampleFixture(t, "ccsds.grib2", 46.1, -5.3)
			if got, ok := r.Value(0, 1, Cloud); !ok || got != math.Round(wc*100) {
				t.Fatalf("ifs cloud %v %v, want %v", got, ok, math.Round(wc*100))
			}
			if _, ok := r.Value(0, 0, Precip); ok {
				t.Fatal("tp at step 0")
			}
			if math.IsNaN(float64(r.Cells[0].Orog)) != true {
				t.Fatal("IFS has no orography")
			}
			if p.Fields != 4+3+4 || p.Missing != 0 || ranges == 0 {
				t.Fatalf("ifs progress %+v, ranges %d", p, ranges)
			}
		case "icon-eu":
			// every field the same fixture, the ground too
			if r.Cells[0].Orog != float32(want) || !math.IsNaN(float64(r.Cells[2].Orog)) {
				t.Fatalf("icon ground %v %v", r.Cells[0].Orog, r.Cells[2].Orog)
			}
			if got, ok := r.Value(0, 2, Cloud); !ok || got != math.Round(want) {
				t.Fatalf("icon cloud %v %v", got, ok)
			}
			if p.Fields != 6*3-1 || p.Missing != 0 {
				t.Fatalf("icon progress %+v", p)
			}
		case "gfs":
			if got, ok := r.Value(0, 1, Precip); !ok || r.PrecipFrom[1] != 3 || got <= 0 {
				t.Fatalf("gfs precip %v %v from %d", got, ok, r.PrecipFrom[1])
			}
			if r.Cells[0].Orog != float32(want) {
				t.Fatalf("gfs ground %v", r.Cells[0].Orog)
			}
			if p.Fields != 3+2+3 || p.Missing != 0 {
				t.Fatalf("gfs progress %+v", p)
			}
		}
	}
	// the forecast at the fix from the windows, and from the cells for the list
	now := run.Add(5 * time.Hour)
	here, ok := ComputeAt(pulled, weather.Place{ID: weather.HereID, Name: "Somewhere", Lat: 46.1, Lon: -5.3}, now, nil, nil)
	if !ok || !here.Here || here.Source != "ifs 2026-10-10 06Z · icon-eu 2026-10-10 06Z · icon-d2 2026-10-10 06Z · gfs 2026-10-10 06Z" || len(here.Hours) == 0 {
		t.Fatalf("here %v %+v", ok, here.Source)
	}
	if _, ok := ComputeAt(pulled, weather.Place{ID: weather.HereID, Lat: 55, Lon: 2}, now, nil, nil); ok {
		t.Fatal("a point outside every window answered")
	}
	all := Compute(pulled, []weather.Place{{ID: 1, Lat: 46.1, Lon: -5.3}}, now, nil, nil)
	if len(all) != 1 || all[0].Here {
		t.Fatalf("%+v", all)
	}
	// the same point read through the windows and through its cell: the same numbers
	for i := range all[0].Hours {
		if all[0].Hours[i].TempC != here.Hours[i].TempC {
			t.Fatalf("hour %d: cell %v, window %v", i, all[0].Hours[i].TempC, here.Hours[i].TempC)
		}
	}
	// a run that is not there: every field missing, nothing enough
	m, _ := ModelByID("gfs")
	m.Steps = steps
	r, p, _ := f.Pull(ctx, m, run.Add(6*time.Hour), cells, nil)
	if Enough(r, m) || p.Missing == 0 || p.LastErr == "" {
		t.Fatalf("%+v", p)
	}
	// the bz2 fixture really is bz2
	if _, err := io.ReadAll(bzip2.NewReader(bytes.NewReader(iconBz))); err != nil {
		t.Fatal(err)
	}
}

func TestGFSIndexLengths(t *testing.T) {
	e := gfsIndex([]byte("1:0:d=2026101006:PRMSL:mean sea level:anl:\n2:1000:d=2026101006:TMP:2 m above ground:anl:\n3:2500:d=2026101006:APCP:surface:0-3 hour acc fcst:\n"))
	if len(e) != 3 || e[0].key != "PRMSL:mean sea level" || e[0].length != 1000 || e[1].offset != 1000 || e[1].length != 1500 || e[2].length != 0 {
		t.Fatalf("%+v", e)
	}
}
