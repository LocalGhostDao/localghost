package oracled

import (
	"context"
	"io"
	"log/slog"
	"sync/atomic"
	"testing"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/oracle"
)

// slowBackend runs until its context ends or release closes, recording what started.
type slowBackend struct {
	started chan string
	release chan struct{}
	runs    atomic.Int32
}

func (s *slowBackend) Name() string { return "slow" }
func (s *slowBackend) Infer(ctx context.Context, req oracle.Request) (oracle.Response, error) {
	s.runs.Add(1)
	s.started <- req.Capability
	select {
	case <-ctx.Done():
		return oracle.Response{}, ctx.Err()
	case <-s.release:
		return oracle.Response{Output: "done " + req.Capability, Model: "m"}, nil
	}
}

func wait(t *testing.T, ch <-chan string, want string) {
	t.Helper()
	select {
	case got := <-ch:
		if got != want {
			t.Fatalf("started %q, want %q", got, want)
		}
	case <-time.After(2 * time.Second):
		t.Fatalf("%q never started", want)
	}
}

func result(t *testing.T, ch <-chan oracle.Response) oracle.Response {
	t.Helper()
	select {
	case r := <-ch:
		return r
	case <-time.After(3 * time.Second):
		t.Fatal("no result")
	}
	return oracle.Response{}
}

func TestChatPausesAndPreemptsBackgroundWork(t *testing.T) {
	old := resumeGrace
	resumeGrace = 150 * time.Millisecond
	defer func() { resumeGrace = old }()
	be := &slowBackend{started: make(chan string, 8), release: make(chan struct{})}
	b := NewBroker(16, slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.SetBackend(oracle.ClassLocalSmall, be)
	b.Run()
	defer b.Stop()

	cap1, _ := b.Submit(oracle.Request{Capability: "caption-1", Priority: oracle.PriorityBackground, DeadlineMS: 60_000})
	wait(t, be.started, "caption-1")

	// a person starts a chat: the running caption is set aside with ErrPreempted, not a failure
	b.Pause()
	if r := result(t, cap1); r.Err != oracle.ErrPreempted {
		t.Fatalf("running caption: %+v", r)
	}
	// background submitted during the chat waits; an interactive request still runs
	cap2, _ := b.Submit(oracle.Request{Capability: "caption-2", Priority: oracle.PriorityBackground, DeadlineMS: 60_000})
	ask, _ := b.Submit(oracle.Request{Capability: "ask", Priority: oracle.PriorityInteractive, DeadlineMS: 60_000})
	wait(t, be.started, "ask")
	be.release <- struct{}{}
	if r := result(t, ask); r.Output != "done ask" {
		t.Fatalf("interactive during a chat: %+v", r)
	}
	select {
	case s := <-be.started:
		t.Fatalf("%s started during the chat", s)
	case <-time.After(100 * time.Millisecond):
	}

	// the chat ends: after the grace, the waiting caption runs
	t0 := time.Now()
	b.Resume()
	wait(t, be.started, "caption-2")
	if time.Since(t0) < resumeGrace {
		t.Fatalf("background resumed after %s, before the grace", time.Since(t0))
	}
	be.release <- struct{}{}
	if r := result(t, cap2); r.Output != "done caption-2" {
		t.Fatalf("caption after the chat: %+v", r)
	}

	// an interactive request running when a chat starts is NOT preempted
	ask2, _ := b.Submit(oracle.Request{Capability: "ask-2", Priority: oracle.PriorityInteractive})
	wait(t, be.started, "ask-2")
	b.Pause()
	be.release <- struct{}{}
	if r := result(t, ask2); r.Err != "" {
		t.Fatalf("interactive was preempted: %+v", r)
	}
	b.Resume()
}

func TestQueueHoldsBackgroundButStillDropsExpired(t *testing.T) {
	q := NewQueue(8)
	old, _ := q.Submit(oracle.Request{Capability: "old", Priority: oracle.PriorityBackground, DeadlineMS: 1})
	time.Sleep(5 * time.Millisecond)
	q.Submit(oracle.Request{Capability: "bg", Priority: oracle.PriorityBackground})
	if it := q.next(false); it != nil {
		t.Fatalf("background came out while held: %s", it.req.Capability)
	}
	if r := <-old; r.Err != "deadline exceeded in queue" {
		t.Fatalf("expired item: %+v", r)
	}
	if it := q.next(true); it == nil || it.req.Capability != "bg" {
		t.Fatalf("after the hold: %+v", it)
	}
}

// A request a person is waiting for (the web plan, a spoken question) does not queue behind a
// running caption: the caption steps aside as it does for a chat, and none starts for the grace.
func TestInteractiveRequestClearsTheWay(t *testing.T) {
	old := resumeGrace
	resumeGrace = 150 * time.Millisecond
	defer func() { resumeGrace = old }()
	be := &slowBackend{started: make(chan string, 8), release: make(chan struct{})}
	b := NewBroker(16, slog.New(slog.NewTextHandler(io.Discard, nil)))
	b.SetBackend(oracle.ClassLocalSmall, be)
	b.Run()
	defer b.Stop()

	cap1, _ := b.Submit(oracle.Request{Capability: "caption-1", Priority: oracle.PriorityBackground, DeadlineMS: 60_000})
	wait(t, be.started, "caption-1")
	t0 := time.Now()
	plan, _ := b.Submit(oracle.Request{Capability: "plan", Priority: oracle.PriorityInteractive, DeadlineMS: 8000})
	if r := result(t, cap1); r.Err != oracle.ErrPreempted {
		t.Fatalf("the running caption: %+v", r)
	}
	wait(t, be.started, "plan")
	if waited := time.Since(t0); waited > time.Second {
		t.Fatalf("the plan waited %s behind a caption", waited)
	}
	// a caption queued meanwhile does not start until the grace after the plan was asked
	cap2, _ := b.Submit(oracle.Request{Capability: "caption-2", Priority: oracle.PriorityBackground, DeadlineMS: 60_000})
	be.release <- struct{}{}
	if r := result(t, plan); r.Output != "done plan" {
		t.Fatalf("plan: %+v", r)
	}
	wait(t, be.started, "caption-2")
	if time.Since(t0) < resumeGrace {
		t.Fatalf("background started %s after the plan, inside the grace", time.Since(t0))
	}
	be.release <- struct{}{}
	if r := result(t, cap2); r.Output != "done caption-2" {
		t.Fatalf("caption after the plan: %+v", r)
	}
	// an interactive request does not preempt another interactive one
	a1, _ := b.Submit(oracle.Request{Capability: "ask-1", Priority: oracle.PriorityInteractive})
	wait(t, be.started, "ask-1")
	a2, _ := b.Submit(oracle.Request{Capability: "ask-2", Priority: oracle.PriorityInteractive})
	be.release <- struct{}{}
	if r := result(t, a1); r.Err != "" {
		t.Fatalf("the first interactive request was preempted: %+v", r)
	}
	wait(t, be.started, "ask-2")
	be.release <- struct{}{}
	_ = result(t, a2)
}
