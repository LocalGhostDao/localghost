package oracled

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/oracle"
)

// TestMain doubles as a fake llama-server: with GHOST_FAKE_LLAMA set, this binary serves /health
// on the --port it was given, then dies the way a crash does.
func TestMain(m *testing.M) {
	if os.Getenv("GHOST_FAKE_LLAMA") != "" {
		fakeLlamaProcess()
		return
	}
	os.Exit(m.Run())
}

func fakeLlamaProcess() {
	port := ""
	for i, a := range os.Args {
		if a == "--port" && i+1 < len(os.Args) {
			port = os.Args[i+1]
		}
	}
	ln, err := net.Listen("tcp", "127.0.0.1:"+port)
	if err != nil {
		os.Exit(9)
	}
	// what it was started with, for the test that asks where the key travelled
	if out := os.Getenv("GHOST_FAKE_LLAMA_OUT"); out != "" {
		_ = os.WriteFile(out, []byte("args: "+strings.Join(os.Args[1:], " ")+"\nenvkey: "+os.Getenv("LLAMA_API_KEY")+"\n"), 0o600)
	}
	mode := os.Getenv("GHOST_FAKE_LLAMA") // "1": up, then a crash; "keyed": stays, wants its key; "open": stays, wants none
	key := os.Getenv("LLAMA_API_KEY")
	go func() {
		_ = http.Serve(ln, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path != "/health" && mode == "keyed" && r.Header.Get("Authorization") != "Bearer "+key {
				w.WriteHeader(http.StatusUnauthorized)
				_, _ = io.WriteString(w, `{"error":{"message":"Invalid API Key","type":"authentication_error","code":401}}`)
				return
			}
			if r.URL.Path == "/v1/chat/completions" {
				w.Header().Set("Content-Type", "application/json")
				_, _ = io.WriteString(w, `{"choices":[{"finish_reason":"stop","message":{"content":"hello from the model"}}]}`)
				return
			}
			w.WriteHeader(http.StatusOK)
		}))
	}()
	fmt.Println("main: server is listening on http://127.0.0.1:" + port)
	if mode == "keyed" || mode == "open" {
		select {} // until oracled stops it
	}
	time.Sleep(800 * time.Millisecond)
	fmt.Fprintln(os.Stderr, "llama.cpp/tools/mtmd/clip.cpp:1234: GGML_ASSERT(img.nx > 0) failed")
	os.Exit(134)
}

// A llama-server that dies at start (here: a flag it does not know, the way a build from another
// commit answers oracled's arguments) is noticed at once, with its own words, not after minutes of
// polling a dead port with "not healthy" as the only explanation.
func TestStartReportsAChildThatDies(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "llama-server")
	script := "#!/bin/sh\necho 'build: 7fe450e (cuda)'\necho 'error: invalid argument: --no-webui' >&2\nexit 1\n"
	if err := os.WriteFile(bin, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	model := filepath.Join(dir, "m.gguf")
	_ = os.WriteFile(model, make([]byte, 2<<20), 0o644)
	b := NewLlamaBackend(LlamaConfig{BinPath: bin, ModelPath: model, Port: 18999, ModelName: "test"})
	t0 := time.Now()
	err := b.Start(context.Background())
	if err == nil {
		t.Fatal("a child that exited was taken as started")
	}
	if time.Since(t0) > 10*time.Second {
		t.Fatalf("took %s to notice", time.Since(t0))
	}
	if !strings.Contains(err.Error(), "invalid argument: --no-webui") || !strings.Contains(err.Error(), "exited") {
		t.Fatalf("the reason is not in the error: %v", err)
	}
	b.Stop() // already gone: returns at once
	// and a second start tells its own story, not the first one's lines again
	script2 := "#!/bin/sh\necho 'ggml_cuda_init: found 1 CUDA devices'\necho 'CUDA error: out of memory' >&2\nexit 2\n"
	_ = os.WriteFile(bin, []byte(script2), 0o755)
	err = b.Start(context.Background())
	if err == nil || !strings.Contains(err.Error(), "out of memory") || strings.Contains(err.Error(), "--no-webui") {
		t.Fatalf("second start: %v", err)
	}
}

// A flag from conf that this llama.cpp no longer knows is dropped (with its value when it has
// one), and the start goes on without it; oracled's own arguments are never touched.
func TestDropRejectedArg(t *testing.T) {
	b := NewLlamaBackend(LlamaConfig{ExtraArgs: []string{"--threads", "4", "-c", "65536", "--flash-attn", "on", "--mlock"}})
	err := errors.New(`llama-server exited (exit status 1) before it was ready; it said: error: invalid argument: --mlock`)
	if d, ok := b.DropRejectedArg(err); !ok || d != "--mlock" {
		t.Fatalf("dropped %q %v", d, ok)
	}
	if got := strings.Join(b.cfg.ExtraArgs, " "); got != "--threads 4 -c 65536 --flash-attn on" {
		t.Fatalf("left %q", got)
	}
	if d, ok := b.DropRejectedArg(errors.New("error: invalid argument: --flash-attn")); !ok || d != "--flash-attn on" {
		t.Fatalf("with its value: %q %v", d, ok)
	}
	if _, ok := b.DropRejectedArg(errors.New("error: invalid argument: --no-webui")); ok {
		t.Fatal("dropped an argument that was not from conf")
	}
	if _, ok := b.DropRejectedArg(errors.New("CUDA error: out of memory")); ok {
		t.Fatal("dropped something on an error that names no argument")
	}
}

// A llama-server that was ready and then died (a crash on a request, the OOM killer) is noticed:
// Died's channel closes and its account carries the exit and the child's last words. On 29 Sep
// 2026 oracled kept saying "ok" over a dead port because nothing watched after ready.
func TestDiedAfterReady(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "llama-server") // a copy, so the stray sweep before a start never sees this test
	src, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	dst, _ := os.OpenFile(bin, os.O_CREATE|os.O_WRONLY, 0o755)
	_, _ = io.Copy(dst, src)
	src.Close()
	dst.Close()
	model := filepath.Join(dir, "m.gguf")
	_ = os.WriteFile(model, make([]byte, 1<<20), 0o644)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port := ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	t.Setenv("GHOST_FAKE_LLAMA", "1")
	b := NewLlamaBackend(LlamaConfig{BinPath: bin, ModelPath: model, Port: port, ModelName: "test"})
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	died, how := b.Died()
	if died == nil {
		t.Fatal("no channel for a running child")
	}
	select {
	case <-died:
	case <-time.After(10 * time.Second):
		t.Fatal("the death was not noticed")
	}
	msg := how()
	if !strings.Contains(msg, "exit status 134") || !strings.Contains(msg, "GGML_ASSERT") {
		t.Fatalf("the account: %s", msg)
	}
	b.Stop() // already reaped: returns at once, signals nothing
	if d, _ := b.Died(); d != nil {
		t.Fatal("a stopped backend still hands out the old channel")
	}
}

// fakeLlamaBin is this test binary under another name (so the stray sweep before a start never
// sees the test itself), a model file, and a free port.
func fakeLlamaBin(t *testing.T) (bin, model string, port int) {
	t.Helper()
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	dir := t.TempDir()
	bin = filepath.Join(dir, "llama-server")
	src, err := os.Open(self)
	if err != nil {
		t.Fatal(err)
	}
	dst, _ := os.OpenFile(bin, os.O_CREATE|os.O_WRONLY, 0o755)
	_, _ = io.Copy(dst, src)
	src.Close()
	dst.Close()
	model = filepath.Join(dir, "m.gguf")
	_ = os.WriteFile(model, make([]byte, 1<<20), 0o644)
	ln, _ := net.Listen("tcp", "127.0.0.1:0")
	port = ln.Addr().(*net.TCPAddr).Port
	ln.Close()
	return bin, model, port
}

// The model server asks for a key (second audit, C1): loopback is every user's on the box, and
// before 12 Nov 2026 any process could ask the model and watch what it was busy with. The key
// is made by oracled, travels in the child's environment (never its arguments, which `ps` shows
// to everyone), and goes with every request oracled makes.
func TestTheModelServerAsksForItsKey(t *testing.T) {
	bin, model, port := fakeLlamaBin(t)
	out := filepath.Join(t.TempDir(), "started-with")
	t.Setenv("GHOST_FAKE_LLAMA", "keyed")
	t.Setenv("GHOST_FAKE_LLAMA_OUT", out)
	t.Setenv("LLAMA_API_KEY", "left-in-the-operators-shell") // oracled's own key wins over one it inherited
	b := NewLlamaBackend(LlamaConfig{BinPath: bin, ModelPath: model, Port: port, ModelName: "test"})
	if len(b.key) != 64 {
		t.Fatalf("key: %q", b.key)
	}
	if other := NewLlamaBackend(LlamaConfig{}); other.key == b.key {
		t.Fatal("two backends made the same key")
	}
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer b.Stop()
	said, _ := os.ReadFile(out)
	if strings.Contains(strings.SplitN(string(said), "\n", 2)[0], b.key) || strings.Contains(string(said), "--api-key") {
		t.Fatalf("the key is in the child's arguments: %s", said)
	}
	if !strings.Contains(string(said), "envkey: "+b.key+"\n") {
		t.Fatalf("the child's environment does not carry oracled's key: %s", said)
	}
	// anyone else on loopback: refused
	base := fmt.Sprintf("http://127.0.0.1:%d", port)
	for _, path := range []string{"/props", "/slots", "/v1/chat/completions", "/v1/models"} {
		resp, err := http.Post(base+path, "application/json", strings.NewReader("{}"))
		if err != nil {
			t.Fatal(err)
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusUnauthorized {
			t.Fatalf("%s without the key: %d", path, resp.StatusCode)
		}
	}
	if resp, err := http.Get(base + "/health"); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("/health needs no key: %v", err)
	}
	if held, err := b.keyEnforced(); err != nil || !held {
		t.Fatalf("keyEnforced: %v %v", held, err)
	}
	// oracled itself: answered
	got, err := b.Infer(context.Background(), oracle.Request{Capability: "chat", Input: "hi"})
	if err != nil || !strings.Contains(got.Output, "hello from the model") {
		t.Fatalf("oracled's own request: %+v %v", got, err)
	}
}

// A llama.cpp that does not read the key still starts and still answers oracled; what is lost
// is said (the log, tools/privacy_check.sh), never the model.
func TestAModelServerThatIgnoresTheKeyStillRuns(t *testing.T) {
	bin, model, port := fakeLlamaBin(t)
	t.Setenv("GHOST_FAKE_LLAMA", "open")
	b := NewLlamaBackend(LlamaConfig{BinPath: bin, ModelPath: model, Port: port, ModelName: "test"})
	if err := b.Start(context.Background()); err != nil {
		t.Fatalf("start: %v", err)
	}
	defer b.Stop()
	if held, err := b.keyEnforced(); err != nil || held {
		t.Fatalf("keyEnforced on a server that asks for nothing: %v %v", held, err)
	}
	if got, err := b.Infer(context.Background(), oracle.Request{Capability: "chat", Input: "hi"}); err != nil || got.Output == "" {
		t.Fatalf("infer: %+v %v", got, err)
	}
}
