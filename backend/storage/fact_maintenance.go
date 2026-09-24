package storage

import (
	"context"
	"digital-mirror/backend/core"
	"fmt"

	"github.com/jackc/pgx/v5"
	pgvector "github.com/pgvector/pgvector-go"
	"github.com/rs/zerolog/log"
)

// Fact maintenance queries used by the sweeps in backend/core (fold, verb
// backfill, embedding regeneration) and by the duplicate-repoint pass.

// RepointDuplicates finds every 'duplicate' fact that still anchors a
// contradiction and (when apply) repoints those contradictions onto the
// canonical fact. Returns (duplicatePairsProcessed, repointed, dropped). Shared
// by `dm facts repoint-duplicates` and the daily-pipeline cleanup step. A
// per-pair failure is logged and skipped so one bad pair never aborts the sweep.
func (s *PostgresStorer) RepointDuplicates(ctx context.Context, apply bool) (processed, repointed, dropped int, err error) {
	pairs, err := s.GetDuplicatesNeedingRepoint(ctx)
	if err != nil {
		return 0, 0, 0, fmt.Errorf("failed to list duplicates needing repoint: %w", err)
	}
	if len(pairs) == 0 {
		log.Info().Msg("Repoint duplicates: no duplicate facts anchor contradictions, nothing to do")
		return 0, 0, 0, nil
	}
	if !apply {
		return len(pairs), 0, 0, nil
	}

	for _, p := range pairs {
		r, d, rErr := s.RepointFactDuplicate(ctx, p.General, p.Specific)
		if rErr != nil {
			log.Error().Err(rErr).Int64("general", p.General).Int64("specific", p.Specific).
				Msg("Repoint duplicates: failed for pair, skipping")
			continue
		}
		processed++
		repointed += r
		dropped += d
		log.Info().Int64("general", p.General).Int64("specific", p.Specific).
			Int("repointed", r).Int("dropped", d).
			Msg("Repointed contradictions off duplicate fact")
	}
	return processed, repointed, dropped, nil
}

// GetFoldFactsForPerson returns the person's active (non-duplicate) facts
// with their embeddings, in the shape the fold pass needs.
func (s *PostgresStorer) GetFoldFactsForPerson(ctx context.Context, personID int64) ([]core.FactForFold, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT f.id, f.fact_text, f.embedding
		FROM facts f
		WHERE EXISTS (
		      SELECT 1 FROM fact_sources fs
		      JOIN fact_mentions fm ON fm.fact_source_id = fs.id
		      WHERE fs.fact_id = f.id AND fm.person_id = $1
		  )
		  AND NOT EXISTS (
		      SELECT 1 FROM fact_relations fr
		      WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
		  )
		ORDER BY f.id`, personID)
	if err != nil {
		return nil, fmt.Errorf("loading fold facts for person %d: %w", personID, err)
	}
	defer rows.Close()
	var out []core.FactForFold
	for rows.Next() {
		var f core.FactForFold
		var vec pgvector.Vector
		if err := rows.Scan(&f.ID, &f.Text, &vec); err != nil {
			return nil, fmt.Errorf("scanning fold fact: %w", err)
		}
		f.Embedding = vec.Slice()
		out = append(out, f)
	}
	return out, rows.Err()
}

// ListContradictionFactPairs returns the (fact_a, fact_b) pairs of every
// recorded contradiction of the person.
func (s *PostgresStorer) ListContradictionFactPairs(ctx context.Context, personID int64) ([]core.ContradictionFactPair, error) {
	rows, err := s.ListContradictions(ctx, &personID, false, 0)
	if err != nil {
		return nil, err
	}
	out := make([]core.ContradictionFactPair, len(rows))
	for i, c := range rows {
		out[i] = core.ContradictionFactPair{ID: c.ID, FactAID: c.FactAID, FactBID: c.FactBID}
	}
	return out, nil
}

// ListFactsWithoutCanonicalVerb returns every fact whose canonical_verb is
// unset, with its current embedding_text, oldest id first.
func (s *PostgresStorer) ListFactsWithoutCanonicalVerb(ctx context.Context) ([]core.FactEmbeddingText, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, COALESCE(embedding_text, '')
		FROM facts
		WHERE canonical_verb IS NULL OR canonical_verb = ''
		ORDER BY id`)
	if err != nil {
		return nil, fmt.Errorf("failed to query facts: %w", err)
	}
	defer rows.Close()
	return scanFactEmbeddingTexts(rows)
}

// UpdateFactVerbAndEmbedding sets a fact's canonical verb, its verb-free
// embedding text and the embedding regenerated from that text.
func (s *PostgresStorer) UpdateFactVerbAndEmbedding(ctx context.Context, factID int64, verb, embeddingText string, embedding []float32) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE facts
		SET canonical_verb = $1, embedding_text = $2, embedding = $3
		WHERE id = $4`,
		verb, embeddingText, pgvector.NewVector(embedding), factID)
	if err != nil {
		return fmt.Errorf("failed to update fact %d: %w", factID, err)
	}
	return nil
}

// CountFactsWithoutEmbedding counts the facts whose embedding is NULL.
func (s *PostgresStorer) CountFactsWithoutEmbedding(ctx context.Context) (int64, error) {
	var total int64
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM facts WHERE embedding IS NULL`).Scan(&total); err != nil {
		return 0, fmt.Errorf("count facts to regenerate: %w", err)
	}
	return total, nil
}

// ListFactsWithoutEmbedding returns up to limit facts whose embedding is NULL,
// oldest id first, with the text to embed (embedding_text, else fact_text —
// the same source string the ingestion pipeline uses).
func (s *PostgresStorer) ListFactsWithoutEmbedding(ctx context.Context, limit int) ([]core.FactEmbeddingText, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT id, COALESCE(NULLIF(embedding_text, ''), fact_text)
		FROM facts
		WHERE embedding IS NULL
		ORDER BY id
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("query batch: %w", err)
	}
	defer rows.Close()
	return scanFactEmbeddingTexts(rows)
}

func scanFactEmbeddingTexts(rows pgx.Rows) ([]core.FactEmbeddingText, error) {
	var out []core.FactEmbeddingText
	for rows.Next() {
		var f core.FactEmbeddingText
		if err := rows.Scan(&f.ID, &f.EmbeddingText); err != nil {
			return nil, fmt.Errorf("scan row: %w", err)
		}
		out = append(out, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate rows: %w", err)
	}
	return out, nil
}

// CountEmbeddingInvalidationPlan returns the row counts an embedding
// invalidation would touch: all facts, facts with an embedding, and stance
// documents.
func (s *PostgresStorer) CountEmbeddingInvalidationPlan(ctx context.Context) (facts, embedded, stances int64, err error) {
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM facts`).Scan(&facts); err != nil {
		return 0, 0, 0, fmt.Errorf("count facts: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM facts WHERE embedding IS NOT NULL`).Scan(&embedded); err != nil {
		return 0, 0, 0, fmt.Errorf("count embedded facts: %w", err)
	}
	if err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM stance_documents`).Scan(&stances); err != nil {
		return 0, 0, 0, fmt.Errorf("count stance documents: %w", err)
	}
	return facts, embedded, stances, nil
}

// InvalidateEmbeddings sets facts.embedding = NULL for every row and deletes
// every stance document, in one transaction. Used when swapping the embedding
// model: the stored vectors live in a different vector space than the new
// model. Returns the number of embeddings nulled and stance documents deleted.
func (s *PostgresStorer) InvalidateEmbeddings(ctx context.Context) (nulled, stancesDeleted int64, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, 0, fmt.Errorf("begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	updateRes, err := tx.Exec(ctx, `UPDATE facts SET embedding = NULL WHERE embedding IS NOT NULL`)
	if err != nil {
		return 0, 0, fmt.Errorf("nulling fact embeddings: %w", err)
	}
	deleteRes, err := tx.Exec(ctx, `DELETE FROM stance_documents`)
	if err != nil {
		return 0, 0, fmt.Errorf("deleting stance_documents: %w", err)
	}
	if err := tx.Commit(ctx); err != nil {
		return 0, 0, fmt.Errorf("commit transaction: %w", err)
	}
	return updateRes.RowsAffected(), deleteRes.RowsAffected(), nil
}

// ListTextSimilarFactPairs returns the pairs among factIDs whose fact_text
// trigram similarity is at least floor, as [smaller id, larger id]. The
// pg_trgm similarity runs in SQL over the given ids only.
func (s *PostgresStorer) ListTextSimilarFactPairs(ctx context.Context, factIDs []int64, floor float64) ([][2]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, b.id
		FROM facts a
		JOIN facts b ON b.id > a.id AND b.id = ANY($1)
		WHERE a.id = ANY($1) AND similarity(a.fact_text, b.fact_text) >= $2`, factIDs, floor)
	if err != nil {
		return nil, fmt.Errorf("listing text-similar fact pairs: %w", err)
	}
	defer rows.Close()
	var out [][2]int64
	for rows.Next() {
		var p [2]int64
		if err := rows.Scan(&p[0], &p[1]); err != nil {
			return nil, fmt.Errorf("scanning text-similar pair: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
