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

// GetStanceLookups returns the memo of the stance lookups of stanceIDs whose
// fingerprint is still the given one: stance id -> the named stance (0:
// none). A stance absent from the map is looked up again.
func (s *PostgresStorer) GetStanceLookups(ctx context.Context, fingerprints map[int64]string) (map[int64]int64, error) {
	if len(fingerprints) == 0 {
		return map[int64]int64{}, nil
	}
	ids := make([]int64, 0, len(fingerprints))
	fps := make([]string, 0, len(fingerprints))
	for id, fp := range fingerprints {
		ids = append(ids, id)
		fps = append(fps, fp)
	}
	rows, err := s.pool.Query(ctx, `
		SELECT c.stance_id, COALESCE(c.named_id, 0)
		FROM stance_lookup_checks c
		JOIN unnest($1::bigint[], $2::text[]) AS w(id, fp) ON w.id = c.stance_id AND w.fp = c.fingerprint`, ids, fps)
	if err != nil {
		return nil, fmt.Errorf("loading stance lookups: %w", err)
	}
	defer rows.Close()
	out := map[int64]int64{}
	for rows.Next() {
		var id, named int64
		if err := rows.Scan(&id, &named); err != nil {
			return nil, fmt.Errorf("loading stance lookups: %w", err)
		}
		out[id] = named
	}
	return out, rows.Err()
}

// RecordStanceLookup memoizes the lookup of stanceID under fingerprint:
// namedID is the stance the LLM named, 0 for none.
func (s *PostgresStorer) RecordStanceLookup(ctx context.Context, stanceID int64, fingerprint string, namedID int64) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO stance_lookup_checks (stance_id, fingerprint, named_id, checked_at)
		VALUES ($1, $2, NULLIF($3, 0), NOW())
		ON CONFLICT (stance_id) DO UPDATE SET fingerprint = EXCLUDED.fingerprint, named_id = EXCLUDED.named_id, checked_at = EXCLUDED.checked_at`,
		stanceID, fingerprint, namedID)
	if err != nil {
		return fmt.Errorf("recording the lookup of stance %d: %w", stanceID, err)
	}
	return nil
}
