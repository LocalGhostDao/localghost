package imgfit

import (
	"bytes"
	"errors"
	"image"
	"image/color"
	"image/png"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func webpHead(kind string, w, h int) []byte {
	b := make([]byte, 30)
	copy(b[0:], "RIFF")
	copy(b[8:], "WEBP")
	copy(b[12:], kind)
	switch kind {
	case "VP8 ":
		b[23], b[24], b[25] = 0x9d, 0x01, 0x2a
		b[26], b[27] = byte(w), byte(w>>8)
		b[28], b[29] = byte(h), byte(h>>8)
	case "VP8L":
		b[20] = 0x2f
		v := uint32(w-1) | uint32(h-1)<<14
		b[21], b[22], b[23], b[24] = byte(v), byte(v>>8), byte(v>>16), byte(v>>24)
	case "VP8X":
		b[24], b[25], b[26] = byte(w-1), byte((w-1)>>8), byte((w-1)>>16)
		b[27], b[28], b[29] = byte(h-1), byte((h-1)>>8), byte((h-1)>>16)
	}
	return b
}

// A WebP's size is read from its header before any decoder is run on it: Go reads no WebP, so
// the check every other picture gets never saw one (second audit, 12 Nov 2026).
func TestWebPSizeAndLimit(t *testing.T) {
	for _, kind := range []string{"VP8 ", "VP8L", "VP8X"} {
		for _, d := range [][2]int{{1, 1}, {4000, 3000}, {16383, 16383}} {
			w, h, ok := WebPSize(webpHead(kind, d[0], d[1]))
			if !ok || w != d[0] || h != d[1] {
				t.Fatalf("%s %dx%d read as %dx%d %v", kind, d[0], d[1], w, h, ok)
			}
		}
	}
	if w, h, ok := WebPSize(webpHead("VP8X", 1<<24, 1<<24)); !ok || w != 1<<24 || h != 1<<24 {
		t.Fatalf("the largest canvas: %d %d %v", w, h, ok)
	}
	for _, bad := range [][]byte{nil, []byte("RIFF"), webpHead("ALPH", 1, 1), bytes.Repeat([]byte{0}, 30), append([]byte("RIFF\x00\x00\x00\x00WAVEfmt "), make([]byte, 14)...)} {
		if _, _, ok := WebPSize(bad); ok {
			t.Fatalf("%q taken for a WebP", bad)
		}
	}
	dir := t.TempDir()
	put := func(name string, b []byte) string {
		p := filepath.Join(dir, name)
		if err := os.WriteFile(p, b, 0o600); err != nil {
			t.Fatal(err)
		}
		return p
	}
	if err := CheckWebPFile(put("phone.webp", webpHead("VP8 ", 4000, 3000))); err != nil {
		t.Fatalf("a phone-sized WebP refused: %v", err)
	}
	var tl ErrTooLarge
	for _, kind := range []string{"VP8 ", "VP8L", "VP8X"} {
		if err := CheckWebPFile(put("bomb.webp", webpHead(kind, 16383, 16383))); !errors.As(err, &tl) {
			t.Fatalf("a %s of 268 MP: %v", kind, err)
		}
	}
	if err := CheckWebPFile(put("short.webp", []byte("RIFF"))); err == nil {
		t.Fatal("a four-byte file passed")
	}
	if err := CheckWebPFile(put("not.webp", bytes.Repeat([]byte("x"), 64))); err == nil {
		t.Fatal("a file that is not a WebP passed")
	}
}

func TestPipeStopsAtItsLimit(t *testing.T) {
	p := &Pipe{Max: 10}
	if n, err := p.Write([]byte("12345678")); n != 8 || err != nil {
		t.Fatal(n, err)
	}
	if _, err := p.Write([]byte("123")); !errors.Is(err, ErrPipeFull) {
		t.Fatalf("the eleventh byte: %v", err)
	}
	if p.Len() != 8 {
		t.Fatalf("kept %d bytes", p.Len())
	}
	// a writer and nothing else: io.Copy must not find a ReadFrom to go round the limit by
	if _, ok := interface{}(p).(io.ReaderFrom); ok {
		t.Fatal("Pipe has a ReadFrom: a command's output would be read into it without limit")
	}
	// and the program writing into it is ended, not waited on
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("no sh")
	}
	cmd := exec.Command("sh", "-c", "exec yes aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	out := &Pipe{Max: 1 << 20}
	cmd.Stdout = out
	cmd.WaitDelay = 2 * time.Second
	t0 := time.Now()
	if err := cmd.Run(); err == nil {
		t.Fatal("a program that never stops writing ended well")
	}
	if time.Since(t0) > 10*time.Second || out.Len() > 1<<20 {
		t.Fatalf("after %s, %d bytes", time.Since(t0), out.Len())
	}
}

// With the real ffmpeg, where there is one: an ordinary picture still comes through, one past
// the pixel limit does not, and a file that names another address is not followed there.
func TestFFmpegInputWithARealFFmpeg(t *testing.T) {
	ff, err := exec.LookPath("ffmpeg")
	if err != nil {
		t.Skip("no ffmpeg on this machine")
	}
	dir := t.TempDir()
	solid := func(name string, w, h int) string {
		img := image.NewGray(image.Rect(0, 0, w, h))
		img.SetGray(0, 0, color.Gray{Y: 200})
		p := filepath.Join(dir, name)
		f, err := os.Create(p)
		if err != nil {
			t.Fatal(err)
		}
		if err := png.Encode(f, img); err != nil {
			t.Fatal(err)
		}
		f.Close()
		return p
	}
	frame := func(args []string) ([]byte, string, error) {
		var errb bytes.Buffer
		out := NewPipe()
		cmd := exec.Command(ff, append(append([]string{"-hide_banner", "-loglevel", "error"}, args...), "-frames:v", "1", "-f", "image2", "-c:v", "mjpeg", "pipe:1")...)
		cmd.Stdout, cmd.Stderr = out, &errb
		cmd.WaitDelay = 5 * time.Second
		err := cmd.Run()
		return out.Bytes(), errb.String(), err
	}
	small := solid("small.png", 640, 480)
	if out, said, err := frame(FFmpegInput(small)); err != nil || len(out) < 100 || out[0] != 0xff || out[1] != 0xd8 {
		t.Fatalf("an ordinary picture: %v %q (%d bytes)", err, said, len(out))
	}
	// 9000 x 9000 = 81 MP, past MaxPixels; the file itself is a few kilobytes
	big := solid("big.png", 9000, 9000)
	if fi, _ := os.Stat(big); fi.Size() > 4<<20 {
		t.Fatalf("the test's own file is %d bytes", fi.Size())
	}
	if out, _, err := frame(FFmpegInput(big)); err == nil && len(out) > 0 {
		t.Fatalf("a picture of 81 MP was decoded (%d bytes back)", len(out))
	}
	// a playlist that names an address on this machine: followed without the rule (where this
	// ffmpeg speaks http at all), never with it
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	defer ln.Close()
	hits := make(chan struct{}, 8)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			hits <- struct{}{}
			c.Close()
		}
	}()
	list := filepath.Join(dir, "clip.m3u8")
	os.WriteFile(list, []byte("#EXTM3U\n#EXT-X-TARGETDURATION:1\n#EXTINF:1,\nhttp://"+ln.Addr().String()+"/seg.ts\n#EXT-X-ENDLIST\n"), 0o600)
	called := func() bool {
		select {
		case <-hits:
			return true
		case <-time.After(1500 * time.Millisecond):
			return false
		}
	}
	_, said, _ := frame(FFmpegInput(list))
	if called() {
		t.Fatalf("with the rule, ffmpeg still went to the address the file named: %s", said)
	}
	if !strings.Contains(strings.ToLower(said), "whitelist") && !strings.Contains(strings.ToLower(said), "not on") {
		t.Logf("ffmpeg's refusal, for the record: %q", said)
	}
	frame([]string{"-i", list})
	if !called() {
		// ffmpeg since 2017 gives a playlist opened from a file no protocols but file, crypto and
		// data by itself; the rule then says twice what this build already does
		t.Log("this ffmpeg does not follow a local playlist to the network even without the rule: the rule is the second lock here, not the first")
	}
}
