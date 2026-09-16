package storage

import (
	"context"
	"fmt"
)

// GetPersonFactCountsBySourceTypes returns, per person id, how many active
// facts they carry from sources of the given types.
func (s *PostgresStorer) GetPersonFactCountsBySourceTypes(ctx context.Context, sourceTypes []string) (map[int64]int, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT fm.person_id, COUNT(DISTINCT fs.fact_id)
		FROM fact_sources fs
		JOIN fact_mentions fm ON fm.fact_source_id = fs.id
		WHERE fs.source_type = ANY($1)
		  AND NOT EXISTS (
		      SELECT 1 FROM fact_relations fr
		      WHERE fr.specific_fact_id = fs.fact_id AND fr.relation_type = 'duplicate'
		  )
		GROUP BY fm.person_id`, sourceTypes)
	if err != nil {
		return nil, fmt.Errorf("querying fact counts by source type: %w", err)
	}
	defer rows.Close()
	out := map[int64]int{}
	for rows.Next() {
		var id int64
		var n int
		if err := rows.Scan(&id, &n); err != nil {
			return nil, fmt.Errorf("scanning fact count by source type: %w", err)
		}
		out[id] = n
	}
	return out, rows.Err()
}
