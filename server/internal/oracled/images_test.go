package oracled

import (
	"context"
	"encoding/base64"
	"errors"
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
		"webp":    []byte("RIFF\x00\x00\x00\x00WEBPVP8 "),
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
	os.WriteFile(jpg, []byte{0xFF, 0xD8, 0xFF, 0xE0, 1, 2, 3}, 0o644)
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
		t.Fatalf("jpeg passthrough: %v %.30s calls=%d", err, uri, calls)
	}
	if uri, err := imageForModel(context.Background(), webp); err != nil || !strings.HasPrefix(uri, "data:image/png;base64,") {
		t.Fatalf("webp converted: %v %.30s", err, uri)
	}
	if _, err := imageForModel(context.Background(), heic); err == nil || !strings.Contains(err.Error(), "heic") {
		t.Fatalf("heic: %v", err)
	}
	if _, err := imageForModel(context.Background(), filepath.Join(dir, "gone.jpg")); err == nil || !strings.Contains(err.Error(), "read image") {
		t.Fatalf("missing file: %v", err)
	}
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
