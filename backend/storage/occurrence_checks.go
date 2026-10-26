package storage

import (
	"context"
	"fmt"

	"digital-mirror/backend/core"
)

// LoadOccurrenceChecks returns the grouping gate's verdicts stored under
// ruleHash for pairs (smaller id first).
func (s *PostgresStorer) LoadOccurrenceChecks(ctx context.Context, pairs [][2]int64, ruleHash string) (map[[2]int64]core.OccurrenceVerdict, error) {
	out := make(map[[2]int64]core.OccurrenceVerdict)
	if len(pairs) == 0 {
		return out, nil
	}
	as := make([]int64, len(pairs))
	bs := make([]int64, len(pairs))
	for i, p := range pairs {
		k := core.OrderedPair(p[0], p[1])
		as[i], bs[i] = k[0], k[1]
	}
	rows, err := s.pool.Query(ctx, `
		SELECT c.fact_a_id, c.fact_b_id, c.verdict
		FROM unnest($1::bigint[], $2::bigint[]) AS p(a, b)
		JOIN occurrence_checks c ON c.fact_a_id = p.a AND c.fact_b_id = p.b
		WHERE c.rule_hash = $3`, as, bs, ruleHash)
	if err != nil {
		return nil, fmt.Errorf("loading occurrence checks: %w", err)
	}
	defer rows.Close()
	for rows.Next() {
		var a, b int64
		var verdict string
		if err := rows.Scan(&a, &b, &verdict); err != nil {
			return nil, fmt.Errorf("scanning occurrence check: %w", err)
		}
		v, err := core.ParseOccurrenceVerdict(verdict)
		if err != nil {
			return nil, err
		}
		out[[2]int64{a, b}] = v
	}
	return out, rows.Err()
}

// RecordOccurrenceCheck stores the gate's verdict on the pair a, b.
func (s *PostgresStorer) RecordOccurrenceCheck(ctx context.Context, a, b int64, ruleHash string, v core.OccurrenceVerdict) error {
	k := core.OrderedPair(a, b)
	if k[0] == k[1] {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO occurrence_checks (fact_a_id, fact_b_id, rule_hash, verdict, checked_at)
		VALUES ($1, $2, $3, $4, NOW())
		ON CONFLICT (fact_a_id, fact_b_id) DO UPDATE
		SET rule_hash = EXCLUDED.rule_hash, verdict = EXCLUDED.verdict, checked_at = EXCLUDED.checked_at`,
		k[0], k[1], ruleHash, v.String())
	if err != nil {
		return fmt.Errorf("recording occurrence check %d-%d: %w", k[0], k[1], err)
	}
	return nil
}

// foldFingerprintSQL is the fingerprint of a person's active facts: their
// count and largest id. A fact that comes, goes or joins a group changes it.
const foldFingerprintSQL = `
	SELECT count(*)::text || ':' || COALESCE(max(f.id), 0)::text
	FROM facts f
	WHERE EXISTS (
	      SELECT 1 FROM fact_sources fs
	      JOIN fact_mentions fm ON fm.fact_source_id = fs.id
	      WHERE fs.fact_id = f.id AND fm.person_id = $1)
	  AND NOT EXISTS (
	      SELECT 1 FROM fact_relations fr
	      WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate')`

// ListPersonsNeedingFold returns the persons with at least two active facts
// whose active facts changed since their last fold under ruleHash, the
// persons with the most recent facts first.
func (s *PostgresStorer) ListPersonsNeedingFold(ctx context.Context, ruleHash string) ([]core.PersonRecord, error) {
	return s.queryPersonRecords(ctx, `
		SELECT p.id, p.name, COALESCE(p.entity_type, 'person')
		FROM persons p
		JOIN (
			SELECT fm.person_id, count(DISTINCT f.id)::text || ':' || max(f.id)::text AS fp, max(f.id) AS last_id
			FROM facts f
			JOIN fact_sources fs ON fs.fact_id = f.id
			JOIN fact_mentions fm ON fm.fact_source_id = fs.id
			WHERE NOT EXISTS (
			      SELECT 1 FROM fact_relations fr
			      WHERE fr.specific_fact_id = f.id AND fr.relation_type = 'duplicate')
			GROUP BY fm.person_id HAVING count(DISTINCT f.id) >= 2
		) a ON a.person_id = p.id
		LEFT JOIN fact_fold_checks c ON c.person_id = p.id
		WHERE c.fingerprint IS DISTINCT FROM (a.fp || ':' || $1)
		ORDER BY a.last_id DESC`, ruleHash)
}

// RecordFold memoizes the fold of the person's current active facts under
// ruleHash: the person is not folded again until they change.
func (s *PostgresStorer) RecordFold(ctx context.Context, personID int64, ruleHash string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO fact_fold_checks (person_id, fingerprint, checked_at)
		SELECT $1, fp || ':' || $2, NOW() FROM (`+foldFingerprintSQL+`) f(fp)
		ON CONFLICT (person_id) DO UPDATE SET fingerprint = EXCLUDED.fingerprint, checked_at = EXCLUDED.checked_at`,
		personID, ruleHash)
	if err != nil {
		return fmt.Errorf("recording the fold of person %d: %w", personID, err)
	}
	return nil
}
