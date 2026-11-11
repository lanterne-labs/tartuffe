package storage

import (
	"context"

	"digital-mirror/backend/core"
	"digital-mirror/backend/metrics"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/rs/zerolog/log"
)

// loadPairSet returns a set of normalized "factAID-factBID" keys (smaller ID
// first, as stored) from a table holding (fact_a_id, fact_b_id) columns for a
// person. Backs GetExistingContradictionPairs and GetCheckedPairs.
func (s *PostgresStorer) loadPairSet(ctx context.Context, table string, personID int64) (map[string]bool, error) {
	query := fmt.Sprintf(`SELECT fact_a_id, fact_b_id FROM %s WHERE person_id = $1`, table)
	rows, err := s.pool.Query(ctx, query, personID)
	if err != nil {
		return nil, fmt.Errorf("failed to query %s for person %d: %w", table, personID, err)
	}
	defer rows.Close()

	pairs := make(map[string]bool)
	for rows.Next() {
		var factAID, factBID int64
		if err := rows.Scan(&factAID, &factBID); err != nil {
			return nil, fmt.Errorf("failed to scan pair from %s: %w", table, err)
		}
		pairs[fmt.Sprintf("%d-%d", factAID, factBID)] = true
	}
	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating pairs from %s: %w", table, rows.Err())
	}
	return pairs, nil
}

// PairCheckResult represents the result of a fact pair check.
type PairCheckResult struct {
	FactAID         int64
	FactBID         int64
	IsContradiction bool
}

// GetCheckedPairs returns all previously checked fact pairs for a person.
// Returns map["factAID-factBID"] = true for efficient lookup.
// This includes both contradictory and non-contradictory pairs.
func (s *PostgresStorer) GetCheckedPairs(ctx context.Context, personID int64) (map[string]bool, error) {
	return s.loadPairSet(ctx, "fact_pair_checks", personID)
}

// EvictStaleJudgedPairs deletes up to `limit` of the OLDEST negative pair
// verdicts whose judge_hash is not currentHash (NULL included), so the nightly
// detector re-judges those pairs under the current rulebook. Returns the
// number deleted and how many stale negatives remain. Positive verdicts are
// never touched here — they are the auditor's jurisdiction — nor are the
// negatives a dismissal recorded (dismissed): their NO does not come from the
// detector's rulebook. Self-terminating: once every negative carries the
// current hash, nothing is deleted.
//
// currentHashes are the hashes of the judges whose verdicts stand: the
// detector's rulebook, and the stance analysis that judges a new fact against
// the closest facts of its stance (core.StanceJudgeHash).
func (s *PostgresStorer) EvictStaleJudgedPairs(ctx context.Context, currentHashes []string, limit int) (deleted, remaining int, err error) {
	res, err := s.pool.Exec(ctx, `
		WITH victims AS (
			SELECT id FROM fact_pair_checks
			WHERE is_contradiction = false AND NOT dismissed AND (judge_hash IS NULL OR judge_hash <> ALL($1))
			ORDER BY checked_at ASC
			LIMIT $2
		)
		DELETE FROM fact_pair_checks WHERE id IN (SELECT id FROM victims)`, currentHashes, limit)
	if err != nil {
		return 0, 0, fmt.Errorf("evicting stale judged pairs: %w", err)
	}
	deleted = int(res.RowsAffected())
	err = s.pool.QueryRow(ctx,
		`SELECT count(*) FROM fact_pair_checks WHERE is_contradiction = false AND NOT dismissed AND (judge_hash IS NULL OR judge_hash <> ALL($1))`,
		currentHashes).Scan(&remaining)
	if err != nil {
		return deleted, 0, fmt.Errorf("counting stale judged pairs: %w", err)
	}
	return deleted, remaining, nil
}

// RecordPairChecksBatch records multiple pair checks efficiently.
// judgeHash is the rulebook the verdicts were produced under
// (contradiction.JudgeRulebookHash); the nightly re-judge step evicts negatives
// whose hash is not the current one. A re-judged pair is the detector's
// verdict again: a dismissal recorded on it before is cleared.
func (s *PostgresStorer) RecordPairChecksBatch(ctx context.Context, personID int64, judgeHash string, results []PairCheckResult) error {
	if len(results) == 0 {
		return nil
	}

	// Build batch insert query
	query := `
		INSERT INTO fact_pair_checks (person_id, fact_a_id, fact_b_id, is_contradiction, judge_hash)
		VALUES ($1, $2, $3, $4, $5)
		ON CONFLICT (person_id, fact_a_id, fact_b_id) DO UPDATE
		SET is_contradiction = EXCLUDED.is_contradiction,
		    judge_hash = EXCLUDED.judge_hash,
		    dismissed = false,
		    checked_at = NOW()`

	batch := &pgx.Batch{}
	for _, result := range results {
		factAID, factBID := result.FactAID, result.FactBID
		// Ensure consistent ordering
		if factAID > factBID {
			factAID, factBID = factBID, factAID
		}
		batch.Queue(query, personID, factAID, factBID, result.IsContradiction, judgeHash)
	}

	br := s.pool.SendBatch(ctx, batch)
	defer br.Close()

	for i := 0; i < len(results); i++ {
		_, err := br.Exec()
		if err != nil {
			return fmt.Errorf("failed to record pair check batch at index %d: %w", i, err)
		}
	}

	return nil
}

// RecordContradictionImmediate records a contradiction and commits immediately.
// Use this when you want each contradiction saved right away (e.g., in CLI commands).
//
// Returns:
//   - contradictionID: the row id (whether newly inserted or pre-existing)
//   - needsBundler: true iff this insert produced a NULL bundle_stance_id (cross-stance
//     or unstanced facts) — callers should pass this row to the contradiction Bundler
//     for LLM-driven stance assignment. False for same-stance fast-path bundles and for
//     pre-existing rows (which already had a chance to be bundled).
func (s *PostgresStorer) RecordContradictionImmediate(ctx context.Context, personID int64, factAID int64, factBID int64, reason string, reasonEn string) (contradictionID int64, needsBundler bool, err error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, false, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	id, wasInserted, bundleStanceID, err := s.RecordContradiction(ctx, tx, personID, factAID, factBID, reason, reasonEn)
	if err != nil {
		return 0, false, err
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, false, fmt.Errorf("failed to commit contradiction: %w", err)
	}

	return id, wasInserted && bundleStanceID == nil, nil
}

// RecordContradiction inserts a record indicating that two facts for a person are contradictory.
// Ensures that factAID < factBID to avoid duplicate pairs in different orders.
// Now includes a reason for the contradiction.
// On new contradiction insertion, recomputes the person's honesty score and emits events.
// Returns the contradiction id, whether it was newly inserted, and the bundle_stance_id
// (nil = needs bundler resolution).
func (s *PostgresStorer) RecordContradiction(ctx context.Context, tx pgx.Tx, personID int64, factAID int64, factBID int64, reason string, reasonEn string) (int64, bool, *int64, error) {
	timer := metrics.NewQueryTimer("record_contradiction")

	// A contradiction points at the parents of the claim groups: a member
	// stands for its parent, so two members of one group, or a member and
	// its parent's other wordings, give one row. A path that judged a member
	// (the stance path reads a stance's key facts) lands on the parent.
	var err error
	if factAID, err = claimGroupParent(ctx, tx, factAID); err != nil {
		return 0, false, nil, err
	}
	if factBID, err = claimGroupParent(ctx, tx, factBID); err != nil {
		return 0, false, nil, err
	}
	if factAID == factBID {
		log.Info().Int64("person_id", personID).Int64("fact_id", factAID).
			Msg("Skipped contradiction: both facts are in one claim group")
		return 0, false, nil, nil
	}

	// Ensure consistent ordering of fact IDs to prevent duplicates like (1,2) and (2,1)
	if factAID > factBID {
		factAID, factBID = factBID, factAID
	}

	// Fast-path bundle resolution: bundle_stance_id is set to the stance shared by both
	// facts (if any). Cross-stance and unstanced pairs get NULL and behave as singleton
	// solo bundles in the score until an LLM-driven Bundler pass upgrades them.
	// The shared stance MUST belong to the contradiction's own person: a fact
	// mentioned by several persons is a member of one stance per person, so without
	// the person filter the CTE could pick another person's stance and file the
	// bundle under a foreign key.
	query := `
        WITH shared AS (
            SELECT sa.stance_document_id
            FROM stance_document_facts sa
            JOIN stance_document_facts sb ON sb.fact_id = $3
                                          AND sb.stance_document_id = sa.stance_document_id
            JOIN stance_documents sd ON sd.id = sa.stance_document_id
                                     AND sd.person_id = $1
            WHERE sa.fact_id = $2
            LIMIT 1
        )
        INSERT INTO contradictions (person_id, fact_a_id, fact_b_id, reason, reason_en, bundle_stance_id)
        SELECT $1::bigint, $2::bigint, $3::bigint, $4::text, NULLIF($5, '')::text,
               (SELECT stance_document_id FROM shared)
        -- Cross-person guard: only record a contradiction when both facts concern the
        -- SAME person (share a mentioned person via fact_sources -> fact_mentions). A
        -- pair with no shared person is a "different subjects" false positive (e.g. a
        -- Patricia Lemoine fact vs a Darmanin fact), so the SELECT yields no row and
        -- nothing is inserted; RETURNING comes back empty and the caller treats the
        -- pgx.ErrNoRows as a skip.
        WHERE EXISTS (
            SELECT 1
            FROM fact_sources fsa
            JOIN fact_mentions fma ON fma.fact_source_id = fsa.id
            JOIN fact_sources fsb ON fsb.fact_id = $3
            JOIN fact_mentions fmb ON fmb.fact_source_id = fsb.id
                                   AND fmb.person_id = fma.person_id
            WHERE fsa.fact_id = $2
        )
        ON CONFLICT (person_id, fact_a_id, fact_b_id) DO UPDATE
        SET reason = EXCLUDED.reason,
            reason_en = EXCLUDED.reason_en,
            detected_at = CURRENT_TIMESTAMP,
            -- Only fill in bundle_stance_id if it's still NULL; never clobber an
            -- LLM-resolved override on re-detection.
            bundle_stance_id = COALESCE(contradictions.bundle_stance_id, EXCLUDED.bundle_stance_id)
        RETURNING id, (xmax = 0) AS was_inserted, bundle_stance_id`

	var contradictionID int64
	var wasInserted bool
	var bundleStanceID *int64
	err = tx.QueryRow(ctx, query, personID, factAID, factBID, reason, reasonEn).Scan(&contradictionID, &wasInserted, &bundleStanceID)
	timer.ObserveDuration(err)

	if err == pgx.ErrNoRows {
		// Cross-person guard fired (see WHERE EXISTS above): the two facts share no
		// mentioned person, so this pair is a different-subjects false positive. Skip
		// recording it — return as not-inserted rather than an error.
		log.Warn().Int64("person_id", personID).Int64("fact_a_id", factAID).Int64("fact_b_id", factBID).
			Msg("Skipped contradiction: facts share no common subject person (cross-person pair)")
		return 0, false, nil, nil
	}
	if err != nil {
		return 0, false, nil, fmt.Errorf("failed to insert/update contradiction for person %d between facts %d and %d: %w", personID, factAID, factBID, err)
	}

	if wasInserted && bundleStanceID == nil {
		log.Warn().Int64("person_id", personID).Int64("fact_a_id", factAID).Int64("fact_b_id", factBID).
			Msg("Contradiction recorded with no bundle_stance_id (facts cross-stance or unstanced) — run dm stances rebuild and the Bundler to group it")
	}

	log.Info().Int64("person_id", personID).Int64("fact_a_id", factAID).Int64("fact_b_id", factBID).Bool("was_new_contradiction", wasInserted).Interface("bundle_stance_id", bundleStanceID).Msg("Recorded/updated contradiction")

	// If this was a new contradiction, recompute honesty score and emit events
	if wasInserted {
		metrics.IncrementContradictionsDetected()
		if err := s.recomputeHonestyScoreAndEmitEvents(ctx, tx, personID, contradictionID); err != nil {
			return 0, false, nil, fmt.Errorf("failed to recompute honesty score and emit events: %w", err)
		}
	}

	return contradictionID, wasInserted, bundleStanceID, nil
}

// getContradictionBundleInfo returns (bundleID, bundleSize) for a contradiction id.
// bundleID is *int64 — nil for solo (no bundle_stance_id). bundleSize counts pairs in the
// bundle (1 for solo). Used to enrich SSE payloads with bundle context.
func (s *PostgresStorer) getContradictionBundleInfo(ctx context.Context, tx pgx.Tx, personID int64, contradictionID int64) (*int64, int, error) {
	query := `
		WITH target AS (
			SELECT bundle_stance_id FROM contradictions WHERE id = $1
		)
		SELECT
			(SELECT bundle_stance_id FROM target),
			CASE WHEN (SELECT bundle_stance_id FROM target) IS NULL THEN 1
				 ELSE (SELECT COUNT(*) FROM contradictions
					   WHERE person_id = $2
						 AND bundle_stance_id = (SELECT bundle_stance_id FROM target))
			END AS bundle_size`
	var bundleID *int64
	var bundleSize int
	if err := tx.QueryRow(ctx, query, contradictionID, personID).Scan(&bundleID, &bundleSize); err != nil {
		return nil, 0, fmt.Errorf("failed to query bundle info for contradiction %d: %w", contradictionID, err)
	}
	return bundleID, bundleSize, nil
}

// getPersonContradictionStats returns (bundles, total) for a person, where bundles is the
// number of distinct stance-grouped clusters of contradictions and total is the raw pair count.
// Rows whose bundle_stance_id is NULL (cross-stance, or facts predating stance backfill)
// are each their own singleton bundle. See core.ComputeHonestyScore for how these feed the score.
func (s *PostgresStorer) getPersonContradictionStats(ctx context.Context, tx pgx.Tx, personID int64) (core.ContradictionStats, error) {
	query := `
		SELECT
			COUNT(DISTINCT COALESCE(bundle_stance_id::text, 'solo:'||id::text)) AS bundles,
			COUNT(*) AS total
		FROM contradictions
		WHERE person_id = $1`

	var stats core.ContradictionStats
	err := tx.QueryRow(ctx, query, personID).Scan(&stats.Bundles, &stats.Total)
	if err != nil {
		return core.ContradictionStats{}, fmt.Errorf("failed to compute contradiction stats for person %d: %w", personID, err)
	}
	return stats, nil
}

// GetStanceContradictionCounts returns per-stance contradiction totals for a person,
// keyed by stance_documents.id. Stances with zero contradictions are omitted (callers
// should treat missing keys as zero). Used by the stance-first profile UI to compute
// per-stance honesty scores that match the global formula.
func (s *PostgresStorer) GetStanceContradictionCounts(ctx context.Context, personID int64) (map[int64]int, error) {
	query := `
		SELECT bundle_stance_id, COUNT(*)
		FROM contradictions
		WHERE person_id = $1 AND bundle_stance_id IS NOT NULL
		GROUP BY bundle_stance_id`

	rows, err := s.pool.Query(ctx, query, personID)
	if err != nil {
		return nil, fmt.Errorf("failed to query stance contradiction counts for person %d: %w", personID, err)
	}
	defer rows.Close()

	counts := make(map[int64]int)
	for rows.Next() {
		var stanceID int64
		var n int
		if err := rows.Scan(&stanceID, &n); err != nil {
			return nil, fmt.Errorf("failed to scan stance contradiction count: %w", err)
		}
		counts[stanceID] = n
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating stance contradiction counts: %w", err)
	}
	return counts, nil
}

// PaginatedContradictions wraps a page of contradictions with the total count.
type PaginatedContradictions struct {
	Items []ContradictionWithFacts `json:"items"`
	Total int                      `json:"total"`
}

// ContradictionWithFacts represents a contradiction with both fact texts, sources, and dates.
// Bundle fields (BundleID/BundleLabel/BundleSummary/BundleSize) describe the stance bundle
// this contradiction belongs to, used by the frontend to group paraphrase pairs under one card.
// BundleID is nil for "solo" rows (cross-stance or unstanced).
// FactAStanceLabel/FactBStanceLabel surface each fact's own stance label so the frontend can
// render a "topic A ↔ topic B" header on cross-stance solos and a data-quality badge if either
// is nil (which means a fact is missing its stance row — should never happen post-migration).
type ContradictionWithFacts struct {
	ID              int64    `json:"id"`
	PersonID        int64    `json:"person_id"`
	FactAID         int64    `json:"fact_a_id"`
	FactBID         int64    `json:"fact_b_id"`
	FactAText       string   `json:"fact_a_text"`
	FactAtextEn     string   `json:"fact_a_text_en,omitempty"`
	FactBText       string   `json:"fact_b_text"`
	FactBtextEn     string   `json:"fact_b_text_en,omitempty"`
	FactASourceURLs []string `json:"fact_a_source_urls"`
	FactBSourceURLs []string `json:"fact_b_source_urls"`
	// FactAQuote and FactBQuote are the source quote of the earliest source of
	// each fact, "" when no source has one. Only GetContradictionByID fills
	// them (the complaint judge reads them).
	FactAQuote         string     `json:"fact_a_quote,omitempty"`
	FactBQuote         string     `json:"fact_b_quote,omitempty"`
	FactADate          *time.Time `json:"fact_a_date"`
	FactBDate          *time.Time `json:"fact_b_date"`
	Reason             string     `json:"reason"`
	ReasonEn           string     `json:"reason_en,omitempty"`
	DetectedAt         time.Time  `json:"detected_at"`
	BundleID           *int64     `json:"bundle_id"`
	BundleLabel        *string    `json:"bundle_label"`
	BundleLabelEn      *string    `json:"bundle_label_en,omitempty"`
	BundleSummary      *string    `json:"bundle_summary"`
	BundleSummaryEn    *string    `json:"bundle_summary_en,omitempty"`
	BundleSize         int        `json:"bundle_size"`
	FactAStanceID      *int64     `json:"fact_a_stance_id"`
	FactBStanceID      *int64     `json:"fact_b_stance_id"`
	FactAStanceLabel   *string    `json:"fact_a_stance_label"`
	FactAStanceLabelEn *string    `json:"fact_a_stance_label_en,omitempty"`
	FactBStanceLabel   *string    `json:"fact_b_stance_label"`
	FactBStanceLabelEn *string    `json:"fact_b_stance_label_en,omitempty"`
}

// ContradictionAuditRow is a contradiction enriched with the person name,
// used by the audit tool which may span all persons in the DB.
type ContradictionAuditRow struct {
	ContradictionWithFacts
	PersonName string `json:"person_name"`
	// LastAuditHash pins the last KEEP verdict to its exact inputs (see
	// contradiction.AuditInputHash); nil = never audited-kept.
	LastAuditHash *string `json:"last_audit_hash,omitempty"`
}

// ListContradictions returns recorded contradictions, optionally filtered to a
// single person, optionally ordered randomly (for sampled audits), optionally
// capped at `limit` rows (limit <= 0 means no cap). It joins facts +
// fact_sources + persons in a single query to avoid N round-trips.
func (s *PostgresStorer) ListContradictions(ctx context.Context, personID *int64, random bool, limit int) ([]ContradictionAuditRow, error) {
	var (
		whereClause string
		orderClause string
		args        []any
	)
	if personID != nil {
		whereClause = "WHERE c.person_id = $1"
		args = append(args, *personID)
	}
	if random {
		orderClause = "ORDER BY RANDOM()"
	} else {
		orderClause = "ORDER BY c.detected_at DESC"
	}
	if limit > 0 {
		args = append(args, limit)
		orderClause += fmt.Sprintf(" LIMIT $%d", len(args))
	}
	return s.listContradictions(ctx, whereClause, orderClause, args)
}

// listContradictions runs the ListContradictions query with the given WHERE
// and ORDER BY clauses (c is contradictions, p persons).
func (s *PostgresStorer) listContradictions(ctx context.Context, whereClause, orderClause string, args []any) ([]ContradictionAuditRow, error) {
	query := fmt.Sprintf(`
		SELECT c.id, c.person_id, p.name AS person_name,
		       c.fact_a_id, c.fact_b_id,
		       fa.fact_text AS fact_a_text, fb.fact_text AS fact_b_text,
		       COALESCE(fsa.urls, '{}') AS fact_a_urls, COALESCE(fsb.urls, '{}') AS fact_b_urls,
		       COALESCE(fsa.earliest, fa.created_at) AS fact_a_date,
		       COALESCE(fsb.earliest, fb.created_at) AS fact_b_date,
		       c.reason, c.detected_at, c.last_audit_hash,
		       fa.statement_date::timestamp AT TIME ZONE 'UTC', fa.statement_date_precision, fb.statement_date::timestamp AT TIME ZONE 'UTC', fb.statement_date_precision
		FROM contradictions c
		JOIN persons p ON p.id = c.person_id
		JOIN facts fa ON fa.id = c.fact_a_id
		JOIN facts fb ON fb.id = c.fact_b_id
		LEFT JOIN LATERAL (
			SELECT array_agg(source_url ORDER BY published_at ASC NULLS LAST) AS urls,
			       MIN(published_at) AS earliest
			FROM fact_sources WHERE fact_id = fa.id
		) fsa ON true
		LEFT JOIN LATERAL (
			SELECT array_agg(source_url ORDER BY published_at ASC NULLS LAST) AS urls,
			       MIN(published_at) AS earliest
			FROM fact_sources WHERE fact_id = fb.id
		) fsb ON true
		%s
		%s`, whereClause, orderClause)

	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("failed to query contradictions: %w", err)
	}
	defer rows.Close()

	out := make([]ContradictionAuditRow, 0)
	for rows.Next() {
		var r ContradictionAuditRow
		var sda, sdb statementDate
		if err := rows.Scan(
			&r.ID, &r.PersonID, &r.PersonName,
			&r.FactAID, &r.FactBID,
			&r.FactAText, &r.FactBText,
			&r.FactASourceURLs, &r.FactBSourceURLs,
			&r.FactADate, &r.FactBDate,
			&r.Reason, &r.DetectedAt, &r.LastAuditHash,
			&sda.date, &sda.precision, &sdb.date, &sdb.precision,
		); err != nil {
			return nil, fmt.Errorf("failed to scan contradiction row: %w", err)
		}
		r.FactADate, r.FactBDate = sda.of(r.FactADate), sdb.of(r.FactBDate)
		out = append(out, r)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("error iterating contradictions: %w", err)
	}
	return out, nil
}

// SetContradictionAuditHash pins an audit KEEP verdict to its inputs (see
// contradiction.AuditInputHash). The audit step skips rows whose stored hash
// matches the freshly computed one.
func (s *PostgresStorer) SetContradictionAuditHash(ctx context.Context, id int64, hash string) error {
	tag, err := s.pool.Exec(ctx,
		`UPDATE contradictions SET last_audit_hash = $2 WHERE id = $1`, id, hash)
	if err != nil {
		return fmt.Errorf("failed to set audit hash for contradiction %d: %w", id, err)
	}
	if tag.RowsAffected() == 0 {
		return fmt.Errorf("contradiction %d not found when setting audit hash", id)
	}
	return nil
}

// GetPersonContradictions returns a page of contradictions for a person together with the total count.
func (s *PostgresStorer) GetPersonContradictions(ctx context.Context, personID int64, limit int, offset int) (*PaginatedContradictions, error) {
	// Bundle key is the contradiction's bundle_stance_id (set at insert by the fast path
	// or upgraded later by the Bundler). Solo rows (NULL) become singleton bundles via the
	// 'solo:'||id key. bundle_size counts pairs sharing the same bundle key for this person,
	// so the frontend can render one card per bundle with a "N contradictions" badge.
	// Per-fact stance is pulled via stance_document_facts → stance_documents so the frontend
	// can render "topic A ↔ topic B" headers on cross-stance solos (bundle_id NULL but both
	// fact stance ids non-null) and a data-quality badge when either is NULL (post-migration
	// invariant violation: a fact landed without a stance).
	query := `
		WITH contradiction_rows AS (
			SELECT c.id, c.person_id, c.fact_a_id, c.fact_b_id,
				   fa.fact_text AS fact_a_text, COALESCE(fa.fact_text_en,'') AS fact_a_text_en,
				   fb.fact_text AS fact_b_text, COALESCE(fb.fact_text_en,'') AS fact_b_text_en,
				   COALESCE(fsa.urls, '{}') AS fact_a_urls, COALESCE(fsb.urls, '{}') AS fact_b_urls,
				   COALESCE(fa.statement_date::timestamp AT TIME ZONE 'UTC', fsa.earliest, fa.created_at) AS fact_a_date,
				   COALESCE(fb.statement_date::timestamp AT TIME ZONE 'UTC', fsb.earliest, fb.created_at) AS fact_b_date,
				   c.reason, COALESCE(c.reason_en,'') AS reason_en, c.detected_at,
				   c.bundle_stance_id,
				   sd.cluster_label AS bundle_label,
				   sd.label_en      AS bundle_label_en,
				   sd.summary       AS bundle_summary,
				   sd.summary_en    AS bundle_summary_en,
				   sda.id           AS fact_a_stance_id,
				   sda.cluster_label AS fact_a_stance_label,
				   sda.label_en      AS fact_a_stance_label_en,
				   sdb.id           AS fact_b_stance_id,
				   sdb.cluster_label AS fact_b_stance_label,
				   sdb.label_en      AS fact_b_stance_label_en,
				   COALESCE(c.bundle_stance_id::text, 'solo:'||c.id::text) AS bundle_key
			FROM contradictions c
			JOIN facts fa ON fa.id = c.fact_a_id
			JOIN facts fb ON fb.id = c.fact_b_id
			LEFT JOIN stance_documents sd ON sd.id = c.bundle_stance_id
			-- Per-fact stance, scoped to THIS person's stances: a fact mentioned by
			-- several persons is a member of one stance per person, so an unscoped
			-- join would both multiply rows (breaking bundle_size/total_count/
			-- pagination) and pick an arbitrary person's topic label.
			LEFT JOIN LATERAL (
				SELECT s.id, s.cluster_label, s.label_en
				FROM stance_document_facts sdfa
				JOIN stance_documents s ON s.id = sdfa.stance_document_id
				WHERE sdfa.fact_id = c.fact_a_id AND s.person_id = c.person_id
				LIMIT 1
			) sda ON true
			LEFT JOIN LATERAL (
				SELECT s.id, s.cluster_label, s.label_en
				FROM stance_document_facts sdfb
				JOIN stance_documents s ON s.id = sdfb.stance_document_id
				WHERE sdfb.fact_id = c.fact_b_id AND s.person_id = c.person_id
				LIMIT 1
			) sdb ON true
			LEFT JOIN LATERAL (
				SELECT array_agg(source_url ORDER BY published_at ASC NULLS LAST) AS urls,
				       MIN(published_at) AS earliest
				FROM fact_sources WHERE fact_id = fa.id
			) fsa ON true
			LEFT JOIN LATERAL (
				SELECT array_agg(source_url ORDER BY published_at ASC NULLS LAST) AS urls,
				       MIN(published_at) AS earliest
				FROM fact_sources WHERE fact_id = fb.id
			) fsb ON true
			WHERE c.person_id = $1
		),
		ranked AS (
			SELECT *,
			       COUNT(*) OVER (PARTITION BY bundle_key) AS bundle_size
			FROM contradiction_rows
		)
		SELECT id, person_id, fact_a_id, fact_b_id,
		       fact_a_text, fact_a_text_en, fact_b_text, fact_b_text_en,
		       fact_a_urls, fact_b_urls,
		       fact_a_date, fact_b_date,
		       reason, reason_en, detected_at,
		       bundle_stance_id, bundle_label, bundle_label_en, bundle_summary, bundle_summary_en, bundle_size,
		       fact_a_stance_id, fact_a_stance_label, fact_a_stance_label_en,
		       fact_b_stance_id, fact_b_stance_label, fact_b_stance_label_en,
		       (SELECT COUNT(*) FROM contradiction_rows) AS total_count
		FROM ranked
		ORDER BY bundle_size DESC, bundle_stance_id NULLS LAST, detected_at DESC
		LIMIT $2 OFFSET $3`

	rows, err := s.pool.Query(ctx, query, personID, limit, offset)
	if err != nil {
		return nil, fmt.Errorf("failed to query contradictions for person %d: %w", personID, err)
	}
	defer rows.Close()

	result := &PaginatedContradictions{Items: make([]ContradictionWithFacts, 0)}
	for rows.Next() {
		var c ContradictionWithFacts
		err := rows.Scan(&c.ID, &c.PersonID, &c.FactAID, &c.FactBID,
			&c.FactAText, &c.FactAtextEn, &c.FactBText, &c.FactBtextEn,
			&c.FactASourceURLs, &c.FactBSourceURLs,
			&c.FactADate, &c.FactBDate,
			&c.Reason, &c.ReasonEn, &c.DetectedAt,
			&c.BundleID, &c.BundleLabel, &c.BundleLabelEn, &c.BundleSummary, &c.BundleSummaryEn, &c.BundleSize,
			&c.FactAStanceID, &c.FactAStanceLabel, &c.FactAStanceLabelEn,
			&c.FactBStanceID, &c.FactBStanceLabel, &c.FactBStanceLabelEn,
			&result.Total)
		if err != nil {
			return nil, fmt.Errorf("failed to scan contradiction: %w", err)
		}
		result.Items = append(result.Items, c)
	}

	if rows.Err() != nil {
		return nil, fmt.Errorf("error iterating contradictions: %w", rows.Err())
	}

	// If no rows returned, we still need the total count
	if len(result.Items) == 0 {
		countQuery := `SELECT COUNT(*) FROM contradictions WHERE person_id = $1`
		if err := s.pool.QueryRow(ctx, countQuery, personID).Scan(&result.Total); err != nil {
			return nil, fmt.Errorf("failed to count contradictions for person %d: %w", personID, err)
		}
	}

	return result, nil
}

// DeleteContradictionsForPerson deletes all contradictions for a person and recalculates their honesty score.
// Returns the number of contradictions deleted.
func (s *PostgresStorer) DeleteContradictionsForPerson(ctx context.Context, personID int64) (int, error) {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return 0, fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	// Delete all contradictions for this person
	query := `DELETE FROM contradictions WHERE person_id = $1`
	result, err := tx.Exec(ctx, query, personID)
	if err != nil {
		return 0, fmt.Errorf("failed to delete contradictions for person %d: %w", personID, err)
	}

	deletedCount := int(result.RowsAffected())

	// Reset pair tracking so all pairs are re-analyzed on next detection run
	_, err = tx.Exec(ctx, `DELETE FROM fact_pair_checks WHERE person_id = $1`, personID)
	if err != nil {
		return 0, fmt.Errorf("failed to reset pair checks for person %d: %w", personID, err)
	}

	if deletedCount > 0 {
		// Recalculate honesty score (will be 1.0 since no contradictions remain)
		factsCount, err := s.getPersonFactsCount(ctx, tx, personID)
		if err != nil {
			return 0, fmt.Errorf("failed to get facts count for person %d: %w", personID, err)
		}

		// No contradiction left: 1.0 at any rate.
		honestyScore := core.ComputeHonestyScore(factsCount, core.ContradictionStats{}, 0)

		if err := s.updatePersonHonestyScore(ctx, tx, personID, honestyScore, nil, honestyScore); err != nil {
			return 0, fmt.Errorf("failed to update honesty score for person %d: %w", personID, err)
		}

		// Emit person_updated event
		payload := map[string]interface{}{
			"honesty_score":          honestyScore,
			"contradictions_deleted": deletedCount,
		}
		if err := s.insertEvent(ctx, tx, "person_updated", "person", personID, payload); err != nil {
			return 0, fmt.Errorf("failed to emit person_updated event: %w", err)
		}

		log.Info().Int64("person_id", personID).Int("deleted", deletedCount).Float64("honesty_score", honestyScore).Msg("Deleted contradictions and updated honesty score")
	}

	if err := tx.Commit(ctx); err != nil {
		return 0, fmt.Errorf("failed to commit transaction: %w", err)
	}

	return deletedCount, nil
}

// GetContradictionByID retrieves a single contradiction enriched with both fact texts,
// source URLs, earliest dates and the quote of the earliest source of each fact.
// Returns pgx.ErrNoRows if not found.
func (s *PostgresStorer) GetContradictionByID(ctx context.Context, id int64) (*ContradictionWithFacts, error) {
	query := `
		SELECT c.id, c.person_id, c.fact_a_id, c.fact_b_id,
		       fa.fact_text, fb.fact_text,
		       COALESCE(fsa.urls, '{}'), COALESCE(fsb.urls, '{}'),
		       COALESCE(fsa.earliest, fa.created_at), COALESCE(fsb.earliest, fb.created_at),
		       COALESCE(fsa.quotes[1], ''), COALESCE(fsb.quotes[1], ''),
		       c.reason, c.detected_at,
		       fa.statement_date::timestamp AT TIME ZONE 'UTC', fa.statement_date_precision, fb.statement_date::timestamp AT TIME ZONE 'UTC', fb.statement_date_precision
		FROM contradictions c
		JOIN facts fa ON fa.id = c.fact_a_id
		JOIN facts fb ON fb.id = c.fact_b_id
		LEFT JOIN LATERAL (
			SELECT array_agg(source_url ORDER BY published_at ASC NULLS LAST) AS urls,
			       array_agg(COALESCE(original_quote, '') ORDER BY published_at ASC NULLS LAST) AS quotes,
			       MIN(published_at) AS earliest
			FROM fact_sources WHERE fact_id = fa.id
		) fsa ON true
		LEFT JOIN LATERAL (
			SELECT array_agg(source_url ORDER BY published_at ASC NULLS LAST) AS urls,
			       array_agg(COALESCE(original_quote, '') ORDER BY published_at ASC NULLS LAST) AS quotes,
			       MIN(published_at) AS earliest
			FROM fact_sources WHERE fact_id = fb.id
		) fsb ON true
		WHERE c.id = $1`

	var r ContradictionWithFacts
	var sda, sdb statementDate
	err := s.pool.QueryRow(ctx, query, id).Scan(
		&r.ID, &r.PersonID, &r.FactAID, &r.FactBID,
		&r.FactAText, &r.FactBText,
		&r.FactASourceURLs, &r.FactBSourceURLs,
		&r.FactADate, &r.FactBDate,
		&r.FactAQuote, &r.FactBQuote,
		&r.Reason, &r.DetectedAt,
		&sda.date, &sda.precision, &sdb.date, &sdb.precision,
	)
	r.FactADate, r.FactBDate = sda.of(r.FactADate), sdb.of(r.FactBDate)
	if err != nil {
		if err == pgx.ErrNoRows {
			return nil, pgx.ErrNoRows
		}
		return nil, fmt.Errorf("failed to get contradiction %d: %w", id, err)
	}
	return &r, nil
}

// DeleteContradiction removes a single contradiction by ID, resets the corresponding
// fact_pair_checks row so the pair can be re-analyzed, and recomputes the owner's
// honesty score. Returns pgx.ErrNoRows if the contradiction does not exist.
func (s *PostgresStorer) DeleteContradiction(ctx context.Context, id int64) error {
	return s.removeContradiction(ctx, id, false)
}

// FindCrossPersonContradictionIDs returns the ids of contradictions whose two
// facts share no common subject (mentioned) person — the "different subjects"
// false-positive class that the cross-person guard in RecordContradiction now
// prevents at insert time. Used by `dm contradictions prune-cross-person` to
// clean up rows recorded before the guard existed.
func (s *PostgresStorer) FindCrossPersonContradictionIDs(ctx context.Context) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id
		FROM contradictions c
		WHERE NOT EXISTS (
			SELECT 1
			FROM fact_sources fsa
			JOIN fact_mentions fma ON fma.fact_source_id = fsa.id
			JOIN fact_sources fsb ON fsb.fact_id = c.fact_b_id
			JOIN fact_mentions fmb ON fmb.fact_source_id = fsb.id
			                       AND fmb.person_id = fma.person_id
			WHERE fsa.fact_id = c.fact_a_id
		)
		ORDER BY c.id`)
	if err != nil {
		return nil, fmt.Errorf("querying cross-person contradictions: %w", err)
	}
	defer rows.Close()

	var ids []int64
	for rows.Next() {
		var id int64
		if err := rows.Scan(&id); err != nil {
			return nil, fmt.Errorf("scanning contradiction id: %w", err)
		}
		ids = append(ids, id)
	}
	return ids, rows.Err()
}

// DismissContradictionAsFalsePositive removes a contradiction the auditor judged
// a false positive. Unlike DeleteContradiction it does NOT clear the pair check —
// it records a NEGATIVE fact_pair_checks row (is_contradiction=false) so the daily
// detector treats the pair as already-judged-not-contradictory and will not
// re-raise it. Without this, an auditor-deleted pair would be re-detected next
// run, re-approved by the (more lenient) insertion verifier, and re-deleted at
// the next audit — thrashing every cycle. Recomputes the owner's honesty score.
// Returns pgx.ErrNoRows if the contradiction does not exist.
func (s *PostgresStorer) DismissContradictionAsFalsePositive(ctx context.Context, id int64) error {
	return s.removeContradiction(ctx, id, true)
}

// removeContradiction is the shared body of DeleteContradiction and
// DismissContradictionAsFalsePositive. suppressReDetection controls the
// fact_pair_checks handling: false clears the row (pair re-analyzed later —
// self-correcting), true upserts a negative row (pair suppressed from future
// detection). Either way the owner's honesty score is recomputed.
func (s *PostgresStorer) removeContradiction(ctx context.Context, id int64, suppressReDetection bool) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("failed to begin transaction: %w", err)
	}
	defer tx.Rollback(ctx)

	var personID, factAID, factBID int64
	err = tx.QueryRow(ctx,
		`SELECT person_id, fact_a_id, fact_b_id FROM contradictions WHERE id = $1`, id,
	).Scan(&personID, &factAID, &factBID)
	if err != nil {
		if err == pgx.ErrNoRows {
			return pgx.ErrNoRows
		}
		return fmt.Errorf("failed to load contradiction %d: %w", id, err)
	}

	if _, err := tx.Exec(ctx, `DELETE FROM contradictions WHERE id = $1`, id); err != nil {
		return fmt.Errorf("failed to delete contradiction %d: %w", id, err)
	}

	if suppressReDetection {
		// Record the pair as dismissed so the detector skips it for good (the
		// stale-verdict re-judge leaves dismissed rows alone).
		// fact_pair_checks enforces fact_a_id < fact_b_id (canonical ordering).
		a, b := factAID, factBID
		if a > b {
			a, b = b, a
		}
		if _, err := tx.Exec(ctx,
			`INSERT INTO fact_pair_checks (person_id, fact_a_id, fact_b_id, is_contradiction, dismissed)
			 VALUES ($1, $2, $3, false, true)
			 ON CONFLICT (person_id, fact_a_id, fact_b_id)
			 DO UPDATE SET is_contradiction = false, dismissed = true, checked_at = NOW()`,
			personID, a, b); err != nil {
			return fmt.Errorf("failed to record negative pair check: %w", err)
		}
	} else {
		// fact_pair_checks enforces fact_a_id < fact_b_id; check both orderings to be safe.
		if _, err := tx.Exec(ctx,
			`DELETE FROM fact_pair_checks
			 WHERE person_id = $1
			   AND ((fact_a_id = $2 AND fact_b_id = $3) OR (fact_a_id = $3 AND fact_b_id = $2))`,
			personID, factAID, factBID); err != nil {
			return fmt.Errorf("failed to reset pair check: %w", err)
		}
	}

	factsCount, err := s.getPersonFactsCount(ctx, tx, personID)
	if err != nil {
		return fmt.Errorf("failed to get facts count: %w", err)
	}
	stats, err := s.getPersonContradictionStats(ctx, tx, personID)
	if err != nil {
		return fmt.Errorf("failed to compute contradiction stats: %w", err)
	}
	rate, err := scoreRate(ctx, tx)
	if err != nil {
		return err
	}
	ownScore := core.ComputeHonestyScore(factsCount, stats, rate)

	var entityType *string
	if err := tx.QueryRow(ctx, `SELECT entity_type FROM persons WHERE id = $1`, personID).Scan(&entityType); err != nil {
		return fmt.Errorf("failed to get entity_type for person %d: %w", personID, err)
	}
	var membersScore *float64
	if entityType != nil && *entityType != "human" {
		membersScore, err = s.computeMembersScore(ctx, tx, personID)
		if err != nil {
			return fmt.Errorf("failed to compute members score: %w", err)
		}
	}
	aggregate := core.ComputeAggregateScore(ownScore, membersScore)

	if err := s.updatePersonHonestyScore(ctx, tx, personID, ownScore, membersScore, aggregate); err != nil {
		return fmt.Errorf("failed to update honesty score: %w", err)
	}

	payload := map[string]interface{}{
		"honesty_score":          aggregate,
		"contradiction_id":       id,
		"contradictions_deleted": 1,
	}
	if err := s.insertEvent(ctx, tx, "person_updated", "person", personID, payload); err != nil {
		return fmt.Errorf("failed to emit person_updated event: %w", err)
	}

	if err := s.recomputeParentEntityScores(ctx, tx, personID); err != nil {
		return fmt.Errorf("failed to recompute parent entity scores: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("failed to commit deletion: %w", err)
	}

	log.Info().
		Int64("contradiction_id", id).
		Int64("person_id", personID).
		Float64("honesty_score", aggregate).
		Bool("suppressed", suppressReDetection).
		Msg("Deleted single contradiction")
	return nil
}

// SoloContradictionForBundling is the minimal payload the Bundler needs to retroactively
// resolve a contradiction whose bundle_stance_id is NULL.
type SoloContradictionForBundling struct {
	ID        int64
	PersonID  int64
	FactAID   int64
	FactBID   int64
	FactAText string
	FactBText string
	Reason    string
}

// BundleBackfillFastPath sets bundle_stance_id on every contradiction whose two facts now
// share a stance via stance_document_facts, but where the column is currently NULL. Returns
// the number of rows updated. Mirrors the SQL in migration 20260503000000 — used by the
// `dm contradictions bundle-backfill` CLI to re-run after a `stances rebuild` pass since
// the migration ran before stances were retroactively populated.
func (s *PostgresStorer) BundleBackfillFastPath(ctx context.Context, personID *int64) (int64, error) {
	args := []any{}
	personFilter := ""
	if personID != nil {
		args = append(args, *personID)
		personFilter = "AND c.person_id = $1"
	}
	// The shared stance must belong to the contradiction's own person (facts can
	// be members of one stance per voter); DISTINCT ON keeps the pick
	// deterministic should a person ever hold duplicate memberships.
	query := fmt.Sprintf(`
		WITH shared AS (
			SELECT DISTINCT ON (c.id) c.id AS contradiction_id, sa.stance_document_id
			FROM contradictions c
			JOIN stance_document_facts sa ON sa.fact_id = c.fact_a_id
			JOIN stance_document_facts sb ON sb.fact_id = c.fact_b_id
			                              AND sb.stance_document_id = sa.stance_document_id
			JOIN stance_documents sd ON sd.id = sa.stance_document_id
			                         AND sd.person_id = c.person_id
			WHERE c.bundle_stance_id IS NULL
			%s
			ORDER BY c.id, sa.stance_document_id
		)
		UPDATE contradictions c
		SET bundle_stance_id = shared.stance_document_id
		FROM shared
		WHERE shared.contradiction_id = c.id
		  AND c.bundle_stance_id IS NULL`, personFilter)
	tag, err := s.pool.Exec(ctx, query, args...)
	if err != nil {
		return 0, fmt.Errorf("bundle backfill fast path: %w", err)
	}
	return tag.RowsAffected(), nil
}

// ListSoloContradictionsForBundling returns contradictions with bundle_stance_id IS NULL
// joined to fact text + reason, ready for the Bundler. Optionally scoped to a person and
// capped at limit (limit <= 0 means no cap).
func (s *PostgresStorer) ListSoloContradictionsForBundling(ctx context.Context, personID *int64, limit int) ([]SoloContradictionForBundling, error) {
	args := []any{}
	whereExtra := ""
	if personID != nil {
		args = append(args, *personID)
		whereExtra = fmt.Sprintf("AND c.person_id = $%d", len(args))
	}
	limitClause := ""
	if limit > 0 {
		args = append(args, limit)
		limitClause = fmt.Sprintf("LIMIT $%d", len(args))
	}
	query := fmt.Sprintf(`
		SELECT c.id, c.person_id, c.fact_a_id, c.fact_b_id,
		       fa.fact_text, fb.fact_text, COALESCE(c.reason, '')
		FROM contradictions c
		JOIN facts fa ON fa.id = c.fact_a_id
		JOIN facts fb ON fb.id = c.fact_b_id
		WHERE c.bundle_stance_id IS NULL
		%s
		ORDER BY c.person_id, c.id
		%s`, whereExtra, limitClause)
	rows, err := s.pool.Query(ctx, query, args...)
	if err != nil {
		return nil, fmt.Errorf("list solo contradictions: %w", err)
	}
	defer rows.Close()
	out := make([]SoloContradictionForBundling, 0)
	for rows.Next() {
		var r SoloContradictionForBundling
		if err := rows.Scan(&r.ID, &r.PersonID, &r.FactAID, &r.FactBID, &r.FactAText, &r.FactBText, &r.Reason); err != nil {
			return nil, fmt.Errorf("scan solo contradiction: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// SetContradictionBundleStance assigns a bundle stance to a contradiction and recomputes the
// person's honesty score (since the bundle count may have changed). No-op if the contradiction
// already had the same bundle_stance_id. Used by the Bundler after an LLM-verified resolution.
func (s *PostgresStorer) SetContradictionBundleStance(ctx context.Context, contradictionID int64, bundleStanceID int64) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return fmt.Errorf("begin tx: %w", err)
	}
	defer tx.Rollback(ctx)

	var personID int64
	var prev *int64
	err = tx.QueryRow(ctx,
		`UPDATE contradictions SET bundle_stance_id = $1 WHERE id = $2 AND (bundle_stance_id IS DISTINCT FROM $1)
		 RETURNING person_id, bundle_stance_id`, bundleStanceID, contradictionID).Scan(&personID, &prev)
	if err != nil {
		if err == pgx.ErrNoRows {
			// Already had that bundle — nothing to do.
			return nil
		}
		return fmt.Errorf("updating contradiction %d bundle: %w", contradictionID, err)
	}

	if err := s.recomputeHonestyScoreAndEmitEvents(ctx, tx, personID, contradictionID); err != nil {
		return fmt.Errorf("recompute after bundle update: %w", err)
	}

	if err := tx.Commit(ctx); err != nil {
		return fmt.Errorf("commit bundle update: %w", err)
	}
	return nil
}

// ContradictionsSharingFact returns the person's contradictions holding
// factA or factB but not both, oldest first, with the facts of the existing
// row and of the new pair (factA, factB) for the restatement judge.
func (s *PostgresStorer) ContradictionsSharingFact(ctx context.Context, personID, factA, factB int64, newReason string) ([]core.SharedContradiction, error) {
	rows, err := s.pool.Query(ctx, `
		WITH cand AS (
			SELECT c.id, c.reason, c.created_at,
			       CASE WHEN c.fact_a_id IN ($2, $3) THEN c.fact_a_id ELSE c.fact_b_id END AS shared,
			       CASE WHEN c.fact_a_id IN ($2, $3) THEN c.fact_b_id ELSE c.fact_a_id END AS other
			FROM contradictions c
			WHERE c.person_id = $1
			  AND (c.fact_a_id IN ($2, $3) OR c.fact_b_id IN ($2, $3))
			  AND NOT (c.fact_a_id IN ($2, $3) AND c.fact_b_id IN ($2, $3)))
		SELECT cand.id, cand.shared, cand.other, cand.reason, cand.created_at,
		       fs.fact_text, (SELECT min(published_at) FROM fact_sources WHERE fact_id = cand.shared),
		       fo.fact_text, (SELECT min(published_at) FROM fact_sources WHERE fact_id = cand.other),
		       CASE WHEN cand.shared = $2 THEN $3 ELSE $2 END AS new_other,
		       fs.statement_date::timestamp AT TIME ZONE 'UTC', fs.statement_date_precision,
		       fo.statement_date::timestamp AT TIME ZONE 'UTC', fo.statement_date_precision
		FROM cand
		JOIN facts fs ON fs.id = cand.shared
		JOIN facts fo ON fo.id = cand.other
		ORDER BY cand.created_at, cand.id`, personID, factA, factB)
	if err != nil {
		return nil, fmt.Errorf("listing the contradictions sharing a fact: %w", err)
	}
	defer rows.Close()
	var out []core.SharedContradiction
	for rows.Next() {
		var c core.SharedContradiction
		var reason *string
		var sds, sdo statementDate
		if err := rows.Scan(&c.ID, &c.SharedFact, &c.OtherFact, &reason, &c.CreatedAt,
			&c.SharedText, &c.SharedDate, &c.OtherText, &c.OtherDate, &c.NewOther,
			&sds.date, &sds.precision, &sdo.date, &sdo.precision); err != nil {
			return nil, fmt.Errorf("scanning a contradiction sharing a fact: %w", err)
		}
		c.SharedDate, c.OtherDate = sds.of(c.SharedDate), sdo.of(c.OtherDate)
		if reason != nil {
			c.Reason = *reason
		}
		c.NewReason = newReason
		out = append(out, c)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}
	for i := range out {
		var sd statementDate
		if err := s.pool.QueryRow(ctx, `SELECT f.fact_text, (SELECT min(published_at) FROM fact_sources WHERE fact_id = f.id),
			f.statement_date::timestamp AT TIME ZONE 'UTC', f.statement_date_precision FROM facts f WHERE f.id = $1`, out[i].NewOther).
			Scan(&out[i].NewText, &out[i].NewDate, &sd.date, &sd.precision); err != nil {
			return nil, fmt.Errorf("loading fact %d: %w", out[i].NewOther, err)
		}
		out[i].NewDate = sd.of(out[i].NewDate)
	}
	return out, nil
}

// RecordRestatedPair records a pair that restates a recorded contradiction
// as judged (a dismissed pair check): the detector does not raise it again.
func (s *PostgresStorer) RecordRestatedPair(ctx context.Context, personID, factA, factB int64) error {
	a, b := factA, factB
	if a > b {
		a, b = b, a
	}
	if _, err := s.pool.Exec(ctx, `
		INSERT INTO fact_pair_checks (person_id, fact_a_id, fact_b_id, is_contradiction, dismissed)
		VALUES ($1, $2, $3, false, true)
		ON CONFLICT (person_id, fact_a_id, fact_b_id)
		DO UPDATE SET is_contradiction = false, dismissed = true, checked_at = NOW()`, personID, a, b); err != nil {
		return fmt.Errorf("recording the restated pair %d-%d: %w", a, b, err)
	}
	return nil
}

// DedupeRow is a recorded contradiction as the dedupe command reads it.
type DedupeRow struct {
	ID        int64
	PersonID  int64
	Person    string
	FactA     int64
	FactB     int64
	Reason    string
	CreatedAt time.Time
}

// ListContradictionsForDedupe returns every recorded contradiction (of one
// person when name is set), for the restatement check.
func (s *PostgresStorer) ListContradictionsForDedupe(ctx context.Context, name string) ([]DedupeRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT c.id, c.person_id, p.name, c.fact_a_id, c.fact_b_id, COALESCE(c.reason, ''), c.created_at
		FROM contradictions c JOIN persons p ON p.id = c.person_id
		WHERE ($1 = '' OR p.name = $1)
		ORDER BY c.created_at, c.id`, name)
	if err != nil {
		return nil, fmt.Errorf("listing contradictions: %w", err)
	}
	defer rows.Close()
	var out []DedupeRow
	for rows.Next() {
		var r DedupeRow
		if err := rows.Scan(&r.ID, &r.PersonID, &r.Person, &r.FactA, &r.FactB, &r.Reason, &r.CreatedAt); err != nil {
			return nil, fmt.Errorf("scanning contradiction: %w", err)
		}
		out = append(out, r)
	}
	return out, rows.Err()
}
