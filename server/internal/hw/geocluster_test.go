package hw

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
)

// TestGeoClusterJSONCarriesCoordinates pins the wire contract the map depends on. The regression
// it guards: Lat and Lon declared on one line under one json:"lat" tag gave two fields one name,
// and encoding/json drops every field that shares a name at the same level , silently. The map
// then received cells with no coordinates and drew nothing while counting everything.
func TestGeoClusterJSONCarriesCoordinates(t *testing.T) {
	b, err := json.Marshal(GeoCluster{Lat: 51.5074, Lon: -0.1278, N: 3, Hash: "abc", TakenAt: 1700000000})
	if err != nil {
		t.Fatal(err)
	}
	var m map[string]any
	if err := json.Unmarshal(b, &m); err != nil {
		t.Fatal(err)
	}
	for _, k := range []string{"lat", "lon", "n"} {
		if _, ok := m[k]; !ok {
			t.Fatalf("GeoCluster JSON lacks %q: %s", k, b)
		}
	}
	if m["lat"].(float64) != 51.5074 || m["lon"].(float64) != -0.1278 {
		t.Fatalf("coordinates mangled: %s", b)
	}
	// The equator and the prime meridian are real places , 0.0 must survive the encoder too.
	b, _ = json.Marshal(GeoCluster{N: 1})
	if !strings.Contains(string(b), `"lat":0`) || !strings.Contains(string(b), `"lon":0`) {
		t.Fatalf("zero coordinates dropped (omitempty on a coordinate): %s", b)
	}
}

// TestAPIStructsHaveUniqueJSONNames sweeps every JSON-bearing struct this package hands to secd and
// refuses duplicate names outright, so the same one-line mistake cannot land again on a different
// type. Add new wire structs here as they appear.
func TestAPIStructsHaveUniqueJSONNames(t *testing.T) {
	for _, v := range []any{GeoCluster{}, GeoPoint{}, FrameRow{}, DaemonKV{}} {
		rt := reflect.TypeOf(v)
		seen := map[string]string{}
		for i := 0; i < rt.NumField(); i++ {
			f := rt.Field(i)
			tag := f.Tag.Get("json")
			if tag == "" || tag == "-" {
				continue
			}
			name := strings.Split(tag, ",")[0]
			if name == "" {
				name = f.Name
			}
			if prev, dup := seen[name]; dup {
				t.Errorf("%s: fields %s and %s both encode as %q (encoding/json drops both)", rt.Name(), prev, f.Name, name)
			}
			seen[name] = f.Name
		}
	}
}
