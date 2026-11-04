package storage

import (
	"context"
	"fmt"
	"time"
)

// UndatedNewsPage is a news page whose fact sources carry no date, with the
// title of its stored document.
type UndatedNewsPage struct {
	URL     string
	Title   string
	Sources int
}

// ListUndatedNewsPages returns the news pages with at least one fact source
// without a date, by URL.
func (s *PostgresStorer) ListUndatedNewsPages(ctx context.Context) ([]UndatedNewsPage, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT fs.source_url, COALESCE(max(sd.title), ''), count(DISTINCT fs.id)
		FROM fact_sources fs
		LEFT JOIN source_documents sd ON sd.source_type = 'news' AND sd.url = fs.source_url
		WHERE fs.source_type = 'news' AND fs.published_at IS NULL
		GROUP BY fs.source_url
		ORDER BY fs.source_url`)
	if err != nil {
		return nil, fmt.Errorf("listing the undated news pages: %w", err)
	}
	defer rows.Close()
	var out []UndatedNewsPage
	for rows.Next() {
		var p UndatedNewsPage
		if err := rows.Scan(&p.URL, &p.Title, &p.Sources); err != nil {
			return nil, fmt.Errorf("scanning an undated news page: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetNewsPageDate dates the undated news fact sources of the page, and its
// stored document when undated, and returns how many fact sources it dated.
// A date already stored is never overwritten.
func (s *PostgresStorer) SetNewsPageDate(ctx context.Context, url string, publishedAt time.Time) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("dating the news page %s: %w", url, err)
	}
	defer tx.Rollback(ctx)
	tag, err := tx.Exec(ctx, `UPDATE fact_sources SET published_at = $2
		WHERE source_type = 'news' AND source_url = $1 AND published_at IS NULL`, url, publishedAt)
	if err != nil {
		return 0, fmt.Errorf("dating the fact sources of %s: %w", url, err)
	}
	if _, err := tx.Exec(ctx, `UPDATE source_documents SET published_at = $2
		WHERE source_type = 'news' AND url = $1 AND published_at IS NULL`, url, publishedAt); err != nil {
		return 0, fmt.Errorf("dating the document of %s: %w", url, err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("dating the news page %s: %w", url, err)
	}
	return tag.RowsAffected(), nil
}

// ClearLateNewsPageDates removes the dates of the news pages dated later
// than the end of the search window that found them, by more than slack: the
// page's update or the day it was served, not its publication (audit
// 2026-11-04c). The fact sources dated so and the documents go undated, for
// ListUndatedNewsPages to date again. With apply false it only counts.
// Returns the pages and the fact sources concerned.
func (s *PostgresStorer) ClearLateNewsPageDates(ctx context.Context, slack time.Duration, apply bool) (pages, sources int64, err error) {
	const late = `sd.source_type = 'news' AND sd.metadata ? 'search_to'
		AND sd.published_at > (sd.metadata->>'search_to')::timestamptz + $1::interval`
	interval := fmt.Sprintf("%d seconds", int64(slack.Seconds()))
	if err := s.pool.QueryRow(ctx, `
		SELECT count(DISTINCT sd.url), count(fs.id)
		FROM source_documents sd
		LEFT JOIN fact_sources fs ON fs.source_type = 'news' AND fs.source_url = sd.url
			AND fs.published_at > (sd.metadata->>'search_to')::timestamptz + $1::interval
		WHERE `+late, interval).Scan(&pages, &sources); err != nil {
		return 0, 0, fmt.Errorf("counting the late news page dates: %w", err)
	}
	if !apply {
		return pages, sources, nil
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("clearing the late news page dates: %w", err)
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `
		UPDATE fact_sources fs SET published_at = NULL
		FROM source_documents sd
		WHERE fs.source_type = 'news' AND fs.source_url = sd.url
			AND fs.published_at > (sd.metadata->>'search_to')::timestamptz + $1::interval
			AND `+late, interval); err != nil {
		return 0, 0, fmt.Errorf("clearing the late fact source dates: %w", err)
	}
	if _, err := tx.Exec(ctx, `UPDATE source_documents sd SET published_at = NULL WHERE `+late, interval); err != nil {
		return 0, 0, fmt.Errorf("clearing the late document dates: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("clearing the late news page dates: %w", err)
	}
	return pages, sources, nil
}
