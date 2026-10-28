package storage

import (
	"context"
	"fmt"
	"time"
)

// QuoteOnPageOfOtherDate reports whether personID already has a fact source
// with exactly this quote on another URL whose publication date is more than
// gap away from publishedAt; a source without a date, or a new fact without
// one, counts as another date. See core.QuoteElsewhereStorer.
func (s *PostgresStorer) QuoteOnPageOfOtherDate(ctx context.Context, personID int64, quote, sourceURL string, publishedAt *time.Time, gap time.Duration) (bool, error) {
	var seen bool
	err := s.pool.QueryRow(ctx, `
		SELECT EXISTS (
			SELECT 1
			FROM fact_mentions fm
			JOIN fact_sources fs ON fs.id = fm.fact_source_id
			WHERE fm.person_id = $1
			  AND fs.original_quote = $2
			  AND fs.source_url <> $3
			  AND ($4::timestamptz IS NULL OR fs.published_at IS NULL
			       OR abs(extract(epoch FROM fs.published_at - $4::timestamptz)) > $5)
		)`, personID, quote, sourceURL, publishedAt, gap.Seconds()).Scan(&seen)
	if err != nil {
		return false, fmt.Errorf("quote on a page of another date (person %d): %w", personID, err)
	}
	return seen, nil
}
