package storage

import (
	"context"
	"fmt"
)

// QueuedFactPair is one pair a producer asked the detector to judge.
type QueuedFactPair struct {
	FactAID int64
	FactBID int64
	Source  string
}

// EnqueueFactPairsBySharedSourceKey enqueues, for every person, each pair of
// their active facts where one fact has a source of type sourceTypeA and the
// other a source of type sourceTypeB and both sources carry the same
// non-empty value under metadata key. A group member resolves to its parent
// (the fact that actually takes part in detection). Pairs already judged
// (fact_pair_checks) or already queued are skipped, so the call is idempotent
// and cheap to repeat nightly. One set-based statement — no per-fact
// bookkeeping in the producers. Returns the number of pairs enqueued.
func (s *PostgresStorer) EnqueueFactPairsBySharedSourceKey(ctx context.Context, sourceTypeA, sourceTypeB, key, source string) (int, error) {
	res, err := s.pool.Exec(ctx, `
		WITH a AS (
			SELECT fm.person_id, COALESCE(dup.general_fact_id, fs.fact_id) AS fact_id, fs.metadata->>$3 AS k
			FROM fact_sources fs
			JOIN fact_mentions fm ON fm.fact_source_id = fs.id
			LEFT JOIN fact_relations dup ON dup.specific_fact_id = fs.fact_id AND dup.relation_type = 'duplicate'
			WHERE fs.source_type = $1 AND COALESCE(fs.metadata->>$3, '') <> ''
		),
		b AS (
			SELECT fm.person_id, COALESCE(dup.general_fact_id, fs.fact_id) AS fact_id, fs.metadata->>$3 AS k
			FROM fact_sources fs
			JOIN fact_mentions fm ON fm.fact_source_id = fs.id
			LEFT JOIN fact_relations dup ON dup.specific_fact_id = fs.fact_id AND dup.relation_type = 'duplicate'
			WHERE fs.source_type = $2 AND COALESCE(fs.metadata->>$3, '') <> ''
		),
		candidates AS (
			SELECT DISTINCT a.person_id,
			       LEAST(a.fact_id, b.fact_id) AS fact_a_id,
			       GREATEST(a.fact_id, b.fact_id) AS fact_b_id
			FROM a
			JOIN b ON b.person_id = a.person_id AND b.k = a.k AND b.fact_id <> a.fact_id
			JOIN persons p ON p.id = a.person_id AND `+CandidateSQL+`
		)
		INSERT INTO fact_pair_queue (person_id, fact_a_id, fact_b_id, source)
		SELECT c.person_id, c.fact_a_id, c.fact_b_id, $4
		FROM candidates c
		WHERE NOT EXISTS (
			SELECT 1 FROM fact_pair_checks pc
			WHERE pc.person_id = c.person_id AND pc.fact_a_id = c.fact_a_id AND pc.fact_b_id = c.fact_b_id
		)
		ON CONFLICT DO NOTHING`, sourceTypeA, sourceTypeB, key, source)
	if err != nil {
		return 0, fmt.Errorf("enqueuing pairs by shared source key %q: %w", key, err)
	}
	return int(res.RowsAffected()), nil
}

// EnqueueFactPair asks the detector to judge one pair of the person's facts
// (in either order). A pair already queued stays as it is.
func (s *PostgresStorer) EnqueueFactPair(ctx context.Context, personID, factA, factB int64, source string) error {
	if factA > factB {
		factA, factB = factB, factA
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fact_pair_queue (person_id, fact_a_id, fact_b_id, source) VALUES ($1, $2, $3, $4)
		ON CONFLICT DO NOTHING`, personID, factA, factB, source)
	if err != nil {
		return fmt.Errorf("queueing pair %d-%d: %w", factA, factB, err)
	}
	return nil
}

// ConsumeQueuedPairsForPerson atomically takes (returns and deletes) up to
// limit of the person's oldest queued pairs. The queue is transient: whatever
// the detector does with them next, they are gone — a pair that does not end
// up in fact_pair_checks (judge error, unusable fact) is simply re-enqueued
// by the producer's next idempotent run.
func (s *PostgresStorer) ConsumeQueuedPairsForPerson(ctx context.Context, personID int64, limit int) ([]QueuedFactPair, error) {
	rows, err := s.pool.Query(ctx, `
		WITH taken AS (
			SELECT person_id, fact_a_id, fact_b_id
			FROM fact_pair_queue
			WHERE person_id = $1
			ORDER BY enqueued_at, fact_a_id, fact_b_id
			LIMIT $2
		)
		DELETE FROM fact_pair_queue q
		USING taken t
		WHERE q.person_id = t.person_id AND q.fact_a_id = t.fact_a_id AND q.fact_b_id = t.fact_b_id
		RETURNING q.fact_a_id, q.fact_b_id, q.source`, personID, limit)
	if err != nil {
		return nil, fmt.Errorf("consuming queued pairs for person %d: %w", personID, err)
	}
	defer rows.Close()
	var out []QueuedFactPair
	for rows.Next() {
		var q QueuedFactPair
		if err := rows.Scan(&q.FactAID, &q.FactBID, &q.Source); err != nil {
			return nil, fmt.Errorf("scanning queued pair: %w", err)
		}
		out = append(out, q)
	}
	return out, rows.Err()
}

// CountQueuedPairs returns the queue size (for the pipeline log).
func (s *PostgresStorer) CountQueuedPairs(ctx context.Context) (int, error) {
	var n int
	if err := s.pool.QueryRow(ctx, `SELECT count(*) FROM fact_pair_queue`).Scan(&n); err != nil {
		return 0, fmt.Errorf("counting queued pairs: %w", err)
	}
	return n, nil
}

// SetFactSourceMetadataByKey sets metadata[setKey] = byMatch[metadata[matchKey]]
// on every source of the given type whose metadata matchKey is a key of
// byMatch, in one statement (a backfill for keys added after ingestion).
// Returns the rows updated.
func (s *PostgresStorer) SetFactSourceMetadataByKey(ctx context.Context, sourceType, matchKey, setKey string, byMatch map[string]string) (int, error) {
	if len(byMatch) == 0 {
		return 0, nil
	}
	matches := make([]string, 0, len(byMatch))
	values := make([]string, 0, len(byMatch))
	for k, v := range byMatch {
		matches = append(matches, k)
		values = append(values, v)
	}
	res, err := s.pool.Exec(ctx, `
		UPDATE fact_sources fs
		SET metadata = COALESCE(fs.metadata, '{}'::jsonb) || jsonb_build_object($3::text, m.value)
		FROM unnest($4::text[], $5::text[]) AS m(match, value)
		WHERE fs.source_type = $1
		  AND fs.metadata->>$2 = m.match
		  AND fs.metadata->>$3 IS DISTINCT FROM m.value`, sourceType, matchKey, setKey, matches, values)
	if err != nil {
		return 0, fmt.Errorf("backfilling %s on %s sources: %w", setKey, sourceType, err)
	}
	return int(res.RowsAffected()), nil
}
