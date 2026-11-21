package oracled

// LANES. The box's model is a serial resource with one worker (broker.go). ghost.shadowd's
// arbiter is meant to be a different model from the one that answered (Hard Truths, "How to
// prevent building your own dictator brain": a model that knows you agrees with you, and a
// second opinion from the same weights is not a second opinion). A box that has such a model
// runs it as a second llama-server child, on the CPU because the card is the main model's, and
// that child is a second serial resource: it gets a broker of its own, so a cold read that
// takes minutes on the CPU is never in the queue in front of a caption on the GPU.
//
//	no second model on the box   the arbiter class is served by the main model, which says so
//	a second model, up           the arbiter class goes to it
//	a second model, not up yet   oracle.ErrArbiterDown: the caller comes back (an unlock, a restart)
//	a second model, down a while the main model again, saying so: a broken file must not leave
//	                             the box with no cold read at all
//
// The second model is whatever GGUF is at <mount>/ai-models/arbiter-<name>.gguf (PickArbiter),
// or the conf's arbiterModelPath. Which model is the owner's choice and the mirror's signed
// manifest's; nothing here knows its name.

import (
	"context"
	"log/slog"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/oracle"
)

// arbiterGrace is how long a second model that is not up is waited for before the arbiter class
// is served by the main model instead.
var arbiterGrace = 15 * time.Minute

// Lanes is the main model's broker and the arbiter's.
type Lanes struct {
	Main *Broker
	arb  *Broker
	log  *slog.Logger

	mu        sync.Mutex
	has       bool      // the box has a second model for the arbiter
	up        bool      // and its process is serving
	downSince time.Time // since when it has not been
	name      string    // its model name
	why       string    // why it is not up, for the health line
	arbPauses int       // Pauses passed on to the arbiter's broker, to be undone one for one
	// Shares is whether the main model runs on the same cores as the arbiter's (a box with no
	// GPU). When it does, a person using the main model sets the arbiter's work aside exactly
	// as they set aside the main model's background work; when the main model is on the card,
	// the two do not meet and the arbiter reads on. nil: they do not share.
	Shares func() bool
}

// NewLanes builds both brokers. The arbiter's stays empty on a box with no second model.
func NewLanes(queueDepth int, log *slog.Logger) *Lanes {
	return &Lanes{Main: NewBroker(queueDepth, log), arb: NewBroker(queueDepth, log), log: log}
}

// Run starts both workers; Stop halts them.
func (l *Lanes) Run() { l.Main.Run(); l.arb.Run() }
func (l *Lanes) Stop() {
	l.Main.Stop()
	l.arb.Stop()
}

func (l *Lanes) shares() bool { return l.Shares != nil && l.Shares() }

// Submit routes a request to the lane that serves its class (the table above).
func (l *Lanes) Submit(req oracle.Request) (<-chan oracle.Response, bool) {
	if req.Class == oracle.ClassArbiter {
		l.mu.Lock()
		has, up, down := l.has, l.up, time.Since(l.downSince)
		l.mu.Unlock()
		switch {
		case has && up:
			return l.arb.Submit(req)
		case has && down < arbiterGrace:
			ch := make(chan oracle.Response, 1)
			ch <- oracle.Response{Err: oracle.ErrArbiterDown}
			return ch, true
		}
	}
	ch, ok := l.Main.Submit(req)
	if ok && req.Priority >= oracle.PriorityInteractive && l.shares() {
		l.arb.Yield()
	}
	return ch, ok
}

// Pause and Resume are a person starting and ending a streamed chat on the main model.
func (l *Lanes) Pause() {
	l.Main.Pause()
	if l.shares() {
		l.mu.Lock()
		l.arbPauses++
		l.mu.Unlock()
		l.arb.Pause()
	}
}

func (l *Lanes) Resume() {
	l.Main.Resume()
	l.mu.Lock()
	undo := l.arbPauses > 0
	if undo {
		l.arbPauses--
	}
	l.mu.Unlock()
	if undo {
		l.arb.Resume()
	}
}

// Arbiter reports the second model, for `models` and the health line.
func (l *Lanes) Arbiter() oracle.ArbiterInfo {
	l.mu.Lock()
	defer l.mu.Unlock()
	st := oracle.ArbiterInfo{Has: l.has, Model: l.name, Ready: l.up, ServedBy: "the main model"}
	switch {
	case l.has && l.up:
		st.ServedBy = l.name
	case l.has:
		st.Why = l.why
		if time.Since(l.downSince) < arbiterGrace {
			st.ServedBy = "nothing yet: waiting for " + l.name
		}
	}
	return st
}

// arbiterChild is what KeepArbiter needs of the model process (llamaBackend; a fake in tests).
type arbiterChild interface {
	Backend
	Start(ctx context.Context) error
	Stop()
	Died() (<-chan struct{}, func() string)
	DropRejectedArg(err error) (string, bool)
}

// arbiterBackoff: a start that fails, or a child that dies soon after starting, is tried again
// after these, then every ten minutes. A child that stayed up ten minutes starts at the front.
var arbiterBackoff = []time.Duration{30 * time.Second, time.Minute, 2 * time.Minute, 5 * time.Minute, 10 * time.Minute}

// arbiterRestart is the wait before a child that died is started again the first time.
var arbiterRestart = 10 * time.Second

// KeepArbiter runs the second model's process for as long as ctx lives: started, watched,
// started again when it dies, the lane opened and closed as it comes and goes. It blocks.
func (l *Lanes) KeepArbiter(ctx context.Context, be arbiterChild) {
	l.mu.Lock()
	l.has, l.up, l.name, l.downSince, l.why = true, false, be.Name(), time.Now(), "starting"
	l.mu.Unlock()
	fails := 0   // starts that failed, in a row
	crashes := 0 // deaths after ready, in a row; a child that stays up ten minutes resets it
	sleep := func(d time.Duration) bool {
		select {
		case <-ctx.Done():
			return false
		case <-time.After(d):
			return true
		}
	}
	for ctx.Err() == nil {
		l.log.Info("starting the arbiter's model", "fn", "KeepArbiter", "model", be.Name(), "try", fails+1)
		if err := be.Start(ctx); err != nil {
			if ctx.Err() != nil {
				return
			}
			if dropped, ok := be.DropRejectedArg(err); ok {
				l.log.Warn("llama-server does not know a flag from conf/ghost.oracled.conf arbiterArgs , starting without it; take it out of the conf", "fn", "KeepArbiter", "dropped", dropped)
				continue
			}
			wait := arbiterBackoff[min(fails, len(arbiterBackoff)-1)]
			fails++
			l.setWhy("not running (try " + strconv.Itoa(fails) + ", next in " + wait.String() + "): " + clipWhy(err.Error()))
			l.log.Error("the arbiter's model did not become ready", "fn", "KeepArbiter", "try", fails, "retryIn", wait.String(), "err", err)
			be.Stop() // a child still running but never healthy is ended before the next try
			if !sleep(wait) {
				return
			}
			continue
		}
		fails = 0
		l.arb.SetBackend(oracle.ClassArbiter, be)
		l.mu.Lock()
		l.up, l.why = true, ""
		l.mu.Unlock()
		l.log.Info("the arbiter's model is ready", "fn", "KeepArbiter", "model", be.Name())
		died, how := be.Died()
		if died == nil {
			return
		}
		upAt := time.Now()
		select {
		case <-ctx.Done():
			return
		case <-died:
		}
		if ctx.Err() != nil {
			return // our own shutdown stopped it
		}
		why := clipWhy(how())
		if time.Since(upAt) > 10*time.Minute {
			crashes = 0
		}
		wait := arbiterRestart
		if crashes > 0 {
			wait = arbiterBackoff[min(crashes-1, len(arbiterBackoff)-1)]
		}
		crashes++
		l.mu.Lock()
		l.up, l.downSince = false, time.Now()
		l.why = "died after " + time.Since(upAt).Round(time.Second).String() + " up (starting again in " + wait.String() + "): " + why
		l.mu.Unlock()
		l.log.Error("the arbiter's model died while serving", "fn", "KeepArbiter", "up", time.Since(upAt).Round(time.Second).String(), "restartIn", wait.String(), "err", why)
		be.Stop() // reaped already: clears the handle for the next start
		if !sleep(wait) {
			return
		}
	}
}

func (l *Lanes) setWhy(why string) {
	l.mu.Lock()
	l.why = why
	l.mu.Unlock()
}

func clipWhy(s string) string {
	if len(s) > 400 {
		return s[:400]
	}
	return s
}

// arbiterPrefix names the second model's file: ai-models/arbiter-<name>.gguf.
const arbiterPrefix = "arbiter-"

// PickArbiter is the second model on the volume: the first arbiter-*.gguf under dir by name, and
// the model's name (the file's, without the prefix and the extension). "" when there is none,
// which is a box with one model.
func PickArbiter(dir string) (path, name string) {
	ents, err := os.ReadDir(dir)
	if err != nil {
		return "", ""
	}
	var names []string
	for _, e := range ents {
		n := e.Name()
		if e.Type().IsRegular() && strings.HasPrefix(n, arbiterPrefix) && strings.HasSuffix(n, ".gguf") && len(n) > len(arbiterPrefix)+len(".gguf") {
			names = append(names, n)
		}
	}
	if len(names) == 0 {
		return "", ""
	}
	sort.Strings(names)
	return filepath.Join(dir, names[0]), ArbiterName(names[0])
}

// ArbiterName is a model name from its file: arbiter-Qwen3-4B-Q4_K_M.gguf is "Qwen3-4B-Q4_K_M".
func ArbiterName(file string) string {
	n := strings.TrimSuffix(filepath.Base(file), ".gguf")
	return strings.TrimPrefix(n, arbiterPrefix)
}

// ArbiterArgs are the second model's tuning flags before the conf's own: a context long enough
// for the arbiter's longest prompt (twelve turns of a chat) and its reply, one request at a
// time, and half the box's cores, eight at most, so the box stays a box while it reads.
func ArbiterArgs() []string {
	t := runtime.NumCPU() / 2
	if t < 2 {
		t = 2
	}
	if t > 8 {
		t = 8
	}
	return []string{"-c", "8192", "-np", "1", "-t", strconv.Itoa(t)}
}

// NewArbiterBackend prepares (does not start) the second model's process: on the CPU, no
// projector, a longer bound on one request than a model on the card needs.
func NewArbiterBackend(bin, model, name string, port int, extra []string) *llamaBackend {
	return NewLlamaBackend(LlamaConfig{
		BinPath: bin, ModelPath: model, Port: port, ModelName: name,
		ExtraArgs:    append(ArbiterArgs(), extra...),
		CPUOnly:      true,
		InferTimeout: 6 * time.Minute,
	})
}
