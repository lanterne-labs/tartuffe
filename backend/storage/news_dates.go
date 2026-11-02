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
