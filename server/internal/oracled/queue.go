// Package oracled is the server side of the inference broker (ghost.oracled). The model is a serial
// resource , one inference at a time , so a single worker pulls from a priority queue: interactive
// requests before background ones, and within a priority, oldest first (FIFO fairness). A request with
// a deadline that ages out WHILE WAITING is dropped at dequeue without ever hitting the model, which
// reclaims the slot instead of spending it on stale work , this is what makes a caller's short
// deadline (watchd's log triage) cheap.
package oracled

import (
	"container/heap"
	"sync"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/oracle"
)

// item is one queued request plus the channel the caller blocks on.
type item struct {
	req      oracle.Request
	enqueued time.Time
	result   chan oracle.Response
	index    int // heap bookkeeping
}

// pq orders by priority (higher first), then by enqueue time (older first). It is a container/heap.
type pq []*item

func (q pq) Len() int { return len(q) }
func (q pq) Less(i, j int) bool {
	if q[i].req.Priority != q[j].req.Priority {
		return q[i].req.Priority > q[j].req.Priority // interactive (higher) first
	}
	return q[i].enqueued.Before(q[j].enqueued) // then FIFO
}
func (q pq) Swap(i, j int) {
	q[i], q[j] = q[j], q[i]
	q[i].index, q[j].index = i, j
}
func (q *pq) Push(x any) {
	it := x.(*item)
	it.index = len(*q)
	*q = append(*q, it)
}
func (q *pq) Pop() any {
	old := *q
	n := len(old)
	it := old[n-1]
	old[n-1] = nil
	*q = old[:n-1]
	return it
}

// Queue is the bounded priority queue with a single consumer (the backend worker).
type Queue struct {
	mu     sync.Mutex
	items  pq
	notify chan struct{} // signals the worker that an item is available
	maxLen int
	closed bool
}

// NewQueue builds a queue that holds at most maxLen waiting requests (backpressure: submits beyond
// this are rejected rather than growing unbounded and OOMing the box).
func NewQueue(maxLen int) *Queue {
	return &Queue{notify: make(chan struct{}, 1), maxLen: maxLen}
}

// Submit enqueues a request and returns the channel the caller waits on. If the queue is full it
// returns ok=false immediately, so the caller can shed rather than block.
func (q *Queue) Submit(req oracle.Request) (<-chan oracle.Response, bool) {
	q.mu.Lock()
	if q.closed || len(q.items) >= q.maxLen {
		q.mu.Unlock()
		return nil, false
	}
	it := &item{req: req, enqueued: time.Now(), result: make(chan oracle.Response, 1)}
	heap.Push(&q.items, it)
	q.mu.Unlock()
	q.signal()
	return it.result, true
}

// next pops the highest-priority request whose deadline has NOT passed, dropping (and failing) any
// that aged out while waiting. Returns nil if the queue is empty. Blocks the caller? No , it is
// non-blocking; the worker calls it after a notify.
func (q *Queue) next() *item {
	q.mu.Lock()
	defer q.mu.Unlock()
	for len(q.items) > 0 {
		it := heap.Pop(&q.items).(*item)
		if it.req.DeadlineMS > 0 {
			if time.Since(it.enqueued) > time.Duration(it.req.DeadlineMS)*time.Millisecond {
				// aged out in the queue , fail it without running, reclaim the slot
				it.result <- oracle.Response{Err: "deadline exceeded in queue"}
				continue
			}
		}
		return it
	}
	return nil
}

func (q *Queue) signal() {
	select {
	case q.notify <- struct{}{}:
	default:
	}
}

// Wait blocks until there may be work or the stop channel closes. The worker loop uses this.
func (q *Queue) Wait(stop <-chan struct{}) bool {
	select {
	case <-stop:
		return false
	case <-q.notify:
		return true
	}
}

// Close drains the queue, failing every waiter, and stops accepting new work.
func (q *Queue) Close() {
	q.mu.Lock()
	q.closed = true
	for len(q.items) > 0 {
		it := heap.Pop(&q.items).(*item)
		it.result <- oracle.Response{Err: "oracled shutting down"}
	}
	q.mu.Unlock()
}
