package framed

import (
	"bytes"
	"image"
	"image/color"
	"image/jpeg"
	"io"
	"log/slog"
	"os/exec"
	"testing"
)

// A JPEG with a damaged scan is refused by Go's decoder whole; ffmpeg's decoder is tolerant, and
// makePreviews goes through it once before giving up.
func TestDamagedJPEGPreviewGoesThroughFFmpeg(t *testing.T) {
	if _, err := exec.LookPath("ffmpeg"); err != nil {
		t.Skip("no ffmpeg here")
	}
	img := image.NewRGBA(image.Rect(0, 0, 64, 48))
	for y := 0; y < 48; y++ {
		for x := 0; x < 64; x++ {
			img.Set(x, y, color.RGBA{uint8(x * 4), uint8(y * 5), 90, 255})
		}
	}
	var buf bytes.Buffer
	jpeg.Encode(&buf, img, &jpeg.Options{Quality: 90})
	raw := buf.Bytes()
	// damage the entropy-coded data: a stray 0xFF without a following 0x00 is exactly "missing
	// 0xff00 sequence"
	damaged := append([]byte{}, raw...)
	damaged[len(damaged)-20] = 0xFF
	damaged[len(damaged)-19] = 0x37
	if _, _, err := image.Decode(bytes.NewReader(damaged)); err == nil {
		t.Skip("this damage did not upset Go's decoder; nothing to test")
	}
	p := &Pipeline{log: slog.New(slog.NewTextHandler(io.Discard, nil))}
	fixed, err := p.redecode(damaged)
	if err != nil {
		t.Fatalf("ffmpeg could not re-encode: %v", err)
	}
	got, _, err := image.Decode(bytes.NewReader(fixed))
	if err != nil {
		t.Fatalf("re-encoded JPEG undecodable: %v", err)
	}
	if got.Bounds().Dx() != 64 || got.Bounds().Dy() != 48 {
		t.Fatalf("size %v", got.Bounds())
	}
}
