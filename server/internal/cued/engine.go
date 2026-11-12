// Package cued is the engine of the cueing daemon: the four mechanisms from the "Before You Ask" Hard
// Truth, made concrete. It reads a running description of context, primes candidates cheaply on every
// change (committing to nothing), applies a suppression threshold so it surfaces almost nothing, and
// tracks a per-cue learning curve so a cue worth showing once is not worth showing the fiftieth time.
//
// The mechanisms map one-to-one to the neuroscience the post draws on:
//   - hippocampal remapping -> Context: a live representation of the moment, updated on signals.
//   - spreading activation   -> Engine.prime: cheap pre-warming of candidates, no commitment.
//   - executive suppression  -> Threshold: the default is silence; only distinctive AND task-relevant
//                               candidates that clear the bar are allowed through.
//   - Fitts-Posner curve     -> curve: usage lowers a cue's value; a well-learned cue is suppressed.
//
// cued does NOT own retrieval , it asks ghost.synthd (via the synth.Client) for candidates. synthd's
// index for this is the empty one today and nothing feeds this engine a context, so prime returns
// nothing and the engine stays silent while its logic is whole and tested. What ghost.cued does say
// today is its own loops' (cmd/ghost.cued: a day a year ago, something nearby).
package cued

import (
	"context"
	"log/slog"
	"math"
	"sync"
	"time"

	"github.com/LocalGhostDao/localghost/server/internal/synth"
)

// Context is cued's running description of the current moment (hippocampal remapping). It is a small
// structured view, updated as signals arrive; a meaningful change is what triggers a priming pass.
type Context struct {
	Summary string            // a short human description cued maintains
	Signals map[string]string // structured signals: app, place, timeOfDay, activity, ...
	updated time.Time
}

// Config is the tunable behaviour , all of it live-reloadable via ghost.cued.conf and the ctlsock.
type Config struct {
	// SurfaceThreshold is the bar a candidate's combined score must clear to be surfaced. High on
	// purpose: silence is the default. 0..1.
	SurfaceThreshold float64
	// MinDistinctiveness gates on standing out from the moment: a candidate that is relevant but not
	// distinctive is noise. 0..1.
	MinDistinctiveness float64
	// PrimeLimit caps how many candidates cued asks synthd for per context change (kept small; priming
	// is cheap but not free).
	PrimeLimit int
	// LearningHalfLife is how fast a repeatedly-surfaced cue's value decays toward suppression. After
	// this many surfaces of the SAME cue, its contribution is roughly halved.
	LearningHalfLife float64
	// Cooldown is the minimum time between ANY two surfaced cues , cued is quiet and rare regardless of
	// how many candidates clear the bar.
	Cooldown time.Duration
}

// DefaultConfig is deliberately conservative: a high bar, real cooldown, small prime batch.
func DefaultConfig() Config {
	return Config{
		SurfaceThreshold:   0.80,
		MinDistinctiveness: 0.50,
		PrimeLimit:         8,
		LearningHalfLife:   5,
		Cooldown:           2 * time.Hour,
	}
}

// Surfacer is what the engine calls when a candidate clears every gate , it produces the actual ask.
// Kept an interface so the engine is testable without the notification store.
type Surfacer interface {
	Surface(c synth.Candidate) error
}

// Engine runs the four mechanisms. It is fed context updates; it decides, quietly and rarely, whether
// to surface anything.
type Engine struct {
	mu        sync.Mutex
	cfg       Config
	synth     synth.Client
	surfacer  Surfacer
	log       *slog.Logger
	ctx       Context
	lastCue   time.Time
	seen      map[string]int // per-cue-id surface count , the learning curve state
}

// New builds an engine. cfg is live-swappable via SetConfig on reload.
func New(cfg Config, sc synth.Client, sf Surfacer, log *slog.Logger) *Engine {
	return &Engine{
		cfg: cfg, synth: sc, surfacer: sf, log: log,
		ctx:  Context{Signals: map[string]string{}},
		seen: map[string]int{},
	}
}

// SetConfig swaps the tuning live (reload path).
func (e *Engine) SetConfig(cfg Config) {
	e.mu.Lock()
	e.cfg = cfg
	e.mu.Unlock()
}

// UpdateContext replaces the running context and, if the change is meaningful, runs a priming +
// evaluation pass. Callers feed this from whatever context sources exist (today: little; when the
// signal sources are built, more). A no-op change does not trigger a pass.
func (e *Engine) UpdateContext(summary string, signals map[string]string) {
	e.mu.Lock()
	changed := summary != e.ctx.Summary || !sameSignals(signals, e.ctx.Signals)
	e.ctx = Context{Summary: summary, Signals: signals, updated: time.Now()}
	cfg := e.cfg
	e.mu.Unlock()
	if !changed {
		return
	}
	e.evaluate(cfg)
}

// evaluate is one pass: prime candidates for the current context, then apply the threshold + learning
// curve + cooldown. It surfaces at most ONE cue per pass (the best one that clears the bar), because
// cued is a First Officer, not a firehose.
func (e *Engine) evaluate(cfg Config) {
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()

	e.mu.Lock()
	q := synth.Query{Summary: e.ctx.Summary, Signals: e.ctx.Signals, Limit: cfg.PrimeLimit}
	e.mu.Unlock()

	cands, err := e.synth.Prime(ctx, q) // spreading activation , cheap, commits to nothing
	if err != nil {
		e.log.Warn("prime failed", "fn", "evaluate", "err", err)
		return
	}
	if len(cands) == 0 {
		return // the normal case, and the ONLY case until synthd is built , stay silent
	}

	// Cooldown: cued is quiet regardless of how many candidates exist.
	e.mu.Lock()
	if !e.lastCue.IsZero() && time.Since(e.lastCue) < cfg.Cooldown {
		e.mu.Unlock()
		return
	}
	// Pick the single best candidate that clears every gate.
	var best *synth.Candidate
	var bestScore float64
	for i := range cands {
		c := cands[i]
		s := e.score(c, cfg)
		if s >= cfg.SurfaceThreshold && c.Distinctiveness >= cfg.MinDistinctiveness && s > bestScore {
			best = &cands[i]
			bestScore = s
		}
	}
	if best == nil {
		e.mu.Unlock()
		return // nothing cleared the bar , the intended default
	}
	e.seen[best.ID]++
	e.lastCue = time.Now()
	id, score := best.ID, bestScore
	e.mu.Unlock()

	if err := e.surfacer.Surface(*best); err != nil {
		e.log.Warn("surface failed", "fn", "evaluate", "id", id, "err", err)
		return
	}
	e.log.Info("surfaced cue", "fn", "evaluate", "id", id, "score", score)
}

// score combines task-relevance and distinctiveness, then applies the learning-curve decay: a cue
// surfaced many times before is worth progressively less (Fitts-Posner). The decay halves the score
// every LearningHalfLife surfaces of the same cue.
func (e *Engine) score(c synth.Candidate, cfg Config) float64 {
	base := 0.6*c.Relevance + 0.4*c.Distinctiveness
	if cfg.LearningHalfLife <= 0 {
		return base
	}
	n := float64(e.seen[c.ID])          // caller holds the lock
	decay := math.Exp2(-n / cfg.LearningHalfLife) // 1.0 unseen, halves every half-life
	return base * decay
}

// Snapshot is the engine's state for the ctlsock status/queue command.
type Snapshot struct {
	SynthReady bool              `json:"synthReady"`
	ContextSummary string        `json:"contextSummary"`
	LastCue    string            `json:"lastCue,omitempty"`
	SeenCounts map[string]int    `json:"seenCounts"`
}

// Snapshot returns the current engine state (for ghost-cli ghost.cued status / queue).
func (e *Engine) Snapshot() Snapshot {
	e.mu.Lock()
	defer e.mu.Unlock()
	seen := make(map[string]int, len(e.seen))
	for k, v := range e.seen {
		seen[k] = v
	}
	last := ""
	if !e.lastCue.IsZero() {
		last = e.lastCue.UTC().Format(time.RFC3339)
	}
	return Snapshot{
		SynthReady:     e.synth.Ready(),
		ContextSummary: e.ctx.Summary,
		LastCue:        last,
		SeenCounts:     seen,
	}
}

// --- helpers ---

func sameSignals(a, b map[string]string) bool {
	if len(a) != len(b) {
		return false
	}
	for k, v := range a {
		if b[k] != v {
			return false
		}
	}
	return true
}



