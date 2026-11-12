package oracled

import (
	"bytes"
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"image"
	"image/jpeg"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/LocalGhostDao/localghost/server/internal/oracle"
)

var tinyPNG, _ = base64.StdEncoding.DecodeString("iVBORw0KGgoAAAANSUhEUgAAABAAAAAQCAIAAACQkWg2AAAAFklEQVR4nGO4YGBAEmIY1TCqYfhqAAA+XjAQexaBIwAAAABJRU5ErkJggg==")

func TestImageKinds(t *testing.T) {
	for want, raw := range map[string][]byte{
		"jpeg":    {0xFF, 0xD8, 0xFF, 0xE0, 0, 0},
		"png":     tinyPNG,
		"webp":    []byte("RIFF\x16\x00\x00\x00WEBPVP8X\x0a\x00\x00\x00\x00\x00\x00\x00\x07\x00\x00\x05\x00\x00"),
		"heic":    []byte("\x00\x00\x00\x18ftypheic\x00\x00"),
		"gif":     []byte("GIF89a...."),
		"unknown": []byte("hello world!"),
	} {
		if got := imageKind(raw); got != want {
			t.Errorf("%s: got %s", want, got)
		}
	}
}

func TestImageForModelConvertsWhatTheModelCannotRead(t *testing.T) {
	dir := t.TempDir()
	jpg := filepath.Join(dir, "a.jpg")
	os.WriteFile(jpg, testJPEG(t, 40, 30, 0), 0o644)
	broken := filepath.Join(dir, "broken.jpg")
	os.WriteFile(broken, []byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3}, 0o644)
	webp := filepath.Join(dir, "a.webp")
	os.WriteFile(webp, []byte("RIFF\x00\x00\x00\x00WEBPVP8 data"), 0o644)
	heic := filepath.Join(dir, "a.heic")
	os.WriteFile(heic, []byte("\x00\x00\x00\x18ftypheic\x00\x00"), 0o644)

	old := converters
	defer func() { converters = old }()
	calls := 0
	converters = []func(context.Context, string, string) ([]byte, error){
		func(_ context.Context, p, kind string) ([]byte, error) {
			calls++
			if kind == "webp" {
				return tinyPNG, nil
			}
			return nil, errors.New("no decoder for " + kind)
		},
	}
	if uri, err := imageForModel(context.Background(), jpg); err != nil || !strings.HasPrefix(uri, "data:image/jpeg;base64,") || calls != 0 {
		t.Fatalf("jpeg decoded here: %v %.30s calls=%d", err, uri, calls)
	}
	if uri, err := imageForModel(context.Background(), webp); err != nil || !strings.HasPrefix(uri, "data:image/jpeg;base64,") {
		t.Fatalf("webp converted, then fitted: %v %.30s", err, uri)
	}
	if _, err := imageForModel(context.Background(), heic); err == nil || !strings.Contains(err.Error(), "heic") {
		t.Fatalf("heic: %v", err)
	}
	// a JPEG nothing can decode is never sent whole
	// (the converters here refuse rather than being missing: it reads as damaged, so searchd
	// finishes the job without a caption instead of retrying it)
	if _, err := imageForModel(context.Background(), broken); err == nil || !strings.Contains(err.Error(), "not sent whole") || !strings.Contains(err.Error(), "the file looks damaged") {
		t.Fatalf("broken jpeg: %v", err)
	}
	if _, err := imageForModel(context.Background(), filepath.Join(dir, "gone.jpg")); err == nil || !strings.Contains(err.Error(), "read image") {
		t.Fatalf("missing file: %v", err)
	}
}

// A full-size photo reaches the model at ImageMaxSide on its long side, turned upright.
func TestImageForModelFitsAndTurns(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "big.jpg")
	os.WriteFile(p, testJPEG(t, 3000, 2000, 6), 0o644) // landscape pixels, "rotate 90 to show"
	uri, err := imageForModel(context.Background(), p)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/jpeg;base64,"))
	cfg, err := jpeg.DecodeConfig(bytes.NewReader(raw))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Width != 682 || cfg.Height != 1024 {
		t.Fatalf("sent %dx%d, want 682x1024 (fitted, then turned upright)", cfg.Width, cfg.Height)
	}
	old := ImageMaxSide
	ImageMaxSide = 512
	defer func() { ImageMaxSide = old }()
	uri, _ = imageForModel(context.Background(), p)
	raw, _ = base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/jpeg;base64,"))
	if cfg, _ = jpeg.DecodeConfig(bytes.NewReader(raw)); cfg.Height != 512 {
		t.Fatalf("conf size not used: %dx%d", cfg.Width, cfg.Height)
	}
}

// testJPEG is a w x h JPEG, with an EXIF Orientation tag when orient > 0.
func testJPEG(t *testing.T, w, h, orient int) []byte {
	t.Helper()
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for i := range img.Pix {
		img.Pix[i] = byte(i * 7)
	}
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	if orient == 0 {
		return b
	}
	// APP1 "Exif\0\0" + little-endian TIFF: IFD0 with one entry, 0x0112 SHORT 1 = orient
	tiff := []byte{'I', 'I', 42, 0, 8, 0, 0, 0, 1, 0, 0x12, 0x01, 3, 0, 1, 0, 0, 0, byte(orient), 0, 0, 0, 0, 0, 0, 0}
	seg := append([]byte("Exif\x00\x00"), tiff...)
	app1 := []byte{0xFF, 0xE1, byte((len(seg) + 2) >> 8), byte(len(seg) + 2)}
	out := append([]byte{0xFF, 0xD8}, app1...)
	out = append(out, seg...)
	return append(out, b[2:]...)
}

// fakeLlama answers every chat completion with the given status and body.
func fakeLlama(t *testing.T, status int, body string) *llamaBackend {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(status)
		w.Write([]byte(body))
	}))
	t.Cleanup(srv.Close)
	return &llamaBackend{client: srv.Client(), streamClient: srv.Client(), addr: strings.TrimPrefix(srv.URL, "http://")}
}

func TestRefusalsCarryLlamaServersReason(t *testing.T) {
	img := filepath.Join(t.TempDir(), "p.png")
	os.WriteFile(img, tinyPNG, 0o644)
	req := oracle.Request{Capability: "caption", Input: "describe", Images: []string{img}, MaxTokens: 50}

	// a server started without its projector: "no vision", which searchd holds on instead of failing
	b := fakeLlama(t, 400, `{"error":{"code":400,"message":"image input is not supported - hint: if this is unexpected, you may need to provide the mmproj","type":"invalid_request_error"}}`)
	_, err := b.Infer(context.Background(), req)
	if err == nil || !strings.HasPrefix(err.Error(), "no vision:") || !strings.Contains(err.Error(), "image input is not supported") {
		t.Fatalf("no projector: %v", err)
	}
	// a per-request refusal: the reason is in the error, the job fails (and parks) with it
	b = fakeLlama(t, 400, `{"error":{"code":400,"message":"the request exceeds the available context size, try increasing it","type":"exceed_context_size_error"}}`)
	_, err = b.Infer(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "http 400: the request exceeds the available context size") || strings.Contains(err.Error(), "no vision") {
		t.Fatalf("context: %v", err)
	}
	// the text path checks the status too (it used to decode the error body as an empty answer)
	b = fakeLlama(t, 500, `upstream exploded`)
	_, err = b.Infer(context.Background(), oracle.Request{Input: "hi"})
	if err == nil || !strings.Contains(err.Error(), "http 500: upstream exploded") {
		t.Fatalf("text path: %v", err)
	}
	// and the stream
	b = fakeLlama(t, 400, `{"error":{"message":"bad things","type":"invalid_request_error"}}`)
	if _, _, err := b.StreamChat(context.Background(), nil, "hi", "", ""); err == nil || !strings.Contains(err.Error(), "bad things") {
		t.Fatalf("stream: %v", err)
	}
}

// An image the engine dies on twice is not sent a third time; a death with nothing in flight, or
// a plain refusal, counts nothing.
func TestStrikesStopAPoisonImage(t *testing.T) {
	dir := t.TempDir()
	img := filepath.Join(dir, "p.jpg")
	os.WriteFile(img, testJPEG(t, 64, 48, 0), 0o644)
	hits := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		hits++
		// the engine aborting mid-request: the connection just ends
		hj, _ := w.(http.Hijacker)
		c, _, _ := hj.Hijack()
		c.Close()
	}))
	defer srv.Close()
	dead := make(chan struct{})
	close(dead) // the reaper saw it exit
	strikesPath := filepath.Join(dir, "strikes")
	b := &llamaBackend{client: srv.Client(), streamClient: srv.Client(), addr: strings.TrimPrefix(srv.URL, "http://"),
		exited: dead, strikes: newImageStrikes(strikesPath), stats: NewEngineStats()}
	req := oracle.Request{Input: "caption", Images: []string{img}}
	for i := 1; i <= 2; i++ {
		_, err := b.Infer(context.Background(), req)
		if err == nil || !strings.Contains(err.Error(), fmt.Sprintf("strike %d", i)) {
			t.Fatalf("try %d: %v", i, err)
		}
	}
	_, err := b.Infer(context.Background(), req)
	if err == nil || !strings.Contains(err.Error(), "not sent again") || hits != 2 {
		t.Fatalf("third try: %v, hits %d", err, hits)
	}
	// the count survives a restart of oracled
	b.strikes = newImageStrikes(strikesPath)
	if _, err := b.Infer(context.Background(), req); err == nil || hits != 2 {
		t.Fatalf("after reload: %v, hits %d", err, hits)
	}
	// an engine that did not die: a failure is just a failure
	os.Remove(strikesPath)
	b.strikes = newImageStrikes(strikesPath)
	b.exited = make(chan struct{})
	if _, err := b.Infer(context.Background(), req); err == nil || strings.Contains(err.Error(), "strike") {
		t.Fatalf("no death, no strike: %v", err)
	}
	if _, err := os.Stat(strikesPath); err == nil {
		t.Fatal("a strike file for a live engine")
	}
}

// A chat attachment is prepared like a captioned image: decoded, fitted, re-encoded; what does
// not decode never reaches the engine.
func TestChatImageIsPreparedOrRefused(t *testing.T) {
	img := image.NewRGBA(image.Rect(0, 0, 3000, 2000))
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, img, &jpeg.Options{Quality: 80}); err != nil {
		t.Fatal(err)
	}
	uri, err := chatImage(base64.StdEncoding.EncodeToString(buf.Bytes()))
	if err != nil || !strings.HasPrefix(uri, "data:image/jpeg;base64,") {
		t.Fatalf("a JPEG: %v", err)
	}
	fitted, _ := base64.StdEncoding.DecodeString(strings.TrimPrefix(uri, "data:image/jpeg;base64,"))
	cfg, _, err := image.DecodeConfig(bytes.NewReader(fitted))
	if err != nil || cfg.Width > ImageMaxSide || cfg.Height > ImageMaxSide {
		t.Fatalf("not fitted: %dx%d %v", cfg.Width, cfg.Height, err)
	}
	for name, b64 := range map[string]string{
		"not base64":    "!!!not base64!!!",
		"not an image":  base64.StdEncoding.EncodeToString([]byte("#!/bin/sh\necho hi\n")),
		"a cut JPEG":    base64.StdEncoding.EncodeToString(buf.Bytes()[:200]),
		"an empty body": base64.StdEncoding.EncodeToString(nil),
	} {
		if _, err := chatImage(b64); err == nil {
			t.Errorf("%s was passed on", name)
		}
	}
}
