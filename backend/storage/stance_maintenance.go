package storage

import (
	"context"
	"digital-mirror/backend/core"
	"fmt"

	pgvector "github.com/pgvector/pgvector-go"
)

// GetFactsForStanceBackfill returns every non-duplicate fact mentioning the
// person that carries an embedding, oldest id first. Facts without an
// embedding are skipped (they cannot be routed).
func (s *PostgresStorer) GetFactsForStanceBackfill(ctx context.Context, personID int64) ([]core.StanceRoutingFact, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT f.id, f.fact_text, COALESCE(f.embedding_text, ''), COALESCE(f.canonical_verb, ''), f.embedding, MIN(fs.published_at)
		FROM facts f
		JOIN fact_sources fs ON f.id = fs.fact_id
		JOIN fact_mentions fm ON fm.fact_source_id = fs.id
		WHERE fm.person_id = $1
		AND NOT EXISTS (
			SELECT 1 FROM fact_relations fr
			WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
		)
		GROUP BY f.id
		ORDER BY f.id`, personID)
	if err != nil {
		return nil, fmt.Errorf("querying facts: %w", err)
	}
	defer rows.Close()

	var facts []core.StanceRoutingFact
	for rows.Next() {
		var f core.StanceRoutingFact
		var vec pgvector.Vector
		if err := rows.Scan(&f.ID, &f.FactText, &f.EmbeddingText, &f.CanonicalVerb, &vec, &f.PublishedAt); err != nil {
			return nil, fmt.Errorf("scanning fact: %w", err)
		}
		f.Embedding = vec.Slice()
		if len(f.Embedding) == 0 {
			continue
		}
		facts = append(facts, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating facts: %w", err)
	}
	return facts, nil
}
