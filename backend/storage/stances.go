package storage

import (
	"context"

	"digital-mirror/backend/core"
	"fmt"
	"strings"

	"github.com/jackc/pgx/v5"
	pgvector "github.com/pgvector/pgvector-go"
	"github.com/rs/zerolog/log"
)

// InsertStanceDocument inserts a new stance document and returns its ID.
//
// (person_id, cluster_label) is unique, and the topic assigner can legitimately
// decide "new stance" while generating a label the person already has — two
// laws about renewables both yield "énergies renouvelables". That used to blow
// up the whole insert batch with a 23505 (frpolitics backfill, 2026-09-04). On
// conflict the EXISTING stance's id is returned instead: the caller attaches
// the fact to it and the next metadata refresh reconciles summary and counts,
// which is exactly what routing to the existing stance would have done.
func (s *PostgresStorer) InsertStanceDocument(ctx context.Context, doc core.StanceDocument) (int64, error) {
	query := `
		INSERT INTO stance_documents (person_id, cluster_label, label_en, cluster_embedding, summary, summary_en, key_fact_ids, fact_count, last_updated_at)
		VALUES ($1, $2, NULLIF($3,''), $4, $5, NULLIF($6,''), $7, $8, NOW())
		ON CONFLICT (person_id, cluster_label) DO UPDATE SET last_updated_at = NOW()
		RETURNING id, (xmax = 0) AS inserted`

	var id int64
	var inserted bool
	err := s.pool.QueryRow(ctx, query,
		doc.PersonID,
		doc.ClusterLabel,
		doc.LabelEn,
		pgvector.NewVector(doc.ClusterEmbedding),
		doc.Summary,
		doc.SummaryEn,
		doc.KeyFactIDs,
		doc.FactCount,
	).Scan(&id, &inserted)
	if err != nil {
		return 0, fmt.Errorf("inserting stance document: %w", err)
	}
	if !inserted {
		log.Warn().Int64("person_id", doc.PersonID).Str("label", doc.ClusterLabel).Int64("stance_id", id).
			Msg("New-stance label already exists for person; routing into the existing stance")
	}

	return id, nil
}

// UpdateStanceDocument updates an existing stance document's summary, embedding and key facts.
// fact_count follows the stance_document_facts rows (trigger stance_document_facts_count).
func (s *PostgresStorer) UpdateStanceDocument(ctx context.Context, id int64, summary string, summaryEn string, embedding []float32, keyFactIDs []int64) error {
	query := `
		UPDATE stance_documents
		SET summary = $1, summary_en = NULLIF($2,''), cluster_embedding = $3, key_fact_ids = $4, last_updated_at = NOW()
		WHERE id = $5`

	_, err := s.pool.Exec(ctx, query, summary, summaryEn, pgvector.NewVector(embedding), keyFactIDs, id)
	if err != nil {
		return fmt.Errorf("updating stance document %d: %w", id, err)
	}

	return nil
}

// UpdateStanceMetadata updates only the native cluster_label and summary of a stance.
// The _en (and other-language) columns are owned by the async translation phase and
// intentionally left untouched here.
// Used by the per-fact stance refresh path (StanceManager.triggerStanceRefresh)
// to keep the stance's title and description in sync with its accumulating
// fact set without touching the centroid / fact_count / key_fact_ids.
func (s *PostgresStorer) UpdateStanceMetadata(ctx context.Context, id int64, label, summary string, consumedFactIDs []int64) error {
	// summarized_at advances to the newest membership the refresh actually
	// absorbed (never backwards). A membership added while the LLM was running
	// is newer than every consumed one, so it stays pending for the next
	// refresh (GetUnsummarizedFactsForStance) instead of being stamped past.
	query := `
		UPDATE stance_documents sd
		SET cluster_label = $1, summary = $2, last_updated_at = NOW(),
		    summarized_at = GREATEST(
		        COALESCE(sd.summarized_at, '-infinity'::timestamptz),
		        COALESCE((SELECT MAX(sdf.added_at) FROM stance_document_facts sdf
		                  WHERE sdf.stance_document_id = sd.id AND sdf.fact_id = ANY($4)),
		                 '-infinity'::timestamptz))
		WHERE sd.id = $3`

	_, err := s.pool.Exec(ctx, query, label, summary, id, consumedFactIDs)
	if err != nil {
		return fmt.Errorf("updating stance metadata %d: %w", id, err)
	}
	return nil
}

// GetStanceTensions returns, per fact of the stance, the recorded
// contradictions between that fact and OTHER facts of the same stance (both
// directions), owned by the stance's person. Feeds the incremental metadata
// refresh so a summary never has to rediscover a tension among facts it does
// not see.
func (s *PostgresStorer) GetStanceTensions(ctx context.Context, stanceID int64) (map[int64][]core.StanceTension, error) {
	rows, err := s.pool.Query(ctx, `
		WITH members AS (
			SELECT sdf.fact_id, sd.person_id
			FROM stance_document_facts sdf
			JOIN stance_documents sd ON sd.id = sdf.stance_document_id
			WHERE sdf.stance_document_id = $1
		)
		SELECT ma.fact_id, fb.fact_text, COALESCE(c.reason, '')
		FROM contradictions c
		JOIN members ma ON ma.fact_id = c.fact_a_id AND c.person_id = ma.person_id
		JOIN members mb ON mb.fact_id = c.fact_b_id
		JOIN facts fb ON fb.id = c.fact_b_id
		UNION ALL
		SELECT mb.fact_id, fa.fact_text, COALESCE(c.reason, '')
		FROM contradictions c
		JOIN members mb ON mb.fact_id = c.fact_b_id AND c.person_id = mb.person_id
		JOIN members ma ON ma.fact_id = c.fact_a_id
		JOIN facts fa ON fa.id = c.fact_a_id`, stanceID)
	if err != nil {
		return nil, fmt.Errorf("querying tensions for stance %d: %w", stanceID, err)
	}
	defer rows.Close()

	out := map[int64][]core.StanceTension{}
	for rows.Next() {
		var t core.StanceTension
		if err := rows.Scan(&t.FactID, &t.OtherFactText, &t.Reason); err != nil {
			return nil, fmt.Errorf("scanning stance tension: %w", err)
		}
		out[t.FactID] = append(out[t.FactID], t)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating tensions for stance %d: %w", stanceID, err)
	}
	return out, nil
}

// GetStanceMetadata returns the stance's current native label and summary.
func (s *PostgresStorer) GetStanceMetadata(ctx context.Context, stanceID int64) (label, summary string, err error) {
	err = s.pool.QueryRow(ctx,
		`SELECT cluster_label, summary FROM stance_documents WHERE id = $1`, stanceID).Scan(&label, &summary)
	if err != nil {
		return "", "", fmt.Errorf("loading stance %d metadata: %w", stanceID, err)
	}
	return label, summary, nil
}

// GetUnsummarizedFactsForStance returns the stance's facts whose membership was
// added after the stance's summarized_at (all of them when it is NULL), oldest
// membership first — the facts the label + summary have not absorbed yet.
func (s *PostgresStorer) GetUnsummarizedFactsForStance(ctx context.Context, stanceID int64) ([]core.StanceFactInfo, error) {
	query := `
		SELECT f.id, f.fact_text, COALESCE(f.embedding_text, ''), COALESCE(f.canonical_verb, ''), COALESCE(f.lang, ''),
		       (SELECT MIN(fs.published_at) FROM fact_sources fs WHERE fs.fact_id = f.id)
		FROM facts f
		JOIN stance_document_facts sdf ON f.id = sdf.fact_id
		JOIN stance_documents sd ON sd.id = sdf.stance_document_id
		WHERE sdf.stance_document_id = $1
		  AND (sd.summarized_at IS NULL OR sdf.added_at > sd.summarized_at)
		ORDER BY sdf.added_at, f.id`

	rows, err := s.pool.Query(ctx, query, stanceID)
	if err != nil {
		return nil, fmt.Errorf("querying unsummarized facts for stance %d: %w", stanceID, err)
	}
	defer rows.Close()

	var facts []core.StanceFactInfo
	for rows.Next() {
		var f core.StanceFactInfo
		if err := rows.Scan(&f.ID, &f.NormalizedFactText, &f.EmbeddingText, &f.CanonicalVerb, &f.Lang, &f.Date); err != nil {
			return nil, fmt.Errorf("scanning unsummarized stance fact: %w", err)
		}
		facts = append(facts, f)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating unsummarized facts for stance %d: %w", stanceID, err)
	}
	return facts, nil
}

// AddFactToStance adds a fact to the stance document join table. No-op if already linked.
func (s *PostgresStorer) AddFactToStance(ctx context.Context, stanceID int64, factID int64) error {
	query := `INSERT INTO stance_document_facts (stance_document_id, fact_id) VALUES ($1, $2) ON CONFLICT DO NOTHING`

	_, err := s.pool.Exec(ctx, query, stanceID, factID)
	if err != nil {
		return fmt.Errorf("adding fact %d to stance %d: %w", factID, stanceID, err)
	}

	return nil
}

// GetStanceDocumentsForPerson lists all stance documents for a person, ordered by fact count descending.
func (s *PostgresStorer) GetStanceDocumentsForPerson(ctx context.Context, personID int64) ([]core.StanceDocument, error) {
	query := `
		SELECT id, person_id, cluster_label, COALESCE(label_en,'') AS label_en, cluster_embedding,
		       summary, COALESCE(summary_en,'') AS summary_en, key_fact_ids, fact_count, last_updated_at, created_at
		FROM stance_documents
		WHERE person_id = $1
		ORDER BY fact_count DESC`

	rows, err := s.pool.Query(ctx, query, personID)
	if err != nil {
		return nil, fmt.Errorf("querying stance documents for person %d: %w", personID, err)
	}
	defer rows.Close()

	var docs []core.StanceDocument
	for rows.Next() {
		var doc core.StanceDocument
		var vec pgvector.Vector
		if err := rows.Scan(
			&doc.ID,
			&doc.PersonID,
			&doc.ClusterLabel,
			&doc.LabelEn,
			&vec,
			&doc.Summary,
			&doc.SummaryEn,
			&doc.KeyFactIDs,
			&doc.FactCount,
			&doc.LastUpdatedAt,
			&doc.CreatedAt,
		); err != nil {
			return nil, fmt.Errorf("scanning stance document: %w", err)
		}
		doc.ClusterEmbedding = vec.Slice()
		docs = append(docs, doc)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating stance documents for person %d: %w", personID, err)
	}

	return docs, nil
}

// GetStanceDocument returns one stance document by id.
func (s *PostgresStorer) GetStanceDocument(ctx context.Context, id int64) (core.StanceDocument, error) {
	var doc core.StanceDocument
	var vec pgvector.Vector
	err := s.pool.QueryRow(ctx, `
		SELECT id, person_id, cluster_label, COALESCE(label_en,''), cluster_embedding,
		       summary, COALESCE(summary_en,''), key_fact_ids, fact_count, last_updated_at, created_at
		FROM stance_documents
		WHERE id = $1`, id).Scan(
		&doc.ID, &doc.PersonID, &doc.ClusterLabel, &doc.LabelEn, &vec,
		&doc.Summary, &doc.SummaryEn, &doc.KeyFactIDs, &doc.FactCount, &doc.LastUpdatedAt, &doc.CreatedAt,
	)
	if err != nil {
		return core.StanceDocument{}, fmt.Errorf("loading stance document %d: %w", id, err)
	}
	doc.ClusterEmbedding = vec.Slice()
	return doc, nil
}

// ListStancesPendingRefresh returns up to limit stances whose label and
// summary have not absorbed all their facts: a membership was added after
// summarized_at, or the stance was never summarized. A stance with one fact
// is left out: its label comes from the assignment and its summary is the
// fact text. The stances with the most recent pending membership come first.
func (s *PostgresStorer) ListStancesPendingRefresh(ctx context.Context, limit int) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT sd.id
		FROM stance_documents sd
		JOIN stance_document_facts sdf ON sdf.stance_document_id = sd.id
		GROUP BY sd.id, sd.summarized_at
		HAVING count(*) >= 2
		   AND (sd.summarized_at IS NULL OR max(sdf.added_at) > sd.summarized_at)
		ORDER BY max(sdf.added_at) DESC, sd.id
		LIMIT $1`, limit)
	if err != nil {
		return nil, fmt.Errorf("listing stances pending refresh: %w", err)
	}
	defer rows.Close()
	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning stance pending refresh: %w", err)
		}
		ids = append(ids, id)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating stances pending refresh: %w", err)
	}
	return ids, nil
}

// ShortlistStancesByNearestFact ranks a person's stances by their nearest member
// fact's distance to factEmbedding and returns the top-K, each with its
// factsPerStance nearest member fact texts (closest first). Duplicate facts are
// excluded. Stances are ordered by nearest distance ascending; member facts
// within a stance are ordered by distance ascending.
//
// Ranking by the NEAREST member fact (not the centroid) is the fix for the
// mega-stance attractor: a large catch-all stance's averaged centroid sits
// moderately close to everything, but its individual facts do not, so this
// ranking keeps loosely related facts out of broad buckets. (Distinct from
// StanceCandidate, the centroid-ranked struct returned by
// FindTopStancesForPerson for the Bundler.)
func (s *PostgresStorer) ShortlistStancesByNearestFact(ctx context.Context, personID int64, factEmbedding []float32, topK, factsPerStance int, excludeFactID int64) ([]core.StanceCandidate, error) {
	query := `
		WITH fd AS (
			SELECT sd.id AS stance_id, sd.cluster_label, COALESCE(sd.summary, '') AS summary, sd.fact_count,
			       f.fact_text, (f.embedding <-> $1) AS dist
			FROM stance_documents sd
			JOIN stance_document_facts sdf ON sdf.stance_document_id = sd.id
			JOIN facts f ON f.id = sdf.fact_id
			WHERE sd.person_id = $2 AND f.id <> $5
			  AND NOT EXISTS (
			      SELECT 1 FROM fact_relations fr
			      WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
			  )
		),
		ranked AS (
			SELECT *,
			       row_number() OVER (PARTITION BY stance_id ORDER BY dist) AS fact_rn,
			       MIN(dist) OVER (PARTITION BY stance_id) AS stance_min_dist
			FROM fd
		),
		top_stances AS (
			SELECT stance_id, MIN(stance_min_dist) AS d
			FROM ranked
			GROUP BY stance_id
			ORDER BY d ASC
			LIMIT $3
		)
		SELECT r.stance_id, r.cluster_label, r.summary, r.fact_count, r.fact_text, r.stance_min_dist
		FROM ranked r
		JOIN top_stances ts ON ts.stance_id = r.stance_id
		WHERE r.fact_rn <= $4
		ORDER BY r.stance_min_dist ASC, r.stance_id ASC, r.dist ASC`

	rows, err := s.pool.Query(ctx, query,
		pgvector.NewVector(factEmbedding), // $1
		personID,                          // $2
		topK,                              // $3
		factsPerStance,                    // $4
		excludeFactID,                     // $5
	)
	if err != nil {
		return nil, fmt.Errorf("querying nearest-fact stance shortlist for person %d: %w", personID, err)
	}
	defer rows.Close()

	return scanStanceCandidates(rows, personID)
}

// StanceCandidatesByIDs returns the given stances of the person as topic
// candidates in the same shape as ShortlistStancesByNearestFact (each with its
// factsPerStance nearest active member facts, closest first), ordered by their
// nearest member fact's distance to factEmbedding. Used to add the stances the
// label pass picked to the embedding shortlist.
func (s *PostgresStorer) StanceCandidatesByIDs(ctx context.Context, personID int64, factEmbedding []float32, stanceIDs []int64, factsPerStance int, excludeFactID int64) ([]core.StanceCandidate, error) {
	if len(stanceIDs) == 0 {
		return nil, nil
	}
	query := `
		WITH fd AS (
			SELECT sd.id AS stance_id, sd.cluster_label, COALESCE(sd.summary, '') AS summary, sd.fact_count,
			       f.fact_text, (f.embedding <-> $1) AS dist
			FROM stance_documents sd
			JOIN stance_document_facts sdf ON sdf.stance_document_id = sd.id
			JOIN facts f ON f.id = sdf.fact_id
			WHERE sd.person_id = $2 AND sd.id = ANY($3) AND f.id <> $5
			  AND NOT EXISTS (
			      SELECT 1 FROM fact_relations fr
			      WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
			  )
		),
		ranked AS (
			SELECT *,
			       row_number() OVER (PARTITION BY stance_id ORDER BY dist) AS fact_rn,
			       MIN(dist) OVER (PARTITION BY stance_id) AS stance_min_dist
			FROM fd
		)
		SELECT stance_id, cluster_label, summary, fact_count, fact_text, stance_min_dist
		FROM ranked
		WHERE fact_rn <= $4
		ORDER BY stance_min_dist ASC, stance_id ASC, dist ASC`
	rows, err := s.pool.Query(ctx, query, pgvector.NewVector(factEmbedding), personID, stanceIDs, factsPerStance, excludeFactID)
	if err != nil {
		return nil, fmt.Errorf("querying stance candidates by id for person %d: %w", personID, err)
	}
	defer rows.Close()
	return scanStanceCandidates(rows, personID)
}

// GetFactsForStance retrieves all facts associated with a stance document.
func (s *PostgresStorer) GetFactsForStance(ctx context.Context, stanceID int64) ([]core.StanceFactInfo, error) {
	query := `
		SELECT f.id, f.fact_text, COALESCE(f.embedding_text, ''), COALESCE(f.canonical_verb, ''), COALESCE(f.lang, ''),
		       (SELECT MIN(fs.published_at) FROM fact_sources fs WHERE fs.fact_id = f.id)
		FROM facts f
		JOIN stance_document_facts sdf ON f.id = sdf.fact_id
		WHERE sdf.stance_document_id = $1
		ORDER BY f.id`

	rows, err := s.pool.Query(ctx, query, stanceID)
	if err != nil {
		return nil, fmt.Errorf("querying facts for stance %d: %w", stanceID, err)
	}
	defer rows.Close()

	var facts []core.StanceFactInfo
	for rows.Next() {
		var f core.StanceFactInfo
		if err := rows.Scan(&f.ID, &f.NormalizedFactText, &f.EmbeddingText, &f.CanonicalVerb, &f.Lang, &f.Date); err != nil {
			return nil, fmt.Errorf("scanning stance fact: %w", err)
		}
		facts = append(facts, f)
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating facts for stance %d: %w", stanceID, err)
	}

	return facts, nil
}

// GetStanceDocumentCounts returns aggregate stats: total stance documents and total facts linked.
func (s *PostgresStorer) GetStanceDocumentCounts(ctx context.Context) (totalStances int, totalFactsLinked int, err error) {
	query := `SELECT COUNT(*), COALESCE(SUM(fact_count), 0) FROM stance_documents`

	err = s.pool.QueryRow(ctx, query).Scan(&totalStances, &totalFactsLinked)
	if err != nil {
		return 0, 0, fmt.Errorf("querying stance document counts: %w", err)
	}

	return totalStances, totalFactsLinked, nil
}

// DeleteStanceDocumentsForPerson deletes all stance documents for a person.
// The CASCADE on stance_document_facts handles the join table cleanup.
func (s *PostgresStorer) DeleteStanceDocumentsForPerson(ctx context.Context, personID int64) error {
	_, err := s.pool.Exec(ctx, `DELETE FROM stance_documents WHERE person_id = $1`, personID)
	if err != nil {
		return fmt.Errorf("deleting stance documents for person %d: %w", personID, err)
	}

	return nil
}

// GetStanceDocumentByID retrieves a single stance document by ID.
// Returns pgx.ErrNoRows if not found.
func (s *PostgresStorer) GetStanceDocumentByID(ctx context.Context, id int64) (*core.StanceDocument, error) {
	query := `
		SELECT id, person_id, cluster_label, cluster_embedding, summary, key_fact_ids, fact_count, last_updated_at, created_at
		FROM stance_documents
		WHERE id = $1`

	var doc core.StanceDocument
	var vec pgvector.Vector
	err := s.pool.QueryRow(ctx, query, id).Scan(
		&doc.ID, &doc.PersonID, &doc.ClusterLabel, &vec,
		&doc.Summary, &doc.KeyFactIDs, &doc.FactCount,
		&doc.LastUpdatedAt, &doc.CreatedAt,
	)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("failed to get stance document %d: %w", id, err)
	}
	doc.ClusterEmbedding = vec.Slice()
	return &doc, nil
}

// DeleteStanceDocument deletes a single stance document by ID.
// The CASCADE on stance_document_facts handles join table cleanup.
// Returns pgx.ErrNoRows if not found.
func (s *PostgresStorer) DeleteStanceDocument(ctx context.Context, id int64) error {
	result, err := s.pool.Exec(ctx, `DELETE FROM stance_documents WHERE id = $1`, id)
	if err != nil {
		return fmt.Errorf("failed to delete stance document %d: %w", id, err)
	}
	if result.RowsAffected() == 0 {
		return pgx.ErrNoRows
	}
	log.Info().Int64("stance_id", id).Msg("Deleted stance document")
	return nil
}

// UpdateStanceSummary rewrites a stance document's summary and cluster_label
// (used by the LLM-merge phase, which produces a new unified summary/label for
// the merged group). Centroid, key facts, and fact_count are left untouched —
// those are managed by the merge tx itself.
func (s *PostgresStorer) UpdateStanceSummary(ctx context.Context, stanceID int64, summary, clusterLabel string) error {
	if strings.TrimSpace(summary) == "" {
		return fmt.Errorf("UpdateStanceSummary: summary must not be empty")
	}
	if strings.TrimSpace(clusterLabel) == "" {
		return fmt.Errorf("UpdateStanceSummary: cluster_label must not be empty")
	}
	_, err := s.pool.Exec(ctx, `
		UPDATE stance_documents
		SET summary = $1, cluster_label = $2, last_updated_at = NOW()
		WHERE id = $3`, summary, clusterLabel, stanceID)
	if err != nil {
		return fmt.Errorf("updating stance summary/label for %d: %w", stanceID, err)
	}
	return nil
}

// MergeStancesForPerson merges src into dst in a single transaction. Fact memberships
// are moved to dst (duplicates dropped via ON CONFLICT) with added_at = NOW(): the
// facts are new TO DST, so the next metadata refresh folds them into dst's summary
// via summarized_at. Contradictions whose bundle
// pointed at src are repointed to dst BEFORE the row is dropped (the FK is ON DELETE
// SET NULL, so a naive delete would silently null them), then dst's centroid and
// fact_count are updated and src is deleted. The new centroid is pre-computed by the
// caller (weighted normalised mean over members); summary and key_fact_ids on dst are
// preserved intentionally — dst is the larger stance.
func (s *PostgresStorer) MergeStancesForPerson(ctx context.Context, srcID, dstID int64, newDstCentroid []float32) error {
	if srcID == dstID {
		return fmt.Errorf("MergeStancesForPerson: src and dst are the same (%d)", srcID)
	}

	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin merge tx: %w", err)
	}
	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, `
		INSERT INTO stance_document_facts (stance_document_id, fact_id, added_at)
		SELECT $1, fact_id, NOW() FROM stance_document_facts
		WHERE stance_document_id = $2
		ON CONFLICT (stance_document_id, fact_id) DO NOTHING`, dstID, srcID); err != nil {
		return fmt.Errorf("moving fact memberships %d->%d: %w", srcID, dstID, err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM stance_document_facts WHERE stance_document_id = $1`, srcID); err != nil {
		return fmt.Errorf("clearing src memberships %d: %w", srcID, err)
	}

	if _, err := tx.Exec(ctx, `UPDATE contradictions SET bundle_stance_id = $1 WHERE bundle_stance_id = $2`, dstID, srcID); err != nil {
		return fmt.Errorf("repointing contradictions %d->%d: %w", srcID, dstID, err)
	}

	if _, err := tx.Exec(ctx, `
		UPDATE stance_documents
		SET cluster_embedding = $1,
		    fact_count = (SELECT count(*) FROM stance_document_facts WHERE stance_document_id = $2),
		    last_updated_at = NOW()
		WHERE id = $2`, pgvector.NewVector(newDstCentroid), dstID); err != nil {
		return fmt.Errorf("updating dst stance %d: %w", dstID, err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM stance_documents WHERE id = $1`, srcID); err != nil {
		return fmt.Errorf("deleting src stance %d: %w", srcID, err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit merge %d->%d: %w", srcID, dstID, err)
	}
	return nil
}

// GetFactStanceMap returns a map fact_id -> stance_document_id for the given
// facts, restricted to the given person's stances. Facts without a membership
// in one of that person's stances are simply absent from the map. The person
// scope matters: a fact mentioned by several persons is a member of one stance
// PER person, so an unscoped lookup would return an arbitrary person's stance.
func (s *PostgresStorer) GetFactStanceMap(ctx context.Context, personID int64, factIDs []int64) (map[int64]int64, error) {
	if len(factIDs) == 0 {
		return map[int64]int64{}, nil
	}
	rows, err := s.pool.Query(ctx,
		`SELECT sdf.fact_id, sdf.stance_document_id
		 FROM stance_document_facts sdf
		 JOIN stance_documents sd ON sd.id = sdf.stance_document_id
		 WHERE sdf.fact_id = ANY($2) AND sd.person_id = $1`,
		personID, factIDs)
	if err != nil {
		return nil, fmt.Errorf("querying fact stances: %w", err)
	}
	defer rows.Close()
	out := make(map[int64]int64, len(factIDs))
	for rows.Next() {
		var fid, sid int64
		if err := rows.Scan(&fid, &sid); err != nil {
			return nil, fmt.Errorf("scanning fact stance row: %w", err)
		}
		out[fid] = sid
	}
	return out, rows.Err()
}

// StanceCandidate is a stance returned by FindTopStancesForPerson.
type StanceCandidate struct {
	ID           int64
	ClusterLabel string
	Summary      string
	Distance     float64
}

// FindTopStancesForPerson returns up to `limit` stances for the person, ranked by cosine
// distance from the given embedding (lowest distance first). Used by the Bundler to pick
// LLM candidates when fact-level stance lookups don't agree.
func (s *PostgresStorer) FindTopStancesForPerson(ctx context.Context, personID int64, embedding []float32, limit int) ([]StanceCandidate, error) {
	if limit <= 0 {
		return nil, nil
	}
	rows, err := s.pool.Query(ctx, `
		SELECT id, cluster_label, summary, cluster_embedding <-> $2 AS distance
		FROM stance_documents
		WHERE person_id = $1
		ORDER BY cluster_embedding <-> $2
		LIMIT $3`, personID, pgvector.NewVector(embedding), limit)
	if err != nil {
		return nil, fmt.Errorf("querying top stances for person %d: %w", personID, err)
	}
	defer rows.Close()
	var out []StanceCandidate
	for rows.Next() {
		var c StanceCandidate
		if err := rows.Scan(&c.ID, &c.ClusterLabel, &c.Summary, &c.Distance); err != nil {
			return nil, fmt.Errorf("scanning stance candidate: %w", err)
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// ListPersonsWithOrphanFacts returns every person who has at least one
// non-duplicate mentioned fact not linked to any of THAT PERSON's stance
// documents. Drives the per-person worker loop in `dm facts assign-stances`
// and the nightly stance fan-out step. The scope is per person: a fact in one
// person's stance is not stanced for another person it mentions (mirror-py3).
//
// Symmetric with ListOrphanFactsForPerson: facts marked as
// `fact_relations.relation_type = 'duplicate'` are excluded — duplicate
// facts are pointers to a canonical fact and do not get their own stance
// link by design. Without this filter, persons whose only orphans are
// duplicates loop forever in the assign-stances worker (the per-person
// orphan list comes back empty, the person is "skipped", but they still
// show up next iteration).
func (s *PostgresStorer) ListPersonsWithOrphanFacts(ctx context.Context) ([]core.PersonRecord, error) {
	return s.queryPersonRecords(ctx, `
		SELECT DISTINCT p.id, p.name, COALESCE(p.entity_type, 'person')
		FROM persons p
		JOIN fact_mentions fm ON fm.person_id = p.id
		JOIN fact_sources fs ON fs.id = fm.fact_source_id
		JOIN facts f ON f.id = fs.fact_id
		WHERE f.embedding IS NOT NULL
		AND NOT EXISTS (
		    SELECT 1 FROM stance_document_facts sdf
		    JOIN stance_documents sd ON sd.id = sdf.stance_document_id
		    WHERE sdf.fact_id = f.id AND sd.person_id = p.id
		)
		AND NOT EXISTS (
		    SELECT 1 FROM fact_relations fr
		    WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
		)
		ORDER BY p.name`)
}

// ListOrphanFactsForPerson returns every active fact mentioning the person
// that is not linked to any of THAT PERSON's stance documents (a fact stanced
// for another person it mentions is still an orphan for this one). Skips facts
// marked as duplicates via fact_relations (mirrors the filter in
// cmd/stances_backfill.go's loadFactsForBackfill so we never try to assign a
// stance to a known dup).
func (s *PostgresStorer) ListOrphanFactsForPerson(ctx context.Context, personID int64) ([]core.StanceRoutingFact, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT f.id, f.fact_text, COALESCE(f.embedding_text, ''),
		       COALESCE(f.canonical_verb, ''), f.embedding, MIN(fs.published_at)
		FROM facts f
		JOIN fact_sources fs ON f.id = fs.fact_id
		JOIN fact_mentions fm ON fm.fact_source_id = fs.id
		WHERE fm.person_id = $1
		AND f.embedding IS NOT NULL
		AND NOT EXISTS (
		    SELECT 1 FROM stance_document_facts sdf
		    JOIN stance_documents sd ON sd.id = sdf.stance_document_id
		    WHERE sdf.fact_id = f.id AND sd.person_id = $1
		)
		AND NOT EXISTS (
		    SELECT 1 FROM fact_relations fr
		    WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate'
		)
		GROUP BY f.id
		ORDER BY f.id`, personID)
	if err != nil {
		return nil, fmt.Errorf("listing orphan facts for person %d: %w", personID, err)
	}
	defer rows.Close()

	var out []core.StanceRoutingFact
	for rows.Next() {
		var f core.StanceRoutingFact
		var vec pgvector.Vector
		if err := rows.Scan(&f.ID, &f.FactText, &f.EmbeddingText, &f.CanonicalVerb, &vec, &f.PublishedAt); err != nil {
			return nil, fmt.Errorf("scanning orphan fact: %w", err)
		}
		f.Embedding = vec.Slice()
		if len(f.Embedding) == 0 {
			continue
		}
		out = append(out, f)
	}
	return out, rows.Err()
}

// scanStanceCandidates builds candidates from rows ordered by stance then
// member distance: a new candidate starts whenever the stance id changes.
func scanStanceCandidates(rows pgx.Rows, personID int64) ([]core.StanceCandidate, error) {
	var candidates []core.StanceCandidate
	for rows.Next() {
		var (
			stanceID      int64
			label, summ   string
			factCount     int
			factText      string
			stanceMinDist float64
		)
		if err := rows.Scan(&stanceID, &label, &summ, &factCount, &factText, &stanceMinDist); err != nil {
			return nil, fmt.Errorf("scanning stance candidate for person %d: %w", personID, err)
		}
		if n := len(candidates); n == 0 || candidates[n-1].ID != stanceID {
			candidates = append(candidates, core.StanceCandidate{
				ID:          stanceID,
				Label:       label,
				Summary:     summ,
				FactCount:   factCount,
				NearestDist: stanceMinDist,
				MemberFacts: []string{factText},
			})
		} else {
			candidates[n-1].MemberFacts = append(candidates[n-1].MemberFacts, factText)
		}
	}

	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterating stance candidates for person %d: %w", personID, err)
	}

	return candidates, nil
}

// ListPersonsWithSingletonStances lists the persons with at least one
// one-fact stance and at least two stances in total (a singleton on a
// one-stance person has nothing to join). Search queue members come first,
// then by person id, so a deadline-cut sweep resumes deterministically.
func (s *PostgresStorer) ListPersonsWithSingletonStances(ctx context.Context) ([]core.PersonRecord, error) {
	return s.queryPersonRecords(ctx, `
		SELECT p.id, p.name, COALESCE(p.entity_type, 'person')
		FROM persons p
		JOIN (
			SELECT person_id
			FROM stance_documents
			GROUP BY person_id
			HAVING COUNT(*) >= 2 AND COUNT(*) FILTER (WHERE fact_count = 1) >= 1
		) s ON s.person_id = p.id
		ORDER BY EXISTS (SELECT 1 FROM search_queue q WHERE q.person_id = p.id) DESC, p.id`)
}

// SampleFactsInMultiFactStances draws up to n random active facts that sit
// in a stance with more than one fact, among personIDs (all persons when
// empty), with the stance and person they belong to.
func (s *PostgresStorer) SampleFactsInMultiFactStances(ctx context.Context, personIDs []int64, n int) ([]core.FactPlacement, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT sdf.fact_id, sd.id, sd.cluster_label, p.id, p.name
		FROM stance_document_facts sdf
		JOIN stance_documents sd ON sd.id = sdf.stance_document_id
		JOIN persons p ON p.id = sd.person_id
		WHERE sd.fact_count > 1
		  AND (coalesce(cardinality($1::bigint[]), 0) = 0 OR sd.person_id = ANY($1))
		  AND NOT EXISTS (
		      SELECT 1 FROM fact_relations fr
		      WHERE fr.specific_fact_id = sdf.fact_id AND fr.relation_type = 'duplicate'
		  )
		ORDER BY random()
		LIMIT $2`, personIDs, n)
	if err != nil {
		return nil, fmt.Errorf("sampling facts in multi-fact stances: %w", err)
	}
	defer rows.Close()
	var out []core.FactPlacement
	for rows.Next() {
		var pl core.FactPlacement
		if err := rows.Scan(&pl.FactID, &pl.StanceID, &pl.StanceLabel, &pl.PersonID, &pl.PersonName); err != nil {
			return nil, fmt.Errorf("scanning sampled fact: %w", err)
		}
		out = append(out, pl)
	}
	return out, rows.Err()
}

// ListLabelSimilarStancePairs returns the person's stance pairs whose labels
// have pg_trgm similarity of at least floor, as [smaller id, larger id].
func (s *PostgresStorer) ListLabelSimilarStancePairs(ctx context.Context, personID int64, floor float64) ([][2]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT a.id, b.id
		FROM stance_documents a
		JOIN stance_documents b ON b.person_id = a.person_id AND b.id > a.id
		WHERE a.person_id = $1 AND similarity(a.cluster_label, b.cluster_label) >= $2`, personID, floor)
	if err != nil {
		return nil, fmt.Errorf("listing label-similar stance pairs for person %d: %w", personID, err)
	}
	defer rows.Close()
	var out [][2]int64
	for rows.Next() {
		var p [2]int64
		if err := rows.Scan(&p[0], &p[1]); err != nil {
			return nil, fmt.Errorf("scanning label-similar pair: %w", err)
		}
		out = append(out, p)
	}
	return out, rows.Err()
}
