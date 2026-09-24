package storage

import (
	"context"
	"fmt"
)

// CountPendingStanceRoutings returns the number of (person, fact) mention
// pairs whose fact is active, has an embedding, and is not yet a member of any
// of that person's stance documents: the gaps the stance fan-out closes
// (mirror-py3), e.g. unfolded group members. The definition matches ListPersonsWithOrphanFacts /
// ListOrphanFactsForPerson exactly — when those queries drain, this count
// reaches zero.
func (s *PostgresStorer) CountPendingStanceRoutings(ctx context.Context) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM (
			SELECT DISTINCT fm.person_id, fs.fact_id
			FROM fact_mentions fm
			JOIN fact_sources fs ON fm.fact_source_id = fs.id
			JOIN facts f ON f.id = fs.fact_id
			WHERE f.embedding IS NOT NULL
			AND NOT EXISTS (
				SELECT 1 FROM fact_relations fr
				WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
			)
			AND NOT EXISTS (
				SELECT 1 FROM stance_document_facts sdf
				JOIN stance_documents sd ON sd.id = sdf.stance_document_id
				WHERE sdf.fact_id = f.id AND sd.person_id = fm.person_id
			)
		) pending`).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("counting pending stance routings: %w", err)
	}
	return n, nil
}
