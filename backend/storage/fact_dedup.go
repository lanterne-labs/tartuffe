package storage

import (
	"context"

	"digital-mirror/backend/core"
	"fmt"

	pgvector "github.com/pgvector/pgvector-go"
	"github.com/rs/zerolog/log"
)

// GetFactsForDeduplication retrieves all facts for a person with their embeddings.
// Includes whether each fact is a duplicate (specific side of a 'duplicate' relation).
func (s *PostgresStorer) GetFactsForDeduplication(ctx context.Context, personID int64) ([]core.FactForDedup, error) {
	query := `
		SELECT DISTINCT f.id, f.fact_text, COALESCE(f.embedding_text, ''),
			   COALESCE(f.canonical_verb, ''),
			   COALESCE(fs.original_quote, ''), f.embedding,
			   NOT EXISTS (
			       SELECT 1 FROM fact_relations fr
			       WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
			   ) AS is_active
		FROM facts f
		JOIN fact_sources fs ON fs.fact_id = f.id
		JOIN fact_mentions fm ON fm.fact_source_id = fs.id
		WHERE fm.person_id = $1
		ORDER BY f.id`

	rows, err := s.pool.Query(ctx, query, personID)
	if err != nil {
		return nil, fmt.Errorf("failed to query facts for deduplication: %w", err)
	}
	defer rows.Close()

	var results []core.FactForDedup
	for rows.Next() {
		var f core.FactForDedup
		var emb pgvector.Vector
		var isActive bool
		err := rows.Scan(&f.ID, &f.NormalizedFactText, &f.EmbeddingText,
			&f.CanonicalVerb, &f.OriginalQuote, &emb, &isActive)
		if err != nil {
			return nil, fmt.Errorf("failed to scan fact for deduplication: %w", err)
		}
		f.Embedding = emb.Slice()
		f.IsCanonical = isActive // Reuse field: true means active (not a duplicate)
		f.CanonicalFactID = nil  // No longer used
		results = append(results, f)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating facts for deduplication: %w", rows.Err())
	}

	return results, nil
}

// FindSimilarFactsForDedup finds active (non-duplicate) facts similar to a given embedding.
// Returns facts within the distance threshold, excluding the specified fact ID,
// with the canonical verb and embedding text the dedup tiers compare.
func (s *PostgresStorer) FindSimilarFactsForDedup(ctx context.Context, personIDs []int64, embedding []float32, excludeFactID int64, distanceThreshold float64) ([]core.SimilarFactResult, error) {
	query := `
		WITH PersonFacts AS (
			SELECT DISTINCT fs.fact_id
			FROM fact_mentions fm
			JOIN fact_sources fs ON fm.fact_source_id = fs.id
			WHERE fm.person_id = ANY($1)
		)
		SELECT f.id, f.fact_text, COALESCE(f.canonical_verb, ''), COALESCE(f.embedding_text, ''),
		       f.embedding <=> $2 AS distance
		FROM facts f
		JOIN PersonFacts pf ON f.id = pf.fact_id
		WHERE f.id <> $3
		  AND f.embedding <=> $2 <= $4
		  AND NOT EXISTS (
		      SELECT 1 FROM fact_relations fr
		      WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
		  )
		ORDER BY distance
		LIMIT 20`

	rows, err := s.pool.Query(ctx, query,
		personIDs,
		pgvector.NewVector(embedding),
		excludeFactID,
		distanceThreshold,
	)
	if err != nil {
		return nil, fmt.Errorf("failed to find similar facts for dedup: %w", err)
	}
	defer rows.Close()

	var results []core.SimilarFactResult
	for rows.Next() {
		var sf core.SimilarFactResult
		if err := rows.Scan(&sf.ID, &sf.Text, &sf.CanonicalVerb, &sf.EmbeddingText, &sf.Distance); err != nil {
			return nil, fmt.Errorf("failed to scan similar fact: %w", err)
		}
		sf.Similarity = 1.0 - sf.Distance
		results = append(results, sf)
	}

	return results, rows.Err()
}

// MarkFactAsDuplicate marks a fact as a duplicate of a canonical (general) fact
// by inserting a 'duplicate' relation into fact_relations.
func (s *PostgresStorer) MarkFactAsDuplicate(ctx context.Context, duplicateID int64, canonicalID int64) error {
	query := `
		INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type, reason)
		VALUES ($1, $2, 'duplicate', 'Marked as duplicate')
		ON CONFLICT (general_fact_id, specific_fact_id) DO UPDATE
		SET relation_type = 'duplicate',
		    reason = EXCLUDED.reason`

	_, err := s.pool.Exec(ctx, query, canonicalID, duplicateID)
	if err != nil {
		return fmt.Errorf("failed to mark fact %d as duplicate of %d: %w", duplicateID, canonicalID, err)
	}

	log.Info().
		Int64("duplicate_id", duplicateID).
		Int64("canonical_id", canonicalID).
		Msg("Marked fact as duplicate")

	return nil
}

// MarkFactDuplicate records that two facts are duplicates by inserting a
// 'duplicate' fact_relations row (keeping the lower id as the canonical/general
// fact, hiding the higher id as the specific one — it is then excluded from
// active-fact queries, dedup candidates, and future contradiction detection).
// Non-destructive and reversible (delete the row to un-hide). Idempotent via the
// uq_fact_relation constraint. Used by the contradiction-detection dedup net.
func (s *PostgresStorer) MarkFactDuplicate(ctx context.Context, factID1, factID2 int64, reason string) error {
	if factID1 == factID2 {
		return fmt.Errorf("cannot mark a fact as a duplicate of itself: %d", factID1)
	}
	general, specific := factID1, factID2
	if general > specific {
		general, specific = specific, general
	}
	_, err := s.pool.Exec(ctx,
		`INSERT INTO fact_relations (general_fact_id, specific_fact_id, relation_type, reason)
		 VALUES ($1, $2, 'duplicate', NULLIF($3, ''))
		 ON CONFLICT (general_fact_id, specific_fact_id) DO NOTHING`,
		general, specific, reason)
	if err != nil {
		return fmt.Errorf("mark fact %d duplicate of %d: %w", specific, general, err)
	}
	return nil
}

// FactDuplicatePair is a (canonical, duplicate) fact pair from a 'duplicate'
// fact_relations row whose duplicate side still anchors live contradictions.
type FactDuplicatePair struct {
	General  int64 // canonical fact kept
	Specific int64 // duplicate fact (hidden); its contradictions need repointing
}

// GetDuplicatesNeedingRepoint returns 'duplicate' relations whose specific
// (hidden) fact is still referenced by at least one contradiction — the only
// ones RepointFactDuplicate has work to do on.
func (s *PostgresStorer) GetDuplicatesNeedingRepoint(ctx context.Context) ([]FactDuplicatePair, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT fr.general_fact_id, fr.specific_fact_id
		 FROM fact_relations fr
		 WHERE fr.relation_type = 'duplicate'
		   AND EXISTS (SELECT 1 FROM contradictions c
		               WHERE c.fact_a_id = fr.specific_fact_id OR c.fact_b_id = fr.specific_fact_id)`)
	if err != nil {
		return nil, fmt.Errorf("query duplicates needing repoint: %w", err)
	}
	defer rows.Close()
	var out []FactDuplicatePair
	for rows.Next() {
		var p FactDuplicatePair
		if err := rows.Scan(&p.General, &p.Specific); err != nil {
			return nil, fmt.Errorf("scan duplicate pair: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}

// RepointFactDuplicate moves contradictions that reference the `specific`
// (hidden duplicate) fact onto the `general` (canonical) fact, then recomputes
// affected persons' honesty scores. For each such contradiction {specific, X}:
//   - X == general  → it is a fact-vs-its-own-duplicate self-pair: delete it.
//   - {general, X} already recorded for that person → the repointed pair would
//     duplicate it: delete the {specific, X} row.
//   - otherwise → repoint the row to {general, X} (ordered, fact_a_id<fact_b_id).
//
// Stance memberships of `specific` are intentionally left untouched: the fact is
// already hidden from active queries, and keeping its stance row preserves the
// every-fact-has-a-stance invariant. Returns (repointed, dropped).
func (s *PostgresStorer) RepointFactDuplicate(ctx context.Context, general, specific int64) (repointed int, dropped int, err error) {
	if general == specific {
		return 0, 0, fmt.Errorf("general and specific are the same fact: %d", general)
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("begin repoint tx: %w", err)
	}
	defer tx.Rollback(ctx)

	rows, err := tx.Query(ctx,
		`SELECT id, person_id, fact_a_id, fact_b_id FROM contradictions
		 WHERE fact_a_id = $1 OR fact_b_id = $1`, specific)
	if err != nil {
		return 0, 0, fmt.Errorf("load contradictions for fact %d: %w", specific, err)
	}
	type crow struct{ id, person, a, b int64 }
	var crows []crow
	for rows.Next() {
		var c crow
		if err := rows.Scan(&c.id, &c.person, &c.a, &c.b); err != nil {
			rows.Close()
			return 0, 0, fmt.Errorf("scan contradiction: %w", err)
		}
		crows = append(crows, c)
	}
	rows.Close()
	if err := rows.Err(); err != nil {
		return 0, 0, fmt.Errorf("iterate contradictions: %w", err)
	}

	affected := make(map[int64]struct{})
	for _, c := range crows {
		other := c.a
		if c.a == specific {
			other = c.b
		}
		affected[c.person] = struct{}{}

		if other == general {
			if _, e := tx.Exec(ctx, `DELETE FROM contradictions WHERE id = $1`, c.id); e != nil {
				return 0, 0, fmt.Errorf("delete self-pair contradiction %d: %w", c.id, e)
			}
			dropped++
			continue
		}

		na, nb := general, other
		if na > nb {
			na, nb = nb, na
		}
		var exists bool
		if e := tx.QueryRow(ctx,
			`SELECT EXISTS(SELECT 1 FROM contradictions WHERE person_id=$1 AND fact_a_id=$2 AND fact_b_id=$3 AND id<>$4)`,
			c.person, na, nb, c.id).Scan(&exists); e != nil {
			return 0, 0, fmt.Errorf("check existing contradiction: %w", e)
		}
		if exists {
			if _, e := tx.Exec(ctx, `DELETE FROM contradictions WHERE id = $1`, c.id); e != nil {
				return 0, 0, fmt.Errorf("delete redundant contradiction %d: %w", c.id, e)
			}
			dropped++
		} else {
			if _, e := tx.Exec(ctx,
				`UPDATE contradictions SET fact_a_id=$2, fact_b_id=$3 WHERE id=$1`, c.id, na, nb); e != nil {
				return 0, 0, fmt.Errorf("repoint contradiction %d: %w", c.id, e)
			}
			repointed++
		}
	}

	for p := range affected {
		if e := s.recomputeHonestyScoreAndEmitEvents(ctx, tx, p, 0); e != nil {
			return 0, 0, fmt.Errorf("recompute score for person %d: %w", p, e)
		}
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("commit repoint: %w", err)
	}
	return repointed, dropped, nil
}
