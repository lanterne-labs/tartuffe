package storage

import (
	"context"
	"fmt"
)

// removedFactSimilarity is the trigram similarity at or above which a fact
// read again from a source is the one removed from it. Two extractions of
// one passage differ by a few words (measured on the pages extracted twice
// on 2026-11-14 and 2026-11-18: 0.75 to 1.0); two facts of one article about
// different things are under 0.4.
const removedFactSimilarity = 0.6

// RemoveFactsForGood deletes facts a person decided to remove and remembers
// them, source by source, so the same fact is not stored again when its
// document is extracted again. Use DeleteFactsAndCleanup for a deletion that
// a new extraction is meant to replace (a rebuild, a reset).
func (s *PostgresStorer) RemoveFactsForGood(ctx context.Context, factIDs []int64, reason string) (FactDeletionCleanupResult, error) {
	if len(factIDs) == 0 {
		return FactDeletionCleanupResult{}, nil
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO removed_facts (fact_id, source_url, fact_text, quote, reason)
		SELECT f.id, fs.source_url, f.fact_text, COALESCE(fs.original_quote, ''), $2
		FROM facts f JOIN fact_sources fs ON fs.fact_id = f.id
		WHERE f.id = ANY($1) AND fs.source_url <> ''
		ON CONFLICT (fact_id, source_url) DO NOTHING`, factIDs, reason); err != nil {
		return FactDeletionCleanupResult{}, fmt.Errorf("recording the removed facts: %w", err)
	}
	return s.DeleteFactsAndCleanup(ctx, factIDs)
}

// WasRemoved tells whether a fact read from sourceURL is one a person
// removed from that source: the same text or the same quote, give or take a
// few words.
func (s *PostgresStorer) WasRemoved(ctx context.Context, sourceURL, factText, quote string) (bool, error) {
	if sourceURL == "" {
		return false, nil
	}
	var removed bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1 FROM removed_facts
			WHERE source_url = $1
			  AND (similarity(fact_text, $2) >= $4
			       OR ($3 <> '' AND quote <> '' AND similarity(quote, $3) >= $4)))`,
		sourceURL, factText, quote, removedFactSimilarity).Scan(&removed)
	if err != nil {
		return false, fmt.Errorf("looking up the removed facts of %s: %w", sourceURL, err)
	}
	return removed, nil
}
