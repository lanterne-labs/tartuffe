package storage

import (
	"context"
	"fmt"
	"time"

	"digital-mirror/backend"
	"digital-mirror/backend/core"
	"digital-mirror/backend/statementday"
)

// ListFactsForStatementDay returns up to limit active facts of the
// candidates (or of every person) whose statement date is a year or a month
// and whose day was never searched (again: also those searched without a
// day), with the quote and the date of their earliest source. The facts of a
// contradiction come first (the site shows their dates face to face), then
// the newest.
func (s *PostgresStorer) ListFactsForStatementDay(ctx context.Context, candidatesOnly bool, limit int, again bool) ([]statementday.Pending, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT t.id, t.person_id, t.name, t.fact_text, t.quote, t.published_at, t.statement_date, t.statement_date_precision
		FROM (
			SELECT DISTINCT ON (f.id) f.id, p.id AS person_id, p.name, f.fact_text, COALESCE(fs.original_quote, '') AS quote, fs.published_at,
			       f.statement_date::timestamp AT TIME ZONE 'UTC' AS statement_date, f.statement_date_precision
			FROM facts f
			JOIN fact_sources fs ON fs.fact_id = f.id
			JOIN fact_mentions fm ON fm.fact_source_id = fs.id
			JOIN persons p ON p.id = fm.person_id
			WHERE f.statement_date_precision IN ('year', 'month')
			  AND ($3 OR f.statement_day_searched_at IS NULL)
			  AND ($1 = false OR `+CandidateSQL+`)
			  AND NOT EXISTS (SELECT 1 FROM fact_relations r WHERE r.relation_type = 'duplicate' AND r.specific_fact_id = f.id)
			ORDER BY f.id DESC, fs.published_at ASC NULLS LAST
		) t
		ORDER BY EXISTS (SELECT 1 FROM contradictions c WHERE t.id IN (c.fact_a_id, c.fact_b_id)) DESC, t.id DESC
		LIMIT $2`, candidatesOnly, limit, again)
	if err != nil {
		return nil, fmt.Errorf("listing the facts to find the day of: %w", err)
	}
	defer rows.Close()
	var out []statementday.Pending
	for rows.Next() {
		var p statementday.Pending
		var known time.Time
		var precision string
		if err := rows.Scan(&p.FactID, &p.PersonID, &p.Person, &p.Text, &p.Quote, &p.SourceDate, &known, &precision); err != nil {
			return nil, fmt.Errorf("listing the facts to find the day of: %w", err)
		}
		p.Known = backend.WithDatePrecision(known, precision)
		out = append(out, p)
	}
	return out, rows.Err()
}

// SetStatementDay records the web search of a fact's day: the day found
// becomes its statement date; nil leaves the year or the month it has.
func (s *PostgresStorer) SetStatementDay(ctx context.Context, factID int64, day *time.Time) error {
	var err error
	if day == nil {
		_, err = s.pool.Exec(ctx, `UPDATE facts SET statement_day_searched_at = now() WHERE id = $1`, factID)
	} else {
		d, _ := statementDateArgs(day)
		_, err = s.pool.Exec(ctx, `
			UPDATE facts SET statement_date = $2, statement_date_precision = 'day', statement_day_searched_at = now()
			WHERE id = $1 AND statement_date_precision IN ('year', 'month')`, factID, d)
	}
	if err != nil {
		return fmt.Errorf("setting the statement day of fact %d: %w", factID, err)
	}
	return nil
}

// QueueArticle stores the page that dated a statement of the person as a
// fetched news document, unless a document of that URL exists: the next news
// task of the person extracts its facts (core.ProcessDocuments).
func (s *PostgresStorer) QueueArticle(ctx context.Context, personID int64, person string, page statementday.Page) (bool, error) {
	known, err := s.KnownDocumentURLs(ctx, "news", []string{page.URL})
	if err != nil {
		return false, fmt.Errorf("queueing the dating article %s: %w", page.URL, err)
	}
	if known[page.URL] {
		return false, nil
	}
	_, err = s.SaveFetchedDocuments(ctx, []core.SourceDocument{{
		SourceType:  "news",
		URL:         page.URL,
		PersonID:    personID,
		Title:       page.Title,
		Content:     page.Text,
		PublishedAt: page.Published,
		Metadata:    map[string]any{"target_type": "person", "target_name": person, "found_by": "statementday"},
	}})
	if err != nil {
		return false, fmt.Errorf("queueing the dating article %s: %w", page.URL, err)
	}
	return true, nil
}
