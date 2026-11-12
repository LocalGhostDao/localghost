// Package synth is the client contract for ghost.synthd's priming: ghost.cued asks "given this
// context, what memories are worth priming, and which (if any) clears the bar to surface". synthd
// owns the retrieval; cued owns the WHEN and the WHETHER.
//
// WHERE IT STANDS (12 November 2026). ghost.synthd is built and makes the memories, but the index
// its "prime" command ranks over is the empty one (internal/synthd), so the socket client below
// gets no candidates and cued's priming stays silent. The contract is the real one: when an index
// over the memories goes in on synthd's side, cued starts surfacing without a line here changing.
package synth

import "context"

// Candidate is one memory synthd thinks MIGHT be relevant to the current context, with the scores cued
// needs to decide whether it clears the bar. synthd proposes; cued disposes.
type Candidate struct {
	ID            string  `json:"id"`            // stable memory id, so cued can track its learning curve
	Title         string  `json:"title"`         // short human label for the cue
	Body          string  `json:"body"`          // the memory/ask text
	Relevance     float64 `json:"relevance"`     // task-relevance to the current context, 0..1
	Distinctiveness float64 `json:"distinctiveness"` // how much it stands out from the moment, 0..1
	Options       []string `json:"options"`      // if this should be an answerable ask, its choices
}

// Query is the context cued hands synthd to prime against. It is deliberately a description of the
// MOMENT (what the user is doing / where / when), not a search string , cued is the First Officer
// reading the situation, not a search box the user typed into.
type Query struct {
	Summary string            `json:"summary"` // cued's running description of the current context
	Signals map[string]string `json:"signals"` // structured context signals (app, place, time-of-day, ...)
	Limit   int               `json:"limit"`   // max candidates to prime (cued keeps this small)
}

// Client is how cued talks to synthd. Prime is the cheap, frequent call (spreading activation on every
// context change); it commits to nothing and returns candidates ranked by synthd. cued then applies
// its own threshold and learning curve before anything reaches the user.
type Client interface {
	// Prime returns candidate memories for a context. Cheap and frequent; returns nil,nil when there
	// is nothing (which is the normal case, and the only case while synthd's index is the empty one).
	Prime(ctx context.Context, q Query) ([]Candidate, error)

	// Ready reports whether synthd has an index behind "prime", so cued can say plainly that its
	// priming has nothing to draw on rather than look as if it found nothing worth showing.
	Ready() bool
}
