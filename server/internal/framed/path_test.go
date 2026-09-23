package framed

import (
	"encoding/json"
	"image"
	"image/color"
	"testing"
	"time"
)

// TestDouglasPeuckerCollapsesStraightLine: colinear jitter-free points collapse to endpoints.
func TestDouglasPeuckerCollapsesStraightLine(t *testing.T) {
	var pts []TrackPoint
	for i := 0; i < 50; i++ {
		pts = append(pts, TrackPoint{TS: int64(i), Lat: 51.5, Lon: 0.0 + float64(i)*0.001})
	}
	out := douglasPeucker(pts, simplifyEpsilonDeg)
	if len(out) != 2 {
		t.Fatalf("straight line should collapse to 2 points, got %d", len(out))
	}
}

// TestDouglasPeuckerKeepsCorner: a right-angle turn survives simplification.
func TestDouglasPeuckerKeepsCorner(t *testing.T) {
	pts := []TrackPoint{
		{TS: 0, Lat: 51.50, Lon: 0.00},
		{TS: 1, Lat: 51.50, Lon: 0.01}, // the corner, ~700m off the direct chord
		{TS: 2, Lat: 51.51, Lon: 0.01},
	}
	out := douglasPeucker(pts, simplifyEpsilonDeg)
	if len(out) != 3 {
		t.Fatalf("corner must survive, got %d points", len(out))
	}
}

// TestBuildDayPath: the GeoJSON has one LineString for the track and one Point per photo.
func TestBuildDayPath(t *testing.T) {
	day, _ := time.Parse("2006-01-02", "2024-06-01")
	d0 := day.Unix() // the points must lie in the day: BuildDayPath keeps the day's own and no more
	pts := []TrackPoint{
		{TS: d0 + 600, Lat: 51.50, Lon: 0.00},
		{TS: d0 + 1200, Lat: 51.50, Lon: 0.01},
		{TS: d0 + 1800, Lat: 51.51, Lon: 0.01},
	}
	photos := []PhotoPoint{{Hash: "abc", TakenAt: d0 + 900, Lat: 51.505, Lon: 0.005}}
	doc, err := BuildDayPath(day, pts, photos)
	if err != nil {
		t.Fatal(err)
	}
	var g struct {
		Type     string `json:"type"`
		Features []struct {
			Geometry struct {
				Type string `json:"type"`
			} `json:"geometry"`
			Properties map[string]any `json:"properties"`
		} `json:"features"`
	}
	if err := json.Unmarshal(doc, &g); err != nil {
		t.Fatal(err)
	}
	if g.Type != "FeatureCollection" || len(g.Features) != 2 {
		t.Fatalf("want FeatureCollection with 2 features, got %s with %d", g.Type, len(g.Features))
	}
	if g.Features[0].Geometry.Type != "LineString" || g.Features[1].Geometry.Type != "Point" {
		t.Fatalf("want LineString then Point, got %s then %s",
			g.Features[0].Geometry.Type, g.Features[1].Geometry.Type)
	}
	if g.Features[1].Properties["hash"] != "abc" {
		t.Fatal("photo feature must carry the frame hash for the app to open the preview")
	}
	// The track carries a clock per kept vertex and its length over the raw points: a right angle
	// of ~695m east then ~1112m north, none of it under the standing-still floor.
	times, _ := g.Features[0].Properties["times"].([]any)
	if len(times) != 3 || int64(times[0].(float64)) != d0+600 || int64(times[2].(float64)) != d0+1800 {
		t.Fatalf("times = %v, want the three clocks parallel to the coordinates", times)
	}
	if g.Features[0].Properties["glitches"].(float64) != 0 {
		t.Fatal("a clean right angle has no glitches")
	}
	if d, _ := g.Features[0].Properties["distanceM"].(float64); d < 1780 || d > 1830 {
		t.Fatalf("distanceM = %v, want ~1806", d)
	}
}

func TestTrackDistanceIgnoresJitter(t *testing.T) {
	still := []TrackPoint{{TS: 1, Lat: 51.5, Lon: 0}, {TS: 2, Lat: 51.50005, Lon: 0}, {TS: 3, Lat: 51.5, Lon: 0.00005}}
	if d := TrackDistanceM(still); d != 0 {
		t.Fatalf("a café afternoon walked %.1fm", d)
	}
	if d := TrackDistanceM([]TrackPoint{{TS: 1, Lat: 0, Lon: 0}, {TS: 2, Lat: 0, Lon: 1}}); d < 111000 || d > 111400 {
		t.Fatalf("one degree at the equator = %.0fm, want ~111195", d)
	}
	if TrackDistanceM(nil) != 0 || TrackDistanceM(still[:1]) != 0 {
		t.Fatal("no track, no distance")
	}
}

// TestDownscaleDimensions: 3200x1600 at maxEdge 1600 becomes 1600x800; an already-small image is
// returned unchanged.
func TestDownscaleDimensions(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 3200, 1600))
	out := downscale(src, 1600)
	if b := out.Bounds(); b.Dx() != 1600 || b.Dy() != 800 {
		t.Fatalf("got %dx%d, want 1600x800", b.Dx(), b.Dy())
	}
	small := image.NewRGBA(image.Rect(0, 0, 100, 50))
	if downscale(small, 1600) != image.Image(small) {
		t.Fatal("small image must be returned unchanged")
	}
}

// TestDownscaleAverages: a half-black half-white source averages to grey at 1px.
func TestDownscaleAverages(t *testing.T) {
	src := image.NewRGBA(image.Rect(0, 0, 2, 1))
	src.Set(0, 0, color.RGBA{0, 0, 0, 255})
	src.Set(1, 0, color.RGBA{255, 255, 255, 255})
	out := downscale(src, 1).(*image.RGBA)
	r := out.Pix[0]
	if r < 120 || r > 135 {
		t.Fatalf("expected mid-grey, got %d", r)
	}
}

func TestExtFor(t *testing.T) {
	if extFor("jpeg", "x") != ".jpg" || extFor("png", "x") != ".png" {
		t.Fatal("known formats map to canonical extensions")
	}
	if extFor("", "photo.HEIC") != ".heic" {
		t.Fatal("unknown format falls back to the original extension, lowered")
	}
	if extFor("", "noext") != ".bin" {
		t.Fatal("no extension falls back to .bin")
	}
}
