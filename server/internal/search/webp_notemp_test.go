package search

import (
	"bytes"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"testing"
)

// The pHash's WebP decode goes through dwebp on a pipe: nothing lands in a temporary directory.
func TestDecodeWebPNeverTouchesTemp(t *testing.T) {
	dir := t.TempDir()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, 8, 6))); err != nil {
		t.Fatal(err)
	}
	pngPath := filepath.Join(dir, "out.png")
	if err := os.WriteFile(pngPath, buf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	script := "#!/bin/sh\n[ \"$3\" = \"-o\" ] && [ \"$4\" = \"-\" ] || { echo \"want -o -, got $*\" >&2; exit 2; }\ncat '" + pngPath + "'\n"
	if err := os.WriteFile(filepath.Join(bin, "dwebp"), []byte(script), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	tmp := filepath.Join(dir, "tmp")
	if err := os.MkdirAll(tmp, 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("TMPDIR", tmp)
	// the file dwebp is given: a WebP header claiming 8 x 6 (its size is read before dwebp runs)
	src := filepath.Join(dir, "a.webp")
	if err := os.WriteFile(src, []byte("RIFF\x16\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x00\x00\x00\x00\x07\x00\x00\x05\x00\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	img, err := decodeWebP(src)
	if err != nil {
		t.Fatalf("decodeWebP: %v", err)
	}
	if img.Bounds().Dx() != 8 || img.Bounds().Dy() != 6 {
		t.Fatalf("size %v", img.Bounds())
	}
	if left, _ := os.ReadDir(tmp); len(left) != 0 {
		t.Fatalf("temporary directory not empty: %v", left)
	}
}

// A WebP claiming more pixels than the box decodes is never handed to dwebp (second audit).
func TestDecodeWebPRefusesAHugeCanvas(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "bin")
	if err := os.MkdirAll(bin, 0o700); err != nil {
		t.Fatal(err)
	}
	ran := filepath.Join(dir, "ran")
	if err := os.WriteFile(filepath.Join(bin, "dwebp"), []byte("#!/bin/sh\ntouch '"+ran+"'\n"), 0o700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	src := filepath.Join(dir, "bomb.webp")
	// VP8X, canvas 16383 x 16383: 268 MP in thirty bytes
	if err := os.WriteFile(src, []byte("RIFF\x16\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x00\x00\x00\x00\xfe\x3f\x00\xfe\x3f\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := decodeWebP(src); err == nil {
		t.Fatal("a 268 MP WebP was handed to dwebp")
	}
	if _, err := os.Stat(ran); err == nil {
		t.Fatal("dwebp was run on it")
	}
}
