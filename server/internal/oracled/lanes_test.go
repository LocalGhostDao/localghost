package oracled

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/oracle"
)

func quietLanes() *Lanes { return NewLanes(16, slog.New(slog.NewTextHandler(io.Discard, nil))) }

// fakeChild is a second model's process: its starts fail while failStarts lasts, then it is up
// until die closes.
type fakeChild struct {
	mu         sync.Mutex
	failStarts int
	starts     int
	die        chan struct{}
	started    chan string
	release    chan struct{}
}

func newFakeChild() *fakeChild {
	return &fakeChild{die: make(chan struct{}), started: make(chan string, 8), release: make(chan struct{})}
}

func (f *fakeChild) Name() string { return "other-lab-4b" }
func (f *fakeChild) Infer(ctx context.Context, req oracle.Request) (oracle.Response, error) {
	f.started <- req.Capability
	select {
	case <-ctx.Done():
		return oracle.Response{}, ctx.Err()
	case <-f.release:
		return oracle.Response{Output: "cold " + req.Capability, Model: f.Name()}, nil
	}
}
func (f *fakeChild) Start(context.Context) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.starts++
	if f.failStarts > 0 {
		f.failStarts--
		return errors.New("llama-server exited (exit status 1) before it was ready; it said: out of memory")
	}
	return nil
}
func (f *fakeChild) Stop() {}
func (f *fakeChild) Died() (<-chan struct{}, func() string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.die, func() string { return "llama-server exited (signal: killed)" }
}
func (f *fakeChild) DropRejectedArg(error) (string, bool) { return "", false }
func (f *fakeChild) kill() {
	f.mu.Lock()
	old := f.die
	f.die = make(chan struct{})
	f.mu.Unlock()
	close(old)
}
func (f *fakeChild) started2() int { f.mu.Lock(); defer f.mu.Unlock(); return f.starts }

func until(t *testing.T, what string, ok func() bool) {
	t.Helper()
	for end := time.Now().Add(3 * time.Second); time.Now().Before(end); time.Sleep(5 * time.Millisecond) {
		if ok() {
			return
		}
	}
	t.Fatalf("never: %s", what)
}

// A box with a second model: the arbiter's question goes to it while the main model is busy
// with something else, and the answer says which model read it. That is the whole point of the
// second lane: a cold read on the CPU is not in front of a caption on the card, nor behind one.
func TestTheArbiterHasALaneOfItsOwn(t *testing.T) {
	l := quietLanes()
	main := &slowBackend{started: make(chan string, 8), release: make(chan struct{})}
	l.Main.SetBackend(oracle.ClassLocalSmall, main)
	l.Run()
	defer l.Stop()
	child := newFakeChild()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.KeepArbiter(ctx, child)
	until(t, "the second model up", func() bool { return l.Arbiter().Ready })

	caption, _ := l.Submit(oracle.Request{Capability: "caption", Priority: oracle.PriorityBackground, DeadlineMS: 60_000})
	wait(t, main.started, "caption") // the main model is busy
	cold, ok := l.Submit(oracle.Request{Capability: "cold", Class: oracle.ClassArbiter, Priority: oracle.PriorityBackground, DeadlineMS: 60_000})
	if !ok {
		t.Fatal("not taken")
	}
	wait(t, child.started, "cold") // and the second model reads anyway
	// a person starts a chat on the card: the caption steps aside, the cold read on the CPU does not
	l.Pause()
	if r := result(t, caption); r.Err != oracle.ErrPreempted {
		t.Fatalf("caption: %+v", r)
	}
	close(child.release)
	if r := result(t, cold); r.Err != "" || r.Served != oracle.ClassArbiter || r.Model != "other-lab-4b" || r.Output != "cold cold" {
		t.Fatalf("cold read: %+v", r)
	}
	l.Resume()
	if st := l.Arbiter(); !st.Has || st.ServedBy != "other-lab-4b" || st.Model != "other-lab-4b" {
		t.Fatalf("state: %+v", st)
	}
}

// A box with no GPU runs both models on the same cores: there a person's chat sets the
// arbiter's reading aside too, and it is taken up again afterwards.
func TestOnSharedCoresTheArbiterStepsAside(t *testing.T) {
	old := resumeGrace
	resumeGrace = 50 * time.Millisecond
	defer func() { resumeGrace = old }()
	l := quietLanes()
	l.Shares = func() bool { return true }
	main := &slowBackend{started: make(chan string, 8), release: make(chan struct{})}
	l.Main.SetBackend(oracle.ClassLocalSmall, main)
	l.Run()
	defer l.Stop()
	child := newFakeChild()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.KeepArbiter(ctx, child)
	until(t, "the second model up", func() bool { return l.Arbiter().Ready })

	cold, _ := l.Submit(oracle.Request{Capability: "cold", Class: oracle.ClassArbiter, Priority: oracle.PriorityBackground, DeadlineMS: 60_000})
	wait(t, child.started, "cold")
	l.Pause()
	if r := result(t, cold); r.Err != oracle.ErrPreempted {
		t.Fatalf("cold read on shared cores: %+v", r)
	}
	again, _ := l.Submit(oracle.Request{Capability: "again", Class: oracle.ClassArbiter, Priority: oracle.PriorityBackground, DeadlineMS: 60_000})
	select {
	case got := <-child.started:
		t.Fatalf("%q ran while a person was using the box", got)
	case <-time.After(100 * time.Millisecond):
	}
	l.Resume()
	wait(t, child.started, "again")
	close(child.release)
	if r := result(t, again); r.Err != "" {
		t.Fatalf("after the chat: %+v", r)
	}
}

// The second model's comings and goings. No second model: the main one reads and says so.
// Starting, or just died: the caller is told to come back, so an unlock's first pass is not
// read by the wrong model. Down for good: the main model again, saying so, and started again
// as it dies.
func TestTheSecondModelComesAndGoes(t *testing.T) {
	oldGrace, oldBackoff, oldRestart := arbiterGrace, arbiterBackoff, arbiterRestart
	defer func() { arbiterGrace, arbiterBackoff, arbiterRestart = oldGrace, oldBackoff, oldRestart }()
	arbiterRestart = 10 * time.Millisecond
	l := quietLanes()
	main := &slowBackend{started: make(chan string, 8), release: make(chan struct{})}
	close(main.release) // the main model answers at once
	l.Main.SetBackend(oracle.ClassLocalSmall, main)
	l.Run()
	defer l.Stop()
	ask := func() oracle.Response {
		ch, ok := l.Submit(oracle.Request{Capability: "cold", Class: oracle.ClassArbiter, Priority: oracle.PriorityBackground, DeadlineMS: 60_000})
		if !ok {
			t.Fatal("not taken")
		}
		return result(t, ch)
	}
	// one model on the box
	if r := ask(); r.Err != "" || r.Served != oracle.ClassLocalSmall {
		t.Fatalf("one model: %+v", r)
	}
	<-main.started
	if st := l.Arbiter(); st.Has || st.ServedBy != "the main model" {
		t.Fatalf("state with one model: %+v", st)
	}

	// a second model whose first start fails
	child := newFakeChild()
	child.failStarts = 1
	arbiterBackoff = []time.Duration{300 * time.Millisecond}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go l.KeepArbiter(ctx, child)
	until(t, "the failed start said", func() bool { return strings.Contains(l.Arbiter().Why, "out of memory") })
	if r := ask(); r.Err != oracle.ErrArbiterDown {
		t.Fatalf("while it starts: %+v", r)
	}
	if st := l.Arbiter(); !st.Has || st.Ready || !strings.Contains(st.ServedBy, "waiting for other-lab-4b") {
		t.Fatalf("state while starting: %+v", st)
	}
	until(t, "the second start", func() bool { return l.Arbiter().Ready })
	close(child.release)
	if r := ask(); r.Err != "" || r.Served != oracle.ClassArbiter {
		t.Fatalf("once up: %+v", r)
	}
	<-child.started

	// it dies and stays down past the grace: the main model reads, and says it is the main model
	child.mu.Lock()
	child.failStarts = 1 << 20
	child.mu.Unlock()
	arbiterGrace = 40 * time.Millisecond
	child.kill()
	until(t, "its death seen", func() bool { return !l.Arbiter().Ready })
	if st := l.Arbiter(); !strings.Contains(st.Why, "died after") && !strings.Contains(st.Why, "not running") {
		t.Fatalf("why: %+v", st)
	}
	time.Sleep(60 * time.Millisecond)
	if r := ask(); r.Err != "" || r.Served != oracle.ClassLocalSmall {
		t.Fatalf("down for good: %+v", r)
	}
	if st := l.Arbiter(); st.ServedBy != "the main model" {
		t.Fatalf("state when down for good: %+v", st)
	}
	if child.started2() < 3 {
		t.Fatalf("it was not started again: %d starts", child.started2())
	}
}

// Which file is the second model, and what it is called.
func TestPickingTheArbitersModel(t *testing.T) {
	dir := t.TempDir()
	if p, n := PickArbiter(dir); p != "" || n != "" {
		t.Fatalf("an empty folder has none: %q %q", p, n)
	}
	for _, f := range []string{"gemma-4-12b-it-Q4_K_M.gguf", "mmproj-F16.gguf", "arbiter-.gguf", "arbiter-notes.txt", "arbiter-Qwen3-4B-Q4_K_M.gguf", "arbiter-Mistral-7B-Q4_K_M.gguf"} {
		if err := os.WriteFile(filepath.Join(dir, f), []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.Mkdir(filepath.Join(dir, "arbiter-dir.gguf"), 0o700); err != nil {
		t.Fatal(err)
	}
	p, n := PickArbiter(dir)
	if filepath.Base(p) != "arbiter-Mistral-7B-Q4_K_M.gguf" || n != "Mistral-7B-Q4_K_M" {
		t.Fatalf("picked %q as %q", p, n)
	}
	if ArbiterName("/x/arbiter-Qwen3-4B-Q4_K_M.gguf") != "Qwen3-4B-Q4_K_M" || ArbiterName("/x/other.gguf") != "other" {
		t.Fatal("names")
	}
	if p, _ := PickArbiter(filepath.Join(dir, "nowhere")); p != "" {
		t.Fatal("a folder that is not there has none")
	}
}

// The second model's process: on the CPU whatever the main model's default says, no projector,
// one request at a time with room for the longest prompt, and the conf's own flags last.
func TestTheArbitersModelStartsOnTheCPU(t *testing.T) {
	bin, model, port := fakeLlamaBin(t)
	out := filepath.Join(t.TempDir(), "started-with")
	t.Setenv("GHOST_FAKE_LLAMA", "open")
	t.Setenv("GHOST_FAKE_LLAMA_OUT", out)
	b := NewArbiterBackend(bin, model, "other-lab-4b", port, []string{"--mlock"})
	if err := b.Start(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer b.Stop()
	said, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	args := strings.SplitN(string(said), "\n", 2)[0]
	for _, want := range []string{"-ngl 0", "-c 8192", "-np 1", " -t ", "--no-webui", "--host 127.0.0.1"} {
		if !strings.Contains(args, want) {
			t.Fatalf("started without %q: %s", want, args)
		}
	}
	if strings.Contains(args, "-ngl 99") || strings.Contains(args, "--mmproj") || !strings.HasSuffix(args, "--mlock") {
		t.Fatalf("started with: %s", args)
	}
	if b.Name() != "other-lab-4b" || b.client.Timeout != 6*time.Minute {
		t.Fatalf("name %q, timeout %s", b.Name(), b.client.Timeout)
	}
	r, err := b.Infer(context.Background(), oracle.Request{Input: "hello"})
	if err != nil || r.Model != "other-lab-4b" {
		t.Fatalf("asked: %+v %v", r, err)
	}
}
