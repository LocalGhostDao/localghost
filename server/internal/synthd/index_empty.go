package synthd

import (
	"context"

	"github.com/LocalGhostDao/localghost/server/internal/synth"
)

// EmptyIndex is the Index wired until one over the memories is built. It holds nothing and reports
// not-ready, so a priming query runs the whole pipeline and returns nothing: no candidate is ever
// invented. The memories themselves are real and are read by the chat another way (memoriesSource
// in cmd/ghost.synthd); this is only the path ghost.cued primes through.
type EmptyIndex struct{}

// NewEmptyIndex returns the not-ready, returns-nothing index.
func NewEmptyIndex() *EmptyIndex { return &EmptyIndex{} }

func (EmptyIndex) Lookup(context.Context, synth.Query) ([]Match, error) { return nil, nil }
func (EmptyIndex) Ready() bool                                          { return false }
func (EmptyIndex) Size() int                                            { return 0 }
