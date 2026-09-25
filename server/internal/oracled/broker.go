package oracled

// Broker ties the queue to the backends. A single worker goroutine pulls the highest-priority,
// non-expired request and runs it on the backend resolved from the request's class. One worker means
// the local model is never asked to do two inferences at once (the hardware reality); the queue in
// front provides fairness, priority, deadlines, and backpressure.
//
// Routing is class -> backend. Today "local-small" resolves to the llama-server child and "frontier"
// resolves to nothing unless the owner configured a frontier backend. A capability ("classify" vs
// "chat") could shape the prompt or pick a different backend later; for now it is passed through and
// the backend treats the Input as the prompt.

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/oracle"
)

// Broker owns the queue, the backends by class, and the worker.
type Broker struct {
	queue    *Queue
	backends map[oracle.Class]Backend
	log      *slog.Logger
	stop     chan struct{}
	wg       sync.WaitGroup

	// what is running now, so a person starting a chat can set background work aside (Pause)
	runMu     sync.Mutex
	running   *item
	runCancel context.CancelFunc
	preempted bool
	pauses    int
	holdUntil time.Time
}

// resumeGrace keeps background work off the model for a moment after a chat ends, so a follow-up
// question does not find a ten-minute CPU caption in its way.
var resumeGrace = 20 * time.Second

// Pause is called when a person starts using the model directly (a streamed chat, which bypasses
// the queue). A BACKGROUND inference running now is cancelled , its caller gets oracle.ErrPreempted
// and retries later without losing an attempt , and no background work starts until Resume, plus a
// short grace. Interactive queued requests still run. On the GPU this costs a caption a few seconds;
// on the CPU it is the difference between a chat that answers and one that waits behind a
// ten-minute caption (llama-server's parallel slots share the same cores).
func (b *Broker) Pause() {
	b.runMu.Lock()
	b.pauses++
	if b.running != nil && b.running.req.Priority < oracle.PriorityInteractive && b.runCancel != nil && !b.preempted {
		b.preempted = true
		b.runCancel()
	}
	b.runMu.Unlock()
}

// Resume undoes one Pause; background work starts again after resumeGrace.
func (b *Broker) Resume() {
	b.runMu.Lock()
	if b.pauses > 0 {
		b.pauses--
	}
	b.holdUntil = time.Now().Add(resumeGrace)
	b.runMu.Unlock()
	time.AfterFunc(resumeGrace+50*time.Millisecond, b.queue.signal)
}

func (b *Broker) backgroundAllowed() bool {
	b.runMu.Lock()
	defer b.runMu.Unlock()
	return b.pauses == 0 && !time.Now().Before(b.holdUntil)
}

// NewBroker builds a broker over a queue of the given depth.
func NewBroker(queueDepth int, log *slog.Logger) *Broker {
	return &Broker{
		queue:    NewQueue(queueDepth),
		backends: map[oracle.Class]Backend{},
		log:      log,
		stop:     make(chan struct{}),
	}
}

// SetBackend registers the backend that serves a class. Called at start once the llama-server child is
// healthy (for local-small) and/or a frontier backend is configured.
func (b *Broker) SetBackend(class oracle.Class, be Backend) {
	b.backends[class] = be
}

// Submit enqueues a request; the caller waits on the returned channel. ok=false means the queue is
// full (backpressure) , the caller should shed.
func (b *Broker) Submit(req oracle.Request) (<-chan oracle.Response, bool) {
	// default class if unset
	if req.Class == "" {
		req.Class = oracle.ClassLocalSmall
	}
	return b.queue.Submit(req)
}

// Run starts the single worker. It blocks until Stop.
func (b *Broker) Run() {
	b.wg.Add(1)
	go b.worker()
}

func (b *Broker) worker() {
	defer b.wg.Done()
	for {
		if !b.queue.Wait(b.stop) {
			return
		}
		// drain everything currently available before waiting again
		for {
			it := b.queue.next(b.backgroundAllowed())
			if it == nil {
				break
			}
			b.serve(it)
		}
	}
}

func (b *Broker) serve(it *item) {
	be, ok := b.backends[it.req.Class]
	if !ok {
		it.result <- oracle.Response{Err: "no backend for class " + string(it.req.Class)}
		return
	}
	// Bound the inference by the request deadline if set, else a generous ceiling.
	timeout := 120 * time.Second
	if it.req.DeadlineMS > 0 {
		// remaining budget from when it was enqueued
		spent := time.Since(it.enqueued)
		remaining := time.Duration(it.req.DeadlineMS)*time.Millisecond - spent
		if remaining <= 0 {
			it.result <- oracle.Response{Err: "deadline exceeded before dispatch"}
			return
		}
		timeout = remaining
	}
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	b.runMu.Lock()
	b.running, b.runCancel, b.preempted = it, cancel, false
	if b.pauses > 0 && it.req.Priority < oracle.PriorityInteractive {
		b.preempted = true // a chat began between the dequeue and here
		cancel()
	}
	b.runMu.Unlock()
	start := time.Now()
	resp, err := be.Infer(ctx, it.req)
	b.runMu.Lock()
	preempted := b.preempted
	b.running, b.runCancel, b.preempted = nil, nil, false
	b.runMu.Unlock()
	if preempted {
		b.log.Info("background inference set aside for a chat", "fn", "serve", "capability", it.req.Capability,
			"after_ms", time.Since(start).Milliseconds())
		it.result <- oracle.Response{Err: oracle.ErrPreempted}
		return
	}
	if err != nil {
		b.log.Warn("inference failed", "fn", "serve", "capability", it.req.Capability,
			"class", string(it.req.Class), "err", err)
		it.result <- oracle.Response{Err: err.Error()}
		return
	}
	b.log.Debug("inference served", "fn", "serve", "capability", it.req.Capability,
		"model", resp.Model, "ms", time.Since(start).Milliseconds())
	it.result <- resp
}

// Stop halts the worker and drains the queue (failing waiters).
func (b *Broker) Stop() {
	close(b.stop)
	b.queue.Close()
	b.wg.Wait()
}
