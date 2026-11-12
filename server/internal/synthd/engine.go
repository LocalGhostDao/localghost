// Package synthd is the PRIMING engine ghost.synthd answers ghost.cued with: the retrieval side of
// the "Before You Ask" loop. Given a context (what the person is doing now), it ranks memories
// from an index and returns candidates for cued to gate. cued decides WHEN and WHETHER; this
// decides WHAT is even a candidate.
//
// WHERE IT STANDS (12 November 2026). The memories exist: ghost.synthd makes them, links them and
// reads them for the chat (cmd/ghost.synthd, docs/MEMORY.md). What does not exist is an index
// that ranks them against a MOMENT rather than against a question's words: memories carry no
// embedding, and cued has no context signals to send. So the daemon wires the EmptyIndex here,
// Query runs its whole path and returns nothing, and cued's priming stays silent. An Index over
// the memories table goes behind the interface below without a change to this pipeline or to cued.
package synthd

import (
	"context"
	"log/slog"
	"sort"

	"github.com/LocalGhostDao/localghost/server/internal/synth"
)

// Index is the retrieval backend the engine ranks over. A real implementation ranks the memories
// against a context (by meaning, once they carry embeddings); the one wired today holds nothing.
// Kept an interface so that work goes behind it without touching the query pipeline.
type Index interface {
	// Lookup returns raw matches for a context query: memories the index thinks are near this moment,
	// with a similarity score. synthd turns these into ranked Candidates. Empty until the index exists.
	Lookup(ctx context.Context, q synth.Query) ([]Match, error)
	// Ready reports whether the corpus is actually loaded and queryable.
	Ready() bool
	// Size is the number of indexed memories, for the index-stats command.
	Size() int
}

// Match is one raw hit from the index before synthd ranks/shapes it.
type Match struct {
	ID         string
	Title      string
	Body       string
	Similarity float64  // raw index similarity to the query context, 0..1
	Options    []string // if this memory is an answerable ask
}

// Engine runs the query pipeline over an Index.
type Engine struct {
	index Index
	log   *slog.Logger
}

// New builds the engine over an index (the empty one, until one over the memories is built).
func New(index Index, log *slog.Logger) *Engine {
	return &Engine{index: index, log: log}
}

// Ready surfaces whether retrieval is actually available (drives cued's Ready() and synthd health).
func (e *Engine) Ready() bool { return e.index.Ready() }

// Size is the indexed-memory count for index-stats.
func (e *Engine) Size() int { return e.index.Size() }

// Query is the pipeline cued's Prime call runs: look up matches, shape them into ranked Candidates,
// return the top Limit. Runs end to end today; returns nothing because the index is empty.
func (e *Engine) Query(ctx context.Context, q synth.Query) ([]synth.Candidate, error) {
	matches, err := e.index.Lookup(ctx, q)
	if err != nil {
		return nil, err
	}
	if len(matches) == 0 {
		return nil, nil // the normal case until the corpus exists
	}

	cands := make([]synth.Candidate, 0, len(matches))
	for _, m := range matches {
		cands = append(cands, synth.Candidate{
			ID:    m.ID,
			Title: m.Title,
			Body:  m.Body,
			// Relevance is the index similarity , how near this memory is to the current context.
			Relevance: m.Similarity,
			// Distinctiveness is how much the memory stands out from the moment. Without the corpus and
			// its base-rate statistics we cannot compute this properly yet, so the pipeline carries the
			// similarity through as a placeholder; the real distinctiveness (novelty vs the context's
			// expected memories) lands with the index. cued's threshold gates on both, so an honest
			// placeholder here is safe , it does not manufacture a false "stands out" signal.
			Distinctiveness: m.Similarity,
			Options:         m.Options,
		})
	}
	// Rank by relevance, highest first, and cap at the query limit.
	sort.Slice(cands, func(i, j int) bool { return cands[i].Relevance > cands[j].Relevance })
	if q.Limit > 0 && len(cands) > q.Limit {
		cands = cands[:q.Limit]
	}
	return cands, nil
}
