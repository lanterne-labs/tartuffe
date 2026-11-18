package core

import (
	"context"
	"sync/atomic"

	"digital-mirror/backend/util"

	"github.com/rs/zerolog/log"
)

// RemovedFacts tells whether a fact read from a source is one a person
// removed from it (storage.WasRemoved).
type RemovedFacts func(ctx context.Context, sourceURL, factText, quote string) (bool, error)

var removedFacts atomic.Pointer[RemovedFacts]

// UseRemovedFacts sets the memory of the removed facts every inserter of the
// process asks before it stores a fact: a fact removed by a person's
// decision does not come back when its document is extracted again (prod
// 2479: a fact deleted on 2026-11-16 was stored again two days later with
// the false contradiction it had caused). nil: no check.
func UseRemovedFacts(f RemovedFacts) {
	if f == nil {
		removedFacts.Store(nil)
		return
	}
	removedFacts.Store(&f)
}

// wasRemoved asks the memory of the removed facts. A failed lookup lets the
// fact in: a fact back is repaired by the next audit, a fact lost is not.
func wasRemoved(ctx context.Context, input StoreFactInput) bool {
	f := removedFacts.Load()
	if f == nil {
		return false
	}
	removed, err := (*f)(ctx, input.SourceURL, input.NormalizedFactText, input.OriginalQuote)
	if err != nil {
		log.Warn().Err(err).Str("source_url", input.SourceURL).Msg("Removed facts could not be looked up; the fact is stored")
		return false
	}
	if removed {
		log.Info().
			Str("source_url", input.SourceURL).
			Str("subject", input.SubjectEntity).
			Str("fact_text", util.TruncateString(input.NormalizedFactText, 100)).
			Msg("Skipping fact: a person removed it from this source")
	}
	return removed
}
