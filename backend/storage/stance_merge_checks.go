package storage

import (
	"context"
	"fmt"

	"digital-mirror/backend/core"
)

// stanceMergeFingerprintSQL computes, for the person bound as $1, the md5 of
// its stance set as (id, label, summary) in id order. It is the live half of
// the merge-sweep memo: any change to a stance (a refreshed summary, a new
// stance, a merge) changes it.
const stanceMergeFingerprintSQL = `
	SELECT md5(string_agg(id::text || E'\x1f' || cluster_label || E'\x1f' || COALESCE(summary, ''), E'\x1e' ORDER BY id))
	FROM stance_documents WHERE person_id = $1`

// ListPersonsNeedingMergeSweep returns the persons with at least two stances
// whose stance set changed since the merge sweep last found nothing to merge,
// or that were never swept under rulebookKey. The comparison runs in SQL so
// the thousands of unchanged persons cost one query, not one LLM round each.
func (s *PostgresStorer) ListPersonsNeedingMergeSweep(ctx context.Context, rulebookKey string) ([]core.PersonRecord, error) {
	return s.queryPersonRecords(ctx, `
		SELECT p.id, p.name, COALESCE(p.entity_type, 'person')
		FROM persons p
		JOIN (
			SELECT person_id,
			       md5(string_agg(id::text || E'\x1f' || cluster_label || E'\x1f' || COALESCE(summary, ''), E'\x1e' ORDER BY id)) AS fp
			FROM stance_documents
			GROUP BY person_id HAVING COUNT(*) >= 2
		) s ON s.person_id = p.id
		LEFT JOIN stance_merge_checks c ON c.person_id = p.id
		WHERE c.fingerprint IS DISTINCT FROM (s.fp || ':' || $1)
		ORDER BY p.name`, rulebookKey)
}

// RecordMergeSweep memoizes that the merge sweep has judged the person's
// CURRENT stance set under rulebookKey, so the next sweep skips the person
// until a stance or the rulebook changes. Called after the sweep, so the
// fingerprint stored is the post-merge one.
func (s *PostgresStorer) RecordMergeSweep(ctx context.Context, personID int64, rulebookKey string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO stance_merge_checks (person_id, fingerprint, checked_at)
		SELECT $1, fp || ':' || $2, NOW()
		FROM (`+stanceMergeFingerprintSQL+`) f(fp)
		WHERE fp IS NOT NULL
		ON CONFLICT (person_id) DO UPDATE SET fingerprint = EXCLUDED.fingerprint, checked_at = EXCLUDED.checked_at`,
		personID, rulebookKey)
	if err != nil {
		return fmt.Errorf("recording merge sweep for person %d: %w", personID, err)
	}
	return nil
}
