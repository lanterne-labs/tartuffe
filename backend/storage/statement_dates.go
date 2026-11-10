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

// shownDateSQL is the date the site shows for fact alias f: its statement
// date, else earliest (an SQL expression: the date of its first source). The
// precision of a statement date is the column f.statement_date_precision,
// NULL for a source's date, which is a full date.
func shownDateSQL(f, earliest string) string {
	return fmt.Sprintf("COALESCE(%s.statement_date::timestamp AT TIME ZONE 'UTC', %s)", f, earliest)
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
	// Article is the stored text of the source and ArticleTitle its title
	// (source_documents), empty when the source's page was not kept.
	Article      string
	ArticleTitle string
}

// ListFactsForStatementDate returns up to limit active text facts (news,
// tweets, vie-publique, debates) of the candidates (or of every person when
// candidatesOnly is false) whose statement date was never checked, with the
// quote, the date and the stored text of their earliest source. The facts of
// a contradiction come first (the site shows their dates side by side), then
// the newest.
//
// recheckBefore also gives the facts an earlier run checked without finding
// a date, when their source's text is stored: that run read the fact alone,
// and the article often dates what the fact does not ("lançait-il en 2017").
// A fact checked at or after recheckBefore (this run's start) is not given
// again, nor one the extractor dated at insertion: it read the article.
func (s *PostgresStorer) ListFactsForStatementDate(ctx context.Context, candidatesOnly bool, limit int, recheckBefore *time.Time) ([]StatementDateFact, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.id, t.name, t.fact_text, t.quote, t.source_url, t.published_at, COALESCE(d.content, ''), COALESCE(d.title, '')
		FROM (
			SELECT DISTINCT ON (f.id) f.id, p.name, f.fact_text, COALESCE(fs.original_quote, '') AS quote,
			       fs.source_url, fs.published_at, fs.source_type, f.statement_date_checked_at AS checked_at
			FROM facts f
			JOIN fact_sources fs ON fs.fact_id = f.id
			JOIN fact_mentions fm ON fm.fact_source_id = fs.id
			JOIN persons p ON p.id = fm.person_id
			WHERE (f.statement_date_checked_at IS NULL
			       OR ($3::timestamptz IS NOT NULL AND f.statement_date IS NULL
			           AND f.statement_date_checked_at < $3
			           AND f.statement_date_checked_at > f.created_at + interval '1 hour'))
			  AND fs.source_type IN ('news', 'twitter', 'viepublique', 'assemblee_debat')
			  AND ($1 = false OR `+CandidateSQL+`)
			  AND NOT EXISTS (SELECT 1 FROM fact_relations r WHERE r.relation_type = 'duplicate' AND r.specific_fact_id = f.id)
			ORDER BY f.id DESC, fs.published_at ASC NULLS LAST
		) t
		LEFT JOIN LATERAL (
			SELECT content, title FROM source_documents WHERE url = t.source_url AND content <> '' LIMIT 1
		) d ON true
		WHERE t.checked_at IS NULL OR d.content IS NOT NULL
		ORDER BY EXISTS (SELECT 1 FROM contradictions c WHERE t.id IN (c.fact_a_id, c.fact_b_id)) DESC, t.id DESC
		LIMIT $2`, candidatesOnly, limit, recheckBefore)
	if err != nil {
		return nil, fmt.Errorf("listing the facts to date: %w", err)
	}
	defer rows.Close()
	var out []StatementDateFact
	for rows.Next() {
		var f StatementDateFact
		if err := rows.Scan(&f.ID, &f.PersonName, &f.Text, &f.Quote, &f.SourceURL, &f.PublishedAt, &f.Article, &f.ArticleTitle); err != nil {
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
