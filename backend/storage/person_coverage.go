package storage

import (
	"context"
	"fmt"
	"time"
)

// CoverageYear is one year of a person's record: how many of their facts were
// stated in it.
type CoverageYear struct {
	Year  int `json:"year"`
	Facts int `json:"facts"`
}

// Coverage is the histogram of a person's facts by year of statement, every
// year from the first to the last (the empty ones at 0), so the reader sees
// where the record is thin and where it is thick.
type Coverage struct {
	Years []CoverageYear `json:"years"`
	// Undated counts the facts with neither a statement date nor a dated
	// source: they are in the record but on no year.
	Undated int `json:"undated"`
	// Total is the number of facts counted, dated or not: the person's
	// fact_count.
	Total int `json:"total"`
}

// GetPersonCoverage returns the histogram of a person's facts by year. A fact
// is dated by its statement date, else by the publication of its earliest
// source (the same date the person's collected period and the honesty
// timeline use). Duplicates are left out, as in fact_count.
func (s *PostgresStorer) GetPersonCoverage(ctx context.Context, personID int64) (*Coverage, error) {
	rows, err := s.pool.Query(ctx, `
		WITH person_facts AS (
			SELECT DISTINCT f.id, f.statement_date
			FROM facts f
			JOIN fact_sources fs ON fs.fact_id = f.id
			JOIN fact_mentions fm ON fm.fact_source_id = fs.id
			WHERE fm.person_id = $1
			  AND NOT EXISTS (
				SELECT 1 FROM fact_relations fr
				WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
			  )
		),
		dated AS (
			SELECT pf.id,
			       COALESCE(pf.statement_date::timestamp,
			                (SELECT MIN(fs.published_at) FROM fact_sources fs WHERE fs.fact_id = pf.id) AT TIME ZONE 'UTC') AS at
			FROM person_facts pf
		)
		SELECT EXTRACT(YEAR FROM at)::int AS year, COUNT(*)::int
		FROM dated
		GROUP BY year
		ORDER BY year NULLS LAST`, personID)
	if err != nil {
		return nil, fmt.Errorf("query the coverage of person %d: %w", personID, err)
	}
	defer rows.Close()

	cov := &Coverage{Years: []CoverageYear{}}
	byYear := map[int]int{}
	first, last := 0, 0
	for rows.Next() {
		var year *int
		var n int
		if err := rows.Scan(&year, &n); err != nil {
			return nil, fmt.Errorf("scan the coverage of person %d: %w", personID, err)
		}
		cov.Total += n
		if year == nil {
			cov.Undated = n
			continue
		}
		byYear[*year] = n
		if first == 0 || *year < first {
			first = *year
		}
		if *year > last {
			last = *year
		}
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("read the coverage of person %d: %w", personID, err)
	}
	if first == 0 {
		return cov, nil
	}
	// The histogram ends this year at the earliest: a record that stops
	// earlier shows the empty years since.
	if now := time.Now().UTC().Year(); now > last {
		last = now
	}
	for y := first; y <= last; y++ {
		cov.Years = append(cov.Years, CoverageYear{Year: y, Facts: byYear[y]})
	}
	return cov, nil
}
