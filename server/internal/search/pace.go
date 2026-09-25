package search

import (
	"sync"
	"time"
)

// Pace answers one question for the background lanes: is the model on the CPU right now?
//
// On the GPU a caption takes seconds and the two-minute deadline is generous. On the CPU (the card
// gone, September 2026) a caption takes minutes, often more than two: every one that did died at the
// deadline, was retried, died again, and after five tries parked , invisible to the worker. The
// queue looked stopped with most of the library undescribed. The deadlines follow the model now.
//
// Asked of oracled at most once a minute. When oracled does not say, the answer is "slow": a longer
// wait costs minutes, a deadline that is too short costs the job.
type Pace struct {
	Probe func() (onGPU bool, err error)
	Log   func(msg string, args ...any) // told when the answer changes
	mu    sync.Mutex
	at    time.Time
	slow  bool
	known bool
	now   func() time.Time // tests
}

// Slow reports whether the model is on the CPU (or oracled did not say).
func (p *Pace) Slow() bool {
	if p == nil || p.Probe == nil {
		return false
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	now := time.Now
	if p.now != nil {
		now = p.now
	}
	if p.known && now().Sub(p.at) < time.Minute {
		return p.slow
	}
	onGPU, err := p.Probe()
	slow := err != nil || !onGPU
	if p.Log != nil && (!p.known || slow != p.slow) {
		if slow {
			p.Log("model on the CPU (or oracled not saying) , caption and tag deadlines stretched to CPU speed")
		} else {
			p.Log("model on the GPU , caption and tag deadlines back to GPU speed")
		}
	}
	p.slow, p.known, p.at = slow, true, now()
	return slow
}

// pick is fast, or slow when the pace says the model is on the CPU (never shorter than fast).
func (p *Pace) pick(fast, slow time.Duration) time.Duration {
	if p.Slow() && slow > fast {
		return slow
	}
	return fast
}
