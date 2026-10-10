package secd

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/ctlsock"
)

func TestFeedID(t *testing.T) {
	for u, want := range map[string]string{
		"https://feeds.bbci.co.uk/news/rss.xml":                     "bbci",
		"https://www.theguardian.com/world/rss":                     "theguardian-world",
		"https://feeds.content.dowjones.io/public/rss/RSSWorldNews": "dowjones-public",
		"https://rss.dw.com/rdf/rss-en-all":                         "dw-rdf",
		"https://news.ycombinator.com/rss":                          "ycombinator",
		"https://www.politico.eu/feed/":                             "politico",
		"https://x.y.example.org/":                                  "example",
		"not a url":                                                 "",
	} {
		if got := feedID(u); got != want {
			t.Errorf("%s: %q, want %q", u, got, want)
		}
	}
}

// A fetch runs in the background, one at a time, and its last line and exit come back.
func TestFetchJob(t *testing.T) {
	dir := t.TempDir()
	f := fetchState{dir: dir}
	f.run = func(step, region, logPath string) (*exec.Cmd, error) {
		logf, err := os.Create(logPath)
		if err != nil {
			return nil, err
		}
		cmd := exec.Command("/bin/sh", "-c", "echo fetching "+step+" "+region+"; sleep 0.4; echo done "+step)
		cmd.Stdout, cmd.Stderr = logf, logf
		if err := cmd.Start(); err != nil {
			return nil, err
		}
		logf.Close()
		return cmd, nil
	}
	job, err := f.start("maps", "34:72,-25:45")
	if err != nil {
		t.Fatal(err)
	}
	if !job.Running || job.Step != "maps" {
		t.Fatalf("%+v", job)
	}
	if _, err := f.start("wiki", ""); err == nil || !strings.Contains(err.Error(), "already running") {
		t.Fatalf("a second fetch at once: %v", err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for f.snapshot().Running && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	j := f.snapshot()
	if j.Running || j.Exit != 0 || j.Last != "done maps" || j.EndedAt == 0 {
		t.Fatalf("%+v", j)
	}
	if _, err := f.start("wiki", ""); err != nil {
		t.Fatalf("after the first ended: %v", err)
	}
}

// A ctl answer in Data is the body; one in Text is the body; the wiki command's Data used to
// be dropped on the floor.
func TestCtlBody(t *testing.T) {
	if got := string(ctlBody(ctlsock.Response{OK: true, Data: []byte(`{"state":"ready"}`)})); got != `{"state":"ready"}` {
		t.Fatalf("data: %q", got)
	}
	if got := string(ctlBody(ctlsock.Response{OK: true, Text: `{"years":[]}`})); got != `{"years":[]}` {
		t.Fatalf("text: %q", got)
	}
	if got := string(ctlBody(ctlsock.Response{OK: true})); got != "" {
		t.Fatalf("nothing: %q", got)
	}
}

// The maps card counts the tiles where the map is served from (<mount>/landtiles,
// <mount>/roadtiles/{0,1}), not a geo/ folder that never held them: a box with the maps on it
// says so.
func TestMapsDocReadsTheTilesWhereTheyAre(t *testing.T) {
	mount := t.TempDir()
	if d := mapsDoc(mount); d.State != "missing" {
		t.Fatalf("empty volume: state %q", d.State)
	}
	for _, p := range []string{"landtiles/010_020.lgt", "landtiles/011_020.lgt", "roadtiles/0/0100_0200.lgr", "roadtiles/1/010_020.lgr",
		"geo/elevation/GLO-90_N30_E000.heights", "geo/tz/grid.bin"} {
		full := filepath.Join(mount, p)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	d := mapsDoc(mount)
	if d.State != "ready" {
		t.Fatalf("the maps on the box: state %q, line %q", d.State, d.Line)
	}
	if !strings.Contains(d.Line, "coastline 2 tiles") || !strings.Contains(d.Line, "roads 2 tiles") || !strings.Contains(d.Line, "heights 1 packs") {
		t.Fatalf("line %q", d.Line)
	}
	if err := os.Remove(filepath.Join(mount, "geo/tz/grid.bin")); err != nil {
		t.Fatal(err)
	}
	if d := mapsDoc(mount); d.State != "partial" || !strings.Contains(d.Detail, "the time zones") {
		t.Fatalf("without the zones: state %q detail %q", d.State, d.Detail)
	}
}
