package oracled

import (
	"bytes"
	"context"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fakeDwebp puts a dwebp on PATH that insists on "-o -" and writes a small PNG to stdout, and
// points TMPDIR at an empty directory the test then checks.
func fakeDwebp(t *testing.T) (tmp string) {
	t.Helper()
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 6))); err != nil {
		t.Fatal(err)
	}
	pngPath := filepath.Join(dir, "out.png")
	if err := os.WriteFile(pngPath, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n[ \"$3\" = \"-o\" ] && [ \"$4\" = \"-\" ] || { echo \"want -o -, got $*\" >&2; exit 2; }\ncat '" + pngPath + "'\n"
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bin, "dwebp"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	tmp = filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmp)
	return tmp
}

// A WebP is decoded on a pipe: nothing of the picture lands in a temporary directory.
func TestWebPNeverTouchesTemp(t *testing.T) {
	tmp := fakeDwebp(t)
	src := filepath.Join(t.TempDir(), "a.webp")
	if err := os.WriteFile(src, []byte("RIFF\x16\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x00\x00\x00\x00\x07\x00\x00\x05\x00\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	out, err := dwebpPNG(context.Background(), src, "webp")
	if err != nil {
		t.Fatalf("dwebpPNG: %v", err)
	}
	if cfg, err := png.DecodeConfig(bytes.NewReader(out)); err != nil || cfg.Width != 8 {
		t.Fatalf("not the PNG dwebp wrote: %v %+v", err, cfg)
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Fatalf("temporary directory not empty: %v", left)
	}
}

// A WebP claiming more pixels than the box decodes is handed to no decoder (second audit): Go
// reads no WebP, so until 12 Nov 2026 nothing looked at its size before dwebp or ffmpeg did.
func TestAHugeWebPIsNotDecoded(t *testing.T) {
	fakeDwebp(t)
	src := filepath.Join(t.TempDir(), "bomb.webp")
	// VP8X, canvas 16383 x 16383: 268 MP in thirty bytes
	if err := os.WriteFile(src, []byte("RIFF\x16\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x00\x00\x00\x00\xfe\x3f\x00\xfe\x3f\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	if out, err := dwebpPNG(context.Background(), src, "webp"); err == nil || !strings.Contains(err.Error(), "too large") {
		t.Fatalf("dwebp was run on a 268 MP WebP: %d bytes, %v", len(out), err)
	}
	if out, err := ffmpegJPEG(context.Background(), src, "webp"); err == nil || (!strings.Contains(err.Error(), "too large") && !strings.Contains(err.Error(), "executable file not found")) {
		t.Fatalf("ffmpeg was run on a 268 MP WebP: %d bytes, %v", len(out), err)
	}
}
