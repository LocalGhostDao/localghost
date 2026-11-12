package search

import (
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

// A copy of this test binary started with EMBED_TEST_SERVE answers /health on the --port it is
// given: a stand-in for the embeddings llama-server.
func init() {
	if os.Getenv("EMBED_TEST_SERVE") == "" {
		return
	}
	port := ""
	for i, a := range os.Args {
		if a == "--port" && i+1 < len(os.Args) {
			port = os.Args[i+1]
		}
	}
	http.HandleFunc("/health", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(200) })
	// like the real one: with LLAMA_API_KEY in its environment, everything else wants that key
	http.HandleFunc("/v1/embeddings", func(w http.ResponseWriter, r *http.Request) {
		if key := os.Getenv("LLAMA_API_KEY"); key != "" && r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(401)
			return
		}
		if strings.Contains(strings.Join(os.Args, " "), os.Getenv("LLAMA_API_KEY")) {
			w.WriteHeader(500) // the key was on the command line, where ps shows it
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"data":[{"embedding":[3,4]}]}`)
	})
	_ = http.ListenAndServe("127.0.0.1:"+port, nil)
	os.Exit(0)
}

// The embedding server asks for a key too (second audit, C1): what is sent to it is the
// person's own text, and loopback is every user's on the box.
func TestTheEmbeddingServerAsksForItsKey(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	src, err := os.ReadFile(self)
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "llama-server")
	model := filepath.Join(dir, "emb.gguf")
	if os.WriteFile(bin, src, 0o755) != nil || os.WriteFile(model, []byte("x"), 0o600) != nil {
		t.Fatal("setup")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	t.Setenv("EMBED_TEST_SERVE", "1")
	e := NewEmbedServer(EmbedServerConfig{BinPath: bin, ModelPath: model, Port: port})
	if len(e.Key()) != 64 || e.Key() == NewEmbedServer(EmbedServerConfig{}).Key() {
		t.Fatalf("key: %q", e.Key())
	}
	if err := e.Start(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	defer e.Stop()
	// a client without the key (any other process on the box) is refused
	stranger := NewEmbedder(e.BaseURL(), "m")
	if _, err := stranger.Embed(context.Background(), []string{"a private line"}); err == nil || !strings.Contains(err.Error(), "401") {
		t.Fatalf("an embed without the key: %v", err)
	}
	// searchd's own is answered, and the vector comes back unit length
	own := NewEmbedder(e.BaseURL(), "m")
	own.Key = e.Key()
	v, err := own.Embed(context.Background(), []string{"a private line"})
	if err != nil || len(v) != 1 || len(v[0]) != 2 || v[0][0] < 0.59 || v[0][0] > 0.61 {
		t.Fatalf("searchd's own embed: %v %v", v, err)
	}
}

// The embedder killed from outside comes back by itself; Stop ends it for good.
func TestEmbedServerComesBack(t *testing.T) {
	self, err := os.Executable()
	if err != nil {
		t.Skip(err)
	}
	src, err := os.ReadFile(self)
	if err != nil {
		t.Skip(err)
	}
	dir := t.TempDir()
	bin := filepath.Join(dir, "llama-server")
	model := filepath.Join(dir, "emb.gguf")
	if os.WriteFile(bin, src, 0o755) != nil || os.WriteFile(model, []byte("x"), 0o600) != nil {
		t.Fatal("setup")
	}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Skip(err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	t.Setenv("EMBED_TEST_SERVE", "1")
	old := embedBackoff
	embedBackoff = []time.Duration{50 * time.Millisecond}
	defer func() { embedBackoff = old }()

	e := NewEmbedServer(EmbedServerConfig{BinPath: bin, ModelPath: model, Port: port})
	if err := e.Start(10 * time.Second); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var said []string
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	watched := make(chan struct{})
	go func() {
		e.Watch(ctx, func(msg string, args ...any) { mu.Lock(); said = append(said, msg); mu.Unlock() })
		close(watched)
	}()
	first := e.cmd.Process.Pid
	_ = syscall.Kill(first, syscall.SIGKILL)
	healthy := func() bool {
		r, err := http.Get(fmt.Sprintf("http://127.0.0.1:%d/health", port))
		if err != nil {
			return false
		}
		r.Body.Close()
		return r.StatusCode == 200
	}
	deadline := time.Now().Add(15 * time.Second)
	back := func() bool {
		mu.Lock()
		defer mu.Unlock()
		return strings.Contains(strings.Join(said, "|"), "embedding server back")
	}
	for {
		e.mu.Lock()
		c := e.cmd
		e.mu.Unlock()
		if back() && c != nil && c.Process.Pid != first && healthy() {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the embedder did not come back")
		}
		time.Sleep(50 * time.Millisecond)
	}
	e.Stop()
	select {
	case <-watched:
	case <-time.After(5 * time.Second):
		t.Fatal("Watch did not end with Stop")
	}
	if healthy() {
		t.Fatal("still serving after Stop")
	}
}
