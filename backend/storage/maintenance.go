package storage

import (
	"context"
	"fmt"
	"time"
)

// PersonsWithFactsSince lists the persons with a fact created after since, by
// name: the persons the maintenance pass of the worker judges again. A zero
// since lists every person with a fact.
func (s *PostgresStorer) PersonsWithFactsSince(ctx context.Context, since time.Time) ([]PersonInfo, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.id, p.name, COALESCE(p.entity_type, 'person')
		FROM persons p
		WHERE EXISTS (
			SELECT 1 FROM fact_mentions fm
			JOIN fact_sources fs ON fs.id = fm.fact_source_id
			JOIN facts f ON f.id = fs.fact_id
			WHERE fm.person_id = p.id AND f.created_at > $1)
		ORDER BY p.name`, since)
	if err != nil {
		return nil, fmt.Errorf("persons with facts since %s: %w", since.Format(time.RFC3339), err)
	}
	defer rows.Close()
	var out []PersonInfo
	for rows.Next() {
		var p PersonInfo
		if err := rows.Scan(&p.ID, &p.Name, &p.EntityType); err != nil {
			return nil, fmt.Errorf("persons with facts since: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// PersonIDsWithContradictionsSince lists the persons with a contradiction
// created after since: the persons whose groups the regroup step rebuilds.
func (s *PostgresStorer) PersonIDsWithContradictionsSince(ctx context.Context, since time.Time) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT DISTINCT person_id FROM contradictions WHERE created_at > $1 ORDER BY person_id`, since)
	if err != nil {
		return nil, fmt.Errorf("persons with contradictions since %s: %w", since.Format(time.RFC3339), err)
	}
	defer rows.Close()
	var out []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("persons with contradictions since: %w", err)
		}
		out = append(out, id)
	}
	return out, rows.Err()
}
