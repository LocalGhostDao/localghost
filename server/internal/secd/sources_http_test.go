package secd

import (
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
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
	var f fetchState
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
	t.Setenv("HOME", dir)
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
