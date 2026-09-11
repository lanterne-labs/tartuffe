package storage

import (
	"context"
	"fmt"

	"github.com/jackc/pgx/v5"
	"github.com/pgvector/pgvector-go"
	"github.com/rs/zerolog/log"
)

// Meta-facts aggregate several near-identical member facts of one person under
// a single LLM-synthesized fact. The meta is a normal facts row (is_meta=true)
// so it participates in KNN pairing and contradiction checks; members carry a
// 'duplicate' relation to it (general = meta), which excludes them from every
// active-fact query and lets RepointFactDuplicate move their contradictions
// onto the meta. Raw member facts (text, sources, quotes) are never deleted.

// CreateMetaFact inserts the meta fact row, a synthetic 'meta' source, and the
// person mention (facts link to persons only via fact_sources+fact_mentions).
// embeddingText/embedding come from a representative member: folding requires
// the members to state the same claim, so the member's stance-blind embedding
// IS the meta's embedding.
func (s *PostgresStorer) CreateMetaFact(ctx context.Context, personID int64, text, embeddingText string, memberEmbedding []float32, lang, canonicalVerb string) (int64, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("begin meta-fact tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var embedding any
	if len(memberEmbedding) > 0 {
		embedding = pgvector.NewVector(memberEmbedding)
	}
	var metaID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO facts (fact_text, embedding_text, embedding, canonical_verb, lang, is_meta)
		VALUES ($1, $2, $3, NULLIF($4, ''), $5, true)
		RETURNING id`,
		text, embeddingText, embedding, canonicalVerb, lang,
	).Scan(&metaID)
	if err != nil {
		return 0, fmt.Errorf("insert meta fact: %w", err)
	}

	var sourceID int64
	err = tx.QueryRow(ctx, `
		INSERT INTO fact_sources (fact_id, source_url, source_type)
		VALUES ($1::bigint, 'meta://fact/' || $1::bigint::text, 'meta')
		RETURNING id`, metaID,
	).Scan(&sourceID)
	if err != nil {
		return 0, fmt.Errorf("insert meta fact source: %w", err)
	}

	if _, err := tx.Exec(ctx, `
		INSERT INTO fact_mentions (fact_source_id, person_id) VALUES ($1, $2)`,
		sourceID, personID); err != nil {
		return 0, fmt.Errorf("insert meta fact mention: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("commit meta fact: %w", err)
	}
	return metaID, nil
}

// UpdateMetaFactText replaces the meta's synthesized text after it absorbed a
// new member, and RE-OPENS its contradiction checks: the pair-check verdicts
// were rendered against the old text, so they are deleted and the nightly
// detector re-judges the meta against its neighbors.
func (s *PostgresStorer) UpdateMetaFactText(ctx context.Context, metaID int64, text string) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin meta-update tx: %w", err)
	}
	defer tx.Rollback(ctx)

	tag, err := tx.Exec(ctx,
		`UPDATE facts SET fact_text = $2, fact_text_en = NULL WHERE id = $1 AND is_meta`,
		metaID, text)
	if err != nil {
		return fmt.Errorf("update meta fact %d: %w", metaID, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("fact %d is not a meta fact", metaID)
	}

	if _, err := tx.Exec(ctx,
		`DELETE FROM fact_pair_checks WHERE fact_a_id = $1 OR fact_b_id = $1`,
		metaID); err != nil {
		return fmt.Errorf("re-open pair checks for meta %d: %w", metaID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit meta update: %w", err)
	}
	log.Debug().Int64("meta_fact_id", metaID).Msg("Meta fact text updated; pair checks re-opened")
	return nil
}

// GetMetaForFact resolves the meta a fact belongs to. Returns (metaID, true)
// when factID is a member of a meta, or is itself a meta; (0, false) otherwise.
func (s *PostgresStorer) GetMetaForFact(ctx context.Context, factID int64) (int64, bool, error) {
	var metaID int64
	err := s.pool.QueryRow(ctx, `
		SELECT f.id FROM facts f
		WHERE f.is_meta AND (
			f.id = $1
			OR EXISTS (
				SELECT 1 FROM fact_relations fr
				WHERE fr.general_fact_id = f.id
				  AND fr.specific_fact_id = $1
				  AND fr.relation_type = 'duplicate'
			)
		)
		LIMIT 1`, factID).Scan(&metaID)
	if err == pgx.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("resolve meta for fact %d: %w", factID, err)
	}
	return metaID, true, nil
}

// GetMetaMemberTexts returns the member fact texts of a meta, oldest first.
func (s *PostgresStorer) GetMetaMemberTexts(ctx context.Context, metaID int64) ([]string, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT f.fact_text FROM fact_relations fr
		JOIN facts f ON f.id = fr.specific_fact_id
		WHERE fr.general_fact_id = $1 AND fr.relation_type = 'duplicate'
		ORDER BY f.id`, metaID)
	if err != nil {
		return nil, fmt.Errorf("load meta %d member texts: %w", metaID, err)
	}
	defer rows.Close()
	var texts []string
	for rows.Next() {
		var t string
		if err := rows.Scan(&t); err != nil {
			return nil, fmt.Errorf("scan member text: %w", err)
		}
		texts = append(texts, t)
	}
	return texts, rows.Err()
}

// GetFactEmbeddingMeta returns the embedding_text, lang and canonical_verb of a
// fact — what CreateMetaFact needs from a representative member during backfill.
func (s *PostgresStorer) GetFactEmbeddingMeta(ctx context.Context, factID int64) (embeddingText, lang, verb string, err error) {
	err = s.pool.QueryRow(ctx, `
		SELECT COALESCE(embedding_text, fact_text), COALESCE(lang, 'fr'), COALESCE(canonical_verb, '')
		FROM facts WHERE id = $1`, factID).Scan(&embeddingText, &lang, &verb)
	if err != nil {
		return "", "", "", fmt.Errorf("load embedding meta for fact %d: %w", factID, err)
	}
	return embeddingText, lang, verb, nil
}

// IsMetaFact reports whether a fact is a meta-fact.
func (s *PostgresStorer) IsMetaFact(ctx context.Context, factID int64) (bool, error) {
	var isMeta bool
	if err := s.pool.QueryRow(ctx, `SELECT is_meta FROM facts WHERE id = $1`, factID).Scan(&isMeta); err != nil {
		return false, fmt.Errorf("check is_meta for fact %d: %w", factID, err)
	}
	return isMeta, nil
}

// RefreshPersonFactCount recomputes persons.fact_count (deduped) outside a
// caller-owned transaction — used after fold operations mark new duplicates.
func (s *PostgresStorer) RefreshPersonFactCount(ctx context.Context, personID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin fact-count refresh tx: %w", err)
	}
	defer tx.Rollback(ctx)
	if err := s.refreshPersonFactCount(ctx, tx, personID); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// GetFactStanceID returns the stance a fact belongs to (0, false if none).
// CountFactMentionPersons returns how many distinct persons carry the fact via
// fact_mentions. Used by the fold path to refuse folding shared (co-actor)
// facts: marking a shared vote fact as a duplicate member would silently remove
// it from every voter's active-fact set, not just the subject's.
func (s *PostgresStorer) CountFactMentionPersons(ctx context.Context, factID int64) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(DISTINCT fm.person_id)
		FROM fact_mentions fm
		JOIN fact_sources fs ON fs.id = fm.fact_source_id
		WHERE fs.fact_id = $1`, factID).Scan(&n)
	if err != nil {
		return 0, fmt.Errorf("count mention persons for fact %d: %w", factID, err)
	}
	return n, nil
}

func (s *PostgresStorer) GetFactStanceID(ctx context.Context, personID int64, factID int64) (int64, bool, error) {
	// Person-scoped: a fact may be a member of many stances (one per person who
	// carries it — shared vote facts), so an unscoped lookup would return an
	// arbitrary person's stance. Per person a fact belongs to at most one stance.
	var stanceID int64
	err := s.pool.QueryRow(ctx,
		`SELECT sdf.stance_document_id
		 FROM stance_document_facts sdf
		 JOIN stance_documents sd ON sd.id = sdf.stance_document_id
		 WHERE sdf.fact_id = $2 AND sd.person_id = $1
		 LIMIT 1`,
		personID, factID).Scan(&stanceID)
	if err == pgx.ErrNoRows {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("resolve stance for fact %d (person %d): %w", factID, personID, err)
	}
	return stanceID, true, nil
}
