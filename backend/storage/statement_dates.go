package storage

import (
	"context"
	"fmt"
	"time"

	"digital-mirror/backend"
)

// The judges read a fact's statement date (facts.statement_date, its
// precision in statement_date_precision) before its source's date
// (backend/fact_date.go). A query that gives the judges a fact's date
// selects the two columns next to the source's date and scans them with
// statementDate, which returns backend.FactDate of the three.

// statementDateArgs are the insert values of a statement date: the day and
// its precision, or two NULLs.
func statementDateArgs(t *time.Time) (any, any) {
	if t == nil {
		return nil, nil
	}
	p := backend.DatePrecisionOf(*t)
	if p == "" {
		p = backend.DatePrecisionDay
	}
	return time.Date(t.Year(), t.Month(), t.Day(), 0, 0, 0, 0, time.UTC), p
}

// statementDate is the scan target of a statement date and its precision.
type statementDate struct {
	date      *time.Time
	precision *string
}

// of returns the date the judges read: the statement date, else source.
func (d statementDate) of(source *time.Time) *time.Time {
	return backend.FactDate(d.date, d.precision, source)
}

// StatementDateFact is a fact of a candidate whose statement date has not
// been checked yet: the input of the repair (dm facts statement-dates).
type StatementDateFact struct {
	ID          int64
	PersonName  string
	Text        string
	Quote       string
	SourceURL   string
	PublishedAt *time.Time
}

// ListFactsForStatementDate returns up to limit active text facts (news,
// tweets, vie-publique, debates) of the candidates (or of every person when
// candidatesOnly is false) whose statement date was never checked, newest
// first, with the quote and the date of their earliest source.
func (s *PostgresStorer) ListFactsForStatementDate(ctx context.Context, candidatesOnly bool, limit int) ([]StatementDateFact, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT ON (f.id) f.id, p.name, f.fact_text, COALESCE(fs.original_quote, ''), fs.source_url, fs.published_at
		FROM facts f
		JOIN fact_sources fs ON fs.fact_id = f.id
		JOIN fact_mentions fm ON fm.fact_source_id = fs.id
		JOIN persons p ON p.id = fm.person_id
		WHERE f.statement_date_checked_at IS NULL
		  AND fs.source_type IN ('news', 'twitter', 'viepublique', 'assemblee_debat')
		  AND ($1 = false OR `+CandidateSQL+`)
		  AND NOT EXISTS (SELECT 1 FROM fact_relations r WHERE r.relation_type = 'duplicate' AND r.specific_fact_id = f.id)
		ORDER BY f.id DESC, fs.published_at ASC NULLS LAST
		LIMIT $2`, candidatesOnly, limit)
	if err != nil {
		return nil, fmt.Errorf("listing the facts to date: %w", err)
	}
	defer rows.Close()
	var out []StatementDateFact
	for rows.Next() {
		var f StatementDateFact
		if err := rows.Scan(&f.ID, &f.PersonName, &f.Text, &f.Quote, &f.SourceURL, &f.PublishedAt); err != nil {
			return nil, fmt.Errorf("listing the facts to date: %w", err)
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// SetStatementDate records the check of a fact's statement date: the date
// found (nil: the text gives none, and the source's date stands).
func (s *PostgresStorer) SetStatementDate(ctx context.Context, factID int64, date *time.Time) error {
	d, p := statementDateArgs(date)
	_, err := s.pool.Exec(ctx, `
		UPDATE facts SET statement_date = $2, statement_date_precision = $3, statement_date_checked_at = now()
		WHERE id = $1`, factID, d, p)
	if err != nil {
		return fmt.Errorf("setting the statement date of fact %d: %w", factID, err)
	}
	return nil
}
