// Package pairfilter screens pairs of statements before a costly judge reads
// them: a Filter says which pairs could conflict and which need no judge. It
// knows nothing of its callers (the contradiction detection is the first).
//
// Filters compose: Fallback(first, second) asks the second when the first
// cannot answer, so a filter that depends on a machine (Decision, on the GPU
// box) is always paired with one that does not (All).
package pairfilter

import (
	"context"
	"fmt"

	"github.com/rs/zerolog/log"
)

// Pair is two statements of one person. A date is "2006-01-02", or empty when
// it is unknown.
type Pair struct {
	A, B         string
	DateA, DateB string
}

// Filter says, pair by pair, which pairs the judge must still read.
type Filter interface {
	// Name names the filter in logs.
	Name() string
	// Keep returns one answer per pair, in order: true when the pair goes to
	// the judge, false when it needs none. An error means the filter could
	// not answer for these pairs; it says nothing about them.
	Keep(ctx context.Context, pairs []Pair) ([]bool, error)
}

// All keeps every pair: no filter.
type All struct{}

func (All) Name() string { return "all" }

func (All) Keep(_ context.Context, pairs []Pair) ([]bool, error) {
	keep := make([]bool, len(pairs))
	for i := range keep {
		keep[i] = true
	}
	return keep, nil
}

// Fallback asks first, and second when first cannot answer.
func Fallback(first, second Filter) Filter {
	return fallback{first: first, second: second}
}

type fallback struct{ first, second Filter }

func (f fallback) Name() string { return f.first.Name() + ", else " + f.second.Name() }

func (f fallback) Keep(ctx context.Context, pairs []Pair) ([]bool, error) {
	keep, err := f.first.Keep(ctx, pairs)
	if err == nil && len(keep) != len(pairs) {
		err = fmt.Errorf("%d answers for %d pairs", len(keep), len(pairs))
	}
	if err == nil {
		return keep, nil
	}
	if ctx.Err() != nil {
		return nil, ctx.Err()
	}
	log.Warn().Err(err).Str("filter", f.first.Name()).Str("fallback", f.second.Name()).Int("pairs", len(pairs)).
		Msg("pair filter: the filter did not answer, the fallback decides")
	return f.second.Keep(ctx, pairs)
}
