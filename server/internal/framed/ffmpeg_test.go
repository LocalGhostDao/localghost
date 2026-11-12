package framed

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// One ffmpeg that never finishes does not hold the pipeline (second audit, 12 Nov 2026): the
// two calls had no deadline, and every photo behind a clip like that waited with it.
func TestFFmpegIsHeldToADeadline(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "ffmpeg")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 60\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	old := ffmpegTimeout
	ffmpegTimeout = 300 * time.Millisecond
	defer func() { ffmpegTimeout = old }()
	p := &Pipeline{}
	p.SetFFmpeg(fake, dir)
	t0 := time.Now()
	if _, err := p.grabVideoFrame(filepath.Join(dir, "clip.mp4")); err == nil || !strings.Contains(err.Error(), "ran past") {
		t.Fatalf("a clip ffmpeg never finished with: %v", err)
	}
	if _, err := p.redecode(filepath.Join(dir, "photo.jpg")); err == nil || !strings.Contains(err.Error(), "ran past") {
		t.Fatalf("a picture ffmpeg never finished with: %v", err)
	}
	if d := time.Since(t0); d > 8*time.Second {
		t.Fatalf("three stuck runs held the caller %s", d)
	}
}

// What ffmpeg is told: local files only, the pixel and allocation limits, and the file last.
func TestFFmpegArguments(t *testing.T) {
	dir := t.TempDir()
	fake := filepath.Join(dir, "ffmpeg")
	seen := filepath.Join(dir, "args")
	// a stand-in that records its arguments and answers with two bytes
	if err := os.WriteFile(fake, []byte("#!/bin/sh\necho \"$@\" >> "+seen+"\nprintf 'ok'\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	p := &Pipeline{}
	p.SetFFmpeg(fake, dir)
	if out, err := p.grabVideoFrame("/vol/archive/a.mp4"); err != nil || string(out) != "ok" {
		t.Fatalf("%q %v", out, err)
	}
	if out, err := p.redecode("/vol/archive/b.jpg"); err != nil || string(out) != "ok" {
		t.Fatalf("%q %v", out, err)
	}
	b, _ := os.ReadFile(seen)
	lines := strings.Split(strings.TrimSpace(string(b)), "\n")
	if len(lines) != 2 {
		t.Fatalf("ffmpeg was run %d times: %s", len(lines), b)
	}
	for _, l := range lines {
		for _, want := range []string{"-protocol_whitelist file", "-max_pixels 60000000", "-max_alloc 1073741824", "-frames:v 1", "pipe:1"} {
			if !strings.Contains(l, want) {
				t.Fatalf("%q is not in: %s", want, l)
			}
		}
		if i, j := strings.Index(l, "-protocol_whitelist"), strings.Index(l, " -i "); i < 0 || j < 0 || i > j {
			t.Fatalf("the rules must come before the file: %s", l)
		}
	}
	if !strings.Contains(lines[0], "-ss 1 ") || strings.Index(lines[0], "-ss 1") > strings.Index(lines[0], " -i ") {
		t.Fatalf("the seek must come before the file: %s", lines[0])
	}
}

// And with the real ffmpeg, where there is one: a clip still gives its frame.
func TestGrabVideoFrameWithARealFFmpeg(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg on this machine")
	}
	dir := t.TempDir()
	clip := filepath.Join(dir, "clip.mp4")
	if out, err := exec.Command(ff, "-hide_banner", "-loglevel", "error", "-y", "-f", "lavfi", "-i", "testsrc=d=2:s=64x64:r=10", "-pix_fmt", "yuv420p", clip).CombinedOutput(); err != nil {
		t.Skipf("this ffmpeg cannot make the test clip: %v %s", err, out)
	}
	p := &Pipeline{}
	jpg, err := p.grabVideoFrame(clip)
	if err != nil || len(jpg) < 100 || jpg[0] != 0xff || jpg[1] != 0xd8 {
		t.Fatalf("no frame from an ordinary clip: %v (%d bytes)", err, len(jpg))
	}
	still := filepath.Join(dir, "still.jpg")
	if err := os.WriteFile(still, jpg, 0o600); err != nil {
		t.Fatal(err)
	}
	if again, err := p.redecode(still); err != nil || len(again) < 100 {
		t.Fatalf("no picture back from an ordinary JPEG: %v", err)
	}
}
